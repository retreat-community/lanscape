package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/retreat-community/lanscape/internal/catalog"
	"github.com/retreat-community/lanscape/internal/monitor"
	"github.com/retreat-community/lanscape/internal/notify"
	"github.com/retreat-community/lanscape/internal/store"
)

// ConfigVersion identifies the configuration file format.
const ConfigVersion = "lanscape/v1"

// ConfigFile is the declarative configuration (GitOps): objects refer to each other by name and
// carry no secrets or runtime state. A section that is left out is not touched on apply.
type ConfigFile struct {
	APIVersion  string            `yaml:"apiVersion" json:"apiVersion"`
	Services    *[]ConfService    `yaml:"services,omitempty" json:"services,omitempty"`
	Monitors    *[]ConfMonitor    `yaml:"monitors,omitempty" json:"monitors,omitempty"`
	Segments    *[]ConfSegment    `yaml:"segments,omitempty" json:"segments,omitempty"`
	Rules       *[]catalog.Rule   `yaml:"rules,omitempty" json:"rules,omitempty"`
	Channels    *[]ConfChannel    `yaml:"channels,omitempty" json:"channels,omitempty"`
	StatusPages *[]ConfStatusPage `yaml:"status_pages,omitempty" json:"status_pages,omitempty"`
}

// ConfService is a service (dashboard tile) keyed by name.
type ConfService struct {
	Name        string          `yaml:"name" json:"name"`
	App         string          `yaml:"app,omitempty" json:"app,omitempty"`
	Icon        string          `yaml:"icon,omitempty" json:"icon,omitempty"`
	Category    string          `yaml:"category,omitempty" json:"category,omitempty"`
	Group       string          `yaml:"group,omitempty" json:"group,omitempty"`
	InternalURL string          `yaml:"internal_url,omitempty" json:"internal_url,omitempty"`
	ExternalURL string          `yaml:"external_url,omitempty" json:"external_url,omitempty"`
	Addresses   []store.Address `yaml:"addresses,omitempty" json:"addresses,omitempty"`
	Discovered  string          `yaml:"discovered,omitempty" json:"discovered,omitempty"` // discovery card key
	Tile        bool            `yaml:"tile,omitempty" json:"tile,omitempty"`
	Sort        int             `yaml:"sort,omitempty" json:"sort,omitempty"`
	Notes       string          `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// ConfMonitor is a monitor keyed by name. Composite expressions name their operands in braces:
// "{Router} && ({NAS} || {Backup NAS})".
type ConfMonitor struct {
	Name       string         `yaml:"name" json:"name"`
	Service    string         `yaml:"service,omitempty" json:"service,omitempty"`
	Check      map[string]any `yaml:"check" json:"check"`
	Interval   int            `yaml:"interval,omitempty" json:"interval,omitempty"` // seconds
	Retries    int            `yaml:"retries,omitempty" json:"retries,omitempty"`
	From       []string       `yaml:"from,omitempty" json:"from,omitempty"` // agent names or "server"
	MinFailing int            `yaml:"min_failing,omitempty" json:"min_failing,omitempty"`
	SLA        float64        `yaml:"sla,omitempty" json:"sla,omitempty"`
	Disabled   bool           `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	DependsOn  []string       `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
}

// ConfSegment holds the manual settings of a network segment.
type ConfSegment struct {
	ID           string `yaml:"id" json:"id"`
	Name         string `yaml:"name,omitempty" json:"name,omitempty"`
	ExpectedMbps int    `yaml:"expected_mbps,omitempty" json:"expected_mbps,omitempty"`
}

// ConfChannel is a notification channel keyed by name; secrets are never exported and are
// kept when left out.
type ConfChannel struct {
	Name     string         `yaml:"name" json:"name"`
	Type     string         `yaml:"type" json:"type"`
	Disabled bool           `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	Config   map[string]any `yaml:"config,omitempty" json:"config,omitempty"` // "monitors" lists monitor names
}

