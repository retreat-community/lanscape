package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/netio"
	"github.com/retreat-community/lanscape/internal/proto"
)

func TestTrafficWidget(t *testing.T) {
	s, ts := newTestServer(t)
	c := client(t)
	do(t, c, "POST", ts.URL+"/api/v1/auth/login", credentials{Username: "admin", Password: "correct-horse-battery"}, nil)
	s.hub.Connected(AgentState{ID: "rt", Name: "router", Kind: "full"}, &fakeConn{id: "rt"})
	s.hub.SetInventory("rt", agent.Inventory{Env: agent.Env{Kind: "openwrt"},
		Ifaces: []netio.Iface{{Name: "wan"}, {Name: "br-lan"}, {Name: "wg0"}},
		Routes: []agent.Route{{Dst: "default", Gateway: "10.0.0.1", Dev: "wan"}}})
	s.hub.Connected(AgentState{ID: "nas", Name: "nas", Kind: "full"}, &fakeConn{id: "nas"})

	send := func(id string, m proto.TrafficMsg) {
		b, _ := json.Marshal(m)
		s.onAgentMessage(context.Background(), id, proto.Envelope{Type: proto.MsgTraffic, Data: b})
	}
	send("rt", proto.TrafficMsg{At: 1, Ifaces: []proto.IfaceTraffic{{Name: "wg0", RXbps: 5}, {Name: "br-lan", RXbps: 20e6, TXbps: 300e6},
		{Name: "wan", RXbps: 290e6, TXbps: 18e6}}})
	send("nas", proto.TrafficMsg{At: 1, Ifaces: []proto.IfaceTraffic{{Name: "eth0", RXbps: 1e6}}})

	var d Dashboard
	do(t, c, "GET", ts.URL+"/api/v1/dashboard", nil, &d)
	if len(d.Traffic) != 2 || d.Traffic[0].Role != "wan" || d.Traffic[0].RXbps != 290e6 || d.Traffic[1].Iface != "br-lan" {
		t.Fatalf("dashboard traffic: %+v", d.Traffic)
	}
	var all []IfaceTrafficView
	do(t, c, "GET", ts.URL+"/api/v1/traffic", nil, &all)
	if len(all) != 4 {
		t.Fatalf("all traffic: %+v", all)
	}
	mfs, err := s.metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mf := range mfs {
		if mf.GetName() != "lanscape_interface_throughput_bits_per_second" {
			continue
		}
		for _, m := range mf.GetMetric() {
			l := map[string]string{}
			for _, p := range m.GetLabel() {
				l[p.GetName()] = p.GetValue()
			}
			if l["agent"] == "rt" && l["iface"] == "wan" && l["direction"] == "rx" {
				found = m.GetGauge().GetValue() == 290e6
			}
		}
	}
	if !found {
		t.Error("no wan rx gauge of 290 Mbit/s")
	}
}
