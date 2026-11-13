package publish

import (
	"context"
	"errors"
	"testing"

	"github.com/pion/rtp"

	"github.com/abuzucom/elysium/gateway/internal/ingest"
)

// fakeReader replays packets and then returns err. It stands in for an
// ingest session, a collaborator of the unit under test.
type fakeReader struct {
	packets []*rtp.Packet
	err     error
}

func (r *fakeReader) ReadRTP(ctx context.Context) (*rtp.Packet, error) {
	if len(r.packets) == 0 {
		return nil, r.err
	}
	packet := r.packets[0]
	r.packets = r.packets[1:]
	return packet, nil
}

// recordingWriter records packets. It stands in for a LiveKit track.
type recordingWriter struct {
	written []*rtp.Packet
	failAt  int
	err     error
}

func (w *recordingWriter) WriteRTP(packet *rtp.Packet) error {
	if w.err != nil && len(w.written) == w.failAt {
		return w.err
	}
	w.written = append(w.written, packet)
	return nil
}

func testPackets(count int) []*rtp.Packet {
	packets := make([]*rtp.Packet, count)
	for i := range packets {
		packets[i] = &rtp.Packet{
			Header:  rtp.Header{Version: 2, SequenceNumber: uint16(i), Timestamp: uint32(i * 480)},
			Payload: []byte{byte(i), 0xa5, byte(255 - i)},
		}
	}
	return packets
}

func TestForwardWritesEveryPacketUntilSourceCloses(t *testing.T) {
	packets := testPackets(3)
	reader := &fakeReader{packets: append([]*rtp.Packet(nil), packets...), err: ingest.ErrClosed}
	writer := &recordingWriter{}

	err := Forward(context.Background(), reader, writer)

	if err != nil {
		t.Fatalf("Forward() error = %v, want nil after source close", err)
	}
	if len(writer.written) != len(packets) {
		t.Fatalf("Forward() wrote %d packets, want %d", len(writer.written), len(packets))
	}
	for i := range packets {
		if writer.written[i] != packets[i] {
			t.Errorf("packet %d = %p, want the same packet %p", i, writer.written[i], packets[i])
		}
	}
}

func TestForwardReturnsWriteError(t *testing.T) {
	writeErr := errors.New("track closed")
	reader := &fakeReader{packets: testPackets(3), err: ingest.ErrClosed}
	writer := &recordingWriter{failAt: 1, err: writeErr}

	err := Forward(context.Background(), reader, writer)

	if !errors.Is(err, writeErr) {
		t.Errorf("Forward() error = %v, want %v", err, writeErr)
	}
	if len(writer.written) != 1 {
		t.Errorf("Forward() wrote %d packets before failure, want 1", len(writer.written))
	}
}

func TestForwardReturnsReadError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &fakeReader{err: ctx.Err()}

	err := Forward(ctx, reader, &recordingWriter{})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Forward() error = %v, want %v", err, context.Canceled)
	}
}

func TestConnectRejectsInvalidConfig(t *testing.T) {
	valid := Config{
		URL:       "ws://127.0.0.1:7880",
		APIKey:    "key",
		APISecret: "secret",
		RoomName:  "event",
		Identity:  "gateway",
		TrackName: "program",
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"missing URL", func(c *Config) { c.URL = "" }},
		{"non-websocket URL", func(c *Config) { c.URL = "http://127.0.0.1:7880" }},
		{"missing API key", func(c *Config) { c.APIKey = "" }},
		{"missing API secret", func(c *Config) { c.APISecret = "" }},
		{"missing room", func(c *Config) { c.RoomName = "" }},
		{"missing identity", func(c *Config) { c.Identity = "" }},
		{"missing track name", func(c *Config) { c.TrackName = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)

			publisher, err := Connect(cfg)

			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("Connect() error = %v, want %v", err, ErrInvalidConfig)
			}
			if publisher != nil {
				t.Errorf("Connect() publisher = %v, want nil", publisher)
			}
		})
	}
}
