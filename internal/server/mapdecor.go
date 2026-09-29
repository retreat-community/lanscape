package server

import (
	"context"
	"encoding/json"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/retreat-community/lanscape/internal/discovery"
)

// hypervisorSource reports whether a discovery source lists virtual machines and containers.
func hypervisorSource(src string) bool {
	return src == discovery.SourceProxmox || src == discovery.SourceLibvirt
}

// guestHosts maps agents running inside a VM/CT (matched by MAC in the hypervisor's guest list)
// and pod-network agents (by node name) to the agent of their host: the dependency the map
// shows by nesting and incident grouping follows (§7.1, §9.2).
func (s *Server) guestHosts(ctx context.Context) map[string]string {
	agents := s.hub.List()
	byHost := map[string]string{}
	macOwner := map[string]string{}
	for _, a := range agents {
		if a.Inv.Env.Kind != "k8s-pod" {
			byHost[strings.ToLower(a.Hostname)] = a.ID
			byHost[strings.ToLower(a.Name)] = a.ID
		}
		for _, ifc := range a.Inv.Ifaces {
			if ifc.MAC != "" {
				macOwner[strings.ToLower(ifc.MAC)] = a.ID
			}
		}
	}
	out := map[string]string{}
	for _, a := range agents {
		if node := strings.ToLower(a.Inv.Env.K8sNode); node != "" && a.Inv.Env.Kind == "k8s-pod" {
			if host, ok := byHost[node]; ok && host != a.ID {
				out[a.ID] = host
			}
		}
	}
	fs, err := s.store.Findings(ctx, "", "")
	if err != nil {
		return out
	}
	for _, f := range fs {
		if !hypervisorSource(f.Source) || f.Gone != 0 {
			continue
		}
		var it discovery.Item
		if json.Unmarshal(f.Data, &it) != nil {
			continue
		}
		host, ok := byHost[strings.ToLower(it.Labels["node"])]
		if !ok && f.Source == discovery.SourceLibvirt {
			host, ok = f.AgentID, true
		}
		if !ok {
			continue
		}
		for _, n := range it.NICs {
			if a, ok := macOwner[n.MAC]; ok && a != host {
				out[a] = host
			}
		}
	}
	return out
}

