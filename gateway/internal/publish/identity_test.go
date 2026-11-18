//go:build livekit

package publish

import (
	"testing"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
)

// Test-only values. They are not approved production values.
const (
	// itListenerIdentity has the shape of an event service device ID, a
	// hex SHA-256 digest of the device public key.
	itListenerIdentity = "5f0c3a8e2b7d4196a0e1c2d3b4a5968778695a4b3c2d1e0f9a8b7c6d5e4f3a2b"
	itObserverIdentity = "observer"
	itDisconnectWait   = 10 * time.Second
	itPresenceWait     = 5 * time.Second
	itPollInterval     = 100 * time.Millisecond
)

// joinAs connects one participant and reports the reason it is
// disconnected.
func joinAs(t *testing.T, server testServer, identity string) (*lksdk.Room, <-chan lksdk.DisconnectionReason) {
	t.Helper()
	reasons := make(chan lksdk.DisconnectionReason, 1)
	callback := lksdk.NewRoomCallback()
	callback.OnDisconnectedWithReason = func(reason lksdk.DisconnectionReason) {
		select {
		case reasons <- reason:
		default:
		}
	}
	room, err := lksdk.ConnectToRoom(server.url, lksdk.ConnectInfo{
		APIKey:              server.apiKey,
		APISecret:           server.apiSecret,
		RoomName:            itRoomName,
		ParticipantIdentity: identity,
	}, callback)
	if err != nil {
		t.Fatalf("ConnectToRoom(%s) error = %v", identity, err)
	}
	t.Cleanup(room.Disconnect)
	return room, reasons
}

// countIdentity returns how many remote participants carry identity.
func countIdentity(room *lksdk.Room, identity string) int {
	count := 0
	for _, participant := range room.GetRemoteParticipants() {
		if participant.Identity() == identity {
			count++
		}
	}
	return count
}

// waitForCount polls until the observer sees want participants with
// identity, and fails the test after itPresenceWait.
func waitForCount(t *testing.T, observer *lksdk.Room, identity string, want int) {
	t.Helper()
	deadline := time.Now().Add(itPresenceWait)
	for {
		got := countIdentity(observer, identity)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("observer sees %d participants with identity %s, want %d", got, identity, want)
		}
		time.Sleep(itPollInterval)
	}
}

// TestDuplicateListenerIdentityDisconnectsEarlierSession checks that a
// shared listener token admits one listener at a time. The event service
// sets the token identity to the device ID. A second join with the same
// identity must end the first session.
func TestDuplicateListenerIdentityDisconnectsEarlierSession(t *testing.T) {
	server := startLiveKit(t)
	observer, _ := joinAs(t, server, itObserverIdentity)
	_, firstReasons := joinAs(t, server, itListenerIdentity)
	waitForCount(t, observer, itListenerIdentity, 1)

	_, secondReasons := joinAs(t, server, itListenerIdentity)

	select {
	case reason := <-firstReasons:
		if reason != lksdk.DuplicateIdentity {
			t.Fatalf("first session disconnect reason = %q, want %q", reason, lksdk.DuplicateIdentity)
		}
	case <-time.After(itDisconnectWait):
		t.Fatalf("first session still connected %v after a duplicate join", itDisconnectWait)
	}
	// The second session must stay connected for a full interval, not
	// only at one instant.
	select {
	case reason := <-secondReasons:
		t.Fatalf("second session disconnected with reason %q, want it to stay", reason)
	case <-time.After(itPresenceWait):
	}
	waitForCount(t, observer, itListenerIdentity, 1)
}
