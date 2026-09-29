//go:build e2e

// Package e2e runs Lanscape against network-namespace topologies built by topo.sh.
// Run as root: go test -tags e2e ./test/e2e
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("e2e tests need root")
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func noVLAN() bool {
	if os.Getenv("E2E_NO_VLAN") == "1" {
		return true
	}
	out := os.Getenv("E2E_OUT")
	if out == "" {
		out = "out"
	}
	_, err := os.Stat(out + "/novlan")
	return err == nil
}

func keep() bool { return os.Getenv("E2E_KEEP") == "1" }

func getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s: %s: %s", url, resp.Status, b)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func postJSON(ctx context.Context, url string, body io.Reader, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s: %s: %s", url, resp.Status, b)
	}
	if v == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// waitFor polls cond until it returns true or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// hookReceiver collects webhook deliveries.
type hookReceiver struct {
	mu     sync.Mutex
	bodies [][]byte
	srv    *http.Server
}

func startHooks(t *testing.T, addr string) *hookReceiver {
	t.Helper()
	h := &hookReceiver{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.bodies = append(h.bodies, b)
		h.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	h.srv = &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = h.srv.ListenAndServe() }()
	t.Cleanup(func() { _ = h.srv.Close() })
	return h
}

func (h *hookReceiver) all() [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]byte(nil), h.bodies...)
}

type httpError struct {
	code int
	body string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.code, e.body) }

func decode(r io.Reader, v any) error { return json.NewDecoder(r).Decode(v) }

// memKB is the memory of a process: resident (VmRSS, including the clean, shared pages of the
// binary) and private (anonymous memory: heap, stacks, runtime).
type memKB struct{ rss, private int }

// residentKB returns the memory of the processes listed in a pid file of the harness, in the
// order they were started.
func residentKB(t *testing.T, pidFile string) []memKB {
	t.Helper()
	out := os.Getenv("E2E_OUT")
	if out == "" {
		out = "out"
	}
	b, err := os.ReadFile(out + "/" + pidFile)
	if err != nil {
		t.Fatalf("pid file: %v", err)
	}
	field := func(file, name string) int {
		b, err := os.ReadFile(file)
		if err != nil {
			return -1
		}
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == name {
				v, _ := strconv.Atoi(f[1])
				return v
			}
		}
		return -1
	}
	var kb []memKB
	for _, pid := range strings.Fields(string(b)) {
		kb = append(kb, memKB{rss: field("/proc/"+pid+"/status", "VmRSS:"), private: field("/proc/"+pid+"/smaps_rollup", "Anonymous:")})
	}
	return kb
}

// iperf3BPS measures TCP throughput from one topology node to another with iperf3 (4 streams,
// 5 s, like the default full run).
func iperf3BPS(from, to, addr string) (float64, error) {
	if _, err := exec.LookPath("iperf3"); err != nil {
		return 0, err
	}
	prefix := os.Getenv("E2E_PREFIX")
	if prefix == "" {
		prefix = "lse"
	}
	srv := exec.Command("ip", "netns", "exec", prefix+"-"+to, "iperf3", "-s", "-1", "-p", "5299", "-B", addr) //nolint:gosec // test topology
	if err := srv.Start(); err != nil {
		return 0, err
	}
	defer func() { _ = srv.Process.Kill(); _ = srv.Wait() }()
	time.Sleep(500 * time.Millisecond)
	out, err := exec.Command("ip", "netns", "exec", prefix+"-"+from, "iperf3", "-c", addr, "-p", "5299", //nolint:gosec // test topology
		"-t", "5", "-P", "4", "-J").Output()
	if err != nil {
		return 0, fmt.Errorf("iperf3: %w", err)
	}
	var res struct {
		End struct {
			SumReceived struct {
				BPS float64 `json:"bits_per_second"`
			} `json:"sum_received"`
		} `json:"end"`
	}
	if err := json.Unmarshal(out, &res); err != nil || res.End.SumReceived.BPS == 0 {
		return 0, fmt.Errorf("iperf3 output: %v", err)
	}
	return res.End.SumReceived.BPS, nil
}
