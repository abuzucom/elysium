//go:build livekit

// This integration test needs a LiveKit server binary. Run it with:
//
//	LIVEKIT_SERVER_BIN=/path/to/livekit-server go test -tags livekit ./internal/publish/
package publish

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
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/abuzucom/elysium/gateway/internal/ingest"
	"github.com/abuzucom/elysium/gateway/internal/rtptime"
)

// Test-only values. They are not approved production values.
const (
	itPacketCount      = 100 // one second of 10 ms frames
	itFrameSamples     = 480
	itPayloadBytes     = 320
	itOpusPayloadType  = 111
	itStartupTimeout   = 20 * time.Second
	itSubscribeTimeout = 15 * time.Second
	itReceiveTimeout   = 10 * time.Second
	itRoomName         = "elysium-it"
	itTrackName        = "program"
	// itSourceACTID is the one-byte header extension ID the test source
	// uses for abs-capture-time. The value 14 is unlikely to match the
	// ID negotiated with LiveKit, so the test exercises ID translation.
	itSourceACTID = 14
	// itCaptureBaseQ32 is an arbitrary NTP Q32.32 capture instant.
	itCaptureBaseQ32 = uint64(0xe8a1b2c3) << 32
	// itFrameQ32 is 10 ms in Q32.32 seconds, truncated.
	itFrameQ32 = (uint64(1) << 32) / 100
	// itClockOffsetQ32 is -0.25 s in signed Q32.32.
	itClockOffsetQ32 = -(int64(1) << 30)
)

type testServer struct {
	url, apiKey, apiSecret string
}

func freePort(t *testing.T, network string) int {
	t.Helper()
	if network == "udp" {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatalf("ListenUDP() error = %v", err)
		}
		defer conn.Close()
		return conn.LocalAddr().(*net.UDPAddr).Port
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func randomHex(t *testing.T, size int) string {
	t.Helper()
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	return hex.EncodeToString(buf)
}

// startLiveKit runs a loopback-only LiveKit server with a random
// test-only key pair.
func startLiveKit(t *testing.T) testServer {
	t.Helper()
	binary := os.Getenv("LIVEKIT_SERVER_BIN")
	if binary == "" {
		t.Fatal("LIVEKIT_SERVER_BIN must name a livekit-server binary for -tags livekit")
	}
	server := testServer{apiKey: "it" + randomHex(t, 4), apiSecret: randomHex(t, 32)}
	port := freePort(t, "tcp")
	config := fmt.Sprintf(`port: %d
bind_addresses: ["127.0.0.1"]
rtc:
  tcp_port: %d
  udp_port: %d
  node_ip: 127.0.0.1
  use_external_ip: false
keys:
  %s: %s
logging:
  level: warn
`, port, freePort(t, "tcp"), freePort(t, "udp"), server.apiKey, server.apiSecret)
	configPath := filepath.Join(t.TempDir(), "livekit.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var output bytes.Buffer
	cmd := exec.Command(binary, "--config", configPath)
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start livekit-server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("livekit-server output:\n%s", output.String())
		}
	})
	waitForHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/", port))
	server.url = fmt.Sprintf("ws://127.0.0.1:%d", port)
	return server
}

func waitForHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(itStartupTimeout)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("livekit-server not ready at %s within %v", url, itStartupTimeout)
}

