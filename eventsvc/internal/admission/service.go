package admission

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/netip"
	"reflect"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/livekit/protocol/auth"
)

const (
	// codeSpace is the number of six-digit confirmation identifiers.
	codeSpace = 1_000_000
	// maxCodeAttempts bounds random draws for one unique code.
	maxCodeAttempts = 32
	// challengeBytes is the random challenge length.
	challengeBytes = 32
	// admissionIDBytes is the random admission identifier length.
	admissionIDBytes = 16
)

var codePattern = regexp.MustCompile(`^[0-9]{6}$`)

// Config wires a Service. Every field is required. The service sets no
// defaults for clocks, randomness, storage, or keys.
type Config struct {
	Clock VenueClock
	// Monotonic returns elapsed time on a monotonic clock.
	Monotonic func() time.Duration
	// SystemClock reads the controller system clock. LiveKit checks token
	// expiry against this clock. Production code passes time.Now.
	SystemClock func() time.Time
	// MaxClockOffset bounds the gap between SystemClock and venue UTC.
	// A larger gap at issue refuses tokens. The deployment must keep the
	// controller system clock within this bound for every token lifetime.
	// It must stay below MinTokenLifetime. The value is experimental.
	MaxClockOffset time.Duration
	// Random supplies confirmation codes and challenges. Production code
	// passes crypto/rand.Reader.
	Random io.Reader
	Store  Store
	// LiveKitAPIKey and LiveKitAPISecret sign listener tokens. The secret
	// stays on the venue controller.
	LiveKitAPIKey    string
	LiveKitAPISecret string
	// Rooms removes participants from LiveKit rooms on revocation and
	// at admission end. Production code passes a LiveKitRooms.
	Rooms RoomRemover
}

