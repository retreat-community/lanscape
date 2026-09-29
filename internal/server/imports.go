package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/monitor"
	"github.com/retreat-community/lanscape/internal/store"
)

// importAgent is the pseudo agent that owns findings imported by the server itself.
const importAgent = "server"

// Discovery sources filled by imports.
const (
	SourcePrometheus    = "prometheus"
	SourceHomeAssistant = "homeassistant"
)

// kumaMonitor holds the fields of an Uptime Kuma backup that map onto Lanscape monitors.
type kumaMonitor struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	URL            string `json:"url"`
	Hostname       string `json:"hostname"`
	Port           int    `json:"port"`
	Interval       int    `json:"interval"`
	MaxRetries     int    `json:"maxretries"`
	Keyword        string `json:"keyword"`
	InvertKeyword  bool   `json:"invertKeyword"`
	IgnoreTLS      bool   `json:"ignoreTls"`
	DNSResolveType string `json:"dns_resolve_type"`
	DNSResolveSrv  string `json:"dns_resolve_server"`
	Active         *bool  `json:"active"`
	Method         string `json:"method"`
	ExpiryNotify   bool   `json:"expiryNotification"`
	JSONPath       string `json:"jsonPath"`
	ExpectedValue  string `json:"expectedValue"`
	BasicAuthUser  string `json:"basic_auth_user"`
	BasicAuthPass  string `json:"basic_auth_pass"`
	PushToken      string `json:"pushToken"`
}

// KumaSpec converts an Uptime Kuma monitor; ok is false for unsupported types.
func KumaSpec(k kumaMonitor) (monitor.Spec, bool) {
	switch k.Type {
	case "http", "keyword", "json-query":
		s := monitor.Spec{Type: monitor.TypeHTTP, Target: k.URL, IgnoreTLSErrors: k.IgnoreTLS, BasicUser: k.BasicAuthUser,
			BasicPassword: k.BasicAuthPass}
		if k.Method != "" && !strings.EqualFold(k.Method, "GET") {
			s.Method = strings.ToUpper(k.Method)
		}
		if k.Type == "keyword" {
			s.Keyword, s.InvertKeyword = k.Keyword, k.InvertKeyword
		}
		if k.Type == "json-query" {
			s.JSONPath, s.JSONValue = strings.TrimPrefix(k.JSONPath, "$."), k.ExpectedValue
		}
		return s, true
	case "port":
		return monitor.Spec{Type: monitor.TypeTCP, Target: net.JoinHostPort(k.Hostname, strconv.Itoa(k.Port))}, true
	case "ping":
		return monitor.Spec{Type: monitor.TypeICMP, Target: k.Hostname}, true
	case "dns":
		return monitor.Spec{Type: monitor.TypeDNS, Target: k.Hostname, Record: k.DNSResolveType, Server: k.DNSResolveSrv}, true
	case "push":
		return monitor.Spec{Type: monitor.TypeHeartbeat}, true
	}
	return monitor.Spec{}, false
}

