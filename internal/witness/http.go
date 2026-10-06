package witness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// The forward stream carries wegweiser's API between members
// (wegweiser's docs/decisions/d47-status-asks-every-member.md). A witness has
// no API of its own. It answers the one question every member is asked over
// that stream, how far it has got, and refuses everything else.

// clusterPath is the route a member asks another one's status on.
const clusterPath = "/api/v1/cluster"

// clusterStatus is the answer, in the shape of wegweiser's ClusterStatus.
type clusterStatus struct {
	Self        clusterMember `json:"self"`
	Replicating bool          `json:"replicating"`
	Removed     bool          `json:"removed"`
	Applied     uint64        `json:"applied"`
	Committed   uint64        `json:"committed"`
	Members     []memberState `json:"members"`
}

type clusterMember struct {
	ID      string `json:"id"`
	Address string `json:"address"`
}

type memberState struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Role    string `json:"role"`
	Leader  bool   `json:"leader"`
}

// problem is an RFC 9457 answer, in the shape wegweiser's API gives one.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

// httpServer serves the forward stream.
type httpServer struct {
	srv *http.Server
}

func newHTTPServer(n *Node) *httpServer {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+clusterPath, func(w http.ResponseWriter, _ *http.Request) {
		st := n.Status()
		id, addr := n.Member()
		out := clusterStatus{
			Self:        clusterMember{ID: id, Address: addr},
			Replicating: n.Replicating(),
			Removed:     st.Removed,
			Applied:     st.Applied,
			Committed:   st.Committed,
			Members:     make([]memberState, 0, len(st.Members)),
		}
		for _, m := range st.Members {
			out.Members = append(out.Members, memberState(m))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out) //nolint:errcheck // the caller hung up; nothing to tell it
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(problem{ //nolint:errcheck // as above
			Type:   "/problems/unavailable",
			Title:  "This member is a witness",
			Status: http.StatusServiceUnavailable,
			Detail: "a witness keeps the cluster's log and answers no requests; send this to a member " +
				"that holds data",
		})
	})
	return &httpServer{srv: &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}}
}

func (n *Node) serveForwards() {
	defer n.wg.Done()
	if err := n.http.srv.Serve(n.forwards); err != nil && !errors.Is(err, http.ErrServerClosed) {
		n.log.Warn("the forward stream stopped", "error", err)
	}
}

func (h *httpServer) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.srv.Shutdown(ctx)
}
