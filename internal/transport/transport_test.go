package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// patience is how long a test waits for something that should take
// milliseconds before it calls the wait a failure.
const patience = 5 * time.Second

func secretOf(b byte) []byte { return bytes.Repeat([]byte{b}, MinSecret) }

// refusals is what a transport says it turned away.
type refusals chan error

func (r refusals) hear(_ net.Addr, err error) {
	select {
	case r <- err:
	default:
	}
}

// next waits for the next refusal, and fails the test when none comes.
func (r refusals) next(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r:
		return err
	case <-time.After(patience):
		t.Fatal("nothing was refused")
		return nil
	}
}

func newTransport(t *testing.T, secret []byte, timeout time.Duration) (*Transport, refusals) {
	t.Helper()
	heard := make(refusals, 64)
	tr, err := New(Config{Secret: secret, HandshakeTimeout: timeout, OnRefused: heard.hear})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr, heard
}

func serve(t *testing.T, tr *Transport) *Mux {
	t.Helper()
	l, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	m := tr.Serve(l)
	t.Cleanup(func() {
		if cerr := m.Close(); cerr != nil {
			t.Errorf("close the mux: %v", cerr)
		}
	})
	return m
}

// keep closes c when the test ends, and bounds everything done with it.
func keep[C net.Conn](t *testing.T, c C) C {
	t.Helper()
	t.Cleanup(func() {
		if err := closeQuietly(c); err != nil {
			t.Errorf("close a stream: %v", err)
		}
	})
	if err := c.SetDeadline(time.Now().Add(patience)); err != nil {
		t.Fatalf("set a deadline: %v", err)
	}
	return c
}

func rawDial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := (&net.Dialer{Timeout: patience}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return c
}

func dial(t *testing.T, tr *Transport, m *Mux, kind StreamKind) net.Conn {
	t.Helper()
	c, err := tr.Dial(t.Context(), m.Addr().String(), kind)
	if err != nil {
		t.Fatalf("Dial %s: %v", kind, err)
	}
	return keep(t, c)
}

func accept(t *testing.T, l net.Listener) net.Conn {
	t.Helper()
	type taken struct {
		c   net.Conn
		err error
	}
	got := make(chan taken, 1)
	go func() {
		c, err := l.Accept()
		got <- taken{c, err}
	}()
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("Accept: %v", r.err)
		}
		return keep(t, r.c)
	case <-time.After(patience):
		t.Fatal("no stream arrived")
		return nil
	}
}

func say(t *testing.T, c net.Conn, what string) {
	t.Helper()
	if _, err := io.WriteString(c, what); err != nil {
		t.Fatalf("write %q: %v", what, err)
	}
}

func hear(t *testing.T, c net.Conn, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buf)
}

// hungUp reports whether the other end closed c, as opposed to c's own
// deadline running out, which a closed-looking read error would also be.
func hungUp(err error) bool {
	var ne net.Error
	return err != nil && (!errors.As(err, &ne) || !ne.Timeout())
}

func TestAMemberReachesAMember(t *testing.T) {
	t.Parallel()
	a, _ := newTransport(t, secretOf(1), 0)
	b, _ := newTransport(t, secretOf(1), 0)
	m := serve(t, b)

	out := dial(t, a, m, StreamRaft)
	in := accept(t, m.Listener(StreamRaft))

	say(t, out, "ping")
	if got := hear(t, in, 4); got != "ping" {
		t.Errorf("the listening side heard %q, want ping", got)
	}
	say(t, in, "pong")
	if got := hear(t, out, 4); got != "pong" {
		t.Errorf("the dialling side heard %q, want pong", got)
	}
}

func TestEachStreamArrivesWhereItsKindIsTaken(t *testing.T) {
	t.Parallel()
	a, _ := newTransport(t, secretOf(1), 0)
	b, _ := newTransport(t, secretOf(1), 0)
	m := serve(t, b)

	say(t, dial(t, a, m, StreamForward), "F")
	say(t, dial(t, a, m, StreamRaft), "R")

	if got := hear(t, accept(t, m.Listener(StreamRaft)), 1); got != "R" {
		t.Errorf("the raft streams delivered %q", got)
	}
	if got := hear(t, accept(t, m.Listener(StreamForward)), 1); got != "F" {
		t.Errorf("the forwarded writes delivered %q", got)
	}
}

