package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SourceScan is the active port scan.
const SourceScan = "scan"

// DefaultScanPorts are popular service ports.
var DefaultScanPorts = []int{21, 22, 23, 25, 53, 80, 81, 88, 110, 139, 143, 161, 389, 443, 445, 515, 548, 554, 631, 993, 995,
	1080, 1883, 1900, 2049, 2375, 2376, 3000, 3001, 3306, 3389, 4040, 5000, 5001, 5060, 5353, 5432, 5601, 5900, 6379, 6443, 7878,
	8000, 8006, 8080, 8081, 8083, 8086, 8088, 8096, 8123, 8181, 8384, 8443, 8581, 8686, 8787, 8888, 8989, 9000, 9001, 9090,
	9091, 9100, 9443, 10000, 32400, 51413}

// ScanRequest asks an agent to scan subnets it can reach.
type ScanRequest struct {
	CIDRs     []string `json:"cidrs"`
	Ports     []int    `json:"ports,omitempty"`
	RatePerS  int      `json:"rate_per_s,omitempty"` // connection attempts per second (default 200)
	TimeoutMS int      `json:"timeout_ms,omitempty"` // per connection (default 700)
	MaxHosts  int      `json:"max_hosts,omitempty"`  // refused above (default 1024)
}

// ScanAllowed reports whether every requested subnet lies inside one of the subnets the
// administrator allowed on the agent (--scan-allow). An empty allow list forbids scanning.
func ScanAllowed(allow []netip.Prefix, cidrs []string) error {
	if len(allow) == 0 {
		return errors.New("active scanning is disabled on this agent (--scan-allow)")
	}
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		p, err := netip.ParsePrefix(c)
		if err != nil {
			a, aerr := netip.ParseAddr(c)
			if aerr != nil {
				return fmt.Errorf("scan: %q is not a subnet", c)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		p = p.Masked()
		ok := false
		for _, a := range allow {
			if a.Bits() <= p.Bits() && a.Contains(p.Addr()) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("scanning %s is not allowed on this agent (--scan-allow)", c)
		}
	}
	return nil
}

// ParsePrefixes parses a list of subnets.
func ParsePrefixes(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, c := range list {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			return nil, fmt.Errorf("%q is not a subnet: %w", c, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// Scan probes TCP ports on every address of the given subnets at a limited rate and reads a
// short banner from open ports. Only explicit subnets are scanned (§8.1).
func Scan(ctx context.Context, req ScanRequest) ([]Item, error) {
	if len(req.Ports) == 0 {
		req.Ports = DefaultScanPorts
	}
	if req.RatePerS <= 0 || req.RatePerS > 2000 {
		req.RatePerS = 200
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 700 * time.Millisecond
	}
	if req.MaxHosts <= 0 {
		req.MaxHosts = 1024
	}
	var hosts []netip.Addr
	for _, c := range req.CIDRs {
		p, err := netip.ParsePrefix(strings.TrimSpace(c))
		if err != nil {
			a, aerr := netip.ParseAddr(strings.TrimSpace(c))
			if aerr != nil {
				return nil, fmt.Errorf("scan: %q is not a subnet", c)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		p = p.Masked()
		if !p.Addr().Is4() {
			return nil, errors.New("scan: only IPv4 subnets can be scanned")
		}
		size := 1 << (32 - p.Bits())
		if len(hosts)+size > req.MaxHosts {
			return nil, fmt.Errorf("scan: more than %d addresses", req.MaxHosts)
		}
		for a := p.Addr(); p.Contains(a); a = a.Next() {
			// skip network and broadcast addresses of real subnets
			if p.Bits() <= 30 && (a == p.Addr() || !p.Contains(a.Next())) {
				continue
			}
			hosts = append(hosts, a)
		}
	}
	for _, port := range req.Ports {
		if port <= 0 || port > 65535 {
			return nil, fmt.Errorf("scan: bad port %d", port)
		}
	}
	tick := time.NewTicker(time.Second / time.Duration(req.RatePerS))
	defer tick.Stop()
	type hit struct {
		host   string
		port   int
		banner string
	}
	var mu sync.Mutex
	var hits []hit
	var wg sync.WaitGroup
	sem := make(chan struct{}, 256)
loop:
	for _, h := range hosts {
		for _, port := range req.Ports {
			select {
			case <-ctx.Done():
				break loop
			case <-tick.C:
			}
			sem <- struct{}{}
			wg.Add(1)
			go func(addr string, port int) {
				defer wg.Done()
				defer func() { <-sem }()
				d := net.Dialer{Timeout: timeout}
				c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
				if err != nil {
					return
				}
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
				buf := make([]byte, 128)
				n, _ := c.Read(buf)
				mu.Lock()
				hits = append(hits, hit{addr, port, cleanBanner(buf[:n])})
				mu.Unlock()
			}(h.String(), port)
		}
	}
	wg.Wait()
	byHost := map[string]*Item{}
	for _, h := range hits {
		it := byHost[h.host]
		if it == nil {
			it = &Item{Key: "scan/" + h.host, Kind: KindDevice, Name: h.host, IPs: []string{h.host}, Labels: map[string]string{}}
			byHost[h.host] = it
		}
		it.Ports = append(it.Ports, Port{Port: h.port, Proto: "tcp"})
		if h.banner != "" {
			it.Labels["banner_"+strconv.Itoa(h.port)] = h.banner
		}
	}
	out := make([]Item, 0, len(byHost))
	for _, it := range byHost {
		sort.Slice(it.Ports, func(a, b int) bool { return it.Ports[a].Port < it.Ports[b].Port })
		var ps []string
		for _, p := range it.Ports {
			ps = append(ps, strconv.Itoa(p.Port))
		}
		it.Labels["open_ports"] = strings.Join(ps, ",")
		it.Labels["type"] = typeFromPorts(it.Ports)
		out = append(out, *it)
	}
	sortItems(out)
	return out, ctx.Err()
}

func cleanBanner(b []byte) string {
	s := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 {
			return ' '
		}
		return r
	}, string(b))
	return strings.Join(strings.Fields(s), " ")
}

func typeFromPorts(ps []Port) string {
	has := map[int]bool{}
	for _, p := range ps {
		has[p.Port] = true
	}
	switch {
	case has[631] || has[9100] || has[515]:
		return "printer"
	case has[554] && !has[22]:
		return "camera"
	case has[8006]:
		return "hypervisor"
	case has[445] || has[2049] || has[548] || has[5000] && has[5001]:
		return "nas"
	case has[1883] || has[8123]:
		return "iot"
	case has[3389] || has[5900]:
		return "computer"
	}
	return "device"
}
