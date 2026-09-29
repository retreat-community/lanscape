package server

import (
	"context"
	"fmt"
	"sort"

	"github.com/retreat-community/lanscape/internal/topo"
)

// MapNode is a Cytoscape-compatible node.
type MapNode struct {
	ID     string         `json:"id"`
	Label  string         `json:"label"`
	Type   string         `json:"type"` // device, port, segment, zone, service, switch
	Parent string         `json:"parent,omitempty"`
	Icon   string         `json:"icon,omitempty"`
	Status string         `json:"status,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

// MapEdge is a Cytoscape-compatible edge.
type MapEdge struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Target  string `json:"target"`
	Type    string `json:"type"` // link, path
	Verdict string `json:"verdict,omitempty"`
	Label   string `json:"label,omitempty"`
}

// Hypothesis is an inferred bottleneck (unmanaged switch or uplink).
type Hypothesis struct {
	ID       string   `json:"id"`
	Segment  string   `json:"segment"`
	Mbps     int      `json:"mbps"`
	GroupA   []string `json:"group_a"`
	GroupB   []string `json:"group_b"`
	Detail   string   `json:"detail"`
	Accepted bool     `json:"accepted"`
}

// MapGraph is the map returned by /api/v1/map.
type MapGraph struct {
	Nodes      []MapNode    `json:"nodes"`
	Edges      []MapEdge    `json:"edges"`
	Hypotheses []Hypothesis `json:"hypotheses"`
	RunID      int64        `json:"run_id,omitempty"`
}

var verdictRank = map[string]int{topo.Green: 1, topo.None: 2, topo.Purple: 3, topo.Yellow: 4, topo.Red: 5}

func worse(a, b string) string {
	if verdictRank[b] > verdictRank[a] {
		return b
	}
	return a
}

// MapDecorator lets modules add services, guests and external zones to the map.
var MapDecorator func(ctx context.Context, s *Server, g *MapGraph, rep *Report)

func (s *Server) buildMap(ctx context.Context, segs []topo.Segment, rep *Report) MapGraph {
	g := MapGraph{Nodes: []MapNode{}, Edges: []MapEdge{}, Hypotheses: []Hypothesis{}}
	if rep != nil {
		g.RunID = rep.ID
	}
	portVerdict := map[string]string{}
	nodeVerdict := map[string]string{}
	type pairKey struct{ a, b, seg string }
	pairs := map[pairKey]*MapEdge{}
	if rep != nil {
		for _, p := range rep.Paths {
			portVerdict[p.Src+"/"+p.SrcIf+"@"+p.SegID] = worse(portVerdict[p.Src+"/"+p.SrcIf+"@"+p.SegID], p.Verdict)
			portVerdict[p.Dst+"/"+p.DstIf+"@"+p.SegID] = worse(portVerdict[p.Dst+"/"+p.DstIf+"@"+p.SegID], p.Verdict)
			nodeVerdict[p.Src] = worse(nodeVerdict[p.Src], p.Verdict)
			nodeVerdict[p.Dst] = worse(nodeVerdict[p.Dst], p.Verdict)
			a, b := p.Src, p.Dst
			if b < a {
				a, b = b, a
			}
			k := pairKey{a, b, p.SegID}
			e := pairs[k]
			if e == nil {
				e = &MapEdge{ID: fmt.Sprintf("path:%s:%s:%s", a, b, p.SegID), Source: "dev:" + a, Target: "dev:" + b,
					Type: "path", Verdict: p.Verdict}
				pairs[k] = e
			}
			e.Verdict = worse(e.Verdict, p.Verdict)
			if p.BestBPS > 0 {
				e.Label = fmt.Sprintf("%d Mbit/s", p.BestBPS/1_000_000)
			}
		}
	}
	for _, a := range s.hub.List() {
		icon := "server"
		switch {
		case a.Inv.Env.Kind == "openwrt":
			icon = "router"
		case a.Inv.Env.Kind == "vm":
			icon = "vm"
		case a.Inv.Env.Kind == "lxc":
			icon = "lxc"
		case a.Inv.Env.Kind == "k8s-pod":
			icon = "pod"
		case a.Inv.Env.Kind == "docker":
			icon = "container"
		case a.Inv.Env.Hypervisor != "":
			icon = "hypervisor"
		case a.Kind == "lite":
			icon = "router"
		}
		status := "offline"
		if a.Online {
			status = nodeVerdict[a.ID]
			if status == "" {
				status = "online"
			}
		}
		g.Nodes = append(g.Nodes, MapNode{ID: "dev:" + a.ID, Label: a.Name, Type: "device", Icon: icon, Status: status,
			Data: map[string]any{"agent": a.ID, "kind": a.Kind, "env": a.Inv.Env.Kind, "online": a.Online}})
	}
	for _, sg := range segs {
		label := sg.ID
		if sg.ExpectedMbps > 0 {
			label += fmt.Sprintf(" · %d Mbit/s", sg.ExpectedMbps)
		}
		g.Nodes = append(g.Nodes, MapNode{ID: "seg:" + sg.ID, Label: label, Type: "segment",
			Data: map[string]any{"cidr": sg.CIDR, "vlan": sg.VLAN, "expected_mbps": sg.ExpectedMbps}})
		for _, m := range sg.Members {
			pid := "port:" + m.Node + "/" + m.Iface
			found := false
			for _, n := range g.Nodes {
				if n.ID == pid {
					found = true
				}
			}
			if !found {
				lbl := m.Iface + " " + m.IP
				if m.Speed > 0 {
					lbl += fmt.Sprintf(" · %dM", m.Speed)
				}
				g.Nodes = append(g.Nodes, MapNode{ID: pid, Label: lbl, Type: "port", Parent: "dev:" + m.Node,
					Data: map[string]any{"iface": m.Iface, "ip": m.IP, "speed": m.Speed, "kind": m.Kind}})
			}
			g.Edges = append(g.Edges, MapEdge{ID: "link:" + pid + "@" + sg.ID, Source: pid, Target: "seg:" + sg.ID,
				Type: "link", Verdict: portVerdict[m.Node+"/"+m.Iface+"@"+sg.ID]})
		}
	}
	keys := make([]pairKey, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return pairs[keys[i]].ID < pairs[keys[j]].ID })
	for _, k := range keys {
		g.Edges = append(g.Edges, *pairs[k])
	}
	if rep != nil {
		g.Hypotheses = Bottlenecks(rep)
	}
	if MapDecorator != nil {
		MapDecorator(ctx, s, &g, rep)
	}
	return g
}

// Bottlenecks infers "unmanaged switch / uplink" hypotheses: when paths between two
// groups of nodes in a segment are consistently capped below their port speed, the
// groups are probably connected through a slower link (§7.1).
func Bottlenecks(rep *Report) []Hypothesis {
	out := []Hypothesis{}
	for si, sg := range rep.Segments {
		speed := map[string]int{}
		for _, m := range sg.Members {
			speed[m.Node] = max(speed[m.Node], m.Speed)
		}
		type edge struct {
			a, b string
			mbps int
		}
		var capped []edge
		fast := map[[2]string]bool{}
		for _, p := range rep.Paths {
			if p.Seg != si || p.BestBPS == 0 {
				continue
			}
			mbps := int(p.BestBPS / 1_000_000)
			port := min(speed[p.Src], speed[p.Dst])
			if port > 0 && mbps*100 < port*80 {
				capped = append(capped, edge{p.Src, p.Dst, mbps})
			} else {
				fast[[2]string{p.Src, p.Dst}] = true
			}
		}
		if len(capped) < 2 {
			continue
		}
		// group nodes connected by fast paths (union-find); capped paths must cross groups
		parent := map[string]string{}
		var find func(string) string
		find = func(x string) string {
			if parent[x] == "" || parent[x] == x {
				parent[x] = x
				return x
			}
			parent[x] = find(parent[x])
			return parent[x]
		}
		for k := range fast {
			parent[find(k[0])] = find(k[1])
		}
		groups := map[[2]string][]int{}
		for _, c := range capped {
			ga, gb := find(c.a), find(c.b)
			if ga == gb {
				continue
			}
			if gb < ga {
				ga, gb = gb, ga
			}
			groups[[2]string{ga, gb}] = append(groups[[2]string{ga, gb}], c.mbps)
		}
		for k, v := range groups {
			if len(v) < 2 {
				continue
			}
			sort.Ints(v)
			med := v[len(v)/2]
			var ga, gb []string
			for n := range speed {
				switch find(n) {
				case k[0]:
					ga = append(ga, n)
				case k[1]:
					gb = append(gb, n)
				}
			}
			sort.Strings(ga)
			sort.Strings(gb)
			capMbps := roundLink(med)
			out = append(out, Hypothesis{ID: fmt.Sprintf("bn:%s:%s:%s", sg.ID, k[0], k[1]), Segment: sg.ID, Mbps: capMbps,
				GroupA: ga, GroupB: gb,
				Detail: fmt.Sprintf("paths between %v and %v stay at ~%d Mbit/s: probable bottleneck %d Mbit/s between the groups",
					ga, gb, med, capMbps)})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// roundLink rounds a measured rate up to the nearest common link speed.
func roundLink(mbps int) int {
	for _, l := range []int{10, 100, 1000, 2500, 5000, 10000, 25000, 40000, 100000} {
		if mbps <= l {
			return l
		}
	}
	return mbps
}