// ConfStatusPage is a status page keyed by slug.
type ConfStatusPage struct {
	Slug        string            `yaml:"slug" json:"slug"`
	Title       string            `yaml:"title" json:"title"`
	Public      bool              `yaml:"public,omitempty" json:"public,omitempty"`
	Domain      string            `yaml:"domain,omitempty" json:"domain,omitempty"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Groups      []ConfStatusGroup `yaml:"groups,omitempty" json:"groups,omitempty"`
	Accent      string            `yaml:"accent,omitempty" json:"accent,omitempty"`
	LogoURL     string            `yaml:"logo_url,omitempty" json:"logo_url,omitempty"`
	Theme       string            `yaml:"theme,omitempty" json:"theme,omitempty"`
	Footer      string            `yaml:"footer,omitempty" json:"footer,omitempty"`
}

// ConfStatusGroup lists monitors by name.
type ConfStatusGroup struct {
	Name     string   `yaml:"name" json:"name"`
	Monitors []string `yaml:"monitors" json:"monitors"`
}

// ConfigChange is one step of an apply plan.
type ConfigChange struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"` // create, update, delete, unchanged
}

var (
	idRef   = regexp.MustCompile(`#(\d+)`)
	nameRef = regexp.MustCompile(`\{([^{}]+)\}`)
)

// names resolves ids to names and back for one configuration run.
type names struct {
	svcByID, monByID     map[int64]string
	svcByName, monByName map[string]int64
	agentName, agentID   map[string]string
}

func (s *Server) loadNames(ctx context.Context) (*names, error) {
	n := &names{svcByID: map[int64]string{}, monByID: map[int64]string{}, svcByName: map[string]int64{},
		monByName: map[string]int64{}, agentName: map[string]string{}, agentID: map[string]string{}}
	svcs, err := s.store.Services(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range svcs {
		n.svcByID[v.ID], n.svcByName[v.Name] = v.Name, v.ID
	}
	for _, m := range s.uptime.snapshot() {
		n.monByID[m.ID], n.monByName[m.Name] = m.Name, m.ID
	}
	for _, a := range s.hub.List() {
		n.agentName[a.ID], n.agentID[a.Name] = a.Name, a.ID
	}
	return n, nil
}

// ExportConfig builds the configuration file from the current state.
func (s *Server) ExportConfig(ctx context.Context) (ConfigFile, error) {
	n, err := s.loadNames(ctx)
	if err != nil {
		return ConfigFile{}, err
	}
	cf := ConfigFile{APIVersion: ConfigVersion}
	svcs, _ := s.store.Services(ctx)
	services := []ConfService{}
	for _, v := range svcs {
		services = append(services, confService(v))
	}
	mons := []ConfMonitor{}
	for _, m := range s.uptime.snapshot() {
		mons = append(mons, n.confMonitor(m))
	}
	sort.Slice(mons, func(i, j int) bool { return mons[i].Name < mons[j].Name })
	segCfg, _ := s.store.SegmentConfigs(ctx)
	segs := []ConfSegment{}
	for _, c := range segCfg {
		segs = append(segs, ConfSegment{ID: c.SegmentID, Name: c.Name, ExpectedMbps: c.ExpectedMbps})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].ID < segs[j].ID })
	rules := []catalog.Rule{}
	_ = s.store.GetSetting(ctx, "discovery_rules", &rules)
	chans, _ := s.store.Channels(ctx)
	channels := []ConfChannel{}
	for _, c := range chans {
		channels = append(channels, n.confChannel(c))
	}
	pages, _ := s.store.StatusPages(ctx)
	status := []ConfStatusPage{}
	for _, p := range pages {
		status = append(status, n.confStatusPage(p))
	}
	cf.Services, cf.Monitors, cf.Segments, cf.Rules, cf.Channels, cf.StatusPages = &services, &mons, &segs, &rules, &channels, &status
	return cf, nil
}

func confService(v store.Service) ConfService {
	return ConfService{Name: v.Name, App: v.AppID, Icon: v.Icon, Category: v.Category, Group: v.Group, InternalURL: v.InternalURL,
		ExternalURL: v.ExternalURL, Addresses: v.Addresses, Discovered: v.CardKey, Tile: v.Tile, Sort: v.Sort, Notes: v.Notes}
}

