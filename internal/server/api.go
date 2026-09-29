package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/retreat-community/lanscape/internal/pki"
	"github.com/retreat-community/lanscape/internal/store"
	"github.com/retreat-community/lanscape/internal/topo"
)

// Handler returns the UI/API handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	v := s.require
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.Handle("GET /metrics", s.metricsHandler())

	mux.HandleFunc("GET /api/v1/setup", s.apiSetupState)
	mux.HandleFunc("POST /api/v1/setup", s.apiSetup)
	mux.HandleFunc("POST /api/v1/auth/login", s.apiLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.apiLogout)
	mux.HandleFunc("GET /api/v1/auth/me", v(RoleViewer, s.apiMe))
	mux.HandleFunc("GET /api/v1/auth/oidc", s.apiOIDCInfo)
	mux.HandleFunc("GET /api/v1/auth/oidc/login", s.apiOIDCLogin)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", s.apiOIDCCallback)
	mux.HandleFunc("POST /api/v1/auth/password", v(RoleViewer, s.apiChangePassword))
	mux.HandleFunc("POST /api/v1/auth/totp", v(RoleViewer, s.apiTOTPStart))
	mux.HandleFunc("PUT /api/v1/auth/totp", v(RoleViewer, s.apiTOTPConfirm))
	mux.HandleFunc("DELETE /api/v1/auth/totp", v(RoleViewer, s.apiTOTPDisable))

	mux.HandleFunc("GET /api/v1/users", v(RoleAdmin, s.apiUsers))
	mux.HandleFunc("POST /api/v1/users", v(RoleAdmin, s.apiCreateUser))
	mux.HandleFunc("PATCH /api/v1/users/{id}", v(RoleAdmin, s.apiUpdateUser))
	mux.HandleFunc("DELETE /api/v1/users/{id}", v(RoleAdmin, s.apiDeleteUser))
	mux.HandleFunc("GET /api/v1/tokens", v(RoleViewer, s.apiTokens))
	mux.HandleFunc("POST /api/v1/tokens", v(RoleViewer, s.apiCreateToken))
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", v(RoleViewer, s.apiDeleteToken))

	mux.HandleFunc("GET /api/v1/agent-tokens", v(RoleAdmin, s.apiAgentTokens))
	mux.HandleFunc("POST /api/v1/agent-tokens", v(RoleAdmin, s.apiCreateAgentToken))
	mux.HandleFunc("DELETE /api/v1/agent-tokens/{id}", v(RoleAdmin, s.apiDeleteAgentToken))
	mux.HandleFunc("GET /api/v1/agents", v(RoleViewer, s.apiAgents))
	mux.HandleFunc("GET /api/v1/agents/{id}", v(RoleViewer, s.apiAgent))
	mux.HandleFunc("PATCH /api/v1/agents/{id}", v(RoleAdmin, s.apiUpdateAgent))
	mux.HandleFunc("DELETE /api/v1/agents/{id}", v(RoleAdmin, s.apiDeleteAgent))
	mux.HandleFunc("GET /api/v1/install", v(RoleAdmin, s.apiInstall))

	mux.HandleFunc("GET /api/v1/segments", v(RoleViewer, s.apiSegments))
	mux.HandleFunc("PUT /api/v1/segments/{id}", v(RoleAdmin, s.apiSetSegment))
	mux.HandleFunc("GET /api/v1/ipam", v(RoleViewer, s.apiIPAM))
	mux.HandleFunc("GET /api/v1/anomalies", v(RoleViewer, s.apiAnomalies))
	mux.HandleFunc("GET /api/v1/map", v(RoleViewer, s.apiMap))

	mux.HandleFunc("GET /api/v1/runs", v(RoleViewer, s.apiRuns))
	mux.HandleFunc("POST /api/v1/runs", v(RoleOperator, s.apiStartRun))
	mux.HandleFunc("GET /api/v1/runs/estimate", v(RoleViewer, s.apiEstimate))
	mux.HandleFunc("GET /api/v1/runs/active", v(RoleViewer, s.apiActiveRun))
	mux.HandleFunc("GET /api/v1/runs/last", v(RoleViewer, s.apiLastRun))
	mux.HandleFunc("GET /api/v1/runs/{id}", v(RoleViewer, s.apiRun))
	mux.HandleFunc("POST /api/v1/runs/cancel", v(RoleOperator, s.apiCancelRun))
	mux.HandleFunc("GET /api/v1/schedules", v(RoleViewer, s.apiSchedules))
	mux.HandleFunc("POST /api/v1/schedules", v(RoleAdmin, s.apiSaveSchedule))
	mux.HandleFunc("PUT /api/v1/schedules/{id}", v(RoleAdmin, s.apiSaveSchedule))
	mux.HandleFunc("DELETE /api/v1/schedules/{id}", v(RoleAdmin, s.apiDeleteSchedule))

	mux.HandleFunc("GET /api/v1/settings", v(RoleViewer, s.apiSettings))
	mux.HandleFunc("PUT /api/v1/settings", v(RoleAdmin, s.apiSaveSettings))
	mux.HandleFunc("GET /api/v1/audit", v(RoleAdmin, s.apiAudit))
	mux.HandleFunc("GET /api/v1/events", v(RoleViewer, s.events.ServeHTTP))
	s.servicesRoutes(mux, v)
	for _, r := range s.extraRoutes {
		mux.HandleFunc(r.pattern, v(r.role, r.h))
	}
	mux.Handle("/", s.spa())
	return securityHeaders(s.statusDomain(mux))
}

