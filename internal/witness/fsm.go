package witness

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/hashicorp/raft"
)

// The log snapshot format, as wegweiser's internal/apply/snapshot.go defines
// it (wegweiser's docs/decisions/d30-what-a-log-snapshot-contains.md). A
// witness writes the header and the end mark with nothing between them, and
// says in the header that it is a witness, so that a member with a store
// refuses to restore it rather than empty itself.
const (
	snapshotFormat  = "wegweiser-log-snapshot"
	snapshotVersion = 1
	snapshotEnd     = "end"
	writerWitness   = "witness"
)

type snapshotHeader struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Index   uint64 `json:"index"`
	Writer  string `json:"writer"`
}

type snapshotItem struct {
	Kind  string `json:"kind"`
	Count uint64 `json:"count,omitempty"`
}

// fsm is a witness's state machine. It is handed every committed entry and
// keeps nothing of it but how far it has got
// (wegweiser's docs/decisions/d39-the-witness.md).
type fsm struct {
	applied atomic.Uint64
}

var (
	_ raft.FSM                = (*fsm)(nil)
	_ raft.ConfigurationStore = (*fsm)(nil)
)

func (f *fsm) Apply(entry *raft.Log) any {
	f.applied.Store(entry.Index)
	return nil
}

// StoreConfiguration moves past a configuration entry the way Apply moves
// past a batch, so that a snapshot names the last entry this member has been
// through, whatever kind.
func (f *fsm) StoreConfiguration(index uint64, _ raft.Configuration) {
	f.applied.Store(index)
}

func (f *fsm) Snapshot() (raft.FSMSnapshot, error) {
	return emptySnapshot{index: f.applied.Load()}, nil
}

// Restore takes a log snapshot sent by the leader. There is nothing to put
// back, so only the position is read and the rest is let go.
func (f *fsm) Restore(rc io.ReadCloser) (err error) {
	defer func() { err = errors.Join(err, rc.Close()) }()
	var h snapshotHeader
	if derr := json.NewDecoder(rc).Decode(&h); derr != nil {
		return fmt.Errorf("witness: the log snapshot has no header that can be read: %w", derr)
	}
	if h.Format != snapshotFormat {
		return errors.New("witness: what was sent as a log snapshot is not one")
	}
	f.applied.Store(h.Index)
	_, err = io.Copy(io.Discard, rc)
	return err
}

// emptySnapshot is what a witness has to hand over: a position and nothing
// at it.
type emptySnapshot struct{ index uint64 }

func (s emptySnapshot) Persist(sink raft.SnapshotSink) error {
	enc := json.NewEncoder(sink)
	err := enc.Encode(snapshotHeader{
		Format: snapshotFormat, Version: snapshotVersion, Index: s.index, Writer: writerWitness,
	})
	if err == nil {
		err = enc.Encode(snapshotItem{Kind: snapshotEnd})
	}
	if err != nil {
		return errors.Join(fmt.Errorf("witness: write the log snapshot: %w", err), sink.Cancel())
	}
	return sink.Close()
}

func (emptySnapshot) Release() {}
