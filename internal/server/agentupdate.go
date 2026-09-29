package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/store"
)

// DefaultReleasesURL lists the published releases (GitHub API format; a mirror can serve the same).
const DefaultReleasesURL = "https://api.github.com/repos/retreat-community/lanscape/releases"

// Release is the newest agent release of a channel.
type Release struct {
	Version    string            `json:"version"`
	Prerelease bool              `json:"prerelease"`
	BaseURL    string            `json:"base_url"`
	Checksums  map[string]string `json:"-"`
}

// AgentUpdateView is one agent in the update list.
type AgentUpdateView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Online    bool   `json:"online"`
	Outdated  bool   `json:"outdated"`
	CanUpdate bool   `json:"can_update"`
	Reason    string `json:"reason,omitempty"` // why it cannot be updated from the panel
}

// AgentUpdates is the state of the agent update page.
type AgentUpdates struct {
	Channel string            `json:"channel"`
	Auto    bool              `json:"auto"`
	Latest  *Release          `json:"latest"`
	Error   string            `json:"error,omitempty"`
	Agents  []AgentUpdateView `json:"agents"`
}

type releaseCache struct {
	mu      sync.Mutex
	channel string
	at      time.Time
	rel     *Release
	running bool
	client  *http.Client // tests swap it
}

// compareVersions orders "1.2.10" after "1.2.9" and a release after its pre-releases.
func compareVersions(a, b string) int {
	a, b = strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v")
	ca, pa, _ := strings.Cut(a, "-")
	cb, pb, _ := strings.Cut(b, "-")
	na, nb := strings.Split(ca, "."), strings.Split(cb, ".")
	for i := 0; i < max(len(na), len(nb)); i++ {
		var x, y int
		if i < len(na) {
			x, _ = strconv.Atoi(na[i])
		}
		if i < len(nb) {
			y, _ = strconv.Atoi(nb[i])
		}
		if x != y {
			return x - y
		}
	}
	switch {
	case pa == pb:
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	}
	return strings.Compare(pa, pb)
}

