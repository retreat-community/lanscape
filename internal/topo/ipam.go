package topo

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// IPAMEntry is one used address in a segment.
type IPAMEntry struct {
	IP     string `json:"ip"`
	Node   string `json:"node,omitempty"`
	Iface  string `json:"iface,omitempty"`
	MAC    string `json:"mac,omitempty"`
	Name   string `json:"name,omitempty"`
	Source string `json:"source"` // agent, dhcp, arp, scan, ...
	InPool bool   `json:"in_pool"`
}

// Pool is a DHCP range.
type Pool struct {
	Start netip.Addr
	End   netip.Addr
}

// IPAM summarises address usage of a segment.
type IPAM struct {
	Segment  string      `json:"segment"`
	CIDR     string      `json:"cidr"`
	Size     int         `json:"size"`
	Used     int         `json:"used"`
	Free     int         `json:"free"`
	PoolSize int         `json:"pool_size"`
	Entries  []IPAMEntry `json:"entries"`
}

// Extra is an address learned from discovery (DHCP leases, ARP, scans).
type Extra struct {
	IP, MAC, Name, Source string
}

// BuildIPAM computes used and free addresses per segment.
func BuildIPAM(segs []Segment, nodes []Node, extra []Extra, pools []Pool) []IPAM {
	macOf := map[string]string{}
	for _, n := range nodes {
		for _, i := range n.Ifaces {
			macOf[n.ID+"/"+i.Name] = i.MAC
		}
	}
	out := make([]IPAM, 0, len(segs))
	for _, s := range segs {
		p := s.prefix
		size := 1 << (32 - p.Bits())
		if p.Bits() < 31 {
			size -= 2 // network and broadcast
		}
		seen := map[string]*IPAMEntry{}
		for _, m := range s.Members {
			seen[m.IP] = &IPAMEntry{IP: m.IP, Node: m.Node, Iface: m.Iface, MAC: macOf[m.Node+"/"+m.Iface], Source: "agent"}
		}
		for _, e := range extra {
			a, err := netip.ParseAddr(e.IP)
			if err != nil || !p.Contains(a) {
				continue
			}
			if cur, ok := seen[e.IP]; ok {
				if cur.Name == "" {
					cur.Name = e.Name
				}
				if cur.MAC == "" {
					cur.MAC = e.MAC
				}
				continue
			}
			seen[e.IP] = &IPAMEntry{IP: e.IP, MAC: e.MAC, Name: e.Name, Source: e.Source}
		}
		poolSize := 0
		for _, pl := range pools {
			if p.Contains(pl.Start) && p.Contains(pl.End) {
				poolSize += int(ipToU32(pl.End)-ipToU32(pl.Start)) + 1
			}
		}
		entries := make([]IPAMEntry, 0, len(seen))
		for _, e := range seen {
			a, _ := netip.ParseAddr(e.IP)
			for _, pl := range pools {
				if !a.Less(pl.Start) && !pl.End.Less(a) {
					e.InPool = true
				}
			}
			entries = append(entries, *e)
		}
		sort.Slice(entries, func(a, b int) bool {
			x, _ := netip.ParseAddr(entries[a].IP)
			y, _ := netip.ParseAddr(entries[b].IP)
			return x.Less(y)
		})
		out = append(out, IPAM{Segment: s.ID, CIDR: s.CIDR, Size: size, Used: len(entries),
			Free: max(0, size-len(entries)), PoolSize: poolSize, Entries: entries})
	}
	return out
}

func ipToU32(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// Anomaly kinds.
const (
	ADuplicateIP  = "duplicate_ip"
	ADuplicateMAC = "duplicate_mac"
	ASubnetVLANs  = "subnet_on_multiple_vlans"
	ANoCarrier    = "no_carrier"
	ANewMAC       = "new_mac"
	AUnknownSpeed = "unknown_speed"
)

// Anomaly is an inventory inconsistency.
type Anomaly struct {
	Kind   string   `json:"kind"`
	Key    string   `json:"key"`
	Nodes  []string `json:"nodes"`
	Detail string   `json:"detail"`
}

// Anomalies finds duplicate IPs (including a VIP held by two nodes), duplicate MACs,
// one subnet on several VLANs, interfaces without link, and MACs not in known.
func Anomalies(nodes []Node, segs []Segment, known map[string]bool, extra []Extra) []Anomaly {
	out := []Anomaly{}
	ipNodes := map[string]map[string]bool{}
	macNodes := map[string]map[string]bool{}
	for _, n := range nodes {
		for _, i := range n.Ifaces {
			if i.Kind == "loopback" {
				continue
			}
			if i.Up && !i.Carrier && (i.Kind == "physical" || i.Kind == "bond") {
				out = append(out, Anomaly{Kind: ANoCarrier, Key: n.ID + "/" + i.Name, Nodes: []string{n.ID},
					Detail: fmt.Sprintf("%s on %s is up without link", i.Name, n.Name)})
			}
			if i.MAC != "" && i.MAC != "00:00:00:00:00:00" && (i.Kind == "physical" || i.Kind == "macvlan") {
				add(macNodes, strings.ToLower(i.MAC), n.ID)
			}
			for _, a := range i.Addrs {
				ip, err := netip.ParseAddr(a.IP)
				if err != nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
					continue
				}
				add(ipNodes, ip.String(), n.ID)
			}
		}
	}
	for _, ip := range sortedKeys(ipNodes) {
		if ns := ipNodes[ip]; len(ns) > 1 {
			out = append(out, Anomaly{Kind: ADuplicateIP, Key: ip, Nodes: keys(ns),
				Detail: fmt.Sprintf("%s is configured on %s (duplicate address or VIP held by two nodes)", ip, strings.Join(keys(ns), ", "))})
		}
	}
	for _, mac := range sortedKeys(macNodes) {
		if ns := macNodes[mac]; len(ns) > 1 {
			out = append(out, Anomaly{Kind: ADuplicateMAC, Key: mac, Nodes: keys(ns),
				Detail: fmt.Sprintf("MAC %s appears on %s", mac, strings.Join(keys(ns), ", "))})
		}
	}
	byCIDR := map[string][]int{}
	for _, s := range segs {
		if s.VLAN > 0 {
			byCIDR[s.CIDR] = append(byCIDR[s.CIDR], s.VLAN)
		}
	}
	for cidr, vlans := range byCIDR {
		if len(vlans) > 1 {
			sort.Ints(vlans)
			out = append(out, Anomaly{Kind: ASubnetVLANs, Key: cidr,
				Detail: fmt.Sprintf("%s is used on VLANs %v", cidr, vlans)})
		}
	}
	if known != nil {
		for _, e := range extra {
			m := strings.ToLower(e.MAC)
			if m != "" && !known[m] {
				out = append(out, Anomaly{Kind: ANewMAC, Key: m,
					Detail: fmt.Sprintf("new MAC %s (%s %s) seen via %s", m, e.IP, e.Name, e.Source)})
			}
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Kind < out[b].Kind })
	return out
}

func add(m map[string]map[string]bool, k, v string) {
	if m[k] == nil {
		m[k] = map[string]bool{}
	}
	m[k][v] = true
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
