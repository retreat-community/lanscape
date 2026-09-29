package server

import (
	"context"
	"strings"
	"testing"

	"github.com/retreat-community/lanscape/internal/discovery"
)

func TestRouterLeasesInDevices(t *testing.T) {
	s, _ := newTestServer(t)
	s.hub.Connected(AgentState{ID: "rt", Name: "router", Kind: "full"}, &fakeConn{id: "rt"})
	err := s.ingestDiscovery(context.Background(), "rt", discovery.Report{Sources: []discovery.SourceReport{{Source: discovery.SourceOpenWrt,
		Items: []discovery.Item{
			{Key: "lease/aa:bb:cc:00:00:02", Kind: discovery.KindLease, Name: "phone", IPs: []string{"192.168.1.20"},
				Labels: map[string]string{"mac": "aa:bb:cc:00:00:02"}},
			{Key: "wifi/aa:bb:cc:00:00:02", Kind: discovery.KindWifiClient, Name: "aa:bb:cc:00:00:02",
				Labels: map[string]string{"mac": "aa:bb:cc:00:00:02", "band": "5", "signal": "-55"}},
			{Key: "fwd/nextcloud/443", Kind: discovery.KindPortForward, Name: "nextcloud", IPs: []string{"192.168.1.10"}},
		}}}})
	if err != nil {
		t.Fatal(err)
	}
	devs := s.devices(context.Background())
	if len(devs) != 1 || devs[0].Name != "phone" || devs[0].Wifi != "5 GHz, -55 dBm" || strings.Join(devs[0].Sources, ",") != "dhcp,wifi" {
		t.Errorf("devices: %+v", devs)
	}
	cards, _ := s.cards(context.Background())
	if len(cards) != 0 {
		t.Errorf("router items must not become service cards: %+v", cards)
	}
}
