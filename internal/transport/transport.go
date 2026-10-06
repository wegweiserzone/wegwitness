// Package transport is the cluster port as wegweiser speaks it: TLS 1.3, the
// secret proved inside the session, and one octet saying what the stream
// carries (wegweiser's docs/decisions/d43-the-cluster-transport.md).
//
// It is a copy of wegweiser's internal/cluster/transport.go, made on purpose
// rather than shared (wegweiser's docs/decisions/d39-the-witness.md). The two
// have to agree on every constant below, and a change to one is a change to
// the other.
package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"time"
)

// StreamKind says what an authenticated stream carries.
type StreamKind byte

const (
	// StreamRaft is Raft's own traffic: votes, entries and snapshots.
	StreamRaft StreamKind = 'r'

	// StreamForward is a write forwarded to the leader
	// (wegweiser's docs/decisions/d40-a-write-reaches-the-leader.md).
	StreamForward StreamKind = 'f'

	// StreamJoin is a node asking to be made a member
	// (wegweiser's docs/decisions/d44-starting-and-joining.md).
	StreamJoin StreamKind = 'j'
)

// String names the kind for a message somebody reads.
func (k StreamKind) String() string {
	switch k {
	case StreamRaft:
		return "raft"
	case StreamForward:
		return "forwarded-write"
	case StreamJoin:
		return "join"
	default:
		return fmt.Sprintf("unknown (%q)", byte(k))
	}
}

func (k StreamKind) known() bool {
	return k == StreamRaft || k == StreamForward || k == StreamJoin
}

// The protocol's constants, as wegweiser's internal/cluster/transport.go
// defines them. That file is the contract; these are a copy of it.
const (
	// ALPN names the protocol and its version. A peer that names anything
	// else, or nothing at all, is refused.
	ALPN = "wegweiser-cluster/1"

	// ExporterLabel is the label the keying material a proof is bound to is
	// drawn under (RFC 5705).
	ExporterLabel = "EXPORTER-wegweiser-cluster-proof"

	// keyingOctets is how much keying material a proof covers.
	keyingOctets = 32

	// The roles a proof names, so that a proof is never reflected back at the
	// side that sent it.
	roleDialer   = "dialer"
	roleListener = "listener"
)

// MinSecret is the shortest secret accepted, in octets: 256 bits. A member
// that dials an impostor hands it a proof, and a proof is an offline test of
// any guess at the secret.
const MinSecret = 32

// DefaultHandshakeTimeout bounds how long a connection has to prove itself.
const DefaultHandshakeTimeout = 10 * time.Second

// maxHandshakes bounds how many connections may be proving themselves at once.
// The port answers whoever can reach the host, and each handshake holds a
// goroutine until its deadline.
const maxHandshakes = 64

// queueDepth is how many authenticated streams of one kind may wait to be
// taken before more are turned away.
const queueDepth = 16

// acceptBackoff is the pause after an accept that failed for a reason other
// than the listener closing, such as running out of file descriptors.
const acceptBackoff = 50 * time.Millisecond

var (
	// ErrSecretMismatch is a proof that does not verify: the other end holds
	// another secret, or none.
	ErrSecretMismatch = errors.New("transport: the other end's proof does not match this member's secret")

	// ErrRefused is the other end hanging up rather than proving itself. A
	// member that does not accept a proof says nothing, so this is most often
	// a different secret; it is also what another version, or a stream the
	// other end does not take, looks like from here.
	ErrRefused = errors.New(
		"transport: the other end hung up instead of proving itself; it holds another secret, " +
			"speaks another version, or does not take this stream")
)

// Config is what a [Transport] needs.
type Config struct {
	// Secret is the cluster's shared secret, at least [MinSecret] octets.
	Secret []byte

	// HandshakeTimeout bounds TLS and the proof together. Zero picks
	// [DefaultHandshakeTimeout].
	HandshakeTimeout time.Duration

	// OnRefused hears about a connection turned away, and why. A port that a
	// network can reach is probed by strangers, so this is an observation
	// rather than a fault, and the wiring decides whether to count it or log
	// it. May be nil.
	OnRefused func(remote net.Addr, err error)
}

