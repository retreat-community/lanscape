//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain doubles as a fake Gitea: with E2E_FAKEAPP=addr the test binary serves a page that the
// fingerprint library recognises, so discovery finds a real listening process in a namespace.
func TestMain(m *testing.M) {
	if addr := os.Getenv("E2E_FAKEAPP"); addr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/healthz", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"status":"pass"}`)
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			http.SetCookie(w, &http.Cookie{Name: "i_like_gitea", Value: "e2e"})
			_, _ = io.WriteString(w, "<html><head><title>Gitea: Git with a cup of tea</title></head></html>")
		})
		srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		fmt.Fprintln(os.Stderr, srv.ListenAndServe())
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// fakeApp runs the fake Gitea in a namespace; stop kills it.
func fakeApp(t *testing.T, ns string) (stop func()) {
	t.Helper()
	cmd := exec.Command("ip", "netns", "exec", ns, os.Args[0], "-test.run=^$") //nolint:gosec // test binary re-exec
	cmd.Env = append(os.Environ(), "E2E_FAKEAPP=0.0.0.0:3000")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}
	t.Cleanup(stop)
	return stop
}

type hookRecorder struct {
	mu     sync.Mutex
	events []string
}

func (h *hookRecorder) has(ev string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.events {
		if e == ev {
			return true
		}
	}
	return false
}

// servicesScenario: discovery finds a Gitea-like service on n2, it is added from the found
// queue with its recommended monitor, an outage opens an incident with a webhook notification,
// recovery closes it (§17 stage 2 acceptance).
func servicesScenario(t *testing.T, ctx context.Context, s *session) {
	// webhook receiver in the root namespace (the server runs there too)
	rec := &hookRecorder{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hook := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		rec.mu.Lock()
		rec.events = append(rec.events, m.Event)
		rec.mu.Unlock()
	})}
	go func() { _ = hook.Serve(ln) }()
	t.Cleanup(func() { _ = hook.Close() })
	if err := s.post(ctx, "/api/v1/channels", fmt.Sprintf(`{"name":"e2e","type":"webhook","enabled":true,"config":{"url":"http://%s/"}}`,
		ln.Addr()), nil); err != nil {
		t.Fatal(err)
	}

	// the first discovery report of every agent is a baseline without feed entries; take it now
	if err := s.post(ctx, "/api/v1/discovery/rescan", "", nil); err != nil {
		t.Fatal(err)
	}
	stop := fakeApp(t, prefix()+"-n2")
	type card struct {
		Key         string `json:"key"`
		Name        string `json:"name"`
		Status      string `json:"status"`
		InternalURL string `json:"internal_url"`
		App         *struct {
			ID string `json:"id"`
		} `json:"app"`
		Monitor *struct {
			Target string `json:"target"`
		} `json:"monitor"`
	}
	var gitea card
	waitFor(t, 2*time.Minute, "Gitea in the found queue", func() bool {
		_ = s.post(ctx, "/api/v1/discovery/rescan", "", nil)
		var cards []card
		if s.get(ctx, "/api/v1/found", &cards) != nil {
			return false
		}
		for _, c := range cards {
			if c.App != nil && c.App.ID == "gitea" && c.Monitor != nil {
				gitea = c
				return true
			}
		}
		return false
	})
	if gitea.Name != "Gitea" || !strings.HasPrefix(gitea.InternalURL, "http://192.168.250.12:3000") ||
		gitea.Monitor.Target != "http://192.168.250.12:3000/api/healthz" {
		t.Errorf("card: %+v monitor %+v", gitea, gitea.Monitor)
	}
	var svc struct {
		ID int64 `json:"id"`
	}
	if err := s.post(ctx, "/api/v1/found/add", fmt.Sprintf(`{"key":%q,"monitor":true,"tile":true}`, gitea.Key), &svc); err != nil {
		t.Fatal(err)
	}
	type monitorView struct {
		ID         int64           `json:"id"`
		ServiceID  int64           `json:"service_id"`
		Name       string          `json:"name"`
		Spec       json.RawMessage `json:"spec"`
		Status     string          `json:"status"`
		IncidentID int64           `json:"incident_id"`
	}
	monitorOf := func() (m monitorView) {
		var ms []monitorView
		if s.get(ctx, "/api/v1/monitors", &ms) == nil {
			for _, x := range ms {
				if x.ServiceID == svc.ID {
					m = x
				}
			}
		}
		return m
	}
	m := monitorOf()
	if m.ID == 0 {
		t.Fatal("no monitor created for the service")
	}
	// check often in the test
	body := fmt.Sprintf(`{"name":%q,"service_id":%d,"spec":%s,"interval_s":5,"retries":2}`, m.Name, svc.ID, m.Spec)
	if err := s.put(ctx, fmt.Sprintf("/api/v1/monitors/%d", m.ID), body); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 60*time.Second, "monitor up", func() bool { return monitorOf().Status == "up" })

	stop()
	waitFor(t, 60*time.Second, "incident opened and notified", func() bool {
		return monitorOf().IncidentID > 0 && rec.has("incident.opened")
	})
	restart := fakeApp(t, prefix()+"-n2")
	defer restart()
	waitFor(t, 60*time.Second, "incident resolved and notified", func() bool {
		x := monitorOf()
		return x.Status == "up" && x.IncidentID == 0 && rec.has("incident.resolved")
	})

	var dash struct {
		Groups []struct {
			Tiles []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"tiles"`
		} `json:"groups"`
	}
	if err := s.get(ctx, "/api/v1/dashboard", &dash); err != nil {
		t.Fatal(err)
	}
	if len(dash.Groups) == 0 || len(dash.Groups[0].Tiles) == 0 || dash.Groups[0].Tiles[0].Name != "Gitea" {
		t.Errorf("dashboard: %+v", dash)
	}
	var changes []struct {
		Kind    string `json:"kind"`
		Subject string `json:"subject"`
	}
	if err := s.get(ctx, "/api/v1/changes", &changes); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range changes {
		if c.Kind == "port_opened" && strings.HasPrefix(c.Subject, "n2 tcp/3000") {
			found = true
		}
	}
	if !found {
		t.Errorf("no port_opened n2 tcp/3000 in the change feed: %+v", changes)
	}
}

func prefix() string {
	if p := os.Getenv("E2E_PREFIX"); p != "" {
		return p
	}
	return "lse"
}
