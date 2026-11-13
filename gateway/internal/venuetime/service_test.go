package venuetime

import (
	"errors"
	"testing"
	"time"
)

// Test-only limits. They are not approved production values.
const (
	testFreshLimit    = 2 * time.Second
	testHoldoverLimit = 60 * time.Second
	testDriftPPM      = 50
	testStepThreshold = 5 * time.Millisecond
	testUncertainty   = time.Millisecond
)

// fakeMonotonic is an injected monotonic clock for tests.
type fakeMonotonic struct{ now time.Duration }

func (c *fakeMonotonic) Now() time.Duration { return c.now }

func (c *fakeMonotonic) Advance(d time.Duration) { c.now += d }

var testUTC = time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)

func newTestService(t *testing.T) (*Service, *fakeMonotonic) {
	t.Helper()
	clock := &fakeMonotonic{now: time.Hour}
	service, err := NewService(ServiceConfig{
		Monotonic:     clock.Now,
		FreshLimit:    testFreshLimit,
		HoldoverLimit: testHoldoverLimit,
		MaxDriftPPM:   testDriftPPM,
		StepThreshold: testStepThreshold,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, clock
}

func reference(source Source, utc time.Time, monotonic time.Duration) Reference {
	return Reference{Source: source, UTC: utc, Monotonic: monotonic, Uncertainty: testUncertainty}
}

func TestServiceStartsUnsynchronized(t *testing.T) {
	service, _ := newTestService(t)

	got := service.Now()

	if got.State != StateUnsynchronized || got.Epoch != 0 {
		t.Errorf("Now() = %+v, want unsynchronized with epoch 0", got)
	}
}

func TestServiceExtendsReferenceWithMonotonicElapsed(t *testing.T) {
	service, clock := newTestService(t)
	if _, err := service.Update(reference(SourceGPS, testUTC, clock.Now())); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	clock.Advance(1500 * time.Millisecond)

	got := service.Now()

	want := Estimate{
		UTC:         testUTC.Add(1500 * time.Millisecond),
		Uncertainty: testUncertainty + 75*time.Microsecond, // 1.5 s at 50 ppm
		State:       StateSynchronized,
		Source:      SourceGPS,
		Epoch:       1,
	}
	if got != want {
		t.Errorf("Now() = %+v, want %+v", got, want)
	}
}

func TestServiceEntersHoldoverThenUnsynchronized(t *testing.T) {
	service, clock := newTestService(t)
	if _, err := service.Update(reference(SourceGPS, testUTC, clock.Now())); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	clock.Advance(testFreshLimit + time.Millisecond)
	if got := service.Now().State; got != StateHoldover {
		t.Errorf("after fresh limit State = %v, want %v", got, StateHoldover)
	}
	clock.Advance(testHoldoverLimit)
	if got := service.Now().State; got != StateUnsynchronized {
		t.Errorf("after holdover limit State = %v, want %v", got, StateUnsynchronized)
	}
}

func TestServiceKeepsEpochForSmallCorrection(t *testing.T) {
	service, clock := newTestService(t)
	if _, err := service.Update(reference(SourceGPS, testUTC, clock.Now())); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	clock.Advance(time.Second)

	result, err := service.Update(reference(SourceGPS, testUTC.Add(time.Second+2*time.Millisecond), clock.Now()))

	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if result.Stepped || result.Epoch != 1 || result.Correction != 2*time.Millisecond {
		t.Errorf("Update() = %+v, want correction 2ms in epoch 1 without step", result)
	}
}

func TestServiceStartsNewEpochOnStep(t *testing.T) {
	service, clock := newTestService(t)
	if _, err := service.Update(reference(SourceGPS, testUTC, clock.Now())); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	clock.Advance(time.Second)

	result, err := service.Update(reference(SourceGPS, testUTC.Add(time.Second-20*time.Millisecond), clock.Now()))

	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !result.Stepped || result.Epoch != 2 || result.Correction != -20*time.Millisecond {
		t.Errorf("Update() = %+v, want step to epoch 2 with correction -20ms", result)
	}
	if got := service.Now().Epoch; got != 2 {
		t.Errorf("Now().Epoch = %d, want 2", got)
	}
}

func TestServiceRejectsNTPWhileGPSFresh(t *testing.T) {
	service, clock := newTestService(t)
	if _, err := service.Update(reference(SourceGPS, testUTC, clock.Now())); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	clock.Advance(time.Second)

	_, err := service.Update(reference(SourceNTP, testUTC.Add(time.Second), clock.Now()))

	if !errors.Is(err, ErrLowerPriority) {
		t.Errorf("Update(NTP) error = %v, want %v", err, ErrLowerPriority)
	}
	if got := service.Now().Source; got != SourceGPS {
		t.Errorf("Now().Source = %v, want %v", got, SourceGPS)
	}
}

func TestServiceAcceptsNTPWhenGPSInHoldover(t *testing.T) {
	service, clock := newTestService(t)
	if _, err := service.Update(reference(SourceGPS, testUTC, clock.Now())); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	clock.Advance(testFreshLimit + time.Second)

	_, err := service.Update(reference(SourceNTP, testUTC.Add(testFreshLimit+time.Second), clock.Now()))

	if err != nil {
		t.Fatalf("Update(NTP) error = %v, want nil during GPS holdover", err)
	}
	if got := service.Now(); got.Source != SourceNTP || got.State != StateSynchronized {
		t.Errorf("Now() = %+v, want synchronized from NTP", got)
	}
}

func TestServiceRejectsInvalidReferences(t *testing.T) {
	tests := []struct {
		name string
		ref  func(now time.Duration) Reference
		want error
	}{
		{"non-UTC location", func(now time.Duration) Reference {
			return reference(SourceGPS, testUTC.In(time.FixedZone("venue", 3600)), now)
		}, ErrInvalidReference},
		{"unknown source", func(now time.Duration) Reference {
			return reference(SourceNone, testUTC, now)
		}, ErrInvalidReference},
		{"negative uncertainty", func(now time.Duration) Reference {
			ref := reference(SourceGPS, testUTC, now)
			ref.Uncertainty = -time.Millisecond
			return ref
		}, ErrInvalidReference},
		{"monotonic reading in the future", func(now time.Duration) Reference {
			return reference(SourceGPS, testUTC, now+time.Millisecond)
		}, ErrInvalidReference},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, clock := newTestService(t)

			_, err := service.Update(tt.ref(clock.Now()))

			if !errors.Is(err, tt.want) {
				t.Errorf("Update() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestServiceRejectsReferenceOlderThanCurrent(t *testing.T) {
	service, clock := newTestService(t)
	start := clock.Now()
	clock.Advance(time.Second)
	if _, err := service.Update(reference(SourceGPS, testUTC, clock.Now())); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	_, err := service.Update(reference(SourceGPS, testUTC.Add(-time.Second), start))

	if !errors.Is(err, ErrStaleReference) {
		t.Errorf("Update() error = %v, want %v", err, ErrStaleReference)
	}
}

func TestNewServiceRejectsInvalidConfig(t *testing.T) {
	clock := &fakeMonotonic{}
	valid := ServiceConfig{
		Monotonic:     clock.Now,
		FreshLimit:    testFreshLimit,
		HoldoverLimit: testHoldoverLimit,
		MaxDriftPPM:   testDriftPPM,
		StepThreshold: testStepThreshold,
	}
	tests := []struct {
		name   string
		mutate func(*ServiceConfig)
	}{
		{"missing monotonic clock", func(c *ServiceConfig) { c.Monotonic = nil }},
		{"zero fresh limit", func(c *ServiceConfig) { c.FreshLimit = 0 }},
		{"holdover below fresh limit", func(c *ServiceConfig) { c.HoldoverLimit = c.FreshLimit - time.Millisecond }},
		{"drift beyond one million ppm", func(c *ServiceConfig) { c.MaxDriftPPM = 1_000_001 }},
		{"zero step threshold", func(c *ServiceConfig) { c.StepThreshold = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)

			service, err := NewService(cfg)

			if !errors.Is(err, ErrInvalidConfig) || service != nil {
				t.Errorf("NewService() = %v, %v; want nil, %v", service, err, ErrInvalidConfig)
			}
		})
	}
}