// isNilRemover reports a nil remover, including a nil pointer wrapped in
// the interface. A wrapped nil pointer would pass a plain nil check and
// panic on the first removal.
func isNilRemover(rooms RoomRemover) bool {
	if rooms == nil {
		return true
	}
	value := reflect.ValueOf(rooms)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func (cfg Config) validate() error {
	switch {
	case cfg.Clock == nil:
		return fmt.Errorf("%w: venue clock is required", ErrInvalidArgument)
	case cfg.Monotonic == nil:
		return fmt.Errorf("%w: monotonic clock is required", ErrInvalidArgument)
	case cfg.SystemClock == nil:
		return fmt.Errorf("%w: system clock is required", ErrInvalidArgument)
	case cfg.MaxClockOffset <= 0 || cfg.MaxClockOffset >= MinTokenLifetime:
		return fmt.Errorf("%w: maximum clock offset must be positive and below %v", ErrInvalidArgument, MinTokenLifetime)
	case cfg.Random == nil:
		return fmt.Errorf("%w: random source is required", ErrInvalidArgument)
	case cfg.Store == nil:
		return fmt.Errorf("%w: store is required", ErrInvalidArgument)
	case isNilRemover(cfg.Rooms):
		return fmt.Errorf("%w: room remover is required", ErrInvalidArgument)
	case cfg.LiveKitAPIKey == "":
		return fmt.Errorf("%w: LiveKit API key is required", ErrInvalidArgument)
	case len(cfg.LiveKitAPISecret) < MinLiveKitSecretBytes:
		return fmt.Errorf("%w: LiveKit API secret needs at least %d bytes", ErrInvalidArgument, MinLiveKitSecretBytes)
	}
	return nil
}

type challenge struct {
	eventID   string
	expiresAt time.Duration
}

// Service runs staff approval and listener token issuance. It is safe for
// concurrent use. One mutex serializes all state.
type Service struct {
	cfg Config

	mu sync.Mutex
	// limiter counts admission requests per source address.
	limiter *rateLimiter[netip.Addr]
	// challengeFailures counts failed challenge requests per source
	// address.
	challengeFailures *rateLimiter[netip.Addr]
	// deviceChallenges counts challenges per admitted device.
	deviceChallenges *rateLimiter[string]
	// nonces holds recent challenge request nonces and their expiry.
	nonces map[nonceKey]time.Duration
	// pendingRemovals counts room removal calls in flight per device.
	// Approval waits until the count drops to zero, because LiveKit
	// removes by identity and would end a replacement's session.
	pendingRemovals map[deviceKey]int
	// sweepCursors holds the last device ID each event's expiry sweep
	// attempted. The next sweep starts after it.
	sweepCursors map[string]string
	// retryRemovals lists, per event, devices whose last removal failed or
	// was not reached. The next sweep tries them before its page.
	retryRemovals map[string][]string
	// sweepHolds holds, per event, the device the cursor last held back for
	// because the retry list was full. The cursor holds back for a device
	// once. If it overflows again, the sweep moves on, so failures that
	// persist cannot block later admissions.
	sweepHolds map[string]string
	challenges map[string]challenge
	// eventChallenges counts outstanding challenges per event.
	eventChallenges map[string]int
}

// NewService validates the configuration and returns a Service.
func NewService(cfg Config) (*Service, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Service{
		cfg:               cfg,
		limiter:           newRateLimiter[netip.Addr](RequestRateLimit, RequestRateWindow, MaxTrackedAddresses),
		challengeFailures: newRateLimiter[netip.Addr](ChallengeFailureLimit, ChallengeRateWindow, MaxTrackedAddresses),
		deviceChallenges:  newRateLimiter[string](ChallengeRateLimit, ChallengeRateWindow, MaxTrackedDevices),
		nonces:            make(map[nonceKey]time.Duration),
		pendingRemovals:   make(map[deviceKey]int),
		sweepCursors:      make(map[string]string),
		retryRemovals:     make(map[string][]string),
		sweepHolds:        make(map[string]string),
		challenges:        make(map[string]challenge),
		eventChallenges:   make(map[string]int),
	}, nil
}

// AddEvent registers an event. Start and End are stored as UTC instants.
func (s *Service) AddEvent(event Event) error {
	// Copy before validation so the caller cannot change the grace
	// between the check and the store.
	event = cloneEvent(event)
	if err := validateEvent(event); err != nil {
		return err
	}
	event.Start = event.Start.UTC()
	event.End = event.End.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.cfg.Store.Event(event.ID); exists {
		return fmt.Errorf("%w: event %s already exists", ErrInvalidArgument, event.ID)
	}
	s.cfg.Store.PutEvent(event)
	return nil
}

func validateEvent(event Event) error {
	if !identifierPattern.MatchString(event.ID) {
		return fmt.Errorf("%w: event identifier", ErrInvalidArgument)
	}
	if event.Start.IsZero() || !event.End.After(event.Start) {
		return fmt.Errorf("%w: event must end after it starts", ErrInvalidArgument)
	}
	grace := event.grace()
	if grace < 0 || grace > MaxGrace {
		return fmt.Errorf("%w: grace %v outside 0 to %v", ErrInvalidArgument, grace, MaxGrace)
	}
	return nil
}

// RequestAdmission opens an on-site admission request for a device and
// returns its confirmation identifier. A device with an open request for
// the event receives that request again.
func (s *Service) RequestAdmission(source netip.Addr, request SignedRequest) (RequestReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Monotonic()
	// The rate limit runs first, so rejected requests still cost the
	// sender and signature checks stay bounded per address.
	if !s.limiter.allow(source, now) {
		return RequestReceipt{}, ErrRateLimited
	}
	if _, err := s.event(request.EventID); err != nil {
		return RequestReceipt{}, err
	}
	message := RequestMessage(request.EventID, request.PublicKey)
	if err := verifyDevice(request.PublicKey, message, request.Signature); err != nil {
		return RequestReceipt{}, err
	}
	deviceID := DeviceID(request.PublicKey)
	open := s.openRequests(request.EventID, now)
	for _, existing := range open {
		if existing.DeviceID == deviceID {
			return receiptFor(existing, now), nil
		}
	}
	// The cap applies only to new requests. A device with an open request
	// above still receives it.
	if len(open) >= MaxOpenRequests {
		return RequestReceipt{}, ErrTooManyRequests
	}
	code, err := s.uniqueCode(open)
	if err != nil {
		return RequestReceipt{}, err
	}
	created := Request{
		EventID:   request.EventID,
		DeviceID:  deviceID,
		PublicKey: request.PublicKey,
		Code:      code,
		ExpiresAt: now + RequestLifetime,
	}
	s.cfg.Store.PutRequest(created)
	return receiptFor(created, now), nil
}

func receiptFor(request Request, now time.Duration) RequestReceipt {
	return RequestReceipt{DeviceID: request.DeviceID, Code: request.Code, ExpiresIn: request.ExpiresAt - now}
}

// Approve converts the open request with the given code into an admission
// valid until the event end plus its grace. Staff authentication happens
// before this call and stays outside this package.
func (s *Service) Approve(eventID, code, approverID string) (Admission, error) {
	if !identifierPattern.MatchString(approverID) {
		return Admission{}, fmt.Errorf("%w: approver identifier", ErrInvalidArgument)
	}
	if !codePattern.MatchString(code) {
		return Admission{}, fmt.Errorf("%w: confirmation code format", ErrInvalidArgument)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, err := s.event(eventID)
	if err != nil {
		return Admission{}, err
	}
	request, found := findByCode(s.openRequests(eventID, s.cfg.Monotonic()), code)
	if !found {
		return Admission{}, ErrRequestNotFound
	}
	// The request stays open, so staff can approve once removal ends. A
	// revoked admission whose room removal has not succeeded also holds
	// approval back. The sweep retries that removal.
	if s.pendingRemovals[deviceKey{eventID, request.DeviceID}] > 0 {
		return Admission{}, ErrRemovalPending
	}
	if current, ok := s.cfg.Store.Admission(eventID, request.DeviceID); ok && current.Revoked && !current.RoomRemoved {
		return Admission{}, ErrRemovalPending
	}
	venue := s.cfg.Clock.Now()
	if !venue.usable() {
		return Admission{}, ErrVenueTimeUnavailable
	}
	validUntil := event.End.Add(event.grace())
	if !venue.latest().Before(validUntil) {
		return Admission{}, ErrAdmissionExpired
	}
	id, err := s.newAdmissionID()
	if err != nil {
		return Admission{}, err
	}
	admission := Admission{
		ID:         id,
		EventID:    eventID,
		DeviceID:   request.DeviceID,
		PublicKey:  request.PublicKey,
		Method:     MethodStaffApproval,
		ApproverID: approverID,
		ApprovedAt: venue.UTC,
		ValidUntil: validUntil,
	}
	s.cfg.Store.PutAdmission(admission)
	s.cfg.Store.DeleteRequest(eventID, request.DeviceID)
	return admission, nil
}

func findByCode(requests []Request, code string) (Request, bool) {
	for _, request := range requests {
		if request.Code == code {
			return request, true
		}
	}
	return Request{}, false
}

// Revoke marks an admission revoked and removes the device from the event
// room. Refresh stops at once. The admission stays revoked when removal
// fails. A repeated Revoke retries the removal.
func (s *Service) Revoke(ctx context.Context, eventID, deviceID, admissionID string) error {
	admission, alreadyRemoved, err := s.markRevoked(eventID, deviceID, admissionID)
	if err != nil {
		return err
	}
	if alreadyRemoved {
		return nil
	}
	defer s.endRemoval(eventID, deviceID)
	// The room service call runs outside the lock. A slow LiveKit server
	// must not stall admission and token issuance.
	if err := s.cfg.Rooms.RemoveParticipant(ctx, eventID, deviceID); err != nil {
		// The sweep retries the removal. Approval of the device waits
		// until it succeeds.
		s.addRetryRemoval(eventID, deviceID)
		return fmt.Errorf("admission revoked, room removal failed: %w", err)
	}
	s.markRoomRemoved(admission)
	return nil
}

// markRevoked revokes the admission and starts a pending removal in one
// locked step, so no approval can slip in before the room call. It
// refuses an admission ID that is no longer current, so a stale decision
// cannot revoke a replacement. It reports whether the device already left
// the room, in which case no removal starts.
func (s *Service) markRevoked(eventID, deviceID, admissionID string) (Admission, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	admission, ok := s.cfg.Store.Admission(eventID, deviceID)
	if !ok {
		return Admission{}, false, ErrNoAdmission
	}
	if admission.ID != admissionID {
		return Admission{}, false, ErrAdmissionChanged
	}
	admission.Revoked = true
	s.cfg.Store.PutAdmission(admission)
	if admission.RoomRemoved {
		return admission, true, nil
	}
	s.pendingRemovals[deviceKey{eventID, deviceID}]++
	return admission, false, nil
}

// endRemoval ends one pending removal for the device.
func (s *Service) endRemoval(eventID, deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := deviceKey{eventID, deviceID}
	remaining := s.pendingRemovals[key] - 1
	if remaining <= 0 {
		delete(s.pendingRemovals, key)
		return
	}
	s.pendingRemovals[key] = remaining
}

// Challenge returns a single-use random challenge bound to one event.
// Only a device with a valid admission for the event receives one. The
// quota binds to the signing device, so senders sharing a source address
// cannot spend each other's quota with valid requests.
func (s *Service) Challenge(source netip.Addr, request ChallengeRequest) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The event check runs first. Calls for unknown or malformed events
	// store no state and cost no budget.
	if _, err := s.event(request.EventID); err != nil {
		return nil, err
	}
	now := s.cfg.Monotonic()
	// A source past its failure budget is refused before any signature
	// work. The budget bounds verification cost per address.
	if s.challengeFailures.blocked(source, now) {
		return nil, ErrChallengeFailures
	}
	// The key size check bounds the hashing below. A failure charges
	// the address like any other invalid request.
	if len(request.PublicKey) == 0 || len(request.PublicKey) > maxPublicKeyBytes {
		s.challengeFailures.record(source, now)
		return nil, fmt.Errorf("%w: public key size %d", ErrInvalidArgument, len(request.PublicKey))
	}
	deviceID := DeviceID(request.PublicKey)
	// A device over its quota is refused before signature work. The
	// check records nothing, so a sender cannot spend another device's
	// quota by naming its key. It does not charge the address either,
	// so an admitted device cannot lock out its neighbors.
	if s.deviceChallenges.blocked(deviceID, now) {
		return nil, ErrChallengeRateLimited
	}
	if err := s.authorizeChallenge(request, deviceID, now); err != nil {
		s.challengeFailures.record(source, now)
		return nil, err
	}
	return s.issueChallenge(request.EventID, deviceID, request.Nonce, now)
}

// authorizeChallenge verifies a signed challenge request from an
// admitted, unrevoked device.
func (s *Service) authorizeChallenge(request ChallengeRequest, deviceID string, now time.Duration) error {
	if len(request.Nonce) != ChallengeNonceBytes {
		return fmt.Errorf("%w: nonce must hold %d bytes", ErrInvalidArgument, ChallengeNonceBytes)
	}
	message := ChallengeRequestMessage(request.EventID, request.PublicKey, request.Nonce)
	if err := verifyDevice(request.PublicKey, message, request.Signature); err != nil {
		return err
	}
	if s.nonceSeen(deviceID, request.Nonce, now) {
		return ErrChallengeReplayed
	}
	admission, ok := s.cfg.Store.Admission(request.EventID, deviceID)
	if !ok || !bytes.Equal(admission.PublicKey, request.PublicKey) {
		return ErrNoAdmission
	}
	if admission.Revoked {
		return ErrRevoked
	}
	return nil
}

// nonceKey identifies one challenge request nonce of one device.
type nonceKey struct {
	deviceID string
	nonce    [ChallengeNonceBytes]byte
}

func newNonceKey(deviceID string, nonce []byte) nonceKey {
	key := nonceKey{deviceID: deviceID}
	copy(key.nonce[:], nonce)
	return key
}

// nonceSeen reports whether the device used the nonce inside the window.
func (s *Service) nonceSeen(deviceID string, nonce []byte, now time.Duration) bool {
	expiresAt, ok := s.nonces[newNonceKey(deviceID, nonce)]
	return ok && now < expiresAt
}

// rememberNonce records a nonce for ChallengeRateWindow. Nonces are only
// recorded inside the device quota, which bounds the map.
func (s *Service) rememberNonce(deviceID string, nonce []byte, now time.Duration) {
	if len(s.nonces) > rateSweepThreshold {
		expired := make([]nonceKey, 0)
		for key, expiresAt := range s.nonces {
			if now >= expiresAt {
				expired = append(expired, key)
			}
		}
		for _, key := range expired {
			delete(s.nonces, key)
		}
	}
	s.nonces[newNonceKey(deviceID, nonce)] = now + ChallengeRateWindow
}

// issueChallenge draws and stores a challenge within the pool caps. The
// device quota and the nonce are charged only once the challenge exists.
// A pool cap rejection therefore costs the device nothing and leaves the
// signed request valid for a retry.
func (s *Service) issueChallenge(eventID, deviceID string, nonce []byte, now time.Duration) ([]byte, error) {
	s.sweepChallenges(now)
	if len(s.challenges) >= MaxOutstandingChallenges || s.eventChallenges[eventID] >= MaxEventChallenges {
		return nil, ErrTooManyChallenges
	}
	value := make([]byte, challengeBytes)
	if _, err := io.ReadFull(s.cfg.Random, value); err != nil {
		return nil, fmt.Errorf("read challenge randomness: %w", err)
	}
	s.deviceChallenges.record(deviceID, now)
	s.rememberNonce(deviceID, nonce, now)
	s.challenges[hex.EncodeToString(value)] = challenge{eventID: eventID, expiresAt: now + ChallengeLifetime}
	s.eventChallenges[eventID]++
	return value, nil
}

func (s *Service) sweepChallenges(now time.Duration) {
	expired := make([]string, 0)
	for key, entry := range s.challenges {
		if now >= entry.expiresAt {
			expired = append(expired, key)
		}
	}
	for _, key := range expired {
		s.removeChallenge(key)
	}
}

// removeChallenge deletes one challenge and releases its event slot.
// Every removal must go through this helper. A direct delete would leave
// eventChallenges counting a challenge that no longer exists.
func (s *Service) removeChallenge(key string) {
	entry, ok := s.challenges[key]
	if !ok {
		return
	}
	delete(s.challenges, key)
	remaining := s.eventChallenges[entry.eventID] - 1
	if remaining <= 0 {
		delete(s.eventChallenges, entry.eventID)
		return
	}
	s.eventChallenges[entry.eventID] = remaining
}

// consumeChallenge removes a challenge and reports whether it was valid
// for the event. Any lookup consumes the challenge.
// maxRemovalsPerSweep bounds the room service calls of one
// ExpireAdmissions call. The value is experimental (docs/backlog.md).
const maxRemovalsPerSweep = 256

// maxRetriesPerSweep bounds the retries of earlier failures in one
// sweep. The rest of the sweep continues from the cursor, so failures
// that persist cannot keep later admissions from removal.
const maxRetriesPerSweep = maxRemovalsPerSweep / 2

// ExpireAdmissions removes devices from the event room whose admission
// ended, or was revoked without a successful room removal. It returns the
// number of devices removed. The server loop calls it periodically for
// each event.
//
// A sweep first handles the event's retry list, then reads one page from
// its cursor. A device stays on the retry list until its current
// admission is removed from the room, even across a replacement. One call
// makes at most maxRemovalsPerSweep room calls. A timeout or a cancelled
// context ends the sweep at once, because the room service is then likely
// unreachable. Other failures skip only that device.
func (s *Service) ExpireAdmissions(ctx context.Context, eventID string) (int, error) {
	plan, err := s.planSweep(eventID)
	if err != nil {
		return 0, err
	}
	result := sweepResult{failedAt: make(map[int]bool)}
	var firstFailure error
	// Room service calls run outside the lock, like Revoke.
	for i, item := range plan.items {
		// Staff may have approved the device again since the sweep
		// selected it. Skip it then, so the call cannot end the
		// replacement's session. Approval waits while the call runs.
		if !s.beginRemoval(item.admission) {
			result.reached = i + 1
			continue
		}
		result.attempted++
		err := s.cfg.Rooms.RemoveParticipant(ctx, eventID, item.admission.DeviceID)
		s.endRemoval(eventID, item.admission.DeviceID)
		result.reached = i + 1
		if err == nil {
			s.markRoomRemoved(item.admission)
			result.removed++
			continue
		}
		result.failedAt[i] = true
		if firstFailure == nil {
			firstFailure = fmt.Errorf("remove device %s: %w", item.admission.DeviceID, err)
		}
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			break
		}
	}
	s.finishSweep(eventID, plan, result)
	if firstFailure != nil {
		// The error keeps one sample and a count, so its size does not
		// grow with the number of admissions.
		return result.removed, fmt.Errorf("%d of %d room removals failed, first: %w", len(result.failedAt), result.attempted, firstFailure)
	}
	return result.removed, nil
}

