// Package publish forwards authenticated ingest RTP into a self-hosted
// LiveKit room without transcoding (D-004, D-007).
package publish

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/abuzucom/elysium/gateway/internal/ingest"
)

// ErrInvalidConfig reports an unusable publisher configuration.
var ErrInvalidConfig = errors.New("publish: invalid configuration")

// opusStereoCodec describes the 48 kHz stereo Opus program feed. Opus RTP
// always signals two channels. The fmtp parameters request stereo
// decoding and declare a stereo source (RFC 7587).
var opusStereoCodec = webrtc.RTPCodecCapability{
	MimeType:    webrtc.MimeTypeOpus,
	ClockRate:   48000,
	Channels:    2,
	SDPFmtpLine: "minptime=10;useinbandfec=1;stereo=1;sprop-stereo=1",
}

// RTPReader yields RTP packets. An ingest session satisfies it.
type RTPReader interface {
	ReadRTP(ctx context.Context) (*rtp.Packet, error)
}

// RTPWriter accepts RTP packets. A Publisher satisfies it.
type RTPWriter interface {
	WriteRTP(packet *rtp.Packet) error
}

// Forward copies packets from reader to writer until the reader ends.
// It returns nil when the ingest session closes, and the first other
// read or write error otherwise.
func Forward(ctx context.Context, reader RTPReader, writer RTPWriter) error {
	for {
		packet, err := reader.ReadRTP(ctx)
		if errors.Is(err, ingest.ErrClosed) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("publish: read ingest RTP: %w", err)
		}
		if err := writer.WriteRTP(packet); err != nil {
			return fmt.Errorf("publish: write LiveKit RTP: %w", err)
		}
	}
}

// Config configures a LiveKit publisher. APISecret is a credential and
// must never be logged.
type Config struct {
	// URL is the LiveKit signaling endpoint with a ws or wss scheme.
	URL       string
	APIKey    string
	APISecret string
	RoomName  string
	// Identity names the gateway participant in the room.
	Identity string
	// TrackName labels the published program track.
	TrackName string
}

func (cfg Config) validate() error {
	parsed, err := url.Parse(cfg.URL)
	switch {
	case cfg.URL == "" || err != nil:
		return fmt.Errorf("%w: LiveKit URL required", ErrInvalidConfig)
	case parsed.Scheme != "ws" && parsed.Scheme != "wss":
		return fmt.Errorf("%w: LiveKit URL scheme must be ws or wss", ErrInvalidConfig)
	case cfg.APIKey == "" || cfg.APISecret == "":
		return fmt.Errorf("%w: API key and secret required", ErrInvalidConfig)
	case cfg.RoomName == "":
		return fmt.Errorf("%w: room name required", ErrInvalidConfig)
	case cfg.Identity == "":
		return fmt.Errorf("%w: participant identity required", ErrInvalidConfig)
	case cfg.TrackName == "":
		return fmt.Errorf("%w: track name required", ErrInvalidConfig)
	}
	return nil
}

// Publisher publishes one stereo Opus program track into a LiveKit room.
type Publisher struct {
	room  *lksdk.Room
	track *webrtc.TrackLocalStaticRTP
}

// Connect joins the room as the gateway participant and publishes the
// program track with DTX disabled.
func Connect(cfg Config) (*Publisher, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	track, err := webrtc.NewTrackLocalStaticRTP(opusStereoCodec, cfg.TrackName, cfg.Identity)
	if err != nil {
		return nil, fmt.Errorf("publish: create track: %w", err)
	}
	room, err := lksdk.ConnectToRoom(cfg.URL, lksdk.ConnectInfo{
		APIKey:              cfg.APIKey,
		APISecret:           cfg.APISecret,
		RoomName:            cfg.RoomName,
		ParticipantIdentity: cfg.Identity,
	}, lksdk.NewRoomCallback())
	if err != nil {
		return nil, fmt.Errorf("publish: connect to room %q: %w", cfg.RoomName, err)
	}
	_, err = room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:       cfg.TrackName,
		Stereo:     true,
		DisableDTX: true,
	})
	if err != nil {
		room.Disconnect()
		return nil, fmt.Errorf("publish: publish track %q: %w", cfg.TrackName, err)
	}
	return &Publisher{room: room, track: track}, nil
}

// WriteRTP forwards one packet. The payload, sequence number, and
// timestamp pass through unchanged. The track binding sets the
// negotiated SSRC and payload type.
func (p *Publisher) WriteRTP(packet *rtp.Packet) error {
	return p.track.WriteRTP(packet)
}

// Close leaves the room.
func (p *Publisher) Close() {
	p.room.Disconnect()
}
