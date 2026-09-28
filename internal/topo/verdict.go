package topo

import "fmt"

// Verdicts.
const (
	Green  = "green"
	Yellow = "yellow"
	Red    = "red"
	Purple = "purple" // limited by agent CPU
	None   = "none"   // no expected speed / no data
)

// Probe is a reachability/latency/MTU measurement.
type Probe struct {
	Status   string `json:"status"`
	Size     int    `json:"size,omitempty"`
	Sent     int    `json:"sent"`
	Recv     int    `json:"recv"`
	RTTMinUS uint32 `json:"rtt_min_us"`
	RTTAvgUS uint32 `json:"rtt_avg_us"`
	RTTP95US uint32 `json:"rtt_p95_us"`
	RTTMaxUS uint32 `json:"rtt_max_us"`
	JitterUS uint32 `json:"jitter_us"`
}

// Throughput is a TCP or UDP measurement.
type Throughput struct {
	Status     string  `json:"status"`
	Streams    int     `json:"streams"`
	BPS        uint64  `json:"bps"`
	BPSReverse uint64  `json:"bps_reverse,omitempty"`
	CPULocal   int     `json:"cpu_local"`
	CPUPeer    int     `json:"cpu_peer"`
	PathOK     bool    `json:"path_ok"`
	Verified   bool    `json:"verified"`
	Sent       uint64  `json:"sent,omitempty"`
	Lost       uint64  `json:"lost,omitempty"`
	LossPct    float64 `json:"loss_pct,omitempty"`
	JitterUS   uint32  `json:"jitter_us,omitempty"`
	Message    string  `json:"message,omitempty"`
}

// PathResult is everything measured on one directed path.
type PathResult struct {
	Seg          int         `json:"seg"`
	SegID        string      `json:"seg_id"`
	Src          string      `json:"src"`
	SrcIf        string      `json:"src_if"`
	SrcIP        string      `json:"src_ip"`
	Dst          string      `json:"dst"`
	DstIf        string      `json:"dst_if"`
	DstIP        string      `json:"dst_ip"`
	Ping         *Probe      `json:"ping"`
	Echo         *Probe      `json:"echo"`
	MTU          *Probe      `json:"mtu"`
	Jumbo        *Probe      `json:"jumbo,omitempty"`
	TCP1         *Throughput `json:"tcp1"`
	TCPN         *Throughput `json:"tcpn"`
	UDP          *Throughput `json:"udp,omitempty"`
	Bidir        *Throughput `json:"bidir,omitempty"`
	RouteDev     string      `json:"route_dev,omitempty"`
	ExpectedMbps int         `json:"expected_mbps"`
	BestBPS      uint64      `json:"best_bps"`
	Verdict      string      `json:"verdict"`
	Mismatch     bool        `json:"mismatch"`
	MTUOK        bool        `json:"mtu_ok"`
	LossHigh     bool        `json:"loss_high"`
	RTTHigh      bool        `json:"rtt_high"`
}

func ok(s string) bool { return s == "ok" }

// Judge fills BestBPS, Verdict and the separate MTU/loss/RTT flags (§6.4).
func Judge(p *PathResult, rttWarnUS uint32) {
	cpu := false
	measured := false
	p.BestBPS = 0
	for _, t := range []*Throughput{p.TCP1, p.TCPN} {
		if t == nil {
			continue
		}
		if t.Status == "route_mismatch" || t.Status == "counter_mismatch" {
			p.Mismatch = true
		}
		if ok(t.Status) {
			measured = true
			p.BestBPS = max(p.BestBPS, t.BPS)
		}
		if (ok(t.Status) || t.Status == "counter_mismatch") && (t.CPULocal >= 900 || t.CPUPeer >= 900) {
			cpu = true
		}
	}
	for _, pr := range []*Probe{p.Ping, p.Echo} {
		if pr != nil && pr.Status == "route_mismatch" {
			p.Mismatch = true
		}
	}
	p.MTUOK = p.MTU == nil || ok(p.MTU.Status)
	if p.Jumbo != nil && !ok(p.Jumbo.Status) {
		p.MTUOK = false
	}
	if p.Ping != nil && p.Ping.Sent > 0 && p.Ping.Recv > 0 {
		p.LossHigh = (p.Ping.Sent-p.Ping.Recv)*1000/p.Ping.Sent > 5
		p.RTTHigh = rttWarnUS > 0 && p.Ping.RTTAvgUS > rttWarnUS
	}
	switch {
	case p.Mismatch:
		p.Verdict = Red
	case !measured:
		if p.TCP1 == nil && p.TCPN == nil && p.Ping != nil && ok(p.Ping.Status) {
			p.Verdict = None // reachability-only run
		} else {
			p.Verdict = Red
		}
	case p.ExpectedMbps == 0:
		p.Verdict = None
	default:
		pct := p.BestBPS / 10000 / uint64(p.ExpectedMbps)
		switch {
		case pct >= 85:
			p.Verdict = Green
		case cpu:
			p.Verdict = Purple
		case pct >= 50:
			p.Verdict = Yellow
		default:
			p.Verdict = Red
		}
	}
}

