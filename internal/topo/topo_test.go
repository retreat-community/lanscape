package topo

import (
	"testing"

	"github.com/retreat-community/lanscape/internal/netio"
)

func iface(name, kind string, vlan, speed int, ip string, prefix int) netio.Iface {
	return netio.Iface{Name: name, Kind: kind, VLAN: vlan, Speed: speed, Up: true, Carrier: true, MTU: 1500,
		Addrs: []netio.Addr{{IP: ip, Prefix: prefix}}}
}

func refNodes() []Node {
	return []Node{
		{ID: "n1", Name: "n1", Ifaces: []netio.Iface{iface("eth0", "physical", 0, 1000, "10.10.1.1", 24), iface("eth1.301", "vlan", 301, 1000, "10.31.0.1", 24)}},
		{ID: "n3", Name: "n3", Ifaces: []netio.Iface{iface("eth0", "physical", 0, 1000, "10.10.1.3", 24), iface("eth1.301", "vlan", 301, 1000, "10.31.0.3", 24)}},
		{ID: "pod", Name: "pod", Ifaces: []netio.Iface{iface("eth0", "macvlan", 0, 0, "10.31.0.13", 24)}},
		{ID: "lo", Name: "lo", Ifaces: []netio.Iface{{Name: "lo", Kind: "loopback", Up: true, Addrs: []netio.Addr{{IP: "127.0.0.1", Prefix: 8}}}}},
	}
}

func TestBuildSegments(t *testing.T) {
	segs := BuildSegments(refNodes(), map[string]int{"10.31.0.0/16": 100})
	if len(segs) != 2 {
		t.Fatalf("want 2 segments, got %+v", segs)
	}
	if segs[0].ID != "10.10.1.0/24" || segs[0].Manual || len(segs[0].Members) != 2 {
		t.Errorf("segment A: %+v", segs[0])
	}
	c := segs[1]
	if c.ID != "10.31.0.0/24 vlan 301" || c.VLAN != 301 || !c.Manual || c.ExpectedMbps != 100 || len(c.Members) != 3 {
		t.Errorf("segment C: %+v", c)
	}
	// a second VLAN with the same subnet forms its own segment and an anomaly
	nodes := append(refNodes(), Node{ID: "x", Ifaces: []netio.Iface{iface("eth9", "vlan", 999, 0, "10.31.0.99", 24)}})
	segs = BuildSegments(nodes, nil)
	if len(segs) != 3 {
		t.Fatalf("want 3 segments: %+v", segs)
	}
	an := Anomalies(nodes, segs, nil, nil)
	found := false
	for _, a := range an {
		found = found || a.Kind == ASubnetVLANs
	}
	if !found {
		t.Errorf("no subnet/VLAN anomaly: %+v", an)
	}
}

func TestExpected(t *testing.T) {
	seg := &Segment{}
	a, b := Member{Speed: 2500}, Member{Speed: 1000}
	if ExpectedMbps(seg, a, b, nil) != 1000 {
		t.Error("min of link speeds")
	}
	if ExpectedMbps(seg, a, Member{}, nil) != 0 {
		t.Error("unknown speed")
	}
	if ExpectedMbps(seg, a, Member{}, func(Member) int { return 2500 }) != 2500 {
		t.Error("inherited speed")
	}
	seg.Manual, seg.ExpectedMbps = true, 900
	if ExpectedMbps(seg, a, b, nil) != 900 {
		t.Error("manual wins")
	}
}

func tp(bps uint64) *Throughput { return &Throughput{Status: "ok", BPS: bps, PathOK: true} }

