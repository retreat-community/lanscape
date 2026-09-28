//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type miniRun struct {
	ID       uint32 `json:"id"`
	Status   string `json:"status"`
	Segments []struct {
		ID           string `json:"id"`
		CIDR         string `json:"cidr"`
		VLAN         int    `json:"vlan"`
		ExpectedMbps int    `json:"expected_mbps"`
		Members      []struct {
			Agent string `json:"agent"`
			If    string `json:"if"`
		} `json:"members"`
	} `json:"segments"`
	Paths []struct {
		Seg   int    `json:"seg"`
		Src   string `json:"src"`
		SrcIf string `json:"src_if"`
		Dst   string `json:"dst"`
		DstIf string `json:"dst_if"`
		Ping  struct {
			Recv int `json:"recv"`
		} `json:"ping"`
		Echo struct {
			Status string `json:"status"`
		} `json:"echo"`
		TCP1 *struct {
			Status string `json:"status"`
			BPS    uint64 `json:"bps"`
			PathOK bool   `json:"path_ok"`
		} `json:"tcp1"`
		TCPN *struct {
			Status string `json:"status"`
			BPS    uint64 `json:"bps"`
			PathOK bool   `json:"path_ok"`
		} `json:"tcpn"`
		ExpectedMbps int    `json:"expected_mbps"`
		BestBPS      uint64 `json:"best_bps"`
		Verdict      string `json:"verdict"`
	} `json:"paths"`
	Problems []struct {
		Kind   string `json:"kind"`
		Seg    int    `json:"seg"`
		Src    string `json:"src"`
		Dst    string `json:"dst"`
		Detail string `json:"detail"`
	} `json:"problems"`
}

func TestMini(t *testing.T) {
	requireRoot(t)
	if _, err := os.Stat(os.Getenv("LSM_BIN") + "/lsm-server"); os.Getenv("LSM_BIN") != "" && err != nil {
		t.Fatalf("LSM_BIN: %v", err)
	}
	hooks := startHooks(t, "127.0.0.1:18099")
	run(t, "./topo.sh", "up")
	cmd := exec.Command("./mini.sh", "start")
	cmd.Env = append(os.Environ(), "E2E_WEBHOOK=http://127.0.0.1:18099/hook")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mini.sh start: %v\n%s", err, out)
	}
	if !keep() {
		t.Cleanup(func() {
			_ = exec.Command("./mini.sh", "stop").Run()
			_ = exec.Command("./topo.sh", "down").Run()
		})
	}
	base := "http://127.0.0.1:18080"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	waitFor(t, 30*time.Second, "5 agents online", func() bool {
		var st struct {
			Agents []struct {
				Online bool  `json:"online"`
				Ifs    []any `json:"ifs"`
			} `json:"agents"`
		}
		if getJSON(ctx, base+"/api/state", &st) != nil {
			return false
		}
		n := 0
		for _, a := range st.Agents {
			if a.Online && len(a.Ifs) > 0 {
				n++
			}
		}
		return n == 5
	})

	var started struct {
		ID int `json:"id"`
	}
	if err := postJSON(ctx, base+"/api/run", nil, &started); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 14*time.Minute, "run to finish", func() bool {
		var st struct {
			Running bool `json:"running"`
		}
		return getJSON(ctx, base+"/api/state", &st) == nil && !st.Running
	})
	var r miniRun
	if err := getJSON(ctx, base+"/api/last", &r); err != nil {
		t.Fatal(err)
	}
	if int(r.ID) != started.ID || r.Status != "done" {
		t.Fatalf("unexpected run %d status %q", r.ID, r.Status)
	}

	// three segments with the right VLAN IDs
	want := map[string]int{"10.10.1.0/24": 0, "10.30.0.0/24": 300, "10.31.0.0/24": 301}
	if len(r.Segments) != 3 {
		t.Fatalf("want 3 segments, got %d: %+v", len(r.Segments), r.Segments)
	}
	segIdx := map[string]int{}
	for i, s := range r.Segments {
		vlan, ok := want[s.CIDR]
		if !ok {
			t.Errorf("unexpected segment %s", s.ID)
		}
		if !noVLAN() && s.VLAN != vlan {
			t.Errorf("segment %s: want VLAN %d, got %d", s.CIDR, vlan, s.VLAN)
		}
		segIdx[s.CIDR] = i
	}

	// every path is measured separately: 4 nodes in A and C, 3 in B
	if len(r.Paths) != 12+6+12 {
		t.Errorf("want 30 paths, got %d", len(r.Paths))
	}
	seen := map[string]bool{}
	for _, p := range r.Paths {
		k := p.Src + "/" + p.SrcIf + ">" + p.Dst + "/" + p.DstIf
		if seen[k] {
			t.Errorf("duplicate path %s", k)
		}
		seen[k] = true
		// counters confirm the interface for every successful measurement
		for _, tc := range []*struct {
			Status string `json:"status"`
			BPS    uint64 `json:"bps"`
			PathOK bool   `json:"path_ok"`
		}{p.TCP1, p.TCPN} {
			if tc != nil && tc.Status == "ok" && !tc.PathOK {
				t.Errorf("path %s: counters do not confirm the interface", k)
			}
		}
	}

	c := segIdx["10.31.0.0/24"]
	measuredC := 0
	for _, p := range r.Paths {
		if p.Seg != c || p.BestBPS == 0 {
			continue
		}
		measuredC++
		mbps := float64(p.BestBPS) / 1e6
		if mbps < 85 || mbps > 115 {
			t.Errorf("segment C %s->%s: %.1f Mbit/s, want 100±15%%", p.Src, p.Dst, mbps)
		}
		if p.ExpectedMbps != 100 || p.Verdict != "green" {
			t.Errorf("segment C %s->%s: expected %d verdict %s", p.Src, p.Dst, p.ExpectedMbps, p.Verdict)
		}
	}
	if measuredC < 8 {
		t.Errorf("segment C: only %d measured paths", measuredC)
	}

	has := func(kind, src, dst string) bool {
		for _, pr := range r.Problems {
			if pr.Kind == kind && pr.Src == src && pr.Dst == dst {
				return true
			}
		}
		return false
	}
	if !has("tcp_intercepted", "n1", "n2") {
		t.Errorf("TPROXY on n2 not recognised: %+v", r.Problems)
	}
	if !has("mtu", "n1", "rt") {
		t.Errorf("MTU problem n1->rt not recognised: %+v", r.Problems)
	}
	if !has("macvlan", "n3", "pod") {
		t.Errorf("macvlan problem not recognised: %+v", r.Problems)
	}
	for _, pr := range r.Problems {
		if pr.Kind == "macvlan" && !strings.Contains(pr.Detail, "macvlan sibling") {
			t.Errorf("macvlan problem without sibling recommendation: %s", pr.Detail)
		}
	}

	// webhook delivered the finished run
	waitFor(t, 10*time.Second, "webhook", func() bool { return len(hooks.all()) > 0 })
	var hooked miniRun
	if err := json.Unmarshal(hooks.all()[0], &hooked); err != nil || hooked.ID != r.ID {
		t.Errorf("webhook payload: id %d err %v", hooked.ID, err)
	}
}
