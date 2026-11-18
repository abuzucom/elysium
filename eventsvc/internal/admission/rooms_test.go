package admission

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
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
	// failing lists identities whose removal fails with errRoomService.
	failing map[string]bool
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
	if r.failing[identity] {
		return errRoomService
	}
	return r.err
}

func (r *recordingRooms) setFailing(identity string, failing bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failing == nil {
		r.failing = make(map[string]bool)
	}
	r.failing[identity] = failing
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
	if err := f.service.Revoke(context.Background(), testEventID, admission.DeviceID, admission.ID); err != nil {
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
	if err := f.service.Revoke(context.Background(), testEventID, admission.DeviceID, admission.ID); !errors.Is(err, errRoomService) {
		t.Fatalf("error = %v, want the room service error", err)
	}
	if _, err := f.token(t, device); !errors.Is(err, ErrRevoked) {
		t.Fatalf("token after failed removal: error = %v, want ErrRevoked", err)
	}
	f.rooms.take()
	f.rooms.setErr(nil)
	if err := f.service.Revoke(context.Background(), testEventID, admission.DeviceID, admission.ID); err != nil {
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

func TestExpireAdmissionsSkipsAdmissionReplacedBeforeItsCall(t *testing.T) {
	f := newFixture(t)
	first, second := newTestDevice(t), newTestDevice(t)
	sources := &sourceSequence{}
	ended := []Admission{
		f.admitFrom(t, first, sources.next(), testEventID),
		f.admitFrom(t, second, sources.next(), testEventID),
	}
	devices := map[string]testDevice{ended[0].DeviceID: first, ended[1].DeviceID: second}
	// The sweep handles admissions in device ID order.
	if ended[1].DeviceID < ended[0].DeviceID {
		ended[0], ended[1] = ended[1], ended[0]
	}
	beforeEnd := ended[0].ValidUntil.Add(-time.Hour)
	f.clock.set(synchronizedAt(ended[0].ValidUntil))
	// During the first removal call the clock steps back and staff
	// approve the second device again.
	f.rooms.setDuring(func() {
		f.rooms.setDuring(nil)
		f.clock.set(synchronizedAt(beforeEnd))
		f.admitFrom(t, devices[ended[1].DeviceID], sources.next(), testEventID)
	})
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); err != nil {
		t.Fatalf("ExpireAdmissions: %v", err)
	}
	calls := f.rooms.take()
	if len(calls) != 1 || calls[0].identity != ended[0].DeviceID {
		t.Fatalf("removals = %v, want only %s", calls, ended[0].DeviceID)
	}
}

func TestNewServiceRejectsTypedNilRooms(t *testing.T) {
	clock := &fakeClock{}
	var rooms *LiveKitRooms
	_, err := NewService(Config{
		Clock:            clock,
		Monotonic:        clock.Monotonic,
		SystemClock:      clock.System,
		MaxClockOffset:   testMaxClockOffset,
		Random:           zeroReader{},
		Store:            NewMemoryStore(),
		LiveKitAPIKey:    testAPIKey,
		LiveKitAPISecret: testAPISecret,
		Rooms:            rooms,
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("NewService error = %v, want ErrInvalidArgument", err)
	}
}

func TestNewLiveKitRoomsRejectsAmbiguousURLs(t *testing.T) {
	for _, serverURL := range []string{
		"http://127.0.0.1:7880/?room=x",
		"http://127.0.0.1:7880/#fragment",
		"http://user:pass@127.0.0.1:7880",
	} {
		if _, err := NewLiveKitRooms(serverURL, testAPIKey, testAPISecret, http.DefaultClient); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("NewLiveKitRooms(%q) error = %v, want ErrInvalidArgument", serverURL, err)
		}
	}
	if _, err := NewLiveKitRooms("https://proxy.example/livekit", testAPIKey, testAPISecret, http.DefaultClient); err != nil {
		t.Fatalf("path prefix behind a proxy: %v", err)
	}
}

// requestAndApprove sends a fresh admission request for the device and
// tries to approve it. It returns the receipt and the approval result.
func (f fixture) requestAndApprove(t *testing.T, device testDevice, source netip.Addr) (RequestReceipt, Admission, error) {
	t.Helper()
	receipt, err := f.service.RequestAdmission(source, device.admissionRequest(t, testEventID))
	if err != nil {
		t.Fatalf("RequestAdmission: %v", err)
	}
	admission, err := f.service.Approve(testEventID, receipt.Code, testApprover)
	return receipt, admission, err
}

func TestExpireAdmissionsBlocksReapprovalDuringRemoval(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	expired := f.admit(t, device)
	beforeEnd := expired.ValidUntil.Add(-time.Hour)
	f.clock.set(synchronizedAt(expired.ValidUntil))
	var receipt RequestReceipt
	var duringErr error
	// The venue clock steps back during the removal call, for example on
	// a resync, and staff try to approve the device again.
	f.rooms.setDuring(func() {
		f.rooms.setDuring(nil)
		f.clock.set(synchronizedAt(beforeEnd))
		receipt, _, duringErr = f.requestAndApprove(t, device, testSource)
	})
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); err != nil {
		t.Fatalf("ExpireAdmissions: %v", err)
	}
	if !errors.Is(duringErr, ErrRemovalPending) {
		t.Fatalf("approval during removal: error = %v, want ErrRemovalPending", duringErr)
	}
	replacement, err := f.service.Approve(testEventID, receipt.Code, testApprover)
	if err != nil {
		t.Fatalf("approval after removal: %v", err)
	}
	if replacement.ID == expired.ID || replacement.RoomRemoved {
		t.Fatalf("replacement %+v, want a new unmarked admission", replacement)
	}
}

func TestRevokeBlocksReapprovalDuringRemoval(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	revoked := f.admit(t, device)
	var receipt RequestReceipt
	var duringErr error
	f.rooms.setDuring(func() {
		f.rooms.setDuring(nil)
		receipt, _, duringErr = f.requestAndApprove(t, device, testSource)
	})
	if err := f.service.Revoke(context.Background(), testEventID, revoked.DeviceID, revoked.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !errors.Is(duringErr, ErrRemovalPending) {
		t.Fatalf("approval during removal: error = %v, want ErrRemovalPending", duringErr)
	}
	if calls := f.rooms.take(); len(calls) != 1 {
		t.Fatalf("Revoke made %d removals, want 1", len(calls))
	}
	if _, err := f.service.Approve(testEventID, receipt.Code, testApprover); err != nil {
		t.Fatalf("approval after removal: %v", err)
	}
}

func TestExpireAdmissionsTellsReplacementApartBySameTimestamps(t *testing.T) {
	f := newFixture(t)
	first, second := newTestDevice(t), newTestDevice(t)
	sources := &sourceSequence{}
	ended := []Admission{
		f.admitFrom(t, first, sources.next(), testEventID),
		f.admitFrom(t, second, sources.next(), testEventID),
	}
	devices := map[string]testDevice{ended[0].DeviceID: first, ended[1].DeviceID: second}
	if ended[1].DeviceID < ended[0].DeviceID {
		ended[0], ended[1] = ended[1], ended[0]
	}
	f.clock.set(synchronizedAt(ended[0].ValidUntil))
	// During the first call the venue clock returns to the second
	// admission's approval instant, so its replacement carries the same
	// approval and validity times. Only the admission ID differs.
	f.rooms.setDuring(func() {
		f.rooms.setDuring(nil)
		f.clock.set(synchronizedAt(ended[1].ApprovedAt))
		f.admitFrom(t, devices[ended[1].DeviceID], sources.next(), testEventID)
	})
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); err != nil {
		t.Fatalf("ExpireAdmissions: %v", err)
	}
	if calls := f.rooms.take(); len(calls) != 1 || calls[0].identity != ended[0].DeviceID {
		t.Fatalf("removals = %v, want only %s", calls, ended[0].DeviceID)
	}
	replacement, ok := f.service.cfg.Store.Admission(testEventID, ended[1].DeviceID)
	if !ok || !replacement.ApprovedAt.Equal(ended[1].ApprovedAt) || replacement.ID == ended[1].ID {
		t.Fatalf("test setup: replacement %+v, want same timestamps and a new ID", replacement)
	}
	if replacement.RoomRemoved {
		t.Fatal("replacement admission marked removed")
	}
}

func TestExpireAdmissionsResumesAfterPreviousSweep(t *testing.T) {
	f := newFixture(t)
	admissions := f.admitMany(t, maxRemovalsPerSweep+1)
	f.clock.set(synchronizedAt(admissions[0].ValidUntil))
	f.rooms.setErr(errRoomService)
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); !errors.Is(err, errRoomService) {
		t.Fatalf("first sweep: error = %v, want the room service error", err)
	}
	attempted := map[string]bool{}
	for _, call := range f.rooms.take() {
		attempted[call.identity] = true
	}
	// Retries of earlier failures come first, and the cursor holds back
	// once for failures the retry list cannot keep. Within two more sweeps
	// the sweep must still reach the admission the first never reached.
	reached := false
	for sweep := 2; sweep <= 3 && !reached; sweep++ {
		if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); !errors.Is(err, errRoomService) {
			t.Fatalf("sweep %d: error = %v, want the room service error", sweep, err)
		}
		for _, call := range f.rooms.take() {
			if !attempted[call.identity] {
				reached = true
			}
		}
	}
	if !reached {
		t.Fatal("sweeps 2 and 3 made no call for the admission the first sweep never reached")
	}
}

