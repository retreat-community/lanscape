package proto

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	inner := NewBuilder().String(TIfName, "eth0").U16(TIfVLAN, 300)
	b := NewBuilder().U8(TTestKind, KindTCP).U16(TUDPPort, 47700).U32(TRunID, 0xdeadbeef).
		U64(TBytes, 0x0102030405060708).String(TAgentID, "node-1").Nested(TIface, inner)
	var buf bytes.Buffer
	if err := Write(&buf, Hello, b); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	if raw[0] != 'L' || raw[1] != 'S' || raw[2] != 1 || raw[3] != Hello {
		t.Fatalf("bad header % x", raw[:6])
	}
	for i := 0; i < len(raw); i++ {
		if _, n, err := Parse(raw[:i]); n != 0 || err != nil {
			t.Fatalf("prefix %d: n=%d err=%v", i, n, err)
		}
	}
	f, n, err := Parse(raw)
	if err != nil || n != len(raw) || f.Type != Hello {
		t.Fatalf("parse: %v %d", err, n)
	}
	if v, _ := f.Payload.U8(TTestKind); v != KindTCP {
		t.Error("kind")
	}
	if v, _ := f.Payload.U16(TUDPPort); v != 47700 {
		t.Error("port")
	}
	if v, _ := f.Payload.U32(TRunID); v != 0xdeadbeef {
		t.Error("run id")
	}
	if v, _ := f.Payload.U64(TBytes); v != 0x0102030405060708 {
		t.Error("bytes")
	}
	if f.Payload.Str(TAgentID) != "node-1" {
		t.Error("agent id")
	}
	if _, ok := f.Payload.U32(TTestKind); ok {
		t.Error("wrong-size value must be absent")
	}
	nested, _ := f.Payload.Get(TIface)
	if TLV(nested).Str(TIfName) != "eth0" {
		t.Error("nested")
	}
	rf, err := Read(bytes.NewReader(raw))
	if err != nil || rf.Type != Hello {
		t.Fatal(err)
	}
}

func TestMalformed(t *testing.T) {
	good, _ := Encode(Ready, TLV{0x0a, 0, 4, 1, 2, 3, 4})
	bad := append([]byte(nil), good...)
	bad[0] = 'X'
	if _, _, err := Parse(bad); !errors.Is(err, ErrBadMagic) {
		t.Error("magic")
	}
	bad = append([]byte(nil), good...)
	bad[2] = 2
	if _, _, err := Parse(bad); !errors.Is(err, ErrBadMagic) {
		t.Error("version")
	}
	bad = append([]byte(nil), good...)
	bad[HeaderLen+2] = 9
	if _, _, err := Parse(bad); !errors.Is(err, ErrMalformed) {
		t.Error("tlv")
	}
	if _, err := Read(bytes.NewReader(good[:len(good)-1])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short read: %v", err)
	}
	if _, err := NewBuilder().Bytes(1, make([]byte, 70000)).TLV(); !errors.Is(err, ErrTooLarge) {
		t.Error("too large")
	}
}

// Frames produced by the C implementation (mini/common/tlv.c) must decode identically.
func TestMiniCompat(t *testing.T) {
	// LS 01 04 len=7 | SESSION(0x0a) len 4 = 0x01020304
	raw := []byte{'L', 'S', 1, Ready, 0, 7, TSession, 0, 4, 1, 2, 3, 4}
	f, n, err := Parse(raw)
	if err != nil || n != len(raw) {
		t.Fatal(err)
	}
	if s, _ := f.Payload.U32(TSession); s != 0x01020304 {
		t.Errorf("session %x", s)
	}
}

func FuzzParse(f *testing.F) {
	g, _ := Encode(Hello, TLV{1, 0, 1, 'a'})
	f.Add(g)
	f.Fuzz(func(t *testing.T, b []byte) {
		fr, n, err := Parse(b)
		if err == nil && n > 0 {
			if n > len(b) || !fr.Payload.Valid() {
				t.Fatal("invalid parse")
			}
		}
	})
}
