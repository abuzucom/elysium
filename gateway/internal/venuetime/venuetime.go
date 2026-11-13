// Package venuetime provides venue UTC estimates with bounded holdover and
// maps source sample positions to venue UTC (D-010).
//
// The package keeps venue UTC, monotonic elapsed time, and source sample
// positions distinct. Callers inject the monotonic clock. Every tuning
// value is caller configuration with no default, as docs/backlog.md
// tracks.
package venuetime

import (
	"errors"
	"time"
)

// Errors reported by Service and Mapper.
var (
	ErrInvalidConfig       = errors.New("venuetime: invalid configuration")
	ErrInvalidReference    = errors.New("venuetime: invalid time reference")
	ErrStaleReference      = errors.New("venuetime: reference older than current reference")
	ErrLowerPriority       = errors.New("venuetime: lower-priority source while higher-priority source is fresh")
	ErrInvalidAnchor       = errors.New("venuetime: invalid sample anchor")
	ErrNoAnchor            = errors.New("venuetime: no sample anchor")
	ErrMappingExpired      = errors.New("venuetime: sample mapping beyond holdover limit")
	ErrStreamEpochMismatch = errors.New("venuetime: stream epoch differs from anchor")
	ErrVenueEpochMismatch  = errors.New("venuetime: venue time epoch differs from anchor")
	ErrOutOfRange          = errors.New("venuetime: sample distance beyond exact conversion range")
)

const (
	partsPerMillion = 1_000_000
	nanosPerSecond  = int64(time.Second)
)

// scaleByPPM returns d scaled by ppm parts per million, truncated. The
// split multiplication cannot overflow for ppm up to one million.
func scaleByPPM(d time.Duration, ppm uint32) time.Duration {
	whole, rest := int64(d)/partsPerMillion, int64(d)%partsPerMillion
	scale := int64(ppm)
	return time.Duration(whole*scale + rest*scale/partsPerMillion)
}

// isUTC reports whether t carries the UTC location. Zone-less or local
// instants are ambiguous at service boundaries and are rejected.
func isUTC(t time.Time) bool {
	return !t.IsZero() && t.Location() == time.UTC
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