// Whichever end holds the other secret, it is the answering side that finds
// out, because it checks first and hangs up. The dialling side sees only that.
func TestADifferentSecretIsRefused(t *testing.T) {
	t.Parallel()
	a, _ := newTransport(t, secretOf(1), 0)
	b, heard := newTransport(t, secretOf(2), 0)
	m := serve(t, b)

	if _, err := a.Dial(t.Context(), m.Addr().String(), StreamRaft); !errors.Is(err, ErrRefused) {
		t.Errorf("Dial error = %v, want the other end to have hung up", err)
	}
	if got := heard.next(t); !errors.Is(got, ErrSecretMismatch) {
		t.Errorf("the listener refused for %v, want a proof that did not match", got)
	}
}

// A stranger who runs a TLS server and answers with thirty-two octets of
// anything is not taken for a member: the dialling side checks the answer.
func TestAnImpostorThatAnswersIsNotBelieved(t *testing.T) {
	t.Parallel()

	cert, err := throwawayCertificate()
	if err != nil {
		t.Fatalf("make a certificate: %v", err)
	}
	inner, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	l := tls.NewListener(inner, &tls.Config{
		Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{ALPN},
	})
	t.Cleanup(func() {
		if cerr := l.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			t.Errorf("close the impostor: %v", cerr)
		}
	})

	var problem error
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, aerr := l.Accept()
		if aerr != nil {
			problem = aerr
			return
		}
		if _, rerr := io.ReadFull(c, make([]byte, sha256.Size+1)); rerr != nil {
			problem = errors.Join(rerr, closeQuietly(c))
			return
		}
		if _, werr := c.Write(make([]byte, sha256.Size)); werr != nil {
			problem = errors.Join(werr, closeQuietly(c))
			return
		}
		// Hold on until the member hangs up, so that what it reads is the
		// false proof and not the end of the connection.
		_, cerr := io.Copy(io.Discard, c)
		problem = errors.Join(cerr, closeQuietly(c))
	}()

	a, _ := newTransport(t, secretOf(1), 0)
	if _, derr := a.Dial(t.Context(), l.Addr().String(), StreamRaft); !errors.Is(derr, ErrSecretMismatch) {
		t.Errorf("Dial error = %v, want the impostor's proof refused", derr)
	}
	select {
	case <-done:
		if problem != nil {
			t.Errorf("the impostor did not get to lie: %v", problem)
		}
	case <-time.After(patience):
		t.Fatal("the impostor is still waiting")
	}
}

func TestAShortSecretIsRefused(t *testing.T) {
	t.Parallel()

	_, err := New(Config{Secret: secretOf(1)[:MinSecret-1]})
	if err == nil || !strings.Contains(err.Error(), "248 bits") {
		t.Errorf("New with 31 octets = %v, want a refusal naming the length", err)
	}
}

func TestAPeerThatDoesNotSpeakTheProtocolIsRefused(t *testing.T) {
	t.Parallel()

	// tlsKnock is a stranger's TLS client. It checks nothing, because it is a
	// stranger, and whether its handshake gets through is one of the outcomes.
	tlsKnock := func(protos []string) func(*testing.T, string) {
		return func(t *testing.T, addr string) {
			d := tls.Dialer{Config: &tls.Config{
				MinVersion: tls.VersionTLS13, NextProtos: protos,
				InsecureSkipVerify: true,
			}}
			if c, err := d.DialContext(t.Context(), "tcp", addr); err == nil {
				keep(t, c)
			}
		}
	}

	tests := []struct {
		name  string
		knock func(*testing.T, string)
		want  string
	}{
		{
			name: "plain text",
			knock: func(t *testing.T, addr string) {
				say(t, keep(t, rawDial(t, addr)), "GET / HTTP/1.1\r\n\r\n")
			},
			want: "TLS handshake",
		},
		{name: "TLS naming no protocol at all", knock: tlsKnock(nil), want: "does not speak"},
		{name: "TLS naming another protocol", knock: tlsKnock([]string{"h2"}), want: "TLS handshake"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, heard := newTransport(t, secretOf(1), 0)
			m := serve(t, b)

			tt.knock(t, m.Addr().String())
			if got := heard.next(t); !strings.Contains(got.Error(), tt.want) {
				t.Errorf("refused for %v, want a reason mentioning %q", got, tt.want)
			}
		})
	}
}