// Transport opens and accepts connections between members of one cluster.
type Transport struct {
	secret    []byte
	cert      tls.Certificate
	timeout   time.Duration
	onRefused func(net.Addr, error)
}

// New returns a transport for the cluster the secret belongs to.
func New(cfg Config) (*Transport, error) {
	if len(cfg.Secret) < MinSecret {
		return nil, fmt.Errorf(
			"transport: the secret is %d bits and has to be at least %d; a member that dials an "+
				"impostor hands it something to test guesses against (wegweiser's docs/decisions/d43-the-cluster-transport.md)",
			len(cfg.Secret)*8, MinSecret*8)
	}
	cert, err := throwawayCertificate()
	if err != nil {
		return nil, err
	}
	timeout := cfg.HandshakeTimeout
	if timeout <= 0 {
		timeout = DefaultHandshakeTimeout
	}
	return &Transport{
		secret: bytes.Clone(cfg.Secret), cert: cert, timeout: timeout, onRefused: cfg.OnRefused,
	}, nil
}

// throwawayCertificate makes the certificate TLS agrees a key with. It is made
// at start, never written down, and never verified by anybody: who the peer
// is follows from the proof exchanged inside the session, not from this.
func throwawayCertificate() (tls.Certificate, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("transport: make a key pair: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("transport: draw a serial number: %w", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "wegweiser cluster member"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(100, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("transport: make a certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, nil
}

func (t *Transport) listenerTLS() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{t.cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{ALPN},
	}
}

func (t *Transport) dialerTLS() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{ALPN},
		// The certificate vouches for nothing here, so there is nothing to
		// check it against. Whether the peer is a member is the proof it sends
		// inside this session, which a party in the middle cannot pass on.
		InsecureSkipVerify: true, //nolint:gosec // D43: membership is proved inside the session, not by the certificate
	}
}

// proof is what one side of a connection sends: the secret's HMAC over the
// side's role and the session's keying material.
func (t *Transport) proof(role string, keying []byte) []byte {
	mac := hmac.New(sha256.New, t.secret)
	mac.Write([]byte(role + "\x00"))
	mac.Write(keying)
	return mac.Sum(nil)
}

// sessionKeying draws the material a proof is bound to, once it is clear the
// session speaks this protocol. A peer offering no ALPN at all gets through
// the TLS handshake with nothing negotiated, which is why this checks rather
// than trusting the handshake to have refused it.
func sessionKeying(conn *tls.Conn) ([]byte, error) {
	state := conn.ConnectionState()
	if state.NegotiatedProtocol != ALPN {
		return nil, fmt.Errorf("the other end does not speak %s", ALPN)
	}
	return state.ExportKeyingMaterial(ExporterLabel, nil, keyingOctets)
}

// Dial opens a stream of one kind to the member at addr.
func (t *Transport) Dial(ctx context.Context, addr string, kind StreamKind) (net.Conn, error) {
	if !kind.known() {
		return nil, fmt.Errorf("transport: there are no %s streams", kind)
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("transport: reach %s: %w", addr, err)
	}
	conn := tls.Client(raw, t.dialerTLS())
	if cerr := t.call(ctx, conn, kind); cerr != nil {
		return nil, errors.Join(fmt.Errorf("transport: %s: %w", addr, cerr), closeQuietly(conn))
	}
	return stream{conn}, nil
}

// call is the dialling side of the handshake. It proves itself first and says
// which stream it wants in the same breath, then insists on the other end's
// proof before the stream is used.
func (t *Transport) call(ctx context.Context, conn *tls.Conn, kind StreamKind) error {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("TLS handshake: %w", err)
	}
	keying, err := sessionKeying(conn)
	if err != nil {
		return err
	}
	if _, werr := conn.Write(append(t.proof(roleDialer, keying), byte(kind))); werr != nil {
		return fmt.Errorf("send the proof: %w", werr)
	}

	theirs := make([]byte, sha256.Size)
	if _, rerr := io.ReadFull(conn, theirs); rerr != nil {
		return fmt.Errorf("%w (%w)", ErrRefused, rerr)
	}
	if !hmac.Equal(theirs, t.proof(roleListener, keying)) {
		return ErrSecretMismatch
	}
	return conn.SetDeadline(time.Time{})
}

