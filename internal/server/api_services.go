package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/catalog"
	"github.com/retreat-community/lanscape/internal/monitor"
	"github.com/retreat-community/lanscape/internal/notify"
	"github.com/retreat-community/lanscape/internal/store"
)

func (s *Server) servicesRoutes(mux *http.ServeMux, v func(string, http.HandlerFunc) http.HandlerFunc) {
	// heartbeat push URLs are authenticated by their secret token (cron jobs, backup scripts)
	mux.HandleFunc("GET /api/push/{token}", s.apiPush)
	mux.HandleFunc("POST /api/push/{token}", s.apiPush)

	mux.HandleFunc("GET /api/v1/found", v(RoleViewer, s.apiFound))
	mux.HandleFunc("POST /api/v1/found/add", v(RoleOperator, s.apiFoundAdd))
	mux.HandleFunc("POST /api/v1/found/state", v(RoleOperator, s.apiFoundState))
	mux.HandleFunc("POST /api/v1/discovery/rescan", v(RoleOperator, s.apiRescan))
	mux.HandleFunc("GET /api/v1/discovery/rules", v(RoleViewer, s.apiRules))
	mux.HandleFunc("PUT /api/v1/discovery/rules", v(RoleAdmin, s.apiSaveRules))

	mux.HandleFunc("GET /api/v1/services", v(RoleViewer, s.apiServices))
	mux.HandleFunc("POST /api/v1/services", v(RoleOperator, s.apiSaveService))
	mux.HandleFunc("PUT /api/v1/services/order", v(RoleOperator, s.apiServiceOrder))
	mux.HandleFunc("PUT /api/v1/services/{id}", v(RoleOperator, s.apiSaveService))
	mux.HandleFunc("DELETE /api/v1/services/{id}", v(RoleOperator, s.apiDeleteService))

	mux.HandleFunc("GET /api/v1/monitors", v(RoleViewer, s.apiMonitors))
	mux.HandleFunc("POST /api/v1/monitors", v(RoleOperator, s.apiSaveMonitor))
	mux.HandleFunc("POST /api/v1/monitors/test", v(RoleOperator, s.apiTestMonitor))
	mux.HandleFunc("GET /api/v1/monitors/{id}", v(RoleViewer, s.apiMonitor))
	mux.HandleFunc("PUT /api/v1/monitors/{id}", v(RoleOperator, s.apiSaveMonitor))
	mux.HandleFunc("DELETE /api/v1/monitors/{id}", v(RoleOperator, s.apiDeleteMonitor))
	mux.HandleFunc("POST /api/v1/monitors/{id}/check", v(RoleOperator, s.apiCheckNow))

	mux.HandleFunc("GET /api/v1/incidents", v(RoleViewer, s.apiIncidents))
	mux.HandleFunc("GET /api/v1/incidents/{id}", v(RoleViewer, s.apiIncident))
	mux.HandleFunc("POST /api/v1/incidents/{id}/ack", v(RoleOperator, s.apiAckIncident))
	mux.HandleFunc("POST /api/v1/incidents/{id}/notes", v(RoleOperator, s.apiIncidentNote))

	mux.HandleFunc("GET /api/v1/maintenance", v(RoleViewer, s.apiMaintenance))
	mux.HandleFunc("POST /api/v1/maintenance", v(RoleOperator, s.apiSaveMaintenance))
	mux.HandleFunc("DELETE /api/v1/maintenance/{id}", v(RoleOperator, s.apiDeleteMaintenance))

	mux.HandleFunc("GET /api/v1/channels", v(RoleAdmin, s.apiChannels))
	mux.HandleFunc("POST /api/v1/channels", v(RoleAdmin, s.apiSaveChannel))
	mux.HandleFunc("PUT /api/v1/channels/{id}", v(RoleAdmin, s.apiSaveChannel))
	mux.HandleFunc("DELETE /api/v1/channels/{id}", v(RoleAdmin, s.apiDeleteChannel))
	mux.HandleFunc("POST /api/v1/channels/{id}/test", v(RoleAdmin, s.apiTestChannel))

	mux.HandleFunc("GET /api/v1/changes", v(RoleViewer, s.apiChanges))
	mux.HandleFunc("GET /api/v1/dashboard", v(RoleViewer, s.apiDashboard))
	mux.HandleFunc("GET /api/v1/search", v(RoleViewer, s.apiSearch))
}

