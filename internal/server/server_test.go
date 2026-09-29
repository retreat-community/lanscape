package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/netio"
	"github.com/retreat-community/lanscape/internal/store/storetest"
	"github.com/retreat-community/lanscape/internal/testengine"
)

// fakeConn answers tests from a table keyed by "src->dst".
type fakeConn struct {
	id    string
	mu    sync.Mutex
	calls []string
	bps   map[string]uint64
	block map[string]string // dst ip -> failing echo status
}

func (f *fakeConn) ID() string                                            { return f.id }
func (f *fakeConn) Lite() bool                                            { return false }
func (f *fakeConn) Close()                                                {}
func (f *fakeConn) Grant(context.Context, uint32, testengine.Token) error { return nil }
func (f *fakeConn) Request(context.Context, string, any) (json.RawMessage, error) {
	return nil, nil
}

func (f *fakeConn) Test(_ context.Context, sp TestSpec) (agent.TestResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, sp.Kind)
	f.mu.Unlock()
	switch sp.Kind {
	case "ping", "pmtu":
		return agent.TestResult{Ping: &testengine.PingResult{Status: "ok", Sent: 3, Recv: 3, RTTAvgUS: 300}}, nil
	case "echo":
		if st := f.block[sp.Dst]; st != "" {
			return agent.TestResult{LSTP: &testengine.Result{Status: st}}, nil
		}
		return agent.TestResult{LSTP: &testengine.Result{Status: "ok", Sent: 10, Recv: 10, RTTAvgUS: 400}}, nil
	default:
		return agent.TestResult{LSTP: &testengine.Result{Status: "ok", BPS: f.bps[sp.Dst], PathOK: true, Verified: true}}, nil
	}
}

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := New(ctx, Config{DataDir: t.TempDir(), DB: storetest.DSN(t), AdminPassword: "correct-horse-battery", Version: "test",
		Expect: map[string]int{"10.31.0.0/24": 100}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func node(ip301, ip0 string) agent.Inventory {
	return agent.Inventory{Ifaces: []netio.Iface{
		{Name: "eth0", Kind: "physical", Up: true, Carrier: true, MTU: 1500, Speed: 1000, Addrs: []netio.Addr{{IP: ip0, Prefix: 24}}},
		{Name: "eth1.301", Kind: "vlan", VLAN: 301, Up: true, Carrier: true, MTU: 1500, Addrs: []netio.Addr{{IP: ip301, Prefix: 24}}},
	}}
}

func client(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 30 * time.Second}
}

func do(t *testing.T, c *http.Client, method, url string, body any, out any) int {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestRunEndToEnd(t *testing.T) {
	s, ts := newTestServer(t)
	a := &fakeConn{id: "a", bps: map[string]uint64{"10.31.0.2": 95e6, "10.10.1.2": 950e6}, block: map[string]string{}}
	b := &fakeConn{id: "b", bps: map[string]uint64{"10.31.0.1": 40e6, "10.10.1.1": 900e6},
		block: map[string]string{"10.10.1.1": "refused"}}
	s.hub.Connected(AgentState{ID: "a", Name: "n1", Kind: "full", Inv: node("10.31.0.1", "10.10.1.1")}, a)
	s.hub.Connected(AgentState{ID: "b", Name: "n2", Kind: "full", Inv: node("10.31.0.2", "10.10.1.2")}, b)

	c := client(t)
	if code := do(t, c, "GET", ts.URL+"/api/v1/runs/last", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated access: %d", code)
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "wrong-password"}, nil); code != 401 {
		t.Fatalf("bad password: %d", code)
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil); code != 200 {
		t.Fatalf("login: %d", code)
	}
	var segs []map[string]any
	do(t, c, "GET", ts.URL+"/api/v1/segments", nil, &segs)
	if len(segs) != 2 {
		t.Fatalf("segments: %+v", segs)
	}
	var est map[string]int
	do(t, c, "GET", ts.URL+"/api/v1/runs/estimate?kind=full", nil, &est)
	if est["paths"] != 4 || est["seconds"] <= 0 {
		t.Errorf("estimate: %v", est)
	}
	var started map[string]int64
	if code := do(t, c, "POST", ts.URL+"/api/v1/runs", RunOptions{Kind: KindFull, DurationMS: 1000}, &started); code != 202 {
		t.Fatalf("start run: %d", code)
	}
	deadline := time.Now().Add(10 * time.Second)
	var rep Report
	for time.Now().Before(deadline) {
		var active *Progress
		do(t, c, "GET", ts.URL+"/api/v1/runs/active", nil, &active)
		if active == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	do(t, c, "GET", ts.URL+"/api/v1/runs/last", nil, &rep)
	if rep.ID != started["id"] || rep.Status != "done" || len(rep.Paths) != 4 {
		t.Fatalf("report: id %d status %s paths %d", rep.ID, rep.Status, len(rep.Paths))
	}
	verdicts := map[string]string{}
	for _, p := range rep.Paths {
		verdicts[p.Src+">"+p.Dst+"@"+p.SegID] = p.Verdict
	}
	if verdicts["a>b@10.31.0.0/24 vlan 301"] != "green" || verdicts["b>a@10.31.0.0/24 vlan 301"] != "red" {
		t.Errorf("verdicts: %v", verdicts)
	}
	found := false
	for _, pr := range rep.Problems {
		if pr.Kind == "tcp_intercepted" && pr.Src == "b" && pr.Dst == "a" {
			found = true
		}
	}
	if !found {
		t.Errorf("tcp interception not reported: %+v", rep.Problems)
	}
	// throughput is skipped where TCP echo fails
	var mapg MapGraph
	do(t, c, "GET", ts.URL+"/api/v1/map", nil, &mapg)
	if len(mapg.Nodes) < 4 || len(mapg.Edges) == 0 {
		t.Errorf("map: %d nodes %d edges", len(mapg.Nodes), len(mapg.Edges))
	}
	// viewer cannot start runs; CSRF protection rejects form posts
	do(t, c, "POST", ts.URL+"/api/v1/users", map[string]string{"username": "v", "password": "viewer-password", "role": "viewer"}, nil)
	vc := client(t)
	do(t, vc, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "v", Password: "viewer-password"}, nil)
	if code := do(t, vc, "POST", ts.URL+"/api/v1/runs", RunOptions{Kind: KindReachability}, nil); code != http.StatusForbidden {
		t.Errorf("viewer started a run: %d", code)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/runs", strings.NewReader("kind=full"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("form post accepted: %d", resp.StatusCode)
		}
	}
	// API token works as bearer
	var tok map[string]any
	do(t, c, "POST", ts.URL+"/api/v1/tokens", map[string]string{"name": "ci", "role": "viewer"}, &tok)
	req, _ = http.NewRequest("GET", ts.URL+"/api/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer "+tok["token"].(string))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("bearer token: %v %v", err, resp.StatusCode)
	}
	resp.Body.Close()
	// metrics exported
	mr, _ := http.Get(ts.URL + "/metrics")
	mb, _ := io.ReadAll(mr.Body)
	mr.Body.Close()
	if !strings.Contains(string(mb), "lanscape_path_throughput_bits_per_second") || !strings.Contains(string(mb), "lanscape_agent_up") {
		t.Error("metrics missing")
	}
}

