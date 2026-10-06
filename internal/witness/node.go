// Package witness is a Raft voter that keeps the log and applies none of it
// (wegweiser's docs/decisions/d39-the-witness.md).
package witness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"

	"github.com/wegweiserzone/wegwitness/internal/transport"
)

// A witness's settings. Raft's timeouts are its defaults, as they are on
// every other member.
const (
	snapshotsKept = 2
	streamPool    = 3
	streamTimeout = 10 * time.Second

	// trailingLogs is how much log a witness keeps behind its last snapshot:
	// a hundred times Raft's default. A member brought up to date from the
	// log is fine; one sent this witness's empty snapshot would refuse it and
	// stop, and keeping the log is all a witness has to spend its disk on.
	trailingLogs = 1_024_000

	// handOverRetry is the pause between rounds of trying to hand leadership
	// on while no member with data could take it.
	handOverRetry = time.Second
)

// Config is what a [Node] needs.
type Config struct {
	// ID is the identifier this witness is a member by; it begins with
	// [Prefix].
	ID string
	// Advertise is where the other members reach this one, as host:port.
	Advertise string
	// Dir is where Raft keeps its log and its snapshots.
	Dir string

	Transport *transport.Transport
	Mux       *transport.Mux

	// Logger is where Raft's lines and this witness's own go. Nil discards
	// them.
	Logger *slog.Logger
}