func (s *Server) actor(r *http.Request) string {
	if p, ok := principal(r.Context()); ok {
		return p.User.Username
	}
	return "anonymous"
}

// --- discovery ---

func (s *Server) apiFound(w http.ResponseWriter, r *http.Request) {
	cards, err := s.foundCards(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if st := r.URL.Query().Get("status"); st != "" {
		out := cards[:0]
		for _, c := range cards {
			if c.Status == st {
				out = append(out, c)
			}
		}
		cards = out
	}
	writeJSON(w, http.StatusOK, cards)
}

func (s *Server) findCard(ctx context.Context, key string) (FoundCard, bool) {
	cards, err := s.foundCards(ctx)
	if err != nil {
		return FoundCard{}, false
	}
	for _, c := range cards {
		if c.Key == key {
			return c, true
		}
	}
	return FoundCard{}, false
}

func (s *Server) apiFoundAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
		addOptions
	}
	if !readJSON(w, r, &req) {
		return
	}
	c, ok := s.findCard(r.Context(), req.Key)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown card")
		return
	}
	if c.Status == FoundAdded && c.ServiceID > 0 {
		writeError(w, http.StatusConflict, "already added")
		return
	}
	id, err := s.addFromCard(r.Context(), &c.Card, req.addOptions)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetFoundState(r.Context(), store.FoundState{Key: c.Key, Status: FoundAdded, ServiceID: id}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "service.add", c.Key, "ok", req.Name)
	s.events.Publish("services", map[string]any{"id": id})
	v, _ := s.store.ServiceByID(r.Context(), id)
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) apiFoundState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key    string `json:"key"`
		Status string `json:"status"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Status != FoundNew && req.Status != FoundIgnored && req.Status != FoundHidden {
		writeError(w, http.StatusBadRequest, "status must be new, ignored or hidden")
		return
	}
	c, ok := s.findCard(r.Context(), req.Key)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown card")
		return
	}
	if c.Status == FoundAdded {
		writeError(w, http.StatusConflict, "delete the service to return the card to the queue")
		return
	}
	if err := s.store.SetFoundState(r.Context(), store.FoundState{Key: c.Key, Status: req.Status}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": req.Status})
}

func (s *Server) apiRescan(w http.ResponseWriter, r *http.Request) {
	n := s.rescan(r.Context())
	writeJSON(w, http.StatusOK, map[string]int{"agents": n})
}

func (s *Server) apiRules(w http.ResponseWriter, r *http.Request) {
	rules := []catalog.Rule{}
	_ = s.store.GetSetting(r.Context(), "discovery_rules", &rules)
	writeJSON(w, http.StatusOK, rules)
}

func (s *Server) apiSaveRules(w http.ResponseWriter, r *http.Request) {
	var rules []catalog.Rule
	if !readJSON(w, r, &rules) {
		return
	}
	for _, rl := range rules {
		if rl.Action != "add" && rl.Action != "ignore" {
			writeError(w, http.StatusBadRequest, "rule action must be add or ignore")
			return
		}
	}
	if err := s.store.SetSetting(r.Context(), "discovery_rules", rules); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "discovery.rules", "", "ok", strconv.Itoa(len(rules)))
	writeJSON(w, http.StatusOK, rules)
}

// --- services ---

// ServiceView is a service with its live status.
type ServiceView struct {
	store.Service
	Status     string   `json:"status"`
	LatencyMS  float64  `json:"latency_ms"`
	Monitors   []int64  `json:"monitors"`
	URL        string   `json:"url"` // smart link for the requesting client
	UptimeDay  *float64 `json:"uptime_day"`
	IncidentID int64    `json:"incident_id,omitempty"`
}

var statusRank = map[string]int{monitor.Down: 5, monitor.Degraded: 4, StatusPending: 3, StatusMaintenance: 2, monitor.Up: 1, StatusPaused: 0}

// clientIsLocal reports whether the request comes from the local network (smart links, §10).
func clientIsLocal(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return true
	}
	if (ip.IsPrivate() || ip.IsLoopback()) && r.Header.Get("X-Forwarded-For") != "" {
		// behind a local reverse proxy: the original client decides
		first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
		if fip := net.ParseIP(strings.TrimSpace(first)); fip != nil {
			ip = fip
		}
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

func smartURL(v *store.Service, local bool) string {
	switch {
	case local && v.InternalURL != "":
		return v.InternalURL
	case v.ExternalURL != "":
		return v.ExternalURL
	}
	return v.InternalURL
}

func (s *Server) serviceViews(ctx context.Context, r *http.Request) ([]ServiceView, error) {
	svcs, err := s.store.Services(ctx)
	if err != nil {
		return nil, err
	}
	mons := s.uptime.snapshot()
	open, _ := s.store.Incidents(ctx, true, 0, 0, 1000)
	incByMon := map[int64]int64{}
	for _, i := range open {
		incByMon[i.MonitorID] = i.ID
	}
	local := clientIsLocal(r)
	out := make([]ServiceView, 0, len(svcs))
	for i := range svcs {
		v := ServiceView{Service: svcs[i], Monitors: []int64{}, URL: smartURL(&svcs[i], local)}
		for _, m := range mons {
			if m.ServiceID != v.ID {
				continue
			}
			v.Monitors = append(v.Monitors, m.ID)
			if statusRank[m.Status] > statusRank[v.Status] || v.Status == "" {
				v.Status = m.Status
				v.LatencyMS = m.LastLatency
			}
			if id := incByMon[m.ID]; id > 0 {
				v.IncidentID = id
			}
			if v.UptimeDay == nil {
				v.UptimeDay, _ = s.uptimeDay(ctx, m.ID)
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) uptimeDay(ctx context.Context, id int64) (*float64, error) {
	now := time.Now()
	days, err := s.store.DayStats(ctx, id, dayKey(now))
	return uptimeOf(days, dayKey(now)), err
}

func (s *Server) apiServices(w http.ResponseWriter, r *http.Request) {
	vs, err := s.serviceViews(r.Context(), r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, vs)
}

func validURL(u string) bool {
	return u == "" || strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

func (s *Server) apiSaveService(w http.ResponseWriter, r *http.Request) {
	var v store.Service
	if !readJSON(w, r, &v) {
		return
	}
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" || len(v.Name) > 100 {
		writeError(w, http.StatusBadRequest, "name is required (up to 100 characters)")
		return
	}
	if !validURL(v.InternalURL) || !validURL(v.ExternalURL) {
		writeError(w, http.StatusBadRequest, "URLs must start with http:// or https://")
		return
	}
	if r.PathValue("id") != "" {
		id, ok := pathID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad id")
			return
		}
		old, err := s.store.ServiceByID(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		v.ID, v.CardKey, v.CreatedAt = id, old.CardKey, old.CreatedAt
		if v.Addresses == nil {
			v.Addresses = old.Addresses
		}
	} else {
		v.ID, v.CardKey = 0, ""
	}
	id, err := s.store.SaveService(r.Context(), v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "service.save", strconv.FormatInt(id, 10), "ok", v.Name)
	s.events.Publish("services", map[string]any{"id": id})
	v, _ = s.store.ServiceByID(r.Context(), id)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) apiServiceOrder(w http.ResponseWriter, r *http.Request) {
	var req []struct {
		ID    int64  `json:"id"`
		Group string `json:"group"`
		Sort  int    `json:"sort"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	for _, o := range req {
		if err := s.store.SetServiceOrder(r.Context(), o.ID, o.Group, o.Sort); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.events.Publish("services", nil)
	writeJSON(w, http.StatusOK, map[string]int{"updated": len(req)})
}

