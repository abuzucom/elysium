package ingest

import (
	"net"
	"os"
	"sync"
	"time"
)

// datagramClass names the protocol of a datagram on a shared 5-tuple.
type datagramClass uint8

const (
	classUnknown datagramClass = iota
	classDTLS
	classRTP
	classRTCP
)

// First-byte ranges from RFC 7983 section 7, and the RTCP packet type
// range from RFC 5761 section 4.
const (
	dtlsFirstByteMin   = 20
	dtlsFirstByteMax   = 63
	rtpFirstByteMin    = 128
	rtpFirstByteMax    = 191
	rtcpPayloadTypeMin = 64
	rtcpPayloadTypeMax = 95
	payloadTypeMask    = 0x7f
)

// classifyDatagram identifies DTLS, RTP, and RTCP datagrams.
func classifyDatagram(datagram []byte) datagramClass {
	if len(datagram) == 0 {
		return classUnknown
	}
	first := datagram[0]
	if first >= dtlsFirstByteMin && first <= dtlsFirstByteMax {
		return classDTLS
	}
	if first < rtpFirstByteMin || first > rtpFirstByteMax || len(datagram) < 2 {
		return classUnknown
	}
	payloadType := datagram[1] & payloadTypeMask
	if payloadType >= rtcpPayloadTypeMin && payloadType <= rtcpPayloadTypeMax {
		return classRTCP
	}
	return classRTP
}

// datagramConn presents the demultiplexed DTLS datagrams of one peer as
// the net.PacketConn that pion/dtls reads. The inbound queue is bounded.
// Writes go straight to the shared UDP socket.
type datagramConn struct {
	inbound chan []byte
	write   func([]byte) (int, error)
	local   net.Addr
	remote  net.Addr

	closeOnce sync.Once
	closed    chan struct{}

	mu              sync.Mutex
	readDeadline    time.Time
	deadlineChanged chan struct{}
}

func newDatagramConn(local, remote net.Addr, write func([]byte) (int, error)) *datagramConn {
	return &datagramConn{
		inbound:         make(chan []byte, handshakeQueueDatagrams),
		write:           write,
		local:           local,
		remote:          remote,
		closed:          make(chan struct{}),
		deadlineChanged: make(chan struct{}),
	}
}

// deliver queues a copy of datagram without blocking. It reports false
// when the queue is full or the conn is closed.
func (c *datagramConn) deliver(datagram []byte) bool {
	select {
	case <-c.closed:
		return false
	default:
	}
	select {
	case c.inbound <- append([]byte(nil), datagram...):
		return true
	default:
		return false
	}
}

// ReadFrom implements net.PacketConn.
func (c *datagramConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		deadline, changed := c.readDeadline, c.deadlineChanged
		c.mu.Unlock()

		timer, expired := deadlineTimer(deadline)
		if expired {
			return 0, nil, os.ErrDeadlineExceeded
		}
		var timeout <-chan time.Time
		if timer != nil {
			timeout = timer.C
		}
		select {
		case datagram := <-c.inbound:
			stopTimer(timer)
			return copy(p, datagram), c.remote, nil
		case <-c.closed:
			stopTimer(timer)
			return 0, nil, net.ErrClosed
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		case <-changed:
			stopTimer(timer)
		}
	}
}

func deadlineTimer(deadline time.Time) (*time.Timer, bool) {
	if deadline.IsZero() {
		return nil, false
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, true
	}
	return time.NewTimer(remaining), false
}

func stopTimer(timer *time.Timer) {
	if timer != nil {
		timer.Stop()
	}
}

// WriteTo implements net.PacketConn. The peer address is fixed.
func (c *datagramConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	return c.write(p)
}

// Close implements net.PacketConn. It is safe to call more than once.
func (c *datagramConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

// LocalAddr implements net.PacketConn.
func (c *datagramConn) LocalAddr() net.Addr { return c.local }

// SetDeadline implements net.PacketConn.
func (c *datagramConn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}

// SetReadDeadline implements net.PacketConn. It wakes a blocked read so
// the new deadline takes effect at once.
func (c *datagramConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	close(c.deadlineChanged)
	c.deadlineChanged = make(chan struct{})
	return nil
}

// SetWriteDeadline implements net.PacketConn. UDP writes do not block on
// the peer, so the conn ignores write deadlines.
func (c *datagramConn) SetWriteDeadline(time.Time) error { return nil }
