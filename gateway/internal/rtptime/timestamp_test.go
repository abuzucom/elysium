package rtptime

import (
	"errors"
	"math"
	"testing"
	"time"
)

// Test-only limits. They are not approved production values.
const (
	testSampleRateHz   = 48000
	testFrameSamples   = 480 // 10 ms Opus frame at 48 kHz
	testReorderWindow  = 100 * time.Millisecond
	testReorderSamples = 4800
	testMaxForwardJump = time.Second
	testMaxJumpSamples = 48000
	testSSRC           = 0x1234abcd
	rtpTimestampSpan   = int64(1) << 32
)

func newTestUnwrapper(t *testing.T) *Unwrapper {
	t.Helper()
	unwrapper, err := NewUnwrapper(UnwrapperConfig{
		SampleRateHz:   testSampleRateHz,
		ReorderWindow:  testReorderWindow,
		MaxForwardJump: testMaxForwardJump,
	})
	if err != nil {
		t.Fatalf("NewUnwrapper() error = %v, want nil", err)
	}
	return unwrapper
}

func assertObservation(t *testing.T, step string, got, want Observation) {
	t.Helper()
	if got != want {
		t.Errorf("%s: Observe() = %+v, want %+v", step, got, want)
	}
}

func TestUnwrapperFirstPacketStartsEpoch(t *testing.T) {
	unwrapper := newTestUnwrapper(t)

	got := unwrapper.Observe(testSSRC, 1000)

	assertObservation(t, "first packet", got, Observation{
		Epoch:             1,
		ExtendedTimestamp: 1000,
		Position:          0,
		NewEpoch:          true,
		Reason:            EpochReasonFirstPacket,
	})
}

func TestUnwrapperExtendsPositionAcrossWraparound(t *testing.T) {
	unwrapper := newTestUnwrapper(t)
	beforeWrap := uint32(rtpTimestampSpan - testFrameSamples)

	unwrapper.Observe(testSSRC, beforeWrap)
	got := unwrapper.Observe(testSSRC, 0)

	assertObservation(t, "packet after wrap", got, Observation{
		Epoch:             1,
		ExtendedTimestamp: rtpTimestampSpan,
		Position:          testFrameSamples,
	})
}

func TestUnwrapperCountsMultipleWraparounds(t *testing.T) {
	unwrapper := newTestUnwrapper(t)
	const step = testMaxJumpSamples / 2
	const packets = 400000 // spans more than two full 2^32 cycles
	start := uint32(rtpTimestampSpan - step)

	unwrapper.Observe(testSSRC, start)
	for i := int64(1); i <= packets; i++ {
		timestamp := uint32(int64(start) + i*step)
		got := unwrapper.Observe(testSSRC, timestamp)
		wantPosition := i * step
		if got.Epoch != 1 || got.NewEpoch || got.Position != wantPosition {
			t.Fatalf("packet %d: Observe() = %+v, want epoch 1 position %d", i, got, wantPosition)
		}
	}
}

func TestUnwrapperKeepsLatePacketWithoutRegression(t *testing.T) {
	unwrapper := newTestUnwrapper(t)

	unwrapper.Observe(testSSRC, 0)
	assertObservation(t, "after gap", unwrapper.Observe(testSSRC, 960), Observation{
		Epoch: 1, ExtendedTimestamp: 960, Position: 960,
	})
	assertObservation(t, "late packet", unwrapper.Observe(testSSRC, 480), Observation{
		Epoch: 1, ExtendedTimestamp: 480, Position: 480, Late: true,
	})
	assertObservation(t, "next in order", unwrapper.Observe(testSSRC, 1440), Observation{
		Epoch: 1, ExtendedTimestamp: 1440, Position: 1440,
	})
}

func TestUnwrapperPlacesLatePacketAcrossWraparound(t *testing.T) {
	unwrapper := newTestUnwrapper(t)
	beforeWrap := uint32(rtpTimestampSpan - testFrameSamples)

	unwrapper.Observe(testSSRC, beforeWrap)
	unwrapper.Observe(testSSRC, testFrameSamples)
	got := unwrapper.Observe(testSSRC, 0)

	assertObservation(t, "late packet at wrap", got, Observation{
		Epoch:             1,
		ExtendedTimestamp: rtpTimestampSpan,
		Position:          testFrameSamples,
		Late:              true,
	})
}

func TestUnwrapperReportsNegativePositionBeforeAnchor(t *testing.T) {
	unwrapper := newTestUnwrapper(t)

	unwrapper.Observe(testSSRC, testFrameSamples)
	got := unwrapper.Observe(testSSRC, 0)

	assertObservation(t, "late packet before anchor", got, Observation{
		Epoch:             1,
		ExtendedTimestamp: 0,
		Position:          -testFrameSamples,
		Late:              true,
	})
}

