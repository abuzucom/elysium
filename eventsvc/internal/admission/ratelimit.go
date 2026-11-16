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
	limit   int
	window  time.Duration
	history map[netip.Addr][]time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, history: make(map[netip.Addr][]time.Duration)}
}

// allow records one event at now and reports whether it fits the limit.
func (r *rateLimiter) allow(address netip.Addr, now time.Duration) bool {
	if len(r.history) > rateSweepThreshold {
		r.sweep(now)
	}
	recent := r.recent(r.history[address], now)
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