func TestJudge(t *testing.T) {
	cases := []struct {
		bps  uint64
		exp  int
		cpu  int
		want string
	}{
		{950e6, 1000, 0, Green}, {700e6, 1000, 0, Yellow}, {300e6, 1000, 0, Red},
		{300e6, 1000, 950, Purple}, {300e6, 0, 0, None},
	}
	for _, c := range cases {
		th := tp(c.bps)
		th.CPUPeer = c.cpu
		p := PathResult{TCPN: th, ExpectedMbps: c.exp, Ping: &Probe{Status: "ok", Sent: 10, Recv: 10}}
		Judge(&p, 0)
		if p.Verdict != c.want {
			t.Errorf("%d/%d cpu %d: got %s want %s", c.bps, c.exp, c.cpu, p.Verdict, c.want)
		}
	}
	p := PathResult{TCP1: &Throughput{Status: "counter_mismatch", BPS: 1e9}, ExpectedMbps: 1000}
	Judge(&p, 0)
	if p.Verdict != Red || !p.Mismatch {
		t.Errorf("mismatch: %+v", p)
	}
	p = PathResult{Ping: &Probe{Status: "ok", Sent: 100, Recv: 90, RTTAvgUS: 50000}}
	Judge(&p, 20000)
	if !p.LossHigh || !p.RTTHigh {
		t.Errorf("loss/rtt flags: %+v", p)
	}
}

func TestProblems(t *testing.T) {
	segs := BuildSegments(refNodes(), nil)
	okp := &Probe{Status: "ok", Sent: 5, Recv: 5}
	bad := &Probe{Status: "unreachable", Sent: 5}
	mk := func(seg int, src, srcIf, dst, dstIf string, ping, echo, mtu *Probe) PathResult {
		return PathResult{Seg: seg, Src: src, SrcIf: srcIf, Dst: dst, DstIf: dstIf, DstIP: "x", Ping: ping, Echo: echo, MTU: mtu}
	}
	paths := []PathResult{
		mk(0, "n1", "eth0", "n3", "eth0", okp, &Probe{Status: "refused"}, okp),
		mk(0, "n3", "eth0", "n1", "eth0", okp, okp, &Probe{Status: "unreachable", Size: 1500}),
		mk(1, "n3", "eth1.301", "pod", "eth0", bad, bad, bad),
		mk(1, "pod", "eth0", "n3", "eth1.301", bad, bad, bad),
		mk(1, "n1", "eth1.301", "pod", "eth0", okp, okp, okp),
		mk(1, "pod", "eth0", "n1", "eth1.301", okp, okp, okp),
		mk(1, "n1", "eth1.301", "n3", "eth1.301", okp, okp, okp),
		mk(1, "n3", "eth1.301", "n1", "eth1.301", okp, okp, okp),
	}
	for i := range paths {
		Judge(&paths[i], 0)
	}
	got := map[string]string{}
	for _, pr := range Problems(segs, paths, 47700) {
		got[pr.Kind] = pr.Src + ">" + pr.Dst
	}
	want := map[string]string{PTCPIntercepted: "n1>n3", PMTU: "n3>n1", PMacvlan: "n3>pod"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q want %q (all %v)", k, got[k], v, got)
		}
	}
	if _, ok := got[PUnreachable]; ok {
		t.Errorf("macvlan pair must not be reported as unreachable: %v", got)
	}
}

func TestIPAMAndAnomalies(t *testing.T) {
	nodes := refNodes()
	nodes[0].Ifaces[0].MAC = "aa:bb:cc:00:00:01"
	nodes[1].Ifaces[0].MAC = "aa:bb:cc:00:00:01"
	nodes[1].Ifaces = append(nodes[1].Ifaces, iface("vip", "physical", 0, 0, "10.10.1.1", 32))
	nodes[1].Ifaces = append(nodes[1].Ifaces, netio.Iface{Name: "eth5", Kind: "physical", Up: true})
	segs := BuildSegments(nodes, nil)
	ipam := BuildIPAM(segs, nodes, []Extra{{IP: "10.10.1.50", MAC: "de:ad:be:ef:00:01", Name: "printer", Source: "dhcp"}}, nil)
	if ipam[0].Size != 254 || ipam[0].Used != 3 || ipam[0].Free != 251 {
		t.Errorf("ipam: %+v", ipam[0])
	}
	an := Anomalies(nodes, segs, map[string]bool{}, []Extra{{IP: "10.10.1.50", MAC: "de:ad:be:ef:00:01", Source: "dhcp"}})
	kinds := map[string]bool{}
	for _, a := range an {
		kinds[a.Kind] = true
	}
	for _, k := range []string{ADuplicateIP, ADuplicateMAC, ANoCarrier, ANewMAC} {
		if !kinds[k] {
			t.Errorf("missing anomaly %s in %+v", k, an)
		}
	}
}

