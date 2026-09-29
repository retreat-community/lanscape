// Package server implements the Lanscape server: HTTP API and UI, the agent gateway
// (mTLS WebSocket), the Mini-compatible agent port, the run scheduler and metrics.
package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/notify"
	"github.com/retreat-community/lanscape/internal/pki"
	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/store"
	"github.com/retreat-community/lanscape/internal/topo"
)

// Config is the static server configuration (flags / environment).
type Config struct {
	Listen        string // UI/API, ":8080"
	TLSCert       string // optional TLS for the UI
	TLSKey        string
	GatewayListen string         // agent gateway, ":8443"
	MiniListen    string         // Mini-compatible agent port, ":47701"; empty disables
	MiniToken     string         // shared token for Mini agents (settings override)
	GatewayHosts  []string       // names/IPs in the gateway certificate
	DataDir       string         // data/ (database, pki)
	DB            string         // SQLite path or postgres:// URL
	Expect        map[string]int // expected speed per CIDR/segment (static)
	Parallel      int            // parallel reachability tests
	SessionTTL    time.Duration
	SecureCookies bool
	MetricsToken  string // bearer token for /metrics; empty = public
	AdminUser     string // bootstrap admin when there are no users
	AdminPassword string
	PublicURL     string // links in notifications (settings override)
	SecretKey     string // base64 key for secrets at rest; default <data-dir>/secret.key
	OIDC          OIDCConfig
	Version       string
}

// Settings are editable at runtime and stored in the database.
type Settings struct {
	DurationMS    int      `json:"duration_ms"`
	Streams       int      `json:"streams"`
	PingCount     int      `json:"ping_count"`
	RTTWarnMS     int      `json:"rtt_warn_ms"`
	MiniToken     string   `json:"mini_token,omitempty"`
	Webhooks      []string `json:"webhooks"`
	RetentionDays int      `json:"retention_days"`
	PublicURL     string   `json:"public_url,omitempty"`
	// GuestDashboard shows a reduced, read-only dashboard to visitors who are not signed in (§4)
	GuestDashboard bool `json:"guest_dashboard"`
	// aggregates (daily uptime, Internet tests) are kept longer than raw checks
	AggregateDays int `json:"aggregate_days"`
	// Internet test (§6.1): observation points ("server" or agent ids), period of the address
	// check and of the speed test (0 = off), and the endpoints used
	InternetPoints      []string `json:"internet_points"`
	InternetEveryMin    int      `json:"internet_every_min"`
	InternetSpeedEveryH int      `json:"internet_speed_every_h"`
	InternetIPURL       string   `json:"internet_ip_url,omitempty"`
	InternetDownloadURL string   `json:"internet_download_url,omitempty"`
	// newer container image tags are looked up every ImageUpdatesEveryH hours (0 = off)
	ImageUpdatesEveryH int `json:"image_updates_every_h"`
	// agent updates from the panel: channel "" (off), "stable" or "beta"; Auto installs new
	// releases by itself; the release list can come from a mirror
	AgentUpdateChannel string `json:"agent_update_channel"`
	AgentUpdateAuto    bool   `json:"agent_update_auto"`
	AgentReleasesURL   string `json:"agent_releases_url,omitempty"`
}

// DefaultSettings are used until an administrator changes them.
var DefaultSettings = Settings{DurationMS: 5000, Streams: 4, PingCount: 20, RTTWarnMS: 20, RetentionDays: 30, AggregateDays: 730}

// Server is the Lanscape control plane.
type Server struct {
	cfg     Config
	log     *slog.Logger
	store   *store.Store
	ca      *pki.CA
	hub     *Hub
	events  *Broker
	metrics *Metrics
	runner  *Runner
	limiter *loginLimiter
	ctx     context.Context
	hooks   []func(ctx context.Context, agentID string, env proto.Envelope)
	mu      sync.Mutex
	extras  []topo.Extra

	extraRoutes []route

	disc   discoveryState
	uptime *uptime

	pushClient *http.Client // Web Push delivery (tests swap it)
	oidc       oidcState
	inet       internetState
	restarts   restartCounts
	traffic    trafficState
	images     imageState
	rel        releaseCache
}

