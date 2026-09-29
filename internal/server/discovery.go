package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/catalog"
	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/monitor"
	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/store"
)

// Found-queue statuses.
const (
	FoundNew     = "new"
	FoundAdded   = "added"
	FoundIgnored = "ignored"
	FoundHidden  = "hidden"
)

// FoundCard is a catalog card with its triage state.
type FoundCard struct {
	catalog.Card
	Status    string `json:"status"`
	ServiceID int64  `json:"service_id,omitempty"`
	Rule      string `json:"rule,omitempty"`
}

// discoveryState caches the merged cards.
type discoveryState struct {
	mu     sync.Mutex
	cards  []catalog.Card
	valid  bool
	ingest sync.Mutex // serialises report processing
}

// agentLANIP returns the address of the interface holding the default route, or the
// first private IPv4 address.
func agentLANIP(inv *agent.Inventory) string {
	dev := ""
	for _, r := range inv.Routes {
		if (r.Dst == "default" || r.Dst == "0.0.0.0/0") && (r.Table == 0 || r.Table == 254) {
			dev = r.Dev
			if r.Src != "" {
				return r.Src
			}
			break
		}
	}
	var fallback string
	for _, ifc := range inv.Ifaces {
		for _, a := range ifc.Addrs {
			ip := net.ParseIP(a.IP)
			if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ifc.Name == dev {
				return a.IP
			}
			if fallback == "" && ip.IsPrivate() {
				fallback = a.IP
			}
		}
	}
	return fallback
}

func (s *Server) agentInfos() map[string]catalog.AgentInfo {
	m := map[string]catalog.AgentInfo{}
	for _, a := range s.hub.List() {
		// the address the agent connects from is reachable from the server; fall back to the inventory
		ip := a.Addr
		if pip := net.ParseIP(ip); pip == nil || pip.IsLoopback() || pip.To4() == nil {
			ip = agentLANIP(&a.Inv)
		}
		m[a.ID] = catalog.AgentInfo{Name: a.Name, IP: ip}
	}
	return m
}

func (s *Server) agentName(id string) string {
	if a, ok := s.hub.Get(id); ok && a.Name != "" {
		return a.Name
	}
	return id
}

// onDiscovery ingests a discovery report from an agent.
func (s *Server) onDiscovery(ctx context.Context, agentID string, env proto.Envelope) {
	if env.Type != proto.MsgDiscovery {
		return
	}
	var rep discovery.Report
	if err := json.Unmarshal(env.Data, &rep); err != nil {
		s.log.Warn("invalid discovery report", "agent", agentID, "err", err)
		return
	}
	if err := s.ingestDiscovery(ctx, agentID, rep); err != nil {
		s.log.Warn("cannot store discovery report", "agent", agentID, "err", err)
	}
}

func (s *Server) ingestDiscovery(ctx context.Context, agentID string, rep discovery.Report) error {
	s.disc.ingest.Lock()
	defer s.disc.ingest.Unlock()
	ts := time.Now().UnixMilli()
	host := s.agentName(agentID)
	for _, src := range rep.Sources {
		if src.Error != "" && len(src.Items) == 0 {
			// a failing source (Docker restarting, API unavailable) must not mark everything gone
			s.log.Debug("discovery source error", "agent", agentID, "source", src.Source, "err", src.Error)
			continue
		}
		prev, err := s.store.Findings(ctx, agentID, src.Source)
		if err != nil {
			return err
		}
		var prevItems []discovery.Item
		for _, f := range prev {
			if f.Gone != 0 {
				continue
			}
			var it discovery.Item
			if json.Unmarshal(f.Data, &it) == nil {
				prevItems = append(prevItems, it)
			}
		}
		fs := make([]store.Finding, 0, len(src.Items))
		for _, it := range src.Items {
			if it.Key == "" {
				continue
			}
			data, _ := json.Marshal(it)
			fs = append(fs, store.Finding{Key: it.Key, Kind: it.Kind, Data: data})
		}
		if err := s.store.ReplaceFindings(ctx, agentID, src.Source, fs, ts); err != nil {
			return err
		}
		if len(prev) == 0 {
			continue // first report of this source: everything is new, the feed gets one card event each instead
		}
		subject := host
		if src.Source == discovery.SourceK8s {
			subject = "k8s"
			if s.otherK8sReporter(ctx, agentID) {
				continue // the cluster is already reported by another agent
			}
		}
		for _, c := range catalog.Diff(prevItems, src.Items, subject) {
			s.addChange(ctx, store.Change{TS: ts, Kind: c.Kind, Subject: c.Subject, Detail: c.Detail, AgentID: agentID})
		}
	}
	s.disc.mu.Lock()
	s.disc.valid = false
	s.disc.mu.Unlock()
	if _, err := s.foundCards(ctx); err != nil {
		return err
	}
	s.events.Publish("discovery", map[string]any{"agent": agentID})
	return nil
}

