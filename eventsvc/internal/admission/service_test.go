package admission

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
)

var sixDigits = regexp.MustCompile(`^[0-9]{6}$`)

func TestNewServiceRejectsMissingConfiguration(t *testing.T) {
	clock := &fakeClock{}
	valid := Config{
		Clock:            clock,
		Monotonic:        clock.Monotonic,
		SystemClock:      clock.System,
		MaxClockOffset:   testMaxClockOffset,
		Random:           zeroReader{},
		Store:            NewMemoryStore(),
		LiveKitAPIKey:    testAPIKey,
		LiveKitAPISecret: testAPISecret,
		Rooms:            &recordingRooms{},
	}
	cases := map[string]func(*Config){
		"clock":     func(c *Config) { c.Clock = nil },
		"monotonic": func(c *Config) { c.Monotonic = nil },
		"system":    func(c *Config) { c.SystemClock = nil },
		"offset":    func(c *Config) { c.MaxClockOffset = 0 },
		"random":    func(c *Config) { c.Random = nil },
		"store":     func(c *Config) { c.Store = nil },
		"api key":   func(c *Config) { c.LiveKitAPIKey = "" },
		"secret":    func(c *Config) { c.LiveKitAPISecret = "" },
		"rooms":     func(c *Config) { c.Rooms = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			mutate(&cfg)
			if _, err := NewService(cfg); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("NewService error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestAddEventValidatesGrace(t *testing.T) {
	f := newFixture(t)
	tooLong := 6*time.Hour + time.Second
	negative := -time.Second
	cases := map[string]Event{
		"grace over maximum": {ID: "e2", Start: testEventStart, End: testEventEnd, Grace: &tooLong},
		"negative grace":     {ID: "e3", Start: testEventStart, End: testEventEnd, Grace: &negative},
		"end before start":   {ID: "e4", Start: testEventEnd, End: testEventStart},
		"bad identifier":     {ID: "room/../x", Start: testEventStart, End: testEventEnd},
		"duplicate":          {ID: testEventID, Start: testEventStart, End: testEventEnd},
	}
	for name, event := range cases {
		t.Run(name, func(t *testing.T) {
			if err := f.service.AddEvent(event); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("AddEvent error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestRequestAdmissionReturnsSixDigitCode(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	receipt, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, testEventID))
	if err != nil {
		t.Fatalf("RequestAdmission: %v", err)
	}
	if !sixDigits.MatchString(receipt.Code) {
		t.Fatalf("code %q is not six digits", receipt.Code)
	}
	if receipt.DeviceID != DeviceID(device.der) {
		t.Fatalf("device id %q, want %q", receipt.DeviceID, DeviceID(device.der))
	}
	if receipt.ExpiresIn != RequestLifetime {
		t.Fatalf("expires in %v, want %v", receipt.ExpiresIn, RequestLifetime)
	}
}

func TestRequestAdmissionRejectsBadSignature(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	other := newTestDevice(t)
	request := device.admissionRequest(t, testEventID)
	request.Signature = other.sign(t, RequestMessage(testEventID, device.der))
	if _, err := f.service.RequestAdmission(testSource, request); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("error = %v, want ErrBadSignature", err)
	}
}

func TestRequestAdmissionRejectsSignatureForOtherEvent(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	request := device.admissionRequest(t, "event-2")
	request.EventID = testEventID
	if _, err := f.service.RequestAdmission(testSource, request); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("error = %v, want ErrBadSignature", err)
	}
}

func TestRequestAdmissionRejectsUnknownEvent(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	if _, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, "missing")); !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("error = %v, want ErrUnknownEvent", err)
	}
}

func TestRequestAdmissionRejectsNonP256Key(t *testing.T) {
	f := newFixture(t)
	request := SignedRequest{EventID: testEventID, PublicKey: []byte("not a key"), Signature: []byte{1}}
	if _, err := f.service.RequestAdmission(testSource, request); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument", err)
	}
}

func TestOneOpenRequestPerDevice(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	first, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, testEventID))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	f.clock.advance(time.Minute)
	second, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, testEventID))
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	if second.Code != first.Code {
		t.Fatalf("second code %q, want the open request code %q", second.Code, first.Code)
	}
	if want := RequestLifetime - time.Minute; second.ExpiresIn != want {
		t.Fatalf("second expires in %v, want %v", second.ExpiresIn, want)
	}
}