// sweepItem is one admission a sweep tries to remove.
type sweepItem struct {
	admission Admission
	// fromPage marks an item read from the page rather than the retry
	// list. prev is the device ID read just before it, which the cursor
	// takes when the sweep must read this item again.
	fromPage bool
	prev     string
}

// sweepPlan is what one sweep reads under the lock.
type sweepPlan struct {
	items []sweepItem
	// waiting lists retry devices whose current admission is still valid.
	// They stay on the retry list until that admission needs removal.
	waiting []string
	// lastRead is the last device ID of the page, or empty when the sweep
	// read no page.
	lastRead string
}

// sweepResult is what one sweep did with its plan.
type sweepResult struct {
	removed   int
	attempted int
	// reached counts the plan items the sweep got to. Items after it were
	// not tried because the sweep stopped.
	reached  int
	failedAt map[int]bool
}

// needsRemoval reports whether the device must leave the room: its
// admission ended by the latest possible venue time, or it was revoked,
// and no removal succeeded yet. The end test matches the approval check,
// so a device leaves the room no later than token issuance stops.
func needsRemoval(admission Admission, venue VenueTime) bool {
	if admission.RoomRemoved {
		return false
	}
	return admission.Revoked || !venue.latest().Before(admission.ValidUntil)
}

// planSweep selects the admissions of one sweep. Retry devices come
// first, at most maxRetriesPerSweep, resolved against their current
// admission. One page of the store after the cursor fills the rest and
// wraps around once. The page bounds the work of one sweep however large
// the event grows.
func (s *Service) planSweep(eventID string) (sweepPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.event(eventID); err != nil {
		return sweepPlan{}, err
	}
	venue := s.cfg.Clock.Now()
	if !venue.usable() {
		return sweepPlan{}, ErrVenueTimeUnavailable
	}
	plan := sweepPlan{}
	selected := make(map[string]bool)
	for _, deviceID := range s.retryRemovals[eventID] {
		admission, ok := s.cfg.Store.Admission(eventID, deviceID)
		if !ok || admission.RoomRemoved {
			continue
		}
		if !needsRemoval(admission, venue) {
			plan.waiting = append(plan.waiting, deviceID)
			continue
		}
		plan.items = append(plan.items, sweepItem{admission: admission})
		selected[deviceID] = true
	}
	pageLimit := maxRemovalsPerSweep - len(plan.items)
	cursor := s.sweepCursors[eventID]
	page := s.cfg.Store.AdmissionsAfter(eventID, cursor, pageLimit)
	if remaining := pageLimit - len(page); remaining > 0 && cursor != "" {
		for _, admission := range s.cfg.Store.AdmissionsAfter(eventID, "", remaining) {
			if admission.DeviceID > cursor {
				break
			}
			page = append(page, admission)
		}
	}
	prev := cursor
	for _, admission := range page {
		if !selected[admission.DeviceID] && needsRemoval(admission, venue) {
			plan.items = append(plan.items, sweepItem{admission: admission, fromPage: true, prev: prev})
		}
		prev = admission.DeviceID
		plan.lastRead = admission.DeviceID
	}
	return plan, nil
}

