package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/monitor"
	"github.com/retreat-community/lanscape/internal/topo"
)

// resourceResult evaluates container, Kubernetes and VM/CT monitors from the latest discovery
// reports (§9.1).
func (s *Server) resourceResult(ctx context.Context, spec monitor.Spec) monitor.Result {
	res := monitor.Result{At: time.Now().UnixMilli()}
	src, agentID, key, err := monitor.ResourceRef(spec.Target)
	if err != nil {
		res.Status, res.Message = monitor.Down, err.Error()
		return res
	}
	fs, err := s.store.Findings(ctx, "", "")
	if err != nil {
		res.Status, res.Message = statusUnknown, err.Error()
		return res
	}
	var it discovery.Item
	var found, live bool
	var lastSeen int64
	var reporter string
	for _, f := range fs {
		if f.Source != src || f.Key != key || (agentID != "*" && f.AgentID != agentID) {
			continue
		}
		found = true
		if f.Gone != 0 || f.LastSeen < lastSeen {
			continue
		}
		if json.Unmarshal(f.Data, &it) == nil {
			live, lastSeen, reporter = true, f.LastSeen, f.AgentID
		}
	}
	switch {
	case !found:
		res.Status, res.Message = monitor.Down, key+" is not reported by any agent"
		return res
	case !live:
		res.Status, res.Message = monitor.Down, key+" disappeared"
		return res
	}
	if a, ok := s.hub.Get(reporter); ok && !a.Online && time.Since(time.UnixMilli(lastSeen)) > 15*time.Minute {
		res.Status, res.Message = statusUnknown, "the reporting agent "+a.Name+" is offline"
		return res
	}
	switch spec.Type {
	case monitor.TypeContainer:
		switch {
		case it.State != "running":
			res.Status, res.Message = monitor.Down, "container "+it.State
		case it.Health == "unhealthy":
			res.Status, res.Message = monitor.Down, "healthcheck failing"
		case it.Health == "starting":
			res.Status, res.Message = monitor.Degraded, "healthcheck starting"
		default:
			res.Status = monitor.Up
		}
	case monitor.TypeK8s:
		switch it.State {
		case "ready", "bound":
			res.Status = monitor.Up
		case "degraded", "scaled_down", "pending":
			res.Status, res.Message = monitor.Degraded, fmt.Sprintf("%s %s", it.State, it.Ready)
		default:
			res.Status, res.Message = monitor.Down, strings.TrimSpace(it.State+" "+it.Ready)
		}
		if it.Kind == discovery.KindService {
			res.Status, res.Message = monitor.Up, ""
		}
	case monitor.TypeVM:
		if it.State == "running" {
			res.Status = monitor.Up
		} else {
			res.Status, res.Message = monitor.Down, "guest "+it.State
		}
	}
	return res
}

// proxmoxSpeed lets virtio guests inherit the speed of the physical ports of the bridge they are
// attached to on the Proxmox or libvirt host (§7.4). The guest NIC is matched by MAC address.
func proxmoxSpeed(s *Server, m topo.Member) int {
	a, ok := s.hub.Get(m.Node)
	if !ok {
		return 0
	}
	var mac string
	for _, ifc := range a.Inv.Ifaces {
		if ifc.Name == m.Iface || (m.Parent != "" && ifc.Name == m.Parent) {
			mac = strings.ToLower(ifc.MAC)
			if mac != "" {
				break
			}
		}
	}
	if mac == "" {
		return 0
	}
	fs, err := s.store.Findings(s.ctx, "", "")
	if err != nil {
		return 0
	}
	for _, f := range fs {
		if !hypervisorSource(f.Source) || f.Gone != 0 {
			continue
		}
		var it discovery.Item
		if json.Unmarshal(f.Data, &it) != nil {
			continue
		}
		for _, n := range it.NICs {
			if n.MAC != mac || n.Bridge == "" {
				continue
			}
			if sp := bridgeSpeed(s, it.Labels["node"], n.Bridge); sp > 0 {
				return sp
			}
		}
	}
	return 0
}

// bridgeSpeed returns the fastest physical member of a bridge (bonds count their members) on
// the agent running on a Proxmox node.
func bridgeSpeed(s *Server, node, bridge string) int {
	for _, a := range s.hub.List() {
		if a.Hostname != node && a.Name != node {
			continue
		}
		byName := map[string]int{}
		for i, ifc := range a.Inv.Ifaces {
			byName[ifc.Name] = i
		}
		var speed func(name string, depth int) int
		speed = func(name string, depth int) int {
			i, ok := byName[name]
			if !ok || depth > 3 {
				return 0
			}
			ifc := a.Inv.Ifaces[i]
			best := ifc.Speed
			if ifc.Kind == "bond" {
				best = 0
			}
			for _, mem := range ifc.Members {
				sp := speed(mem, depth+1)
				if ifc.Kind == "bond" {
					best += sp
				} else if sp > best {
					best = sp
				}
			}
			if ifc.Kind == "vlan" && ifc.Parent != "" && best == 0 {
				best = speed(ifc.Parent, depth+1)
			}
			return best
		}
		if sp := speed(bridge, 0); sp > 0 {
			return sp
		}
	}
	return 0
}
