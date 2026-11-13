package venuetime

import (
	"fmt"
	"sync"
	"time"
)

// Source identifies an authoritative venue time source.
type Source uint8

// Time sources in ascending priority order.
const (
	SourceNone Source = iota
	SourceNTP
	SourceGPS
)

// String returns a stable lowercase name for logs.
func (s Source) String() string {
	switch s {
	case SourceNTP:
		return "ntp"
	case SourceGPS:
		return "gps"
	default:
		return "none"
	}
}

// State describes the quality of the venue UTC estimate.
type State uint8

// Estimate states.
const (
	// StateUnsynchronized means no usable reference exists.
	StateUnsynchronized State = iota
	// StateSynchronized means the latest reference is within FreshLimit.
	StateSynchronized
	// StateHoldover means the estimate extends an older reference with
	// monotonic elapsed time and bounded drift.
	StateHoldover
)

// String returns a stable lowercase name for logs.
func (s State) String() string {
	switch s {
	case StateSynchronized:
		return "synchronized"
	case StateHoldover:
		return "holdover"
	default:
		return "unsynchronized"
	}
}

// ServiceConfig configures a Service. All limits are experimental.
type ServiceConfig struct {
	// Monotonic returns elapsed time on a monotonic clock.
	Monotonic func() time.Duration
	// FreshLimit bounds the reference age for StateSynchronized.
	FreshLimit time.Duration
	// HoldoverLimit bounds the reference age for StateHoldover.
	HoldoverLimit time.Duration
	// MaxDriftPPM bounds local oscillator drift during holdover.
	MaxDriftPPM uint32
	// StepThreshold is the largest correction applied within an epoch.
	// Larger corrections start a new epoch instead of silently
	// rewriting the timeline.
	StepThreshold time.Duration
}

func (cfg ServiceConfig) validate() error {
	switch {
	case cfg.Monotonic == nil:
		return fmt.Errorf("%w: monotonic clock required", ErrInvalidConfig)
	case cfg.FreshLimit <= 0:
		return fmt.Errorf("%w: fresh limit must be positive", ErrInvalidConfig)
	case cfg.HoldoverLimit < cfg.FreshLimit:
		return fmt.Errorf("%w: holdover limit below fresh limit", ErrInvalidConfig)
	case cfg.MaxDriftPPM > partsPerMillion:
		return fmt.Errorf("%w: drift above %d ppm", ErrInvalidConfig, partsPerMillion)
	case cfg.StepThreshold <= 0:
		return fmt.Errorf("%w: step threshold must be positive", ErrInvalidConfig)
	}
	return nil
}

// Reference is one validated UTC observation from a time source.
type Reference struct {
	Source Source
	// UTC is the observed instant. It must carry the UTC location.
	UTC time.Time
	// Monotonic is the monotonic reading at which UTC held.
	Monotonic time.Duration
	// Uncertainty bounds the error of UTC at Monotonic.
	Uncertainty time.Duration
}

// Estimate is the current venue UTC estimate.
type Estimate struct {
	// UTC is zero when State is StateUnsynchronized.
	UTC         time.Time
	Uncertainty time.Duration
	State       State
	Source      Source
	// Epoch identifies a continuous UTC timeline. It starts at 1 and
	// increments on every step or resynchronization after loss.
	Epoch uint64
}

// UpdateResult reports how a reference changed the timeline.
type UpdateResult struct {
	Epoch uint64
	// Stepped reports a correction beyond StepThreshold.
	Stepped bool
	// Correction is the reference UTC minus the prior estimate.
	Correction time.Duration
}

// Service estimates venue UTC from one authoritative source at a time.
// It is safe for concurrent use.
type Service struct {
	cfg ServiceConfig

	mu     sync.Mutex
	ref    Reference
	hasRef bool
	epoch  uint64
}

// NewService validates cfg and returns an unsynchronized Service.
func NewService(cfg ServiceConfig) (*Service, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Service{cfg: cfg}, nil
}

// Now returns the venue UTC estimate at the current monotonic reading.
func (s *Service) Now() Estimate {
	now := s.cfg.Monotonic()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.estimateLocked(now)
}

func (s *Service) estimateLocked(at time.Duration) Estimate {
	if !s.hasRef {
		return Estimate{State: StateUnsynchronized, Epoch: s.epoch}
	}
	elapsed := at - s.ref.Monotonic
	state := s.stateFor(elapsed)
	if state == StateUnsynchronized {
		return Estimate{State: state, Source: s.ref.Source, Epoch: s.epoch}
	}
	return Estimate{
		UTC:         s.ref.UTC.Add(elapsed),
		Uncertainty: s.ref.Uncertainty + scaleByPPM(elapsed, s.cfg.MaxDriftPPM),
		State:       state,
		Source:      s.ref.Source,
		Epoch:       s.epoch,
	}
}

func (s *Service) stateFor(elapsed time.Duration) State {
	switch {
	case elapsed <= s.cfg.FreshLimit:
		return StateSynchronized
	case elapsed <= s.cfg.HoldoverLimit:
		return StateHoldover
	default:
		return StateUnsynchronized
	}
}

// Update applies a reference. A lower-priority source cannot replace a
// fresh higher-priority source. Corrections beyond StepThreshold start a
// new epoch.
func (s *Service) Update(ref Reference) (UpdateResult, error) {
	now := s.cfg.Monotonic()
	if err := validateReference(ref, now); err != nil {
		return UpdateResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.hasRef && ref.Monotonic < s.ref.Monotonic {
		return UpdateResult{}, ErrStaleReference
	}
	current := s.estimateLocked(now)
	if current.State == StateSynchronized && ref.Source < current.Source {
		return UpdateResult{}, fmt.Errorf("%w: %v while %v is fresh", ErrLowerPriority, ref.Source, current.Source)
	}

	result := s.applyLocked(ref)
	s.ref = ref
	s.hasRef = true
	return result, nil
}

// applyLocked decides whether ref continues the current epoch.
func (s *Service) applyLocked(ref Reference) UpdateResult {
	prior := s.estimateLocked(ref.Monotonic)
	if prior.State == StateUnsynchronized {
		// No continuous timeline exists to correct.
		s.epoch++
		return UpdateResult{Epoch: s.epoch}
	}
	correction := ref.UTC.Sub(prior.UTC)
	stepped := absDuration(correction) > s.cfg.StepThreshold
	if stepped {
		s.epoch++
	}
	return UpdateResult{Epoch: s.epoch, Stepped: stepped, Correction: correction}
}

func validateReference(ref Reference, now time.Duration) error {
	switch {
	case ref.Source != SourceGPS && ref.Source != SourceNTP:
		return fmt.Errorf("%w: unknown source %d", ErrInvalidReference, ref.Source)
	case !isUTC(ref.UTC):
		return fmt.Errorf("%w: instant must carry the UTC location", ErrInvalidReference)
	case ref.Uncertainty < 0:
		return fmt.Errorf("%w: negative uncertainty", ErrInvalidReference)
	case ref.Monotonic > now:
		return fmt.Errorf("%w: monotonic reading in the future", ErrInvalidReference)
	}
	return nil
}