func (n *names) confMonitor(m store.Monitor) ConfMonitor {
	c := ConfMonitor{Name: m.Name, Service: n.svcByID[m.ServiceID], Interval: m.IntervalS, Retries: m.Retries,
		MinFailing: m.MinFailing, SLA: m.SLA, Disabled: !m.Enabled}
	_ = json.Unmarshal(m.Spec, &c.Check)
	if expr, ok := c.Check["expr"].(string); ok {
		c.Check["expr"] = idRef.ReplaceAllStringFunc(expr, func(x string) string {
			id, _ := strconv.ParseInt(x[1:], 10, 64)
			if name, ok := n.monByID[id]; ok {
				return "{" + name + "}"
			}
			return x
		})
	}
	for _, p := range m.Points {
		if name, ok := n.agentName[p]; ok {
			p = name
		}
		c.From = append(c.From, p)
	}
	for _, id := range m.Parents {
		c.DependsOn = append(c.DependsOn, n.monByID[id])
	}
	if c.MinFailing == 1 {
		c.MinFailing = 0
	}
	return c
}

func (n *names) confChannel(c store.Channel) ConfChannel {
	cfg := notify.StripSecrets(c.Type, c.Config)
	if ids, ok := cfg["monitors"].([]any); ok {
		var list []any
		for _, x := range ids {
			if f, ok := x.(float64); ok {
				list = append(list, n.monByID[int64(f)])
			}
		}
		cfg["monitors"] = list
	}
	if len(cfg) == 0 {
		cfg = nil
	}
	return ConfChannel{Name: c.Name, Type: c.Type, Disabled: !c.Enabled, Config: cfg}
}

func (n *names) confStatusPage(p store.StatusPage) ConfStatusPage {
	c := ConfStatusPage{Slug: p.Slug, Title: p.Title, Public: p.Public, Domain: p.Domain, Description: p.Config.Description,
		Accent: p.Config.Accent, LogoURL: p.Config.LogoURL, Theme: p.Config.Theme, Footer: p.Config.Footer}
	for _, g := range p.Config.Groups {
		cg := ConfStatusGroup{Name: g.Name, Monitors: []string{}}
		for _, id := range g.Monitors {
			cg.Monitors = append(cg.Monitors, n.monByID[id])
		}
		c.Groups = append(c.Groups, cg)
	}
	return c
}

// ParseConfig reads a YAML (or JSON) configuration file.
func ParseConfig(b []byte) (ConfigFile, error) {
	var cf ConfigFile
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&cf); err != nil && !errors.Is(err, io.EOF) {
		return cf, fmt.Errorf("config: %w", err)
	}
	if cf.APIVersion != ConfigVersion {
		return cf, fmt.Errorf("config: apiVersion must be %q", ConfigVersion)
	}
	return cf, nil
}

