package venuetime

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// MapperConfig configures a Mapper. All limits are experimental.
type MapperConfig struct {
	// SampleRateHz is the nominal source sample rate.
	SampleRateHz uint32
	// Monotonic returns elapsed time on a monotonic clock.
	Monotonic func() time.Duration
	// FreshLimit bounds the anchor age for a non-holdover mapping.
	FreshLimit time.Duration
	// HoldoverLimit bounds the anchor age for any mapping (D-010).
	HoldoverLimit time.Duration
	// SourceDriftPPM bounds the source sample clock deviation from the
	// nominal rate.
	SourceDriftPPM uint32
}

func (cfg MapperConfig) validate() error {
	switch {
	case cfg.SampleRateHz == 0:
		return fmt.Errorf("%w: sample rate must be positive", ErrInvalidConfig)
	case cfg.Monotonic == nil:
		return fmt.Errorf("%w: monotonic clock required", ErrInvalidConfig)
	case cfg.FreshLimit <= 0:
		return fmt.Errorf("%w: fresh limit must be positive", ErrInvalidConfig)
	case cfg.HoldoverLimit < cfg.FreshLimit:
		return fmt.Errorf("%w: holdover limit below fresh limit", ErrInvalidConfig)
	case cfg.SourceDriftPPM > partsPerMillion:
		return fmt.Errorf("%w: drift above %d ppm", ErrInvalidConfig, partsPerMillion)
	}
	return nil
}

// Anchor ties one source sample position to a venue UTC instant.
type Anchor struct {
	// StreamEpoch is the rtptime epoch of Position.
	StreamEpoch uint64
	// VenueEpoch is the venue time epoch of UTC. It starts at 1.
	VenueEpoch uint64
	// Position is the epoch-relative source sample position.
	Position int64
	// UTC is the venue instant of Position. It must carry the UTC
	// location.
	UTC         time.Time
	Uncertainty time.Duration
	// Monotonic is the monotonic reading when the anchor was taken.
	Monotonic time.Duration
}

// Mapping is the venue UTC instant of one source sample position.
type Mapping struct {
	UTC         time.Time
	Uncertainty time.Duration
	// Holdover reports an anchor older than FreshLimit.
	Holdover bool
}

// Mapper maps source sample positions to venue UTC from the latest
// anchor at the nominal sample rate. It is safe for concurrent use.
type Mapper struct {
	cfg MapperConfig

	mu        sync.Mutex
	anchor    Anchor
	hasAnchor bool
}

// NewMapper validates cfg and returns a Mapper without an anchor.
func NewMapper(cfg MapperConfig) (*Mapper, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Mapper{cfg: cfg}, nil
}

// SetAnchor replaces the current anchor.
func (m *Mapper) SetAnchor(anchor Anchor) error {
	now := m.cfg.Monotonic()
	switch {
	case !isUTC(anchor.UTC):
		return fmt.Errorf("%w: instant must carry the UTC location", ErrInvalidAnchor)
	case anchor.Uncertainty < 0:
		return fmt.Errorf("%w: negative uncertainty", ErrInvalidAnchor)
	case anchor.VenueEpoch == 0:
		return fmt.Errorf("%w: venue epoch must be at least 1", ErrInvalidAnchor)
	case anchor.Monotonic > now:
		return fmt.Errorf("%w: monotonic reading in the future", ErrInvalidAnchor)
	}
	m.mu.Lock()
	m.anchor = anchor
	m.hasAnchor = true
	m.mu.Unlock()
	return nil
}

// Map returns the venue UTC instant of position. It refuses positions
// from another stream or venue epoch and anchors beyond HoldoverLimit.
func (m *Mapper) Map(streamEpoch, venueEpoch uint64, position int64) (Mapping, error) {
	now := m.cfg.Monotonic()
	m.mu.Lock()
	anchor, ok := m.anchor, m.hasAnchor
	m.mu.Unlock()

	switch {
	case !ok:
		return Mapping{}, ErrNoAnchor
	case streamEpoch != anchor.StreamEpoch:
		return Mapping{}, ErrStreamEpochMismatch
	case venueEpoch != anchor.VenueEpoch:
		return Mapping{}, ErrVenueEpochMismatch
	}
	age := now - anchor.Monotonic
	if age > m.cfg.HoldoverLimit {
		return Mapping{}, ErrMappingExpired
	}
	offset, err := m.samplesToDuration(position, anchor.Position)
	if err != nil {
		return Mapping{}, err
	}
	return Mapping{
		UTC:         anchor.UTC.Add(offset),
		Uncertainty: anchor.Uncertainty + scaleByPPM(absDuration(offset), m.cfg.SourceDriftPPM),
		Holdover:    age > m.cfg.FreshLimit,
	}, nil
}

// samplesToDuration converts position minus origin to time at the
// nominal rate with exact integer arithmetic, truncating toward zero.
func (m *Mapper) samplesToDuration(position, origin int64) (time.Duration, error) {
	delta := position - origin
	if (position >= origin) != (delta >= 0) {
		return 0, ErrOutOfRange
	}
	const maxExactSamples = math.MaxInt64 / nanosPerSecond
	if delta > maxExactSamples || delta < -maxExactSamples {
		return 0, fmt.Errorf("%w: %d samples", ErrOutOfRange, delta)
	}
	return time.Duration(delta * nanosPerSecond / int64(m.cfg.SampleRateHz)), nil
}
