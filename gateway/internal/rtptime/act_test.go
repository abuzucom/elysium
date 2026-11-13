package rtptime

import (
	"bytes"
	"encoding"
	"errors"
	"math"
	"testing"
)

var (
	_ encoding.BinaryMarshaler   = AbsCaptureTime{}
	_ encoding.BinaryUnmarshaler = (*AbsCaptureTime)(nil)
)

// halfSecondQ32 is 0.5 seconds in Q32.32 fixed point.
const halfSecondQ32 = int64(1) << 31

func TestAbsCaptureTimeMarshalsTimestampOnly(t *testing.T) {
	value := AbsCaptureTime{CaptureTimestamp: 0x0123456789abcdef}

	got, err := value.MarshalBinary()

	if err != nil {
		t.Fatalf("MarshalBinary() error = %v, want nil", err)
	}
	want := []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	if !bytes.Equal(got, want) {
		t.Errorf("MarshalBinary() = % x, want % x", got, want)
	}
}

func TestAbsCaptureTimeMarshalsNegativeClockOffset(t *testing.T) {
	value := AbsCaptureTime{
		CaptureTimestamp:               0x0123456789abcdef,
		EstimatedCaptureClockOffset:    -halfSecondQ32,
		HasEstimatedCaptureClockOffset: true,
	}

	got, err := value.MarshalBinary()

	if err != nil {
		t.Fatalf("MarshalBinary() error = %v, want nil", err)
	}
	want := []byte{
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0xff, 0xff, 0xff, 0xff, 0x80, 0x00, 0x00, 0x00,
	}
	if !bytes.Equal(got, want) {
		t.Errorf("MarshalBinary() = % x, want % x", got, want)
	}
}

func TestAbsCaptureTimeRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		value   AbsCaptureTime
		wantLen int
	}{
		{"timestamp only", AbsCaptureTime{CaptureTimestamp: 0xe8a1b2c3d4e5f607}, 8},
		{"max timestamp", AbsCaptureTime{CaptureTimestamp: math.MaxUint64}, 8},
		{"zero offset present", AbsCaptureTime{
			CaptureTimestamp: 1, HasEstimatedCaptureClockOffset: true,
		}, 16},
		{"positive offset", AbsCaptureTime{
			CaptureTimestamp: 2, EstimatedCaptureClockOffset: halfSecondQ32, HasEstimatedCaptureClockOffset: true,
		}, 16},
		{"min offset", AbsCaptureTime{
			CaptureTimestamp: 3, EstimatedCaptureClockOffset: math.MinInt64, HasEstimatedCaptureClockOffset: true,
		}, 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := tt.value.MarshalBinary()
			if err != nil {
				t.Fatalf("MarshalBinary() error = %v, want nil", err)
			}
			if len(encoded) != tt.wantLen {
				t.Fatalf("MarshalBinary() length = %d, want %d", len(encoded), tt.wantLen)
			}

			var decoded AbsCaptureTime
			if err := decoded.UnmarshalBinary(encoded); err != nil {
				t.Fatalf("UnmarshalBinary() error = %v, want nil", err)
			}
			if decoded != tt.value {
				t.Errorf("round trip = %+v, want %+v", decoded, tt.value)
			}
		})
	}
}

func TestAbsCaptureTimeOmitsOffsetValueWhenAbsent(t *testing.T) {
	value := AbsCaptureTime{CaptureTimestamp: 7, EstimatedCaptureClockOffset: halfSecondQ32}

	got, err := value.MarshalBinary()

	if err != nil {
		t.Fatalf("MarshalBinary() error = %v, want nil", err)
	}
	want := []byte{0, 0, 0, 0, 0, 0, 0, 7}
	if !bytes.Equal(got, want) {
		t.Errorf("MarshalBinary() = % x, want % x", got, want)
	}
}

func TestAbsCaptureTimeRejectsInvalidLength(t *testing.T) {
	original := AbsCaptureTime{
		CaptureTimestamp:               42,
		EstimatedCaptureClockOffset:    -1,
		HasEstimatedCaptureClockOffset: true,
	}
	for _, length := range []int{0, 1, 7, 9, 15, 17, 24} {
		value := original

		err := value.UnmarshalBinary(make([]byte, length))

		if !errors.Is(err, ErrInvalidAbsCaptureTime) {
			t.Errorf("length %d: UnmarshalBinary() error = %v, want %v", length, err, ErrInvalidAbsCaptureTime)
		}
		if value != original {
			t.Errorf("length %d: value changed to %+v, want %+v", length, value, original)
		}
	}
}