func TestCodesStayUniqueAmongOpenRequests(t *testing.T) {
	f := newFixtureWithRandom(t, zeroReader{})
	first := newTestDevice(t)
	if _, err := f.service.RequestAdmission(testSource, first.admissionRequest(t, testEventID)); err != nil {
		t.Fatalf("first request: %v", err)
	}
	second := newTestDevice(t)
	_, err := f.service.RequestAdmission(netip.MustParseAddr("10.10.0.6"), second.admissionRequest(t, testEventID))
	if !errors.Is(err, ErrCodeSpaceExhausted) {
		t.Fatalf("error = %v, want ErrCodeSpaceExhausted", err)
	}
}

func TestExpiredRequestReleasesItsCode(t *testing.T) {
	f := newFixtureWithRandom(t, zeroReader{})
	first := newTestDevice(t)
	if _, err := f.service.RequestAdmission(testSource, first.admissionRequest(t, testEventID)); err != nil {
		t.Fatalf("first request: %v", err)
	}
	f.clock.advance(RequestLifetime)
	second := newTestDevice(t)
	if _, err := f.service.RequestAdmission(netip.MustParseAddr("10.10.0.6"), second.admissionRequest(t, testEventID)); err != nil {
		t.Fatalf("request after expiry: %v", err)
	}
}

func TestRateLimitPerSourceAddress(t *testing.T) {
	f := newFixture(t)
	for i := range RequestRateLimit {
		device := newTestDevice(t)
		if _, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, testEventID)); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	device := newTestDevice(t)
	if _, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, testEventID)); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
	other := netip.MustParseAddr("10.10.0.7")
	if _, err := f.service.RequestAdmission(other, device.admissionRequest(t, testEventID)); err != nil {
		t.Fatalf("request from another address: %v", err)
	}
	f.clock.advance(RequestRateWindow)
	late := newTestDevice(t)
	if _, err := f.service.RequestAdmission(testSource, late.admissionRequest(t, testEventID)); err != nil {
		t.Fatalf("request after the window: %v", err)
	}
}

func TestApproveCreatesAdmission(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	approvedAt := f.clock.Now().UTC
	admission := f.admit(t, device)
	if admission.Method != MethodStaffApproval {
		t.Fatalf("method %v, want staff approval", admission.Method)
	}
	if admission.ApproverID != testApprover {
		t.Fatalf("approver %q, want %q", admission.ApproverID, testApprover)
	}
	if !admission.ApprovedAt.Equal(approvedAt) {
		t.Fatalf("approved at %v, want venue UTC %v", admission.ApprovedAt, approvedAt)
	}
	if want := testEventEnd.Add(DefaultGrace); !admission.ValidUntil.Equal(want) {
		t.Fatalf("valid until %v, want %v", admission.ValidUntil, want)
	}
}

func TestApproveUsesEventGrace(t *testing.T) {
	f := newFixture(t)
	grace := 6 * time.Hour
	if err := f.service.AddEvent(Event{ID: "event-2", Start: testEventStart, End: testEventEnd, Grace: &grace}); err != nil {
		t.Fatalf("AddEvent: %v", err)
	}
	device := newTestDevice(t)
	receipt, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, "event-2"))
	if err != nil {
		t.Fatalf("RequestAdmission: %v", err)
	}
	admission, err := f.service.Approve("event-2", receipt.Code, testApprover)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if want := testEventEnd.Add(grace); !admission.ValidUntil.Equal(want) {
		t.Fatalf("valid until %v, want %v", admission.ValidUntil, want)
	}
}

func TestApproveConsumesTheRequest(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	receipt, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, testEventID))
	if err != nil {
		t.Fatalf("RequestAdmission: %v", err)
	}
	if _, err := f.service.Approve(testEventID, receipt.Code, testApprover); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if _, err := f.service.Approve(testEventID, receipt.Code, testApprover); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("second approve error = %v, want ErrRequestNotFound", err)
	}
}