func TestNewLiveKitRoomsRequiresTLSOffLoopback(t *testing.T) {
	for _, serverURL := range []string{
		"http://10.20.0.1:7880",
		"http://livekit.venue.example",
		"http://[fd00::1]:7880",
	} {
		if _, err := NewLiveKitRooms(serverURL, testAPIKey, testAPISecret, http.DefaultClient); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("NewLiveKitRooms(%q) error = %v, want ErrInvalidArgument", serverURL, err)
		}
	}
	for _, serverURL := range []string{
		"http://127.0.0.1:7880",
		"http://[::1]:7880",
		"http://localhost:7880",
		"https://livekit.venue.example",
	} {
		if _, err := NewLiveKitRooms(serverURL, testAPIKey, testAPISecret, http.DefaultClient); err != nil {
			t.Fatalf("NewLiveKitRooms(%q): %v", serverURL, err)
		}
	}
}

// countingStore records how many admissions each paged read returns.
type countingStore struct {
	*MemoryStore
	mu   sync.Mutex
	read int
}

func (s *countingStore) AdmissionsAfter(eventID, afterDeviceID string, limit int) []Admission {
	page := s.MemoryStore.AdmissionsAfter(eventID, afterDeviceID, limit)
	s.mu.Lock()
	s.read += len(page)
	s.mu.Unlock()
	return page
}

