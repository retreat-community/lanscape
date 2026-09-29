package fingerprint

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLibrary(t *testing.T) {
	lib, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.Sigs) < 100 {
		t.Fatalf("only %d signatures, want at least 100", len(lib.Sigs))
	}
	seen := map[string]bool{}
	for _, s := range lib.Sigs {
		if seen[s.ID] {
			t.Errorf("duplicate id %s", s.ID)
		}
		seen[s.ID] = true
		if len(s.Title)+len(s.Headers)+len(s.Body)+len(s.Paths)+len(s.Images) == 0 {
			t.Errorf("%s has no matcher", s.ID)
		}
	}
}

func TestIdentify(t *testing.T) {
	lib, _ := Load()
	cases := []struct {
		o    Observation
		want string
	}{
		{Observation{Title: "Gitea: Git with a cup of tea", Headers: http.Header{"Set-Cookie": {"i_like_gitea=abc"}}}, "gitea"},
		{Observation{Title: "Jellyfin"}, "jellyfin"},
		{Observation{Title: "Home Assistant"}, "homeassistant"},
		{Observation{Title: "Grafana"}, "grafana"},
		{Observation{Title: "Vaultwarden Web"}, "vaultwarden"},
		{Observation{Image: "docker.io/vaultwarden/server:1.32"}, "vaultwarden"},
		{Observation{Image: "ghcr.io/immich-app/immich-server:release"}, "immich"},
		{Observation{Image: "longhornio/longhorn-ui:v1.7.0"}, "longhorn"},
		{Observation{Image: "postgres:16"}, "postgresql"},
		{Observation{Title: "LuCI", Body: "<link href=\"/luci-static/bootstrap/cascade.css\">"}, "openwrt"},
		{Observation{Headers: http.Header{"Server": {"pve-api-daemon/3.0"}}}, "proxmox"},
	}
	for _, c := range cases {
		m, ok := lib.Identify(&c.o)
		if !ok || m.ID != c.want {
			t.Errorf("%+v: got %v %v, want %s", c.o, m.ID, ok, c.want)
		}
	}
	if _, ok := lib.Identify(&Observation{Title: "My homepage"}); ok {
		t.Error("unrelated page matched")
	}
}

func TestProbeAndUserSignatures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.SetCookie(w, &http.Cookie{Name: "i_like_gitea", Value: "x"})
			_, _ = w.Write([]byte("<html><head><title> Gitea:   Git with a cup of tea </title></head></html>"))
		case "/api/v1/version":
			_, _ = w.Write([]byte(`{"version":"1.22.0"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	lib, _ := Load()
	o, m, ok := NewProber(lib).Probe(context.Background(), srv.URL)
	if !ok || m.ID != "gitea" || o.Title != "Gitea: Git with a cup of tea" || m.Score < 10 {
		t.Fatalf("probe: %+v %v %+v", m, ok, o)
	}
	if m.Monitor.Path != "/api/healthz" {
		t.Errorf("monitor: %+v", m.Monitor)
	}
	f := filepath.Join(t.TempDir(), "user.yaml")
	_ = os.WriteFile(f, []byte("- id: myapp\n  name: My App\n  category: custom\n  title: [\"^My App$\"]\n"), 0o600)
	lib2, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := lib2.Identify(&Observation{Title: "My App"}); !ok || m.ID != "myapp" {
		t.Errorf("user signature: %+v", m)
	}
}