func TestApproveRejectsExpiredRequest(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	receipt, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, testEventID))
	if err != nil {
		t.Fatalf("RequestAdmission: %v", err)
	}
	f.clock.advance(RequestLifetime)
	if _, err := f.service.Approve(testEventID, receipt.Code, testApprover); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("error = %v, want ErrRequestNotFound", err)
	}
}

func TestApproveRequiresApprover(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	receipt, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, testEventID))
	if err != nil {
		t.Fatalf("RequestAdmission: %v", err)
	}
	if _, err := f.service.Approve(testEventID, receipt.Code, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v, want ErrInvalidArgument", err)
	}
}

func TestApproveRefusesWithoutVenueTime(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	receipt, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, testEventID))
	if err != nil {
		t.Fatalf("RequestAdmission: %v", err)
	}
	f.clock.set(VenueTime{State: SyncStateUnsynchronized})
	if _, err := f.service.Approve(testEventID, receipt.Code, testApprover); !errors.Is(err, ErrVenueTimeUnavailable) {
		t.Fatalf("error = %v, want ErrVenueTimeUnavailable", err)
	}
}

func TestApproveRefusesAfterAdmissionWindow(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	receipt, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, testEventID))
	if err != nil {
		t.Fatalf("RequestAdmission: %v", err)
	}
	f.clock.set(synchronizedAt(testEventEnd.Add(DefaultGrace)))
	if _, err := f.service.Approve(testEventID, receipt.Code, testApprover); !errors.Is(err, ErrAdmissionExpired) {
		t.Fatalf("error = %v, want ErrAdmissionExpired", err)
	}
}

func TestIssueTokenGrantsSubscribeOnly(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	f.admit(t, device)
	token, err := f.token(t, device)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if token.ValidFor != TokenLifetime {
		t.Fatalf("valid for %v, want %v", token.ValidFor, TokenLifetime)
	}
	if token.RefreshAfter != TokenRefreshAfter {
		t.Fatalf("refresh after %v, want %v", token.RefreshAfter, TokenRefreshAfter)
	}
	verifier, err := auth.ParseAPIToken(token.JWT)
	if err != nil {
		t.Fatalf("ParseAPIToken: %v", err)
	}
	if verifier.APIKey() != testAPIKey || verifier.Identity() != DeviceID(device.der) {
		t.Fatalf("api key %q identity %q", verifier.APIKey(), verifier.Identity())
	}
	claims := mustDecodeToken(t, token.JWT, f.clock.System())
	if lifetime := claims.ExpiresAt.Sub(claims.NotBefore.Time); lifetime != TokenLifetime {
		t.Fatalf("jwt lifetime %v, want %v", lifetime, TokenLifetime)
	}
	if claims.Identity != DeviceID(device.der) {
		t.Fatalf("identity %q, want device id", claims.Identity)
	}
	video := claims.Video
	if video == nil || !video.RoomJoin || video.Room != testEventID {
		t.Fatalf("video grant %+v does not join only the event room", video)
	}
	if video.RoomCreate || video.RoomList || video.RoomAdmin || video.RoomRecord {
		t.Fatalf("video grant %+v carries room administration", video)
	}
	if !video.GetCanSubscribe() {
		t.Fatal("token cannot subscribe")
	}
	if video.GetCanPublish() || video.GetCanPublishData() || video.GetCanUpdateOwnMetadata() {
		t.Fatalf("token can publish: %+v", video)
	}
}

func TestIssueTokenRequiresAdmission(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	if _, err := f.token(t, device); !errors.Is(err, ErrNoAdmission) {
		t.Fatalf("error = %v, want ErrNoAdmission", err)
	}
}

func TestIssueTokenRejectsBadSignature(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	f.admit(t, device)
	challenge, err := f.challenge(t, device, testSource, testEventID)
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	other := newTestDevice(t)
	request := device.tokenRequest(t, testEventID, challenge)
	request.Signature = other.sign(t, SessionMessage(testEventID, challenge))
	if _, err := f.service.IssueToken(request); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("error = %v, want ErrBadSignature", err)
	}
}