type route struct {
	pattern string
	role    string
	h       http.HandlerFunc
}

// Route registers an additional authenticated API route (used by modules).
func (s *Server) Route(pattern, role string, h http.HandlerFunc) {
	s.extraRoutes = append(s.extraRoutes, route{pattern, role, h})
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet && r.Method != http.MethodHead {
			// CSRF: state-changing API calls must be JSON (or YAML configuration) or carry a bearer
			// token; neither type can be sent cross-site without a CORS preflight
			ct := r.Header.Get("Content-Type")
			if r.Header.Get("Authorization") == "" && r.ContentLength > 0 && !strings.HasPrefix(ct, "application/json") &&
				!strings.HasPrefix(ct, "application/yaml") {
				writeError(w, http.StatusUnsupportedMediaType, "application/json required")
				return
			}
			if o := r.Header.Get("Origin"); o != "" && r.Header.Get("Authorization") == "" && !sameOrigin(o, r) {
				writeError(w, http.StatusForbidden, "cross-origin request rejected")
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func sameOrigin(origin string, r *http.Request) bool {
	o := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	return o == r.Host
}

func (s *Server) metricsHandler() http.Handler {
	h := promhttp.HandlerFor(s.metrics.Registry, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.MetricsToken != "" && r.Header.Get("Authorization") != "Bearer "+s.cfg.MetricsToken {
			writeError(w, http.StatusUnauthorized, "metrics token required")
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) audit(r *http.Request, action, target, result, detail string) {
	actor := "anonymous"
	if p, ok := principal(r.Context()); ok {
		actor = p.User.Username
	}
	_ = s.store.Audit(r.Context(), store.AuditEntry{Actor: actor, Action: action, Target: target, Result: result, Detail: detail})
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

// --- setup and authentication ---

func (s *Server) apiSetupState(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"needs_setup": n == 0, "version": s.cfg.Version})
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Code     string `json:"code,omitempty"`
}

func validPassword(p string) error {
	if len(p) < 10 {
		return errors.New("password must have at least 10 characters")
	}
	return nil
}

func (s *Server) apiSetup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	n, err := s.store.CountUsers(r.Context())
	if err != nil || n > 0 {
		writeError(w, http.StatusConflict, "already set up")
		return
	}
	if c.Username == "" || validPassword(c.Password) != nil {
		writeError(w, http.StatusBadRequest, "username and a password of at least 10 characters are required")
		return
	}
	h, err := HashPassword(c.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, err := s.store.CreateUser(r.Context(), store.User{Username: c.Username, PasswordHash: h, Role: RoleAdmin})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	u, _ := s.store.UserByID(r.Context(), id)
	if err := s.startSession(w, r, u); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.Audit(r.Context(), store.AuditEntry{Actor: c.Username, Action: "setup", Result: "ok"})
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) apiLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	u, err := s.login(w, r, c.Username, c.Password, c.Code)
	if err != nil {
		_ = s.store.Audit(r.Context(), store.AuditEntry{Actor: c.Username, Action: "login", Result: "denied", Detail: clientIP(r)})
		code := http.StatusUnauthorized
		if strings.Contains(err.Error(), "two-factor") {
			writeJSON(w, code, map[string]any{"error": err.Error(), "totp_required": true})
			return
		}
		writeError(w, code, err.Error())
		return
	}
	_ = s.store.Audit(r.Context(), store.AuditEntry{Actor: u.Username, Action: "login", Result: "ok", Detail: clientIP(r)})
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) apiLogout(w http.ResponseWriter, r *http.Request) {
	s.logout(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"user": p.User, "role": p.Role, "version": s.cfg.Version})
}

func (s *Server) apiChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p, _ := principal(r.Context())
	if !CheckPassword(p.User.PasswordHash, req.Old) {
		writeError(w, http.StatusForbidden, "current password is wrong")
		return
	}
	if err := validPassword(req.New); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h, _ := HashPassword(req.New)
	p.User.PasswordHash = h
	if err := s.store.UpdateUser(r.Context(), p.User); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "user.password", p.User.Username, "ok", "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) apiTOTPStart(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	secret, url, err := NewTOTP(p.User.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "url": url})
}

func (s *Server) apiTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !ValidateTOTP(req.Secret, req.Code) {
		writeError(w, http.StatusBadRequest, "code does not match")
		return
	}
	p, _ := principal(r.Context())
	p.User.TOTPSecret = req.Secret
	if err := s.store.UpdateUser(r.Context(), p.User); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "user.totp.enable", p.User.Username, "ok", "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) apiTOTPDisable(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	p.User.TOTPSecret = ""
	if err := s.store.UpdateUser(r.Context(), p.User); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "user.totp.disable", p.User.Username, "ok", "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- users and tokens ---

func (s *Server) apiUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.store.Users(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if us == nil {
		us = []store.User{}
	}
	writeJSON(w, http.StatusOK, us)
}

func (s *Server) apiCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Username == "" || !ValidRole(req.Role) || validPassword(req.Password) != nil {
		writeError(w, http.StatusBadRequest, "username, role (viewer/operator/admin) and a password of 10+ characters are required")
		return
	}
	h, _ := HashPassword(req.Password)
	id, err := s.store.CreateUser(r.Context(), store.User{Username: req.Username, PasswordHash: h, Role: req.Role})
	if err != nil {
		writeError(w, http.StatusConflict, "user exists")
		return
	}
	s.audit(r, "user.create", req.Username, "ok", req.Role)
	u, _ := s.store.UserByID(r.Context(), id)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) apiUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	u, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such user")
		return
	}
	var req struct {
		Role      *string `json:"role"`
		Password  *string `json:"password"`
		Disabled  *bool   `json:"disabled"`
		ResetTOTP bool    `json:"reset_totp"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Role != nil {
		if !ValidRole(*req.Role) {
			writeError(w, http.StatusBadRequest, "invalid role")
			return
		}
		u.Role = *req.Role
	}
	if req.Password != nil {
		if err := validPassword(*req.Password); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		u.PasswordHash, _ = HashPassword(*req.Password)
	}
	if req.Disabled != nil {
		u.Disabled = *req.Disabled
	}
	if req.ResetTOTP {
		u.TOTPSecret = ""
	}
	if err := s.store.UpdateUser(r.Context(), u); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "user.update", u.Username, "ok", "")
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) apiDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	p, _ := principal(r.Context())
	if !ok || id == p.User.ID {
		writeError(w, http.StatusBadRequest, "cannot delete this user")
		return
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "user.delete", strconv.FormatInt(id, 10), "ok", "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiTokens(w http.ResponseWriter, r *http.Request) {
	p, _ := principal(r.Context())
	uid := p.User.ID
	if p.Role == RoleAdmin && r.URL.Query().Get("all") == "1" {
		uid = 0
	}
	ts, err := s.store.APITokens(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) apiCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Role     string `json:"role"`
		ExpiresS int64  `json:"expires_in_s"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p, _ := principal(r.Context())
	if req.Role == "" {
		req.Role = p.Role
	}
	if !ValidRole(req.Role) || roleRank[req.Role] > roleRank[p.Role] || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name and a role not above your own are required")
		return
	}
	raw := NewSecret("lsk_")
	t := store.APIToken{UserID: p.User.ID, Name: req.Name, Role: req.Role}
	if req.ExpiresS > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(req.ExpiresS) * time.Second).UnixMilli()
	}
	id, err := s.store.CreateAPIToken(r.Context(), t, hashToken(raw))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "token.create", req.Name, "ok", req.Role)
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "token": raw})
}

