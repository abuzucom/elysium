package ingest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	mathrand "math/rand/v2"
	"net"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// Test-only limits. They are not approved production values.
const (
	testHandshakeTimeout = 5 * time.Second
	testReadTimeout      = 5 * time.Second
	testRejectWait       = 500 * time.Millisecond
	testMaxSources       = 4
	testQueuePackets     = 512
	testPacketCount      = 200 // two seconds of 10 ms frames
	testFrameSamples     = 480
	testOpusPayloadType  = 111
	testSSRC             = 0x5eed1234
	// testPayloadBytes approximates one 10 ms frame at 256 kbps. The
	// payloads are synthetic bytes, not encoded Opus.
	testPayloadBytes = 320
)

var loopback = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}

// newTestCertificate creates a self-signed ECDSA P-256 certificate for
// tests. Production enrollment remains a deferred decision.
func newTestCertificate(t *testing.T, name string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate() error = %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func certificateFingerprint(cert tls.Certificate) Fingerprint {
	return FingerprintSHA256(cert.Certificate[0])
}

func startTestGateway(t *testing.T, gatewayCert tls.Certificate, allowed ...Fingerprint) *Gateway {
	t.Helper()
	gateway, err := Listen(loopback, GatewayConfig{
		Certificate:      gatewayCert,
		AllowedSources:   allowed,
		HandshakeTimeout: testHandshakeTimeout,
		MaxSources:       testMaxSources,
		QueuePackets:     testQueuePackets,
	})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() {
		if err := gateway.Close(); err != nil {
			t.Errorf("Gateway.Close() error = %v", err)
		}
	})
	return gateway
}

func dialTestSource(t *testing.T, gateway *Gateway, sourceCert tls.Certificate, pinned Fingerprint) (*Source, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testHandshakeTimeout)
	defer cancel()
	source, err := DialSource(ctx, gateway.Addr(), SourceConfig{
		Certificate:        sourceCert,
		GatewayFingerprint: pinned,
	})
	if err == nil {
		t.Cleanup(func() {
			if err := source.Close(); err != nil {
				t.Errorf("Source.Close() error = %v", err)
			}
		})
	}
	return source, err
}

func acceptSession(t *testing.T, gateway *Gateway) *Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testReadTimeout)
	defer cancel()
	session, err := gateway.Accept(ctx)
	if err != nil {
		t.Fatalf("Gateway.Accept() error = %v", err)
	}
	return session
}

func syntheticPackets(count int) []*rtp.Packet {
	generator := mathrand.New(mathrand.NewPCG(1, 2))
	packets := make([]*rtp.Packet, count)
	for i := range packets {
		payload := make([]byte, testPayloadBytes)
		for j := range payload {
			payload[j] = byte(generator.UintN(256))
		}
		packets[i] = &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    testOpusPayloadType,
				SequenceNumber: uint16(65500 + i), // crosses the 16-bit wrap
				Timestamp:      uint32(i * testFrameSamples),
				SSRC:           testSSRC,
			},
			Payload: payload,
		}
	}
	return packets
}

