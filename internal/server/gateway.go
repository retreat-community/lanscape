package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/pki"
	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/store"
)

// hashToken returns the storage hash of a high-entropy secret.
func hashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

// NewSecret returns a random URL-safe secret with a prefix.
func NewSecret(prefix string) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) gatewayTLS() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  s.ca.Pool(),
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return s.ca.ServerCert(s.cfg.GatewayHosts)
		},
	}
}

func (s *Server) gatewayMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/register", s.handleRegister)
	mux.HandleFunc("POST /v1/renew", s.handleRenew)
	mux.HandleFunc("GET /v1/agent", s.handleAgentWS)
	mux.HandleFunc("GET /v1/ca", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write(s.ca.CertPEM)
	})
	return mux
}

func clientAgentID(r *http.Request) (string, bool) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		return "", false
	}
	return pki.AgentID(r.TLS.VerifiedChains[0][0])
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req proto.RegisterRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	tok, err := s.store.UseAgentToken(r.Context(), hashToken(req.Token))
	if err != nil {
		s.log.Warn("agent registration rejected", "remote", r.RemoteAddr, "name", req.Name)
		_ = s.store.Audit(r.Context(), store.AuditEntry{Actor: "agent:" + req.Name, Action: "agent.register",
			Result: "denied", Detail: r.RemoteAddr})
		http.Error(w, "invalid or expired registration token", http.StatusForbidden)
		return
	}
	idb := make([]byte, 8)
	_, _ = rand.Read(idb)
	id := hex.EncodeToString(idb)
	certPEM, cert, err := s.ca.SignAgent([]byte(req.CSR), id)
	if err != nil {
		http.Error(w, "invalid CSR", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = req.Hostname
	}
	a := store.Agent{ID: id, Name: name, Kind: "full", Hostname: req.Hostname, Labels: tok.Labels,
		CertFingerprint: pki.Fingerprint(cert), CertNotAfter: cert.NotAfter.UnixMilli()}
	if err := s.store.UpsertAgent(r.Context(), a); err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	_ = s.store.Audit(r.Context(), store.AuditEntry{Actor: "agent:" + name, Action: "agent.register", Target: id,
		Result: "ok", Detail: "token " + tok.Name})
	s.log.Info("agent registered", "id", id, "name", name)
	writeJSON(w, http.StatusOK, proto.RegisterResponse{AgentID: id, Cert: string(certPEM), CA: string(s.ca.CertPEM)})
}

func (s *Server) handleRenew(w http.ResponseWriter, r *http.Request) {
	id, ok := clientAgentID(r)
	if !ok {
		http.Error(w, "client certificate required", http.StatusUnauthorized)
		return
	}
	a, err := s.store.AgentByID(r.Context(), id)
	if err != nil {
		http.Error(w, "unknown agent", http.StatusForbidden)
		return
	}
	var req proto.RenewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	certPEM, cert, err := s.ca.SignAgent([]byte(req.CSR), id)
	if err != nil {
		http.Error(w, "invalid CSR", http.StatusBadRequest)
		return
	}
	a.CertFingerprint, a.CertNotAfter = pki.Fingerprint(cert), cert.NotAfter.UnixMilli()
	a.LastSeen = time.Now().UnixMilli()
	_ = s.store.UpsertAgent(r.Context(), a)
	writeJSON(w, http.StatusOK, proto.RegisterResponse{AgentID: id, Cert: string(certPEM), CA: string(s.ca.CertPEM)})
}

func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	id, ok := clientAgentID(r)
	if !ok {
		http.Error(w, "client certificate required", http.StatusUnauthorized)
		return
	}
	rec, err := s.store.AgentByID(r.Context(), id)
	if err != nil {
		// deleted agents lose access even with a valid certificate
		http.Error(w, "unknown agent", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(16 << 20)
	ctx := s.ctx
	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	var hello proto.Envelope
	err = wsjson.Read(hctx, ws, &hello)
	cancel()
	var hm proto.HelloMsg
	if err != nil || hello.Type != proto.MsgHello || json.Unmarshal(hello.Data, &hm) != nil {
		_ = ws.Close(websocket.StatusPolicyViolation, "hello expected")
		return
	}
	if hm.Proto > proto.ControlVersion {
		_ = ws.Close(websocket.StatusPolicyViolation, "upgrade the server: unsupported protocol version")
		return
	}
	cfg, _ := json.Marshal(map[string]any{"inventory_interval_s": 300})
	welcome, _ := proto.NewEnvelope(proto.MsgWelcome, "", proto.WelcomeMsg{Proto: proto.ControlVersion, Config: cfg})
	if err := wsjson.Write(ctx, ws, welcome); err != nil {
		return
	}
	c := newFullConn(id, ws)
	rec.Version, rec.OS, rec.Arch, rec.Hostname, rec.HostID = hm.Version, hm.OS, hm.Arch, hm.Hostname, hm.HostID
	rec.LastSeen = time.Now().UnixMilli()
	_ = s.store.UpsertAgent(ctx, rec)
	var inv agent.Inventory
	_ = json.Unmarshal(rec.Inventory, &inv)
	st := AgentState{ID: id, Name: rec.Name, Kind: "full", Version: hm.Version, OS: hm.OS, Arch: hm.Arch,
		Hostname: hm.Hostname, HostID: hm.HostID, DataPort: hm.DataPort, Caps: hm.Caps, Inv: inv}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		st.Addr = host
	}
	s.hub.Connected(st, c)
	s.log.Info("agent connected", "id", id, "name", rec.Name, "version", hm.Version)
	defer func() {
		s.hub.Disconnected(id, c)
		c.Close()
		s.log.Info("agent disconnected", "id", id, "name", rec.Name)
	}()
	go s.keepalive(ctx, c)
	for {
		var env proto.Envelope
		if err := wsjson.Read(ctx, ws, &env); err != nil {
			var ce websocket.CloseError
			if !errors.As(err, &ce) && ctx.Err() == nil {
				s.log.Debug("agent read", "id", id, "err", err)
			}
			return
		}
		s.hub.Touch(id)
		if c.deliver(env) {
			continue
		}
		switch env.Type {
		case proto.MsgInventory:
			var inv agent.Inventory
			if err := json.Unmarshal(env.Data, &inv); err == nil {
				if prev, ok := s.hub.Get(id); ok {
					s.inventoryChanges(ctx, id, &prev.Inv, &inv)
				}
				s.hub.SetInventory(id, inv)
				rec.Inventory = env.Data
				rec.LastSeen = time.Now().UnixMilli()
				_ = s.store.UpsertAgent(ctx, rec)
			}
		case proto.MsgPong:
		case proto.MsgRunRequest, proto.MsgRunSummary:
			go s.agentRequest(ctx, id, c, env)
		default:
			s.onAgentMessage(ctx, id, env)
		}
	}
}

func (s *Server) keepalive(ctx context.Context, c *fullConn) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		case <-t.C:
			env, _ := proto.NewEnvelope(proto.MsgPing, "", nil)
			if c.send(ctx, env) != nil {
				c.Close()
				return
			}
		}
	}
}