// TestAcceptanceProblems covers the artificial problems of §18 that the e2e topology does not
// build: a VLAN switched off, a 100M port where 1G is expected, and the path/loss/RTT/CPU findings.
func TestAcceptanceProblems(t *testing.T) {
	gig := func(n string) Member { return Member{Node: n, Iface: "eth0", Speed: 1000, Kind: "physical"} }
	seg := Segment{ID: "s", CIDR: "10.0.0.0/24", VLAN: 300, Members: []Member{gig("a"), gig("b"), gig("c"),
		{Node: "d", Iface: "eth0", Speed: 100, Kind: "physical"}}}
	okPing := &Probe{Status: "ok", Sent: 20, Recv: 20, RTTAvgUS: 300}
	tcp := func(mbps uint64, cpu int) *Throughput {
		return &Throughput{Status: "ok", BPS: mbps * 1e6, CPULocal: cpu, PathOK: true}
	}
	path := func(src, dst string) PathResult {
		return PathResult{Src: src, SrcIf: "eth0", Dst: dst, DstIf: "eth0", DstIP: "10.0.0.9"}
	}
	var paths []PathResult
	// VLAN 300 switched off on the switch port of b: nothing answers
	off := path("a", "b")
	off.Ping, off.Echo = &Probe{Status: "timeout", Sent: 3}, &Probe{Status: "timeout"}
	off.ExpectedMbps = 1000
	paths = append(paths, off)
	// d negotiated 100M on a gigabit segment: the path runs at a tenth of what a->c gets
	slow := path("a", "d")
	slow.Ping, slow.Echo, slow.MTU, slow.TCPN = okPing, &Probe{Status: "ok"}, &Probe{Status: "ok"}, tcp(94, 100)
	slow.ExpectedMbps = 1000 // the uplink of d should be gigabit (manual expectation of the segment)
	paths = append(paths, slow)
	// the route to c leaves through another interface
	wrong := path("b", "c")
	wrong.Ping, wrong.TCPN = okPing, &Throughput{Status: "route_mismatch"}
	wrong.ExpectedMbps = 1000
	paths = append(paths, wrong)
	// lossy, slow to answer and limited by the CPU of a router
	busy := path("c", "a")
	busy.Ping = &Probe{Status: "ok", Sent: 20, Recv: 17, RTTAvgUS: 40000}
	busy.Echo, busy.MTU, busy.TCPN = &Probe{Status: "ok"}, &Probe{Status: "ok"}, tcp(400, 990)
	busy.ExpectedMbps = 1000
	paths = append(paths, busy)
	for i := range paths {
		Judge(&paths[i], 20000)
	}
	if paths[1].Verdict != Red || paths[2].Verdict != Red || paths[3].Verdict != Purple {
		t.Errorf("verdicts: slow %s, mismatch %s, cpu %s", paths[1].Verdict, paths[2].Verdict, paths[3].Verdict)
	}
	got := map[string]string{}
	for _, pr := range Problems([]Segment{seg}, paths, 47700) {
		got[pr.Kind] += pr.Src + ">" + pr.Dst + " "
	}
	want := map[string]string{PUnreachable: "a>b ", PSlow: "a>d ", PPathMismatch: "b>c ", PLoss: "c>a ", PRTT: "c>a ",
		PCPUBound: "c>a ", PLinkSpeed: "d> "}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q (all %v)", k, got[k], v, got)
		}
	}
}
