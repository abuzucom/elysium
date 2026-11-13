package ingest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"

	"github.com/pion/dtls/v3"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

// SourceConfig configures a broadcaster source.
type SourceConfig struct {
	// Certificate authenticates the source to the gateway.
	Certificate tls.Certificate
	// GatewayFingerprint pins the gateway certificate.
	GatewayFingerprint Fingerprint
}

func (cfg SourceConfig) validate() error {
	switch {
	case !hasCertificate(cfg.Certificate):
		return fmt.Errorf("%w: source certificate and key required", ErrInvalidConfig)
	case cfg.GatewayFingerprint == Fingerprint{}:
		return fmt.Errorf("%w: gateway fingerprint required", ErrInvalidConfig)
	}
	return nil
}

// Source sends SRTP-protected RTP to one gateway.
type Source struct {
	udp  *net.UDPConn
	peer *datagramConn
	dtls *dtls.Conn
	wg   sync.WaitGroup

	// mu guards srtp. SRTP contexts are not safe for concurrent use.
	mu   sync.Mutex
	srtp *srtp.Context

	closeOnce sync.Once
	closeErr  error
}

// DialSource authenticates to the gateway at addr. ctx bounds the
// handshake.
func DialSource(ctx context.Context, addr *net.UDPAddr, cfg SourceConfig) (*Source, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	udp, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("ingest: dial: %w", err)
	}
	source := &Source{
		udp:  udp,
		peer: newDatagramConn(udp.LocalAddr(), addr, udp.Write),
	}
	source.wg.Add(1)
	go source.readLoop()

	if err := source.handshake(ctx, addr, cfg); err != nil {
		return nil, errors.Join(err, source.Close())
	}
	source.wg.Add(1)
	go func() {
		defer source.wg.Done()
		drainDTLS(source.dtls)
	}()
	return source, nil
}

func (s *Source) handshake(ctx context.Context, addr *net.UDPAddr, cfg SourceConfig) error {
	pinned := map[Fingerprint]struct{}{cfg.GatewayFingerprint: {}}
	conn, err := dtls.Client(s.peer, addr, &dtls.Config{
		Certificates: []tls.Certificate{cfg.Certificate},
		// Fingerprint pinning in VerifyPeerCertificate replaces CA chain
		// validation. pion/dtls runs the callback in both modes.
		InsecureSkipVerify:     true,
		VerifyPeerCertificate:  pinVerifier(pinned),
		SRTPProtectionProfiles: srtpProfiles,
		ExtendedMasterSecret:   dtls.RequireExtendedMasterSecret,
	})
	if err != nil {
		return fmt.Errorf("ingest: start DTLS client: %w", err)
	}
	s.dtls = conn
	if err := conn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("ingest: DTLS handshake: %w", err)
	}
	contexts, err := deriveSRTPContexts(conn, true)
	if err != nil {
		return err
	}
	s.srtp = contexts.local
	return nil
}

// readLoop forwards DTLS datagrams from the gateway to the DTLS client.
// The gateway sends no RTP in this direction yet.
func (s *Source) readLoop() {
	defer s.wg.Done()
	buf := make([]byte, maxDatagramBytes)
	for {
		n, err := s.udp.Read(buf)
		if errors.Is(err, syscall.ECONNREFUSED) {
			// An ICMP port-unreachable reply surfaces on a connected
			// UDP socket. The DTLS handshake timeout handles it.
			continue
		}
		if err != nil {
			s.peer.Close()
			return
		}
		if classifyDatagram(buf[:n]) == classDTLS {
			s.peer.deliver(buf[:n])
		}
	}
}

// WriteRTP protects and sends one RTP packet. The payload passes through
// unchanged.
func (s *Source) WriteRTP(packet *rtp.Packet) error {
	plaintext, err := packet.Marshal()
	if err != nil {
		return fmt.Errorf("ingest: marshal RTP: %w", err)
	}
	s.mu.Lock()
	ciphertext, err := s.srtp.EncryptRTP(nil, plaintext, nil)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("ingest: encrypt RTP: %w", err)
	}
	if _, err := s.udp.Write(ciphertext); err != nil {
		return fmt.Errorf("ingest: send RTP: %w", err)
	}
	return nil
}

// Close sends a DTLS close_notify when possible, releases the socket, and
// waits for the source goroutines to exit.
func (s *Source) Close() error {
	s.closeOnce.Do(func() {
		var dtlsErr error
		if s.dtls != nil {
			dtlsErr = s.dtls.Close()
		}
		s.peer.Close()
		udpErr := s.udp.Close()
		s.wg.Wait()
		s.closeErr = errors.Join(ignoreClosed(dtlsErr), ignoreClosed(udpErr))
	})
	return s.closeErr
}

// ignoreClosed drops errors that only report an already closed conn.
func ignoreClosed(err error) error {
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
