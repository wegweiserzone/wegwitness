package witness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/wegweiserzone/wegwitness/internal/transport"
)

// Probed is what a running witness says about itself when it is asked.
type Probed struct {
	ID          string
	Replicating bool
	Removed     bool
	Applied     uint64
	Committed   uint64
	// Leader is the member leading, as far as the witness knows; empty while
	// none is.
	Leader string
}

// TakesPart reports whether the witness is a member of a cluster and has not
// been taken out of it.
func (p Probed) TakesPart() bool { return p.Replicating && !p.Removed }

// Probe asks the witness at addr how it stands, over its own cluster port and
// with the cluster's secret: the question every member is asked over the
// forward stream (wegweiser's docs/decisions/d47-status-asks-every-member.md).
// A witness has no API of its own to ask instead.
func Probe(ctx context.Context, t *transport.Transport, addr string) (Probed, error) {
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return t.Dial(ctx, addr, transport.StreamForward)
		},
	}}
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+clusterPath, http.NoBody)
	if err != nil {
		return Probed{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		// That the question travels as HTTP is this package's business, and
		// the request line is noise in a message about an unreachable port.
		if ue := (*url.Error)(nil); errors.As(err, &ue) {
			err = ue.Err
		}
		return Probed{}, fmt.Errorf("witness: ask the witness at %s: %w", addr, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Probed{}, fmt.Errorf("witness: the witness at %s answered %s", addr, resp.Status)
	}

	var st clusterStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return Probed{}, fmt.Errorf("witness: read the witness's answer: %w", err)
	}
	p := Probed{
		ID: st.Self.ID, Replicating: st.Replicating, Removed: st.Removed,
		Applied: st.Applied, Committed: st.Committed,
	}
	for _, m := range st.Members {
		if m.Leader {
			p.Leader = m.ID
		}
	}
	return p, nil
}