func TestChallengeIsSingleUse(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	f.admit(t, device)
	challenge, err := f.challenge(t, device, testSource, testEventID)
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	request := device.tokenRequest(t, testEventID, challenge)
	if _, err := f.service.IssueToken(request); err != nil {
		t.Fatalf("first IssueToken: %v", err)
	}
	if _, err := f.service.IssueToken(request); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("replay error = %v, want ErrChallengeInvalid", err)
	}
}

func TestChallengeExpires(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	f.admit(t, device)
	challenge, err := f.challenge(t, device, testSource, testEventID)
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	f.clock.advance(ChallengeLifetime)
	if _, err := f.service.IssueToken(device.tokenRequest(t, testEventID, challenge)); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("error = %v, want ErrChallengeInvalid", err)
	}
}

func TestChallengeIsBoundToEvent(t *testing.T) {
	f := newFixture(t)
	grace := DefaultGrace
	if err := f.service.AddEvent(Event{ID: "event-2", Start: testEventStart, End: testEventEnd, Grace: &grace}); err != nil {
		t.Fatalf("AddEvent: %v", err)
	}
	device := newTestDevice(t)
	f.admit(t, device)
	f.admitFrom(t, device, testSource, "event-2")
	challenge, err := f.challenge(t, device, testSource, "event-2")
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	if _, err := f.service.IssueToken(device.tokenRequest(t, testEventID, challenge)); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("error = %v, want ErrChallengeInvalid", err)
	}
}