// ApplyConfig makes the state match the file. With dryRun nothing is changed; with prune
// objects of the listed sections that are missing from the file are deleted.
func (s *Server) ApplyConfig(ctx context.Context, cf ConfigFile, dryRun, prune bool) ([]ConfigChange, error) {
	if cf.APIVersion != ConfigVersion {
		return nil, badInput("apiVersion must be %q", ConfigVersion)
	}
	n, err := s.loadNames(ctx)
	if err != nil {
		return nil, err
	}
	if err := n.validate(cf, prune); err != nil {
		return nil, err
	}
	if err := s.checkPruneRefs(ctx, n, cf, prune); err != nil {
		return nil, err
	}
	var plan []ConfigChange
	add := func(kind, name, action string) {
		plan = append(plan, ConfigChange{Kind: kind, Name: name, Action: action})
	}

	if cf.Services != nil {
		existing, _ := s.store.Services(ctx)
		byName := map[string]store.Service{}
		for _, v := range existing {
			byName[v.Name] = v
		}
		want := map[string]bool{}
		for _, c := range *cf.Services {
			want[c.Name] = true
			old, ok := byName[c.Name]
			switch {
			case ok && reflect.DeepEqual(normService(confService(old)), normService(c)):
				add("service", c.Name, "unchanged")
				continue
			case ok:
				add("service", c.Name, "update")
			default:
				add("service", c.Name, "create")
			}
			if dryRun {
				continue
			}
			v := store.Service{ID: old.ID, Name: c.Name, AppID: c.App, Icon: c.Icon, Category: c.Category, Group: c.Group,
				InternalURL: c.InternalURL, ExternalURL: c.ExternalURL, Addresses: c.Addresses, CardKey: c.Discovered, Tile: c.Tile,
				Sort: c.Sort, Notes: c.Notes, CreatedAt: old.CreatedAt}
			if v.Addresses == nil {
				v.Addresses = []store.Address{}
			}
			id, err := s.store.SaveService(ctx, v)
			if err != nil {
				return plan, err
			}
			n.svcByName[c.Name], n.svcByID[id] = id, c.Name
		}
		if prune {
			for _, v := range existing {
				if !want[v.Name] {
					add("service", v.Name, "delete")
					if !dryRun {
						if err := s.store.DeleteService(ctx, v.ID); err != nil {
							return plan, err
						}
					}
				}
			}
		}
		if !dryRun {
			s.events.Publish("services", map[string]any{})
		}
	}

	if cf.Monitors != nil {
		steps, err := s.applyMonitors(ctx, n, *cf.Monitors, dryRun, prune)
		plan = append(plan, steps...)
		if err != nil {
			return plan, err
		}
	}

	if cf.Segments != nil {
		existing, _ := s.store.SegmentConfigs(ctx)
		for _, c := range *cf.Segments {
			old, ok := existing[c.ID]
			switch {
			case ok && old.Name == c.Name && old.ExpectedMbps == c.ExpectedMbps:
				add("segment", c.ID, "unchanged")
				continue
			case ok:
				add("segment", c.ID, "update")
			default:
				add("segment", c.ID, "create")
			}
			if !dryRun {
				if err := s.store.SetSegmentConfig(ctx, store.SegmentConfig{SegmentID: c.ID, Name: c.Name, ExpectedMbps: c.ExpectedMbps}); err != nil {
					return plan, err
				}
			}
		}
	}

	if cf.Rules != nil {
		old := []catalog.Rule{}
		_ = s.store.GetSetting(ctx, "discovery_rules", &old)
		rules := *cf.Rules
		if rules == nil {
			rules = []catalog.Rule{}
		}
		if reflect.DeepEqual(old, rules) {
			add("rules", strconv.Itoa(len(rules)), "unchanged")
		} else {
			add("rules", strconv.Itoa(len(rules)), "update")
			if !dryRun {
				if err := s.store.SetSetting(ctx, "discovery_rules", rules); err != nil {
					return plan, err
				}
			}
		}
	}

	if cf.Channels != nil {
		steps, err := s.applyChannels(ctx, n, *cf.Channels, dryRun, prune)
		plan = append(plan, steps...)
		if err != nil {
			return plan, err
		}
	}

	if cf.StatusPages != nil {
		steps, err := s.applyStatusPages(ctx, n, *cf.StatusPages, dryRun, prune)
		plan = append(plan, steps...)
		if err != nil {
			return plan, err
		}
	}
	return plan, nil
}

func normService(c ConfService) ConfService {
	if len(c.Addresses) == 0 {
		c.Addresses = nil
	}
	return c
}

// validate checks names, references and duplicates before anything is changed.
func (n *names) validate(cf ConfigFile, prune bool) error {
	svc := map[string]bool{}
	for name := range n.svcByName {
		svc[name] = true
	}
	if cf.Services != nil {
		seen := map[string]bool{}
		for _, c := range *cf.Services {
			if strings.TrimSpace(c.Name) == "" || seen[c.Name] {
				return badInput("services: empty or duplicate name %q", c.Name)
			}
			if !validURL(c.InternalURL) || !validURL(c.ExternalURL) {
				return badInput("service %q: URLs must start with http:// or https://", c.Name)
			}
			seen[c.Name], svc[c.Name] = true, true
		}
	}
	mon := map[string]bool{}
	if !prune || cf.Monitors == nil {
		// with prune the file's monitors are all that will exist
		for name := range n.monByName {
			mon[name] = true
		}
	}
	if cf.Monitors != nil {
		seen := map[string]bool{}
		for _, c := range *cf.Monitors {
			if strings.TrimSpace(c.Name) == "" || seen[c.Name] || strings.ContainsAny(c.Name, "{}") {
				return badInput("monitors: empty, duplicate or invalid name %q", c.Name)
			}
			seen[c.Name], mon[c.Name] = true, true
		}
		for _, c := range *cf.Monitors {
			if c.Service != "" && !svc[c.Service] {
				return badInput("monitor %q: unknown service %q", c.Name, c.Service)
			}
			for _, p := range c.From {
				if _, ok := n.agentID[p]; !ok && p != pointServer {
					if _, ok := n.agentName[p]; !ok {
						return badInput("monitor %q: unknown agent %q", c.Name, p)
					}
				}
			}
			refs := append([]string(nil), c.DependsOn...)
			if expr, ok := c.Check["expr"].(string); ok {
				for _, m := range nameRef.FindAllStringSubmatch(expr, -1) {
					refs = append(refs, m[1])
				}
			}
			for _, r := range refs {
				if !mon[r] {
					return badInput("monitor %q: unknown monitor %q", c.Name, r)
				}
				if r == c.Name {
					return badInput("monitor %q depends on itself", c.Name)
				}
			}
		}
	}
	if cf.StatusPages != nil {
		for _, p := range *cf.StatusPages {
			for _, g := range p.Groups {
				for _, m := range g.Monitors {
					if !mon[m] {
						return badInput("status page %q: unknown monitor %q", p.Slug, m)
					}
				}
			}
		}
	}
	if cf.Channels != nil {
		for _, c := range *cf.Channels {
			if ms, ok := c.Config["monitors"].([]any); ok {
				for _, m := range ms {
					if name, _ := m.(string); !mon[name] {
						return badInput("channel %q: unknown monitor %v", c.Name, m)
					}
				}
			}
		}
	}
	return nil
}

