// Package interop runs a witness against real Wegweiser servers. It needs a
// weg binary, named by WEG_BIN, and is skipped without one: `make interop`
// builds nothing of wegweiser's, it runs the one it is pointed at.
package interop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wegweiserzone/wegwitness/internal/transport"
	"github.com/wegweiserzone/wegwitness/internal/witness"
)

const patience = 30 * time.Second

var secret = bytes.Repeat([]byte{9}, transport.MinSecret)

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("still waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

// server is one `weg serve`, which can be stopped and started again on the
// same configuration and database.
type server struct {
	t       *testing.T
	bin     string
	config  string
	db      string
	cluster string

	cmd    *exec.Cmd
	stderr *syncBuffer
	api    string
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newServer(t *testing.T, bin string) *server {
	t.Helper()
	dir := t.TempDir()
	s := &server{t: t, bin: bin, db: filepath.Join(dir, "weg.db"), cluster: freeAddr(t)}
	s.config = filepath.Join(dir, "config.yaml")
	body := fmt.Sprintf("cluster:\n  listen: %q\n  advertise: %q\n  secret: %q\n",
		s.cluster, s.cluster, base64.StdEncoding.EncodeToString(secret))
	if err := os.WriteFile(s.config, []byte(body), 0o600); err != nil {
		t.Fatalf("write the configuration: %v", err)
	}
	t.Cleanup(s.stop)
	return s
}

// start runs the server and waits until it answers.
func (s *server) start(args ...string) {
	s.t.Helper()
	s.stderr = &syncBuffer{}
	s.cmd = exec.Command(s.bin, append([]string{
		"serve", "--config", s.config, "--db", s.db,
		"--listen", "127.0.0.1:0", "--api-listen", "127.0.0.1:0", "--output", "json",
	}, args...)...)
	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		s.t.Fatalf("stdout: %v", err)
	}
	s.cmd.Stderr = s.stderr
	if err := s.cmd.Start(); err != nil {
		s.t.Fatalf("start weg: %v", err)
	}
	var status struct {
		APIAddress string `json:"apiAddress"`
	}
	if err := json.NewDecoder(bufio.NewReader(stdout)).Decode(&status); err != nil {
		s.t.Fatalf("read what weg serve reported: %v\n%s", err, s.stderr.String())
	}
	s.api = "http://" + status.APIAddress
	// Nothing else is read from standard output, and the process ending is
	// what ends this.
	go io.Copy(io.Discard, stdout) //nolint:errcheck // see above
}

func (s *server) stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	if err := s.cmd.Process.Signal(os.Interrupt); err != nil {
		s.t.Logf("stop weg: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		if err := s.cmd.Process.Kill(); err != nil {
			s.t.Logf("kill weg: %v", err)
		}
		<-done
	}
	s.cmd = nil
}

// token is the bootstrap token the first server printed.
func (s *server) token() string {
	s.t.Helper()
	var tok string
	waitFor(s.t, "the bootstrap token", func() bool {
		out := s.stderr.String()
		i := strings.Index(out, "weg_")
		if i < 0 {
			return false
		}
		tok = strings.Fields(out[i:])[0]
		return true
	})
	return tok
}

func (s *server) do(method, path, token string, body any) (code int, answer []byte) {
	s.t.Helper()
	var in io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			s.t.Fatalf("encode: %v", err)
		}
		in = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, s.api+"/api/v1"+path, in)
	if err != nil {
		s.t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatalf("read the answer to %s %s: %v", method, path, err)
	}
	return resp.StatusCode, out
}

type clusterStatus struct {
	Members []struct {
		ID       string          `json:"id"`
		Role     string          `json:"role"`
		Leader   bool            `json:"leader"`
		Progress json.RawMessage `json:"progress"`
	} `json:"members"`
}

func (s *server) status(token string) clusterStatus {
	s.t.Helper()
	code, body := s.do(http.MethodGet, "/cluster", token, nil)
	var st clusterStatus
	if code != http.StatusOK || json.Unmarshal(body, &st) != nil {
		s.t.Fatalf("GET /cluster: %d %s", code, body)
	}
	return st
}

