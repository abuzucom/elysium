package admission

import "time"

// SyncState mirrors the venue time service estimate states.
type SyncState uint8

// Venue time states.
const (
	// SyncStateUnsynchronized means no usable venue UTC exists.
	SyncStateUnsynchronized SyncState = iota
	// SyncStateSynchronized means a fresh reference backs the estimate.
	SyncStateSynchronized
	// SyncStateHoldover means the estimate extends an older reference
	// with bounded drift.
	SyncStateHoldover
)

// VenueTime is one venue UTC estimate.
type VenueTime struct {
	// UTC is zero when State is SyncStateUnsynchronized.
	UTC         time.Time
	Uncertainty time.Duration
	State       SyncState
}

// usable reports whether the estimate can back an expiry decision.
func (v VenueTime) usable() bool {
	return v.State != SyncStateUnsynchronized && !v.UTC.IsZero() && v.Uncertainty >= 0
}

// latest returns the latest instant the true venue time may have reached.
func (v VenueTime) latest() time.Time {
	return v.UTC.Add(v.Uncertainty)
}

// VenueClock reads the authoritative venue UTC estimate. The venue time
// service on the controller implements it.
type VenueClock interface {
	Now() VenueTime
}
