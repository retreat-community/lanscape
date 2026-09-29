//go:build !lanscape_small

package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var hostRuleRe = regexp.MustCompile(`Host(?:SNI)?\(([^)]*)\)`)
var quotedRe = regexp.MustCompile("[`\"']([^`\"']+)[`\"']")

// hostsFromRule extracts host names from a Traefik rule such as Host(`a.example`) || Host(`b.example`).
func hostsFromRule(rule string) []string {
	var out []string
	for _, m := range hostRuleRe.FindAllStringSubmatch(rule, -1) {
		for _, q := range quotedRe.FindAllStringSubmatch(m[1], -1) {
			out = append(out, q[1])
		}
	}
	return out
}

func getJSON(ctx context.Context, hc *http.Client, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(v)
}

// Proxies reads routes (public names → backends) from the configured reverse proxies.
func Proxies(ctx context.Context, cfg ProxyConfig) ([]Item, error) {
	hc := &http.Client{Timeout: 10 * time.Second}
	var items []Item
	var errs []string
	if cfg.TraefikURL != "" {
		it, err := traefik(ctx, hc, strings.TrimRight(cfg.TraefikURL, "/"))
		if err != nil {
			errs = append(errs, err.Error())
		}
		items = append(items, it...)
	}
	if cfg.CaddyAdmin != "" {
		it, err := caddy(ctx, hc, strings.TrimRight(cfg.CaddyAdmin, "/"))
		if err != nil {
			errs = append(errs, err.Error())
		}
		items = append(items, it...)
	}
	if cfg.NginxDir != "" {
		items = append(items, Nginx(cfg.NginxDir)...)
	}
	sortItems(items)
	if len(errs) > 0 && len(items) == 0 {
		return nil, fmt.Errorf("proxy: %s", strings.Join(errs, "; "))
	}
	return items, nil
}

func traefik(ctx context.Context, hc *http.Client, base string) ([]Item, error) {
	var routers []struct {
		Name    string `json:"name"`
		Rule    string `json:"rule"`
		Service string `json:"service"`
		Status  string `json:"status"`
		TLS     *struct {
			CertResolver string `json:"certResolver"`
		} `json:"tls"`
		Provider string `json:"provider"`
	}
	if err := getJSON(ctx, hc, base+"/api/http/routers", &routers); err != nil {
		return nil, fmt.Errorf("traefik: %w", err)
	}
	var services []struct {
		Name         string `json:"name"`
		LoadBalancer *struct {
			Servers []struct {
				URL string `json:"url"`
			} `json:"servers"`
		} `json:"loadBalancer"`
	}
	_ = getJSON(ctx, hc, base+"/api/http/services", &services)
	backends := map[string][]string{}
	for _, s := range services {
		if s.LoadBalancer == nil {
			continue
		}
		for _, sv := range s.LoadBalancer.Servers {
			backends[s.Name] = append(backends[s.Name], sv.URL)
		}
	}
	var out []Item
	for _, r := range routers {
		hosts := hostsFromRule(r.Rule)
		if len(hosts) == 0 || strings.HasPrefix(r.Service, "api@") || strings.HasSuffix(r.Service, "@internal") {
			continue
		}
		svc := r.Service
		if !strings.Contains(svc, "@") && r.Provider != "" {
			svc += "@" + r.Provider
		}
		it := Item{Key: "traefik/" + r.Name, Kind: KindProxyRoute, Name: r.Name, Hosts: hosts, TLS: r.TLS != nil,
			State: r.Status, Backends: backends[svc], Labels: map[string]string{"proxy": "traefik"}}
		if it.Backends == nil {
			it.Backends = backends[r.Service]
		}
		out = append(out, it)
	}
	return out, nil
}