func newCertificate(t *testing.T, name string) tls.Certificate {
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

// startIngest returns an authenticated source and its gateway session.
func startIngest(t *testing.T) (*ingest.Source, *ingest.Session) {
	t.Helper()
	gatewayCert := newCertificate(t, "gateway")
	sourceCert := newCertificate(t, "dj-computer")
	gateway, err := ingest.Listen(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, ingest.GatewayConfig{
		Certificate:      gatewayCert,
		AllowedSources:   []ingest.Fingerprint{ingest.FingerprintSHA256(sourceCert.Certificate[0])},
		HandshakeTimeout: 5 * time.Second,
		MaxSources:       1,
		QueuePackets:     512,
	})
	if err != nil {
		t.Fatalf("ingest.Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source, err := ingest.DialSource(ctx, gateway.Addr(), ingest.SourceConfig{
		Certificate:        sourceCert,
		GatewayFingerprint: ingest.FingerprintSHA256(gatewayCert.Certificate[0]),
	})
	if err != nil {
		t.Fatalf("ingest.DialSource() error = %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })
	session, err := gateway.Accept(ctx)
	if err != nil {
		t.Fatalf("Gateway.Accept() error = %v", err)
	}
	return source, session
}

// subscribe joins as a listener and returns the first subscribed track.
type subscription struct {
	track       *webrtc.TrackRemote
	publication *lksdk.RemoteTrackPublication
}

func subscribe(t *testing.T, server testServer) subscription {
	t.Helper()
	tracks := make(chan subscription, 1)
	callback := lksdk.NewRoomCallback()
	callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
		select {
		case tracks <- subscription{track: track, publication: publication}:
		default:
		}
	}
	room, err := lksdk.ConnectToRoom(server.url, lksdk.ConnectInfo{
		APIKey:              server.apiKey,
		APISecret:           server.apiSecret,
		RoomName:            itRoomName,
		ParticipantIdentity: "listener",
	}, callback)
	if err != nil {
		t.Fatalf("subscriber ConnectToRoom() error = %v", err)
	}
	t.Cleanup(room.Disconnect)
	select {
	case sub := <-tracks:
		return sub
	case <-time.After(itSubscribeTimeout):
		t.Fatalf("no track subscribed within %v", itSubscribeTimeout)
		return subscription{}
	}
}

func indexedPayload(index int) []byte {
	payload := make([]byte, itPayloadBytes)
	binary.BigEndian.PutUint32(payload, uint32(index))
	for i := 4; i < len(payload); i++ {
		payload[i] = byte(index*31 + i)
	}
	return payload
}

// testCaptureTime returns the synthetic capture time of packet index.
func testCaptureTime(index int) rtptime.AbsCaptureTime {
	return rtptime.AbsCaptureTime{
		CaptureTimestamp:               itCaptureBaseQ32 + uint64(index)*itFrameQ32,
		EstimatedCaptureClockOffset:    itClockOffsetQ32,
		HasEstimatedCaptureClockOffset: true,
	}
}

// sendPaced sends one packet per 10 ms. A nonzero actID attaches the
// abs-capture-time extension under that one-byte extension ID.
func sendPaced(source *ingest.Source, actID uint8) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; i < itPacketCount; i++ {
		<-ticker.C
		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    itOpusPayloadType,
				SequenceNumber: uint16(i),
				Timestamp:      uint32(i * itFrameSamples),
				SSRC:           0x5eed1234,
			},
			Payload: indexedPayload(i),
		}
		if actID != 0 {
			act, err := testCaptureTime(i).MarshalBinary()
			if err != nil {
				return fmt.Errorf("marshal ACT packet %d: %w", i, err)
			}
			if err := packet.SetExtension(actID, act); err != nil {
				return fmt.Errorf("set ACT packet %d: %w", i, err)
			}
		}
		if err := source.WriteRTP(packet); err != nil {
			return fmt.Errorf("WriteRTP() packet %d: %w", i, err)
		}
	}
	return nil
}

// sfuSilencePrefix starts the Opus silence frame that the LiveKit SFU
// injects on a downtrack (pkg/sfu/downtrack.go, OpusSilenceFrame). The
// SFU sends it when a subscriber attaches before media flows, on mute,
// and on close. These frames are synthetic audio, not program audio.
var sfuSilencePrefix = []byte{0xf8, 0xff, 0xfe}

type receivedPacket struct {
	index     int
	timestamp uint32
	payload   []byte
	act       []byte
	synthetic bool
}

// receive collects count program packets. A nonzero actID records the
// abs-capture-time extension under the subscriber's negotiated ID.
func receive(t *testing.T, track *webrtc.TrackRemote, actID uint8, count int) []receivedPacket {
	t.Helper()
	packets := make(chan receivedPacket, count)
	stop := make(chan struct{})
	defer close(stop)
	// The reader exits when the room disconnects in test cleanup and
	// ReadRTP fails, or when receive returns.
	go func() {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			received := receivedPacket{
				timestamp: packet.Timestamp,
				payload:   append([]byte(nil), packet.Payload...),
				synthetic: bytes.HasPrefix(packet.Payload, sfuSilencePrefix),
			}
			if actID != 0 {
				received.act = append([]byte(nil), packet.GetExtension(actID)...)
			}
			if !received.synthetic && len(packet.Payload) >= 4 {
				received.index = int(binary.BigEndian.Uint32(packet.Payload))
			}
			select {
			case packets <- received:
			case <-stop:
				return
			}
		}
	}()
	program := make([]receivedPacket, 0, count)
	synthetic := 0
	timeout := time.After(itReceiveTimeout)
	for len(program) < count {
		select {
		case packet := <-packets:
			if !packet.synthetic {
				program = append(program, packet)
				continue
			}
			if len(program) > 0 {
				t.Fatalf("SFU silence frame after %d program packets, want none during the program", len(program))
			}
			synthetic++
		case <-timeout:
			t.Fatalf("received %d of %d program packets within %v", len(program), count, itReceiveTimeout)
		}
	}
	t.Logf("SFU injected %d Opus silence frames before the first program packet", synthetic)
	return program
}

