// Package admission implements on-site staff approval and listening
// session tokens from the approved event flow v0
// (docs/design/event-flow-v0.md, flows 2 and 3).
//
// Every expiry decision on an absolute instant reads venue UTC from the
// venue time service. Phone clocks play no part. Durations such as request
// expiry, challenge expiry, and rate windows use a monotonic clock.
package admission

import (
	"errors"
	"regexp"
	"time"
)

// Approved values from docs/design/event-flow-v0.md.
const (
	// RequestLifetime bounds how long an admission request stays open.
	RequestLifetime = 60 * time.Minute
	// RequestRateLimit caps admission requests per source address within
	// RequestRateWindow.
	RequestRateLimit  = 10
	RequestRateWindow = time.Minute
	// DefaultGrace extends admission past the scheduled event end when the
	// event sets no grace. MaxGrace caps an event's grace.
	DefaultGrace = 4 * time.Hour
	MaxGrace     = 6 * time.Hour
	// TokenLifetime is the listener token lifetime. TokenRefreshAfter is
	// when the app refreshes a full-lifetime token.
	TokenLifetime     = 60 * time.Minute
	TokenRefreshAfter = 30 * time.Minute
)

// Experimental values. They carry no approval (docs/backlog.md).
const (
	// ChallengeLifetime bounds the time between a challenge and its use.
	ChallengeLifetime = time.Minute
	// MaxOutstandingChallenges bounds challenge memory.
	MaxOutstandingChallenges = 4096
	// MinTokenLifetime is the shortest token the service issues.
	MinTokenLifetime = time.Minute
)

// Errors returned by the service. Callers match them with errors.Is.
var (
	ErrInvalidArgument      = errors.New("invalid argument")
	ErrUnknownEvent         = errors.New("unknown event")
	ErrBadSignature         = errors.New("device signature does not verify")
	ErrRateLimited          = errors.New("too many admission requests from this address")
	ErrCodeSpaceExhausted   = errors.New("no unique confirmation code available")
	ErrRequestNotFound      = errors.New("no open admission request matches")
	ErrVenueTimeUnavailable = errors.New("venue time is not usable")
	ErrAdmissionExpired     = errors.New("admission is no longer valid")
	ErrNoAdmission          = errors.New("device has no admission for this event")
	ErrRevoked              = errors.New("admission was revoked")
	ErrChallengeInvalid     = errors.New("challenge is unknown, used, or expired")
	ErrTooManyChallenges    = errors.New("too many outstanding challenges")
)

// identifierPattern limits event and approver identifiers to characters
// that are safe as LiveKit room names and in logs.
var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Event is one private event. Start and End are UTC instants.
type Event struct {
	ID    string
	Start time.Time
	End   time.Time
	// Grace extends admission past End. Nil selects DefaultGrace.
	Grace *time.Duration
}

// grace returns the effective admission grace.
func (e Event) grace() time.Duration {
	if e.Grace == nil {
		return DefaultGrace
	}
	return *e.Grace
}

// Method records how a device gained admission.
type Method uint8

// Admission methods.
const (
	MethodInvitation Method = iota + 1
	MethodStaffApproval
)

// String returns a stable lowercase name for logs.
func (m Method) String() string {
	switch m {
	case MethodInvitation:
		return "invitation"
	case MethodStaffApproval:
		return "staff_approval"
	default:
		return "unknown"
	}
}

// Request is an open on-site admission request.
type Request struct {
	EventID   string
	DeviceID  string
	PublicKey []byte
	Code      string
	// ExpiresAt is a monotonic clock reading.
	ExpiresAt time.Duration
}

// Admission binds one device to one event.
type Admission struct {
	EventID    string
	DeviceID   string
	PublicKey  []byte
	Method     Method
	ApproverID string
	// ApprovedAt and ValidUntil are venue UTC instants.
	ApprovedAt time.Time
	ValidUntil time.Time
	Revoked    bool
}

// SignedRequest is an admission request signed with the device key.
type SignedRequest struct {
	EventID string
	// PublicKey is a PKIX DER encoded ECDSA P-256 public key.
	PublicKey []byte
	// Signature is an ASN.1 ECDSA signature over SHA-256 of
	// RequestMessage(EventID, PublicKey).
	Signature []byte
}

// RequestReceipt answers an admission request.
type RequestReceipt struct {
	DeviceID string
	// Code is the six-digit confirmation identifier staff match against.
	// It is not a secret.
	Code      string
	ExpiresIn time.Duration
}

// TokenRequest asks for a listener token with a signed challenge.
type TokenRequest struct {
	EventID   string
	PublicKey []byte
	Challenge []byte
	// Signature is an ASN.1 ECDSA signature over SHA-256 of
	// SessionMessage(EventID, Challenge).
	Signature []byte
}

// Token is a signed LiveKit listener token.
type Token struct {
	JWT          string
	Room         string
	Identity     string
	ValidFor     time.Duration
	RefreshAfter time.Duration
}
