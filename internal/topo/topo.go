// Package topo turns inventories and test results into segments, verdicts, problems,
// IPAM tables, anomalies and the map graph.
package topo

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/retreat-community/lanscape/internal/netio"
)

// Node is an agent-managed device as seen by the server.
type Node struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Hostname string        `json:"hostname"`
	HostID   string        `json:"host_id"`
	Lite     bool          `json:"lite"`
	Online   bool          `json:"online"`
	Env      string        `json:"env,omitempty"`    // bare-metal, vm, lxc, docker, k8s-pod, openwrt
	Parent   string        `json:"parent,omitempty"` // hypervisor node id for guests
	Ifaces   []netio.Iface `json:"ifaces"`
}

// Member is one interface address of a node inside a segment.
type Member struct {
	Node   string `json:"node"`
	Iface  string `json:"iface"`
	IP     string `json:"ip"`
	Speed  int    `json:"speed"`
	MTU    int    `json:"mtu"`
	Kind   string `json:"kind"`
	Parent string `json:"parent,omitempty"`
}

// Segment is an L2/L3 network: a subnet and, when known, a VLAN.
type Segment struct {
	ID           string   `json:"id"`
	CIDR         string   `json:"cidr"`
	VLAN         int      `json:"vlan"`
	ExpectedMbps int      `json:"expected_mbps"`
	Manual       bool     `json:"manual"`
	Members      []Member `json:"members"`
	prefix       netip.Prefix
}

// Prefix returns the parsed subnet.
func (s *Segment) Prefix() netip.Prefix { return s.prefix }

// SegmentID formats the stable segment identifier.
func SegmentID(p netip.Prefix, vlan int) string {
	if vlan > 0 {
		return fmt.Sprintf("%s vlan %d", p, vlan)
	}
	return p.String()
}

// Excluded reports whether an interface should not form segments.
func Excluded(i netio.Iface) bool {
	return i.Kind == "loopback" || !i.Up
}

// BuildSegments groups IPv4 interface addresses by subnet and VLAN. Interfaces with an
// unknown VLAN (0) join the segment of the same subnet, so a macvlan child in a pod
// namespace lands in the tagged segment of its parent. manual maps CIDR or segment ID to
// an expected speed in Mbit/s.
func BuildSegments(nodes []Node, manual map[string]int) []Segment {
	var segs []*Segment
	for _, n := range nodes {
		for _, i := range n.Ifaces {
			if Excluded(i) {
				continue
			}
			for _, a := range i.Addrs {
				ip, err := netip.ParseAddr(a.IP)
				if err != nil || !ip.Is4() || a.Prefix == 0 || a.Prefix > 30 {
					continue
				}
				p := netip.PrefixFrom(ip, a.Prefix).Masked()
				var seg *Segment
				for _, s := range segs {
					if s.prefix == p && (s.VLAN == i.VLAN || s.VLAN == 0 || i.VLAN == 0) {
						seg = s
						break
					}
				}
				if seg == nil {
					seg = &Segment{CIDR: p.String(), prefix: p}
					segs = append(segs, seg)
				}
				if seg.VLAN == 0 && i.VLAN != 0 {
					seg.VLAN = i.VLAN
				}
				seg.Members = append(seg.Members, Member{Node: n.ID, Iface: i.Name, IP: ip.String(),
					Speed: i.Speed, MTU: i.MTU, Kind: i.Kind, Parent: i.Parent})
			}
		}
	}
	out := make([]Segment, 0, len(segs))
	for _, s := range segs {
		s.ID = SegmentID(s.prefix, s.VLAN)
		if v, ok := manual[s.ID]; ok && v > 0 {
			s.ExpectedMbps, s.Manual = v, true
		} else if v := manualFor(manual, s.prefix); v > 0 {
			s.ExpectedMbps, s.Manual = v, true
		}
		sort.Slice(s.Members, func(a, b int) bool {
			if s.Members[a].Node != s.Members[b].Node {
				return s.Members[a].Node < s.Members[b].Node
			}
			return s.Members[a].Iface < s.Members[b].Iface
		})
		out = append(out, *s)
	}
	sort.Slice(out, func(a, b int) bool {
		pa, pb := out[a].prefix, out[b].prefix
		if pa.Addr() != pb.Addr() {
			return pa.Addr().Less(pb.Addr())
		}
		return out[a].VLAN < out[b].VLAN
	})
	return out
}

// manualFor finds the most specific configured network containing p.
func manualFor(manual map[string]int, p netip.Prefix) int {
	best, v := -1, 0
	for k, mbps := range manual {
		mp, err := netip.ParsePrefix(strings.TrimSpace(k))
		if err != nil {
			continue
		}
		if mp.Bits() <= p.Bits() && mp.Contains(p.Addr()) && mp.Bits() > best {
			best, v = mp.Bits(), mbps
		}
	}
	return v
}

// ExpectedMbps implements §7.4: manual value, then the speed inherited from the physical
// uplink of the hypervisor bridge (inherit), then the minimum of known link speeds;
// 0 means unknown.
func ExpectedMbps(seg *Segment, a, b Member, inherit func(m Member) int) int {
	if seg.Manual {
		return seg.ExpectedMbps
	}
	sa, sb := a.Speed, b.Speed
	if sa == 0 && inherit != nil {
		sa = inherit(a)
	}
	if sb == 0 && inherit != nil {
		sb = inherit(b)
	}
	if sa == 0 || sb == 0 {
		return 0
	}
	return min(sa, sb)
}