func payloadDigest(packets []*rtp.Packet) [sha256.Size]byte {
	hash := sha256.New()
	for _, packet := range packets {
		hash.Write(packet.Payload)
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func readPackets(t *testing.T, session *Session, count int) []*rtp.Packet {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testReadTimeout)
	defer cancel()
	received := make([]*rtp.Packet, 0, count)
	for len(received) < count {
		packet, err := session.ReadRTP(ctx)
		if err != nil {
			t.Fatalf("ReadRTP() after %d packets error = %v", len(received), err)
		}
		received = append(received, packet)
	}
	return received
}

func TestIngestDeliversOpusPayloadsUnchanged(t *testing.T) {
	gatewayCert := newTestCertificate(t, "gateway")
	sourceCert := newTestCertificate(t, "dj-computer")
	gateway := startTestGateway(t, gatewayCert, certificateFingerprint(sourceCert))
	source, err := dialTestSource(t, gateway, sourceCert, certificateFingerprint(gatewayCert))
	if err != nil {
		t.Fatalf("DialSource() error = %v", err)
	}
	session := acceptSession(t, gateway)
	sent := syntheticPackets(testPacketCount)

	for _, packet := range sent {
		if err := source.WriteRTP(packet); err != nil {
			t.Fatalf("WriteRTP() error = %v", err)
		}
	}
	received := readPackets(t, session, len(sent))

	if got := session.SourceFingerprint(); got != certificateFingerprint(sourceCert) {
		t.Errorf("SourceFingerprint() = %x, want %x", got, certificateFingerprint(sourceCert))
	}
	for i := range sent {
		if received[i].Header.SequenceNumber != sent[i].Header.SequenceNumber ||
			received[i].Header.Timestamp != sent[i].Header.Timestamp ||
			received[i].Header.SSRC != sent[i].Header.SSRC ||
			received[i].Header.PayloadType != sent[i].Header.PayloadType {
			t.Fatalf("packet %d header = %+v, want %+v", i, received[i].Header, sent[i].Header)
		}
	}
	if got, want := payloadDigest(received), payloadDigest(sent); got != want {
		t.Errorf("payload SHA-256 = %x, want %x", got, want)
	}
}

func TestIngestRejectsUnpinnedSourceCertificate(t *testing.T) {
	gatewayCert := newTestCertificate(t, "gateway")
	enrolledCert := newTestCertificate(t, "enrolled")
	intruderCert := newTestCertificate(t, "intruder")
	gateway := startTestGateway(t, gatewayCert, certificateFingerprint(enrolledCert))

	_, err := dialTestSource(t, gateway, intruderCert, certificateFingerprint(gatewayCert))

	if err == nil {
		t.Fatal("DialSource() with unpinned certificate error = nil, want handshake failure")
	}
	assertNoSession(t, gateway)
}

func TestSourceRejectsUnpinnedGatewayCertificate(t *testing.T) {
	gatewayCert := newTestCertificate(t, "gateway")
	impostorFingerprint := certificateFingerprint(newTestCertificate(t, "impostor"))
	sourceCert := newTestCertificate(t, "dj-computer")
	gateway := startTestGateway(t, gatewayCert, certificateFingerprint(sourceCert))

	_, err := dialTestSource(t, gateway, sourceCert, impostorFingerprint)

	if err == nil {
		t.Fatal("DialSource() with wrong gateway pin error = nil, want handshake failure")
	}
	assertNoSession(t, gateway)
}

func TestIngestIgnoresPlainRTPFromUnknownAddress(t *testing.T) {
	gatewayCert := newTestCertificate(t, "gateway")
	sourceCert := newTestCertificate(t, "dj-computer")
	gateway := startTestGateway(t, gatewayCert, certificateFingerprint(sourceCert))
	source, err := dialTestSource(t, gateway, sourceCert, certificateFingerprint(gatewayCert))
	if err != nil {
		t.Fatalf("DialSource() error = %v", err)
	}
	session := acceptSession(t, gateway)
	packets := syntheticPackets(2)
	injectPlainRTP(t, gateway.Addr(), packets[0])

	if err := source.WriteRTP(packets[1]); err != nil {
		t.Fatalf("WriteRTP() error = %v", err)
	}
	got := readPackets(t, session, 1)[0]

	if got.SequenceNumber != packets[1].SequenceNumber || !bytes.Equal(got.Payload, packets[1].Payload) {
		t.Errorf("ReadRTP() = seq %d, want only the authenticated packet seq %d",
			got.SequenceNumber, packets[1].SequenceNumber)
	}
	assertNoSession(t, gateway)
}

func injectPlainRTP(t *testing.T, gatewayAddr *net.UDPAddr, packet *rtp.Packet) {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, gatewayAddr)
	if err != nil {
		t.Fatalf("DialUDP() error = %v", err)
	}
	defer conn.Close()
	raw, err := packet.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if _, err := conn.Write(raw); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
}

func assertNoSession(t *testing.T, gateway *Gateway) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testRejectWait)
	defer cancel()
	session, err := gateway.Accept(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Gateway.Accept() = %v, %v; want no session and %v", session, err, context.DeadlineExceeded)
	}
}

func TestListenRejectsInvalidConfig(t *testing.T) {
	gatewayCert := newTestCertificate(t, "gateway")
	allowed := certificateFingerprint(newTestCertificate(t, "dj-computer"))
	valid := GatewayConfig{
		Certificate:      gatewayCert,
		AllowedSources:   []Fingerprint{allowed},
		HandshakeTimeout: testHandshakeTimeout,
		MaxSources:       testMaxSources,
		QueuePackets:     testQueuePackets,
	}
	tests := []struct {
		name   string
		mutate func(*GatewayConfig)
	}{
		{"missing certificate", func(c *GatewayConfig) { c.Certificate = tls.Certificate{} }},
		{"no allowed sources", func(c *GatewayConfig) { c.AllowedSources = nil }},
		{"zero handshake timeout", func(c *GatewayConfig) { c.HandshakeTimeout = 0 }},
		{"zero max sources", func(c *GatewayConfig) { c.MaxSources = 0 }},
		{"zero queue", func(c *GatewayConfig) { c.QueuePackets = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)

			gateway, err := Listen(loopback, cfg)

			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("Listen() error = %v, want %v", err, ErrInvalidConfig)
			}
			if gateway != nil {
				t.Errorf("Listen() gateway = %v, want nil", gateway)
				_ = gateway.Close()
			}
		})
	}
}

func TestDialSourceRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  SourceConfig
	}{
		{"missing certificate", SourceConfig{GatewayFingerprint: Fingerprint{1}}},
		{"missing gateway pin", SourceConfig{Certificate: newTestCertificate(t, "dj-computer")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, err := DialSource(context.Background(), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}, tt.cfg)

			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("DialSource() error = %v, want %v", err, ErrInvalidConfig)
			}
			if source != nil {
				t.Errorf("DialSource() source = %v, want nil", source)
			}
		})
	}
}

func TestFingerprintSHA256MatchesDigestOfDER(t *testing.T) {
	der := []byte("certificate-der-bytes")

	got := FingerprintSHA256(der)

	if want := Fingerprint(sha256.Sum256(der)); got != want {
		t.Errorf("FingerprintSHA256() = %x, want %x", got, want)
	}
}
