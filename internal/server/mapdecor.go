package server

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/retreat-community/lanscape/internal/discovery"
)

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
	// Proxmox guests: agents inside a VM/CT are matched by MAC, the others are added as devices
	fs, err := s.store.Findings(ctx, "", "")
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, f := range fs {
		if f.Source != discovery.SourceProxmox || f.Gone != 0 || seen[f.Key] {
			continue
		}
		seen[f.Key] = true
		var it discovery.Item
		if json.Unmarshal(f.Data, &it) != nil {
			continue
		}
		host, ok := byHost[strings.ToLower(it.Labels["node"])]
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
