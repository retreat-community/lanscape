// Package proto implements LSTP/1, the data-plane protocol shared by Lanscape Mini and
// Lanscape Full, plus the Mini control plane and the Full control-plane messages.
// See docs/PROTOCOL.md.
package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Protocol constants.
const (
	Version    = 1
	HeaderLen  = 6
	MaxPayload = 65535
	NonceLen   = 16
	TokenLen   = 32
	DataPort   = 47700
	MiniPort   = 47701
	TokenTTL   = 600 // seconds
	WarmupMS   = 1000
)

// Data-plane frame types.
const (
	Hello      = 0x01
	Challenge  = 0x02
	Auth       = 0x03
	Ready      = 0x04
	Start      = 0x05
	Result     = 0x06
	SenderStat = 0x07
	EchoReq    = 0x08
	EchoRep    = 0x09
	Error      = 0x0a
	Bye        = 0x0b
)

// Mini control-plane frame types.
const (
	CHello     = 0x20
	CChallenge = 0x21
	CAuth      = 0x22
	CWelcome   = 0x23
	CInventory = 0x24
	CPing      = 0x25
	CPong      = 0x26
	CGrant     = 0x27
	CGrantAck  = 0x28
	CCmd       = 0x29
	CCmdResult = 0x2a
	CCancel    = 0x2b
)

// Tags.
const (
	TAgentID     = 0x01
	TRunID       = 0x02
	TTestKind    = 0x03
	TDirection   = 0x04
	TStreams     = 0x05
	TDurationMS  = 0x06
	TNonce       = 0x07
	TMAC         = 0x08
	TRole        = 0x09
	TSession     = 0x0a
	TStreamIdx   = 0x0b
	TBytes       = 0x0c
	TWindowUS    = 0x0d
	TCPU         = 0x0e
	TSeq         = 0x0f
	TTsUS        = 0x10
	TErrCode     = 0x11
	TErrMsg      = 0x12
	TUDPRateKbps = 0x13
	TUDPPort     = 0x14
	TPktSize     = 0x15
	TPktsSent    = 0x16
	TPktsRecv    = 0x17
	TJitterUS    = 0x18
	TLost        = 0x19
	TIfTX        = 0x1a
	TIfRX        = 0x1b
	TPathOK      = 0x1c
	TVersion     = 0x1d
	TWarmupMS    = 0x1e

	TIface    = 0x40
	TIfName   = 0x41
	TIfMAC    = 0x42
	TIfAddr4  = 0x43
	TIfPrefix = 0x44
	TIfVLAN   = 0x45
	TIfSpeed  = 0x46
	TIfMTU    = 0x47
	TIfFlags  = 0x48
	TIfKind   = 0x49
	TIfParent = 0x4a
	THostname = 0x4b
	THostID   = 0x4c
	TArch     = 0x4d
	TCaps     = 0x4e

	TCmdID      = 0x50
	TCmdKind    = 0x51
	TSrcDev     = 0x52
	TSrcAddr    = 0x53
	TDstAddr    = 0x54
	TCount      = 0x55
	TIntervalMS = 0x56
	TSize       = 0x57
	TRunToken   = 0x58
	TTTLS       = 0x59
	TStatus     = 0x5a
	TRTTMinUS   = 0x5b
	TRTTAvgUS   = 0x5c
	TRTTMaxUS   = 0x5d
	TSent       = 0x5e
	TRecv       = 0x5f
	TPeerCPU    = 0x60
	TBPS        = 0x61
	TRouteDev   = 0x62
	TRTTP95US   = 0x63
	TServerMAC  = 0x65
	TLocalCPU   = 0x66
	TDstPort    = 0x67
)

// Test kinds, directions and roles.
const (
	KindTCP  = 1
	KindUDP  = 2
	KindEcho = 3

	DirForward = 0
	DirReverse = 1
	DirBidir   = 2

	RoleCtrl = 0
	RoleData = 1
)

// Mini command kinds and capability bits.
const (
	CmdPing = 1
	CmdPMTU = 2
	CmdLSTP = 3

	CapLite = 1
	CapICMP = 2
	CapTCP  = 4
	CapUDP  = 8
)

// Data-plane error codes.
const (
	ErrAuth        = 1
	ErrUnknownRun  = 2
	ErrUnsupported = 3
	ErrLimit       = 4
	ErrBusy        = 5
	ErrProto       = 6
)

// Errors returned by the codec.
var (
	ErrBadMagic  = errors.New("lstp: bad magic or version")
	ErrMalformed = errors.New("lstp: malformed tlv payload")
	ErrTooLarge  = errors.New("lstp: payload too large")
)

// Frame is one decoded LSTP frame.
type Frame struct {
	Type    byte
	Payload TLV
}

// TLV is an encoded TLV sequence.
type TLV []byte

// Builder accumulates TLV items for a frame payload.
type Builder struct {
	buf []byte
	err error
}

// NewBuilder returns an empty builder.
func NewBuilder() *Builder { return &Builder{buf: make([]byte, 0, 256)} }

// Bytes appends a raw value.
func (b *Builder) Bytes(tag byte, v []byte) *Builder {
	if len(v) > 0xffff {
		b.err = ErrTooLarge
		return b
	}
	b.buf = append(b.buf, tag, byte(len(v)>>8), byte(len(v)))
	b.buf = append(b.buf, v...)
	return b
}

// String appends a string value.
func (b *Builder) String(tag byte, s string) *Builder { return b.Bytes(tag, []byte(s)) }

// U8 appends a one-byte value.
func (b *Builder) U8(tag byte, v uint8) *Builder { return b.Bytes(tag, []byte{v}) }