func (s *Server) apiDeleteToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	p, _ := principal(r.Context())
	uid := p.User.ID
	if p.Role == RoleAdmin {
		uid = 0
	}
	if err := s.store.DeleteAPIToken(r.Context(), id, uid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "token.delete", strconv.FormatInt(id, 10), "ok", "")
	w.WriteHeader(http.StatusNoContent)
}

// --- agents ---

func (s *Server) apiAgentTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := s.store.AgentTokens(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) apiCreateAgentToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string            `json:"name"`
		Labels   map[string]string `json:"labels"`
		Reusable bool              `json:"reusable"`
		ExpiresS int64             `json:"expires_in_s"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		req.Name = "token-" + time.Now().Format("20060102-150405")
	}
	raw := NewSecret("lsr_")
	t := store.AgentToken{Name: req.Name, Labels: req.Labels, Reusable: req.Reusable}
	if req.ExpiresS > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(req.ExpiresS) * time.Second).UnixMilli()
	}
	id, err := s.store.CreateAgentToken(r.Context(), t, hashToken(raw))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "agent_token.create", req.Name, "ok", "")
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "token": raw, "install": s.installCommands(r, raw)})
}

func (s *Server) apiDeleteAgentToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.store.DeleteAgentToken(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "agent_token.delete", strconv.FormatInt(id, 10), "ok", "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) gatewayAddr(r *http.Request) string {
	host := r.Host
	if h, _, err := splitHost(host); err == nil {
		host = h
	}
	if len(s.cfg.GatewayHosts) > 0 && s.cfg.GatewayHosts[0] != "" {
		host = s.cfg.GatewayHosts[0]
	}
	_, port, _ := splitHost(s.cfg.GatewayListen)
	if port == "" {
		port = "8443"
	}
	return host + ":" + port
}

// installCommands generates per-platform install snippets for the "Add node" wizard.
func (s *Server) installCommands(r *http.Request, token string) map[string]string {
	gw := s.gatewayAddr(r)
	fp := pki.Fingerprint(s.ca.Cert)
	base := "https://raw.githubusercontent.com/retreat-community/lanscape/main/scripts"
	args := fmt.Sprintf("--server %s --token %s --ca-fingerprint %s", gw, token, fp)
	return map[string]string{
		"linux":   fmt.Sprintf("curl -fsSL %s/install.sh | sudo sh -s -- agent %s", base, args),
		"docker":  fmt.Sprintf("docker run -d --name lanscape-agent --network host --cap-add NET_RAW --cap-add NET_ADMIN -v lanscape-agent:/var/lib/lanscape-agent ghcr.io/retreat-community/lanscape-agent:latest %s", args),
		"compose": fmt.Sprintf("LANSCAPE_SERVER=%s LANSCAPE_TOKEN=%s LANSCAPE_CA_FINGERPRINT=%s docker compose -f agent-nas.yaml up -d", gw, token, fp),
		"helm":    fmt.Sprintf("helm install lanscape oci://ghcr.io/retreat-community/charts/lanscape --set server.enabled=false --set agent.server=%s --set agent.token=%s --set agent.caFingerprint=%s", gw, token, fp),
		"openwrt": fmt.Sprintf("apk add lanscape-agent luci-app-lanscape && uci set lanscape.agent.server=%s && uci set lanscape.agent.token=%s && uci set lanscape.agent.ca_fingerprint=%s && uci set lanscape.agent.enabled=1 && uci commit lanscape && /etc/init.d/lanscape-agent restart", gw, token, fp),
		"windows": fmt.Sprintf("lanscape-agent.exe service install %s", args),
		"macos":   fmt.Sprintf("sudo lanscape-agent service install %s", args),
		"freebsd": fmt.Sprintf("sysrc lanscape_agent_enable=YES lanscape_agent_flags=\"%s\" && service lanscape_agent start", args),
	}
}

func (s *Server) apiInstall(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"gateway": s.gatewayAddr(r), "ca_fingerprint": pki.Fingerprint(s.ca.Cert),
		"commands": s.installCommands(r, "<token>")})
}

type agentView struct {
	AgentState
	Labels map[string]string `json:"labels"`
}

func (s *Server) apiAgents(w http.ResponseWriter, r *http.Request) {
	recs, _ := s.store.Agents(r.Context())
	labels := map[string]map[string]string{}
	for _, a := range recs {
		labels[a.ID] = a.Labels
	}
	out := []agentView{}
	for _, a := range s.hub.List() {
		out = append(out, agentView{AgentState: a, Labels: labels[a.ID]})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiAgent(w http.ResponseWriter, r *http.Request) {
	a, ok := s.hub.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such agent")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) apiUpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, err := s.store.AgentByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such agent")
		return
	}
	var req struct {
		Name   *string           `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name != nil && *req.Name != "" {
		rec.Name = *req.Name
		s.hub.Rename(id, rec.Name)
	}
	if req.Labels != nil {
		rec.Labels = req.Labels
	}
	if err := s.store.UpsertAgent(r.Context(), rec); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "agent.update", id, "ok", "")
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) apiDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteAgent(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.hub.Remove(id)
	s.audit(r, "agent.delete", id, "ok", "")
	w.WriteHeader(http.StatusNoContent)
}

