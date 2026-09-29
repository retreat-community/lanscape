// Package agent implements lanscape-agent: registration, the mTLS control channel,
// inventory reporting and test execution.
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/retreat-community/lanscape/internal/netio"
	"github.com/retreat-community/lanscape/internal/pki"
	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/testengine"
)

// Config configures the agent.
type Config struct {
	Server            string // host:8443
	Token             string // registration token (first start only)
	Name              string
	DataDir           string
	CAFingerprint     string // optional SHA-256 pin of the server CA
	Exclude           []string
	DataAddr          string // ":47700"
	Limits            testengine.Limits
	InventoryInterval time.Duration
	Mode              string // "", "checks-only", "respond-only"
	Version           string
}

// Agent is a running lanscape-agent.
type Agent struct {
	cfg  Config
	log  *slog.Logger
	resp *testengine.Responder

	mu      sync.Mutex
	conn    *websocket.Conn
	agentID string
	extra   map[string]func(ctx context.Context, env proto.Envelope) (any, error)
	lastInv []byte

	pmu     sync.Mutex
	seq     uint64
	pending map[string]chan proto.Envelope
	status  Status
}

// Status is the local view of the agent (LuCI, "lanscape-agent status").
type Status struct {
	Connected bool   `json:"connected"`
	Server    string `json:"server"`
	AgentID   string `json:"agent_id,omitempty"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Since     int64  `json:"since,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// Status returns the connection state.
func (a *Agent) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.status
	st.Server, st.Name, st.Version, st.AgentID = a.cfg.Server, a.cfg.Name, a.cfg.Version, a.agentID
	return st
}

// Request sends a message to the server and waits for the reply with the same id.
func (a *Agent) Request(ctx context.Context, typ string, data any) (json.RawMessage, error) {
	a.pmu.Lock()
	a.seq++
	id := fmt.Sprintf("a%d", a.seq)
	ch := make(chan proto.Envelope, 1)
	if a.pending == nil {
		a.pending = map[string]chan proto.Envelope{}
	}
	a.pending[id] = ch
	a.pmu.Unlock()
	defer func() {
		a.pmu.Lock()
		delete(a.pending, id)
		a.pmu.Unlock()
	}()
	env, err := proto.NewEnvelope(typ, id, data)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	c := a.conn
	a.mu.Unlock()
	if c == nil {
		return nil, errors.New("agent: not connected")
	}
	if err := a.write(ctx, c, env); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.Type == proto.MsgError {
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(r.Data, &e)
			return nil, errors.New(e.Error)
		}
		return r.Data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// deliver hands a reply to a pending Request.
func (a *Agent) deliver(env proto.Envelope) bool {
	if env.ID == "" || !strings.HasPrefix(env.ID, "a") {
		return false
	}
	a.pmu.Lock()
	ch := a.pending[env.ID]
	a.pmu.Unlock()
	if ch == nil {
		return false
	}
	ch <- env
	return true
}

// New creates an agent.
func New(cfg Config, log *slog.Logger) *Agent {
	if cfg.DataAddr == "" {
		cfg.DataAddr = fmt.Sprintf(":%d", proto.DataPort)
	}
	if cfg.InventoryInterval == 0 {
		cfg.InventoryInterval = 5 * time.Minute
	}
	if cfg.Limits.MaxDuration == 0 {
		cfg.Limits = testengine.DefaultLimits
	}
	if cfg.Name == "" {
		cfg.Name, _ = os.Hostname()
	}
	r := testengine.NewResponder(log)
	r.Limits = cfg.Limits
	return &Agent{cfg: cfg, log: log, resp: r, extra: map[string]func(context.Context, proto.Envelope) (any, error){}}
}

// Handle registers an extra control message handler (discovery, checks, actions).
func (a *Agent) Handle(typ string, fn func(ctx context.Context, env proto.Envelope) (any, error)) {
	a.extra[typ] = fn
}

// Send pushes an unsolicited message (e.g. discovery results) to the server.
func (a *Agent) Send(ctx context.Context, typ string, data any) error {
	env, err := proto.NewEnvelope(typ, "", data)
	if err != nil {
		return err
	}
	a.mu.Lock()
	c := a.conn
	a.mu.Unlock()
	if c == nil {
		return errors.New("agent: not connected")
	}
	return a.write(ctx, c, env)
}

func (a *Agent) write(ctx context.Context, c *websocket.Conn, env proto.Envelope) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(wctx, c, env)
}