func TestUnwrapperStartsEpochOnSSRCChange(t *testing.T) {
	unwrapper := newTestUnwrapper(t)

	unwrapper.Observe(testSSRC, 0)
	unwrapper.Observe(testSSRC, testFrameSamples)
	got := unwrapper.Observe(testSSRC+1, 2*testFrameSamples)

	assertObservation(t, "new SSRC", got, Observation{
		Epoch:             2,
		ExtendedTimestamp: 2 * testFrameSamples,
		Position:          0,
		NewEpoch:          true,
		Reason:            EpochReasonSSRCChange,
	})
}

func TestUnwrapperStartsEpochOnForwardJumpBeyondLimit(t *testing.T) {
	unwrapper := newTestUnwrapper(t)

	unwrapper.Observe(testSSRC, 0)
	assertObservation(t, "jump equal to limit", unwrapper.Observe(testSSRC, testMaxJumpSamples), Observation{
		Epoch: 1, ExtendedTimestamp: testMaxJumpSamples, Position: testMaxJumpSamples,
	})

	beyond := uint32(2*testMaxJumpSamples + 1)
	assertObservation(t, "jump beyond limit", unwrapper.Observe(testSSRC, beyond), Observation{
		Epoch:             2,
		ExtendedTimestamp: int64(beyond),
		Position:          0,
		NewEpoch:          true,
		Reason:            EpochReasonForwardJump,
	})
}

func TestUnwrapperStartsEpochOnBackwardJumpBeyondReorderWindow(t *testing.T) {
	unwrapper := newTestUnwrapper(t)
	const highest = testMaxJumpSamples

	unwrapper.Observe(testSSRC, 0)
	unwrapper.Observe(testSSRC, highest)
	atWindow := uint32(highest - testReorderSamples)
	assertObservation(t, "step equal to window", unwrapper.Observe(testSSRC, atWindow), Observation{
		Epoch: 1, ExtendedTimestamp: int64(atWindow), Position: int64(atWindow), Late: true,
	})

	beyond := uint32(highest - testReorderSamples - 1)
	assertObservation(t, "step beyond window", unwrapper.Observe(testSSRC, beyond), Observation{
		Epoch:             2,
		ExtendedTimestamp: int64(beyond),
		Position:          0,
		NewEpoch:          true,
		Reason:            EpochReasonBackwardJump,
	})
}

func TestUnwrapperTreatsHalfRangeStepAsBackwardJump(t *testing.T) {
	unwrapper := newTestUnwrapper(t)

	unwrapper.Observe(testSSRC, 0)
	got := unwrapper.Observe(testSSRC, 1<<31)

	if !got.NewEpoch || got.Reason != EpochReasonBackwardJump {
		t.Errorf("Observe() = %+v, want new epoch with reason %v", got, EpochReasonBackwardJump)
	}
}

func TestNewUnwrapperRejectsInvalidConfig(t *testing.T) {
	valid := UnwrapperConfig{
		SampleRateHz:   testSampleRateHz,
		ReorderWindow:  testReorderWindow,
		MaxForwardJump: testMaxForwardJump,
	}
	tests := []struct {
		name   string
		mutate func(*UnwrapperConfig)
	}{
		{"zero sample rate", func(c *UnwrapperConfig) { c.SampleRateHz = 0 }},
		{"negative reorder window", func(c *UnwrapperConfig) { c.ReorderWindow = -time.Millisecond }},
		{"zero forward jump", func(c *UnwrapperConfig) { c.MaxForwardJump = 0 }},
		{"negative forward jump", func(c *UnwrapperConfig) { c.MaxForwardJump = -time.Second }},
		{"forward jump beyond signed RTP range", func(c *UnwrapperConfig) { c.MaxForwardJump = 24 * time.Hour }},
		{"reorder window beyond signed RTP range", func(c *UnwrapperConfig) { c.ReorderWindow = 24 * time.Hour }},
		{"forward jump overflows sample conversion", func(c *UnwrapperConfig) { c.MaxForwardJump = math.MaxInt64 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)

			unwrapper, err := NewUnwrapper(cfg)

			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("NewUnwrapper() error = %v, want %v", err, ErrInvalidConfig)
			}
			if unwrapper != nil {
				t.Errorf("NewUnwrapper() unwrapper = %+v, want nil", unwrapper)
			}
		})
	}
}

func TestEpochReasonString(t *testing.T) {
	tests := map[EpochReason]string{
		EpochReasonNone:         "none",
		EpochReasonFirstPacket:  "first_packet",
		EpochReasonSSRCChange:   "ssrc_change",
		EpochReasonForwardJump:  "forward_jump",
		EpochReasonBackwardJump: "backward_jump",
		EpochReason(255):        "unknown",
	}
	for reason, want := range tests {
		if got := reason.String(); got != want {
			t.Errorf("EpochReason(%d).String() = %q, want %q", uint8(reason), got, want)
		}
	}
}