// apiImportKuma creates monitors from an Uptime Kuma backup (Settings → Backup → Export).
func (s *Server) apiImportKuma(w http.ResponseWriter, r *http.Request) {
	var backup struct {
		MonitorList []kumaMonitor `json:"monitorList"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&backup); err != nil {
		writeError(w, http.StatusBadRequest, "not an Uptime Kuma backup: "+err.Error())
		return
	}
	existing := map[string]bool{}
	for _, m := range s.uptime.snapshot() {
		existing[m.Name] = true
	}
	res := struct {
		Created int      `json:"created"`
		Skipped []string `json:"skipped"`
	}{Skipped: []string{}}
	for _, k := range backup.MonitorList {
		spec, ok := KumaSpec(k)
		if !ok {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (type %s)", k.Name, k.Type))
			continue
		}
		if err := spec.Validate(); err != nil || existing[k.Name] {
			reason := "already exists"
			if err != nil {
				reason = err.Error()
			}
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (%s)", k.Name, reason))
			continue
		}
		raw, _ := json.Marshal(spec)
		m := store.Monitor{Name: k.Name, Spec: raw, IntervalS: max(k.Interval, minIntervalS), Retries: max(k.MaxRetries, 1),
			MinFailing: 1, Enabled: k.Active == nil || *k.Active}
		if spec.Type == monitor.TypeHeartbeat {
			m.PushToken = NewSecret("lsh_")
		}
		if _, err := s.createMonitor(r.Context(), m); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (%v)", k.Name, err))
			continue
		}
		existing[k.Name] = true
		res.Created++
	}
	s.audit(r, "import.uptime_kuma", "", "ok", strconv.Itoa(res.Created))
	writeJSON(w, http.StatusOK, res)
}

// fetchJSON is a small client for the import sources.
func fetchJSON(ctx context.Context, rawURL, bearer string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", rawURL, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v)
}

func validHTTPURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

// apiImportPrometheus reads the active targets of a Prometheus server into the found queue.
func (s *Server) apiImportPrometheus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !validHTTPURL(req.URL) {
		writeError(w, http.StatusBadRequest, "url must be http(s)")
		return
	}
	var resp struct {
		Data struct {
			ActiveTargets []struct {
				ScrapeURL string            `json:"scrapeUrl"`
				Labels    map[string]string `json:"labels"`
				Health    string            `json:"health"`
			} `json:"activeTargets"`
		} `json:"data"`
	}
	if err := fetchJSON(r.Context(), strings.TrimRight(req.URL, "/")+"/api/v1/targets?state=active", "", &resp); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var items []discovery.Item
	for _, tg := range resp.Data.ActiveTargets {
		u, err := url.Parse(tg.ScrapeURL)
		if err != nil || u.Host == "" {
			continue
		}
		name := tg.Labels["job"]
		if inst := tg.Labels["instance"]; inst != "" {
			name += " " + inst
		}
		items = append(items, discovery.Item{Key: "target/" + tg.ScrapeURL, Kind: discovery.KindDevice, Name: strings.TrimSpace(name),
			URL: u.Scheme + "://" + u.Host, IPs: hostIPs(u.Hostname()), State: tg.Health,
			Labels: map[string]string{"job": tg.Labels["job"], "scrape_url": tg.ScrapeURL}})
	}
	s.importFindings(w, r, SourcePrometheus, items)
}

// apiImportHomeAssistant adds Home Assistant and its entities that expose an address.
func (s *Server) apiImportHomeAssistant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !validHTTPURL(req.URL) || req.Token == "" {
		writeError(w, http.StatusBadRequest, "url and a long-lived access token are required")
		return
	}
	base := strings.TrimRight(req.URL, "/")
	var states []struct {
		EntityID   string         `json:"entity_id"`
		State      string         `json:"state"`
		Attributes map[string]any `json:"attributes"`
	}
	if err := fetchJSON(r.Context(), base+"/api/states", req.Token, &states); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	u, _ := url.Parse(base)
	items := []discovery.Item{{Key: "instance/" + u.Host, Kind: discovery.KindDevice, Name: "Home Assistant", URL: base,
		IPs: hostIPs(u.Hostname()), Labels: map[string]string{"entities": strconv.Itoa(len(states))}}}
	for _, st := range states {
		ip, _ := st.Attributes["ip_address"].(string)
		if ip == "" {
			ip, _ = st.Attributes["ip"].(string)
		}
		if net.ParseIP(ip) == nil {
			continue
		}
		name, _ := st.Attributes["friendly_name"].(string)
		if name == "" {
			name = st.EntityID
		}
		items = append(items, discovery.Item{Key: "entity/" + st.EntityID, Kind: discovery.KindDevice, Name: name, IPs: []string{ip},
			State: st.State, Labels: map[string]string{"entity_id": st.EntityID}})
	}
	s.importFindings(w, r, SourceHomeAssistant, items)
}

func hostIPs(host string) []string {
	if net.ParseIP(host) != nil {
		return []string{host}
	}
	return nil
}

// importFindings stores imported items like an agent report of the server itself.
func (s *Server) importFindings(w http.ResponseWriter, r *http.Request, source string, items []discovery.Item) {
	rep := discovery.Report{At: time.Now().UnixMilli(), Sources: []discovery.SourceReport{{Source: source, Items: items}}}
	if len(items) == 0 {
		// an empty import must still clear the previous one
		rep.Sources[0].Items = []discovery.Item{}
	}
	if err := s.ingestDiscovery(r.Context(), importAgent, rep); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "import."+source, "", "ok", strconv.Itoa(len(items)))
	writeJSON(w, http.StatusOK, map[string]int{"items": len(items)})
}
