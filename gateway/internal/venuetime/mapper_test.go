package venuetime

import (
	"errors"
	"testing"
	"time"
)

// Test-only limits. They are not approved production values.
const (
	testSampleRateHz      = 48000
	testMapperFresh       = time.Second
	testMapperHoldover    = 5 * time.Second
	testSourceDriftPPM    = 100
	testAnchorUncertainty = 2 * time.Millisecond
)

func newTestMapper(t *testing.T) (*Mapper, *fakeMonotonic) {
	t.Helper()
	clock := &fakeMonotonic{now: time.Hour}
	mapper, err := NewMapper(MapperConfig{
		SampleRateHz:   testSampleRateHz,
		Monotonic:      clock.Now,
		FreshLimit:     testMapperFresh,
		HoldoverLimit:  testMapperHoldover,
		SourceDriftPPM: testSourceDriftPPM,
	})
	if err != nil {
		t.Fatalf("NewMapper() error = %v", err)
	}
	return mapper, clock
}

func testAnchor(monotonic time.Duration) Anchor {
	return Anchor{
		StreamEpoch: 3,
		VenueEpoch:  1,
		Position:    96000,
		UTC:         testUTC,
		Uncertainty: testAnchorUncertainty,
		Monotonic:   monotonic,
	}
}

func TestMapperMapsPositionAtNominalRate(t *testing.T) {
	mapper, clock := newTestMapper(t)
	if err := mapper.SetAnchor(testAnchor(clock.Now())); err != nil {
		t.Fatalf("SetAnchor() error = %v", err)
	}

	got, err := mapper.Map(3, 1, 96000+48000+480)

	if err != nil {
		t.Fatalf("Map() error = %v", err)
	}
	want := Mapping{
		UTC:         testUTC.Add(1010 * time.Millisecond),
		Uncertainty: testAnchorUncertainty + 101*time.Microsecond, // 1.01 s at 100 ppm
	}
	if got != want {
		t.Errorf("Map() = %+v, want %+v", got, want)
	}
}

func TestMapperMapsPositionBeforeAnchor(t *testing.T) {
	mapper, clock := newTestMapper(t)
	if err := mapper.SetAnchor(testAnchor(clock.Now())); err != nil {
		t.Fatalf("SetAnchor() error = %v", err)
	}

	got, err := mapper.Map(3, 1, 96000-480)

	if err != nil {
		t.Fatalf("Map() error = %v", err)
	}
	if want := testUTC.Add(-10 * time.Millisecond); !got.UTC.Equal(want) {
		t.Errorf("Map().UTC = %v, want %v", got.UTC, want)
	}
}

func TestMapperTruncatesPartialNanoseconds(t *testing.T) {
	mapper, clock := newTestMapper(t)
	if err := mapper.SetAnchor(testAnchor(clock.Now())); err != nil {
		t.Fatalf("SetAnchor() error = %v", err)
	}

	got, err := mapper.Map(3, 1, 96000+1)

	if err != nil {
		t.Fatalf("Map() error = %v", err)
	}
	// One sample at 48 kHz lasts 20833.33 ns.
	if want := testUTC.Add(20833 * time.Nanosecond); !got.UTC.Equal(want) {
		t.Errorf("Map().UTC = %v, want %v", got.UTC, want)
	}
}

func TestMapperHoldsMappingWithinHoldoverLimit(t *testing.T) {
	mapper, clock := newTestMapper(t)
	if err := mapper.SetAnchor(testAnchor(clock.Now())); err != nil {
		t.Fatalf("SetAnchor() error = %v", err)
	}
	clock.Advance(testMapperHoldover)

	got, err := mapper.Map(3, 1, 96000)

	if err != nil {
		t.Fatalf("Map() at holdover limit error = %v", err)
	}
	if !got.Holdover {
		t.Errorf("Map().Holdover = false, want true past the fresh limit")
	}
}