// checkPruneRefs refuses to prune monitors that stored status pages or channels (sections the
// file leaves out) still use.
func (s *Server) checkPruneRefs(ctx context.Context, n *names, cf ConfigFile, prune bool) error {
	if !prune || cf.Monitors == nil {
		return nil
	}
	keep := map[int64]bool{}
	for _, c := range *cf.Monitors {
		keep[n.monByName[c.Name]] = true
	}
	used := func(id int64) bool { return id != 0 && !keep[id] }
	if cf.StatusPages == nil {
		pages, err := s.store.StatusPages(ctx)
		if err != nil {
			return err
		}
		for _, p := range pages {
			for _, g := range p.Config.Groups {
				for _, id := range g.Monitors {
					if used(id) {
						return badInput("monitor %q is on status page %q; update the page in the same file", n.monByID[id], p.Slug)
					}
				}
			}
		}
	}
	if cf.Channels == nil {
		chans, err := s.store.Channels(ctx)
		if err != nil {
			return err
		}
		for _, c := range chans {
			var common notify.Common
			_ = json.Unmarshal(c.Config, &common)
			for _, id := range common.Monitors {
				if used(id) {
					return badInput("monitor %q is used by channel %q; update the channel in the same file", n.monByID[id], c.Name)
				}
			}
		}
	}
	return nil
}

// monitorReq converts a configuration entry; ok is false while a referenced monitor does
// not exist yet.
func (n *names) monitorReq(c ConfMonitor) (monitorReq, bool, error) {
	check := map[string]any{}
	for k, v := range c.Check {
		check[k] = v
	}
	ready := true
	if expr, ok := check["expr"].(string); ok {
		check["expr"] = nameRef.ReplaceAllStringFunc(expr, func(x string) string {
			id, ok := n.monByName[x[1:len(x)-1]]
			if !ok {
				ready = false
			}
			return "#" + strconv.FormatInt(id, 10)
		})
	}
	spec, err := json.Marshal(check)
	if err != nil {
		return monitorReq{}, false, err
	}
	enabled := !c.Disabled
	req := monitorReq{ServiceID: n.svcByName[c.Service], Name: c.Name, Spec: spec, IntervalS: c.Interval, Retries: c.Retries,
		MinFailing: c.MinFailing, SLA: c.SLA, Enabled: &enabled, Parents: []int64{}}
	if c.Service == "" {
		req.ServiceID = 0
	}
	for _, p := range c.From {
		if id, ok := n.agentID[p]; ok {
			p = id
		}
		req.Points = append(req.Points, p)
	}
	for _, d := range c.DependsOn {
		id, ok := n.monByName[d]
		if !ok {
			ready = false
		}
		req.Parents = append(req.Parents, id)
	}
	return req, ready, nil
}