func TestPasswords(t *testing.T) {
	h, err := HashPassword("secret password")
	if err != nil || !CheckPassword(h, "secret password") || CheckPassword(h, "other") || CheckPassword("garbage", "x") {
		t.Fatal("argon2id round trip failed")
	}
}

func TestParseSpec(t *testing.T) {
	now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	if n, err := ParseSpec("every 5m", now.Add(-10*time.Minute), now); err != nil || !n.Before(now) {
		t.Errorf("every: %v %v", n, err)
	}
	if n, _ := ParseSpec("daily 02:00", now.Add(-time.Hour), now); n.Hour() != 2 || n.Day() != 3 {
		t.Errorf("daily: %v", n)
	}
	if _, err := ParseSpec("every 5s", time.Time{}, now); err == nil {
		t.Error("too short interval accepted")
	}
}

func TestBottlenecks(t *testing.T) {
	// two pairs of nodes on 2.5G ports that only reach ~940 Mbit/s across groups
	rep := &Report{Segments: []topoSegment{{ID: "lan", Members: members("a", "b", "c", "d")}}}
	add := func(src, dst string, mbps uint64) {
		rep.Paths = append(rep.Paths, pathResult(src, dst, mbps))
	}
	add("a", "b", 2300)
	add("c", "d", 2300)
	add("a", "c", 940)
	add("a", "d", 930)
	add("b", "c", 945)
	add("b", "d", 935)
	h := Bottlenecks(rep)
	if len(h) != 1 || h[0].Mbps != 1000 {
		t.Fatalf("hypotheses: %+v", h)
	}
}
