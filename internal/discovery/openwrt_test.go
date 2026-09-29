package discovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseUCI(t *testing.T) {
	secs := ParseUCI(strings.NewReader(`
config dnsmasq
	option leasefile '/tmp/dhcp.leases'

config host 'nas'
	option name 'nas'
	option mac 'AA:BB:CC:00:00:01'
	option ip "192.168.1.10" # comment
	list tag 'x y'
`))
	if len(secs) != 2 || secs[1].Type != "host" || secs[1].Name != "nas" || secs[1].Options["ip"] != "192.168.1.10" ||
		secs[1].Lists["tag"][0] != "x y" {
		t.Errorf("uci: %+v", secs)
	}
}

func TestOpenWrtSource(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o750)
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("tmp/dhcp.leases", "1790000000 aa:bb:cc:00:00:02 192.168.1.20 phone 01:aa:bb:cc:00:00:02\n1790000000 aa:bb:cc:00:00:03 192.168.1.21 * *\n")
	write("etc/config/dhcp", "config dnsmasq\n\toption leasefile '/tmp/dhcp.leases'\n\nconfig host\n\toption name 'nas'\n\toption mac 'AA:BB:CC:00:00:01'\n\toption ip '192.168.1.10'\n\nconfig host\n\toption name 'phone-static'\n\toption mac 'aa:bb:cc:00:00:02'\n")
	write("etc/config/firewall", "config redirect\n\toption name 'nextcloud'\n\toption src 'wan'\n\toption src_dport '443'\n\toption dest_ip '192.168.1.10'\n\toption dest_port '443'\n\toption proto 'tcp'\n\nconfig redirect\n\toption name 'off'\n\toption enabled '0'\n")
	write("etc/config/sqm", "config queue 'eth1'\n\toption enabled '1'\n\toption interface 'wan'\n\toption download '95000'\n\toption upload '38000'\n")
	write("ubus/hostapd.phy0-ap0", `{"freq":5180,"clients":{"AA:BB:CC:00:00:02":{"signal":-55,"authorized":true,"rate":{"rx":866700,"tx":780000}}}}`)
	items, err := OpenWrt(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Item{}
	for _, it := range items {
		by[it.Key] = it
	}
	if l := by["lease/aa:bb:cc:00:00:02"]; l.Name != "phone" || l.Labels["static"] != "1" || l.IPs[0] != "192.168.1.20" {
		t.Errorf("lease: %+v", l)
	}
	if l := by["lease/aa:bb:cc:00:00:03"]; l.Name != "" {
		t.Errorf("anonymous lease: %+v", l)
	}
	if l := by["lease/aa:bb:cc:00:00:01"]; l.Name != "nas" || l.IPs[0] != "192.168.1.10" {
		t.Errorf("static only: %+v", l)
	}
	if f := by["fwd/nextcloud/443"]; f.Labels["dest_ip"] != "192.168.1.10" || f.Ports[0].Port != 443 {
		t.Errorf("forward: %+v", f)
	}
	if _, ok := by["fwd/off/"]; ok {
		t.Error("disabled redirect listed")
	}
	if q := by["sqm/wan"]; q.Labels["download_kbit"] != "95000" {
		t.Errorf("sqm: %+v", q)
	}
	if w := by["wifi/aa:bb:cc:00:00:02"]; w.Labels["band"] != "5" || w.Labels["signal"] != "-55" || w.Labels["iface"] != "phy0-ap0" {
		t.Errorf("wifi: %+v", w)
	}
}

func TestOpenWrtParts(t *testing.T) {
	p := ParseOpenWrtParts([]string{"leases", "wifi"})
	if p.NoLeases || p.NoWifi || !p.NoForwards || !p.NoSQM {
		t.Errorf("parts: %+v", p)
	}
	if (ParseOpenWrtParts(nil) != OpenWrtParts{}) {
		t.Error("empty list must select everything")
	}
}