func caddy(ctx context.Context, hc *http.Client, base string) ([]Item, error) {
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen []string          `json:"listen"`
					Routes []json.RawMessage `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := getJSON(ctx, hc, base+"/config/", &cfg); err != nil {
		return nil, fmt.Errorf("caddy: %w", err)
	}
	var out []Item
	for name, srv := range cfg.Apps.HTTP.Servers {
		tls := false
		for _, l := range srv.Listen {
			if strings.HasSuffix(l, ":443") {
				tls = true
			}
		}
		for i, raw := range srv.Routes {
			var hosts []string
			var ups []string
			walkCaddy(raw, &hosts, &ups)
			if len(hosts) == 0 {
				continue
			}
			for j, u := range ups {
				if !strings.Contains(u, "://") {
					ups[j] = "http://" + u
				}
			}
			out = append(out, Item{Key: fmt.Sprintf("caddy/%s/%d", name, i), Kind: KindProxyRoute, Name: hosts[0], Hosts: hosts,
				TLS: tls, Backends: ups, Labels: map[string]string{"proxy": "caddy"}})
		}
	}
	return out, nil
}

// walkCaddy collects "host" matchers and reverse_proxy "dial" upstreams in a route tree.
func walkCaddy(raw json.RawMessage, hosts, ups *[]string) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			if h, ok := t["host"].([]any); ok {
				for _, s := range h {
					if str, ok := s.(string); ok {
						*hosts = append(*hosts, str)
					}
				}
			}
			if t["handler"] == "reverse_proxy" {
				if us, ok := t["upstreams"].([]any); ok {
					for _, u := range us {
						if m, ok := u.(map[string]any); ok {
							if d, ok := m["dial"].(string); ok {
								*ups = append(*ups, d)
							}
						}
					}
				}
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(v)
}

var (
	nginxServerNameRe = regexp.MustCompile(`(?m)^\s*server_name\s+([^;]+);`)
	nginxProxyPassRe  = regexp.MustCompile(`\bproxy_pass\s+([^;]+);`)
	nginxListenSSLRe  = regexp.MustCompile(`(?m)^\s*listen\s+[^;]*\b(ssl|443)\b`)
	nginxUpstreamRe   = regexp.MustCompile(`(?s)upstream\s+(\S+)\s*\{(.*?)\}`)
	nginxServerLineRe = regexp.MustCompile(`(?m)^\s*server\s+([^;\s]+)`)
)

// Nginx reads server blocks with proxy_pass from an nginx configuration directory.
func Nginx(dir string) []Item {
	var files []string
	for _, pat := range []string{"nginx.conf", "conf.d/*.conf", "sites-enabled/*", "http.d/*.conf"} {
		m, _ := filepath.Glob(filepath.Join(dir, pat))
		files = append(files, m...)
	}
	var all strings.Builder
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil { //nolint:gosec // configuration files of the host
			all.Write(b)
			all.WriteByte('\n')
		}
	}
	text := stripComments(all.String())
	upstreams := map[string][]string{}
	for _, m := range nginxUpstreamRe.FindAllStringSubmatch(text, -1) {
		for _, s := range nginxServerLineRe.FindAllStringSubmatch(m[2], -1) {
			upstreams[m[1]] = append(upstreams[m[1]], s[1])
		}
	}
	var out []Item
	for i, block := range serverBlocks(text) {
		sn := nginxServerNameRe.FindStringSubmatch(block)
		if sn == nil {
			continue
		}
		var hosts []string
		for _, h := range strings.Fields(sn[1]) {
			if h != "_" && !strings.HasPrefix(h, "~") && !strings.Contains(h, "*") {
				hosts = append(hosts, h)
			}
		}
		var backs []string
		for _, pp := range nginxProxyPassRe.FindAllStringSubmatch(block, -1) {
			target := strings.TrimSpace(pp[1])
			if u, err := url.Parse(target); err == nil && upstreams[u.Host] != nil {
				for _, s := range upstreams[u.Host] {
					backs = append(backs, u.Scheme+"://"+s)
				}
				continue
			}
			backs = append(backs, target)
		}
		if len(hosts) == 0 || len(backs) == 0 {
			continue
		}
		out = append(out, Item{Key: fmt.Sprintf("nginx/%s/%d", hosts[0], i), Kind: KindProxyRoute, Name: hosts[0], Hosts: hosts,
			TLS: nginxListenSSLRe.MatchString(block), Backends: backs, Labels: map[string]string{"proxy": "nginx"}})
	}
	return out
}

func stripComments(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		if i := strings.IndexByte(l, '#'); i >= 0 {
			l = l[:i]
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

// serverBlocks returns the bodies of top-level "server { ... }" blocks (nested blocks included).
func serverBlocks(text string) []string {
	var out []string
	re := regexp.MustCompile(`\bserver\s*\{`)
	for _, loc := range re.FindAllStringIndex(text, -1) {
		depth, start := 0, loc[1]-1
		for i := start; i < len(text); i++ {
			if text[i] == '{' {
				depth++
			} else if text[i] == '}' {
				depth--
				if depth == 0 {
					out = append(out, text[start+1:i])
					break
				}
			}
		}
	}
	return out
}

// sorted helper for tests
func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
