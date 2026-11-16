package admission

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testAPIKey    = "test-key"
	testAPISecret = "test-secret-with-at-least-32-bytes-0123"
	testEventID   = "event-1"
	testApprover  = "staff-1"
	// testMaxClockOffset is the tolerated system clock offset in tests.
	testMaxClockOffset = 5 * time.Second
)

var (
	testEventStart = time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	testEventEnd   = time.Date(2026, 11, 21, 2, 0, 0, 0, time.UTC)
	testSource     = netip.MustParseAddr("10.10.0.5")
)

// fakeClock is a venue clock, a system clock, and a monotonic clock under
// test control. The system clock reads venue UTC plus skew. Without venue
// UTC it reads a fixed base plus monotonic time.
type fakeClock struct {
	mu        sync.Mutex
	venue     VenueTime
	monotonic time.Duration
	skew      time.Duration
}

func (c *fakeClock) System() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.venue.UTC.IsZero() {
		return testEventStart.Add(c.monotonic)
	}
	return c.venue.UTC.Add(c.skew)
}

func (c *fakeClock) setSkew(skew time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.skew = skew
}

func (c *fakeClock) Now() VenueTime {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.venue
}

func (c *fakeClock) Monotonic() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.monotonic
}

// advance moves both clocks forward by d. Venue UTC only moves when set.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.monotonic += d
	if !c.venue.UTC.IsZero() {
		c.venue.UTC = c.venue.UTC.Add(d)
	}
}

func (c *fakeClock) set(v VenueTime) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.venue = v
}

func synchronizedAt(utc time.Time) VenueTime {
	return VenueTime{UTC: utc, Uncertainty: time.Millisecond, State: SyncStateSynchronized}
}

// zeroReader always returns zero bytes. Every confirmation code it
// produces is the same, which forces code collisions.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type testDevice struct {
	key *ecdsa.PrivateKey
	der []byte
}

func newTestDevice(t *testing.T) testDevice {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return testDevice{key: key, der: der}
}

func (d testDevice) sign(t *testing.T, message []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(message)
	signature, err := ecdsa.SignASN1(rand.Reader, d.key, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signature
}

func (d testDevice) admissionRequest(t *testing.T, eventID string) SignedRequest {
	t.Helper()
	return SignedRequest{
		EventID:   eventID,
		PublicKey: d.der,
		Signature: d.sign(t, RequestMessage(eventID, d.der)),
	}
}

func (d testDevice) tokenRequest(t *testing.T, eventID string, challenge []byte) TokenRequest {
	t.Helper()
	return TokenRequest{
		EventID:   eventID,
		PublicKey: d.der,
		Challenge: challenge,
		Signature: d.sign(t, SessionMessage(eventID, challenge)),
	}
}

type fixture struct {
	clock   *fakeClock
	service *Service
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	return newFixtureWithRandom(t, rand.Reader)
}

func newFixtureWithRandom(t *testing.T, random interface{ Read([]byte) (int, error) }) fixture {
	t.Helper()
	clock := &fakeClock{venue: synchronizedAt(testEventStart.Add(time.Hour))}
	service, err := NewService(Config{
		Clock:            clock,
		Monotonic:        clock.Monotonic,
		SystemClock:      clock.System,
		MaxClockOffset:   testMaxClockOffset,
		Random:           random,
		Store:            NewMemoryStore(),
		LiveKitAPIKey:    testAPIKey,
		LiveKitAPISecret: testAPISecret,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := service.AddEvent(Event{ID: testEventID, Start: testEventStart, End: testEventEnd}); err != nil {
		t.Fatalf("AddEvent: %v", err)
	}
	return fixture{clock: clock, service: service}
}

// admit runs the staff approval flow for one device.
func (f fixture) admit(t *testing.T, device testDevice) Admission {
	t.Helper()
	receipt, err := f.service.RequestAdmission(testSource, device.admissionRequest(t, testEventID))
	if err != nil {
		t.Fatalf("RequestAdmission: %v", err)
	}
	admission, err := f.service.Approve(testEventID, receipt.Code, testApprover)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	return admission
}

// token runs the challenge and token flow for one admitted device.
func (f fixture) token(t *testing.T, device testDevice) (Token, error) {
	t.Helper()
	challenge, err := f.service.Challenge(testEventID)
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	return f.service.IssueToken(device.tokenRequest(t, testEventID, challenge))
}

// decodeToken verifies a listener token as LiveKit would at the given
// system time.
func decodeToken(t *testing.T, raw string, at time.Time) (*listenerClaims, error) {
	t.Helper()
	claims := &listenerClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
		return []byte(testAPISecret), nil
	},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(testAPIKey),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return at }),
	)
	return claims, err
}

// mustDecodeToken decodes a token that must verify at the given time.
func mustDecodeToken(t *testing.T, raw string, at time.Time) *listenerClaims {
	t.Helper()
	claims, err := decodeToken(t, raw, at)
	if err != nil {
		t.Fatalf("decode token at %v: %v", at, err)
	}
	return claims
}

func isExpired(err error) bool {
	return errors.Is(err, jwt.ErrTokenExpired)
}
