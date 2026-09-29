package discovery

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// SourceNetBIOS names hosts of the local links through NetBIOS node status and LLMNR.
const SourceNetBIOS = "netbios"

// NetBIOS name service and LLMNR ports (tests use unprivileged ones).
var nbPort, llmnrPort = 137, 5355

// nbstatQuery is a NetBIOS node status request for the wildcard name "*" (RFC 1002 4.2.17).
func nbstatQuery(id uint16) []byte {
	q := make([]byte, 0, 50)
	q = binary.BigEndian.AppendUint16(q, id)
	q = append(q, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00) // flags, 1 question
	q = append(q, 0x20)                                                       // encoded name length
	name := append([]byte{'*'}, make([]byte, 15)...)
	for _, c := range name {
		q = append(q, 'A'+c>>4, 'A'+c&0x0f)
	}
	return append(q, 0x00, 0x00, 0x21, 0x00, 0x01) // NBSTAT, IN
}

// NBName is a NetBIOS node status answer.
type NBName struct {
	Name      string // computer name (unique name with suffix 0x00)
	Workgroup string // group name with suffix 0x00
	MAC       string
}

// ParseNBStat parses a node status response.
func ParseNBStat(b []byte) (NBName, error) {
	var r NBName
	// header (12) + name (34) + type/class/ttl (8) + rdlength (2)
	const off = 12 + 34 + 8 + 2
	if len(b) < off+1 || binary.BigEndian.Uint16(b[6:8]) == 0 {
		return r, fmt.Errorf("netbios: short or empty response")
	}
	n := int(b[off])
	p := off + 1
	for i := 0; i < n && p+18 <= len(b); i++ {
		name := strings.TrimRight(string(b[p:p+15]), " \x00")
		suffix, flags := b[p+15], binary.BigEndian.Uint16(b[p+16:p+18])
		group := flags&0x8000 != 0
		switch {
		case suffix == 0x00 && !group && r.Name == "":
			r.Name = name
		case suffix == 0x00 && group && r.Workgroup == "":
			r.Workgroup = name
		}
		p += 18
	}
	if p+6 <= len(b) {
		if mac := net.HardwareAddr(b[p : p+6]).String(); mac != "00:00:00:00:00:00" {
			r.MAC = mac
		}
	}
	if r.Name == "" {
		return r, fmt.Errorf("netbios: no computer name")
	}
	return r, nil
}

// llmnrPTR builds a reverse (PTR) query, sent unicast to the host itself (RFC 4795 2.4).
func llmnrPTR(id uint16, ip net.IP) ([]byte, error) {
	v4 := ip.To4()
	if v4 == nil {
		return nil, fmt.Errorf("llmnr: IPv4 only")
	}
	n, err := dnsmessage.NewName(fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa.", v4[3], v4[2], v4[1], v4[0]))
	if err != nil {
		return nil, err
	}
	m := dnsmessage.Message{Header: dnsmessage.Header{ID: id},
		Questions: []dnsmessage.Question{{Name: n, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}}}
	return m.Pack()
}

func parseLLMNR(b []byte) string {
	var p dnsmessage.Parser
	if _, err := p.Start(b); err != nil {
		return ""
	}
	_ = p.SkipAllQuestions()
	answers, err := p.AllAnswers()
	if err != nil {
		return ""
	}
	for _, a := range answers {
		if ptr, ok := a.Body.(*dnsmessage.PTRResource); ok {
			return strings.TrimSuffix(ptr.PTR.String(), ".")
		}
	}
	return ""
}

// NetBIOS asks every address for its NetBIOS node status and, when that stays silent, for its
// LLMNR reverse name. Queries go out at most rate per second; answers are collected for wait.
func NetBIOS(ctx context.Context, ips []string, rate int, wait time.Duration) ([]Item, error) {
	if rate <= 0 {
		rate = 50
	}
	nb, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer nb.Close()
	ll, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer ll.Close()

	var mu sync.Mutex
	found := map[string]*Item{}
	item := func(ip string) *Item {
		it := found[ip]
		if it == nil {
			it = &Item{Key: "netbios/" + ip, Kind: KindDevice, IPs: []string{ip}, Labels: map[string]string{}}
			found[ip] = it
		}
		return it
	}
	deadline := time.Now().Add(time.Duration(len(ips))*time.Second/time.Duration(rate) + wait)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // NetBIOS answers
		defer wg.Done()
		buf := make([]byte, 1500)
		for {
			_ = nb.SetReadDeadline(deadline)
			n, from, err := nb.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if r, err := ParseNBStat(buf[:n]); err == nil {
				mu.Lock()
				it := item(from.IP.String())
				it.Name, it.Labels["netbios"] = r.Name, r.Name
				if r.Workgroup != "" {
					it.Labels["workgroup"] = r.Workgroup
				}
				if r.MAC != "" {
					it.Labels["mac"] = r.MAC
				}
				it.Labels["type"] = "computer"
				mu.Unlock()
			}
		}
	}()
	go func() { // LLMNR answers
		defer wg.Done()
		buf := make([]byte, 1500)
		for {
			_ = ll.SetReadDeadline(deadline)
			n, from, err := ll.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if name := parseLLMNR(buf[:n]); name != "" {
				mu.Lock()
				it := item(from.IP.String())
				if it.Name == "" {
					it.Name = name
				}
				it.Labels["llmnr"] = name
				mu.Unlock()
			}
		}
	}()
	tick := time.NewTicker(time.Second / time.Duration(rate))
	defer tick.Stop()
	for i, s := range ips {
		ip := net.ParseIP(s).To4()
		if ip == nil {
			continue
		}
		select {
		case <-ctx.Done():
			nb.Close()
			ll.Close()
			wg.Wait()
			return nil, ctx.Err()
		case <-tick.C:
		}
		_, _ = nb.WriteToUDP(nbstatQuery(uint16(i)), &net.UDPAddr{IP: ip, Port: nbPort})
		if q, err := llmnrPTR(uint16(i), ip); err == nil {
			_, _ = ll.WriteToUDP(q, &net.UDPAddr{IP: ip, Port: llmnrPort})
		}
	}
	wg.Wait()
	items := make([]Item, 0, len(found))
	for _, it := range found {
		if it.Name != "" {
			items = append(items, *it)
		}
	}
	sortItems(items)
	return items, nil
}
