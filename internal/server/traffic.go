package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/proto"
)

// IfaceTrafficView is the current throughput of one agent interface (§10 "WAN/LAN traffic").
type IfaceTrafficView struct {
	AgentID string `json:"agent_id"`
	Agent   string `json:"agent"`
	Iface   string `json:"iface"`
	Role    string `json:"role,omitempty"` // wan (holds a default route), lan (br-lan, lan) or empty
	RXbps   uint64 `json:"rx_bps"`
	TXbps   uint64 `json:"tx_bps"`
	At      int64  `json:"at"`
}

type trafficState struct {
	mu   sync.Mutex
	last map[string]proto.TrafficMsg // by agent id
}

func (t *trafficState) set(id string, m proto.TrafficMsg) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil {
		t.last = map[string]proto.TrafficMsg{}
	}
	t.last[id] = m
}

func (t *trafficState) get(id string) (proto.TrafficMsg, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.last[id]
	return m, ok
}

func (s *Server) onTraffic(_ context.Context, agentID string, env proto.Envelope) {
	if env.Type != proto.MsgTraffic {
		return
	}
	var m proto.TrafficMsg
	if err := json.Unmarshal(env.Data, &m); err != nil {
		s.log.Warn("invalid traffic report", "agent", agentID, "err", err)
		return
	}
	s.traffic.set(agentID, m)
	name := s.agentName(agentID)
	for _, i := range m.Ifaces {
		s.metrics.SetIfaceTraffic(agentID, name, i.Name, i.RXbps, i.TXbps)
	}
}

// ifaceRole tells WAN (an interface holding a default route) from LAN (the OpenWrt bridge).
func ifaceRole(inv *agent.Inventory, name string) string {
	for _, r := range inv.Routes {
		if (r.Dst == "default" || r.Dst == "0.0.0.0/0") && r.Dev == name {
			return "wan"
		}
	}
	if name == "br-lan" || name == "lan" {
		return "lan"
	}
	return ""
}

// isRouter reports whether an agent routes for a LAN: OpenWrt, or a host with a LAN bridge.
func isRouter(inv *agent.Inventory) bool {
	if inv.Env.Kind == "openwrt" {
		return true
	}
	for _, i := range inv.Ifaces {
		if i.Name == "br-lan" {
			return true
		}
	}
	return false
}

// trafficViews lists the latest throughput per interface; routersOnly keeps the WAN and LAN
// interfaces of routers (the dashboard widget).
func (s *Server) trafficViews(routersOnly bool) []IfaceTrafficView {
	out := []IfaceTrafficView{}
	for _, a := range s.hub.List() {
		if !a.Online || (routersOnly && !isRouter(&a.Inv)) {
			continue
		}
		m, ok := s.traffic.get(a.ID)
		if !ok {
			continue
		}
		for _, i := range m.Ifaces {
			role := ifaceRole(&a.Inv, i.Name)
			if routersOnly && role == "" {
				continue
			}
			out = append(out, IfaceTrafficView{AgentID: a.ID, Agent: a.Name, Iface: i.Name, Role: role, RXbps: i.RXbps,
				TXbps: i.TXbps, At: m.At})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		// WAN first, then LAN, then the rest
		return strings.Compare(roleOrder(out[i].Role), roleOrder(out[j].Role)) < 0
	})
	return out
}

func roleOrder(r string) string {
	switch r {
	case "wan":
		return "0"
	case "lan":
		return "1"
	}
	return "2"
}

func (s *Server) apiTraffic(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.trafficViews(r.URL.Query().Get("routers") == "1"))
}
