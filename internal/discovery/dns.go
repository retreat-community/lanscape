package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// DNS source and kind.
const (
	SourceDNS     = "dns"
	KindDNSRecord = "dns_record"
)

// DNSConfig lists local DNS servers whose records name devices and services.
type DNSConfig struct {
	PiholeURL       string // http://pi.hole
	PiholePassword  string // v6 app password, or the v5 API token
	AdGuardURL      string
	AdGuardUser     string
	AdGuardPassword string
	TechnitiumURL   string
	TechnitiumToken string
}

// Enabled reports whether a DNS server is configured.
func (c DNSConfig) Enabled() bool {
	return c.PiholeURL != "" || c.AdGuardURL != "" || c.TechnitiumURL != ""
}

type dnsRec struct{ name, ip, server string }

// DNSRecords reads local A records (custom DNS, rewrites, zones).
func DNSRecords(ctx context.Context, cfg DNSConfig) ([]Item, error) {
	hc := &http.Client{Timeout: 10 * time.Second}
	var recs []dnsRec
	var errs []string
	collect := func(r []dnsRec, err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
		recs = append(recs, r...)
	}
	if cfg.PiholeURL != "" {
		collect(pihole(ctx, hc, strings.TrimRight(cfg.PiholeURL, "/"), cfg.PiholePassword))
	}
	if cfg.AdGuardURL != "" {
		collect(adguard(ctx, hc, strings.TrimRight(cfg.AdGuardURL, "/"), cfg.AdGuardUser, cfg.AdGuardPassword))
	}
	if cfg.TechnitiumURL != "" {
		collect(technitium(ctx, hc, strings.TrimRight(cfg.TechnitiumURL, "/"), cfg.TechnitiumToken))
	}
	by := map[string]*Item{}
	for _, r := range recs {
		name := strings.TrimSuffix(strings.ToLower(r.name), ".")
		if net.ParseIP(r.ip) == nil || name == "" || strings.Contains(name, "*") {
			continue
		}
		it := by[name]
		if it == nil {
			it = &Item{Key: "dns/" + name, Kind: KindDNSRecord, Name: name, Labels: map[string]string{"server": r.server}}
			by[name] = it
		}
		if !contains(it.IPs, r.ip) {
			it.IPs = append(it.IPs, r.ip)
		}
	}
	out := make([]Item, 0, len(by))
	for _, it := range by {
		sort.Strings(it.IPs)
		out = append(out, *it)
	}
	sortItems(out)
	if len(errs) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("dns: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func pihole(ctx context.Context, hc *http.Client, base, password string) ([]dnsRec, error) {
	// Pi-hole v6: session from the app password, then the configured local hosts
	body, _ := json.Marshal(map[string]string{"password": password})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/auth", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if resp, err := hc.Do(req); err == nil {
		var auth struct {
			Session struct {
				SID   string `json:"sid"`
				Valid bool   `json:"valid"`
			} `json:"session"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&auth)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && auth.Session.Valid {
			defer func() {
				del, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, base+"/api/auth", nil)
				del.Header.Set("X-FTL-SID", auth.Session.SID)
				if r, err := hc.Do(del); err == nil {
					r.Body.Close()
				}
			}()
			var hosts struct {
				Config struct {
					DNS struct {
						Hosts []string `json:"hosts"`
					} `json:"dns"`
				} `json:"config"`
			}
			g, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/config/dns/hosts", nil)
			g.Header.Set("X-FTL-SID", auth.Session.SID)
			r, err := hc.Do(g)
			if err != nil {
				return nil, fmt.Errorf("pihole: %w", err)
			}
			defer r.Body.Close()
			if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&hosts); err != nil {
				return nil, fmt.Errorf("pihole: %w", err)
			}
			var out []dnsRec
			for _, h := range hosts.Config.DNS.Hosts {
				f := strings.Fields(h) // "192.168.1.10 nas.lan"
				for _, n := range f[1:] {
					out = append(out, dnsRec{n, f[0], "pihole"})
				}
			}
			return out, nil
		}
	}
	// Pi-hole v5: API token
	var v5 struct {
		Data [][]string `json:"data"`
	}
	if err := getJSON(ctx, hc, base+"/admin/api.php?customdns&action=get&auth="+url.QueryEscape(password), &v5); err != nil {
		return nil, fmt.Errorf("pihole: %w", err)
	}
	var out []dnsRec
	for _, d := range v5.Data {
		if len(d) == 2 {
			out = append(out, dnsRec{d[0], d[1], "pihole"})
		}
	}
	return out, nil
}

func adguard(ctx context.Context, hc *http.Client, base, user, password string) ([]dnsRec, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/control/rewrite/list", nil)
	req.SetBasicAuth(user, password)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("adguard: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("adguard: %s", resp.Status)
	}
	var rw []struct {
		Domain string `json:"domain"`
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rw); err != nil {
		return nil, fmt.Errorf("adguard: %w", err)
	}
	var out []dnsRec
	for _, r := range rw {
		out = append(out, dnsRec{r.Domain, r.Answer, "adguard"})
	}
	return out, nil
}

func technitium(ctx context.Context, hc *http.Client, base, token string) ([]dnsRec, error) {
	var zones struct {
		Status   string `json:"status"`
		Response struct {
			Zones []struct {
				Name     string `json:"name"`
				Type     string `json:"type"`
				Internal bool   `json:"internal"`
			} `json:"zones"`
		} `json:"response"`
	}
	if err := getJSON(ctx, hc, base+"/api/zones/list?token="+url.QueryEscape(token), &zones); err != nil {
		return nil, fmt.Errorf("technitium: %w", err)
	}
	if zones.Status != "ok" {
		return nil, fmt.Errorf("technitium: status %s", zones.Status)
	}
	var out []dnsRec
	for _, z := range zones.Response.Zones {
		if z.Internal || z.Type != "Primary" {
			continue
		}
		var recs struct {
			Response struct {
				Records []struct {
					Name  string `json:"name"`
					Type  string `json:"type"`
					RData struct {
						IPAddress string `json:"ipAddress"`
					} `json:"rData"`
				} `json:"records"`
			} `json:"response"`
		}
		u := fmt.Sprintf("%s/api/zones/records/get?token=%s&domain=%s&zone=%s&listZone=true", base, url.QueryEscape(token),
			url.QueryEscape(z.Name), url.QueryEscape(z.Name))
		if getJSON(ctx, hc, u, &recs) != nil {
			continue
		}
		for _, r := range recs.Response.Records {
			if r.Type == "A" || r.Type == "AAAA" {
				out = append(out, dnsRec{r.Name, r.RData.IPAddress, "technitium"})
			}
		}
	}
	return out, nil
}
