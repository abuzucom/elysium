// Package rtptime validates RTP media timing for the venue ingest gateway.
//
// The package keeps source sample time separate from venue UTC and from
// monotonic elapsed time. Positions are exact integer sample counts.
package rtptime

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// maxSignedRTPStep is the largest timestamp step that a signed 32-bit
// difference represents without ambiguity. Larger limits would let a
// forward step alias a backward step.
const maxSignedRTPStep = math.MaxInt32

// ErrInvalidConfig reports an unusable Unwrapper configuration.
var ErrInvalidConfig = errors.New("rtptime: invalid unwrapper configuration")

// EpochReason names the cause of a new stream epoch.
type EpochReason uint8

// Epoch reasons reported by Unwrapper.Observe.
const (
	// EpochReasonNone marks an observation inside the current epoch.
	EpochReasonNone EpochReason = iota
	// EpochReasonFirstPacket marks the first packet of the stream.
	EpochReasonFirstPacket
	// EpochReasonSSRCChange marks a packet from a different source SSRC.
	EpochReasonSSRCChange
	// EpochReasonForwardJump marks a step beyond the forward jump limit.
	EpochReasonForwardJump
	// EpochReasonBackwardJump marks a step beyond the reorder window.
	EpochReasonBackwardJump
)

// String returns a stable lowercase name for logs and metrics.
func (r EpochReason) String() string {
	switch r {
	case EpochReasonNone:
		return "none"
	case EpochReasonFirstPacket:
		return "first_packet"
	case EpochReasonSSRCChange:
		return "ssrc_change"
	case EpochReasonForwardJump:
		return "forward_jump"
	case EpochReasonBackwardJump:
		return "backward_jump"
	default:
		return "unknown"
	}
}

// UnwrapperConfig holds the timing limits for one RTP source stream.
//
// ReorderWindow and MaxForwardJump are experimental tuning values. The
// package supplies no defaults. See docs/backlog.md.
type UnwrapperConfig struct {
	// SampleRateHz is the RTP clock rate. Opus uses 48000.
	SampleRateHz uint32
	// ReorderWindow bounds how far behind the highest timestamp a packet
	// may arrive and still count as late within the current epoch.
	ReorderWindow time.Duration
	// MaxForwardJump bounds the forward timestamp step that stays within
	// the current epoch. Packet loss produces steps up to this limit.
	MaxForwardJump time.Duration
}

// Observation places one RTP packet on the source sample timeline.
type Observation struct {
	// Epoch identifies the continuous timeline segment. It starts at 1.
	Epoch uint64
	// ExtendedTimestamp is the RTP timestamp extended to 64 bits within
	// the epoch.
	ExtendedTimestamp int64
	// Position counts samples from the first packet of the epoch. A late
	// packet that precedes the first packet has a negative position.
	Position int64
	// Late reports a packet older than the highest timestamp seen in the
	// epoch. Late packets never move the timeline backward.
	Late bool
	// NewEpoch reports that this packet started a new epoch.
	NewEpoch bool
	// Reason states why a new epoch started. It is EpochReasonNone
	// otherwise.
	Reason EpochReason
}

// Unwrapper extends 32-bit RTP timestamps into a 64-bit sample timeline
// and detects stream epoch boundaries.
//
// An Unwrapper serves one ingest session. It is not safe for concurrent
// use.
type Unwrapper struct {
	reorderWindowSamples  int64
	maxForwardJumpSamples int64

	started bool
	epoch   uint64
	ssrc    uint32
	anchor  int64
	highest int64
}

// NewUnwrapper validates cfg and returns an Unwrapper.
func NewUnwrapper(cfg UnwrapperConfig) (*Unwrapper, error) {
	if cfg.SampleRateHz == 0 {
		return nil, fmt.Errorf("%w: sample rate must be positive", ErrInvalidConfig)
	}
	if cfg.MaxForwardJump <= 0 {
		return nil, fmt.Errorf("%w: max forward jump must be positive", ErrInvalidConfig)
	}
	reorderSamples, err := durationToSamples(cfg.ReorderWindow, cfg.SampleRateHz)
	if err != nil {
		return nil, fmt.Errorf("reorder window: %w", err)
	}
	forwardSamples, err := durationToSamples(cfg.MaxForwardJump, cfg.SampleRateHz)
	if err != nil {
		return nil, fmt.Errorf("max forward jump: %w", err)
	}
	return &Unwrapper{
		reorderWindowSamples:  reorderSamples,
		maxForwardJumpSamples: forwardSamples,
	}, nil
}

// durationToSamples converts d to whole samples at rateHz, truncating
// any partial sample. It rejects negative durations and results beyond
// the unambiguous signed RTP step range.
func durationToSamples(d time.Duration, rateHz uint32) (int64, error) {
	if d < 0 {
		return 0, fmt.Errorf("%w: duration %v is negative", ErrInvalidConfig, d)
	}
	rate := int64(rateHz)
	if int64(d) > math.MaxInt64/rate {
		return 0, fmt.Errorf("%w: duration %v overflows sample conversion", ErrInvalidConfig, d)
	}
	samples := int64(d) * rate / int64(time.Second)
	if samples > maxSignedRTPStep {
		return 0, fmt.Errorf("%w: duration %v exceeds %d samples", ErrInvalidConfig, d, maxSignedRTPStep)
	}
	return samples, nil
}

// Observe places one packet with the given SSRC and RTP timestamp on the
// source timeline.
func (u *Unwrapper) Observe(ssrc uint32, timestamp uint32) Observation {
	if !u.started {
		return u.startEpoch(ssrc, timestamp, EpochReasonFirstPacket)
	}
	if ssrc != u.ssrc {
		return u.startEpoch(ssrc, timestamp, EpochReasonSSRCChange)
	}

	// Modular subtraction followed by a signed reinterpretation yields
	// the shortest step across a 2^32 wraparound. A step of exactly
	// -2^31 is ambiguous and always exceeds the validated reorder window.
	step := int64(int32(timestamp - uint32(u.highest)))
	if step > u.maxForwardJumpSamples {
		return u.startEpoch(ssrc, timestamp, EpochReasonForwardJump)
	}
	if -step > u.reorderWindowSamples {
		return u.startEpoch(ssrc, timestamp, EpochReasonBackwardJump)
	}

	extended := u.highest + step
	if step > 0 {
		u.highest = extended
	}
	return Observation{
		Epoch:             u.epoch,
		ExtendedTimestamp: extended,
		Position:          extended - u.anchor,
		Late:              step < 0,
	}
}

// startEpoch anchors a new epoch at timestamp. The epoch-local extended
// timeline restarts at the raw 32-bit timestamp value.
func (u *Unwrapper) startEpoch(ssrc uint32, timestamp uint32, reason EpochReason) Observation {
	u.started = true
	u.epoch++
	u.ssrc = ssrc
	u.anchor = int64(timestamp)
	u.highest = u.anchor
	return Observation{
		Epoch:             u.epoch,
		ExtendedTimestamp: u.anchor,
		Position:          0,
		NewEpoch:          true,
		Reason:            reason,
	}
}
