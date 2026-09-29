package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/testengine"
)

// apiIperf3 measures from an agent to a device running "iperf3 -s" (operator).
func (s *Server) apiIperf3(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent string `json:"agent"`
		proto.Iperf3Msg
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Host = strings.TrimSpace(req.Host)
	if req.Host == "" || strings.ContainsAny(req.Host, " /") || req.Port < 0 || req.Port > 65535 {
		writeError(w, http.StatusBadRequest, "host (and an optional port) of the iperf3 server are required")
		return
	}
	req.Seconds = min(max(req.Seconds, 1), 60)
	a, ok := s.hub.Get(req.Agent)
	if !ok || !a.Online || !hasCap(a.Caps, proto.MsgIperf3) {
		writeError(w, http.StatusBadRequest, "the agent is offline or cannot run iperf3 tests")
		return
	}
	c, ok := s.hub.Conn(req.Agent)
	if !ok {
		writeError(w, http.StatusBadRequest, "the agent is offline")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.Seconds)*time.Second+time.Minute)
	defer cancel()
	raw, err := c.Request(ctx, proto.MsgIperf3, req.Iperf3Msg)
	target := a.Name + " → " + req.Host
	if err != nil {
		s.audit(r, "test.iperf3", target, "error", err.Error())
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var res testengine.Iperf3Result
	if err := json.Unmarshal(raw, &res); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.audit(r, "test.iperf3", target, "ok", "")
	writeJSON(w, http.StatusOK, res)
}
