package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/retreat-community/lanscape/internal/proto"
)

// updateConn records update requests.
type updateConn struct {
	fakeConn
	mu  sync.Mutex
	got []proto.UpdateMsg
}

func (c *updateConn) Request(_ context.Context, typ string, data any) (json.RawMessage, error) {
	if typ != proto.MsgUpdate {
		return nil, nil
	}
	m := data.(proto.UpdateMsg)
	c.mu.Lock()
	c.got = append(c.got, m)
	c.mu.Unlock()
	return json.Marshal(proto.UpdateResultMsg{From: "1.0.0", To: m.Version, Detail: "updated"})
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		sign int
	}{{"1.2.10", "1.2.9", 1}, {"v1.0.0", "1.0.0", 0}, {"1.0.0", "1.0.0-beta.1", 1}, {"1.0.0-beta.2", "1.0.0-beta.1", 1}, {"0.9", "1.0", -1}} {
		got := compareVersions(c.a, c.b)
		if (got > 0) != (c.sign > 0) || (got < 0) != (c.sign < 0) {
			t.Errorf("compare(%s, %s) = %d", c.a, c.b, got)
		}
	}
}

func TestAgentUpdates(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	var rel *httptest.Server
	rel = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"tag_name": "v1.1.0", "assets": []map[string]string{{"name": "checksums-full.txt", "browser_download_url": rel.URL + "/dl/v1.1.0/checksums-full.txt"}}},
				{"tag_name": "v1.2.0-beta.1", "prerelease": true, "assets": []map[string]string{{"name": "checksums-full.txt", "browser_download_url": rel.URL + "/dl/v1.2.0-beta.1/checksums-full.txt"}}},
				{"tag_name": "v1.3.0", "draft": true},
			})
		case "/dl/v1.1.0/checksums-full.txt", "/dl/v1.2.0-beta.1/checksums-full.txt":
			_, _ = w.Write([]byte(strings.Repeat("a", 64) + "  lanscape-agent_x_linux_amd64.tar.gz\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer rel.Close()
	s.rel.client = rel.Client()

	up := &updateConn{fakeConn: fakeConn{id: "a1"}}
	s.hub.Connected(AgentState{ID: "a1", Name: "nas", Kind: "full", Version: "1.0.0", Caps: []string{proto.MsgUpdate}}, up)
	s.hub.Connected(AgentState{ID: "a2", Name: "old", Kind: "full", Version: "1.0.0"}, &fakeConn{id: "a2"})
	s.hub.Connected(AgentState{ID: "a3", Name: "new", Kind: "full", Version: "1.1.0", Caps: []string{proto.MsgUpdate}}, &fakeConn{id: "a3"})

	if code := do(t, c, "POST", ts.URL+"/api/v1/agents/a1/update", nil, nil); code != http.StatusBadGateway {
		t.Errorf("update without a channel: %d", code)
	}
	st := DefaultSettings
	st.AgentUpdateChannel, st.AgentReleasesURL = "nightly", rel.URL+"/releases"
	if code := do(t, c, "PUT", ts.URL+"/api/v1/settings", st, nil); code != 400 {
		t.Errorf("unknown channel accepted: %d", code)
	}
	st.AgentUpdateChannel = "stable"
	do(t, c, "PUT", ts.URL+"/api/v1/settings", st, nil)

	var u AgentUpdates
	do(t, c, "GET", ts.URL+"/api/v1/updates/agents", nil, &u)
	if u.Latest == nil || u.Latest.Version != "1.1.0" || u.Latest.BaseURL != rel.URL+"/dl/v1.1.0" || u.Error != "" {
		t.Fatalf("latest: %+v %s", u.Latest, u.Error)
	}
	byName := map[string]AgentUpdateView{}
	for _, a := range u.Agents {
		byName[a.Name] = a
	}
	if !byName["nas"].Outdated || !byName["nas"].CanUpdate || byName["old"].CanUpdate || byName["new"].Outdated {
		t.Errorf("agents: %+v", u.Agents)
	}

	if n := s.updateOutdated(context.Background()); n != 1 || len(up.got) != 1 || up.got[0].Version != "1.1.0" ||
		up.got[0].Checksums["lanscape-agent_x_linux_amd64.tar.gz"] == "" {
		t.Fatalf("update all: %d %+v", n, up.got)
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/agents/a2/update", nil, nil); code != http.StatusBadGateway {
		t.Errorf("agent without the update action: %d", code)
	}

	st.AgentUpdateChannel = "beta"
	do(t, c, "PUT", ts.URL+"/api/v1/settings", st, nil)
	do(t, c, "GET", ts.URL+"/api/v1/updates/agents", nil, &u)
	if u.Latest == nil || u.Latest.Version != "1.2.0-beta.1" {
		t.Errorf("beta: %+v", u.Latest)
	}
}