func (s *Server) applyMonitors(ctx context.Context, n *names, list []ConfMonitor, dryRun, prune bool) ([]ConfigChange, error) {
	var plan []ConfigChange
	current := map[string]store.Monitor{}
	for _, m := range s.uptime.snapshot() {
		current[m.Name] = m
	}
	var pending []ConfMonitor
	for _, c := range list {
		old, ok := current[c.Name]
		switch {
		case ok && reflect.DeepEqual(normMonitor(n.confMonitor(old)), normMonitor(c)):
			plan = append(plan, ConfigChange{"monitor", c.Name, "unchanged"})
		case ok:
			plan = append(plan, ConfigChange{"monitor", c.Name, "update"})
			pending = append(pending, c)
		default:
			plan = append(plan, ConfigChange{"monitor", c.Name, "create"})
			pending = append(pending, c)
		}
	}
	if !dryRun {
		// monitors are created in dependency order: operands and parents first
		for len(pending) > 0 {
			var next []ConfMonitor
			for _, c := range pending {
				req, ready, err := n.monitorReq(c)
				if err != nil {
					return plan, err
				}
				if !ready {
					next = append(next, c)
					continue
				}
				m, err := s.saveMonitor(ctx, current[c.Name].ID, req)
				if err != nil {
					return plan, fmt.Errorf("monitor %q: %w", c.Name, err)
				}
				n.monByName[c.Name], n.monByID[m.ID] = m.ID, c.Name
			}
			if len(next) == len(pending) {
				return plan, badInput("monitors %q form a dependency cycle", next[0].Name)
			}
			pending = next
		}
	}
	if prune {
		want := map[string]bool{}
		for _, c := range list {
			want[c.Name] = true
		}
		for name, m := range current {
			if !want[name] {
				plan = append(plan, ConfigChange{"monitor", name, "delete"})
				if !dryRun {
					s.deleteMonitor(ctx, m)
				}
			}
		}
	}
	return plan, nil
}

// normMonitor applies the defaults the server fills in, so an exported file compares equal.
func normMonitor(c ConfMonitor) ConfMonitor {
	if c.Interval == 0 {
		c.Interval = 60
	}
	if c.Retries <= 0 {
		c.Retries = 3
	}
	if c.MinFailing <= 1 {
		c.MinFailing = 0
	}
	if len(c.From) == 0 {
		c.From = nil
	}
	if len(c.DependsOn) == 0 {
		c.DependsOn = nil
	}
	if sp, err := json.Marshal(c.Check); err == nil {
		var m monitor.Spec
		if json.Unmarshal(sp, &m) == nil && m.ServerSide() {
			c.From = nil
		}
		// compare the check through its JSON form (YAML numbers decode as int, JSON as float64)
		var norm map[string]any
		_ = json.Unmarshal(sp, &norm)
		c.Check = norm
	}
	return c
}

func (s *Server) applyChannels(ctx context.Context, n *names, list []ConfChannel, dryRun, prune bool) ([]ConfigChange, error) {
	var plan []ConfigChange
	existing, err := s.store.Channels(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]store.Channel{}
	for _, c := range existing {
		byName[c.Name] = c
	}
	for _, c := range list {
		cfg := map[string]any{}
		for k, v := range c.Config {
			cfg[k] = v
		}
		if ms, ok := cfg["monitors"].([]any); ok {
			ids := []int64{}
			for _, m := range ms {
				name, _ := m.(string)
				ids = append(ids, n.monByName[name])
			}
			cfg["monitors"] = ids
		}
		old, ok := byName[c.Name]
		if ok && old.Type == c.Type {
			notify.FillSecrets(c.Type, cfg, old.Config)
		}
		raw, _ := json.Marshal(cfg)
		var cur, next map[string]any
		_ = json.Unmarshal(old.Config, &cur)
		_ = json.Unmarshal(raw, &next)
		switch {
		case ok && old.Type == c.Type && old.Enabled == !c.Disabled && reflect.DeepEqual(cur, next):
			plan = append(plan, ConfigChange{"channel", c.Name, "unchanged"})
			continue
		case ok:
			plan = append(plan, ConfigChange{"channel", c.Name, "update"})
		default:
			plan = append(plan, ConfigChange{"channel", c.Name, "create"})
		}
		if _, err := notify.New(c.Type, raw, nil); err != nil {
			return plan, badInput("channel %q: %v", c.Name, err)
		}
		if dryRun {
			continue
		}
		if _, err := s.store.SaveChannel(ctx, store.Channel{ID: old.ID, Name: c.Name, Type: c.Type, Config: raw, Enabled: !c.Disabled}); err != nil {
			return plan, err
		}
	}
	if prune {
		want := map[string]bool{}
		for _, c := range list {
			want[c.Name] = true
		}
		for _, c := range existing {
			if !want[c.Name] {
				plan = append(plan, ConfigChange{"channel", c.Name, "delete"})
				if !dryRun {
					if err := s.store.DeleteChannel(ctx, c.ID); err != nil {
						return plan, err
					}
				}
			}
		}
	}
	return plan, nil
}

