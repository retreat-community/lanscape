package discovery

import (
	"context"
	"net"
	"strconv"
	"testing"
)

func TestScan(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n"))
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	items, err := Scan(context.Background(), ScanRequest{CIDRs: []string{"127.0.0.1"}, Ports: []int{port, 1}, RatePerS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].IPs[0] != "127.0.0.1" || len(items[0].Ports) != 1 ||
		items[0].Labels["banner_"+strconv.Itoa(port)] != "SSH-2.0-OpenSSH_9.6" {
		t.Errorf("scan: %+v", items)
	}
	for _, bad := range []ScanRequest{{CIDRs: []string{"10.0.0.0/8"}}, {CIDRs: []string{"x"}}, {CIDRs: []string{"::1/128"}},
		{CIDRs: []string{"127.0.0.1"}, Ports: []int{70000}}} {
		if _, err := Scan(context.Background(), bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if typeFromPorts([]Port{{Port: 631}}) != "printer" || typeFromPorts([]Port{{Port: 5000}, {Port: 5001}}) != "nas" {
		t.Error("types")
	}
}

func TestScanAllowed(t *testing.T) {
	allow, err := ParsePrefixes([]string{"192.168.1.0/24", " 10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	for req, ok := range map[string]bool{"192.168.1.0/24": true, "192.168.1.128/25": true, "192.168.1.7": true, "10.20.0.0/16": true,
		"192.168.0.0/16": false, "192.168.2.1": false, "0.0.0.0/0": false, "bogus": false} {
		if err := ScanAllowed(allow, []string{req}); (err == nil) != ok {
			t.Errorf("%s: %v", req, err)
		}
	}
	if ScanAllowed(nil, []string{"192.168.1.0/24"}) == nil {
		t.Error("scanning allowed without an allow list")
	}
	if _, err := ParsePrefixes([]string{"x"}); err == nil {
		t.Error("bad prefix accepted")
	}
}
