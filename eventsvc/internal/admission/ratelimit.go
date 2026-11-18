package admission

import "time"

// rateSweepThreshold triggers a sweep of idle keys. It bounds memory from
// keys that never return.
const rateSweepThreshold = 1024

// rateLimiter counts events per key in a sliding window. Keys are source
// addresses or device identifiers. The caller serializes access.
type rateLimiter[K comparable] struct {
	limit  int
	window time.Duration
	// maxKeys caps the tracked keys.
	maxKeys int
	history map[K][]time.Duration
}

func newRateLimiter[K comparable](limit int, window time.Duration, maxKeys int) *rateLimiter[K] {
	return &rateLimiter[K]{
		limit:   limit,
		window:  window,
		maxKeys: maxKeys,
		history: make(map[K][]time.Duration),
	}
}

// allow records one event at now and reports whether it fits the limit.
func (r *rateLimiter[K]) allow(key K, now time.Duration) bool {
	if r.blocked(key, now) {
		return false
	}
	r.record(key, now)
	return true
}

// blocked reports whether one more event for key would exceed the limit.
// An untracked key is blocked while the limiter tracks maxKeys active
// keys. Tracked keys keep their own limit.
func (r *rateLimiter[K]) blocked(key K, now time.Duration) bool {
	if len(r.history) > rateSweepThreshold {
		r.sweep(now)
	}
	stamps, tracked := r.history[key]
	if !tracked {
		if len(r.history) < r.maxKeys {
			return false
		}
		// Idle keys do not hold a slot. The sweep frees them whatever
		// the threshold.
		r.sweep(now)
		return len(r.history) >= r.maxKeys
	}
	recent := r.recent(stamps, now)
	r.history[key] = recent
	return len(recent) >= r.limit
}

// record adds one event at now. Callers check blocked first.
func (r *rateLimiter[K]) record(key K, now time.Duration) {
	r.history[key] = append(r.recent(r.history[key], now), now)
}

// recent returns the stamps still inside the window.
func (r *rateLimiter[K]) recent(stamps []time.Duration, now time.Duration) []time.Duration {
	first := 0
	for first < len(stamps) && now-stamps[first] >= r.window {
		first++
	}
	return stamps[first:]
}

// sweep drops keys with no stamp inside the window.
func (r *rateLimiter[K]) sweep(now time.Duration) {
	idle := make([]K, 0)
	for key, stamps := range r.history {
		if len(r.recent(stamps, now)) == 0 {
			idle = append(idle, key)
		}
	}
	for _, key := range idle {
		delete(r.history, key)
	}
}
