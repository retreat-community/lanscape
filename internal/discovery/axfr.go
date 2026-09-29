//go:build !lanscape_small

package discovery

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// AXFR transfers a zone from a DNS server that allows it (TCP, RFC 5936) and returns its A and
// AAAA records.
func AXFR(ctx context.Context, server, zone string) ([]dnsRec, error) {
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, "53")
	}
	if !strings.HasSuffix(zone, ".") {
		zone += "."
	}
	name, err := dnsmessage.NewName(zone)
	if err != nil {
		return nil, fmt.Errorf("axfr %s: %w", zone, err)
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.DialContext(ctx, "tcp", server)
	if err != nil {
		return nil, fmt.Errorf("axfr %s: %w", zone, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	q := dnsmessage.Message{Header: dnsmessage.Header{ID: 0x4c53},
		Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeAXFR, Class: dnsmessage.ClassINET}}}
	b, err := q.Pack()
	if err != nil {
		return nil, err
	}
	if _, err := c.Write(binary.BigEndian.AppendUint16(nil, uint16(len(b)))); err != nil {
		return nil, err
	}
	if _, err := c.Write(b); err != nil {
		return nil, err
	}
	var recs []dnsRec
	soas := 0
	for soas < 2 {
		var l [2]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return recs, fmt.Errorf("axfr %s: %w", zone, err)
		}
		msg := make([]byte, binary.BigEndian.Uint16(l[:]))
		if _, err := io.ReadFull(c, msg); err != nil {
			return recs, fmt.Errorf("axfr %s: %w", zone, err)
		}
		var m dnsmessage.Message
		if err := m.Unpack(msg); err != nil {
			return recs, fmt.Errorf("axfr %s: %w", zone, err)
		}
		if m.RCode != dnsmessage.RCodeSuccess {
			return nil, fmt.Errorf("axfr %s: refused (%s)", zone, m.RCode)
		}
		if len(m.Answers) == 0 {
			return recs, errors.New("axfr " + zone + ": empty answer")
		}
		for _, a := range m.Answers {
			switch r := a.Body.(type) {
			case *dnsmessage.SOAResource:
				soas++ // the transfer starts and ends with the SOA record
			case *dnsmessage.AResource:
				recs = append(recs, dnsRec{name: a.Header.Name.String(), ip: net.IP(r.A[:]).String(), server: "axfr " + server})
			case *dnsmessage.AAAAResource:
				recs = append(recs, dnsRec{name: a.Header.Name.String(), ip: net.IP(r.AAAA[:]).String(), server: "axfr " + server})
			}
		}
	}
	return recs, nil
}