// finishSweep stores the next retry list and cursor. Retry devices that
// still need removal stay first. Page devices that failed join them up to
// maxRetriesPerSweep. The cursor never moves past a page device the sweep
// did not reach. It also holds back for the first failed page device the
// retry list cannot keep, once. When that device overflows again on the
// next sweep, the cursor moves past the page instead.
func (s *Service) finishSweep(eventID string, plan sweepPlan, result sweepResult) {
	s.mu.Lock()
	held := s.sweepHolds[eventID]
	s.mu.Unlock()
	nextHold := ""
	advance := false
	retry := slices.Clone(plan.waiting)
	for i, item := range plan.items {
		if item.fromPage {
			continue
		}
		// A retry device the sweep did not reach, or whose call failed,
		// stays on the list.
		if i >= result.reached || result.failedAt[i] {
			retry = append(retry, item.admission.DeviceID)
		}
	}
	cursor := plan.lastRead
	for i, item := range plan.items {
		if !item.fromPage {
			continue
		}
		if i >= result.reached {
			cursor = item.prev
			break
		}
		if !result.failedAt[i] {
			continue
		}
		if len(retry) < maxRetriesPerSweep {
			retry = append(retry, item.admission.DeviceID)
			continue
		}
		if advance {
			continue
		}
		if item.admission.DeviceID == held {
			advance = true
			continue
		}
		nextHold = item.admission.DeviceID
		cursor = item.prev
		break
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if nextHold == "" {
		delete(s.sweepHolds, eventID)
	} else {
		s.sweepHolds[eventID] = nextHold
	}
	if len(retry) == 0 {
		delete(s.retryRemovals, eventID)
	} else {
		s.retryRemovals[eventID] = retry[:min(len(retry), maxRetriesPerSweep)]
	}
	if plan.lastRead != "" {
		s.sweepCursors[eventID] = cursor
	}
}

// addRetryRemoval puts a device on the event's retry list when it has
// room. A device left out still needs removal, so a later page finds it.
func (s *Service) addRetryRemoval(eventID, deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	retry := s.retryRemovals[eventID]
	if slices.Contains(retry, deviceID) || len(retry) >= maxRetriesPerSweep {
		return
	}
	s.retryRemovals[eventID] = append(retry, deviceID)
}

func (s *Service) setSweepCursor(eventID, deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepCursors[eventID] = deviceID
}

// beginRemoval starts a pending removal when the stored admission for
// the device is still the given one. The check and the start share one
// locked step, so no approval can slip in between.
func (s *Service) beginRemoval(admission Admission) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.cfg.Store.Admission(admission.EventID, admission.DeviceID)
	if !ok || current.ID != admission.ID {
		return false
	}
	s.pendingRemovals[deviceKey{admission.EventID, admission.DeviceID}]++
	return true
}