// otherK8sReporter reports whether another agent with a smaller id already sends
// Kubernetes findings (only one agent per cluster writes k8s changes to the feed).
func (s *Server) otherK8sReporter(ctx context.Context, agentID string) bool {
	fs, err := s.store.Findings(ctx, "", "")
	if err != nil {
		return false
	}
	for _, f := range fs {
		if f.Source == discovery.SourceK8s && f.Gone == 0 && f.AgentID < agentID {
			return true
		}
	}
	return false
}

func (s *Server) addChange(ctx context.Context, c store.Change) {
	id, err := s.store.AddChange(ctx, c)
	if err != nil {
		s.log.Warn("cannot store change", "err", err)
		return
	}
	c.ID = id
	s.events.Publish("change", c)
}

// cards returns the merged cards (cached until the next report).
func (s *Server) cards(ctx context.Context) ([]catalog.Card, error) {
	s.disc.mu.Lock()
	if s.disc.valid {
		c := s.disc.cards
		s.disc.mu.Unlock()
		return c, nil
	}
	s.disc.mu.Unlock()
	rows, err := s.store.Findings(ctx, "", "")
	if err != nil {
		return nil, err
	}
	fs := make([]catalog.Finding, 0, len(rows))
	for _, r := range rows {
		var it discovery.Item
		if json.Unmarshal(r.Data, &it) != nil {
			continue
		}
		fs = append(fs, catalog.Finding{Agent: r.AgentID, Source: r.Source, Item: it, Gone: r.Gone != 0, First: r.FirstSeen})
	}
	cards := catalog.Build(fs, s.agentInfos())
	s.disc.mu.Lock()
	s.disc.cards, s.disc.valid = cards, true
	s.disc.mu.Unlock()
	return cards, nil
}

// foundCards returns cards with their triage state. New cards get a state, a feed entry
// and are triaged by the rules; services created from cards get fresh addresses.
func (s *Server) foundCards(ctx context.Context) ([]FoundCard, error) {
	cards, err := s.cards(ctx)
	if err != nil {
		return nil, err
	}
	states, err := s.store.FoundStates(ctx)
	if err != nil {
		return nil, err
	}
	var rules []catalog.Rule
	_ = s.store.GetSetting(ctx, "discovery_rules", &rules)
	out := make([]FoundCard, 0, len(cards))
	for i := range cards {
		c := &cards[i]
		st, ok := states[c.Key]
		if !ok && !c.Gone {
			st = store.FoundState{Key: c.Key, Status: FoundNew, FirstSeen: time.Now().UnixMilli()}
			detail := c.Kind
			if c.App != nil {
				detail = c.App.Name
			}
			s.addChange(ctx, store.Change{Kind: catalog.ChangeServiceNew, Subject: c.Name, Detail: detail, AgentID: strings.Join(c.Agents, ",")})
			if r, ok := catalog.FirstMatch(rules, c); ok {
				st.Rule = r.Name
				if r.Action == "ignore" {
					st.Status = FoundIgnored
				} else if id, err := s.addFromCard(ctx, c, addOptions{Group: r.Group, Monitor: r.Monitor, Tile: r.Tile}); err == nil {
					st.Status, st.ServiceID = FoundAdded, id
				} else {
					s.log.Warn("rule could not add service", "rule", r.Name, "card", c.Key, "err", err)
				}
			}
			if err := s.store.SetFoundState(ctx, st); err != nil {
				return nil, err
			}
		}
		if st.Status == "" {
			st.Status = FoundNew
		}
		if st.Status == FoundAdded && st.ServiceID > 0 {
			s.refreshServiceAddresses(ctx, st.ServiceID, c)
		}
		out = append(out, FoundCard{Card: *c, Status: st.Status, ServiceID: st.ServiceID, Rule: st.Rule})
	}
	return out, nil
}

func cardAddresses(c *catalog.Card) []store.Address {
	out := make([]store.Address, 0, len(c.Addresses))
	for _, a := range c.Addresses {
		out = append(out, store.Address{Type: a.Type, Value: a.Value, Agent: a.Agent})
	}
	return out
}

func (s *Server) refreshServiceAddresses(ctx context.Context, id int64, c *catalog.Card) {
	v, err := s.store.ServiceByID(ctx, id)
	if err != nil {
		return
	}
	addrs := cardAddresses(c)
	a, _ := json.Marshal(v.Addresses)
	b, _ := json.Marshal(addrs)
	if string(a) == string(b) {
		return
	}
	v.Addresses = addrs
	if v.InternalURL == "" {
		v.InternalURL = c.InternalURL
	}
	if v.ExternalURL == "" {
		v.ExternalURL = c.ExternalURL
	}
	_, _ = s.store.SaveService(ctx, v)
}