func validVersion(v string) bool {
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	for _, p := range strings.Split(core, ".") {
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

// latestRelease finds the newest release of the channel (cached for an hour).
func (s *Server) latestRelease(ctx context.Context, force bool) (*Release, error) {
	st := s.settings(ctx)
	if st.AgentUpdateChannel == "" {
		return nil, nil
	}
	s.rel.mu.Lock()
	if !force && s.rel.rel != nil && s.rel.channel == st.AgentUpdateChannel && time.Since(s.rel.at) < time.Hour {
		r := s.rel.rel
		s.rel.mu.Unlock()
		return r, nil
	}
	hc := s.rel.client
	s.rel.mu.Unlock()
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	url := st.AgentReleasesURL
	if url == "" {
		url = DefaultReleasesURL
	}
	var list []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := fetchReleases(ctx, hc, url, &list); err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool { return compareVersions(list[i].Tag, list[j].Tag) > 0 })
	for _, r := range list {
		if r.Draft || !validVersion(r.Tag) || (r.Prerelease && st.AgentUpdateChannel != "beta") {
			continue
		}
		for _, a := range r.Assets {
			if a.Name != "checksums-full.txt" {
				continue
			}
			sums, err := fetchChecksums(ctx, hc, a.URL)
			if err != nil {
				return nil, err
			}
			rel := &Release{Version: strings.TrimPrefix(r.Tag, "v"), Prerelease: r.Prerelease, BaseURL: path.Dir(a.URL),
				Checksums: sums}
			// path.Dir collapses the scheme's double slash
			rel.BaseURL = strings.Replace(rel.BaseURL, ":/", "://", 1)
			s.rel.mu.Lock()
			s.rel.channel, s.rel.at, s.rel.rel = st.AgentUpdateChannel, time.Now(), rel
			s.rel.mu.Unlock()
			return rel, nil
		}
	}
	return nil, errors.New("no release with agent archives in the channel")
}

func fetchReleases(ctx context.Context, hc *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("releases: %s", res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(v)
}

// fetchChecksums reads a "sha256  name" list.
func fetchChecksums(ctx context.Context, hc *http.Client, url string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("checksums: %s", res.Status)
	}
	out := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(res.Body, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && len(f[0]) == 64 {
			out[strings.TrimPrefix(f[1], "*")] = f[0]
		}
	}
	return out, sc.Err()
}

// updateReason tells why an agent cannot be updated from the panel ("" when it can).
func updateReason(a AgentState) string {
	switch {
	case a.Kind == "lite":
		return "Mini agents are updated with their package"
	case a.Inv.Env.Kind == "docker" || a.Inv.Env.Kind == "k8s-pod":
		return "runs from a container image"
	case !hasCap(a.Caps, proto.MsgUpdate):
		return "updates are not allowed on this agent (--actions)"
	}
	return ""
}

func (s *Server) agentUpdates(ctx context.Context) AgentUpdates {
	st := s.settings(ctx)
	out := AgentUpdates{Channel: st.AgentUpdateChannel, Auto: st.AgentUpdateAuto, Agents: []AgentUpdateView{}}
	rel, err := s.latestRelease(ctx, false)
	if err != nil {
		out.Error = err.Error()
	}
	out.Latest = rel
	for _, a := range s.hub.List() {
		v := AgentUpdateView{ID: a.ID, Name: a.Name, Version: a.Version, Online: a.Online, Reason: updateReason(a)}
		v.CanUpdate = v.Reason == ""
		if rel != nil && validVersion(a.Version) {
			v.Outdated = compareVersions(rel.Version, a.Version) > 0
		}
		out.Agents = append(out.Agents, v)
	}
	sort.Slice(out.Agents, func(i, j int) bool { return out.Agents[i].Name < out.Agents[j].Name })
	return out
}

// updateAgent asks one agent to install the channel's release.
func (s *Server) updateAgent(ctx context.Context, id string) (proto.UpdateResultMsg, error) {
	a, ok := s.hub.Get(id)
	if !ok || !a.Online {
		return proto.UpdateResultMsg{}, errors.New("agent is offline")
	}
	if why := updateReason(a); why != "" {
		return proto.UpdateResultMsg{}, errors.New(why)
	}
	rel, err := s.latestRelease(ctx, false)
	if err != nil {
		return proto.UpdateResultMsg{}, err
	}
	if rel == nil {
		return proto.UpdateResultMsg{}, errors.New("choose an update channel first")
	}
	c, ok := s.hub.Conn(id)
	if !ok {
		return proto.UpdateResultMsg{}, errors.New("agent is offline")
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	raw, err := c.Request(ctx, proto.MsgUpdate, proto.UpdateMsg{Version: rel.Version, BaseURL: rel.BaseURL, Checksums: rel.Checksums})
	if err != nil {
		return proto.UpdateResultMsg{}, err
	}
	var res proto.UpdateResultMsg
	_ = json.Unmarshal(raw, &res)
	return res, nil
}

// updateOutdated updates every outdated agent that allows it, one after another.
func (s *Server) updateOutdated(ctx context.Context) int {
	s.rel.mu.Lock()
	if s.rel.running {
		s.rel.mu.Unlock()
		return 0
	}
	s.rel.running = true
	s.rel.mu.Unlock()
	defer func() {
		s.rel.mu.Lock()
		s.rel.running = false
		s.rel.mu.Unlock()
	}()
	n := 0
	for _, a := range s.agentUpdates(ctx).Agents {
		if !a.Online || !a.Outdated || !a.CanUpdate {
			continue
		}
		res, err := s.updateAgent(ctx, a.ID)
		result, detail := "ok", res.Detail
		if err != nil {
			result, detail = "error", err.Error()
		} else {
			n++
		}
		s.log.Info("agent update", "agent", a.Name, "from", a.Version, "result", result, "detail", detail)
		_ = s.store.Audit(ctx, store.AuditEntry{Actor: "system", Action: "agent.update", Target: a.ID, Result: result, Detail: detail})
	}
	return n
}

// agentUpdateScheduler installs new releases every 6 hours when automatic updates are on.
func (s *Server) agentUpdateScheduler(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if st := s.settings(ctx); st.AgentUpdateChannel != "" && st.AgentUpdateAuto {
			s.updateOutdated(ctx)
		}
	}
}

func (s *Server) apiAgentUpdates(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") == "1" {
		_, _ = s.latestRelease(r.Context(), true)
	}
	writeJSON(w, http.StatusOK, s.agentUpdates(r.Context()))
}

func (s *Server) apiUpgradeAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, err := s.updateAgent(r.Context(), id)
	if err != nil {
		s.audit(r, "agent.update", id, "error", err.Error())
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.audit(r, "agent.update", id, "ok", res.Detail)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) apiUpdateAllAgents(w http.ResponseWriter, r *http.Request) {
	s.audit(r, "agent.update_all", "", "ok", "")
	go s.updateOutdated(s.ctx)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}