// Problem kinds.
const (
	PTCPIntercepted = "tcp_intercepted"
	PMTU            = "mtu"
	PSlow           = "slow"
	PPathMismatch   = "path_mismatch"
	PUnreachable    = "unreachable"
	PMacvlan        = "macvlan"
	PLoss           = "loss"
	PCPUBound       = "cpu_bound"
	PRTT            = "rtt"
)

// Problem is an explained finding for a path.
type Problem struct {
	Kind   string `json:"kind"`
	Seg    int    `json:"seg"`
	SegID  string `json:"seg_id"`
	Src    string `json:"src"`
	SrcIf  string `json:"src_if"`
	Dst    string `json:"dst"`
	DstIf  string `json:"dst_if"`
	Detail string `json:"detail"`
}

// Problems explains known traps on judged paths (§6.2, §18).
func Problems(segs []Segment, paths []PathResult, port int) []Problem {
	out := []Problem{}
	kindOf := func(seg int, node, iface string) string {
		for _, m := range segs[seg].Members {
			if m.Node == node && m.Iface == iface {
				return m.Kind
			}
		}
		return ""
	}
	find := func(seg int, src, srcIf, dst, dstIf string) *PathResult {
		for i := range paths {
			q := &paths[i]
			if q.Seg == seg && q.Src == src && q.SrcIf == srcIf && q.Dst == dst && q.DstIf == dstIf {
				return q
			}
		}
		return nil
	}
	reachable := func(seg int, node, iface, except string) bool {
		for _, q := range paths {
			if q.Seg != seg || q.Ping == nil || !ok(q.Ping.Status) {
				continue
			}
			if (q.Src == node && q.SrcIf == iface && q.Dst != except) || (q.Dst == node && q.DstIf == iface && q.Src != except) {
				return true
			}
		}
		return false
	}
	for _, p := range paths {
		add := func(kind, detail string) {
			out = append(out, Problem{Kind: kind, Seg: p.Seg, SegID: p.SegID, Src: p.Src, SrcIf: p.SrcIf,
				Dst: p.Dst, DstIf: p.DstIf, Detail: detail})
		}
		pingOK := p.Ping != nil && ok(p.Ping.Status)
		echoOK := p.Echo != nil && ok(p.Echo.Status)
		if p.Mismatch {
			if p.RouteDev != "" {
				add(PPathMismatch, fmt.Sprintf("route to %s leaves via %s, not %s", p.DstIP, p.RouteDev, p.SrcIf))
			} else {
				add(PPathMismatch, fmt.Sprintf("interface counters of %s did not grow with the test traffic", p.SrcIf))
			}
			continue
		}
		if p.Ping != nil && !pingOK && !echoOK && p.Ping.Status != "offline" {
			mvA := kindOf(p.Seg, p.Src, p.SrcIf) == "macvlan"
			mvB := kindOf(p.Seg, p.Dst, p.DstIf) == "macvlan"
			rv := find(p.Seg, p.Dst, p.DstIf, p.Src, p.SrcIf)
			if mvA != mvB && rv != nil && (rv.Ping == nil || !ok(rv.Ping.Status)) &&
				reachable(p.Seg, p.Src, p.SrcIf, p.Dst) && reachable(p.Seg, p.Dst, p.DstIf, p.Src) {
				if mvB {
					add(PMacvlan, fmt.Sprintf("%s (%s) cannot reach macvlan child %s (%s); add a macvlan sibling "+
						"on the host parent interface and move the host address there", p.Src, p.SrcIf, p.Dst, p.DstIP))
				}
				continue
			}
			add(PUnreachable, fmt.Sprintf("no ICMP or TCP reply from %s", p.DstIP))
			continue
		}
		if pingOK && p.Echo != nil && !echoOK && p.Echo.Status != "offline" {
			add(PTCPIntercepted, fmt.Sprintf("ICMP to %s works but TCP to port %d fails (%s): transparent proxy or filter",
				p.DstIP, port, p.Echo.Status))
		}
		if pingOK && p.MTU != nil && !ok(p.MTU.Status) && p.MTU.Status != "offline" {
			add(PMTU, fmt.Sprintf("%d-byte packets with DF do not pass to %s (%s)", p.MTU.Size, p.DstIP, p.MTU.Status))
		} else if pingOK && p.Jumbo != nil && !ok(p.Jumbo.Status) && p.Jumbo.Status != "offline" {
			add(PMTU, fmt.Sprintf("jumbo frames of %d bytes do not pass to %s", p.Jumbo.Size, p.DstIP))
		}
		if p.LossHigh {
			add(PLoss, fmt.Sprintf("%d of %d ICMP packets lost", p.Ping.Sent-p.Ping.Recv, p.Ping.Sent))
		}
		if p.RTTHigh {
			add(PRTT, fmt.Sprintf("average RTT %.1f ms is above the threshold", float64(p.Ping.RTTAvgUS)/1000))
		}
		switch {
		case p.Verdict == Purple:
			add(PCPUBound, fmt.Sprintf("%d Mbit/s of %d expected, limited by agent CPU", p.BestBPS/1_000_000, p.ExpectedMbps))
		case p.BestBPS > 0 && (p.Verdict == Yellow || p.Verdict == Red):
			add(PSlow, fmt.Sprintf("%d Mbit/s of %d expected", p.BestBPS/1_000_000, p.ExpectedMbps))
		}
	}
	return out
}
