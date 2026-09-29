package server

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"html/template"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/monitor"
	"github.com/retreat-community/lanscape/internal/store"
)

//go:embed status.html
var statusHTML string

var statusTmpl = template.Must(template.New("status").Funcs(template.FuncMap{
	"pct": func(v *float64) string {
		if v == nil {
			return "—"
		}
		if *v >= 99.995 {
			return "100%"
		}
		return strconv.FormatFloat(*v, 'f', 2, 64) + "%"
	},
	"date": func(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04 UTC") },
}).Parse(statusHTML))

// StatusView is the public content of a status page (no targets, no internal details).
type StatusView struct {
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Overall     string              `json:"overall"` // up, degraded, down, maintenance
	Groups      []StatusGroupView   `json:"groups"`
	Incidents   []StatusIncident    `json:"incidents"`
	Maintenance []store.Maintenance `json:"maintenance"`
	Updated     int64               `json:"updated"`
	Accent      template.CSS        `json:"-"` // validated by cssColorRe
	LogoURL     string              `json:"-"`
	Theme       string              `json:"-"`
	Footer      string              `json:"-"`
}

// StatusGroupView is a group of monitors on the page.
type StatusGroupView struct {
	Name     string              `json:"name"`
	Monitors []StatusMonitorView `json:"monitors"`
}

// StatusMonitorView is one monitor with 90 days of history.
type StatusMonitorView struct {
	Name   string      `json:"name"`
	Status string      `json:"status"`
	Uptime *float64    `json:"uptime_90d"`
	Days   []StatusDay `json:"days"`
}

// StatusDay is one bar of the 90-day history.
type StatusDay struct {
	Date   string   `json:"date"`
	Uptime *float64 `json:"uptime"`
	Class  string   `json:"-"`
}

// StatusIncident is a current or recent incident shown publicly.
type StatusIncident struct {
	Monitor  string `json:"monitor"`
	Opened   int64  `json:"opened"`
	Closed   int64  `json:"closed,omitempty"`
	Resolved bool   `json:"resolved"`
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var cssColorRe = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|[a-z]{3,20})$`)

func (s *Server) statusView(ctx context.Context, p store.StatusPage) StatusView {
	v := StatusView{Title: p.Title, Description: p.Config.Description, Overall: monitor.Up, Updated: time.Now().UnixMilli(),
		LogoURL: p.Config.LogoURL, Theme: p.Config.Theme, Footer: p.Config.Footer,
		Groups: []StatusGroupView{}, Incidents: []StatusIncident{}, Maintenance: []store.Maintenance{}}
	if cssColorRe.MatchString(p.Config.Accent) {
		v.Accent = template.CSS(p.Config.Accent) //nolint:gosec // validated colour
	}
	mons := map[int64]store.Monitor{}
	for _, m := range s.uptime.snapshot() {
		mons[m.ID] = m
	}
	shown := map[int64]bool{}
	now := time.Now()
	for _, g := range p.Config.Groups {
		gv := StatusGroupView{Name: g.Name, Monitors: []StatusMonitorView{}}
		for _, id := range g.Monitors {
			m, ok := mons[id]
			if !ok {
				continue
			}
			shown[id] = true
			days, _ := s.store.DayStats(ctx, id, dayKey(now.AddDate(0, 0, -89)))
			mv := StatusMonitorView{Name: m.Name, Status: publicStatus(m.Status), Uptime: uptimeOf(days, 0)}
			byDay := map[int]store.DayStat{}
			for _, d := range days {
				byDay[d.Day] = d
			}
			for i := 89; i >= 0; i-- {
				t := now.AddDate(0, 0, -i)
				d, ok := byDay[dayKey(t)]
				sd := StatusDay{Date: t.Format("2006-01-02"), Class: "none"}
				if ok {
					sd.Uptime = uptimeOf([]store.DayStat{d}, 0)
				}
				switch {
				case sd.Uptime == nil && ok && d.Maint > 0:
					sd.Class = "maint"
				case sd.Uptime == nil:
				case *sd.Uptime >= 99.9:
					sd.Class = "up"
				case *sd.Uptime >= 95:
					sd.Class = "degraded"
				default:
					sd.Class = "down"
				}
				mv.Days = append(mv.Days, sd)
			}
			if statusRank[mv.Status] > statusRank[v.Overall] {
				v.Overall = mv.Status
			}
			gv.Monitors = append(gv.Monitors, mv)
		}
		v.Groups = append(v.Groups, gv)
	}
	incs, _ := s.store.Incidents(ctx, false, 0, now.AddDate(0, 0, -7).UnixMilli(), 200)
	for _, i := range incs {
		if !shown[i.MonitorID] || i.Suppressed {
			continue
		}
		v.Incidents = append(v.Incidents, StatusIncident{Monitor: mons[i.MonitorID].Name, Opened: i.Opened, Closed: i.Closed,
			Resolved: i.Closed > 0})
	}
	ms, _ := s.store.Maintenances(ctx, now.UnixMilli())
	for _, m := range ms {
		relevant := len(m.Monitors) == 0
		for _, id := range m.Monitors {
			relevant = relevant || shown[id]
		}
		if relevant {
			v.Maintenance = append(v.Maintenance, store.Maintenance{Name: m.Name, Starts: m.Starts, Ends: m.Ends})
		}
	}
	return v
}

// publicStatus hides internal states.
func publicStatus(st string) string {
	switch st {
	case monitor.Up, monitor.Degraded, monitor.Down, StatusMaintenance:
		return st
	}
	return monitor.Up
}

// statusPageFor resolves a page and checks access (public, or the link token).
func (s *Server) statusPageFor(r *http.Request, slug string) (store.StatusPage, bool) {
	var p store.StatusPage
	var err error
	if slug != "" {
		p, err = s.store.StatusPageBy(r.Context(), 0, slug, "")
	} else {
		host := r.Host
		if h, _, e := net.SplitHostPort(host); e == nil {
			host = h
		}
		p, err = s.store.StatusPageBy(r.Context(), 0, "", strings.ToLower(host))
	}
	if err != nil {
		return p, false
	}
	if !p.Public && (p.Token == "" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("t")), []byte(p.Token)) != 1) {
		return p, false
	}
	return p, true
}

func (s *Server) serveStatusHTML(w http.ResponseWriter, r *http.Request, slug string) {
	p, ok := s.statusPageFor(r, slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := statusTmpl.Execute(w, s.statusView(r.Context(), p)); err != nil {
		s.log.Warn("status page", "err", err)
	}
}

func (s *Server) apiPublicStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := s.statusPageFor(r, r.PathValue("slug"))
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, s.statusView(r.Context(), p))
}

// statusDomain serves status pages on their custom domains in front of the panel.
func (s *Server) statusDomain(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/status" {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if host != "" && net.ParseIP(host) == nil {
				if _, err := s.store.StatusPageBy(r.Context(), 0, "", strings.ToLower(host)); err == nil {
					s.serveStatusHTML(w, r, "")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// --- administration ---

func (s *Server) apiStatusPages(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.StatusPages(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) apiSaveStatusPage(w http.ResponseWriter, r *http.Request) {
	var p store.StatusPage
	if !readJSON(w, r, &p) {
		return
	}
	p.Slug = strings.ToLower(strings.TrimSpace(p.Slug))
	p.Domain = strings.ToLower(strings.TrimSpace(p.Domain))
	if !slugRe.MatchString(p.Slug) {
		writeError(w, http.StatusBadRequest, "slug: lower-case letters, digits and dashes")
		return
	}
	if strings.TrimSpace(p.Title) == "" {
		p.Title = p.Slug
	}
	if p.Domain != "" && (strings.ContainsAny(p.Domain, "/: ") || net.ParseIP(p.Domain) != nil) {
		writeError(w, http.StatusBadRequest, "domain: a host name such as status.example.com")
		return
	}
	if p.Config.Accent != "" && !cssColorRe.MatchString(p.Config.Accent) {
		writeError(w, http.StatusBadRequest, "accent: a CSS colour such as #1f5fd6")
		return
	}
	if p.Config.LogoURL != "" && !strings.HasPrefix(p.Config.LogoURL, "https://") {
		writeError(w, http.StatusBadRequest, "logo_url must be https")
		return
	}
	for _, g := range p.Config.Groups {
		for _, id := range g.Monitors {
			if _, ok := s.uptime.get(id); !ok {
				writeError(w, http.StatusBadRequest, "unknown monitor #"+strconv.FormatInt(id, 10))
				return
			}
		}
	}
	if r.PathValue("id") != "" {
		id, ok := pathID(r)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad id")
			return
		}
		old, err := s.store.StatusPageBy(r.Context(), id, "", "")
		if err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		p.ID, p.Token = id, old.Token
	} else {
		p.ID = 0
	}
	if !p.Public && p.Token == "" {
		p.Token = NewSecret("")
	}
	id, err := s.store.SaveStatusPage(r.Context(), p)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeError(w, http.StatusConflict, "slug already used")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "status_page.save", strconv.FormatInt(id, 10), "ok", p.Slug)
	p, _ = s.store.StatusPageBy(r.Context(), id, "", "")
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) apiDeleteStatusPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.store.DeleteStatusPage(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "status_page.delete", strconv.FormatInt(id, 10), "ok", "")
	w.WriteHeader(http.StatusNoContent)
}
