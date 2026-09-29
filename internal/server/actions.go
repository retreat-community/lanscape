package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/proto"
)

// ActionResult is the outcome of an action on one agent.
type ActionResult struct {
	Agent  string `json:"agent"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// restartable are the finding kinds a restart action applies to.
var restartable = map[string]bool{discovery.KindContainer: true, discovery.KindDeployment: true,
	discovery.KindStatefulSet: true, discovery.KindDaemonSet: true, discovery.KindVM: true, discovery.KindCT: true}

func (s *Server) runAction(ctx context.Context, agentID string, m proto.ActionMsg) ActionResult {
	res := ActionResult{Agent: agentID}
	c, ok := s.hub.Conn(agentID)
	if !ok {
		res.Detail = "agent is offline"
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	raw, err := c.Request(ctx, proto.MsgAction, m)
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	var out proto.ActionResultMsg
	_ = json.Unmarshal(raw, &out)
	res.OK, res.Detail = true, out.Detail
	return res
}

// wakeAgents picks the agents that can reach the device's broadcast domain: those with an
// interface on its subnet or with it in their neighbour table, else all agents that allow
// Wake-on-LAN.
func (s *Server) wakeAgents(ctx context.Context, mac, ip string) []string {
	capable := map[string]bool{}
	var all []string
	near := map[string]bool{}
	target := net.ParseIP(ip)
	for _, a := range s.hub.List() {
		if !a.Online || !hasCap(a.Caps, "action:"+proto.ActionWake) {
			continue
		}
		capable[a.ID] = true
		all = append(all, a.ID)
		for _, ifc := range a.Inv.Ifaces {
			for _, ad := range ifc.Addrs {
				_, n, err := net.ParseCIDR(fmt.Sprintf("%s/%d", ad.IP, ad.Prefix))
				if err == nil && target != nil && n.Contains(target) {
					near[a.ID] = true
				}
			}
		}
	}
	for _, d := range s.devices(ctx) {
		if strings.EqualFold(d.MAC, mac) {
			for _, id := range d.SeenBy {
				near[id] = near[id] || capable[id]
			}
		}
	}
	var out []string
	for id, ok := range near {
		if ok {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		out = all
	}
	sort.Strings(out)
	return out
}

func (s *Server) apiWake(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MAC   string `json:"mac"`
		IP    string `json:"ip"`
		Agent string `json:"agent"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	hw, err := net.ParseMAC(req.MAC)
	if err != nil || len(hw) != 6 {
		writeError(w, http.StatusBadRequest, "a MAC address is required")
		return
	}
	mac := hw.String()
	agents := []string{req.Agent}
	if req.Agent == "" {
		agents = s.wakeAgents(r.Context(), mac, req.IP)
	}
	if len(agents) == 0 {
		writeError(w, http.StatusConflict, "no online agent allows Wake-on-LAN (lanscape-agent --actions wol)")
		return
	}
	results := make([]ActionResult, 0, len(agents))
	okAny := false
	for _, id := range agents {
		res := s.runAction(r.Context(), id, proto.ActionMsg{Action: proto.ActionWake, MAC: mac, IP: req.IP})
		okAny = okAny || res.OK
		results = append(results, res)
	}
	s.audit(r, "action.wol", mac, resultWord(okAny), summarize(results))
	writeJSON(w, http.StatusOK, map[string]any{"ok": okAny, "results": results})
}

func (s *Server) apiRestart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent  string `json:"agent"`
		Source string `json:"source"`
		Key    string `json:"key"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	target := req.Agent + "/" + req.Source + "/" + req.Key
	kind, err := s.findingKind(r.Context(), req.Agent, req.Source, req.Key)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if !restartable[kind] {
		writeError(w, http.StatusBadRequest, "only containers, workloads and virtual machines can be restarted")
		return
	}
	if a, ok := s.hub.Get(req.Agent); !ok || !a.Online || !hasCap(a.Caps, "action:"+proto.ActionRestart) {
		writeError(w, http.StatusConflict, "the agent is offline or does not allow restarts (lanscape-agent --actions wol,restart)")
		return
	}
	res := s.runAction(r.Context(), req.Agent, proto.ActionMsg{Action: proto.ActionRestart, Source: req.Source, Key: req.Key})
	s.audit(r, "action.restart", target, resultWord(res.OK), res.Detail)
	writeJSON(w, http.StatusOK, map[string]any{"ok": res.OK, "results": []ActionResult{res}})
}

func (s *Server) findingKind(ctx context.Context, agentID, source, key string) (string, error) {
	fs, err := s.store.Findings(ctx, agentID, source)
	if err != nil {
		return "", err
	}
	for _, f := range fs {
		if f.Key == key && f.Gone == 0 {
			return f.Kind, nil
		}
	}
	return "", errors.New("no such discovered object")
}

func resultWord(ok bool) string {
	if ok {
		return "ok"
	}
	return "error"
}

func summarize(rs []ActionResult) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		parts = append(parts, r.Agent+": "+r.Detail)
	}
	return strings.Join(parts, "; ")
}
