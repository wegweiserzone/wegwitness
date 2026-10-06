package witness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/wegweiserzone/wegwitness/internal/transport"
)

// joinRequest and joinReply are the join stream's two messages, as
// wegweiser's internal/cluster/join.go defines them
// (wegweiser's docs/decisions/d44-starting-and-joining.md).
type joinRequest struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	Role     string `json:"role"`
	HoldsLog bool   `json:"holdsLog,omitempty"`
}

type joinReply struct {
	Done   bool   `json:"done,omitempty"`
	Staged bool   `json:"staged,omitempty"`
	Leader string `json:"leader,omitempty"`
	Error  string `json:"error,omitempty"`
}

// roleWitness is what a witness joins as.
const roleWitness = "witness"

// joinHops bounds how often a witness follows "ask the leader", for the same
// reason a member does: leadership can move while it asks.
const joinHops = 5

// joinTimeout bounds one attempt. The leader answers once the addition is
// committed.
const joinTimeout = 15 * time.Second

// logWait bounds how long a witness that has been added waits for the log to
// reach it.
const logWait = time.Minute

// Join asks the member at addr to make this witness a member, follows it to
// the leader, and returns once this witness votes and holds the log. Like a
// voter it is added without a vote first, and asks for one once the log has
// reached it.
func (n *Node) Join(ctx context.Context, addr string) error {
	req := joinRequest{ID: string(n.id), Address: string(n.addr), Role: roleWitness}
	target := addr
	for range joinHops {
		reply, err := askToJoin(ctx, n.tr, target, req)
		if err != nil {
			return err
		}
		switch {
		case reply.Error != "":
			return fmt.Errorf("witness: %s would not add this witness: %s", target, reply.Error)
		case reply.Leader != "":
			target = reply.Leader
		case reply.Staged || reply.Done:
			if werr := n.awaitLog(ctx); werr != nil {
				return werr
			}
			if reply.Done {
				return nil
			}
			req.HoldsLog = true
		default:
			return fmt.Errorf("witness: %s knows of no leader", target)
		}
	}
	return fmt.Errorf("witness: sent on %d times without reaching a member that leads; "+
		"the members disagree about who does", joinHops)
}

// awaitLog returns once this witness has been through everything it knows to
// be committed.
func (n *Node) awaitLog(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, logWait)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		committed := n.raft.CommitIndex()
		if committed > 0 && n.raft.AppliedIndex() >= committed {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("witness: this witness was added to the cluster and the log has not reached it "+
				"in %s; check that the members can reach it at %s: %w", logWait, n.addr, ctx.Err())
		case <-tick.C:
		}
	}
}

func askToJoin(ctx context.Context, t *transport.Transport, addr string, req joinRequest) (_ joinReply, err error) {
	ctx, cancel := context.WithTimeout(ctx, joinTimeout)
	defer cancel()

	conn, err := t.Dial(ctx, addr, transport.StreamJoin)
	if err != nil {
		return joinReply{}, err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	if deadline, ok := ctx.Deadline(); ok {
		if derr := conn.SetDeadline(deadline); derr != nil {
			return joinReply{}, derr
		}
	}
	if eerr := json.NewEncoder(conn).Encode(req); eerr != nil {
		return joinReply{}, fmt.Errorf("witness: ask %s to join: %w", addr, eerr)
	}
	var reply joinReply
	if derr := json.NewDecoder(conn).Decode(&reply); derr != nil {
		return joinReply{}, fmt.Errorf("witness: read %s's answer to the join: %w", addr, derr)
	}
	return reply, nil
}

// serveJoins answers nodes that ask this witness to make them members. A
// witness adds nobody: it names the member that leads, which is the answer a
// member that does not lead gives too.
func (n *Node) serveJoins() {
	defer n.wg.Done()
	for {
		conn, err := n.joins.Accept()
		if err != nil {
			return
		}
		n.wg.Add(1)
		go func() {
			defer n.wg.Done()
			n.answerJoin(conn)
		}()
	}
}

func (n *Node) answerJoin(conn net.Conn) {
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(joinTimeout)); err != nil {
		return
	}
	var req joinRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	reply := joinReply{Error: "a witness adds no members, and knows of no member that leads; ask a member"}
	if id, addr := n.Leader(); addr != "" && id != string(n.id) {
		reply = joinReply{Leader: addr}
	}
	if err := json.NewEncoder(conn).Encode(reply); err != nil {
		n.log.Warn("answer a join", "from", conn.RemoteAddr(), "error", err)
	}
}
