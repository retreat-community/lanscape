// Package testengine runs LSTP/1 tests: the responder on port 47700 (TCP and UDP) and the
// initiator for ECHO, TCP and UDP throughput in forward, reverse and bidirectional mode,
// plus ICMP reachability and PMTU probes. It interoperates with Lanscape Mini agents.
package testengine

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/retreat-community/lanscape/internal/proto"
)

// Status values (shared with Mini).
const (
	StatusOK              = "ok"
	StatusRouteMismatch   = "route_mismatch"
	StatusUnreachable     = "unreachable"
	StatusRefused         = "refused"
	StatusProtocol        = "protocol"
	StatusAuthFail        = "auth_fail"
	StatusTimeout         = "timeout"
	StatusCounterMismatch = "counter_mismatch"
	StatusNoDevice        = "no_device"
	StatusUnsupported     = "unsupported"
	StatusInternal        = "internal"
	StatusBusy            = "busy"
	StatusMsgSize         = "msgsize"
	StatusLimit           = "limit"
	StatusSkipped         = "skipped"
	StatusOffline         = "offline"
)

// MiniStatus maps Mini numeric status codes to names.
var MiniStatus = []string{StatusOK, StatusRouteMismatch, StatusUnreachable, StatusRefused, StatusProtocol,
	StatusAuthFail, StatusTimeout, StatusCounterMismatch, StatusNoDevice, StatusUnsupported, StatusInternal,
	StatusBusy, StatusMsgSize, StatusLimit}

// Token is a per-run secret.
type Token [proto.TokenLen]byte

// NewToken returns a random run token and run id.
func NewToken() (uint32, Token, error) {
	var t Token
	var id [4]byte
	if _, err := rand.Read(t[:]); err != nil {
		return 0, t, err
	}
	if _, err := rand.Read(id[:]); err != nil {
		return 0, t, err
	}
	return binary.BigEndian.Uint32(id[:]), t, nil
}

// MAC computes HMAC-SHA256(token, n1 || n2).
func MAC(token Token, n1, n2 []byte) []byte {
	m := hmac.New(sha256.New, token[:])
	m.Write(n1)
	m.Write(n2)
	return m.Sum(nil)
}

// LabelledMAC computes HMAC-SHA256(key, label || a || b) for the Mini control plane.
func LabelledMAC(key []byte, label string, a, b []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(label))
	m.Write(a)
	m.Write(b)
	return m.Sum(nil)
}

func nonce() []byte {
	b := make([]byte, proto.NonceLen)
	_, _ = rand.Read(b)
	return b
}

// classify maps dial errors to statuses.
func classify(err error) string {
	var ne net.Error
	switch {
	case err == nil:
		return StatusOK
	case errors.Is(err, syscall.ECONNREFUSED):
		return StatusRefused
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return StatusUnreachable
	case errors.Is(err, syscall.ENODEV):
		return StatusNoDevice
	case errors.As(err, &ne) && ne.Timeout():
		return StatusUnreachable
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, proto.ErrBadMagic), errors.Is(err, proto.ErrMalformed):
		return StatusProtocol
	default:
		return StatusProtocol
	}
}

// udpHeader is 'L' 'U' | reserved u16 | session u32 | seq u32 | ts_us u64.
const udpHeaderLen = 20

func putUDPHeader(b []byte, session, seq uint32, tsUS uint64) {
	b[0], b[1], b[2], b[3] = 'L', 'U', 0, 0
	binary.BigEndian.PutUint32(b[4:], session)
	binary.BigEndian.PutUint32(b[8:], seq)
	binary.BigEndian.PutUint64(b[12:], tsUS)
}

func parseUDPHeader(b []byte) (session, seq uint32, tsUS uint64, ok bool) {
	if len(b) < udpHeaderLen || b[0] != 'L' || b[1] != 'U' {
		return 0, 0, 0, false
	}
	return binary.BigEndian.Uint32(b[4:]), binary.BigEndian.Uint32(b[8:]), binary.BigEndian.Uint64(b[12:]), true
}

var epoch = time.Now()

func nowUS() uint64 { return uint64(time.Since(epoch).Microseconds()) }

// udpStats accumulates received datagrams (loss by sequence numbers, RFC 3550 jitter).
type udpStats struct {
	bytes     uint64
	counted   uint64
	recv      uint64
	maxSeq    uint32
	haveSeq   bool
	first     time.Time
	warmEnd   time.Time
	last      time.Time
	jitter    float64
	lastTrans int64
	haveTrans bool
	sent      uint64
}

func (s *udpStats) add(n int, seq uint32, tsUS uint64, warmup time.Duration) {
	now := time.Now()
	if s.first.IsZero() {
		s.first = now
		s.warmEnd = now.Add(warmup)
	}
	s.recv++
	s.bytes += uint64(n)
	if !s.haveSeq || seq > s.maxSeq {
		s.maxSeq, s.haveSeq = seq, true
	}
	if !now.Before(s.warmEnd) {
		s.counted += uint64(n)
		s.last = now
	}
	transit := int64(nowUS()) - int64(tsUS)
	if s.haveTrans {
		d := transit - s.lastTrans
		if d < 0 {
			d = -d
		}
		s.jitter += (float64(d) - s.jitter) / 16
	}
	s.lastTrans, s.haveTrans = transit, true
}

func (s *udpStats) window() uint64 {
	if s.last.After(s.warmEnd) {
		return uint64(s.last.Sub(s.warmEnd).Microseconds())
	}
	return 0
}