// markRoomRemoved records the removal on the ended admission. Staff may
// approve the device again while the room service call is in flight. The
// admission ID tells the two apart even when the venue clock repeats an
// instant, so a replacement stays unmarked and a later sweep handles it.
func (s *Service) markRoomRemoved(ended Admission) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.cfg.Store.Admission(ended.EventID, ended.DeviceID)
	if !ok || current.ID != ended.ID {
		return
	}
	current.RoomRemoved = true
	s.cfg.Store.PutAdmission(current)
}

func (s *Service) consumeChallenge(eventID string, value []byte) bool {
	// Generated challenges are exactly challengeBytes long. Rejecting any
	// other length first bounds work on untrusted input.
	if len(value) != challengeBytes {
		return false
	}
	key := hex.EncodeToString(value)
	entry, ok := s.challenges[key]
	if !ok {
		return false
	}
	s.removeChallenge(key)
	return entry.eventID == eventID && s.cfg.Monotonic() < entry.expiresAt
}

// IssueToken verifies a signed challenge and returns a subscribe-only
// LiveKit token for the event room. Venue UTC decides eligibility and
// lifetime.
func (s *Service) IssueToken(request TokenRequest) (Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.consumeChallenge(request.EventID, request.Challenge) {
		return Token{}, ErrChallengeInvalid
	}
	message := SessionMessage(request.EventID, request.Challenge)
	if err := verifyDevice(request.PublicKey, message, request.Signature); err != nil {
		return Token{}, err
	}
	deviceID := DeviceID(request.PublicKey)
	admission, ok := s.cfg.Store.Admission(request.EventID, deviceID)
	if !ok || !bytes.Equal(admission.PublicKey, request.PublicKey) {
		return Token{}, ErrNoAdmission
	}
	if admission.Revoked {
		return Token{}, ErrRevoked
	}
	window, err := s.tokenWindow(admission.ValidUntil)
	if err != nil {
		return Token{}, err
	}
	return s.signToken(request.EventID, deviceID, window)
}

