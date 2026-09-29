package server

import (
	"context"
	"testing"
	"time"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/netio"
	"github.com/retreat-community/lanscape/internal/store"
)

func TestMapExternalZone(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	s.hub.Connected(AgentState{ID: "rt", Name: "router", Kind: "full", Inv: agent.Inventory{Env: agent.Env{Kind: "openwrt"},
		Ifaces: []netio.Iface{{Name: "br-lan", Kind: "bridge", Up: true, Addrs: []netio.Addr{{IP: "192.168.1.1", Prefix: 24}}},
			{Name: "wg0", Kind: "wireguard", Up: true, Addrs: []netio.Addr{{IP: "10.8.0.1", Prefix: 24}}}},
		Routes: []agent.Route{{Dst: "default", Gateway: "100.70.0.1", Dev: "wan"}}}}, &fakeConn{id: "rt"})
	s.hub.Connected(AgentState{ID: "vps", Name: "vps", Kind: "full", Inv: agent.Inventory{
		Ifaces: []netio.Iface{{Name: "eth0", Kind: "physical", Up: true, Addrs: []netio.Addr{{IP: "203.0.113.50", Prefix: 24}}}}}},
		&fakeConn{id: "vps"})
	if _, err := s.store.AddInternetCheck(context.Background(), store.InternetCheck{TS: time.Now().UnixMilli(), Point: "rt", Dev: "wan",
		OK: true, PublicIP: "198.51.100.7"}); err != nil {
		t.Fatal(err)
	}
	var g MapGraph
	do(t, c, "GET", ts.URL+"/api/v1/map", nil, &g)
	byID := map[string]MapNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	if _, ok := byID["zone:external"]; !ok {
		t.Fatalf("no external zone: %+v", g.Nodes)
	}
	if byID["ext:internet"].Parent != "zone:external" || byID["dev:vps"].Parent != "zone:external" ||
		byID["vpn:rt:wg0"].Parent != "zone:external" || byID["dev:rt"].Parent != "" {
		t.Errorf("zone members: internet %+v vps %+v vpn %+v router %+v", byID["ext:internet"], byID["dev:vps"], byID["vpn:rt:wg0"], byID["dev:rt"])
	}
	found := false
	for _, e := range g.Edges {
		if e.Source == "dev:rt" && e.Target == "ext:internet" && e.Label == "wan 198.51.100.7" && e.Verdict == "green" {
			found = true
		}
	}
	if !found {
		t.Errorf("no exit edge: %+v", g.Edges)
	}
}