// startPipeline runs source, ingest gateway, publisher, LiveKit, and a
// subscriber. It returns the source and the subscription.
func startPipeline(t *testing.T, cfg Config) (*ingest.Source, subscription) {
	t.Helper()
	server := startLiveKit(t)
	source, session := startIngest(t)
	cfg.URL = server.url
	cfg.APIKey = server.apiKey
	cfg.APISecret = server.apiSecret
	cfg.RoomName = itRoomName
	cfg.Identity = "gateway"
	cfg.TrackName = itTrackName
	publisher, err := Connect(cfg)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(publisher.Close)
	ctx, cancel := context.WithCancel(context.Background())
	forwardDone := make(chan error, 1)
	go func() { forwardDone <- Forward(ctx, session, publisher) }()
	t.Cleanup(func() {
		cancel()
		<-forwardDone
	})
	return source, subscribe(t, server)
}

// sendAndReceive sends the paced program and collects it at the
// subscriber.
func sendAndReceive(t *testing.T, source *ingest.Source, sub subscription, sourceACTID, subscriberACTID uint8) []receivedPacket {
	t.Helper()
	sendDone := make(chan error, 1)
	go func() { sendDone <- sendPaced(source, sourceACTID) }()
	received := receive(t, sub.track, subscriberACTID, itPacketCount)
	if err := <-sendDone; err != nil {
		t.Fatalf("sendPaced() error = %v", err)
	}
	return received
}

func TestPublisherDeliversIngestPayloadsToSubscriber(t *testing.T) {
	source, sub := startPipeline(t, Config{})
	received := sendAndReceive(t, source, sub, 0, 0)

	if codec := sub.track.Codec(); codec.Channels != 2 || !strings.Contains(codec.SDPFmtpLine, "stereo=1") {
		t.Errorf("subscriber codec = %+v, want stereo Opus", codec.RTPCodecCapability)
	}
	sentHash, receivedHash := sha256.New(), sha256.New()
	for i, packet := range received {
		if packet.index != i {
			t.Fatalf("packet %d carries index %d, want in-order delivery", i, packet.index)
		}
		if !bytes.Equal(packet.payload, indexedPayload(i)) {
			t.Fatalf("packet %d payload differs from the sent payload", i)
		}
		if i > 0 && packet.timestamp-received[i-1].timestamp != itFrameSamples {
			t.Errorf("packet %d timestamp step = %d, want %d", i,
				packet.timestamp-received[i-1].timestamp, itFrameSamples)
		}
		sentHash.Write(indexedPayload(i))
		receivedHash.Write(packet.payload)
	}
	if !bytes.Equal(sentHash.Sum(nil), receivedHash.Sum(nil)) {
		t.Error("payload SHA-256 differs between source and subscriber")
	}
}

// negotiatedACTID returns the subscriber's abs-capture-time extension ID,
// or zero when the subscriber did not negotiate it.
func negotiatedACTID(sub subscription) uint8 {
	for _, extension := range sub.publication.Receiver().GetParameters().HeaderExtensions {
		if extension.URI == rtptime.AbsCaptureTimeURI {
			return uint8(extension.ID)
		}
	}
	return 0
}

// TestPublisherForwardsAbsCaptureTimeToSubscriber is the Gate 2 ACT
// feasibility check (D-009). LiveKit passes the publisher capture time
// through unchanged unless sender report pass-through is disabled.
func TestPublisherForwardsAbsCaptureTimeToSubscriber(t *testing.T) {
	source, sub := startPipeline(t, Config{SourceACTExtensionID: itSourceACTID})
	subscriberACTID := negotiatedACTID(sub)
	if subscriberACTID == 0 {
		t.Fatal("subscriber did not negotiate abs-capture-time")
	}
	t.Logf("source ACT ID %d, subscriber ACT ID %d", itSourceACTID, subscriberACTID)

	received := sendAndReceive(t, source, sub, itSourceACTID, subscriberACTID)

	for i, packet := range received {
		if packet.index != i {
			t.Fatalf("packet %d carries index %d, want in-order delivery", i, packet.index)
		}
		want, err := testCaptureTime(i).MarshalBinary()
		if err != nil {
			t.Fatalf("MarshalBinary() error = %v", err)
		}
		if !bytes.Equal(packet.act, want) {
			t.Fatalf("packet %d ACT = % x, want % x", i, packet.act, want)
		}
	}
}