func TestIssueTokenRefusesRevokedAdmission(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	admission := f.admit(t, device)
	if err := f.service.Revoke(context.Background(), testEventID, admission.DeviceID, admission.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := f.token(t, device); !errors.Is(err, ErrRevoked) {
		t.Fatalf("error = %v, want ErrRevoked", err)
	}
}

func TestIssueTokenRefusesWhenUnsynchronized(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	f.admit(t, device)
	f.clock.set(VenueTime{State: SyncStateUnsynchronized})
	if _, err := f.token(t, device); !errors.Is(err, ErrVenueTimeUnavailable) {
		t.Fatalf("error = %v, want ErrVenueTimeUnavailable", err)
	}
}

func TestIssueTokenShortensLifetimeInHoldover(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	f.admit(t, device)
	now := f.clock.Now().UTC
	f.clock.set(VenueTime{UTC: now, Uncertainty: 90 * time.Second, State: SyncStateHoldover})
	token, err := f.token(t, device)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if want := TokenLifetime - 90*time.Second; token.ValidFor != want {
		t.Fatalf("valid for %v, want %v", token.ValidFor, want)
	}
}

func TestIssueTokenRefusesWhenHoldoverUncertaintyConsumesLifetime(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	f.admit(t, device)
	now := f.clock.Now().UTC
	f.clock.set(VenueTime{UTC: now, Uncertainty: TokenLifetime, State: SyncStateHoldover})
	if _, err := f.token(t, device); !errors.Is(err, ErrVenueTimeUnavailable) {
		t.Fatalf("error = %v, want ErrVenueTimeUnavailable", err)
	}
}

func TestIssueTokenEndsAtAdmissionValidity(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	admission := f.admit(t, device)
	f.clock.set(synchronizedAt(admission.ValidUntil.Add(-20 * time.Minute)))
	token, err := f.token(t, device)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	// The latest possible venue time includes the uncertainty. Lifetimes
	// truncate to whole seconds for the JWT expiry claim.
	if want := 20*time.Minute - time.Second; token.ValidFor != want {
		t.Fatalf("valid for %v, want %v", token.ValidFor, want)
	}
	if want := token.ValidFor / 2; token.RefreshAfter != want {
		t.Fatalf("refresh after %v, want %v", token.RefreshAfter, want)
	}
}

func TestIssueTokenRefusesExpiredAdmission(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	admission := f.admit(t, device)
	f.clock.set(synchronizedAt(admission.ValidUntil))
	if _, err := f.token(t, device); !errors.Is(err, ErrAdmissionExpired) {
		t.Fatalf("error = %v, want ErrAdmissionExpired", err)
	}
}

func TestOutstandingChallengesAreBounded(t *testing.T) {
	f := newFixture(t)
	f.addEvent(t, "event-2")
	f.addEvent(t, "event-3")
	sources := &sourceSequence{}
	// The per-event cap stops one event short of the pool. Two events
	// together fill it.
	f.fillChallenges(t, testEventID, MaxEventChallenges, sources)
	f.fillChallenges(t, "event-2", MaxOutstandingChallenges-MaxEventChallenges, sources)
	late := newTestDevice(t)
	f.admitFrom(t, late, sources.next(), "event-3")
	if _, err := f.challenge(t, late, sources.next(), "event-3"); !errors.Is(err, ErrTooManyChallenges) {
		t.Fatalf("error = %v, want ErrTooManyChallenges", err)
	}
	f.clock.advance(ChallengeLifetime)
	if _, err := f.challenge(t, late, sources.next(), "event-3"); err != nil {
		t.Fatalf("challenge after expiry: %v", err)
	}
}

func TestRevokeRequiresAdmission(t *testing.T) {
	f := newFixture(t)
	if err := f.service.Revoke(context.Background(), testEventID, "missing", ""); !errors.Is(err, ErrNoAdmission) {
		t.Fatalf("error = %v, want ErrNoAdmission", err)
	}
}

func TestIssueTokenStampsVenueTime(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	f.admit(t, device)
	venue := f.clock.Now().UTC
	token, err := f.token(t, device)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	claims := mustDecodeToken(t, token.JWT, venue)
	// Both claims move back by the full tolerated offset. A system clock
	// lagging by up to that offset accepts the token at once and rejects
	// it no later than the venue expiry.
	start := venue.Add(-testMaxClockOffset)
	if !claims.IssuedAt.Equal(start) || !claims.NotBefore.Equal(start) {
		t.Fatalf("iat %v nbf %v, want %v", claims.IssuedAt, claims.NotBefore, start)
	}
	if want := venue.Add(TokenLifetime - testMaxClockOffset); !claims.ExpiresAt.Equal(want) {
		t.Fatalf("exp %v, want %v", claims.ExpiresAt, want)
	}
}

func TestIssueTokenNeverOutlastsAdmissionWhenSystemClockLags(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	admission := f.admit(t, device)
	lag := 3 * time.Second
	f.clock.set(synchronizedAt(admission.ValidUntil.Add(-20 * time.Minute)))
	f.clock.setSkew(-lag)
	issuedAt := f.clock.System()
	token, err := f.token(t, device)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	// The token must verify at issue time on the lagging system clock.
	claims := mustDecodeToken(t, token.JWT, issuedAt)
	// LiveKit compares exp with the lagging system clock. When venue UTC
	// reaches ValidUntil, that clock reads ValidUntil minus the lag.
	if _, err := decodeToken(t, token.JWT, admission.ValidUntil.Add(-lag)); !isExpired(err) {
		t.Fatalf("token still valid at admission end on the lagging clock: %v", err)
	}
	// The reported lifetime matches the signed claim interval.
	if interval := claims.ExpiresAt.Sub(claims.NotBefore.Time); token.ValidFor != interval {
		t.Fatalf("valid for %v, want the claim interval %v", token.ValidFor, interval)
	}
	if want := token.ValidFor / 2; token.RefreshAfter != want {
		t.Fatalf("refresh after %v, want %v", token.RefreshAfter, want)
	}
}

func TestIssueTokenFullLifetimeWhenSystemClockLags(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	f.admit(t, device)
	f.clock.setSkew(-3 * time.Second)
	token, err := f.token(t, device)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if token.ValidFor != TokenLifetime || token.RefreshAfter != TokenRefreshAfter {
		t.Fatalf("valid for %v refresh after %v, want %v and %v",
			token.ValidFor, token.RefreshAfter, TokenLifetime, TokenRefreshAfter)
	}
}

func TestIssueTokenNeverOutlastsAdmissionWhenClockDriftsAfterIssue(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	admission := f.admit(t, device)
	f.clock.set(synchronizedAt(admission.ValidUntil.Add(-20 * time.Minute)))
	token, err := f.token(t, device)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	// After issue, the system clock falls behind by the full tolerated
	// offset. LiveKit must still reject the token by the admission end.
	if _, err := decodeToken(t, token.JWT, admission.ValidUntil.Add(-testMaxClockOffset)); !isExpired(err) {
		t.Fatalf("token still valid at admission end after drift: %v", err)
	}
}

func TestIssueTokenRejectsOversizedChallenge(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	f.admit(t, device)
	oversized := make([]byte, 1<<20)
	if _, err := f.service.IssueToken(device.tokenRequest(t, testEventID, oversized)); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("error = %v, want ErrChallengeInvalid", err)
	}
	allocations := testing.AllocsPerRun(10, func() {
		_, _ = f.service.IssueToken(TokenRequest{EventID: testEventID, Challenge: oversized})
	})
	if allocations > 0 {
		t.Fatalf("oversized challenge allocated %v times per call", allocations)
	}
}

