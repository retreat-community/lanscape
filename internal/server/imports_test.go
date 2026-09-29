package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retreat-community/lanscape/internal/monitor"
)

func TestImports(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	backup := map[string]any{"version": "1.23", "monitorList": []map[string]any{
		{"name": "Blog", "type": "keyword", "url": "https://blog.example", "keyword": "Welcome", "interval": 60, "maxretries": 2, "active": true},
		{"name": "SSH", "type": "port", "hostname": "192.168.1.10", "port": 22, "interval": 30},
		{"name": "Backup job", "type": "push", "interval": 86400},
		{"name": "Game", "type": "gamedig", "hostname": "x"},
	}}
	var res struct {
		Created int      `json:"created"`
		Skipped []string `json:"skipped"`
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/import/uptime-kuma", backup, &res); code != 200 || res.Created != 3 || len(res.Skipped) != 1 {
		t.Fatalf("kuma: %d %+v", code, res)
	}
	do(t, c, "POST", ts.URL+"/api/v1/import/uptime-kuma", backup, &res)
	if res.Created != 0 {
		t.Errorf("re-import duplicated monitors: %+v", res)
	}
	types := map[string]string{}
	for _, m := range s.uptime.snapshot() {
		sp, _ := validateSpec(m.Spec)
		types[m.Name] = sp.Type
		if m.Name == "Backup job" && m.PushToken == "" {
			t.Error("push monitor without token")
		}
	}
	if types["Blog"] != monitor.TypeHTTP || types["SSH"] != monitor.TypeTCP || types["Backup job"] != monitor.TypeHeartbeat {
		t.Errorf("types: %v", types)
	}

	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/targets":
			_, _ = io.WriteString(w, `{"status":"success","data":{"activeTargets":[
				{"scrapeUrl":"http://192.168.1.10:9100/metrics","labels":{"job":"node","instance":"nas:9100"},"health":"up"}]}}`)
		case "/api/states":
			if r.Header.Get("Authorization") != "Bearer ha-token" {
				w.WriteHeader(401)
				return
			}
			_, _ = io.WriteString(w, `[{"entity_id":"sensor.x","state":"1","attributes":{}},
				{"entity_id":"media_player.tv","state":"on","attributes":{"friendly_name":"Living room TV","ip_address":"192.168.1.40"}}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ext.Close()
	if code := do(t, c, "POST", ts.URL+"/api/v1/import/prometheus", map[string]string{"url": ext.URL}, nil); code != 200 {
		t.Fatalf("prometheus: %d", code)
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/import/home-assistant", map[string]string{"url": ext.URL, "token": "ha-token"}, nil); code != 200 {
		t.Fatalf("home assistant: %d", code)
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/import/home-assistant", map[string]string{"url": ext.URL, "token": "bad"}, nil); code != 502 {
		t.Errorf("bad token: %d", code)
	}
	var found []FoundCard
	do(t, c, "GET", ts.URL+"/api/v1/found", nil, &found)
	var names []string
	for _, f := range found {
		names = append(names, f.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "node nas:9100") || !strings.Contains(joined, "Home Assistant") {
		t.Errorf("found: %v", names)
	}
}
