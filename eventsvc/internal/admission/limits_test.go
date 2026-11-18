package admission

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// sourceSequence hands out distinct source addresses in 10.20.0.0/16.
type sourceSequence struct {
	count int
}

func (s *sourceSequence) next() netip.Addr {
	address := netip.AddrFrom4([4]byte{10, 20, byte(s.count >> 8), byte(s.count)})
	s.count++
	return address
}

func (f fixture) addEvent(t *testing.T, eventID string) {
	t.Helper()
	if err := f.service.AddEvent(Event{ID: eventID, Start: testEventStart, End: testEventEnd}); err != nil {
		t.Fatalf("AddEvent %s: %v", eventID, err)
	}
}

// fillChallenges requests count challenges for one event. Each source
// address takes its full per-source quota before the next one starts.
func (f fixture) fillChallenges(t *testing.T, eventID string, count int, sources *sourceSequence) {
	t.Helper()
	source := sources.next()
	for i := range count {
		if i > 0 && i%ChallengeRateLimit == 0 {
			source = sources.next()
		}
		if _, err := f.service.Challenge(source, eventID); err != nil {
			t.Fatalf("challenge %d for %s: %v", i, eventID, err)
		}
	}
}

func TestChallengeQuotaPerSourceAddress(t *testing.T) {
	f := newFixture(t)
	for i := range ChallengeRateLimit {
		if _, err := f.service.Challenge(testSource, testEventID); err != nil {
			t.Fatalf("challenge %d: %v", i, err)
		}
	}
	if _, err := f.service.Challenge(testSource, testEventID); !errors.Is(err, ErrChallengeRateLimited) {
		t.Fatalf("error = %v, want ErrChallengeRateLimited", err)
	}
	other := netip.MustParseAddr("10.10.0.7")
	if _, err := f.service.Challenge(other, testEventID); err != nil {
		t.Fatalf("challenge from another address: %v", err)
	}
	f.clock.advance(ChallengeRateWindow)
	if _, err := f.service.Challenge(testSource, testEventID); err != nil {
		t.Fatalf("challenge after the window: %v", err)
	}
}

func TestOneSourceCannotExhaustChallengePool(t *testing.T) {
	f := newFixture(t)
	for range MaxOutstandingChallenges {
		// Rejections are expected once the source quota runs out.
		_, _ = f.service.Challenge(testSource, testEventID)
	}
	other := netip.MustParseAddr("10.10.0.7")
	if _, err := f.service.Challenge(other, testEventID); err != nil {
		t.Fatalf("challenge from another address after a flood: %v", err)
	}
}

func TestOutstandingChallengesPerEventAreBounded(t *testing.T) {
	f := newFixture(t)
	f.addEvent(t, "event-2")
	sources := &sourceSequence{}
	f.fillChallenges(t, testEventID, MaxEventChallenges, sources)
	if _, err := f.service.Challenge(sources.next(), testEventID); !errors.Is(err, ErrTooManyChallenges) {
		t.Fatalf("error = %v, want ErrTooManyChallenges", err)
	}
	if _, err := f.service.Challenge(sources.next(), "event-2"); err != nil {
		t.Fatalf("challenge for another event: %v", err)
	}
}

func TestConsumedChallengeReleasesEventSlot(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	f.admit(t, device)
	sources := &sourceSequence{}
	f.fillChallenges(t, testEventID, MaxEventChallenges-1, sources)
	if _, err := f.token(t, device); err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if _, err := f.service.Challenge(sources.next(), testEventID); err != nil {
		t.Fatalf("challenge after one was consumed: %v", err)
	}
}