func (s *server) zones(token string) string {
	s.t.Helper()
	code, body := s.do(http.MethodGet, "/zones", token, nil)
	if code != http.StatusOK {
		s.t.Fatalf("GET /zones: %d %s", code, body)
	}
	return string(body)
}

func startWitness(t *testing.T, id string) *witness.Node {
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
	n, err := witness.Start(witness.Config{
		ID: id, Advertise: mux.Addr().String(), Dir: t.TempDir(), Transport: tr, Mux: mux,
		Logger: slog.New(slog.NewTextHandler(testLog{t}, nil)),
	})
	if err != nil {
		t.Fatalf("start the witness: %v", err)
	}
	t.Cleanup(func() {
		if err := errors.Join(n.Close(), mux.Close()); err != nil {
			t.Errorf("stop the witness: %v", err)
		}
	})
	return n
}

// testLog writes what the witness reports into the test's own output.
type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// D39's case: two servers and a witness. A server that was down while a
// change was committed, and whose partner went down before it caught up, is
// brought current from the witness's log and then leads.
func TestTwoServersAndAWitness(t *testing.T) {
	bin := os.Getenv("WEG_BIN")
	if bin == "" {
		t.Skip("WEG_BIN names no weg binary; `make interop WEG_BIN=...` runs this")
	}

	a, b := newServer(t, bin), newServer(t, bin)
	a.start()
	token := a.token()
	if code, body := a.do(http.MethodPost, "/cluster/init", token, nil); code != http.StatusOK {
		t.Fatalf("POST /cluster/init: %d %s", code, body)
	}
	b.start("--join", a.cluster)

	w := startWitness(t, witness.Prefix+"w")
	if err := w.Join(t.Context(), a.cluster); err != nil {
		t.Fatalf("the witness joins: %v", err)
	}

	t.Run("the cluster names the witness as one, and asks it how far it has got", func(t *testing.T) {
		st := a.status(token)
		roles := map[string]string{}
		for _, m := range st.Members {
			roles[m.ID] = m.Role
			if m.ID == witness.Prefix+"w" && len(m.Progress) == 0 {
				t.Errorf("the witness was not asked how far it has got: %+v", m)
			}
		}
		if len(roles) != 3 || roles[witness.Prefix+"w"] != "witness" {
			t.Errorf("roles = %v, want two voters and the witness", roles)
		}
	})

	t.Run("asked on its own port, the witness says it takes part", func(t *testing.T) {
		tr, err := transport.New(transport.Config{Secret: secret})
		if err != nil {
			t.Fatalf("transport: %v", err)
		}
		_, addr := w.Member()
		p, err := witness.Probe(t.Context(), tr, addr)
		if err != nil || !p.TakesPart() || p.Leader == "" {
			t.Errorf("probed %+v, %v; want it taking part, with a leader", p, err)
		}
	})

	t.Run("a second witness would be half of the voters, and is refused", func(t *testing.T) {
		other := startWitness(t, witness.Prefix+"other")
		err := other.Join(t.Context(), a.cluster)
		if err == nil || !strings.Contains(err.Error(), "fewer than half") {
			t.Errorf("a second witness joined: %v", err)
		}
	})

	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("what b reported:\n%s", b.stderr.String())
		}
	})
	b.stop()
	if code, body := a.do(http.MethodPost, "/zones", token, map[string]string{"name": "two.example."}); code != http.StatusCreated {
		t.Fatalf("a write with one server and the witness: %d %s", code, body)
	}
	a.stop()
	b.start()

	waitFor(t, "the server that came back to lead", func() bool {
		for _, m := range b.status(token).Members {
			if m.Leader {
				return m.ID != witness.Prefix+"w" && !w.IsLeader() && m.ID == memberID(t, b, token)
			}
		}
		return false
	})
	if got := b.zones(token); !strings.Contains(got, "two.example.") {
		t.Errorf("the server that came back holds %s, want the zone written while it was down", got)
	}
}

// memberID is the identifier the server is a member by.
func memberID(t *testing.T, s *server, token string) string {
	t.Helper()
	code, body := s.do(http.MethodGet, "/cluster", token, nil)
	var st struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	if code != http.StatusOK || json.Unmarshal(body, &st) != nil {
		t.Fatalf("GET /cluster: %d %s", code, body)
	}
	return st.Self.ID
}