func TestAStrangerWhoSaysNothingIsLetGo(t *testing.T) {
	t.Parallel()
	b, heard := newTransport(t, secretOf(1), 200*time.Millisecond)
	m := serve(t, b)

	c := keep(t, rawDial(t, m.Addr().String()))
	if got := heard.next(t); !strings.Contains(got.Error(), "TLS handshake") {
		t.Errorf("refused for %v, want the handshake to have run out of time", got)
	}
	if _, err := c.Read(make([]byte, 1)); !hungUp(err) {
		t.Errorf("reading after the time ran out gave %v, want the connection closed", err)
	}
}

func TestAnUnknownStreamIsRefused(t *testing.T) {
	t.Parallel()
	a, _ := newTransport(t, secretOf(1), 0)
	b, heard := newTransport(t, secretOf(1), 0)
	m := serve(t, b)

	conn := keep(t, tls.Client(rawDial(t, m.Addr().String()), a.dialerTLS()))
	ctx, cancel := context.WithTimeout(t.Context(), patience)
	defer cancel()
	if err := a.call(ctx, conn, StreamKind('x')); !errors.Is(err, ErrRefused) {
		t.Errorf("asking for an unknown stream gave %v, want the other end to hang up", err)
	}
	if got := heard.next(t); !strings.Contains(got.Error(), "does not carry") {
		t.Errorf("refused for %v, want the stream kind named", got)
	}
}

// What D43 leans on: a proof is good for one session, one side and one
// secret, and for nothing else.
func TestAProofIsBoundToItsSessionItsSideAndItsSecret(t *testing.T) {
	t.Parallel()
	a, _ := newTransport(t, secretOf(1), 0)
	other, _ := newTransport(t, secretOf(2), 0)
	one := bytes.Repeat([]byte{1}, keyingOctets)
	two := bytes.Repeat([]byte{2}, keyingOctets)

	base := a.proof(roleDialer, one)
	if !bytes.Equal(base, a.proof(roleDialer, one)) {
		t.Fatal("the same proof made twice came out different")
	}
	for name, p := range map[string][]byte{
		"another session": a.proof(roleDialer, two),
		"the other side":  a.proof(roleListener, one),
		"another secret":  other.proof(roleDialer, one),
	} {
		if bytes.Equal(p, base) {
			t.Errorf("a proof made for %s is the same proof", name)
		}
	}
}

func TestClosingLetsGoOfEverything(t *testing.T) {
	t.Parallel()
	b, _ := newTransport(t, secretOf(1), 0)
	m := serve(t, b)

	waiting := make(chan error, 1)
	go func() {
		_, err := m.Listener(StreamRaft).Accept()
		waiting <- err
	}()

	// A stranger halfway into the handshake, with the full ten seconds to go.
	stranger := keep(t, rawDial(t, m.Addr().String()))
	deadline := time.Now().Add(patience)
	for inFlight(m) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stranger never got as far as the handshake")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-waiting:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("Accept after Close gave %v, want the listener closed", err)
		}
	case <-time.After(patience):
		t.Fatal("Accept is still waiting after Close")
	}
	if _, err := stranger.Read(make([]byte, 1)); !hungUp(err) {
		t.Errorf("the stranger read %v after Close, want the connection closed", err)
	}
}

func inFlight(m *Mux) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inflight)
}