func (s *countingStore) takeRead() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	read := s.read
	s.read = 0
	return read
}

func TestExpireAdmissionsReadsOnePagePerSweep(t *testing.T) {
	store := &countingStore{MemoryStore: NewMemoryStore()}
	f := newFixtureWith(t, rand.Reader, store)
	admissions := f.admitMany(t, maxRemovalsPerSweep+50)
	store.takeRead()
	// No admission has ended yet, so the sweep finds nothing to remove,
	// but it must still read only one page.
	if removed, err := f.service.ExpireAdmissions(context.Background(), testEventID); err != nil || removed != 0 {
		t.Fatalf("removed %d, error %v, want 0 and nil", removed, err)
	}
	if read := store.takeRead(); read > maxRemovalsPerSweep {
		t.Fatalf("sweep read %d admissions, want at most %d", read, maxRemovalsPerSweep)
	}
	// Two sweeps reach every admission once they have ended.
	f.clock.set(synchronizedAt(admissions[0].ValidUntil))
	total := 0
	for range 2 {
		removed, err := f.service.ExpireAdmissions(context.Background(), testEventID)
		if err != nil {
			t.Fatalf("ExpireAdmissions: %v", err)
		}
		total += removed
		if read := store.takeRead(); read > maxRemovalsPerSweep {
			t.Fatalf("sweep read %d admissions, want at most %d", read, maxRemovalsPerSweep)
		}
	}
	if total != len(admissions) {
		t.Fatalf("two sweeps removed %d, want %d", total, len(admissions))
	}
}

func TestRevokeSkipsRemovalAfterSuccess(t *testing.T) {
	f := newFixture(t)
	admission := f.admit(t, newTestDevice(t))
	for range 2 {
		if err := f.service.Revoke(context.Background(), testEventID, admission.DeviceID, admission.ID); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
	}
	if calls := f.rooms.take(); len(calls) != 1 {
		t.Fatalf("two revocations made %d removals, want 1", len(calls))
	}
	stored, _ := f.service.cfg.Store.Admission(testEventID, admission.DeviceID)
	if !stored.Revoked || !stored.RoomRemoved {
		t.Fatalf("stored admission %+v, want revoked and removed", stored)
	}
}