// answer is the listening side of the handshake. It speaks second, so a
// stranger without the secret learns only that a TLS server is here.
//
// The context carries the handshake's deadline, and ends early when the mux
// closes.
func (t *Transport) answer(ctx context.Context, conn *tls.Conn) (StreamKind, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return 0, err
		}
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		return 0, fmt.Errorf("TLS handshake: %w", err)
	}
	keying, err := sessionKeying(conn)
	if err != nil {
		return 0, err
	}

	got := make([]byte, sha256.Size+1)
	if _, rerr := io.ReadFull(conn, got); rerr != nil {
		return 0, fmt.Errorf("read the proof: %w", rerr)
	}
	if !hmac.Equal(got[:sha256.Size], t.proof(roleDialer, keying)) {
		return 0, ErrSecretMismatch
	}
	kind := StreamKind(got[sha256.Size])
	if !kind.known() {
		return 0, fmt.Errorf("the stream says it is %s, which this member does not carry", kind)
	}

	if _, werr := conn.Write(t.proof(roleListener, keying)); werr != nil {
		return 0, fmt.Errorf("send the proof: %w", werr)
	}
	return kind, conn.SetDeadline(time.Time{})
}

func (t *Transport) refused(remote net.Addr, err error) {
	if t.onRefused != nil {
		t.onRefused(remote, err)
	}
}

// Serve accepts connections on l, authenticates each, and hands it to whoever
// takes streams of the kind it says it is. The mux owns l from here on.
func (t *Transport) Serve(l net.Listener) *Mux {
	m := &Mux{
		t:     t,
		inner: l,
		queues: map[StreamKind]*queue{
			StreamRaft:    newQueue(),
			StreamForward: newQueue(),
			StreamJoin:    newQueue(),
		},
		slots:    make(chan struct{}, maxHandshakes),
		inflight: make(map[net.Conn]struct{}),
	}
	m.ctx, m.stop = context.WithCancel(context.Background())
	m.wg.Add(1)
	go m.accept()
	return m
}

// Mux is the cluster port: one listener, sorted into one stream of
// connections per kind.
type Mux struct {
	t      *Transport
	inner  net.Listener
	queues map[StreamKind]*queue
	slots  chan struct{}

	// ctx ends when the mux closes, and every handshake in flight with it.
	ctx  context.Context
	stop context.CancelFunc

	// inflight are the connections still proving themselves, so that closing
	// the mux does not wait out their deadlines.
	mu       sync.Mutex
	inflight map[net.Conn]struct{}

	closeOnce sync.Once
	closeErr  error
	wg        sync.WaitGroup
}

// Addr is where the cluster port listens.
func (m *Mux) Addr() net.Addr { return m.inner.Addr() }

// Listener returns the streams of one kind, as a listener that whatever
// carries them can serve on. Closing it turns that kind away and leaves the
// others running.
func (m *Mux) Listener(kind StreamKind) net.Listener {
	return &kindListener{m: m, q: m.queues[kind], kind: kind}
}

func (m *Mux) closed() bool { return m.ctx.Err() != nil }

func (m *Mux) accept() {
	defer m.wg.Done()
	for {
		raw, err := m.inner.Accept()
		if err != nil {
			if m.closed() || errors.Is(err, net.ErrClosed) {
				return
			}
			m.t.refused(nil, fmt.Errorf("accept on the cluster port: %w", err))
			select {
			case <-m.ctx.Done():
				return
			case <-time.After(acceptBackoff):
			}
			continue
		}

		select {
		case m.slots <- struct{}{}:
		default:
			m.refuse(raw, errors.New("too many connections are proving themselves at once"))
			continue
		}
		if !m.track(raw) {
			<-m.slots
			m.refuse(raw, net.ErrClosed)
			continue
		}

		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			defer func() { <-m.slots }()
			m.admit(raw)
		}()
	}
}

