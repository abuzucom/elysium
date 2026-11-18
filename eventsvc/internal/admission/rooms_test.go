package admission

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/twitchtv/twirp"
)

// removal is one RemoveParticipant call.
type removal struct {
	room     string
	identity string
}

// recordingRooms is a RoomRemover that records calls and returns err.
// A non-nil during runs inside each call, outside the lock, to model
// work that happens while the room service call is in flight.
type recordingRooms struct {
	mu       sync.Mutex
	err      error
	removals []removal
	during   func()
}

func (r *recordingRooms) RemoveParticipant(_ context.Context, room, identity string) error {
	r.mu.Lock()
	during := r.during
	r.mu.Unlock()
	if during != nil {
		during()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removals = append(r.removals, removal{room: room, identity: identity})
	return r.err
}

func (r *recordingRooms) setDuring(during func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.during = during
}

func (r *recordingRooms) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// take returns the recorded calls and clears them.
func (r *recordingRooms) take() []removal {
	r.mu.Lock()
	defer r.mu.Unlock()
	taken := r.removals
	r.removals = nil
	return taken
}

var errRoomService = errors.New("room service unavailable")

func TestRevokeRemovesParticipantFromRoom(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	admission := f.admit(t, device)
	if err := f.service.Revoke(context.Background(), testEventID, admission.DeviceID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	want := []removal{{room: testEventID, identity: admission.DeviceID}}
	if got := f.rooms.take(); len(got) != 1 || got[0] != want[0] {
		t.Fatalf("removals = %v, want %v", got, want)
	}
}

func TestRevokeReportsRoomRemovalFailure(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	admission := f.admit(t, device)
	f.rooms.setErr(errRoomService)
	if err := f.service.Revoke(context.Background(), testEventID, admission.DeviceID); !errors.Is(err, errRoomService) {
		t.Fatalf("error = %v, want the room service error", err)
	}
	if _, err := f.token(t, device); !errors.Is(err, ErrRevoked) {
		t.Fatalf("token after failed removal: error = %v, want ErrRevoked", err)
	}
	f.rooms.take()
	f.rooms.setErr(nil)
	if err := f.service.Revoke(context.Background(), testEventID, admission.DeviceID); err != nil {
		t.Fatalf("retried Revoke: %v", err)
	}
	if got := f.rooms.take(); len(got) != 1 {
		t.Fatalf("retried Revoke made %d removals, want 1", len(got))
	}
}

func TestExpireAdmissionsRemovesEndedAdmissions(t *testing.T) {
	f := newFixture(t)
	first := f.admit(t, newTestDevice(t))
	second := f.admit(t, newTestDevice(t))
	removed, err := f.service.ExpireAdmissions(context.Background(), testEventID)
	if err != nil || removed != 0 {
		t.Fatalf("before the end: removed %d, error %v, want 0 and nil", removed, err)
	}
	if got := f.rooms.take(); len(got) != 0 {
		t.Fatalf("removals before the end = %v, want none", got)
	}

	f.clock.set(synchronizedAt(first.ValidUntil))
	removed, err = f.service.ExpireAdmissions(context.Background(), testEventID)
	if err != nil || removed != 2 {
		t.Fatalf("at the end: removed %d, error %v, want 2 and nil", removed, err)
	}
	got := map[removal]bool{}
	for _, call := range f.rooms.take() {
		got[call] = true
	}
	for _, admission := range []Admission{first, second} {
		if !got[removal{room: testEventID, identity: admission.DeviceID}] {
			t.Fatalf("device %s not removed, removals %v", admission.DeviceID, got)
		}
	}

	removed, err = f.service.ExpireAdmissions(context.Background(), testEventID)
	if err != nil || removed != 0 {
		t.Fatalf("second sweep: removed %d, error %v, want 0 and nil", removed, err)
	}
	if calls := f.rooms.take(); len(calls) != 0 {
		t.Fatalf("second sweep removals = %v, want none", calls)
	}
}

func TestExpireAdmissionsRetriesFailedRemoval(t *testing.T) {
	f := newFixture(t)
	admission := f.admit(t, newTestDevice(t))
	f.clock.set(synchronizedAt(admission.ValidUntil))
	f.rooms.setErr(errRoomService)
	removed, err := f.service.ExpireAdmissions(context.Background(), testEventID)
	if !errors.Is(err, errRoomService) || removed != 0 {
		t.Fatalf("failed sweep: removed %d, error %v, want 0 and the room service error", removed, err)
	}
	f.rooms.take()
	f.rooms.setErr(nil)
	removed, err = f.service.ExpireAdmissions(context.Background(), testEventID)
	if err != nil || removed != 1 {
		t.Fatalf("retry: removed %d, error %v, want 1 and nil", removed, err)
	}
}

func TestExpireAdmissionsNeedsVenueTime(t *testing.T) {
	f := newFixture(t)
	f.admit(t, newTestDevice(t))
	f.clock.set(VenueTime{})
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); !errors.Is(err, ErrVenueTimeUnavailable) {
		t.Fatalf("error = %v, want ErrVenueTimeUnavailable", err)
	}
	if _, err := f.service.ExpireAdmissions(context.Background(), "missing-event"); !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("unknown event: error = %v, want ErrUnknownEvent", err)
	}
}

