//go:build !lanscape_small

package discovery

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxies(t *testing.T) {
	api := map[string]string{
		"/api/http/routers": `[
			{"name":"gitea@docker","rule":"Host(` + "`git.example`" + `) || Host(` + "`gitea.lan`" + `)","service":"gitea","provider":"docker","status":"enabled","tls":{"certResolver":"le"}},
			{"name":"api@internal","rule":"Host(` + "`traefik.lan`" + `)","service":"api@internal","status":"enabled"}]`,
		"/api/http/services": `[{"name":"gitea@docker","loadBalancer":{"servers":[{"url":"http://172.18.0.2:3000"}]}}]`,
		"/config/": `{"apps":{"http":{"servers":{"srv0":{"listen":[":443"],"routes":[{"match":[{"host":["photos.example"]}],
			"handle":[{"handler":"subroute","routes":[{"handle":[{"handler":"reverse_proxy","upstreams":[{"dial":"192.168.1.10:2283"}]}]}]}]}]}}}}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := api[r.URL.Path]; ok {
			_, _ = io.WriteString(w, b)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "conf.d"), 0o750)
	_ = os.WriteFile(filepath.Join(dir, "conf.d", "apps.conf"), []byte(`
upstream jelly { server 192.168.1.20:8096; }
server {
	listen 443 ssl;
	server_name media.example;   # comment
	location / { proxy_pass http://jelly; }
}
server {
	listen 80;
	server_name _;
	return 404;
}
server {
	listen 80;
	server_name ha.lan;
	location / {
		proxy_pass http://127.0.0.1:8123/;
	}
}
`), 0o600)
	items, err := Proxies(context.Background(), ProxyConfig{TraefikURL: srv.URL, CaddyAdmin: srv.URL, NginxDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Item{}
	for _, it := range items {
		by[it.Key] = it
	}
	if len(items) != 4 {
		t.Fatalf("items: %+v", items)
	}
	if g := by["traefik/gitea@docker"]; strings.Join(sortedStrings(g.Hosts), ",") != "git.example,gitea.lan" || !g.TLS ||
		len(g.Backends) != 1 || g.Backends[0] != "http://172.18.0.2:3000" {
		t.Errorf("traefik: %+v", g)
	}
	if c := by["caddy/srv0/0"]; c.Hosts[0] != "photos.example" || !c.TLS || c.Backends[0] != "http://192.168.1.10:2283" {
		t.Errorf("caddy: %+v", c)
	}
	var media, ha Item
	for _, it := range items {
		switch it.Name {
		case "media.example":
			media = it
		case "ha.lan":
			ha = it
		}
	}
	if !media.TLS || media.Backends[0] != "http://192.168.1.20:8096" || ha.TLS || ha.Backends[0] != "http://127.0.0.1:8123/" {
		t.Errorf("nginx: %+v %+v", media, ha)
	}
	if BackendHostPort("http://127.0.0.1:8123/") != "127.0.0.1:8123" || BackendHostPort("gitea:3000") != "gitea:3000" ||
		BackendHostPort("https://x") != "x:443" {
		t.Error("backend host:port")
	}
}
