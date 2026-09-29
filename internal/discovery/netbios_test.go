package discovery

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// nbstatAnswer builds a node status response with a computer name, a workgroup and a MAC.
func nbstatAnswer(id uint16) []byte {
	b := binary.BigEndian.AppendUint16(nil, id)
	b = append(b, 0x84, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00) // response, 1 answer
	b = append(b, nbstatQuery(0)[12:12+34]...)                                // name
	b = append(b, 0x00, 0x21, 0x00, 0x01, 0, 0, 0, 0)                         // NBSTAT IN ttl
	entry := func(name string, suffix byte, group bool) []byte {
		n := []byte(name)
		for len(n) < 15 {
			n = append(n, ' ')
		}
		flags := uint16(0x0400)
		if group {
			flags |= 0x8000
		}
		return binary.BigEndian.AppendUint16(append(n, suffix), flags)
	}
	data := []byte{3}
	data = append(data, entry("OFFICE-PC", 0x00, false)...)
	data = append(data, entry("WORKGROUP", 0x00, true)...)
	data = append(data, entry("OFFICE-PC", 0x20, false)...)
	data = append(data, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55)
	data = append(data, make([]byte, 40)...) // statistics
	b = binary.BigEndian.AppendUint16(b, uint16(len(data)))
	return append(b, data...)
}

func TestParseNBStat(t *testing.T) {
	r, err := ParseNBStat(nbstatAnswer(7))
	if err != nil || r.Name != "OFFICE-PC" || r.Workgroup != "WORKGROUP" || r.MAC != "00:11:22:33:44:55" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := ParseNBStat([]byte{1, 2, 3}); err == nil {
		t.Error("short packet parsed")
	}
	q := nbstatQuery(1)
	if len(q) != 50 || q[12] != 0x20 || string(q[13:15]) != "CK" {
		t.Errorf("query % x", q)
	}
}

func TestNetBIOS(t *testing.T) {
	nb, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip(err)
	}
	defer nb.Close()
	ll, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip(err)
	}
	defer ll.Close()
	oldNB, oldLL := nbPort, llmnrPort
	defer func() { nbPort, llmnrPort = oldNB, oldLL }()
	nbPort, llmnrPort = nb.LocalAddr().(*net.UDPAddr).Port, ll.LocalAddr().(*net.UDPAddr).Port

	go func() { // NetBIOS responder
		buf := make([]byte, 512)
		for {
			n, from, err := nb.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n == 50 {
				_, _ = nb.WriteToUDP(nbstatAnswer(binary.BigEndian.Uint16(buf[:2])), from)
			}
		}
	}()
	go func() { // LLMNR responder
		buf := make([]byte, 512)
		for {
			n, from, err := ll.ReadFromUDP(buf)
			if err != nil {
				return
			}
			var m dnsmessage.Message
			if m.Unpack(buf[:n]) != nil || len(m.Questions) != 1 {
				continue
			}
			target, _ := dnsmessage.NewName("printer.lan.")
			m.Response = true
			m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: m.Questions[0].Name, Type: dnsmessage.TypePTR,
				Class: dnsmessage.ClassINET, TTL: 30}, Body: &dnsmessage.PTRResource{PTR: target}}}
			b, _ := m.Pack()
			_, _ = ll.WriteToUDP(b, from)
		}
	}()
	items, err := NetBIOS(context.Background(), []string{"127.0.0.1", "not-an-ip"}, 100, 300*time.Millisecond)
	if err != nil || len(items) != 1 {
		t.Fatalf("%v %+v", err, items)
	}
	it := items[0]
	if it.Name != "OFFICE-PC" || it.Labels["workgroup"] != "WORKGROUP" || it.Labels["llmnr"] != "printer.lan" || it.IPs[0] != "127.0.0.1" {
		t.Errorf("%+v", it)
	}
}
