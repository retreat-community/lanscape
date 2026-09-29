//go:build !lanscape_small

package discovery

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// serveZone answers one AXFR request in two messages (SOA … SOA), or refuses when refuse is set.
func serveZone(t *testing.T, l net.Listener, refuse bool) {
	c, err := l.Accept()
	if err != nil {
		return
	}
	defer c.Close()
	var n [2]byte
	if _, err := io.ReadFull(c, n[:]); err != nil {
		return
	}
	q := make([]byte, binary.BigEndian.Uint16(n[:]))
	if _, err := io.ReadFull(c, q); err != nil {
		return
	}
	var req dnsmessage.Message
	if req.Unpack(q) != nil || len(req.Questions) != 1 || req.Questions[0].Type != dnsmessage.TypeAXFR {
		t.Error("not an AXFR request")
		return
	}
	zone := req.Questions[0].Name
	name := func(s string) dnsmessage.Name { return dnsmessage.MustNewName(s) }
	hdr := func(n dnsmessage.Name, typ dnsmessage.Type) dnsmessage.ResourceHeader {
		return dnsmessage.ResourceHeader{Name: n, Type: typ, Class: dnsmessage.ClassINET, TTL: 300}
	}
	soa := dnsmessage.Resource{Header: hdr(zone, dnsmessage.TypeSOA), Body: &dnsmessage.SOAResource{NS: name("ns.home.arpa."),
		MBox: name("admin.home.arpa."), Serial: 1, Refresh: 60, Retry: 60, Expire: 600, MinTTL: 60}}
	msgs := []dnsmessage.Message{
		{Header: dnsmessage.Header{ID: req.ID, Response: true}, Questions: req.Questions, Answers: []dnsmessage.Resource{soa,
			{Header: hdr(name("nas.home.arpa."), dnsmessage.TypeA), Body: &dnsmessage.AResource{A: [4]byte{192, 168, 1, 167}}}}},
		{Header: dnsmessage.Header{ID: req.ID, Response: true}, Answers: []dnsmessage.Resource{
			{Header: hdr(name("prx0.home.arpa."), dnsmessage.TypeA), Body: &dnsmessage.AResource{A: [4]byte{192, 168, 1, 140}}}, soa}},
	}
	if refuse {
		msgs = []dnsmessage.Message{{Header: dnsmessage.Header{ID: req.ID, Response: true, RCode: dnsmessage.RCodeRefused}, Questions: req.Questions}}
	}
	for _, m := range msgs {
		b, err := m.Pack()
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = c.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(b))), b...))
	}
}

func TestAXFR(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	go serveZone(t, l, false)
	recs, err := AXFR(context.Background(), l.Addr().String(), "home.arpa")
	if err != nil || len(recs) != 2 || recs[0].name != "nas.home.arpa." || recs[1].ip != "192.168.1.140" {
		t.Fatalf("%v %+v", err, recs)
	}
	go serveZone(t, l, true)
	if _, err := AXFR(context.Background(), l.Addr().String(), "home.arpa."); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("refused transfer: %v", err)
	}
	// through the DNS source: records become named items
	go serveZone(t, l, false)
	items, err := DNSRecords(context.Background(), DNSConfig{AXFR: []string{l.Addr().String() + "/home.arpa"}})
	if err != nil || len(items) != 2 || items[0].Name != "nas.home.arpa" {
		t.Errorf("items: %v %+v", err, items)
	}
}