func (a *Agent) path(name string) string { return filepath.Join(a.cfg.DataDir, name) }

// Run serves the data plane and keeps the control channel connected until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	if err := os.MkdirAll(a.cfg.DataDir, 0o700); err != nil {
		return err
	}
	if a.cfg.Mode != "checks-only" {
		go func() {
			if err := a.resp.Serve(ctx, a.cfg.DataAddr); err != nil {
				a.log.Error("data plane stopped", "err", err)
			}
		}()
	}
	backoff := time.Second
	for ctx.Err() == nil {
		err := a.session(ctx)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // shutdown, not a failure
		}
		a.log.Warn("control channel down", "err", err, "retry_in", backoff.String())
		if err != nil {
			a.mu.Lock()
			a.status.LastError = err.Error()
			a.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
		if err == nil {
			backoff = time.Second
		}
	}
	return nil
}

// Register obtains a certificate with the registration token (first start).
func (a *Agent) Register(ctx context.Context) error {
	if _, err := os.Stat(a.path("agent.crt")); err == nil {
		return nil
	}
	if a.cfg.Token == "" {
		return errors.New("agent: not registered and no registration token given")
	}
	keyPEM, csrPEM, err := pki.NewKeyAndCSR(a.cfg.Name)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	body, _ := json.Marshal(proto.RegisterRequest{Token: a.cfg.Token, Name: a.cfg.Name, Hostname: host, CSR: string(csrPEM)})
	// The server CA is not known yet: accept the presented chain but pin its root when a
	// fingerprint is configured, and remember it for all later connections.
	var presented []*x509.Certificate
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, //nolint:gosec // verified below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			for _, r := range raw {
				c, err := x509.ParseCertificate(r)
				if err != nil {
					return err
				}
				presented = append(presented, c)
			}
			if a.cfg.CAFingerprint == "" {
				return nil
			}
			for _, c := range presented {
				if c.IsCA && strings.EqualFold(pki.Fingerprint(c), strings.ReplaceAll(a.cfg.CAFingerprint, ":", "")) {
					return nil
				}
			}
			return errors.New("agent: server CA fingerprint mismatch")
		}}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+a.cfg.Server+"/v1/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("agent: register: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent: register: %s: %s", resp.Status, strings.TrimSpace(string(rb)))
	}
	var rr proto.RegisterResponse
	if err := json.Unmarshal(rb, &rr); err != nil {
		return err
	}
	ca, err := pki.ParseCert([]byte(rr.CA))
	if err != nil {
		return err
	}
	// the CA returned in the body must be the one that signed the TLS chain
	chainOK := false
	for _, c := range presented {
		if c.CheckSignatureFrom(ca) == nil || bytes.Equal(c.Raw, ca.Raw) {
			chainOK = true
		}
	}
	if !chainOK {
		return errors.New("agent: server certificate is not signed by the returned CA")
	}
	for name, data := range map[string][]byte{"agent.key": keyPEM, "agent.crt": []byte(rr.Cert), "ca.crt": []byte(rr.CA),
		"agent_id": []byte(rr.AgentID)} {
		if err := os.WriteFile(a.path(name), data, 0o600); err != nil {
			return err
		}
	}
	sum := sha256.Sum256(ca.Raw)
	a.log.Info("registered", "agent_id", rr.AgentID, "ca_fingerprint", hex.EncodeToString(sum[:]))
	return nil
}

func (a *Agent) tlsConfig() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(a.path("agent.crt"), a.path("agent.key"))
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(a.path("ca.crt"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	host, _, _ := net.SplitHostPort(a.cfg.Server)
	return &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, ServerName: host, MinVersion: tls.VersionTLS12}, nil
}

// renew replaces the certificate when it expires within 30 days.
func (a *Agent) renew(ctx context.Context, client *http.Client) {
	certPEM, err := os.ReadFile(a.path("agent.crt"))
	if err != nil || !pki.NeedsRenewal(certPEM) {
		return
	}
	keyPEM, csrPEM, err := pki.NewKeyAndCSR(a.cfg.Name)
	if err != nil {
		return
	}
	body, _ := json.Marshal(proto.RenewRequest{CSR: string(csrPEM)})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+a.cfg.Server+"/v1/renew", bytes.NewReader(body))
	resp, err := client.Do(req)
	if err != nil {
		a.log.Warn("certificate renewal failed", "err", err)
		return
	}
	defer resp.Body.Close()
	var rr proto.RegisterResponse
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&rr) != nil {
		a.log.Warn("certificate renewal rejected", "status", resp.Status)
		return
	}
	_ = os.WriteFile(a.path("agent.key"), keyPEM, 0o600)
	_ = os.WriteFile(a.path("agent.crt"), []byte(rr.Cert), 0o600)
	a.log.Info("certificate renewed")
}