// New opens the store and the CA.
func New(ctx context.Context, cfg Config, log *slog.Logger) (*Server, error) {
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if cfg.DB == "" {
		cfg.DB = filepath.Join(cfg.DataDir, "lanscape.db")
	}
	if cfg.Parallel <= 0 {
		cfg.Parallel = 8
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 7 * 24 * time.Hour
	}
	if err := mkdir(cfg.DataDir); err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, cfg.DB)
	if err != nil {
		return nil, err
	}
	key, err := secretKey(cfg)
	if err != nil {
		return nil, err
	}
	if st.Secrets, err = store.NewSecrets(key, notify.SecretFields()); err != nil {
		return nil, err
	}
	ca, err := pki.LoadOrCreate(filepath.Join(cfg.DataDir, "pki"))
	if err != nil {
		return nil, fmt.Errorf("server: pki: %w", err)
	}
	s := &Server{cfg: cfg, log: log, store: st, ca: ca, hub: NewHub(), events: NewBroker(), metrics: NewMetrics(),
		limiter: &loginLimiter{fail: map[string][]time.Time{}}, ctx: ctx}
	s.runner = &Runner{s: s}
	s.uptime = newUptime(s)
	s.OnAgentMessage(s.onDiscovery)
	s.OnAgentMessage(s.onTraffic)
	s.hub.onConn = func(a *AgentState, up bool) {
		s.metrics.SetAgent(a.ID, a.Name, a.Kind, up)
		if up && a.Kind != "lite" {
			go s.pushSignatures(s.ctx, a.ID)
		}
		s.events.Publish("agent", map[string]any{"id": a.ID, "name": a.Name, "online": up})
	}
	s.hub.onInv = func(a *AgentState) {
		s.events.Publish("inventory", map[string]any{"id": a.ID})
	}
	if err := s.loadAgents(ctx); err != nil {
		return nil, err
	}
	if err := s.bootstrapAdmin(ctx); err != nil {
		return nil, err
	}
	if err := s.sealStoredSecrets(ctx); err != nil {
		return nil, fmt.Errorf("server: encrypt stored secrets: %w", err)
	}
	return s, nil
}

// Store exposes the database (for commands such as backup).
func (s *Server) Store() *store.Store { return s.store }

// Hub exposes the agent registry.
func (s *Server) Hub() *Hub { return s.hub }

// OnAgentMessage registers a handler for unsolicited agent messages (discovery etc.).
func (s *Server) OnAgentMessage(fn func(ctx context.Context, agentID string, env proto.Envelope)) {
	s.hooks = append(s.hooks, fn)
}

func (s *Server) onAgentMessage(ctx context.Context, id string, env proto.Envelope) {
	for _, h := range s.hooks {
		h(ctx, id, env)
	}
}

// SetExtras replaces the addresses learned by discovery (used by IPAM and anomalies).
func (s *Server) SetExtras(e []topo.Extra) {
	s.mu.Lock()
	s.extras = e
	s.mu.Unlock()
}

func (s *Server) discoveredExtras() []topo.Extra {
	s.mu.Lock()
	out := append([]topo.Extra(nil), s.extras...)
	s.mu.Unlock()
	return append(out, s.deviceExtras(s.ctx)...)
}

// SpeedInheritor returns the uplink speed for interfaces without a reported speed
// (virtio guests behind a hypervisor bridge, §7.4). Modules can replace it.
var SpeedInheritor func(s *Server, m topo.Member) int

func (s *Server) inheritSpeed(m topo.Member) int {
	if SpeedInheritor != nil {
		return SpeedInheritor(s, m)
	}
	return proxmoxSpeed(s, m)
}

func (s *Server) settings(ctx context.Context) Settings {
	st := DefaultSettings
	if err := s.store.GetSetting(ctx, "settings", &st); err != nil {
		s.log.Warn("cannot read settings", "err", err)
	}
	if st.DurationMS == 0 {
		st.DurationMS = DefaultSettings.DurationMS
	}
	if st.Streams == 0 {
		st.Streams = DefaultSettings.Streams
	}
	if st.PingCount == 0 {
		st.PingCount = DefaultSettings.PingCount
	}
	return st
}

func (s *Server) loadAgents(ctx context.Context) error {
	agents, err := s.store.Agents(ctx)
	if err != nil {
		return err
	}
	states := make([]AgentState, 0, len(agents))
	for _, a := range agents {
		var inv agent.Inventory
		_ = json.Unmarshal(a.Inventory, &inv)
		states = append(states, AgentState{ID: a.ID, Name: a.Name, Kind: a.Kind, Version: a.Version, OS: a.OS,
			Arch: a.Arch, Hostname: a.Hostname, HostID: a.HostID, LastSeen: a.LastSeen, Inv: inv, DataPort: proto.DataPort})
		s.metrics.SetAgent(a.ID, a.Name, a.Kind, false)
	}
	s.hub.Load(states)
	return nil
}

func (s *Server) persistLite(ctx context.Context, st AgentState) {
	inv, _ := json.Marshal(st.Inv)
	a := store.Agent{ID: st.ID, Name: st.Name, Kind: "lite", Hostname: st.Hostname, HostID: st.HostID,
		Version: st.Version, Arch: st.Arch, OS: st.OS, Inventory: inv, LastSeen: time.Now().UnixMilli()}
	if old, err := s.store.AgentByID(ctx, st.ID); err == nil {
		a.FirstSeen, a.Labels, a.Name = old.FirstSeen, old.Labels, old.Name
	}
	if err := s.store.UpsertAgent(ctx, a); err != nil {
		s.log.Warn("cannot store lite agent", "id", st.ID, "err", err)
	}
}