func (s *Server) apiDeleteService(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	withMonitors := r.URL.Query().Get("monitors") == "1"
	if withMonitors {
		for _, m := range s.uptime.snapshot() {
			if m.ServiceID == id {
				s.deleteMonitor(r.Context(), m)
			}
		}
	}
	if err := s.store.DeleteService(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, m := range s.uptime.snapshot() {
		if m.ServiceID == id {
			m.ServiceID = 0
			s.uptime.set(m)
		}
	}
	s.audit(r, "service.delete", strconv.FormatInt(id, 10), "ok", "")
	s.events.Publish("services", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

// --- monitors ---

// MonitorView is a monitor with uptime figures.
type MonitorView struct {
	store.Monitor
	Uptime     Uptime `json:"uptime"`
	IncidentID int64  `json:"incident_id,omitempty"`
}

func (s *Server) apiMonitors(w http.ResponseWriter, r *http.Request) {
	ms := s.uptime.snapshot()
	open, _ := s.store.Incidents(r.Context(), true, 0, 0, 1000)
	out := make([]MonitorView, 0, len(ms))
	for _, m := range ms {
		up, _ := s.uptimeStats(r.Context(), m.ID)
		v := MonitorView{Monitor: m, Uptime: up}
		for _, i := range open {
			if i.MonitorID == m.ID {
				v.IncidentID = i.ID
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	m, ok := s.uptime.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	since := time.Now().Add(-24 * time.Hour).UnixMilli()
	if v, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64); err == nil && v > 0 {
		since = v
	}
	up, days := s.uptimeStats(r.Context(), id)
	checks, _ := s.store.Checks(r.Context(), id, since, 5000)
	incs, _ := s.store.Incidents(r.Context(), false, id, 0, 50)
	var ninety []store.DayStat
	cut := dayKey(time.Now().AddDate(0, 0, -89))
	for _, d := range days {
		if d.Day >= cut {
			ninety = append(ninety, d)
		}
	}
	if ninety == nil {
		ninety = []store.DayStat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"monitor": m, "uptime": up, "days": ninety, "checks": checks, "incidents": incs})
}

type monitorReq struct {
	ServiceID  int64           `json:"service_id"`
	Name       string          `json:"name"`
	Spec       json.RawMessage `json:"spec"`
	IntervalS  int             `json:"interval_s"`
	Retries    int             `json:"retries"`
	Points     []string        `json:"points"`
	MinFailing int             `json:"min_failing"`
	SLA        float64         `json:"sla"`
	Enabled    *bool           `json:"enabled"`
	Parents    []int64         `json:"parents"`
}

// checkRefs validates parents and composite operands (existing monitors, not the monitor itself).
func (s *Server) checkRefs(self int64, spec monitor.Spec, parents []int64) error {
	ids := append([]int64(nil), parents...)
	if spec.Type == monitor.TypeComposite {
		e, err := monitor.ParseExpr(spec.Expr)
		if err != nil {
			return err
		}
		ids = append(ids, e.IDs()...)
	}
	for _, id := range ids {
		if id == self && self != 0 {
			return errors.New("a monitor cannot depend on itself")
		}
		if _, ok := s.uptime.get(id); !ok {
			return fmt.Errorf("unknown monitor #%d", id)
		}
	}
	return nil
}

func (s *Server) apiPush(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ping, _ := strconv.ParseFloat(q.Get("ping"), 64)
	msg := q.Get("msg")
	if len(msg) > 500 {
		msg = msg[:500]
	}
	if !s.uptime.push(r.Context(), r.PathValue("token"), q.Get("status"), msg, ping) {
		writeError(w, http.StatusNotFound, "unknown push token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) checkPoints(points []string) error {
	for _, p := range points {
		if p == pointServer {
			continue
		}
		if _, ok := s.hub.Get(p); !ok {
			return errors.New("unknown observation point " + p)
		}
	}
	return nil
}

func (s *Server) apiSaveMonitor(w http.ResponseWriter, r *http.Request) {
	var req monitorReq
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	spec, err := validateSpec(req.Spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, "spec: "+err.Error())
		return
	}
	self, _ := pathID(r)
	if err := s.checkRefs(self, spec, req.Parents); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.IntervalS != 0 && (req.IntervalS < minIntervalS || req.IntervalS > 86400) {
		writeError(w, http.StatusBadRequest, "interval_s must be between 5 and 86400")
		return
	}
	if err := s.checkPoints(req.Points); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	m := store.Monitor{ServiceID: req.ServiceID, Name: req.Name, Spec: req.Spec, IntervalS: req.IntervalS, Retries: req.Retries,
		Points: req.Points, MinFailing: req.MinFailing, SLA: req.SLA, Enabled: req.Enabled == nil || *req.Enabled, Parents: req.Parents}
	if spec.ServerSide() {
		m.Points = nil
	}
	if m.IntervalS == 0 {
		m.IntervalS = 60
	}
	if m.ServiceID > 0 {
		if _, err := s.store.ServiceByID(r.Context(), m.ServiceID); err != nil {
			writeError(w, http.StatusBadRequest, "unknown service")
			return
		}
	}
	if r.PathValue("id") == "" {
		if spec.Type == monitor.TypeHeartbeat {
			m.PushToken = NewSecret("lsh_")
		}
		id, err := s.createMonitor(r.Context(), m)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit(r, "monitor.create", strconv.FormatInt(id, 10), "ok", m.Name)
		mv, _ := s.uptime.get(id)
		writeJSON(w, http.StatusCreated, mv)
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	old, ok := s.uptime.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	m.ID, m.CreatedAt, m.PushToken, m.LastPush = id, old.CreatedAt, old.PushToken, old.LastPush
	if spec.Type == monitor.TypeHeartbeat && m.PushToken == "" {
		m.PushToken = NewSecret("lsh_")
	}
	if m.Retries <= 0 {
		m.Retries = 3
	}
	if m.MinFailing <= 0 {
		m.MinFailing = 1
	}
	if _, err := s.store.SaveMonitor(r.Context(), m); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.uptime.set(m)
	s.audit(r, "monitor.update", strconv.FormatInt(id, 10), "ok", m.Name)
	mv, _ := s.uptime.get(id)
	writeJSON(w, http.StatusOK, mv)
}

func (s *Server) deleteMonitor(ctx context.Context, m store.Monitor) {
	s.uptime.remove(m.ID)
	_ = s.store.DeleteMonitor(ctx, m.ID)
	s.metrics.DeleteMonitor(m.ID, m.Name)
}

func (s *Server) apiDeleteMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	m, ok := s.uptime.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	s.deleteMonitor(r.Context(), m)
	s.audit(r, "monitor.delete", strconv.FormatInt(id, 10), "ok", m.Name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiCheckNow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	s.uptime.mu.Lock()
	st := s.uptime.mons[id]
	if st != nil {
		if st.running {
			st = nil
		} else {
			st.running = true
		}
	}
	s.uptime.mu.Unlock()
	if st == nil {
		writeError(w, http.StatusConflict, "unknown monitor or a check is running")
		return
	}
	s.uptime.mu.Lock()
	spec, points, minF := st.spec, st.m.Points, st.m.MinFailing
	s.uptime.mu.Unlock()
	rs := s.uptime.observe(r.Context(), spec, points)
	res := combine(rs, minF)
	s.uptime.record(r.Context(), st, res)
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "points": rs})
}

func (s *Server) apiTestMonitor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Spec   json.RawMessage `json:"spec"`
		Points []string        `json:"points"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	spec, err := validateSpec(req.Spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, "spec: "+err.Error())
		return
	}
	if err := s.checkPoints(req.Points); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rs := s.uptime.observe(r.Context(), spec, req.Points)
	writeJSON(w, http.StatusOK, map[string]any{"result": combine(rs, 1), "points": rs})
}

// --- incidents ---

// IncidentView adds the monitor name.
type IncidentView struct {
	store.Incident
	Monitor string `json:"monitor"`
}

func (s *Server) incidentViews(is []store.Incident) []IncidentView {
	names := map[int64]string{}
	for _, m := range s.uptime.snapshot() {
		names[m.ID] = m.Name
	}
	out := make([]IncidentView, 0, len(is))
	for _, i := range is {
		out = append(out, IncidentView{Incident: i, Monitor: names[i.MonitorID]})
	}
	return out
}

func (s *Server) apiIncidents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mid, _ := strconv.ParseInt(q.Get("monitor"), 10, 64)
	days, _ := strconv.Atoi(q.Get("days"))
	if days <= 0 {
		days = 30
	}
	is, err := s.store.Incidents(r.Context(), q.Get("open") == "1", mid, time.Now().AddDate(0, 0, -days).UnixMilli(), 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.incidentViews(is))
}

func (s *Server) apiIncident(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	i, err := s.store.IncidentByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, s.incidentViews([]store.Incident{i})[0])
}

func (s *Server) apiAckIncident(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	if _, err := s.store.IncidentByID(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.store.AckIncident(r.Context(), id, s.actor(r)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "incident.ack", strconv.FormatInt(id, 10), "ok", "")
	s.events.Publish("incident", map[string]any{"id": id, "state": "acked"})
	i, _ := s.store.IncidentByID(r.Context(), id)
	writeJSON(w, http.StatusOK, s.incidentViews([]store.Incident{i})[0])
}

func (s *Server) apiIncidentNote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || len(req.Text) > 4000 {
		writeError(w, http.StatusBadRequest, "text is required (up to 4000 characters)")
		return
	}
	if _, err := s.store.IncidentByID(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if _, err := s.store.AddIncidentNote(r.Context(), id, s.actor(r), req.Text); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	i, _ := s.store.IncidentByID(r.Context(), id)
	writeJSON(w, http.StatusCreated, s.incidentViews([]store.Incident{i})[0])
}

// --- maintenance ---

func (s *Server) apiMaintenance(w http.ResponseWriter, r *http.Request) {
	ms, err := s.store.Maintenances(r.Context(), time.Now().UnixMilli())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ms)
}

func (s *Server) apiSaveMaintenance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		store.Maintenance
		DurationMin int `json:"duration_min"` // "right now for N minutes"
	}
	if !readJSON(w, r, &req) {
		return
	}
	m := req.Maintenance
	if req.DurationMin > 0 {
		m.Starts = time.Now().UnixMilli()
		m.Ends = m.Starts + int64(req.DurationMin)*60000
	}
	if m.Ends <= m.Starts || m.Ends-m.Starts > 90*24*3600*1000 {
		writeError(w, http.StatusBadRequest, "ends must be after starts (at most 90 days)")
		return
	}
	if strings.TrimSpace(m.Name) == "" {
		m.Name = "Maintenance"
	}
	m.ID, m.CreatedBy = 0, s.actor(r)
	id, err := s.store.SaveMaintenance(r.Context(), m)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.uptime.reloadMaint(r.Context())
	s.audit(r, "maintenance.create", strconv.FormatInt(id, 10), "ok", m.Name)
	s.events.Publish("maintenance", nil)
	m.ID = id
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) apiDeleteMaintenance(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.store.DeleteMaintenance(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.uptime.reloadMaint(r.Context())
	s.audit(r, "maintenance.delete", strconv.FormatInt(id, 10), "ok", "")
	s.events.Publish("maintenance", nil)
	w.WriteHeader(http.StatusNoContent)
}

// --- channels ---

func (s *Server) apiChannels(w http.ResponseWriter, r *http.Request) {
	cs, err := s.store.Channels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range cs {
		cs[i].Config = notify.Redact(cs[i].Type, cs[i].Config)
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) apiSaveChannel(w http.ResponseWriter, r *http.Request) {
	var c store.Channel
	if !readJSON(w, r, &c) {
		return
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		c.Name = c.Type
	}
	if r.PathValue("id") != "" {
		id, ok := pathID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad id")
			return
		}
		old, err := s.store.ChannelByID(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		c.ID = id
		if c.Type == old.Type {
			c.Config = notify.KeepSecrets(c.Type, c.Config, old.Config)
		}
	} else {
		c.ID = 0
	}
	if _, err := notify.New(c.Type, c.Config, nil); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var common notify.Common
	if err := json.Unmarshal(c.Config, &common); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.store.SaveChannel(r.Context(), c)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "channel.save", strconv.FormatInt(id, 10), "ok", c.Type+" "+c.Name)
	c, _ = s.store.ChannelByID(r.Context(), id)
	c.Config = notify.Redact(c.Type, c.Config)
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) apiDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.store.DeleteChannel(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "channel.delete", strconv.FormatInt(id, 10), "ok", "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiTestChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	c, err := s.store.ChannelByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	msg := notify.Message{Event: "test", Severity: notify.SevInfo, Title: "Lanscape test notification",
		Text: "The channel \"" + c.Name + "\" works.", At: time.Now().UnixMilli()}
	if err := s.uptime.send(r.Context(), c, msg); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// --- feed, dashboard, search ---

func (s *Server) apiChanges(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cs, err := s.store.Changes(r.Context(), before, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

// Dashboard is the start page (§10).
type Dashboard struct {
	Groups       []TileGroup         `json:"groups"`
	Summary      map[string]int      `json:"summary"`
	Incidents    []IncidentView      `json:"incidents"`
	Network      *NetworkSummary     `json:"network"`
	Agents       []AgentResources    `json:"agents"`
	Certificates []CertExpiry        `json:"certificates"`
	Changes      []store.Change      `json:"changes"`
	FoundNew     int                 `json:"found_new"`
	Maintenance  []store.Maintenance `json:"maintenance"`
}

// TileGroup is a named group of tiles.
type TileGroup struct {
	Name  string        `json:"name"`
	Tiles []ServiceView `json:"tiles"`
}

// NetworkSummary describes the last full network run.
type NetworkSummary struct {
	RunID    int64          `json:"run_id"`
	Finished int64          `json:"finished"`
	Status   string         `json:"status"`
	Paths    int            `json:"paths"`
	Verdicts map[string]int `json:"verdicts"`
	Problems int            `json:"problems"`
}

// AgentResources are the host resources shown on the dashboard.
type AgentResources struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Online   bool    `json:"online"`
	CPUs     int     `json:"cpus"`
	Load1    float64 `json:"load1"`
	MemTotal uint64  `json:"mem_total"`
	MemAvail uint64  `json:"mem_available"`
	TempC    float64 `json:"temp_c,omitempty"`
	DiskPct  float64 `json:"disk_pct"` // fullest filesystem
	DiskName string  `json:"disk_mount,omitempty"`
	Updates  int     `json:"updates"`
}

// CertExpiry is a certificate that expires soon.
type CertExpiry struct {
	Name     string `json:"name"`
	NotAfter int64  `json:"not_after"`
	Source   string `json:"source"` // monitor, discovery
	Ref      string `json:"ref,omitempty"`
}

func (s *Server) apiDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := Dashboard{Summary: map[string]int{}, Groups: []TileGroup{}, Agents: []AgentResources{}, Certificates: []CertExpiry{}}
	views, err := s.serviceViews(ctx, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	gi := map[string]int{}
	for _, v := range views {
		if !v.Tile {
			continue
		}
		i, ok := gi[v.Group]
		if !ok {
			i = len(d.Groups)
			gi[v.Group] = i
			d.Groups = append(d.Groups, TileGroup{Name: v.Group})
		}
		d.Groups[i].Tiles = append(d.Groups[i].Tiles, v)
	}
	mons := s.uptime.snapshot()
	for _, m := range mons {
		d.Summary[m.Status]++
	}
	d.Summary["monitors"] = len(mons)
	d.Summary["services"] = len(views)
	open, _ := s.store.Incidents(ctx, true, 0, 0, 100)
	d.Incidents = s.incidentViews(open)
	d.Summary["incidents"] = len(open)
	if last, err := s.store.LastRun(ctx, KindFull); err == nil {
		var rep Report
		if json.Unmarshal(last.Report, &rep) == nil {
			ns := &NetworkSummary{RunID: last.ID, Finished: rep.Finished, Status: rep.Status, Paths: len(rep.Paths),
				Problems: len(rep.Problems), Verdicts: map[string]int{}}
			for _, p := range rep.Paths {
				ns.Verdicts[p.Verdict]++
			}
			d.Network = ns
		}
	}
	for _, a := range s.hub.List() {
		res := a.Inv.Resources
		ar := AgentResources{ID: a.ID, Name: a.Name, Online: a.Online, CPUs: res.CPUs, Load1: res.Load1, MemTotal: res.MemTotal,
			MemAvail: res.MemAvail, TempC: res.TempC, Updates: res.Updates}
		for _, dk := range res.Disks {
			if dk.Total == 0 {
				continue
			}
			if p := float64(dk.Used) * 100 / float64(dk.Total); p > ar.DiskPct {
				ar.DiskPct, ar.DiskName = p, dk.Mount
			}
		}
		d.Agents = append(d.Agents, ar)
	}
	soon := time.Now().Add(30 * 24 * time.Hour).UnixMilli()
	for _, m := range mons {
		if m.CertNotAfter > 0 && m.CertNotAfter < soon {
			d.Certificates = append(d.Certificates, CertExpiry{Name: m.Name, NotAfter: m.CertNotAfter, Source: "monitor",
				Ref: strconv.FormatInt(m.ID, 10)})
		}
	}
	cards, _ := s.foundCards(ctx)
	for _, c := range cards {
		if c.Status == FoundNew && !c.Gone {
			d.FoundNew++
		}
		if c.CertNotAfter > 0 && c.CertNotAfter < soon && !c.Gone {
			d.Certificates = append(d.Certificates, CertExpiry{Name: c.Name, NotAfter: c.CertNotAfter, Source: "discovery", Ref: c.Key})
		}
	}
	sort.Slice(d.Certificates, func(a, b int) bool { return d.Certificates[a].NotAfter < d.Certificates[b].NotAfter })
	d.Changes, _ = s.store.Changes(ctx, 0, 10)
	now := time.Now().UnixMilli()
	ms, _ := s.store.Maintenances(ctx, now)
	d.Maintenance = []store.Maintenance{}
	for _, m := range ms {
		if m.Starts <= now+24*3600*1000 {
			d.Maintenance = append(d.Maintenance, m)
		}
	}
	writeJSON(w, http.StatusOK, d)
}

// SearchResult is an entry of the command palette.
type SearchResult struct {
	Type     string `json:"type"` // service, monitor, agent, device, found
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	URL      string `json:"url,omitempty"` // service link
	Href     string `json:"href"`          // panel route
	Status   string `json:"status,omitempty"`
}

func (s *Server) apiSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	out := []SearchResult{}
	match := func(fields ...string) bool {
		if q == "" {
			return true
		}
		for _, f := range fields {
			if strings.Contains(strings.ToLower(f), q) {
				return true
			}
		}
		return false
	}
	views, _ := s.serviceViews(r.Context(), r)
	for _, v := range views {
		fields := []string{v.Name, v.InternalURL, v.ExternalURL, v.AppID}
		for _, a := range v.Addresses {
			fields = append(fields, a.Value)
		}
		if match(fields...) {
			out = append(out, SearchResult{Type: "service", ID: strconv.FormatInt(v.ID, 10), Title: v.Name, Subtitle: v.URL,
				URL: v.URL, Href: "/services/" + strconv.FormatInt(v.ID, 10), Status: v.Status})
		}
	}
	for _, m := range s.uptime.snapshot() {
		var sp monitor.Spec
		_ = json.Unmarshal(m.Spec, &sp)
		if match(m.Name, sp.Target) {
			out = append(out, SearchResult{Type: "monitor", ID: strconv.FormatInt(m.ID, 10), Title: m.Name, Subtitle: sp.Type + " " + sp.Target,
				Href: "/monitors/" + strconv.FormatInt(m.ID, 10), Status: m.Status})
		}
	}
	seenIP := map[string]bool{}
	for _, a := range s.hub.List() {
		fields := []string{a.Name, a.Hostname, a.ID}
		var ips []string
		for _, ifc := range a.Inv.Ifaces {
			fields = append(fields, ifc.MAC)
			for _, ad := range ifc.Addrs {
				fields = append(fields, ad.IP)
				ips = append(ips, ad.IP)
				seenIP[ad.IP] = true
			}
		}
		if match(fields...) {
			st := "offline"
			if a.Online {
				st = "online"
			}
			out = append(out, SearchResult{Type: "agent", ID: a.ID, Title: a.Name, Subtitle: strings.Join(firstN(ips, 3), ", "),
				Href: "/devices?agent=" + a.ID, Status: st})
		}
		for _, n := range a.Inv.Neighbors {
			if seenIP[n.IP] || q == "" || !match(n.IP, n.MAC) {
				continue
			}
			seenIP[n.IP] = true
			out = append(out, SearchResult{Type: "device", ID: n.MAC, Title: n.IP, Subtitle: n.MAC + " · " + a.Name + " " + n.Dev,
				Href: "/devices?ip=" + n.IP})
		}
	}
	if cards, err := s.foundCards(r.Context()); err == nil {
		for _, c := range cards {
			if c.Status != FoundNew || c.Gone {
				continue
			}
			if match(c.Name, c.InternalURL, c.ExternalURL, c.HostPort) {
				out = append(out, SearchResult{Type: "found", ID: c.Key, Title: c.Name, Subtitle: c.HostPort, Href: "/services?tab=found"})
			}
		}
	}
	if len(out) > 50 {
		out = out[:50]
	}
	writeJSON(w, http.StatusOK, out)
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
