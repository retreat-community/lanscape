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

func noVLAN() bool { return os.Getenv("E2E_NO_VLAN") == "1" }

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