func TestNewServiceRejectsOffsetAtOrAboveMinimumLifetime(t *testing.T) {
	clock := &fakeClock{}
	_, err := NewService(Config{
		Clock:            clock,
		Monotonic:        clock.Monotonic,
		SystemClock:      clock.System,
		MaxClockOffset:   MinTokenLifetime,
		Random:           zeroReader{},
		Store:            NewMemoryStore(),
		LiveKitAPIKey:    testAPIKey,
		LiveKitAPISecret: testAPISecret,
		Rooms:            &recordingRooms{},
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("NewService error = %v, want ErrInvalidArgument", err)
	}
}

func TestIssueTokenNeverOutlastsAdmissionWhenSystemClockLeads(t *testing.T) {
	f := newFixture(t)
	device := newTestDevice(t)
	admission := f.admit(t, device)
	lead := 4 * time.Second
	f.clock.set(synchronizedAt(admission.ValidUntil.Add(-20 * time.Minute)))
	f.clock.setSkew(lead)
	token, err := f.token(t, device)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	mustDecodeToken(t, token.JWT, f.clock.System())
	if _, err := decodeToken(t, token.JWT, admission.ValidUntil.Add(lead)); !isExpired(err) {
		t.Fatalf("token still valid at admission end on the leading clock: %v", err)
	}
}

func TestIssueTokenRefusesLargeClockOffset(t *testing.T) {
	for name, skew := range map[string]time.Duration{
		"lagging": -testMaxClockOffset - time.Second,
		"leading": testMaxClockOffset + time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			device := newTestDevice(t)
			f.admit(t, device)
			f.clock.setSkew(skew)
			if _, err := f.token(t, device); !errors.Is(err, ErrClockOffset) {
				t.Fatalf("error = %v, want ErrClockOffset", err)
			}
		})
	}
}

func TestAddEventCopiesGrace(t *testing.T) {
	f := newFixture(t)
	grace := time.Hour
	if err := f.service.AddEvent(Event{ID: "event-2", Start: testEventStart, End: testEventEnd, Grace: &grace}); err != nil {
		t.Fatalf("AddEvent: %v", err)
	}
	// Mutating the caller's value after validation must not change the
	// stored grace or bypass MaxGrace.
	grace = 48 * time.Hour
	device := newTestDevice(t)
	receipt, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, "event-2"))
	if err != nil {
		t.Fatalf("RequestAdmission: %v", err)
	}
	admission, err := f.service.Approve("event-2", receipt.Code, testApprover)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if want := testEventEnd.Add(time.Hour); !admission.ValidUntil.Equal(want) {
		t.Fatalf("valid until %v, want %v", admission.ValidUntil, want)
	}
}

func TestMemoryStoreCopiesGrace(t *testing.T) {
	store := NewMemoryStore()
	grace := time.Hour
	store.PutEvent(Event{ID: testEventID, Start: testEventStart, End: testEventEnd, Grace: &grace})
	grace = 2 * time.Hour
	stored, ok := store.Event(testEventID)
	if !ok || stored.Grace == nil || *stored.Grace != time.Hour {
		t.Fatalf("stored grace changed with the caller's value: %v", stored.Grace)
	}
	*stored.Grace = 3 * time.Hour
	again, _ := store.Event(testEventID)
	if *again.Grace != time.Hour {
		t.Fatalf("stored grace changed through a returned event: %v", *again.Grace)
	}
}