// tokenWindow holds the venue UTC claims for one token.
type tokenWindow struct {
	notBefore time.Time
	expiresAt time.Time
	// validFor is the signed claim interval.
	validFor time.Duration
}

// tokenWindow stamps token claims from venue UTC. LiveKit compares the
// claims with the controller system clock, which may lag venue UTC by up
// to MaxClockOffset at any time during the token lifetime. Both claims
// therefore move back by the full bound. A lagging clock accepts the
// token at once and still rejects it by the venue expiry. A leading clock
// only ends the token early. The offset check at issue catches a clock
// already outside the bound.
func (s *Service) tokenWindow(validUntil time.Time) (tokenWindow, error) {
	venue := s.cfg.Clock.Now()
	lifetime, err := tokenLifetime(venue, validUntil)
	if err != nil {
		return tokenWindow{}, err
	}
	system := s.cfg.SystemClock()
	offset := system.Sub(venue.UTC)
	if offset > s.cfg.MaxClockOffset || offset < -s.cfg.MaxClockOffset {
		return tokenWindow{}, fmt.Errorf("%w: offset %v exceeds %v", ErrClockOffset, offset, s.cfg.MaxClockOffset)
	}
	// The JWT claims have whole-second precision. Truncation moves the
	// expiry earlier and the start earlier, never later.
	notBefore := venue.UTC.Add(-s.cfg.MaxClockOffset).Truncate(time.Second)
	expiresAt := venue.UTC.Add(lifetime - s.cfg.MaxClockOffset).Truncate(time.Second)
	return tokenWindow{notBefore: notBefore, expiresAt: expiresAt, validFor: expiresAt.Sub(notBefore)}, nil
}