func (s *Server) applyStatusPages(ctx context.Context, n *names, list []ConfStatusPage, dryRun, prune bool) ([]ConfigChange, error) {
	var plan []ConfigChange
	existing, err := s.store.StatusPages(ctx)
	if err != nil {
		return nil, err
	}
	bySlug := map[string]store.StatusPage{}
	for _, p := range existing {
		bySlug[p.Slug] = p
	}
	for _, c := range list {
		if !slugRe.MatchString(c.Slug) || strings.TrimSpace(c.Title) == "" {
			return plan, badInput("status page %q: slug (a-z, 0-9, -) and title are required", c.Slug)
		}
		old, ok := bySlug[c.Slug]
		switch {
		case ok && reflect.DeepEqual(n.confStatusPage(old), c):
			plan = append(plan, ConfigChange{"status_page", c.Slug, "unchanged"})
			continue
		case ok:
			plan = append(plan, ConfigChange{"status_page", c.Slug, "update"})
		default:
			plan = append(plan, ConfigChange{"status_page", c.Slug, "create"})
		}
		if dryRun {
			continue
		}
		p := store.StatusPage{ID: old.ID, Slug: c.Slug, Title: c.Title, Public: c.Public, Domain: c.Domain, Token: old.Token,
			Config: store.StatusConfig{Description: c.Description, Accent: c.Accent, LogoURL: c.LogoURL, Theme: c.Theme,
				Footer: c.Footer, Groups: []store.StatusGroup{}}}
		if !p.Public && p.Token == "" {
			p.Token = NewSecret("")
		}
		for _, g := range c.Groups {
			sg := store.StatusGroup{Name: g.Name, Monitors: []int64{}}
			for _, m := range g.Monitors {
				sg.Monitors = append(sg.Monitors, n.monByName[m])
			}
			p.Config.Groups = append(p.Config.Groups, sg)
		}
		if _, err := s.store.SaveStatusPage(ctx, p); err != nil {
			return plan, err
		}
	}
	if prune {
		want := map[string]bool{}
		for _, c := range list {
			want[c.Slug] = true
		}
		for _, p := range existing {
			if !want[p.Slug] {
				plan = append(plan, ConfigChange{"status_page", p.Slug, "delete"})
				if !dryRun {
					if err := s.store.DeleteStatusPage(ctx, p.ID); err != nil {
						return plan, err
					}
				}
			}
		}
	}
	return plan, nil
}

func (s *Server) apiExportConfig(w http.ResponseWriter, r *http.Request) {
	cf, err := s.ExportConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, cf)
		return
	}
	b, err := yaml.Marshal(cf)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="lanscape.yaml"`)
	_, _ = w.Write(b)
}

func (s *Server) apiApplyConfig(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cf, err := ParseConfig(b)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dry, prune := r.URL.Query().Get("dry_run") == "true", r.URL.Query().Get("prune") == "true"
	plan, err := s.ApplyConfig(r.Context(), cf, dry, prune)
	if plan == nil {
		plan = []ConfigChange{}
	}
	var ie inputError
	switch {
	case errors.As(err, &ie):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "plan": plan})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "plan": plan})
		return
	}
	if !dry {
		changed := 0
		for _, c := range plan {
			if c.Action != "unchanged" {
				changed++
			}
		}
		s.audit(r, "config.apply", "", "ok", fmt.Sprintf("%d changes, prune=%v", changed, prune))
	}
	writeJSON(w, http.StatusOK, map[string]any{"dry_run": dry, "plan": plan})
}

var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ExpandVars replaces ${NAME} with lookup(NAME), so secrets stay out of the repository. It is
// applied by whoever reads the file (the CLI or --config), never to files sent to the API.
func ExpandVars(b []byte, lookup func(string) (string, bool)) ([]byte, error) {
	var missing []string
	out := varRef.ReplaceAllFunc(b, func(m []byte) []byte {
		v, ok := lookup(string(m[2 : len(m)-1]))
		if !ok {
			missing = append(missing, string(m[2:len(m)-1]))
		}
		return []byte(v)
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("config: unset variables: %s", strings.Join(missing, ", "))
	}
	return out, nil
}
