package server

import (
	"context"
	"testing"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/netio"
	"github.com/retreat-community/lanscape/internal/store"
)

func TestHardwareWidgets(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	s.hub.Connected(AgentState{ID: "nas", Name: "nas", Kind: "full"}, &fakeConn{id: "nas"})
	rep := discovery.Report{Sources: []discovery.SourceReport{{Source: discovery.SourceHost, Items: []discovery.Item{
		{Key: "ups/eaton", Kind: discovery.KindUPS, Name: "eaton", State: "online", Labels: map[string]string{"charge": "100"}},
		{Key: "disk/S1", Kind: discovery.KindDisk, Name: "/dev/sda", State: "passed"},
		{Key: "power/ipmi", Kind: discovery.KindPower, Name: "BMC", State: "on", Labels: map[string]string{"watts": "187"}},
		{Key: "power/rack", Kind: discovery.KindPower, Name: "rack", State: "on", Labels: map[string]string{"watts": "64.5"}},
	}}}}
	if err := s.ingestDiscovery(context.Background(), "nas", rep); err != nil {
		t.Fatal(err)
	}
	var d Dashboard
	do(t, c, "GET", ts.URL+"/api/v1/dashboard", nil, &d)
	if len(d.UPS) != 1 || len(d.Storage) != 1 || len(d.Power) != 2 || d.Power[0].Agent != "nas" {
		t.Fatalf("widgets: ups %+v storage %+v power %+v", d.UPS, d.Storage, d.Power)
	}
}

func TestTileMetric(t *testing.T) {
	agents := map[string]AgentState{"nas": {ID: "nas", Inv: agent.Inventory{Resources: agent.Resources{Disks: []agent.Disk{
		{Mount: "/", Total: 32 << 30, Used: 30 << 30}, {Mount: "/volume1", Total: 8 << 40, Used: 6 << 40}}}}}}
	nas := store.Service{AppID: "truenas", Addresses: []store.Address{{Type: "hostport", Value: "nas:443", Agent: "nas"}}}
	if m := tileMetric(&nas, agents); m != "2.0 TB free" {
		t.Errorf("nas metric %q", m)
	}
	web := store.Service{AppID: "grafana", Category: "monitoring", Addresses: nas.Addresses}
	if m := tileMetric(&web, agents); m != "" {
		t.Errorf("grafana has a storage metric %q", m)
	}
}

// TestHypervisorGrouping: when a hypervisor goes down, the services inside its guests are
// suppressed with the hypervisor as the cause, so they group under one incident (§9.2, §18).
func TestHypervisorGrouping(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	prx, vm := &fakeConn{id: "prx"}, &fakeConn{id: "vm"}
	s.hub.Connected(AgentState{ID: "prx", Name: "prx1", Hostname: "prx1", Kind: "full"}, prx)
	s.hub.Connected(AgentState{ID: "vm", Name: "nas", Kind: "full", Inv: agent.Inventory{Env: agent.Env{Kind: "vm"},
		Ifaces: []netio.Iface{{Name: "eth0", MAC: "BC:24:11:AA:BB:CC"}}}}, vm)
	if err := s.ingestDiscovery(ctx, "prx", discovery.Report{Sources: []discovery.SourceReport{{Source: discovery.SourceProxmox,
		Items: []discovery.Item{{Key: "qemu/100", Kind: discovery.KindVM, Name: "nas", State: "running",
			Labels: map[string]string{"node": "prx1"}, NICs: []discovery.NIC{{MAC: "bc:24:11:aa:bb:cc"}}}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ingestDiscovery(ctx, "vm", discovery.Report{Sources: []discovery.SourceReport{{Source: discovery.SourceDocker,
		Items: []discovery.Item{{Key: "container/jellyfin", Kind: discovery.KindContainer, Name: "jellyfin", State: "running",
			Image: "jellyfin/jellyfin"}}}}}); err != nil {
		t.Fatal(err)
	}
	cards, _ := s.cards(ctx)
	key := ""
	for _, c := range cards {
		if c.Name == "jellyfin" || (c.App != nil && c.App.ID == "jellyfin") {
			key = c.Key
		}
	}
	if key == "" {
		t.Fatalf("no card: %+v", cards)
	}
	id, err := s.store.SaveService(ctx, store.Service{Name: "Jellyfin", CardKey: key, Addresses: []store.Address{}})
	if err != nil {
		t.Fatal(err)
	}
	m := store.Monitor{ServiceID: id, Name: "jellyfin"}
	if _, why := s.uptime.parentDown(ctx, m); why != "" {
		t.Errorf("all online: %q", why)
	}
	s.hub.Disconnected("vm", vm)
	if _, why := s.uptime.parentDown(ctx, m); why != "agent nas is offline" {
		t.Errorf("guest offline: %q", why)
	}
	s.hub.Disconnected("prx", prx)
	if _, why := s.uptime.parentDown(ctx, m); why != "host prx1 is offline" {
		t.Errorf("hypervisor offline: %q", why)
	}
}