// tokenLifetime applies the approved lifetime rules. The latest possible
// venue time bounds the remaining admission. Holdover shortens the
// lifetime by the current uncertainty.
func tokenLifetime(venue VenueTime, validUntil time.Time) (time.Duration, error) {
	if !venue.usable() {
		return 0, ErrVenueTimeUnavailable
	}
	remaining := validUntil.Sub(venue.latest())
	if remaining < MinTokenLifetime {
		return 0, ErrAdmissionExpired
	}
	lifetime := min(TokenLifetime, remaining)
	if venue.State == SyncStateHoldover {
		lifetime -= venue.Uncertainty
	}
	// The JWT expiry claim has whole-second precision.
	lifetime = lifetime.Truncate(time.Second)
	if lifetime < MinTokenLifetime {
		return 0, ErrVenueTimeUnavailable
	}
	return lifetime, nil
}

// listenerClaims matches the claim layout LiveKit verifies.
type listenerClaims struct {
	jwt.RegisteredClaims
	auth.ClaimGrants
}

func (s *Service) signToken(room, identity string, window tokenWindow) (Token, error) {
	allow, deny := true, false
	grant := &auth.VideoGrant{
		RoomJoin:             true,
		Room:                 room,
		CanSubscribe:         &allow,
		CanPublish:           &deny,
		CanPublishData:       &deny,
		CanUpdateOwnMetadata: &deny,
	}
	claims := listenerClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.cfg.LiveKitAPIKey,
			Subject:   identity,
			IssuedAt:  jwt.NewNumericDate(window.notBefore),
			NotBefore: jwt.NewNumericDate(window.notBefore),
			ExpiresAt: jwt.NewNumericDate(window.expiresAt),
		},
		ClaimGrants: auth.ClaimGrants{Identity: identity, Video: grant},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.cfg.LiveKitAPISecret))
	if err != nil {
		return Token{}, fmt.Errorf("sign listener token: %w", err)
	}
	return Token{
		JWT:          signed,
		Room:         room,
		Identity:     identity,
		ValidFor:     window.validFor,
		RefreshAfter: min(TokenRefreshAfter, window.validFor/2),
	}, nil
}

