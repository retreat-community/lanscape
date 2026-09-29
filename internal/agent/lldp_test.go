package agent

import "testing"

func TestParseLLDP(t *testing.T) {
	out := `{"lldp":[{"interface":[
	 {"name":"eth0","via":"LLDP","rid":"1","age":"0 day, 01:00:00",
	  "chassis":[{"id":[{"type":"mac","value":"00:11:22:33:44:55"}],"name":[{"value":"core-sw"}],
	    "descr":[{"value":"TP-Link JetStream"}],"mgmt-ip":[{"value":"192.168.1.2"}]}],
	  "port":[{"id":[{"type":"ifname","value":"1/0/7"}],"descr":[{"value":"prx0 uplink"}]}]},
	 {"name":"eno2","via":"CDPv2","chassis":[{"id":[{"type":"local","value":"sw2"}]}],"port":[{"id":[{"value":"Gi0/3"}]}]}
	]}]}`
	p := ParseLLDP([]byte(out))
	if len(p) != 2 {
		t.Fatalf("%+v", p)
	}
	if p[0].Iface != "eno2" || p[0].Name != "sw2" || p[0].Port != "Gi0/3" || p[0].Proto != "CDPv2" {
		t.Errorf("cdp: %+v", p[0])
	}
	if p[1].Name != "core-sw" || p[1].MgmtIP != "192.168.1.2" || p[1].Port != "1/0/7" || p[1].PortDescr != "prx0 uplink" {
		t.Errorf("lldp: %+v", p[1])
	}
	if ParseLLDP([]byte("not json")) != nil {
		t.Error("garbage parsed")
	}
}