func TestMapperExpiresMappingBeyondHoldoverLimit(t *testing.T) {
	mapper, clock := newTestMapper(t)
	if err := mapper.SetAnchor(testAnchor(clock.Now())); err != nil {
		t.Fatalf("SetAnchor() error = %v", err)
	}
	clock.Advance(testMapperHoldover + time.Nanosecond)

	_, err := mapper.Map(3, 1, 96000)

	if !errors.Is(err, ErrMappingExpired) {
		t.Errorf("Map() error = %v, want %v", err, ErrMappingExpired)
	}
}

func TestMapperRejectsMismatchedEpochs(t *testing.T) {
	tests := []struct {
		name        string
		streamEpoch uint64
		venueEpoch  uint64
		want        error
	}{
		{"stream restarted", 4, 1, ErrStreamEpochMismatch},
		{"venue clock stepped", 3, 2, ErrVenueEpochMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper, clock := newTestMapper(t)
			if err := mapper.SetAnchor(testAnchor(clock.Now())); err != nil {
				t.Fatalf("SetAnchor() error = %v", err)
			}

			_, err := mapper.Map(tt.streamEpoch, tt.venueEpoch, 96000)

			if !errors.Is(err, tt.want) {
				t.Errorf("Map() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestMapperRequiresAnchor(t *testing.T) {
	mapper, _ := newTestMapper(t)

	_, err := mapper.Map(3, 1, 96000)

	if !errors.Is(err, ErrNoAnchor) {
		t.Errorf("Map() error = %v, want %v", err, ErrNoAnchor)
	}
}

func TestMapperRejectsDistanceBeyondExactRange(t *testing.T) {
	mapper, clock := newTestMapper(t)
	if err := mapper.SetAnchor(testAnchor(clock.Now())); err != nil {
		t.Fatalf("SetAnchor() error = %v", err)
	}

	_, err := mapper.Map(3, 1, 1<<62)

	if !errors.Is(err, ErrOutOfRange) {
		t.Errorf("Map() error = %v, want %v", err, ErrOutOfRange)
	}
}

func TestMapperRejectsInvalidAnchor(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Anchor)
	}{
		{"non-UTC location", func(a *Anchor) { a.UTC = a.UTC.In(time.FixedZone("venue", 3600)) }},
		{"negative uncertainty", func(a *Anchor) { a.Uncertainty = -time.Nanosecond }},
		{"zero venue epoch", func(a *Anchor) { a.VenueEpoch = 0 }},
		{"monotonic reading in the future", func(a *Anchor) { a.Monotonic += time.Millisecond }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper, clock := newTestMapper(t)
			anchor := testAnchor(clock.Now())
			tt.mutate(&anchor)

			err := mapper.SetAnchor(anchor)

			if !errors.Is(err, ErrInvalidAnchor) {
				t.Errorf("SetAnchor() error = %v, want %v", err, ErrInvalidAnchor)
			}
		})
	}
}

func TestNewMapperRejectsInvalidConfig(t *testing.T) {
	clock := &fakeMonotonic{}
	valid := MapperConfig{
		SampleRateHz:   testSampleRateHz,
		Monotonic:      clock.Now,
		FreshLimit:     testMapperFresh,
		HoldoverLimit:  testMapperHoldover,
		SourceDriftPPM: testSourceDriftPPM,
	}
	tests := []struct {
		name   string
		mutate func(*MapperConfig)
	}{
		{"zero sample rate", func(c *MapperConfig) { c.SampleRateHz = 0 }},
		{"missing monotonic clock", func(c *MapperConfig) { c.Monotonic = nil }},
		{"zero fresh limit", func(c *MapperConfig) { c.FreshLimit = 0 }},
		{"holdover below fresh limit", func(c *MapperConfig) { c.HoldoverLimit = c.FreshLimit - time.Nanosecond }},
		{"drift beyond one million ppm", func(c *MapperConfig) { c.SourceDriftPPM = 1_000_001 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)

			mapper, err := NewMapper(cfg)

			if !errors.Is(err, ErrInvalidConfig) || mapper != nil {
				t.Errorf("NewMapper() = %v, %v; want nil, %v", mapper, err, ErrInvalidConfig)
			}
		})
	}
}