// --- topology ---

func (s *Server) currentSegments(r *http.Request) []topo.Segment {
	manual := map[string]int{}
	cfgs, _ := s.store.SegmentConfigs(r.Context())
	for id, c := range cfgs {
		if c.ExpectedMbps > 0 {
			manual[id] = c.ExpectedMbps
		}
	}
	for k, v := range s.cfg.Expect {
		if _, ok := manual[k]; !ok {
			manual[k] = v
		}
	}
	return topo.BuildSegments(s.hub.Nodes(false), manual)
}

func (s *Server) apiSegments(w http.ResponseWriter, r *http.Request) {
	segs := s.currentSegments(r)
	cfgs, _ := s.store.SegmentConfigs(r.Context())
	type segView struct {
		topo.Segment
		Name string `json:"name"`
	}
	out := []segView{}
	for _, sg := range segs {
		out = append(out, segView{Segment: sg, Name: cfgs[sg.ID].Name})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiSetSegment(w http.ResponseWriter, r *http.Request) {
	var c store.SegmentConfig
	if !readJSON(w, r, &c) {
		return
	}
	c.SegmentID = r.PathValue("id")
	if c.ExpectedMbps < 0 {
		writeError(w, http.StatusBadRequest, "expected_mbps must not be negative")
		return
	}
	if err := s.store.SetSegmentConfig(r.Context(), c); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "segment.update", c.SegmentID, "ok", strconv.Itoa(c.ExpectedMbps))
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) apiIPAM(w http.ResponseWriter, r *http.Request) {
	segs := s.currentSegments(r)
	writeJSON(w, http.StatusOK, topo.BuildIPAM(segs, s.hub.Nodes(false), s.discoveredExtras(), s.dhcpPools()))
}

// DHCPPools returns known DHCP ranges (filled by the OpenWrt module).
var DHCPPools func(s *Server) []topo.Pool

func (s *Server) dhcpPools() []topo.Pool {
	if DHCPPools != nil {
		return DHCPPools(s)
	}
	return nil
}

func (s *Server) apiAnomalies(w http.ResponseWriter, r *http.Request) {
	known, _ := s.store.KnownMACs(r.Context())
	writeJSON(w, http.StatusOK, topo.Anomalies(s.hub.Nodes(false), s.currentSegments(r), known, s.discoveredExtras()))
}

func (s *Server) apiMap(w http.ResponseWriter, r *http.Request) {
	var rep *Report
	if last, err := s.store.LastRun(r.Context(), KindFull); err == nil {
		var rp Report
		if json.Unmarshal(last.Report, &rp) == nil {
			rep = &rp
		}
	}
	writeJSON(w, http.StatusOK, s.buildMap(r.Context(), s.currentSegments(r), rep))
}

// --- runs ---

func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	runs, err := s.store.Runs(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) apiStartRun(w http.ResponseWriter, r *http.Request) {
	var opts RunOptions
	if r.ContentLength > 0 && !readJSON(w, r, &opts) {
		return
	}
	p, _ := principal(r.Context())
	opts.Actor = p.User.Username
	id, err := s.runner.Start(r.Context(), opts)
	switch {
	case errors.Is(err, ErrBusy):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNoPaths):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.audit(r, "run.start", strconv.FormatInt(id, 10), "ok", opts.Kind)
		writeJSON(w, http.StatusAccepted, map[string]int64{"id": id})
	}
}