// fakeRoomService answers RemoveParticipant and records the request. Any
// other room service method panics through the nil embedded interface.
type fakeRoomService struct {
	livekit.RoomService
	mu      sync.Mutex
	err     error
	request *livekit.RoomParticipantIdentity
}

func (f *fakeRoomService) RemoveParticipant(_ context.Context, request *livekit.RoomParticipantIdentity) (*livekit.RemoveParticipantResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.request = request
	if f.err != nil {
		return nil, f.err
	}
	return &livekit.RemoveParticipantResponse{}, nil
}

// startRoomService serves a fake LiveKit room service and records the
// Authorization header of the last request.
func startRoomService(t *testing.T, service *fakeRoomService) (*httptest.Server, *string) {
	t.Helper()
	handler := livekit.NewRoomServiceServer(service)
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, &authorization
}

func newTestLiveKitRooms(t *testing.T, url string) *LiveKitRooms {
	t.Helper()
	rooms, err := NewLiveKitRooms(url, testAPIKey, testAPISecret, http.DefaultClient)
	if err != nil {
		t.Fatalf("NewLiveKitRooms: %v", err)
	}
	return rooms
}

func TestLiveKitRoomsRemovesParticipantWithRoomAdminToken(t *testing.T) {
	service := &fakeRoomService{}
	server, authorization := startRoomService(t, service)
	rooms := newTestLiveKitRooms(t, server.URL)
	if err := rooms.RemoveParticipant(context.Background(), testEventID, "device-1"); err != nil {
		t.Fatalf("RemoveParticipant: %v", err)
	}
	if service.request == nil || service.request.Room != testEventID || service.request.Identity != "device-1" {
		t.Fatalf("request = %v, want room %s identity device-1", service.request, testEventID)
	}
	const prefix = "Bearer "
	if len(*authorization) <= len(prefix) || (*authorization)[:len(prefix)] != prefix {
		t.Fatalf("authorization header %q, want a bearer token", *authorization)
	}
	verifier, err := auth.ParseAPIToken((*authorization)[len(prefix):])
	if err != nil {
		t.Fatalf("ParseAPIToken: %v", err)
	}
	if verifier.APIKey() != testAPIKey {
		t.Fatalf("api key %q, want %q", verifier.APIKey(), testAPIKey)
	}
	_, grants, err := verifier.Verify(testAPISecret)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if grants.Video == nil || !grants.Video.RoomAdmin || grants.Video.Room != testEventID {
		t.Fatalf("video grant %+v, want room admin for %s", grants.Video, testEventID)
	}
	if grants.Video.RoomJoin || grants.Video.RoomCreate || grants.Video.RoomList {
		t.Fatalf("video grant %+v carries more than room admin", grants.Video)
	}
}

func TestLiveKitRoomsTreatsMissingParticipantAsRemoved(t *testing.T) {
	service := &fakeRoomService{err: twirp.NotFoundError("participant not found")}
	server, _ := startRoomService(t, service)
	rooms := newTestLiveKitRooms(t, server.URL)
	if err := rooms.RemoveParticipant(context.Background(), testEventID, "device-1"); err != nil {
		t.Fatalf("RemoveParticipant for an absent participant: %v", err)
	}
}

func TestLiveKitRoomsReportsOtherErrors(t *testing.T) {
	service := &fakeRoomService{err: twirp.InternalError("boom")}
	server, _ := startRoomService(t, service)
	rooms := newTestLiveKitRooms(t, server.URL)
	if err := rooms.RemoveParticipant(context.Background(), testEventID, "device-1"); err == nil {
		t.Fatal("RemoveParticipant returned nil for a server error")
	}
}