func TestRevokeRefusesReplacedAdmission(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	old := f.admit(t, device)
	replacement := f.admit(t, device)
	err := f.service.Revoke(context.Background(), testEventID, old.DeviceID, old.ID)
	if !errors.Is(err, ErrAdmissionChanged) {
		t.Fatalf("revoking the replaced admission: error = %v, want ErrAdmissionChanged", err)
	}
	if calls := f.rooms.take(); len(calls) != 0 {
		t.Fatalf("stale revocation made removals %v, want none", calls)
	}
	stored, _ := f.service.cfg.Store.Admission(testEventID, device.id())
	if stored.ID != replacement.ID || stored.Revoked {
		t.Fatalf("stored admission %+v, want the unrevoked replacement", stored)
	}
}

func TestExpireAdmissionsRetriesFailedRemovalFirst(t *testing.T) {
	f := newFixture(t)
	admissions := f.admitMany(t, maxRemovalsPerSweep+10)
	slices.SortFunc(admissions, func(a, b Admission) int { return strings.Compare(a.DeviceID, b.DeviceID) })
	failing := admissions[0].DeviceID
	f.clock.set(synchronizedAt(admissions[0].ValidUntil))
	f.rooms.setFailing(failing, true)
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); !errors.Is(err, errRoomService) {
		t.Fatalf("first sweep: error = %v, want the room service error", err)
	}
	f.rooms.take()
	f.rooms.setFailing(failing, false)
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	calls := f.rooms.take()
	if len(calls) == 0 || calls[0].identity != failing {
		t.Fatalf("second sweep starts with %v, want the failed device %s", calls[:min(1, len(calls))], failing)
	}
	if len(calls) > maxRemovalsPerSweep {
		t.Fatalf("second sweep made %d calls, want at most %d", len(calls), maxRemovalsPerSweep)
	}
}

// sortedAdmissions returns the admissions in device ID order, the order
// the sweep reads them.
func sortedAdmissions(admissions []Admission) []Admission {
	sorted := slices.Clone(admissions)
	slices.SortFunc(sorted, func(a, b Admission) int { return strings.Compare(a.DeviceID, b.DeviceID) })
	return sorted
}

func TestExpireAdmissionsKeepsRetryForReplacement(t *testing.T) {
	f := newFixture(t)
	devices := make(map[string]testDevice)
	sources := &sourceSequence{}
	admissions := make([]Admission, 0, maxRemovalsPerSweep+10)
	for range maxRemovalsPerSweep + 10 {
		device := newTestDevice(t)
		admission := f.admitFrom(t, device, sources.next(), testEventID)
		devices[admission.DeviceID] = device
		admissions = append(admissions, admission)
	}
	sorted := sortedAdmissions(admissions)
	failing := sorted[0].DeviceID
	end := sorted[0].ValidUntil
	f.clock.set(synchronizedAt(end))
	f.rooms.setFailing(failing, true)
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); !errors.Is(err, errRoomService) {
		t.Fatalf("first sweep: error = %v, want the room service error", err)
	}
	f.rooms.setFailing(failing, false)
	// Staff revoke the failed admission, which removes the device. The
	// venue clock then steps back and staff approve the device again
	// while it is still on the retry list.
	if err := f.service.Revoke(context.Background(), testEventID, failing, sorted[0].ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	f.clock.set(synchronizedAt(end.Add(-time.Hour)))
	f.admitFrom(t, devices[failing], sources.next(), testEventID)
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	f.rooms.take()
	// Once the replacement ends, its device is retried first.
	f.clock.set(synchronizedAt(end))
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); err != nil {
		t.Fatalf("third sweep: %v", err)
	}
	calls := f.rooms.take()
	if len(calls) == 0 || calls[0].identity != failing {
		t.Fatalf("third sweep starts with %v, want the retried device %s", calls[:min(1, len(calls))], failing)
	}
}

func TestApproveWaitsForFailedRevokeRemoval(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	admission := f.admit(t, device)
	f.rooms.setErr(errRoomService)
	if err := f.service.Revoke(context.Background(), testEventID, admission.DeviceID, admission.ID); !errors.Is(err, errRoomService) {
		t.Fatalf("Revoke: error = %v, want the room service error", err)
	}
	receipt, _, err := f.requestAndApprove(t, device, testSource)
	if !errors.Is(err, ErrRemovalPending) {
		t.Fatalf("approval after a failed removal: error = %v, want ErrRemovalPending", err)
	}
	f.rooms.setErr(nil)
	f.rooms.take()
	// The sweep retries the failed revocation removal.
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); err != nil {
		t.Fatalf("ExpireAdmissions: %v", err)
	}
	if calls := f.rooms.take(); len(calls) != 1 || calls[0].identity != admission.DeviceID {
		t.Fatalf("sweep removals = %v, want the revoked device", calls)
	}
	if _, err := f.service.Approve(testEventID, receipt.Code, testApprover); err != nil {
		t.Fatalf("approval after the retried removal: %v", err)
	}
}