func (s *Server) apiEstimate(w http.ResponseWriter, r *http.Request) {
	opts := RunOptions{Kind: r.URL.Query().Get("kind"), UDP: r.URL.Query().Get("udp") == "1",
		Bidir: r.URL.Query().Get("bidir") == "1"}
	paths, secs, err := s.runner.Estimate(r.Context(), opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"paths": paths, "seconds": secs})
}

func (s *Server) apiActiveRun(w http.ResponseWriter, _ *http.Request) {
	p, ok := s.runner.Active()
	if !ok {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) writeRun(w http.ResponseWriter, run store.Run) {
	if run.Status == "running" {
		writeJSON(w, http.StatusOK, map[string]any{"id": run.ID, "kind": run.Kind, "status": run.Status, "started": run.Started})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(run.Report)
}

func (s *Server) apiLastRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.LastRun(r.Context(), r.URL.Query().Get("kind"))
	if err != nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	s.writeRun(w, run)
}

func (s *Server) apiRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	run, err := s.store.RunByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such run")
		return
	}
	s.writeRun(w, run)
}

func (s *Server) apiCancelRun(w http.ResponseWriter, r *http.Request) {
	ok := s.runner.Cancel()
	s.audit(r, "run.cancel", "", strconv.FormatBool(ok), "")
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": ok})
}

