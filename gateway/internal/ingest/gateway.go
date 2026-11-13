package ingest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

// GatewayConfig configures the ingest listener.
//
// HandshakeTimeout, MaxSources, and QueuePackets are experimental values
// with no defaults. See docs/backlog.md.
type GatewayConfig struct {
	// Certificate authenticates the gateway to sources.
	Certificate tls.Certificate
	// AllowedSources pins the enrolled source certificate fingerprints.
	AllowedSources []Fingerprint
	// HandshakeTimeout bounds each DTLS handshake.
	HandshakeTimeout time.Duration
	// MaxSources bounds concurrent peers, including handshakes in
	// progress.
	MaxSources int
	// QueuePackets bounds decrypted RTP packets buffered per session.
	// The gateway drops and counts packets beyond this bound.
	QueuePackets int
}

func (cfg GatewayConfig) validate() error {
	switch {
	case !hasCertificate(cfg.Certificate):
		return fmt.Errorf("%w: gateway certificate and key required", ErrInvalidConfig)
	case len(cfg.AllowedSources) == 0:
		return fmt.Errorf("%w: at least one allowed source required", ErrInvalidConfig)
	case cfg.HandshakeTimeout <= 0:
		return fmt.Errorf("%w: handshake timeout must be positive", ErrInvalidConfig)
	case cfg.MaxSources <= 0:
		return fmt.Errorf("%w: max sources must be positive", ErrInvalidConfig)
	case cfg.QueuePackets <= 0:
		return fmt.Errorf("%w: queue packets must be positive", ErrInvalidConfig)
	}
	return nil
}

// Gateway accepts authenticated DTLS-SRTP source sessions on one UDP
// socket.
type Gateway struct {
	conn       *net.UDPConn
	cfg        GatewayConfig
	dtlsConfig *dtls.Config
	accepted   chan *Session

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	peers map[string]*gatewayPeer

	closeOnce sync.Once
	closeErr  error
}

// gatewayPeer tracks one remote address. The session field stays nil
// until the handshake completes. Gateway.mu guards it.
type gatewayPeer struct {
	remote  *net.UDPAddr
	dtls    *datagramConn
	session *Session
}

