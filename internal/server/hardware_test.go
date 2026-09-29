package server

import (
	"context"
	"testing"

	"github.com/retreat-community/lanscape/internal/discovery"
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
