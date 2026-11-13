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
func subscribe(t *testing.T, server testServer) *webrtc.TrackRemote {
	t.Helper()
	tracks := make(chan *webrtc.TrackRemote, 1)
	callback := lksdk.NewRoomCallback()
	callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
		select {
		case tracks <- track:
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
	case track := <-tracks:
		return track
	case <-time.After(itSubscribeTimeout):
		t.Fatalf("no track subscribed within %v", itSubscribeTimeout)
		return nil
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

func sendPaced(source *ingest.Source) error {
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
	synthetic bool
}

func receive(t *testing.T, track *webrtc.TrackRemote, count int) []receivedPacket {
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

func TestPublisherDeliversIngestPayloadsToSubscriber(t *testing.T) {
	server := startLiveKit(t)
	source, session := startIngest(t)
	publisher, err := Connect(Config{
		URL:       server.url,
		APIKey:    server.apiKey,
		APISecret: server.apiSecret,
		RoomName:  itRoomName,
		Identity:  "gateway",
		TrackName: itTrackName,
	})
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
	track := subscribe(t, server)

	sendDone := make(chan error, 1)
	go func() { sendDone <- sendPaced(source) }()
	received := receive(t, track, itPacketCount)
	if err := <-sendDone; err != nil {
		t.Fatalf("sendPaced() error = %v", err)
	}

	if codec := track.Codec(); codec.Channels != 2 || !strings.Contains(codec.SDPFmtpLine, "stereo=1") {
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