func TestExpireAdmissionsHoldsCursorForRetryOverflow(t *testing.T) {
	f := newFixture(t)
	sorted := sortedAdmissions(f.admitMany(t, maxRemovalsPerSweep+10))
	f.clock.set(synchronizedAt(sorted[0].ValidUntil))
	f.rooms.setErr(errRoomService)
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); !errors.Is(err, errRoomService) {
		t.Fatalf("first sweep: error = %v, want the room service error", err)
	}
	f.rooms.take()
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); !errors.Is(err, errRoomService) {
		t.Fatalf("second sweep: error = %v, want the room service error", err)
	}
	firstDropped := sorted[maxRetriesPerSweep].DeviceID
	for _, call := range f.rooms.take() {
		if call.identity == firstDropped {
			return
		}
	}
	t.Fatalf("second sweep skipped %s, the first failure the retry list could not hold", firstDropped)
}

func TestApproveWaitsForFailedExpiryRemoval(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	admission := f.admit(t, device)
	f.clock.set(synchronizedAt(admission.ValidUntil))
	f.rooms.setErr(errRoomService)
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); !errors.Is(err, errRoomService) {
		t.Fatalf("first sweep: error = %v, want the room service error", err)
	}
	// The venue clock steps back before the event end, so the admission
	// looks valid again. Staff try to approve the device again.
	f.clock.set(synchronizedAt(admission.ValidUntil.Add(-time.Hour)))
	receipt, _, err := f.requestAndApprove(t, device, testSource)
	if !errors.Is(err, ErrRemovalPending) {
		t.Fatalf("approval after a failed expiry removal: error = %v, want ErrRemovalPending", err)
	}
	f.rooms.setErr(nil)
	f.rooms.take()
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if calls := f.rooms.take(); len(calls) != 1 || calls[0].identity != admission.DeviceID {
		t.Fatalf("second sweep removals = %v, want the device whose removal failed", calls)
	}
	if _, err := f.service.Approve(testEventID, receipt.Code, testApprover); err != nil {
		t.Fatalf("approval after the retried removal: %v", err)
	}
}

func TestFailedSweepRemovalBlocksApprovalWithoutGap(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	admission := f.admit(t, device)
	f.clock.set(synchronizedAt(admission.ValidUntil))
	f.rooms.setErr(errRoomService)
	var gapErr error
	// Right after the failed call, the venue clock steps back and staff
	// try to approve the device again.
	f.service.removalFinished = func() {
		f.service.removalFinished = nil
		f.clock.set(synchronizedAt(admission.ValidUntil.Add(-time.Hour)))
		_, _, gapErr = f.requestAndApprove(t, device, testSource)
	}
	if _, err := f.service.ExpireAdmissions(context.Background(), testEventID); !errors.Is(err, errRoomService) {
		t.Fatalf("sweep: error = %v, want the room service error", err)
	}
	if !errors.Is(gapErr, ErrRemovalPending) {
		t.Fatalf("approval right after a failed removal: error = %v, want ErrRemovalPending", gapErr)
	}
}

// nilMapRooms is a map type implementing RoomRemover. Its nil value must
// not pass configuration validation.
type nilMapRooms map[string]bool

func (nilMapRooms) RemoveParticipant(context.Context, string, string) error { return nil }

// nilFuncRooms is a func type implementing RoomRemover.
type nilFuncRooms func(context.Context, string, string) error

func (f nilFuncRooms) RemoveParticipant(ctx context.Context, room, identity string) error {
	return f(ctx, room, identity)
}

func TestNewServiceRejectsTypedNilRoomsOfAnyKind(t *testing.T) {
	var nilMap nilMapRooms
	var nilFunc nilFuncRooms
	for name, rooms := range map[string]RoomRemover{"map": nilMap, "func": nilFunc} {
		clock := &fakeClock{}
		_, err := NewService(Config{
			Clock:            clock,
			Monotonic:        clock.Monotonic,
			SystemClock:      clock.System,
			MaxClockOffset:   testMaxClockOffset,
			Random:           zeroReader{},
			Store:            NewMemoryStore(),
			LiveKitAPIKey:    testAPIKey,
			LiveKitAPISecret: testAPISecret,
			Rooms:            rooms,
		})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("nil %s remover: NewService error = %v, want ErrInvalidArgument", name, err)
		}
	}
}