func (a *Agent) session(ctx context.Context) error {
	if err := a.Register(ctx); err != nil {
		return err
	}
	tc, err := a.tlsConfig()
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: tc}}
	a.renew(ctx, client)
	if tc, err = a.tlsConfig(); err != nil {
		return err
	}
	client = &http.Client{Transport: &http.Transport{TLSClientConfig: tc}}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	c, resp, err := websocket.Dial(dctx, "wss://"+a.cfg.Server+"/v1/agent", &websocket.DialOptions{HTTPClient: client})
	cancel()
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return err
	}
	c.SetReadLimit(8 << 20)
	defer c.CloseNow()
	id, _ := os.ReadFile(a.path("agent_id"))
	a.agentID = string(id)
	host, _ := os.Hostname()
	caps := []string{"icmp", "tcp", "udp", "bidir"}
	for typ := range a.extra {
		caps = append(caps, typ)
	}
	sort.Strings(caps[4:])
	hello, _ := proto.NewEnvelope(proto.MsgHello, "", proto.HelloMsg{Proto: proto.ControlVersion, AgentID: a.agentID,
		Name: a.cfg.Name, Version: a.cfg.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Hostname: host,
		HostID: hostID(), Caps: caps, DataPort: dataPort(a.cfg.DataAddr)})
	if err := wsjson.Write(ctx, c, hello); err != nil {
		return err
	}
	var welcome proto.Envelope
	if err := wsjson.Read(ctx, c, &welcome); err != nil || welcome.Type != proto.MsgWelcome {
		return fmt.Errorf("agent: no welcome: %w", err)
	}
	a.mu.Lock()
	a.conn = c
	a.lastInv = nil
	a.status = Status{Connected: true, Since: time.Now().UnixMilli()}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.conn = nil
		a.status.Connected = false
		a.mu.Unlock()
	}()
	a.log.Info("connected", "server", a.cfg.Server, "agent_id", a.agentID)
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	go a.inventoryLoop(sctx, c)
	for {
		var env proto.Envelope
		if err := wsjson.Read(sctx, c, &env); err != nil {
			return err
		}
		if a.deliver(env) {
			continue
		}
		go a.dispatch(sctx, c, env)
	}
}

func (a *Agent) inventoryLoop(ctx context.Context, c *websocket.Conn) {
	changes := watchChanges(ctx)
	tick := time.NewTicker(a.cfg.InventoryInterval)
	defer tick.Stop()
	force := true
	for {
		inv := a.Inventory()
		b, _ := json.Marshal(inv)
		a.mu.Lock()
		same := bytes.Equal(b, a.lastInv)
		a.mu.Unlock()
		if force || !same {
			env := proto.Envelope{Type: proto.MsgInventory, Data: b}
			if err := a.write(ctx, c, env); err != nil {
				return
			}
			a.mu.Lock()
			a.lastInv = b
			a.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			force = true
		case <-changes:
			force = false
			time.Sleep(2 * time.Second) // coalesce bursts of netlink events
		}
	}
}

// Inventory collects the current inventory.
func (a *Agent) Inventory() Inventory {
	ifs, err := netio.Interfaces()
	if err != nil {
		a.log.Warn("interface inventory failed", "err", err)
	}
	ifs = netio.Filter(ifs, a.cfg.Exclude)
	inv := Inventory{Ifaces: ifs}
	inv.Routes, inv.Rules, inv.Neighbors, inv.Env, inv.Resources = CollectSystem()
	inv.Resources.UptimeS = inv.Resources.UptimeS / 60 * 60 // avoid resending every minute
	inv.Resources.MemAvail = inv.Resources.MemAvail >> 24 << 24
	inv.Resources.Load1 = float64(int(inv.Resources.Load1))
	inv.Resources.AgentMemKB = inv.Resources.AgentMemKB >> 12 << 12
	return inv
}

