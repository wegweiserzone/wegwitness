package witness

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/raft"

	"github.com/wegweiserzone/wegwitness/internal/transport"
)

// patience is how long a test waits for Raft to settle. An election takes a
// second or two on Raft's defaults, which are the ones a witness runs.
const patience = 20 * time.Second

var secret = bytes.Repeat([]byte{7}, transport.MinSecret)

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("still waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// port opens a cluster port on loopback with the shared secret.
func port(t *testing.T) (*transport.Transport, *transport.Mux) {
	t.Helper()
	tr, err := transport.New(transport.Config{Secret: secret})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := tr.Serve(l)
	t.Cleanup(func() {
		if err := mux.Close(); err != nil {
			t.Errorf("close the port: %v", err)
		}
	})
	return tr, mux
}

func startWitness(t *testing.T, id string) *Node {
	t.Helper()
	tr, mux := port(t)
	n, err := Start(Config{ID: id, Advertise: mux.Addr().String(), Dir: t.TempDir(), Transport: tr, Mux: mux})
	if err != nil {
		t.Fatalf("Start %s: %v", id, err)
	}
	t.Cleanup(func() {
		if err := n.Close(); err != nil {
			t.Errorf("close %s: %v", id, err)
		}
	})
	return n
}

// member stands in for a member that holds data: a Raft node with a state
// machine that keeps nothing, on the same transport a member uses.
type member struct {
	id   string
	addr string
	raft *raft.Raft
	tr   *transport.Transport
	mux  *transport.Mux
}

type nopFSM struct{}

func (nopFSM) Apply(*raft.Log) any                 { return nil }
func (nopFSM) Snapshot() (raft.FSMSnapshot, error) { return nil, io.EOF }
func (nopFSM) Restore(rc io.ReadCloser) error      { return rc.Close() }

func startMember(t *testing.T, id string) *member {
	t.Helper()
	tr, mux := port(t)
	addr, err := net.ResolveTCPAddr("tcp", mux.Addr().String())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	trans := raft.NewNetworkTransportWithConfig(&raft.NetworkTransportConfig{
		Stream:  &raftStream{l: mux.Listener(transport.StreamRaft), t: tr, addr: addr},
		MaxPool: 3, Timeout: 10 * time.Second,
	})
	conf := raft.DefaultConfig()
	conf.LocalID = raft.ServerID(id)
	conf.LogOutput = io.Discard
	store := raft.NewInmemStore()
	r, err := raft.NewRaft(conf, nopFSM{}, store, store, raft.NewInmemSnapshotStore(), trans)
	if err != nil {
		t.Fatalf("start %s: %v", id, err)
	}
	t.Cleanup(func() {
		if err := r.Shutdown().Error(); err != nil {
			t.Errorf("stop %s: %v", id, err)
		}
	})
	return &member{id: id, addr: mux.Addr().String(), raft: r, tr: tr, mux: mux}
}

func leaderOf(t *testing.T, m *member) string {
	t.Helper()
	var id string
	waitFor(t, "a leader", func() bool {
		_, i := m.raft.LeaderWithID()
		id = string(i)
		return id != ""
	})
	return id
}

func TestTheSnapshotSaysAWitnessWroteIt(t *testing.T) {
	t.Parallel()
	f := &fsm{}
	f.Apply(&raft.Log{Index: 7})
	snap, err := f.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var sink fakeSink
	if err := snap.Persist(&sink); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	raw := bytes.Clone(sink.Bytes())
	dec := json.NewDecoder(&sink.Buffer)
	var h snapshotHeader
	var end snapshotItem
	if err := dec.Decode(&h); err != nil {
		t.Fatalf("read the header: %v", err)
	}
	if err := dec.Decode(&end); err != nil {
		t.Fatalf("read the end mark: %v", err)
	}
	want := snapshotHeader{Format: "wegweiser-log-snapshot", Version: 1, Index: 7, Writer: "witness"}
	if h != want || end.Kind != "end" || end.Count != 0 {
		t.Errorf("snapshot = %+v then %+v, want %+v and an empty end mark", h, end, want)
	}
	if dec.More() {
		t.Error("the snapshot holds something after its end mark")
	}

	g := &fsm{}
	if err := g.Restore(io.NopCloser(bytes.NewReader(raw))); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if g.applied.Load() != 7 {
		t.Errorf("restored at %d, want 7", g.applied.Load())
	}
}

type fakeSink struct{ bytes.Buffer }

func (*fakeSink) ID() string    { return "test" }
func (*fakeSink) Cancel() error { return nil }
func (*fakeSink) Close() error  { return nil }

func TestAWitnessKeepsTheIdentifierItWasGiven(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	minted, err := Identity(dir, "")
	if err != nil || !IsWitness(minted) {
		t.Fatalf("minted %q, %v; want an identifier beginning %q", minted, err, Prefix)
	}
	if again, err := Identity(dir, ""); err != nil || again != minted {
		t.Errorf("asked again: %q, %v; want %q", again, err, minted)
	}
	if _, err := Identity(dir, Prefix+"other"); err == nil {
		t.Error("a file naming another identifier was obeyed")
	}
	if _, err := Identity(t.TempDir(), "ns3"); err == nil {
		t.Error("an identifier without the witness prefix was taken")
	}
}

// D39: a witness that wins an election hands leadership to a member that
// holds data.
func TestAWitnessHandsLeadershipOn(t *testing.T) {
	t.Parallel()
	a, b := startMember(t, "a"), startMember(t, "b")
	w := startWitness(t, Prefix+"w")
	_, waddr := w.Member()

	err := a.raft.BootstrapCluster(raft.Configuration{Servers: []raft.Server{
		{ID: "a", Address: raft.ServerAddress(a.addr)},
		{ID: "b", Address: raft.ServerAddress(b.addr)},
		{ID: raft.ServerID(Prefix + "w"), Address: raft.ServerAddress(waddr)},
	}}).Error()
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	leaderOf(t, a)
	waitFor(t, "a to lead", func() bool {
		if a.raft.State() == raft.Leader {
			return true
		}
		// Whoever won the first election hands over; a refusal here is
		// another round of the wait, not a failure.
		if err := a.raft.LeadershipTransferToServer("a", raft.ServerAddress(a.addr)).Error(); err != nil {
			t.Logf("hand leadership to a: %v", err)
		}
		return false
	})

	if err := a.raft.LeadershipTransferToServer(raft.ServerID(Prefix+"w"), raft.ServerAddress(waddr)).Error(); err != nil {
		t.Fatalf("hand leadership to the witness: %v", err)
	}
	waitFor(t, "the witness to hand leadership on", func() bool {
		id := leaderOf(t, a)
		return id == "a" || id == "b"
	})
	if w.IsLeader() {
		t.Error("the witness still leads")
	}
}

// A witness joins in the two steps a voter does: without a vote, then, once
// the log has reached it, with one.
func TestAWitnessJoinsInTwoSteps(t *testing.T) {
	t.Parallel()
	a := startMember(t, "a")
	if err := a.raft.BootstrapCluster(raft.Configuration{Servers: []raft.Server{
		{ID: "a", Address: raft.ServerAddress(a.addr)},
	}}).Error(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	waitFor(t, "a to lead", func() bool { return a.raft.State() == raft.Leader })

	requests := make(chan joinRequest, 4)
	go serveJoinsLikeAMember(t, a, requests)

	w := startWitness(t, Prefix+"w")
	if err := w.Join(t.Context(), a.addr); err != nil {
		t.Fatalf("Join: %v", err)
	}
	close(requests)
	var asked []joinRequest
	for req := range requests {
		asked = append(asked, req)
	}
	if len(asked) != 2 || asked[0].HoldsLog || !asked[1].HoldsLog || asked[0].Role != "witness" {
		t.Errorf("asked %+v, want a witness asking once without the log and once with it", asked)
	}
	for _, srv := range a.raft.GetConfiguration().Configuration().Servers {
		if string(srv.ID) == Prefix+"w" && srv.Suffrage != raft.Voter {
			t.Errorf("the witness is %v, want a voter", srv.Suffrage)
		}
	}
}

// serveJoinsLikeAMember answers the join stream the way a leading member
// does, as far as a witness can tell.
func serveJoinsLikeAMember(t *testing.T, m *member, asked chan<- joinRequest) {
	l := m.mux.Listener(transport.StreamJoin)
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		var req joinRequest
		if derr := json.NewDecoder(conn).Decode(&req); derr != nil {
			t.Errorf("read a join: %v", derr)
			return
		}
		asked <- req
		reply := joinReply{Done: true}
		if req.HoldsLog {
			err = m.raft.AddVoter(raft.ServerID(req.ID), raft.ServerAddress(req.Address), 0, 0).Error()
		} else {
			err = m.raft.AddNonvoter(raft.ServerID(req.ID), raft.ServerAddress(req.Address), 0, 0).Error()
			reply = joinReply{Staged: true}
		}
		if err != nil {
			reply = joinReply{Error: err.Error()}
		}
		if eerr := json.NewEncoder(conn).Encode(reply); eerr != nil {
			t.Errorf("answer a join: %v", eerr)
		}
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("close a join stream: %v", cerr)
		}
	}
}

// D47: a member asks every other one how far it has got over the forward
// stream. A witness answers that, and refuses everything else.
func TestAWitnessAnswersForItselfOnTheForwardStream(t *testing.T) {
	t.Parallel()
	w := startWitness(t, Prefix+"w")
	_, waddr := w.Member()
	tr, _ := port(t)
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return tr.Dial(ctx, addr, transport.StreamForward)
		},
	}}

	resp, err := client.Get("http://" + waddr + clusterPath)
	if err != nil {
		t.Fatalf("ask the witness: %v", err)
	}
	var st clusterStatus
	err = json.NewDecoder(resp.Body).Decode(&st)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || st.Self.ID != Prefix+"w" || st.Replicating {
		t.Errorf("status %d, %+v (%v); want this witness, in no cluster yet", resp.StatusCode, st, err)
	}

	resp, err = client.Post("http://"+waddr+"/api/v1/zones", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("send the witness a write: %v", err)
	}
	var p problem
	err = json.NewDecoder(resp.Body).Decode(&p)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusServiceUnavailable || p.Status != http.StatusServiceUnavailable {
		t.Errorf("a write: %d, %+v (%v); want it refused with 503", resp.StatusCode, p, err)
	}
}