func (m *Mux) admit(raw net.Conn) {
	defer m.untrack(raw)

	ctx, cancel := context.WithTimeout(m.ctx, m.t.timeout)
	defer cancel()

	conn := tls.Server(raw, m.t.listenerTLS())
	kind, err := m.t.answer(ctx, conn)
	if err != nil {
		m.refuse(conn, err)
		return
	}
	if !m.queues[kind].offer(conn) {
		m.refuse(conn, fmt.Errorf("nobody on this member is taking %s streams", kind))
	}
}

func (m *Mux) track(c net.Conn) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight == nil {
		return false
	}
	m.inflight[c] = struct{}{}
	return true
}

func (m *Mux) untrack(c net.Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight != nil {
		delete(m.inflight, c)
	}
}

// refuse closes a connection and says why, unless the mux is closing: then
// every connection still in flight is closed on purpose and none of it is news.
func (m *Mux) refuse(c net.Conn, why error) {
	err := why
	if cerr := closeQuietly(c); cerr != nil {
		err = errors.Join(err, cerr)
	}
	if m.closed() {
		return
	}
	m.t.refused(c.RemoteAddr(), err)
}

// Close stops the port, closes every connection still proving itself or
// waiting to be taken, and returns once nothing it started is running.
func (m *Mux) Close() error {
	m.closeOnce.Do(func() {
		m.stop()
		err := m.inner.Close()

		m.mu.Lock()
		pending := m.inflight
		m.inflight = nil
		m.mu.Unlock()
		for c := range pending {
			err = errors.Join(err, closeQuietly(c))
		}
		m.wg.Wait()

		for _, q := range m.queues {
			err = errors.Join(err, q.shut())
		}
		m.closeErr = err
	})
	return m.closeErr
}

// queue holds the authenticated streams of one kind until they are taken.
type queue struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newQueue() *queue {
	return &queue{conns: make(chan net.Conn, queueDepth), closed: make(chan struct{})}
}

// offer hands a stream over, and reports false when nobody will take it: the
// kind was closed, or too many are waiting already.
func (q *queue) offer(c net.Conn) bool {
	select {
	case <-q.closed:
		return false
	default:
	}
	select {
	case q.conns <- c:
		return true
	default:
		return false
	}
}

// shut turns the kind away and closes whatever was waiting to be taken.
func (q *queue) shut() error {
	q.once.Do(func() { close(q.closed) })

	var err error
	for {
		select {
		case c := <-q.conns:
			err = errors.Join(err, closeQuietly(c))
		default:
			return err
		}
	}
}

// kindListener is the streams of one kind, as a [net.Listener].
type kindListener struct {
	m    *Mux
	q    *queue
	kind StreamKind
}

func (l *kindListener) Accept() (net.Conn, error) {
	if l.q == nil {
		return nil, fmt.Errorf("transport: there are no %s streams", l.kind)
	}
	select {
	case c := <-l.q.conns:
		return stream{c}, nil
	case <-l.q.closed:
		return nil, net.ErrClosed
	case <-l.m.ctx.Done():
		return nil, net.ErrClosed
	}
}

func (l *kindListener) Close() error {
	if l.q == nil {
		return nil
	}
	return l.q.shut()
}

func (l *kindListener) Addr() net.Addr { return l.m.Addr() }

// stream is an authenticated connection as whoever takes it sees it: a
// stream of bytes. That TLS carries it is the transport's business, and a
// consumer that can see a *tls.Conn acts on it. net/http does: a connection
// whose ALPN names a protocol other than HTTP is handed to whoever registered
// that protocol, and closed when nobody did.
type stream struct{ net.Conn }

// closeQuietly closes c and reports a failure, except the one saying it was
// closed already.
func closeQuietly(c net.Conn) error {
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}