func (s *Server) apiSchedules(w http.ResponseWriter, r *http.Request) {
	sc, err := s.store.Schedules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) apiSaveSchedule(w http.ResponseWriter, r *http.Request) {
	var sc store.Schedule
	if !readJSON(w, r, &sc) {
		return
	}
	if r.Method == http.MethodPut {
		id, ok := pathID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad id")
			return
		}
		sc.ID = id
	}
	if _, err := ParseSpec(sc.Spec, time.Time{}, time.Now()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if sc.Kind != KindFull && sc.Kind != KindReachability && sc.Kind != KindAggregate {
		writeError(w, http.StatusBadRequest, "kind must be full, reachability or aggregate")
		return
	}
	id, err := s.store.SaveSchedule(r.Context(), sc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sc.ID = id
	s.audit(r, "schedule.save", sc.Name, "ok", sc.Spec)
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) apiDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.store.DeleteSchedule(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "schedule.delete", strconv.FormatInt(id, 10), "ok", "")
	w.WriteHeader(http.StatusNoContent)
}

// --- settings and audit ---

func (s *Server) apiSettings(w http.ResponseWriter, r *http.Request) {
	st := s.settings(r.Context())
	p, _ := principal(r.Context())
	if st.MiniToken != "" {
		st.MiniToken = mask(st.MiniToken)
	}
	if p.Role != RoleAdmin {
		st.Webhooks = nil
	}
	writeJSON(w, http.StatusOK, st)
}

func mask(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + strings.Repeat("*", len(s)-4) + s[len(s)-2:]
}

func (s *Server) apiSaveSettings(w http.ResponseWriter, r *http.Request) {
	cur := s.settings(r.Context())
	st := cur
	if !readJSON(w, r, &st) {
		return
	}
	if strings.Contains(st.MiniToken, "*") {
		st.MiniToken = cur.MiniToken // masked value sent back unchanged
	}
	if st.MiniToken != "" && len(st.MiniToken) < 8 {
		writeError(w, http.StatusBadRequest, "mini token must have at least 8 characters")
		return
	}
	st.DurationMS = min(max(st.DurationMS, 1000), 30000)
	st.Streams = min(max(st.Streams, 1), 16)
	st.PingCount = min(max(st.PingCount, 1), 100)
	if err := s.store.SetSetting(r.Context(), "settings", st); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "settings.update", "", "ok", "")
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) apiAudit(w http.ResponseWriter, r *http.Request) {
	es, err := s.store.AuditLog(r.Context(), 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, es)
}
