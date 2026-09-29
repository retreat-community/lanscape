//go:build e2e

package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const fullURL = "http://127.0.0.1:18090"

type fullReport struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Nodes  []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"nodes"`
	Segments []struct {
		ID           string `json:"id"`
		CIDR         string `json:"cidr"`
		VLAN         int    `json:"vlan"`
		ExpectedMbps int    `json:"expected_mbps"`
	} `json:"segments"`
	Paths []struct {
		Seg   int    `json:"seg"`
		Src   string `json:"src"`
		SrcIf string `json:"src_if"`
		Dst   string `json:"dst"`
		DstIf string `json:"dst_if"`
		TCPN  *struct {
			Status string `json:"status"`
			BPS    uint64 `json:"bps"`
			PathOK bool   `json:"path_ok"`
		} `json:"tcpn"`
		UDP *struct {
			Status  string  `json:"status"`
			BPS     uint64  `json:"bps"`
			LossPct float64 `json:"loss_pct"`
		} `json:"udp"`
		ExpectedMbps int    `json:"expected_mbps"`
		BestBPS      uint64 `json:"best_bps"`
		Verdict      string `json:"verdict"`
	} `json:"paths"`
	Problems []struct {
		Kind   string `json:"kind"`
		Src    string `json:"src"`
		Dst    string `json:"dst"`
		Detail string `json:"detail"`
	} `json:"problems"`
}

type session struct {
	c *http.Client
}

func login(t *testing.T) *session {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	s := &session{c: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	var err error
	for i := 0; i < 30; i++ {
		req, _ := http.NewRequest(http.MethodPost, fullURL+"/api/v1/auth/login",
			strings.NewReader(`{"username":"admin","password":"e2e-admin-password"}`))
		req.Header.Set("Content-Type", "application/json")
		var resp *http.Response
		resp, err = s.c.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
			err = io.EOF
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("login: %v", err)
	return nil
}

func (s *session) get(ctx context.Context, path string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fullURL+path, nil)
	resp, err := s.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return &httpError{resp.StatusCode, string(b)}
	}
	return decode(resp.Body, v)
}

func (s *session) post(ctx context.Context, path, body string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fullURL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return &httpError{resp.StatusCode, string(b)}
	}
	if v == nil {
		return nil
	}
	return decode(resp.Body, v)
}

func TestFull(t *testing.T) {
	requireRoot(t)
	run(t, "./topo.sh", "up")
	if out, err := exec.Command("./full.sh", "server").CombinedOutput(); err != nil {
		t.Fatalf("full.sh server: %v\n%s", err, out)
	}
	if !keep() {
		t.Cleanup(func() {
			_ = exec.Command("./full.sh", "stop").Run()
			_ = exec.Command("./topo.sh", "down").Run()
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	s := login(t)

	var tok struct {
		Token   string            `json:"token"`
		Install map[string]string `json:"install"`
	}
	if err := s.post(ctx, "/api/v1/agent-tokens", `{"name":"e2e","reusable":true}`, &tok); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tok.Install["linux"], "--ca-fingerprint") {
		t.Errorf("install command without CA pin: %q", tok.Install["linux"])
	}
	if out, err := exec.Command("./full.sh", "agents", tok.Token).CombinedOutput(); err != nil {
		t.Fatalf("full.sh agents: %v\n%s", err, out)
	}

	waitFor(t, 60*time.Second, "4 full agents and 1 lite agent online", func() bool {
		var agents []struct {
			Kind      string `json:"kind"`
			Online    bool   `json:"online"`
			Inventory struct {
				Ifaces []any `json:"ifaces"`
			} `json:"inventory"`
		}
		if s.get(ctx, "/api/v1/agents", &agents) != nil {
			return false
		}
		full, lite := 0, 0
		for _, a := range agents {
			if !a.Online || len(a.Inventory.Ifaces) == 0 {
				continue
			}
			if a.Kind == "lite" {
				lite++
			} else {
				full++
			}
		}
		return full == 4 && lite == 1
	})
	// agents hold certificates issued by the server CA (mTLS after registration)
	for _, n := range []string{"n1", "n2", "n3", "rt"} {
		if _, err := os.Stat(os.Getenv("E2E_OUT") + "/full-agent-" + n + "/agent.crt"); os.Getenv("E2E_OUT") != "" && err != nil {
			t.Errorf("agent %s has no certificate: %v", n, err)
		}
	}

	var started struct {
		ID int64 `json:"id"`
	}
	if err := s.post(ctx, "/api/v1/runs", `{"kind":"full","duration_ms":2000,"udp":true,"udp_rate_kbps":80000}`, &started); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 25*time.Minute, "full run to finish", func() bool {
		var p *struct {
			ID int64 `json:"id"`
		}
		return s.get(ctx, "/api/v1/runs/active", &p) == nil && p == nil
	})
	var r fullReport
	if err := s.get(ctx, "/api/v1/runs/last", &r); err != nil {
		t.Fatal(err)
	}
	if r.ID != started.ID || r.Status != "done" {
		t.Fatalf("run %d status %q", r.ID, r.Status)
	}
	name := map[string]string{}
	for _, n := range r.Nodes {
		name[n.ID] = n.Name
	}
	want := map[string]int{"10.10.1.0/24": 0, "10.30.0.0/24": 300, "10.31.0.0/24": 301}
	if len(r.Segments) != 3 {
		t.Fatalf("want 3 segments, got %+v", r.Segments)
	}
	segIdx := map[string]int{}
	for i, sg := range r.Segments {
		if v, ok := want[sg.CIDR]; !ok || (!noVLAN() && sg.VLAN != v) {
			t.Errorf("segment %s vlan %d", sg.CIDR, sg.VLAN)
		}
		segIdx[sg.CIDR] = i
	}
	if len(r.Paths) != 30 {
		t.Errorf("want 30 paths, got %d", len(r.Paths))
	}
	measuredC, udpC := 0, 0
	for _, p := range r.Paths {
		if p.TCPN != nil && p.TCPN.Status == "ok" && !p.TCPN.PathOK {
			t.Errorf("%s->%s: counters do not confirm the interface", name[p.Src], name[p.Dst])
		}
		if p.Seg != segIdx["10.31.0.0/24"] || p.BestBPS == 0 {
			continue
		}
		measuredC++
		mbps := float64(p.BestBPS) / 1e6
		if mbps < 85 || mbps > 115 || p.ExpectedMbps != 100 || p.Verdict != "green" {
			t.Errorf("segment C %s->%s: %.1f Mbit/s expected %d verdict %s", name[p.Src], name[p.Dst], mbps,
				p.ExpectedMbps, p.Verdict)
		}
		if p.UDP != nil && p.UDP.Status == "ok" && p.UDP.BPS > 0 {
			udpC++
		}
	}
	if measuredC < 8 {
		t.Errorf("segment C: only %d measured paths", measuredC)
	}
	if udpC == 0 {
		t.Error("no UDP results between full agents in segment C")
	}
	has := func(kind, src, dst string) bool {
		for _, pr := range r.Problems {
			if pr.Kind == kind && name[pr.Src] == src && name[pr.Dst] == dst {
				return true
			}
		}
		return false
	}
	if !has("tcp_intercepted", "n1", "n2") {
		t.Errorf("TPROXY not recognised: %+v", r.Problems)
	}
	if !has("mtu", "n1", "rt") {
		t.Errorf("MTU not recognised: %+v", r.Problems)
	}
	if !has("macvlan", "n3", "pod") {
		t.Errorf("macvlan (lite pod) not recognised: %+v", r.Problems)
	}

	// Prometheus metrics expose the paths of the finished run
	resp, err := http.Get(fullURL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "lanscape_path_throughput_bits_per_second") ||
		!strings.Contains(string(b), `lanscape_agent_up{`) {
		t.Error("metrics missing path or agent series")
	}
}