// Listen validates cfg and starts a gateway on addr.
func Listen(addr *net.UDPAddr, cfg GatewayConfig) (*Gateway, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	pinned := make(map[Fingerprint]struct{}, len(cfg.AllowedSources))
	for _, fingerprint := range cfg.AllowedSources {
		pinned[fingerprint] = struct{}{}
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("ingest: listen: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	gateway := &Gateway{
		conn: conn,
		cfg:  cfg,
		dtlsConfig: &dtls.Config{
			Certificates:           []tls.Certificate{cfg.Certificate},
			ClientAuth:             dtls.RequireAnyClientCert,
			VerifyPeerCertificate:  pinVerifier(pinned),
			SRTPProtectionProfiles: srtpProfiles,
			ExtendedMasterSecret:   dtls.RequireExtendedMasterSecret,
		},
		accepted: make(chan *Session, cfg.MaxSources),
		ctx:      ctx,
		cancel:   cancel,
		peers:    make(map[string]*gatewayPeer),
	}
	gateway.wg.Add(1)
	go gateway.readLoop()
	return gateway, nil
}

// Addr returns the local UDP address.
func (g *Gateway) Addr() *net.UDPAddr {
	return g.conn.LocalAddr().(*net.UDPAddr)
}

// Accept returns the next authenticated session.
func (g *Gateway) Accept(ctx context.Context) (*Session, error) {
	select {
	case session := <-g.accepted:
		return session, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-g.ctx.Done():
		return nil, ErrClosed
	}
}

// Close stops the gateway, ends all sessions, and waits for its
// goroutines to exit.
func (g *Gateway) Close() error {
	g.closeOnce.Do(func() {
		g.cancel()
		g.closeErr = g.conn.Close()
		g.mu.Lock()
		for _, peer := range g.peers {
			peer.dtls.Close()
		}
		g.mu.Unlock()
		g.wg.Wait()
	})
	return g.closeErr
}

// readLoop owns the UDP socket and all remote SRTP contexts. Only this
// goroutine decrypts, so the contexts need no lock.
func (g *Gateway) readLoop() {
	defer g.wg.Done()
	buf := make([]byte, maxDatagramBytes)
	for {
		n, remote, err := g.conn.ReadFromUDP(buf)
		if err != nil {
			// A closed socket ends the gateway. Other UDP read errors
			// also stop the loop rather than spin on a broken socket.
			g.cancel()
			return
		}
		g.dispatch(buf[:n], remote)
	}
}

func (g *Gateway) dispatch(datagram []byte, remote *net.UDPAddr) {
	switch classifyDatagram(datagram) {
	case classDTLS:
		g.dispatchDTLS(datagram, remote)
	case classRTP:
		g.dispatchRTP(datagram, remote)
	default:
		// SRTCP and unknown datagrams are out of scope for this
		// increment and are dropped.
	}
}

func (g *Gateway) dispatchDTLS(datagram []byte, remote *net.UDPAddr) {
	g.mu.Lock()
	peer, ok := g.peers[remote.String()]
	if !ok {
		peer = g.addPeerLocked(remote)
	}
	g.mu.Unlock()
	if peer != nil {
		peer.dtls.deliver(datagram)
	}
}

// addPeerLocked registers a new peer and starts its handshake. It
// returns nil when the gateway is closing or at its source limit.
func (g *Gateway) addPeerLocked(remote *net.UDPAddr) *gatewayPeer {
	if g.ctx.Err() != nil || len(g.peers) >= g.cfg.MaxSources {
		return nil
	}
	peer := &gatewayPeer{
		remote: remote,
		dtls: newDatagramConn(g.conn.LocalAddr(), remote, func(b []byte) (int, error) {
			return g.conn.WriteToUDP(b, remote)
		}),
	}
	g.peers[remote.String()] = peer
	g.wg.Add(1)
	go g.servePeer(peer)
	return peer
}

func (g *Gateway) removePeer(peer *gatewayPeer) {
	g.mu.Lock()
	if g.peers[peer.remote.String()] == peer {
		delete(g.peers, peer.remote.String())
	}
	session := peer.session
	g.mu.Unlock()
	peer.dtls.Close()
	if session != nil {
		session.markClosed()
	}
}

func (g *Gateway) dispatchRTP(datagram []byte, remote *net.UDPAddr) {
	g.mu.Lock()
	var session *Session
	if peer, ok := g.peers[remote.String()]; ok {
		session = peer.session
	}
	g.mu.Unlock()
	if session == nil {
		// Unauthenticated RTP never reaches a session.
		return
	}
	session.receive(datagram)
}

// servePeer runs the DTLS handshake, publishes the session, and then
// keeps reading DTLS records until the peer closes or fails.
func (g *Gateway) servePeer(peer *gatewayPeer) {
	defer g.wg.Done()
	defer g.removePeer(peer)

	conn, err := g.handshake(peer)
	if err != nil {
		return
	}
	defer conn.Close()

	session, err := g.newSession(peer, conn)
	if err != nil {
		return
	}
	g.mu.Lock()
	peer.session = session
	g.mu.Unlock()
	select {
	case g.accepted <- session:
	case <-g.ctx.Done():
		return
	}
	drainDTLS(conn)
}

func (g *Gateway) handshake(peer *gatewayPeer) (*dtls.Conn, error) {
	ctx, cancel := context.WithTimeout(g.ctx, g.cfg.HandshakeTimeout)
	defer cancel()
	conn, err := dtls.Server(peer.dtls, peer.remote, g.dtlsConfig)
	if err != nil {
		return nil, err
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		closeErr := conn.Close()
		return nil, errors.Join(err, closeErr)
	}
	return conn, nil
}

func (g *Gateway) newSession(peer *gatewayPeer, conn *dtls.Conn) (*Session, error) {
	fingerprint, err := peerFingerprint(conn)
	if err != nil {
		return nil, err
	}
	contexts, err := deriveSRTPContexts(conn, false)
	if err != nil {
		return nil, err
	}
	return &Session{
		fingerprint: fingerprint,
		remote:      peer.remote,
		srtp:        contexts.remote,
		packets:     make(chan *rtp.Packet, g.cfg.QueuePackets),
		done:        make(chan struct{}),
	}, nil
}

// drainDTLS reads post-handshake DTLS records so alerts and close_notify
// take effect. It returns when the peer closes or the conn fails.
func drainDTLS(conn *dtls.Conn) {
	buf := make([]byte, maxDatagramBytes)
	for {
		if _, err := conn.Read(buf); err != nil {
			return
		}
	}
}

// Session is one authenticated source session on the gateway.
type Session struct {
	fingerprint Fingerprint
	remote      *net.UDPAddr
	// srtp is used only by the gateway read loop.
	srtp    *srtp.Context
	packets chan *rtp.Packet
	dropped atomic.Uint64

	closeOnce sync.Once
	done      chan struct{}
}

// SourceFingerprint returns the pinned fingerprint the source presented.
func (s *Session) SourceFingerprint() Fingerprint { return s.fingerprint }

// RemoteAddr returns the source UDP address.
func (s *Session) RemoteAddr() *net.UDPAddr { return s.remote }

// DroppedPackets counts packets that failed SRTP authentication, failed
// to parse, or overflowed the session queue.
func (s *Session) DroppedPackets() uint64 { return s.dropped.Load() }

// ReadRTP returns the next authenticated RTP packet. Queued packets stay
// readable after the session closes.
func (s *Session) ReadRTP(ctx context.Context) (*rtp.Packet, error) {
	select {
	case packet := <-s.packets:
		return packet, nil
	default:
	}
	select {
	case packet := <-s.packets:
		return packet, nil
	case <-s.done:
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// receive authenticates, decrypts, and queues one SRTP datagram.
func (s *Session) receive(datagram []byte) {
	plaintext, err := s.srtp.DecryptRTP(nil, datagram, nil)
	if err != nil {
		s.dropped.Add(1)
		return
	}
	packet := &rtp.Packet{}
	if err := packet.Unmarshal(plaintext); err != nil {
		s.dropped.Add(1)
		return
	}
	select {
	case s.packets <- packet:
	default:
		s.dropped.Add(1)
	}
}

func (s *Session) markClosed() {
	s.closeOnce.Do(func() { close(s.done) })
}