func (a *Agent) dispatch(ctx context.Context, c *websocket.Conn, env proto.Envelope) {
	var reply any
	var rtype string
	switch env.Type {
	case proto.MsgPing:
		rtype = proto.MsgPong
	case proto.MsgGrant:
		var g proto.GrantMsg
		if json.Unmarshal(env.Data, &g) != nil || len(g.Token) != proto.TokenLen {
			return
		}
		var tok testengine.Token
		copy(tok[:], g.Token)
		a.resp.Grant(g.RunID, tok, time.Duration(g.TTLS)*time.Second)
		rtype, reply = proto.MsgGrantAck, map[string]uint32{"run_id": g.RunID}
	case proto.MsgTest:
		var t proto.TestMsg
		if err := json.Unmarshal(env.Data, &t); err != nil {
			return
		}
		rtype, reply = proto.MsgTestResult, a.runTest(ctx, t)
	case proto.MsgConfig:
		return
	default:
		fn, ok := a.extra[env.Type]
		if !ok {
			return
		}
		res, err := fn(ctx, env)
		if err != nil {
			rtype, reply = proto.MsgError, map[string]string{"error": err.Error()}
		} else if res != nil {
			rtype, reply = resultType(env.Type), res
		} else {
			return
		}
	}
	out, err := proto.NewEnvelope(rtype, env.ID, reply)
	if err != nil {
		return
	}
	if err := a.write(ctx, c, out); err != nil {
		a.log.Debug("reply failed", "type", rtype, "err", err)
	}
}

func resultType(t string) string {
	switch t {
	case proto.MsgCheck:
		return proto.MsgCheckResult
	case proto.MsgAction:
		return proto.MsgActionResult
	default:
		return t + "_result"
	}
}

// TestResult is the reply to a test request.
type TestResult struct {
	Ping *testengine.PingResult `json:"ping,omitempty"`
	LSTP *testengine.Result     `json:"lstp,omitempty"`
}

func (a *Agent) runTest(ctx context.Context, t proto.TestMsg) TestResult {
	if a.cfg.Mode == "respond-only" {
		return TestResult{LSTP: &testengine.Result{Status: testengine.StatusUnsupported, Message: "agent is in respond-only mode"}}
	}
	src, dst := net.ParseIP(t.Src), net.ParseIP(t.Dst)
	if t.Dev != "" && netio.GlobMatch(strings.Join(a.cfg.Exclude, ","), t.Dev) {
		return TestResult{LSTP: &testengine.Result{Status: testengine.StatusLimit, Message: "interface is excluded"}}
	}
	switch t.Kind {
	case "ping", "pmtu":
		p := testengine.PingParams{Dev: t.Dev, Src: src, Dst: dst, Count: t.Count,
			Interval: time.Duration(t.IntervalMS) * time.Millisecond, Size: t.Size, DF: t.Kind == "pmtu"}
		if rd, ok := testengine.CheckRoute(t.Dev, src, dst); !ok {
			return TestResult{Ping: &testengine.PingResult{Status: testengine.StatusRouteMismatch, Message: "route via " + rd}}
		}
		r := testengine.Ping(ctx, p)
		return TestResult{Ping: &r}
	case "echo", "tcp", "udp":
		var tok testengine.Token
		copy(tok[:], t.Token)
		kind := map[string]uint8{"echo": proto.KindEcho, "tcp": proto.KindTCP, "udp": proto.KindUDP}[t.Kind]
		dur := time.Duration(t.DurationMS) * time.Millisecond
		if dur > a.cfg.Limits.MaxDuration || t.Streams > a.cfg.Limits.MaxStreams {
			return TestResult{LSTP: &testengine.Result{Status: testengine.StatusLimit}}
		}
		r := testengine.Run(ctx, testengine.Params{AgentID: a.agentID, Dev: t.Dev, Src: src, Dst: dst, Port: t.Port,
			RunID: t.RunID, Token: tok, Kind: kind, Dir: t.Dir, Streams: t.Streams, Duration: dur,
			UDPRateKbps: t.UDPRateKbps, PktSize: t.PktSize, EchoCount: t.Count})
		return TestResult{LSTP: &r}
	}
	return TestResult{LSTP: &testengine.Result{Status: testengine.StatusUnsupported}}
}

func dataPort(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return proto.DataPort
	}
	var n int
	_, _ = fmt.Sscanf(p, "%d", &n)
	if n == 0 {
		return proto.DataPort
	}
	return n
}

func hostID() string {
	for _, f := range []string{"/etc/machine-id", "/proc/sys/kernel/random/boot_id"} {
		if b, err := os.ReadFile(f); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}