func TestOpenRequestsPerEventAreBounded(t *testing.T) {
	f := newFixture(t)
	sources := &sourceSequence{}
	first := newTestDevice(t)
	firstReceipt, err := f.service.RequestAdmission(sources.next(), first.admissionRequest(t, testEventID))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	for i := 1; i < MaxOpenRequests; i++ {
		device := newTestDevice(t)
		if _, err := f.service.RequestAdmission(sources.next(), device.admissionRequest(t, testEventID)); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	extra := newTestDevice(t)
	if _, err := f.service.RequestAdmission(sources.next(), extra.admissionRequest(t, testEventID)); !errors.Is(err, ErrTooManyRequests) {
		t.Fatalf("error = %v, want ErrTooManyRequests", err)
	}
	repeat, err := f.service.RequestAdmission(sources.next(), first.admissionRequest(t, testEventID))
	if err != nil {
		t.Fatalf("repeat of an open request at the cap: %v", err)
	}
	if repeat.Code != firstReceipt.Code {
		t.Fatalf("repeat code %q, want %q", repeat.Code, firstReceipt.Code)
	}
	if _, err := f.service.Approve(testEventID, firstReceipt.Code, testApprover); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if _, err := f.service.RequestAdmission(sources.next(), extra.admissionRequest(t, testEventID)); err != nil {
		t.Fatalf("request after approval freed a slot: %v", err)
	}
}

func TestRateLimiterBoundsTrackedAddresses(t *testing.T) {
	limiter := newRateLimiter(RequestRateLimit, RequestRateWindow, MaxTrackedAddresses)
	sources := &sourceSequence{}
	tracked := sources.next()
	if !limiter.allow(tracked, 0) {
		t.Fatalf("first address rejected")
	}
	for i := 1; i < MaxTrackedAddresses; i++ {
		if !limiter.allow(sources.next(), 0) {
			t.Fatalf("address %d rejected below the cap", i)
		}
	}
	if limiter.allow(sources.next(), time.Second) {
		t.Fatalf("new address accepted past the cap")
	}
	if got := len(limiter.history); got > MaxTrackedAddresses {
		t.Fatalf("tracked %d addresses, want at most %d", got, MaxTrackedAddresses)
	}
	if !limiter.allow(tracked, time.Second) {
		t.Fatalf("tracked address rejected at the cap")
	}
	if !limiter.allow(sources.next(), RequestRateWindow+time.Second) {
		t.Fatalf("new address rejected after the window")
	}
}

func TestNewServiceRejectsShortLiveKitSecret(t *testing.T) {
	clock := &fakeClock{}
	cfg := Config{
		Clock:          clock,
		Monotonic:      clock.Monotonic,
		SystemClock:    clock.System,
		MaxClockOffset: testMaxClockOffset,
		Random:         zeroReader{},
		Store:          NewMemoryStore(),
		LiveKitAPIKey:  testAPIKey,
	}
	cfg.LiveKitAPISecret = strings.Repeat("s", MinLiveKitSecretBytes-1)
	if _, err := NewService(cfg); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("short secret: error = %v, want ErrInvalidArgument", err)
	}
	cfg.LiveKitAPISecret = strings.Repeat("s", MinLiveKitSecretBytes)
	if _, err := NewService(cfg); err != nil {
		t.Fatalf("secret at the minimum length: %v", err)
	}
}

func TestInvalidEventDoesNotConsumeChallengeQuota(t *testing.T) {
	f := newFixture(t)
	invalid := map[string]error{
		"missing-event":  ErrUnknownEvent,
		"bad/identifier": ErrInvalidArgument,
	}
	for eventID, want := range invalid {
		for range 2 * ChallengeRateLimit {
			if _, err := f.service.Challenge(testSource, eventID); !errors.Is(err, want) {
				t.Fatalf("challenge for %q: error = %v, want %v", eventID, err, want)
			}
		}
	}
	if tracked := len(f.service.challengeLimiter.history); tracked != 0 {
		t.Fatalf("limiter tracks %d addresses after invalid events, want 0", tracked)
	}
	for i := range ChallengeRateLimit {
		if _, err := f.service.Challenge(testSource, testEventID); err != nil {
			t.Fatalf("valid challenge %d after invalid events: %v", i, err)
		}
	}
}

// assertChallengeAccounting checks that each per-event counter equals the
// live challenges for that event. A drifted counter leaks event slots.
func assertChallengeAccounting(t *testing.T, s *Service, step string) {
	t.Helper()
	live := make(map[string]int)
	for _, entry := range s.challenges {
		live[entry.eventID]++
	}
	if len(live) != len(s.eventChallenges) {
		t.Fatalf("%s: counters %v, live challenges %v", step, s.eventChallenges, live)
	}
	for eventID, count := range live {
		if s.eventChallenges[eventID] != count {
			t.Fatalf("%s: counters %v, live challenges %v", step, s.eventChallenges, live)
		}
	}
}

func TestChallengeAccountingMatchesLiveChallenges(t *testing.T) {
	f := newFixture(t)
	f.addEvent(t, "event-2")
	device := newTestDevice(t)
	f.admit(t, device)
	sources := &sourceSequence{}
	issue := func(eventID string) []byte {
		t.Helper()
		value, err := f.service.Challenge(sources.next(), eventID)
		if err != nil {
			t.Fatalf("Challenge %s: %v", eventID, err)
		}
		return value
	}
	used := issue(testEventID)
	wrongEvent := issue("event-2")
	issue(testEventID)
	issue("event-2")
	assertChallengeAccounting(t, f.service, "after issue")

	if _, err := f.service.IssueToken(device.tokenRequest(t, testEventID, used)); err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	assertChallengeAccounting(t, f.service, "after valid use")

	if _, err := f.service.IssueToken(TokenRequest{EventID: testEventID, Challenge: wrongEvent}); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("wrong-event use: error = %v, want ErrChallengeInvalid", err)
	}
	assertChallengeAccounting(t, f.service, "after wrong-event use")

	if _, err := f.service.IssueToken(TokenRequest{EventID: testEventID, Challenge: used}); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("replay: error = %v, want ErrChallengeInvalid", err)
	}
	assertChallengeAccounting(t, f.service, "after replay")

	f.clock.advance(ChallengeLifetime)
	issue(testEventID)
	assertChallengeAccounting(t, f.service, "after expiry sweep")
	if got := f.service.eventChallenges[testEventID]; got != 1 {
		t.Fatalf("event counter after sweep = %d, want 1", got)
	}
	if _, ok := f.service.eventChallenges["event-2"]; ok {
		t.Fatalf("event-2 counter survived the sweep")
	}
}
