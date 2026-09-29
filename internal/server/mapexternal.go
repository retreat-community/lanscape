package server

import (
	"context"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/topo"
)

// drawExternal puts the external dependencies into their own zone (§7.1): the Internet with the
// exits of routers and Internet-test points, agents that only have public addresses (cloud nodes)
// and VPN interfaces of agents.
func (s *Server) drawExternal(ctx context.Context, g *MapGraph) {
	nodes := map[string]int{}
	for i, n := range g.Nodes {
		nodes[n.ID] = i
	}
	var out []MapNode
	var edges []MapEdge
	zone := MapNode{ID: "zone:external", Label: "External", Type: "zone"}

	// the Internet and its exits: the last result per exit of the Internet test, else routers
	type exit struct {
		dev, ip string
		ok      bool
	}
	exits := map[string][]exit{}
	if checks, err := s.store.InternetChecks(ctx, time.Now().Add(-24*time.Hour).UnixMilli()); err == nil {
		last := map[string]int{}
		for i, c := range checks {
			last[exitKey(c.Point, c.Dev)] = i
		}
		keys := make([]string, 0, len(last))
		for k := range last {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			c := checks[last[k]]
			if c.Point == "server" {
				continue
			}
			exits[c.Point] = append(exits[c.Point], exit{dev: c.Dev, ip: c.PublicIP, ok: c.OK})
		}
	}
	for _, a := range s.hub.List() {
		if _, ok := exits[a.ID]; ok || !isRouter(&a.Inv) {
			continue
		}
		for _, gw := range agent.DefaultGateways(a.Inv.Routes) {
			exits[a.ID] = append(exits[a.ID], exit{dev: gw.Dev, ok: a.Online})
		}
	}
	if len(exits) > 0 {
		out = append(out, MapNode{ID: "ext:internet", Label: "Internet", Type: "device", Icon: "internet", Parent: zone.ID,
			Status: "online"})
		ids := make([]string, 0, len(exits))
		for id := range exits {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if _, ok := nodes["dev:"+id]; !ok {
				continue
			}
			for _, e := range exits[id] {
				v, label := topo.Green, e.dev
				if !e.ok {
					v = topo.Red
				}
				if e.ip != "" {
					label = strings.TrimSpace(label + " " + e.ip)
				}
				edges = append(edges, MapEdge{ID: "ext:" + id + ":" + e.dev, Source: "dev:" + id, Target: "ext:internet",
					Type: "link", Verdict: v, Label: label})
			}
		}
	}

	for _, a := range s.hub.List() {
		i, ok := nodes["dev:"+a.ID]
		if !ok {
			continue
		}
		// cloud nodes and VPS: every address is public
		if g.Nodes[i].Parent == "" && onlyPublic(a.Inv) {
			g.Nodes[i].Parent = zone.ID
		}
		// VPN peers: the tunnels of the agent
		for _, ifc := range a.Inv.Ifaces {
			if ifc.Kind != "wireguard" && !strings.HasPrefix(ifc.Name, "tailscale") && !strings.HasPrefix(ifc.Name, "zt") {
				continue
			}
			id := "vpn:" + a.ID + ":" + ifc.Name
			label := ifc.Name
			if len(ifc.Addrs) > 0 {
				label += " " + ifc.Addrs[0].IP
			}
			out = append(out, MapNode{ID: id, Label: label, Type: "device", Icon: "vpn", Parent: zone.ID, Status: "online"})
			edges = append(edges, MapEdge{ID: id + ":link", Source: "dev:" + a.ID, Target: id, Type: "link"})
		}
	}
	inZone := len(out) > 0
	for _, n := range g.Nodes {
		if n.Parent == zone.ID {
			inZone = true
		}
	}
	if !inZone {
		return
	}
	g.Nodes = append(g.Nodes, zone)
	g.Nodes = append(g.Nodes, out...)
	g.Edges = append(g.Edges, edges...)
}

// onlyPublic reports whether all IPv4 addresses of an agent are public (a cloud node).
func onlyPublic(inv agent.Inventory) bool {
	n := 0
	for _, ifc := range inv.Ifaces {
		for _, a := range ifc.Addrs {
			ip, err := netip.ParseAddr(a.IP)
			if err != nil {
				continue
			}
			if !ip.Is4() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
				return false
			}
			n++
		}
	}
	return n > 0
}