func (s *Server) bootstrapAdmin(ctx context.Context) error {
	if s.cfg.AdminPassword == "" {
		return nil
	}
	n, err := s.store.CountUsers(ctx)
	if err != nil || n > 0 {
		return err
	}
	user := s.cfg.AdminUser
	if user == "" {
		user = "admin"
	}
	h, err := HashPassword(s.cfg.AdminPassword)
	if err != nil {
		return err
	}
	_, err = s.store.CreateUser(ctx, store.User{Username: user, PasswordHash: h, Role: RoleAdmin})
	if err == nil {
		s.log.Info("bootstrap administrator created", "user", user)
	}
	return err
}

// notifyRun posts a run summary to the configured webhooks.
func (s *Server) notifyRun(ctx context.Context, rep *Report) {
	s.emit(ctx, "run.finished", "run", map[string]any{"id": rep.ID, "kind": rep.Kind, "status": rep.Status,
		"started": rep.Started, "finished": rep.Finished, "paths": len(rep.Paths), "problems": rep.Problems})
}

// emit posts {"event": event, key: data} to the webhooks in Settings → General: run.finished,
// incident.opened/resolved/reminder and device.new.
func (s *Server) emit(ctx context.Context, event, key string, data any) {
	st := s.settings(ctx)
	if len(st.Webhooks) == 0 {
		return
	}
	body, _ := json.Marshal(map[string]any{"event": event, key: data})
	for _, u := range st.Webhooks {
		go s.postWebhook(u, body)
	}
}

func (s *Server) postWebhook(url string, body []byte) {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "lanscape/"+s.cfg.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.log.Warn("webhook failed", "url", url, "err", err)
		return
	}
	resp.Body.Close()
}

// Run starts all listeners and background loops until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	s.ctx = ctx
	errc := make(chan error, 4)
	ui := &http.Server{Addr: s.cfg.Listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	gw := &http.Server{Addr: s.cfg.GatewayListen, Handler: s.gatewayMux(), TLSConfig: s.gatewayTLS(),
		ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	if _, err := s.ca.ServerCert(s.cfg.GatewayHosts); err != nil {
		return err
	}
	go func() {
		var err error
		if s.cfg.TLSCert != "" {
			err = ui.ListenAndServeTLS(s.cfg.TLSCert, s.cfg.TLSKey)
		} else {
			err = ui.ListenAndServe()
		}
		if !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("ui listener: %w", err)
		}
	}()
	go func() {
		ln, err := net.Listen("tcp", s.cfg.GatewayListen)
		if err != nil {
			errc <- fmt.Errorf("gateway listener: %w", err)
			return
		}
		if err := gw.Serve(tls.NewListener(ln, gw.TLSConfig)); !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("gateway: %w", err)
		}
	}()
	if s.cfg.MiniListen != "" {
		go func() {
			if err := s.serveMini(ctx, s.cfg.MiniListen); err != nil {
				errc <- err
			}
		}()
	}
	go s.scheduler(ctx)
	go s.uptime.run(ctx)
	go s.housekeeping(ctx)
	go s.internetScheduler(ctx)
	go s.imageScheduler(ctx)
	go s.agentUpdateScheduler(ctx)
	go s.signatureScheduler(ctx)
	s.log.Info("lanscape server started", "version", s.cfg.Version, "ui", s.cfg.Listen, "gateway", s.cfg.GatewayListen,
		"mini", s.cfg.MiniListen, "ca_fingerprint", pki.Fingerprint(s.ca.Cert))
	var err error
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ui.Shutdown(sctx)
	_ = gw.Shutdown(sctx)
	s.runner.Cancel()
	return err
}

func (s *Server) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.store.PurgeSessions(ctx)
			_ = s.store.PruneFindings(ctx, time.Now().Add(-7*24*time.Hour).UnixMilli())
			st := s.settings(ctx)
			if st.RetentionDays > 0 {
				cut := time.Now().Add(-time.Duration(st.RetentionDays) * 24 * time.Hour).UnixMilli()
				if n, err := s.store.PruneRuns(ctx, cut); err == nil && n > 0 {
					s.log.Info("pruned old runs", "count", n)
				}
				if err := s.store.PruneChecks(ctx, cut); err != nil {
					s.log.Warn("cannot prune checks", "err", err)
				}
			}
			if st.AggregateDays > 0 {
				before := time.Now().AddDate(0, 0, -st.AggregateDays)
				if err := s.store.PruneDaily(ctx, dayKey(before)); err != nil {
					s.log.Warn("cannot prune daily aggregates", "err", err)
				}
				_ = s.store.PruneInternetChecks(ctx, before.UnixMilli())
			}
		}
	}
}
