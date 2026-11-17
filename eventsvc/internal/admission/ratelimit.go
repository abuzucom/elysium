package admission

import (
	"net/netip"
	"time"
)

// rateSweepThreshold triggers a sweep of idle addresses. It bounds memory
// from addresses that never return.
const rateSweepThreshold = 1024

// rateLimiter counts events per source address in a sliding window. The
// caller serializes access.
type rateLimiter struct {
	limit  int
	window time.Duration
	// maxAddresses caps the tracked addresses.
	maxAddresses int
	history      map[netip.Addr][]time.Duration
}

func newRateLimiter(limit int, window time.Duration, maxAddresses int) *rateLimiter {
	return &rateLimiter{
		limit:        limit,
		window:       window,
		maxAddresses: maxAddresses,
		history:      make(map[netip.Addr][]time.Duration),
	}
}

// allow records one event at now and reports whether it fits the limit.
// An untracked address is rejected while the limiter tracks maxAddresses
// active addresses. Tracked addresses keep their own limit.
func (r *rateLimiter) allow(address netip.Addr, now time.Duration) bool {
	if len(r.history) > rateSweepThreshold {
		r.sweep(now)
	}
	stamps, tracked := r.history[address]
	if !tracked && len(r.history) >= r.maxAddresses {
		// Idle addresses do not hold a slot. The sweep frees them
		// whatever the threshold.
		r.sweep(now)
		if len(r.history) >= r.maxAddresses {
			return false
		}
	}
	recent := r.recent(stamps, now)
	if len(recent) >= r.limit {
		r.history[address] = recent
		return false
	}
	r.history[address] = append(recent, now)
	return true
}

// recent returns the stamps still inside the window.
func (r *rateLimiter) recent(stamps []time.Duration, now time.Duration) []time.Duration {
	first := 0
	for first < len(stamps) && now-stamps[first] >= r.window {
		first++
	}
	return stamps[first:]
}

// sweep drops addresses with no stamp inside the window.
func (r *rateLimiter) sweep(now time.Duration) {
	idle := make([]netip.Addr, 0)
	for address, stamps := range r.history {
		if len(r.recent(stamps, now)) == 0 {
			idle = append(idle, address)
		}
	}
	for _, address := range idle {
		delete(r.history, address)
	}
}
