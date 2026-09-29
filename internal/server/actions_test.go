package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/store"
)

// actionConn records action requests.
type actionConn struct {
	fakeConn
	mu   sync.Mutex
	got  []proto.ActionMsg
	fail bool
}

func (c *actionConn) Request(_ context.Context, typ string, data any) (json.RawMessage, error) {
	if typ != proto.MsgAction {
		return nil, nil
	}
	m := data.(proto.ActionMsg)
	c.mu.Lock()
	c.got = append(c.got, m)
	c.mu.Unlock()
	if c.fail {
		return nil, errors.New("action \"restart\" is not allowed on this agent")
	}
	return json.Marshal(proto.ActionResultMsg{Detail: "done " + m.Action})
}

func TestActions(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)

	lan := &actionConn{fakeConn: fakeConn{id: "lan"}}
	other := &actionConn{fakeConn: fakeConn{id: "other"}}
	docker := &actionConn{fakeConn: fakeConn{id: "nas"}}
	s.hub.Connected(AgentState{ID: "lan", Name: "n1", Kind: "full", Caps: []string{"action:wol"}, Inv: node("10.31.0.1", "192.168.1.5")}, lan)
	s.hub.Connected(AgentState{ID: "other", Name: "n2", Kind: "full", Caps: []string{"action:wol"}, Inv: node("10.31.0.2", "10.10.1.2")}, other)
	s.hub.Connected(AgentState{ID: "nas", Name: "nas", Kind: "full", Caps: []string{"action:wol", "action:restart"}, Inv: node("10.31.0.3", "10.10.1.3")}, docker)

	var res struct {
		OK      bool           `json:"ok"`
		Results []ActionResult `json:"results"`
	}
	if code := do(t, c, "POST", ts.URL+"/api/v1/actions/wol", map[string]string{"mac": "nope"}, nil); code != 400 {
		t.Errorf("bad mac: %d", code)
	}
	// only the agent on the device's subnet sends the packet
	if code := do(t, c, "POST", ts.URL+"/api/v1/actions/wol", map[string]string{"mac": "AA-BB-CC-00-11-22", "ip": "192.168.1.40"}, &res); code != 200 || !res.OK {
		t.Fatalf("wol: %d %+v", code, res)
	}
	if len(lan.got) != 1 || lan.got[0].MAC != "aa:bb:cc:00:11:22" || len(other.got) != 0 {
		t.Errorf("wol requests: lan=%+v other=%+v", lan.got, other.got)
	}
	// unknown subnet: every agent that allows it tries
	do(t, c, "POST", ts.URL+"/api/v1/actions/wol", map[string]string{"mac": "aa:bb:cc:00:11:22"}, &res)
	if len(res.Results) != 3 {
		t.Errorf("fallback: %+v", res.Results)
	}

	rep := discovery.Report{At: time.Now().UnixMilli(), Sources: []discovery.SourceReport{{Source: discovery.SourceDocker, Items: []discovery.Item{
		{Key: "container/gitea", Kind: discovery.KindContainer, Name: "gitea"}}}, {Source: discovery.SourceSockets, Items: []discovery.Item{
		{Key: "proc/sshd/22", Kind: discovery.KindSocket, Name: "sshd"}}}}}
	if err := s.ingestDiscovery(context.Background(), "nas", rep); err != nil {
		t.Fatal(err)
	}
	restart := func(agentID, source, key string) int {
		return do(t, c, "POST", ts.URL+"/api/v1/actions/restart", map[string]string{"agent": agentID, "source": source, "key": key}, &res)
	}
	if code := restart("nas", "docker", "container/missing"); code != 404 {
		t.Errorf("missing object: %d", code)
	}
	if code := restart("nas", "sockets", "proc/sshd/22"); code != 400 {
		t.Errorf("socket restart: %d", code)
	}
	if code := restart("nas", "docker", "container/gitea"); code != 200 || !res.OK || docker.got[len(docker.got)-1].Key != "container/gitea" {
		t.Errorf("restart: %d %+v", code, res)
	}
	docker.fail = true
	if code := restart("nas", "docker", "container/gitea"); code != 200 || res.OK {
		t.Errorf("failed restart reported ok: %+v", res)
	}

	var audit []store.AuditEntry
	do(t, c, "GET", ts.URL+"/api/v1/audit", nil, &audit)
	actions := map[string]int{}
	for _, e := range audit {
		actions[e.Action+" "+e.Result]++
	}
	if actions["action.wol ok"] != 2 || actions["action.restart ok"] != 1 || actions["action.restart error"] != 1 {
		t.Errorf("audit: %v", actions)
	}

	// viewers cannot run actions
	do(t, c, "POST", ts.URL+"/api/v1/users", map[string]string{"username": "viewer", "password": "viewer-password-1", "role": "viewer"}, nil)
	vc := client(t)
	do(t, vc, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "viewer", Password: "viewer-password-1"}, nil)
	if code := do(t, vc, "POST", ts.URL+"/api/v1/actions/wol", map[string]string{"mac": "aa:bb:cc:00:11:22"}, nil); code != 403 {
		t.Errorf("viewer wol: %d", code)
	}
}
