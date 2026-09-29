package server

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/retreat-community/lanscape/internal/store"
)

func TestStatusPages(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	var m store.Monitor
	do(t, c, "POST", ts.URL+"/api/v1/monitors", map[string]any{"name": "Website", "retries": 1, "spec": map[string]any{"type": "http",
		"target": "http://127.0.0.1:1/"}}, &m)
	var hidden store.Monitor
	do(t, c, "POST", ts.URL+"/api/v1/monitors", map[string]any{"name": "Internal DB", "spec": map[string]any{"type": "tcp",
		"target": "127.0.0.1:1"}}, &hidden)
	s.uptime.mu.Lock()
	st := s.uptime.mons[m.ID]
	s.uptime.mu.Unlock()
	s.uptime.execute(context.Background(), st)

	page := map[string]any{"slug": "home", "title": "Home Lab", "public": true, "domain": "status.example.com",
		"config": map[string]any{"accent": "#ff6600", "groups": []map[string]any{{"name": "Web", "monitors": []int64{m.ID}}}}}
	var p store.StatusPage
	if code := do(t, c, "POST", ts.URL+"/api/v1/status-pages", page, &p); code != 200 || p.ID == 0 {
		t.Fatalf("create: %d %+v", code, p)
	}
	for _, bad := range []map[string]any{{"slug": "Bad Slug"}, {"slug": "x", "config": map[string]any{"accent": "red;}body{"}},
		{"slug": "y", "config": map[string]any{"groups": []map[string]any{{"monitors": []int64{999}}}}}} {
		if code := do(t, c, "POST", ts.URL+"/api/v1/status-pages", bad, nil); code != 400 {
			t.Errorf("%v accepted: %d", bad, code)
		}
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/status-pages", map[string]any{"slug": "home"}, nil); code != 409 {
		t.Errorf("duplicate slug: %d", code)
	}

	get := func(url, host string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req) // anonymous
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	code, body := get(ts.URL+"/status/home", "")
	if code != 200 || !strings.Contains(body, "Home Lab") || !strings.Contains(body, "Website") ||
		strings.Contains(body, "Internal DB") || strings.Contains(body, "127.0.0.1") || !strings.Contains(body, "#ff6600") ||
		!strings.Contains(body, `data-status="down"`) {
		t.Errorf("html %d: %s", code, body)
	}
	if code, body = get(ts.URL+"/", "status.example.com"); code != 200 || !strings.Contains(body, "Home Lab") {
		t.Errorf("custom domain %d", code)
	}
	if code, body = get(ts.URL+"/api/status/home", ""); code != 200 || !strings.Contains(body, `"overall":"down"`) ||
		strings.Contains(body, "127.0.0.1") {
		t.Errorf("json %d: %s", code, body)
	}

	// link-only page
	var priv store.StatusPage
	do(t, c, "POST", ts.URL+"/api/v1/status-pages", map[string]any{"slug": "team", "title": "Team", "public": false}, &priv)
	if priv.Token == "" {
		t.Fatal("no token for a link-only page")
	}
	if code, _ := get(ts.URL+"/status/team", ""); code != 404 {
		t.Errorf("private page without token: %d", code)
	}
	if code, _ := get(ts.URL+"/status/team?t="+priv.Token, ""); code != 200 {
		t.Errorf("private page with token: %d", code)
	}
	// update keeps the token, delete removes the page
	do(t, c, "PUT", ts.URL+"/api/v1/status-pages/"+strconv.FormatInt(priv.ID, 10), map[string]any{"slug": "team", "title": "Team 2"}, &priv)
	if code, body := get(ts.URL+"/status/team?t="+priv.Token, ""); code != 200 || !strings.Contains(body, "Team 2") {
		t.Errorf("after update: %d", code)
	}
	do(t, c, "DELETE", ts.URL+"/api/v1/status-pages/"+strconv.FormatInt(priv.ID, 10), nil, nil)
	if code, _ := get(ts.URL+"/status/team?t="+priv.Token, ""); code != 404 {
		t.Errorf("deleted page: %d", code)
	}
}