// Node is a witness taking part in its cluster.
type Node struct {
	raft    *raft.Raft
	logs    *raftboltdb.BoltStore
	trans   *raft.NetworkTransport
	tr      *transport.Transport
	machine *fsm
	log     *slog.Logger
	id      raft.ServerID
	addr    raft.ServerAddress

	joins    net.Listener
	forwards net.Listener
	http     *httpServer

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Start brings the witness up. One that has never been part of a cluster
// holds no Raft state, and waits to be added by a member that is.
func Start(cfg Config) (_ *Node, err error) {
	switch {
	case !IsWitness(cfg.ID):
		return nil, fmt.Errorf("witness: the identifier %q does not begin %q", cfg.ID, Prefix)
	case cfg.Advertise == "":
		return nil, errors.New("witness: a witness needs an address the others can reach it at")
	case cfg.Dir == "":
		return nil, errors.New("witness: a witness needs a directory for Raft's log")
	case cfg.Transport == nil, cfg.Mux == nil:
		return nil, errors.New("witness: a witness needs a transport and the port it serves")
	}
	advertise, err := net.ResolveTCPAddr("tcp", cfg.Advertise)
	if err != nil {
		return nil, fmt.Errorf("witness: the address to advertise: %w", err)
	}
	if merr := os.MkdirAll(cfg.Dir, 0o700); merr != nil {
		return nil, fmt.Errorf("witness: the directory for Raft's log: %w", merr)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	rlog := newRaftLogger(logger)

	logs, err := raftboltdb.NewBoltStore(filepath.Join(cfg.Dir, "raft.db"))
	if err != nil {
		return nil, fmt.Errorf("witness: open Raft's log: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, logs.Close())
		}
	}()
	snaps, err := raft.NewFileSnapshotStoreWithLogger(cfg.Dir, snapshotsKept, rlog)
	if err != nil {
		return nil, fmt.Errorf("witness: open the log snapshots: %w", err)
	}

	trans := raft.NewNetworkTransportWithConfig(&raft.NetworkTransportConfig{
		Stream:  &raftStream{l: cfg.Mux.Listener(transport.StreamRaft), t: cfg.Transport, addr: advertise},
		MaxPool: streamPool,
		Timeout: streamTimeout,
		Logger:  rlog,
	})
	defer func() {
		if err != nil {
			err = errors.Join(err, trans.Close())
		}
	}()

	conf := raft.DefaultConfig()
	conf.LocalID = raft.ServerID(cfg.ID)
	conf.Logger = rlog
	conf.TrailingLogs = trailingLogs

	machine := &fsm{}
	r, err := raft.NewRaft(conf, machine, logs, logs, snaps, trans)
	if err != nil {
		return nil, fmt.Errorf("witness: start Raft: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	n := &Node{
		raft: r, logs: logs, trans: trans, tr: cfg.Transport, machine: machine, log: logger,
		id: conf.LocalID, addr: raft.ServerAddress(cfg.Advertise),
		joins:    cfg.Mux.Listener(transport.StreamJoin),
		forwards: cfg.Mux.Listener(transport.StreamForward),
		ctx:      ctx, cancel: cancel,
	}
	n.http = newHTTPServer(n)
	n.wg.Add(3)
	go n.handOver()
	go n.serveJoins()
	go n.serveForwards()
	return n, nil
}

// Member is the identifier this witness is a member by, and the address the
// others reach it at.
func (n *Node) Member() (id, addr string) { return string(n.id), string(n.addr) }

// Replicating reports whether this witness holds Raft state: it has been added
// to a cluster.
func (n *Node) Replicating() bool { return n.raft.LastIndex() > 0 }

// IsLeader reports whether this witness is leading, which it does only for as
// long as it takes to hand leadership on.
func (n *Node) IsLeader() bool { return n.raft.State() == raft.Leader }

// Leader names the member leading, as far as this one knows.
func (n *Node) Leader() (id, addr string) {
	a, i := n.raft.LeaderWithID()
	return string(i), string(a)
}

// handOver gives leadership away whenever this witness wins it. A witness
// must not lead: a leader plans writes against the state it holds, and this
// one holds none. Raft brings the member it hands over to up to date first,
// which is how a witness repairs the very case it was elected in
// (wegweiser's docs/decisions/d39-the-witness.md).
func (n *Node) handOver() {
	defer n.wg.Done()
	leading := n.raft.LeaderCh()
	for {
		select {
		case <-n.ctx.Done():
			return
		case won := <-leading:
			for won && n.IsLeader() && n.ctx.Err() == nil {
				if n.handOverOnce() {
					break
				}
				select {
				case <-n.ctx.Done():
					return
				case won = <-leading:
				case <-time.After(handOverRetry):
				}
			}
		}
	}
}

// handOverOnce tries each voter that holds data in turn, and reports whether
// one took leadership.
func (n *Node) handOverOnce() bool {
	for _, srv := range n.raft.GetConfiguration().Configuration().Servers {
		if srv.Suffrage != raft.Voter || srv.ID == n.id || IsWitness(string(srv.ID)) {
			continue
		}
		err := n.raft.LeadershipTransferToServer(srv.ID, srv.Address).Error()
		if err == nil {
			n.log.Info("handed leadership on", "to", srv.ID)
			return true
		}
		n.log.Warn("could not hand leadership on", "to", srv.ID, "error", err)
	}
	return false
}

// Status is what this witness knows of its cluster and of itself.
type Status struct {
	Members   []MemberState
	Applied   uint64
	Committed uint64
	Removed   bool
}

// MemberState is one member, as this witness's copy of the configuration
// lists it.
type MemberState struct {
	ID      string
	Address string
	Role    string
	Leader  bool
}

// Status reports what this witness knows.
func (n *Node) Status() Status {
	st := Status{Applied: n.raft.AppliedIndex(), Committed: n.raft.CommitIndex()}
	leader, _ := n.Leader()
	listed := false
	for _, srv := range n.raft.GetConfiguration().Configuration().Servers {
		role := "nonvoter"
		switch {
		case srv.Suffrage != raft.Voter:
		case IsWitness(string(srv.ID)):
			role = "witness"
		default:
			role = "voter"
		}
		listed = listed || srv.ID == n.id
		st.Members = append(st.Members, MemberState{
			ID: string(srv.ID), Address: string(srv.Address), Role: role, Leader: string(srv.ID) == leader,
		})
	}
	st.Removed = n.Replicating() && !listed
	return st
}

// Close stops taking part in the cluster.
func (n *Node) Close() error {
	n.cancel()
	err := errors.Join(n.raft.Shutdown().Error(), n.joins.Close(), n.http.close())
	n.wg.Wait()
	return errors.Join(err, n.trans.Close(), n.logs.Close())
}

// raftStream is the cluster port's Raft streams, as the stream layer Raft's
// network transport runs on.
type raftStream struct {
	l    net.Listener
	t    *transport.Transport
	addr net.Addr
}

var _ raft.StreamLayer = (*raftStream)(nil)

func (s *raftStream) Accept() (net.Conn, error) { return s.l.Accept() }
func (s *raftStream) Close() error              { return s.l.Close() }

// Addr is the address this witness advertises, which is the one the others
// can reach rather than the one the port is bound to.
func (s *raftStream) Addr() net.Addr { return s.addr }

func (s *raftStream) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.t.Dial(ctx, string(address), transport.StreamRaft)
}