// U16 appends a big-endian uint16.
func (b *Builder) U16(tag byte, v uint16) *Builder {
	return b.Bytes(tag, binary.BigEndian.AppendUint16(nil, v))
}

// U32 appends a big-endian uint32.
func (b *Builder) U32(tag byte, v uint32) *Builder {
	return b.Bytes(tag, binary.BigEndian.AppendUint32(nil, v))
}

// U64 appends a big-endian uint64.
func (b *Builder) U64(tag byte, v uint64) *Builder {
	return b.Bytes(tag, binary.BigEndian.AppendUint64(nil, v))
}

// Nested appends a nested TLV sequence.
func (b *Builder) Nested(tag byte, inner *Builder) *Builder {
	if inner.err != nil {
		b.err = inner.err
	}
	return b.Bytes(tag, inner.buf)
}

// TLV returns the payload built so far.
func (b *Builder) TLV() (TLV, error) {
	if b.err != nil {
		return nil, b.err
	}
	if len(b.buf) > MaxPayload {
		return nil, ErrTooLarge
	}
	return TLV(b.buf), nil
}

// Encode serialises a frame.
func Encode(typ byte, payload TLV) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, ErrTooLarge
	}
	out := make([]byte, HeaderLen, HeaderLen+len(payload))
	out[0], out[1], out[2], out[3] = 'L', 'S', Version, typ
	binary.BigEndian.PutUint16(out[4:], uint16(len(payload)))
	return append(out, payload...), nil
}

// Write encodes and writes a frame built with b.
func Write(w io.Writer, typ byte, b *Builder) error {
	p, err := b.TLV()
	if err != nil {
		return err
	}
	f, err := Encode(typ, p)
	if err != nil {
		return err
	}
	_, err = w.Write(f)
	return err
}

// Parse decodes a frame from buf. It returns the frame, the number of bytes consumed,
// and (0, nil) when more data is needed.
func Parse(buf []byte) (Frame, int, error) {
	if (len(buf) >= 1 && buf[0] != 'L') || (len(buf) >= 2 && buf[1] != 'S') ||
		(len(buf) >= 3 && buf[2] != Version) {
		return Frame{}, 0, ErrBadMagic
	}
	if len(buf) < HeaderLen {
		return Frame{}, 0, nil
	}
	n := int(binary.BigEndian.Uint16(buf[4:]))
	if len(buf) < HeaderLen+n {
		return Frame{}, 0, nil
	}
	p := TLV(buf[HeaderLen : HeaderLen+n])
	if !p.Valid() {
		return Frame{}, 0, ErrMalformed
	}
	return Frame{Type: buf[3], Payload: p}, HeaderLen + n, nil
}

// Read reads exactly one frame from r.
func Read(r io.Reader) (Frame, error) {
	var h [HeaderLen]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Frame{}, err
	}
	if h[0] != 'L' || h[1] != 'S' || h[2] != Version {
		return Frame{}, ErrBadMagic
	}
	n := int(binary.BigEndian.Uint16(h[4:]))
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	if !TLV(p).Valid() {
		return Frame{}, ErrMalformed
	}
	return Frame{Type: h[3], Payload: p}, nil
}

// Each calls fn for every item; it stops at the first malformed item.
func (t TLV) Each(fn func(tag byte, v []byte) bool) error {
	for off := 0; off < len(t); {
		if len(t)-off < 3 {
			return ErrMalformed
		}
		n := int(binary.BigEndian.Uint16(t[off+1:]))
		if n > len(t)-off-3 {
			return ErrMalformed
		}
		if !fn(t[off], t[off+3:off+3+n]) {
			return nil
		}
		off += 3 + n
	}
	return nil
}

// Valid reports whether t splits exactly into TLV items.
func (t TLV) Valid() bool { return t.Each(func(byte, []byte) bool { return true }) == nil }

// Get returns the first value for tag.
func (t TLV) Get(tag byte) ([]byte, bool) {
	var out []byte
	found := false
	_ = t.Each(func(tg byte, v []byte) bool {
		if tg == tag {
			out, found = v, true
			return false
		}
		return true
	})
	return out, found
}

// All returns every value for tag.
func (t TLV) All(tag byte) [][]byte {
	var out [][]byte
	_ = t.Each(func(tg byte, v []byte) bool {
		if tg == tag {
			out = append(out, v)
		}
		return true
	})
	return out
}

// U8 returns a one-byte value.
func (t TLV) U8(tag byte) (uint8, bool) {
	v, ok := t.Get(tag)
	if !ok || len(v) != 1 {
		return 0, false
	}
	return v[0], true
}

// U16 returns a uint16 value.
func (t TLV) U16(tag byte) (uint16, bool) {
	v, ok := t.Get(tag)
	if !ok || len(v) != 2 {
		return 0, false
	}
	return binary.BigEndian.Uint16(v), true
}

// U32 returns a uint32 value.
func (t TLV) U32(tag byte) (uint32, bool) {
	v, ok := t.Get(tag)
	if !ok || len(v) != 4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(v), true
}

// U64 returns a uint64 value.
func (t TLV) U64(tag byte) (uint64, bool) {
	v, ok := t.Get(tag)
	if !ok || len(v) != 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(v), true
}

// Str returns a string value.
func (t TLV) Str(tag byte) string {
	v, _ := t.Get(tag)
	return string(v)
}

// ErrorFrame describes an ERROR frame received from a peer.
type ErrorFrame struct {
	Code byte
	Msg  string
}

func (e *ErrorFrame) Error() string { return fmt.Sprintf("lstp: peer error %d: %s", e.Code, e.Msg) }
