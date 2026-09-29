package netio

import (
	"strings"
	"testing"
)

func TestParsers(t *testing.T) {
	a, err := ParseStat(strings.NewReader("cpu  100 0 100 700 100 0 0 0 0 0\n"))
	if err != nil || a.Total != 1000 || a.Busy != 200 {
		t.Fatalf("stat: %+v %v", a, err)
	}
	b, _ := ParseStat(strings.NewReader("cpu  400 0 400 1000 200 0 0 0 0 0\n"))
	if Permille(a, b) != 600 || Permille(b, a) != 0 {
		t.Error("permille")
	}
	if _, err := ParseStat(strings.NewReader("intr 1\n")); err == nil {
		t.Error("bad stat accepted")
	}
	v := ParseVLANConfig(strings.NewReader("VLAN Dev name | VLAN ID\nName-Type: VLAN_NAME_TYPE_RAW_PLUS_VID_NO_PAD\neth1.300 | 300 | eth1\n"))
	if v["eth1.300"] != [2]string{"300", "eth1"} || len(v) != 1 {
		t.Errorf("vlan: %v", v)
	}
	if !GlobMatch("wan*, tailscale*", "tailscale0") || GlobMatch("wan*", "lan") {
		t.Error("glob")
	}
	if NormalizeSpeed(0xffffffff) != 0 || NormalizeSpeed(2500) != 2500 {
		t.Error("speed")
	}
	ifs := Filter([]Iface{{Name: "lo", Kind: "loopback"}, {Name: "wan"}, {Name: "eth0"}}, []string{"wan*"})
	if len(ifs) != 1 || ifs[0].Name != "eth0" {
		t.Errorf("filter: %+v", ifs)
	}
}
