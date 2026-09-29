package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]Ref{
		"nginx":                              {"registry-1.docker.io", "library/nginx", "latest"},
		"nginx:1.25":                         {"registry-1.docker.io", "library/nginx", "1.25"},
		"vaultwarden/server:1.30.1":          {"registry-1.docker.io", "vaultwarden/server", "1.30.1"},
		"docker.io/grafana/grafana:10.2.0":   {"registry-1.docker.io", "grafana/grafana", "10.2.0"},
		"ghcr.io/org/app:v2.1.0@sha256:abcd": {"ghcr.io", "org/app", "v2.1.0"},
		"registry.lan:5000/tools/x":          {"registry.lan:5000", "tools/x", "latest"},
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	if _, err := Parse("sha256:abcd"); err == nil {
		t.Error("digest-only image accepted")
	}
	if s := (Ref{"registry-1.docker.io", "library/nginx", "1.25"}).String(); s != "nginx:1.25" {
		t.Errorf("String: %s", s)
	}
}

func TestNewer(t *testing.T) {
	tags := []string{"latest", "1.25.3", "1.25.4", "1.27.0", "1.28.0-rc1", "1.27.0-alpine", "1.29.1-alpine", "1.26", "2", "v9.0.0"}
	for cur, want := range map[string]string{
		"1.25.3":        "1.27.0",
		"1.27.0":        "",
		"1.25":          "1.26",
		"1.27.0-alpine": "1.29.1-alpine",
		"latest":        "",
		"1":             "2",
	} {
		if got := Newer(cur, tags); got != want {
			t.Errorf("Newer(%q) = %q, want %q", cur, got, want)
		}
	}
}

func TestTagsWithTokenAndPages(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:org/app:pull" {
				http.Error(w, "scope", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "t0k"})
		case r.Header.Get("Authorization") != "Bearer t0k":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="test",scope="repository:org/app:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Query().Get("last") == "":
			w.Header().Set("Link", `</v2/org/app/tags/list?n=1000&last=1.1>; rel="next"`)
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": []string{"1.0", "1.1"}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": []string{"1.2"}})
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Scheme: "http"}
	tags, err := c.Tags(context.Background(), Ref{Registry: strings.TrimPrefix(srv.URL, "http://"), Repo: "org/app", Tag: "1.0"})
	if err != nil || strings.Join(tags, ",") != "1.0,1.1,1.2" {
		t.Fatalf("tags: %v %v", tags, err)
	}
}