// addOptions control how a card becomes a service.
type addOptions struct {
	Name    string `json:"name"`
	Group   string `json:"group"`
	Monitor bool   `json:"monitor"`
	Tile    bool   `json:"tile"`
}

// addFromCard creates a service (and optionally its recommended monitor) from a card.
func (s *Server) addFromCard(ctx context.Context, c *catalog.Card, o addOptions) (int64, error) {
	v := store.Service{Name: c.Name, Group: o.Group, InternalURL: c.InternalURL, ExternalURL: c.ExternalURL,
		Addresses: cardAddresses(c), CardKey: c.Key, Tile: o.Tile}
	if c.App != nil {
		v.AppID, v.Icon, v.Category = c.App.ID, c.App.Icon, c.App.Category
		if v.Group == "" {
			v.Group = c.App.Category
		}
	}
	if o.Name != "" {
		v.Name = o.Name
	}
	id, err := s.store.SaveService(ctx, v)
	if err != nil {
		return 0, err
	}
	if o.Monitor && c.Monitor != nil {
		spec, _ := json.Marshal(c.Monitor)
		if _, err := s.createMonitor(ctx, store.Monitor{ServiceID: id, Name: v.Name, Spec: spec, IntervalS: 60, Retries: 3,
			MinFailing: 1, Enabled: true}); err != nil {
			return id, err
		}
	}
	return id, nil
}

// inventoryChanges records new MAC addresses and changed interface addresses.
func (s *Server) inventoryChanges(ctx context.Context, id string, prev, cur *agent.Inventory) {
	host := s.agentName(id)
	firstSync := len(prev.Ifaces) == 0
	for _, n := range cur.Neighbors {
		if n.MAC == "" || n.MAC == "00:00:00:00:00:00" {
			continue
		}
		isNew, err := s.store.TouchMAC(ctx, strings.ToLower(n.MAC), n.IP, "", "neighbor:"+host)
		if err == nil && isNew && !firstSync {
			s.addChange(ctx, store.Change{Kind: catalog.ChangeMACNew, Subject: strings.ToLower(n.MAC), Detail: n.IP + " (" + host + " " + n.Dev + ")", AgentID: id})
		}
	}
	if firstSync {
		return
	}
	addrs := func(inv *agent.Inventory) map[string]string {
		m := map[string]string{}
		for _, ifc := range inv.Ifaces {
			var l []string
			for _, a := range ifc.Addrs {
				if ip := net.ParseIP(a.IP); ip != nil && !ip.IsLinkLocalUnicast() {
					l = append(l, fmt.Sprintf("%s/%d", a.IP, a.Prefix))
				}
			}
			sort.Strings(l)
			m[ifc.Name] = strings.Join(l, ", ")
		}
		return m
	}
	was, now := addrs(prev), addrs(cur)
	for name, v := range now {
		if old, ok := was[name]; ok && old != v && old != "" && v != "" {
			s.addChange(ctx, store.Change{Kind: catalog.ChangeIPChanged, Subject: host + " " + name, Detail: old + " → " + v, AgentID: id})
		}
	}
}

// rescan asks all agents that support discovery for a fresh report.
func (s *Server) rescan(ctx context.Context) int {
	n := 0
	var wg sync.WaitGroup
	for _, a := range s.hub.List() {
		if !a.Online || !hasCap(a.Caps, proto.MsgDiscovery) {
			continue
		}
		c, ok := s.hub.Conn(a.ID)
		if !ok {
			continue
		}
		n++
		wg.Add(1)
		go func(id string, c Conn) {
			defer wg.Done()
			rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			raw, err := c.Request(rctx, proto.MsgDiscovery, nil)
			if err != nil {
				s.log.Warn("rescan failed", "agent", id, "err", err)
				return
			}
			var rep discovery.Report
			if json.Unmarshal(raw, &rep) == nil {
				if err := s.ingestDiscovery(ctx, id, rep); err != nil {
					s.log.Warn("rescan ingest failed", "agent", id, "err", err)
				}
			}
		}(a.ID, c)
	}
	wg.Wait()
	return n
}

func hasCap(caps []string, c string) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// validateSpec checks a monitor spec.
func validateSpec(raw json.RawMessage) (monitor.Spec, error) {
	var sp monitor.Spec
	if err := json.Unmarshal(raw, &sp); err != nil {
		return sp, err
	}
	return sp, sp.Validate()
}

// serviceAgents returns the agents a service was discovered on.
func (s *Server) serviceAgents(ctx context.Context, serviceID int64) []string {
	if serviceID == 0 {
		return nil
	}
	v, err := s.store.ServiceByID(ctx, serviceID)
	if err != nil || v.CardKey == "" {
		return nil
	}
	cards, err := s.cards(ctx)
	if err != nil {
		return nil
	}
	for _, c := range cards {
		if c.Key == v.CardKey {
			return c.Agents
		}
	}
	return nil
}
