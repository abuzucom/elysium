package rtptime

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// AbsCaptureTimeURI identifies the abs-capture-time RTP header extension.
const AbsCaptureTimeURI = "http://www.webrtc.org/experiments/rtp-hdrext/abs-capture-time"

// Payload sizes defined by the abs-capture-time extension. The short form
// carries only the capture timestamp. The long form adds the estimated
// capture clock offset.
const (
	absCaptureTimeShortLen = 8
	absCaptureTimeLongLen  = 16
)

// ErrInvalidAbsCaptureTime reports a malformed abs-capture-time payload.
var ErrInvalidAbsCaptureTime = errors.New("rtptime: invalid abs-capture-time extension")

// AbsCaptureTime holds the abs-capture-time header extension fields.
//
// Both values use Q32.32 fixed point to keep exact integer arithmetic.
type AbsCaptureTime struct {
	// CaptureTimestamp is the capture instant as an unsigned NTP Q32.32
	// value in the capturer clock domain.
	CaptureTimestamp uint64
	// EstimatedCaptureClockOffset is the signed Q32.32 offset in seconds
	// between the capturer clock and the sender clock. It is meaningful
	// only when HasEstimatedCaptureClockOffset is true.
	EstimatedCaptureClockOffset int64
	// HasEstimatedCaptureClockOffset selects the 16-byte payload form.
	HasEstimatedCaptureClockOffset bool
}

// MarshalBinary encodes the extension payload in network byte order. It
// emits 8 bytes without a clock offset and 16 bytes with one.
func (a AbsCaptureTime) MarshalBinary() ([]byte, error) {
	if !a.HasEstimatedCaptureClockOffset {
		return binary.BigEndian.AppendUint64(make([]byte, 0, absCaptureTimeShortLen), a.CaptureTimestamp), nil
	}
	data := make([]byte, 0, absCaptureTimeLongLen)
	data = binary.BigEndian.AppendUint64(data, a.CaptureTimestamp)
	data = binary.BigEndian.AppendUint64(data, uint64(a.EstimatedCaptureClockOffset))
	return data, nil
}

// UnmarshalBinary decodes an 8-byte or 16-byte extension payload. It
// leaves the receiver unchanged on error.
func (a *AbsCaptureTime) UnmarshalBinary(data []byte) error {
	switch len(data) {
	case absCaptureTimeShortLen:
		*a = AbsCaptureTime{CaptureTimestamp: binary.BigEndian.Uint64(data)}
		return nil
	case absCaptureTimeLongLen:
		*a = AbsCaptureTime{
			CaptureTimestamp:               binary.BigEndian.Uint64(data[:absCaptureTimeShortLen]),
			EstimatedCaptureClockOffset:    int64(binary.BigEndian.Uint64(data[absCaptureTimeShortLen:])),
			HasEstimatedCaptureClockOffset: true,
		}
		return nil
	default:
		return fmt.Errorf("%w: payload length %d, want %d or %d",
			ErrInvalidAbsCaptureTime, len(data), absCaptureTimeShortLen, absCaptureTimeLongLen)
	}
}