// decorateMap nests guests in their hypervisor and pods in their node, adds Proxmox guests
// without an agent and attaches services to the devices they run on (§7.1).
func (s *Server) decorateMap(ctx context.Context, g *MapGraph) {
	agents := s.hub.List()
	byHost := map[string]string{} // hostname / name -> agent id (hosts only, not guests)
	macOwner := map[string]string{}
	for _, a := range agents {
		if a.Inv.Env.Kind != "k8s-pod" {
			byHost[strings.ToLower(a.Hostname)] = a.ID
			byHost[strings.ToLower(a.Name)] = a.ID
		}
		for _, ifc := range a.Inv.Ifaces {
			if ifc.MAC != "" {
				macOwner[strings.ToLower(ifc.MAC)] = a.ID
			}
		}
	}
	idx := map[string]int{}
	for i, n := range g.Nodes {
		idx[n.ID] = i
	}
	setParent := func(child, parent string) {
		if child == parent {
			return
		}
		if i, ok := idx["dev:"+child]; ok {
			if _, ok := idx["dev:"+parent]; ok && g.Nodes[i].Parent == "" {
				g.Nodes[i].Parent = "dev:" + parent
			}
		}
	}
	// pods inside their node (pod-network agents report the node name)
	for _, a := range agents {
		if node := strings.ToLower(a.Inv.Env.K8sNode); node != "" && a.Inv.Env.Kind == "k8s-pod" {
			if host, ok := byHost[node]; ok {
				setParent(a.ID, host)
			}
		}
	}
	// hypervisor guests (Proxmox, libvirt): agents inside a VM/CT are matched by MAC, the others
	// are added as devices
	fs, err := s.store.Findings(ctx, "", "")
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, f := range fs {
		if !hypervisorSource(f.Source) || f.Gone != 0 || seen[f.Source+f.Key] {
			continue
		}
		seen[f.Source+f.Key] = true
		var it discovery.Item
		if json.Unmarshal(f.Data, &it) != nil {
			continue
		}
		host, ok := byHost[strings.ToLower(it.Labels["node"])]
		if !ok && f.Source == discovery.SourceLibvirt {
			// libvirt reports the guests of the host the agent runs on
			host, ok = f.AgentID, true
		}
		if !ok {
			continue
		}
		guest := ""
		for _, n := range it.NICs {
			if a, ok := macOwner[n.MAC]; ok && a != host {
				guest = a
			}
		}
		if guest != "" {
			setParent(guest, host)
			continue
		}
		icon := "vm"
		if it.Kind == discovery.KindCT {
			icon = "lxc"
		}
		status := "offline"
		if it.State == "running" {
			status = "online"
		}
		id := "guest:" + it.Key
		idx[id] = len(g.Nodes)
		g.Nodes = append(g.Nodes, MapNode{ID: id, Label: it.Name, Type: "device", Icon: icon, Status: status,
			Parent: "dev:" + host, Data: map[string]any{"guest": it.Key, "ips": it.IPs, "state": it.State}})
	}
	s.mapDevices(ctx, g)
	// services as badges on the device they were discovered on
	svcs, err := s.store.Services(ctx)
	if err != nil || len(svcs) == 0 {
		return
	}
	cards, _ := s.cards(ctx)
	cardAgents := map[string][]string{}
	cardGuest := map[string]string{}
	for _, c := range cards {
		cardAgents[c.Key] = c.Agents
		if strings.HasPrefix(c.Key, "proxmox:") {
			cardGuest[c.Key] = "guest:" + strings.TrimPrefix(c.Key, "proxmox:")
		}
	}
	status := map[int64]string{}
	for _, m := range s.uptime.snapshot() {
		if m.ServiceID == 0 {
			continue
		}
		if statusRank[m.Status] > statusRank[status[m.ServiceID]] || status[m.ServiceID] == "" {
			status[m.ServiceID] = m.Status
		}
	}
	verdict := map[string]string{"up": "green", "degraded": "yellow", "down": "red", "maintenance": "purple"}
	sort.Slice(svcs, func(a, b int) bool { return svcs[a].Name < svcs[b].Name })
	for _, v := range svcs {
		parent := ""
		if gid, ok := cardGuest[v.CardKey]; ok {
			if _, ok := idx[gid]; ok {
				parent = gid
			}
		}
		if parent == "" {
			for _, a := range cardAgents[v.CardKey] {
				if _, ok := idx["dev:"+a]; ok {
					parent = "dev:" + a
					break
				}
			}
		}
		if parent == "" {
			continue
		}
		st := verdict[status[v.ID]]
		if st == "" {
			st = "none"
		}
		g.Nodes = append(g.Nodes, MapNode{ID: "svc:" + strconv.FormatInt(v.ID, 10), Label: v.Name, Type: "service", Parent: parent, Icon: v.Icon,
			Status: st, Data: map[string]any{"service": v.ID, "url": v.InternalURL, "external_url": v.ExternalURL}})
	}
}

// mapDevices adds hosts without an agent to the segment their address belongs to.
func (s *Server) mapDevices(ctx context.Context, g *MapGraph) {
	type seg struct {
		id  string
		net *net.IPNet
	}
	var segs []seg
	for _, n := range g.Nodes {
		if n.Type != "segment" {
			continue
		}
		if c, ok := n.Data["cidr"].(string); ok {
			if _, ipn, err := net.ParseCIDR(c); err == nil {
				segs = append(segs, seg{n.ID, ipn})
			}
		}
	}
	added := 0
	for _, d := range s.devices(ctx) {
		ip := net.ParseIP(d.IP)
		if ip == nil || added >= 300 {
			continue
		}
		for _, sg := range segs {
			if !sg.net.Contains(ip) {
				continue
			}
			label := d.Name
			if label == "" {
				label = d.Vendor
			}
			if label == "" {
				label = d.IP
			} else {
				label += "\n" + d.IP
			}
			id := "host:" + d.IP
			g.Nodes = append(g.Nodes, MapNode{ID: id, Label: label, Type: "device", Icon: d.Type, Status: "none",
				Data: map[string]any{"ip": d.IP, "mac": d.MAC, "vendor": d.Vendor, "model": d.Model, "sources": d.Sources, "url": d.URL}})
			g.Edges = append(g.Edges, MapEdge{ID: "link:" + id + "@" + strings.TrimPrefix(sg.id, "seg:"), Source: id, Target: sg.id, Type: "link"})
			added++
			break
		}
	}
}
