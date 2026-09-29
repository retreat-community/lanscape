package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// MapSwitch is a bottleneck hypothesis an operator confirmed: two groups of hosts behind
// switches joined by a slower link (§7.1). It stays on the map until removed.
type MapSwitch struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Segment string   `json:"segment"`
	Mbps    int      `json:"mbps"`
	GroupA  []string `json:"group_a"`
	GroupB  []string `json:"group_b"`
}

func (s *Server) mapSwitches(ctx context.Context) []MapSwitch {
	out := []MapSwitch{}
	_ = s.store.GetSetting(ctx, "map_switches", &out)
	return out
}

// drawSwitches marks confirmed hypotheses and draws their switches and the uplink between them.
func (s *Server) drawSwitches(ctx context.Context, g *MapGraph) {
	sws := s.mapSwitches(ctx)
	if len(sws) == 0 {
		return
	}
	confirmed := map[string]bool{}
	nodes := map[string]bool{}
	for _, n := range g.Nodes {
		nodes[n.ID] = true
	}
	for _, sw := range sws {
		confirmed[sw.ID] = true
		a, b := "sw:"+sw.ID+":a", "sw:"+sw.ID+":b"
		g.Nodes = append(g.Nodes,
			MapNode{ID: a, Label: sw.Name + " A", Type: "switch", Icon: "switch", Data: map[string]any{"switch": sw.ID, "segment": sw.Segment}},
			MapNode{ID: b, Label: sw.Name + " B", Type: "switch", Icon: "switch", Data: map[string]any{"switch": sw.ID, "segment": sw.Segment}})
		g.Edges = append(g.Edges, MapEdge{ID: "sw:" + sw.ID + ":uplink", Source: a, Target: b, Type: "link",
			Verdict: "yellow", Label: fmt.Sprintf("%d Mbit/s", sw.Mbps)})
		for side, group := range map[string][]string{a: sw.GroupA, b: sw.GroupB} {
			for _, id := range group {
				if nodes["dev:"+id] {
					g.Edges = append(g.Edges, MapEdge{ID: side + ":" + id, Source: side, Target: "dev:" + id, Type: "link"})
				}
			}
		}
	}
	for i := range g.Hypotheses {
		g.Hypotheses[i].Accepted = confirmed[g.Hypotheses[i].ID]
	}
}

func (s *Server) apiConfirmSwitch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hypothesis string `json:"hypothesis"`
		Name       string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var h *Hypothesis
	if last, err := s.store.LastRun(r.Context(), KindFull); err == nil {
		var rep Report
		if json.Unmarshal(last.Report, &rep) == nil {
			hs := Bottlenecks(&rep)
			for i := range hs {
				if hs[i].ID == req.Hypothesis {
					h = &hs[i]
				}
			}
		}
	}
	if h == nil {
		writeError(w, http.StatusNotFound, "no such hypothesis in the last run")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "switch"
	}
	sws := s.mapSwitches(r.Context())
	kept := sws[:0]
	for _, sw := range sws {
		if sw.ID != h.ID {
			kept = append(kept, sw)
		}
	}
	kept = append(kept, MapSwitch{ID: h.ID, Name: name, Segment: h.Segment, Mbps: h.Mbps, GroupA: h.GroupA, GroupB: h.GroupB})
	if err := s.store.SetSetting(r.Context(), "map_switches", kept); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "map.switch_confirm", h.ID, "ok", name)
	writeJSON(w, http.StatusOK, kept)
}

func (s *Server) apiDeleteSwitch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sws := s.mapSwitches(r.Context())
	kept := sws[:0]
	for _, sw := range sws {
		if sw.ID != id {
			kept = append(kept, sw)
		}
	}
	if err := s.store.SetSetting(r.Context(), "map_switches", kept); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "map.switch_delete", id, "ok", "")
	w.WriteHeader(http.StatusNoContent)
}