func (s *Service) event(eventID string) (Event, error) {
	if !identifierPattern.MatchString(eventID) {
		return Event{}, fmt.Errorf("%w: event identifier", ErrInvalidArgument)
	}
	event, ok := s.cfg.Store.Event(eventID)
	if !ok {
		return Event{}, ErrUnknownEvent
	}
	return event, nil
}

// openRequests deletes expired requests for the event and returns the
// rest.
func (s *Service) openRequests(eventID string, now time.Duration) []Request {
	open := make([]Request, 0)
	for _, request := range s.cfg.Store.Requests(eventID) {
		if now >= request.ExpiresAt {
			s.cfg.Store.DeleteRequest(eventID, request.DeviceID)
			continue
		}
		open = append(open, request)
	}
	return open
}

// newAdmissionID draws a random admission identifier. The caller holds
// the lock.
func (s *Service) newAdmissionID() (string, error) {
	value := make([]byte, admissionIDBytes)
	if _, err := io.ReadFull(s.cfg.Random, value); err != nil {
		return "", fmt.Errorf("read admission ID randomness: %w", err)
	}
	return hex.EncodeToString(value), nil
}

// uniqueCode draws a six-digit code that no open request uses.
func (s *Service) uniqueCode(open []Request) (string, error) {
	used := make(map[string]struct{}, len(open))
	for _, request := range open {
		used[request.Code] = struct{}{}
	}
	limit := big.NewInt(codeSpace)
	for range maxCodeAttempts {
		value, err := rand.Int(s.cfg.Random, limit)
		if err != nil {
			return "", fmt.Errorf("read code randomness: %w", err)
		}
		code := fmt.Sprintf("%06d", value.Int64())
		if _, taken := used[code]; !taken {
			return code, nil
		}
	}
	return "", ErrCodeSpaceExhausted
}
