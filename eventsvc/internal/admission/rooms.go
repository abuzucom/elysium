package admission

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/twitchtv/twirp"
)

// adminTokenLifetime bounds the room admin token for one removal call.
// LiveKit checks it against the controller system clock. The value is
// experimental (docs/backlog.md).
const adminTokenLifetime = time.Minute

// RoomRemover ends a participant's session in a LiveKit room. Removing a
// participant that is not in the room succeeds.
type RoomRemover interface {
	RemoveParticipant(ctx context.Context, room, identity string) error
}

// LiveKitRooms removes participants through the LiveKit room service API.
type LiveKitRooms struct {
	client    livekit.RoomService
	apiKey    string
	apiSecret string
}

// NewLiveKitRooms returns a RoomRemover for the LiveKit server at
// serverURL, an http or https base URL. The key pair signs short room
// admin tokens and stays on the venue controller.
func NewLiveKitRooms(serverURL, apiKey, apiSecret string, client *http.Client) (*LiveKitRooms, error) {
	parsed, err := url.Parse(serverURL)
	switch {
	case err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https"):
		return nil, fmt.Errorf("%w: LiveKit server URL must be an http or https base URL", ErrInvalidArgument)
	case apiKey == "":
		return nil, fmt.Errorf("%w: LiveKit API key is required", ErrInvalidArgument)
	case len(apiSecret) < MinLiveKitSecretBytes:
		return nil, fmt.Errorf("%w: LiveKit API secret needs at least %d bytes", ErrInvalidArgument, MinLiveKitSecretBytes)
	case client == nil:
		return nil, fmt.Errorf("%w: HTTP client is required", ErrInvalidArgument)
	}
	return &LiveKitRooms{
		client:    livekit.NewRoomServiceProtobufClient(serverURL, client),
		apiKey:    apiKey,
		apiSecret: apiSecret,
	}, nil
}

// RemoveParticipant removes identity from room. The call carries a room
// admin token scoped to that room only.
func (r *LiveKitRooms) RemoveParticipant(ctx context.Context, room, identity string) error {
	token, err := auth.NewAccessToken(r.apiKey, r.apiSecret).
		SetVideoGrant(&auth.VideoGrant{RoomAdmin: true, Room: room}).
		SetValidFor(adminTokenLifetime).
		ToJWT()
	if err != nil {
		return fmt.Errorf("sign room admin token: %w", err)
	}
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+token)
	ctx, err = twirp.WithHTTPRequestHeaders(ctx, header)
	if err != nil {
		return fmt.Errorf("attach room admin token: %w", err)
	}
	_, err = r.client.RemoveParticipant(ctx, &livekit.RoomParticipantIdentity{Room: room, Identity: identity})
	var twirpErr twirp.Error
	if errors.As(err, &twirpErr) && twirpErr.Code() == twirp.NotFound {
		// The participant already left or never joined.
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove participant: %w", err)
	}
	return nil
}
