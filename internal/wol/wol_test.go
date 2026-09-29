package wol

import (
	"bytes"
	"context"
	"net"
	"testing"
)

func TestMagicPacket(t *testing.T) {
	p, err := MagicPacket("AA:bb:cc:00:11:22")
	if err != nil || len(p) != 102 {
		t.Fatalf("%d %v", len(p), err)
	}
	if !bytes.Equal(p[:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}) || !bytes.Equal(p[96:], []byte{0xaa, 0xbb, 0xcc, 0, 0x11, 0x22}) {
		t.Errorf("payload % x", p)
	}
	for _, bad := range []string{"", "zz:bb:cc:00:11:22", "00:11:22:33:44:55:66:77"} {
		if _, err := MagicPacket(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBroadcast(t *testing.T) {
	for in, want := range map[string]string{"192.168.1.10/24": "192.168.1.255", "10.1.2.3/20": "10.1.15.255", "172.16.0.1/31": "172.16.0.1"} {
		ip, n, _ := net.ParseCIDR(in)
		n.IP = ip
		if got := broadcast(n).String(); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestSendTo(t *testing.T) {
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	p, _ := MagicPacket("00:11:22:33:44:55")
	if err := sendTo(context.Background(), l.LocalAddr().String(), p); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 200)
	n, _, err := l.ReadFrom(buf)
	if err != nil || !bytes.Equal(buf[:n], p) {
		t.Fatalf("%d %v", n, err)
	}
}