func TestNewLiveKitRoomsValidatesConfiguration(t *testing.T) {
	cases := map[string]struct {
		url, key, secret string
		client           *http.Client
	}{
		"url":          {"", testAPIKey, testAPISecret, http.DefaultClient},
		"scheme":       {"ws://127.0.0.1:7880", testAPIKey, testAPISecret, http.DefaultClient},
		"key":          {"http://127.0.0.1:7880", "", testAPISecret, http.DefaultClient},
		"short secret": {"http://127.0.0.1:7880", testAPIKey, "short", http.DefaultClient},
		"client":       {"http://127.0.0.1:7880", testAPIKey, testAPISecret, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewLiveKitRooms(c.url, c.key, c.secret, c.client); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestExpireAdmissionsKeepsReplacementAdmission(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	expired := f.admit(t, device)
	beforeEnd := expired.ValidUntil.Add(-time.Hour)
	f.clock.set(synchronizedAt(expired.ValidUntil))
	// The venue clock steps back during the removal call, for example on
	// a resync. Staff approve the device again in that window.
	f.rooms.setDuring(func() {
		f.rooms.setDuring(nil)
		f.clock.set(synchronizedAt(beforeEnd))
		f.admitFrom(t, device, testSource, testEventID)
	})
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); err != nil {
		t.Fatalf("ExpireAdmissions: %v", err)
	}
	replacement, ok := f.service.cfg.Store.Admission(testEventID, expired.DeviceID)
	if !ok {
		t.Fatal("replacement admission missing")
	}
	if replacement.ApprovedAt.Equal(expired.ApprovedAt) {
		t.Fatal("replacement admission was not stored")
	}
	if replacement.RoomRemoved {
		t.Fatal("expiry marked the replacement admission as removed")
	}
}

func TestLiveKitRoomsTimesOutStalledServer(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	rooms := newTestLiveKitRooms(t, server.URL)
	rooms.timeout = 50 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- rooms.RemoveParticipant(context.Background(), testEventID, "device-1") }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RemoveParticipant still blocked on a stalled server")
	}
}

func TestNewLiveKitRoomsBoundsEachCall(t *testing.T) {
	rooms := newTestLiveKitRooms(t, "http://127.0.0.1:7880")
	if rooms.timeout != roomServiceTimeout || roomServiceTimeout <= 0 {
		t.Fatalf("timeout = %v, want the positive roomServiceTimeout %v", rooms.timeout, roomServiceTimeout)
	}
}

// admitMany admits count new devices to the test event from distinct
// source addresses.
func (f fixture) admitMany(t *testing.T, count int) []Admission {
	t.Helper()
	sources := &sourceSequence{}
	admissions := make([]Admission, count)
	for i := range admissions {
		admissions[i] = f.admitFrom(t, newTestDevice(t), sources.next(), testEventID)
	}
	return admissions
}

func TestExpireAdmissionsStopsAfterTimeout(t *testing.T) {
	f := newFixture(t)
	admissions := f.admitMany(t, 3)
	f.clock.set(synchronizedAt(admissions[0].ValidUntil))
	f.rooms.setErr(context.DeadlineExceeded)
	removed, err := f.service.ExpireAdmissions(context.Background(), testEventID)
	if !errors.Is(err, context.DeadlineExceeded) || removed != 0 {
		t.Fatalf("removed %d, error %v, want 0 and context.DeadlineExceeded", removed, err)
	}
	if calls := f.rooms.take(); len(calls) != 1 {
		t.Fatalf("sweep made %d room calls after a timeout, want 1", len(calls))
	}
}

func TestExpireAdmissionsContinuesAfterOtherFailures(t *testing.T) {
	f := newFixture(t)
	admissions := f.admitMany(t, 3)
	f.clock.set(synchronizedAt(admissions[0].ValidUntil))
	f.rooms.setErr(errRoomService)
	removed, err := f.service.ExpireAdmissions(context.Background(), testEventID)
	if !errors.Is(err, errRoomService) || removed != 0 {
		t.Fatalf("removed %d, error %v, want 0 and the room service error", removed, err)
	}
	if calls := f.rooms.take(); len(calls) != 3 {
		t.Fatalf("sweep made %d room calls, want 3", len(calls))
	}
	if !strings.Contains(err.Error(), "3 of 3") {
		t.Fatalf("error %q does not report the failure count", err)
	}
	if strings.Count(err.Error(), errRoomService.Error()) != 1 {
		t.Fatalf("error %q repeats each failure, want one sample", err)
	}
}

func TestExpireAdmissionsBoundsOneSweep(t *testing.T) {
	f := newFixture(t)
	admissions := f.admitMany(t, maxRemovalsPerSweep+1)
	f.clock.set(synchronizedAt(admissions[0].ValidUntil))
	removed, err := f.service.ExpireAdmissions(context.Background(), testEventID)
	if err != nil || removed != maxRemovalsPerSweep {
		t.Fatalf("first sweep: removed %d, error %v, want %d and nil", removed, err, maxRemovalsPerSweep)
	}
	removed, err = f.service.ExpireAdmissions(context.Background(), testEventID)
	if err != nil || removed != 1 {
		t.Fatalf("second sweep: removed %d, error %v, want 1 and nil", removed, err)
	}
}
