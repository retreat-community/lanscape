package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/store"
	"github.com/retreat-community/lanscape/internal/testengine"
	"github.com/retreat-community/lanscape/internal/topo"
)

// Run kinds.
const (
	KindFull         = "full"
	KindReachability = "reachability"
	KindAggregate    = "aggregate"
)

// RunOptions select what a run measures.
type RunOptions struct {
	Kind        string   `json:"kind"`
	DurationMS  int      `json:"duration_ms,omitempty"`
	Streams     int      `json:"streams,omitempty"`
	PingCount   int      `json:"ping_count,omitempty"`
	UDP         bool     `json:"udp,omitempty"`
	UDPRateKbps int      `json:"udp_rate_kbps,omitempty"`
	Bidir       bool     `json:"bidir,omitempty"`
	Segments    []string `json:"segments,omitempty"`
	Nodes       []string `json:"nodes,omitempty"`
	// Involve keeps every node but measures only the paths to or from these nodes ("measure
	// the network to …")
	Involve []string `json:"involve,omitempty"`
	Actor   string   `json:"actor,omitempty"`
}

func (o *RunOptions) defaults(s Settings) {
	if o.Kind == "" {
		o.Kind = KindFull
	}
	if o.DurationMS <= 0 {
		o.DurationMS = s.DurationMS
	}
	if o.Streams <= 0 {
		o.Streams = s.Streams
	}
	if o.PingCount <= 0 {
		o.PingCount = s.PingCount
	}
	if o.UDPRateKbps <= 0 {
		o.UDPRateKbps = 100_000
	}
	o.DurationMS = min(max(o.DurationMS, 1000), 30000)
	o.Streams = min(max(o.Streams, 1), 16)
	o.PingCount = min(max(o.PingCount, 1), 100)
}

// NodeInfo is the node snapshot stored with a report.
type NodeInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Env    string `json:"env,omitempty"`
	Arch   string `json:"arch,omitempty"`
	Parent string `json:"parent,omitempty"`
}

// AggregateSeg is the result of an aggregate run for one segment.
type AggregateSeg struct {
	Seg      int               `json:"seg"`
	SegID    string            `json:"seg_id"`
	TotalBPS uint64            `json:"total_bps"`
	Pairs    int               `json:"pairs"`
	NodeOut  map[string]uint64 `json:"node_out_bps"`
	NodeIn   map[string]uint64 `json:"node_in_bps"`
}

// Report is the stored result of a run.
type Report struct {
	ID        int64             `json:"id"`
	Kind      string            `json:"kind"`
	Status    string            `json:"status"`
	Started   int64             `json:"started"`
	Finished  int64             `json:"finished"`
	Options   RunOptions        `json:"options"`
	Nodes     []NodeInfo        `json:"nodes"`
	Segments  []topo.Segment    `json:"segments"`
	Paths     []topo.PathResult `json:"paths"`
	Problems  []topo.Problem    `json:"problems"`
	Anomalies []topo.Anomaly    `json:"anomalies"`
	Aggregate []AggregateSeg    `json:"aggregate,omitempty"`
	Errors    []string          `json:"errors,omitempty"`
}

// Progress is published while a run is active.
type Progress struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
	ETAS  int    `json:"eta_s"`
	Step  string `json:"step,omitempty"`
}

type activeRun struct {
	rep    *Report
	cancel context.CancelFunc
	mu     sync.Mutex
	done   int
	total  int
	costMS []int
	step   string
}

// Runner executes one run at a time.
type Runner struct {
	s   *Server
	mu  sync.Mutex
	cur *activeRun
}

// ErrBusy is returned while another run is active.
var ErrBusy = errors.New("a run is already in progress")

// ErrNoPaths is returned when nothing can be measured.
var ErrNoPaths = errors.New("no paths: need at least two online agents in a common segment")

// Active returns the progress of the current run.
func (r *Runner) Active() (Progress, bool) {
	r.mu.Lock()
	cur := r.cur
	r.mu.Unlock()
	if cur == nil {
		return Progress{}, false
	}
	return cur.progress(), true
}

func (a *activeRun) progress() Progress {
	a.mu.Lock()
	defer a.mu.Unlock()
	eta := 0
	for _, c := range a.costMS[min(a.done, len(a.costMS)):] {
		eta += c
	}
	return Progress{ID: a.rep.ID, Kind: a.rep.Kind, Done: a.done, Total: a.total, ETAS: eta / 1000, Step: a.step}
}

// Cancel stops the active run; partial results are kept.
func (r *Runner) Cancel() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil {
		return false
	}
	r.cur.cancel()
	return true
}

type pathRef struct {
	idx      int
	seg      *topo.Segment
	a, b     topo.Member
	srcConn  Conn
	dstConn  Conn
	dstPort  int
	jumbo    int
	liteSide bool
}

// Plan builds the path list for options without running anything.
func (r *Runner) plan(ctx context.Context, opts RunOptions) ([]topo.Segment, []pathRef, []NodeInfo, error) {
	s := r.s
	manual := map[string]int{}
	cfgs, err := s.store.SegmentConfigs(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	for id, c := range cfgs {
		if c.ExpectedMbps > 0 {
			manual[id] = c.ExpectedMbps
		}
	}
	for k, v := range s.cfg.Expect {
		if _, ok := manual[k]; !ok {
			manual[k] = v
		}
	}
	nodes := s.hub.Nodes(false)
	if len(opts.Nodes) > 0 {
		keep := map[string]bool{}
		for _, n := range opts.Nodes {
			keep[n] = true
		}
		var f []topo.Node
		for _, n := range nodes {
			if keep[n.ID] || keep[n.Name] {
				f = append(f, n)
			}
		}
		nodes = f
	}
	segs := topo.BuildSegments(nodes, manual)
	if len(opts.Segments) > 0 {
		keep := map[string]bool{}
		for _, id := range opts.Segments {
			keep[id] = true
		}
		var f []topo.Segment
		for _, sg := range segs {
			if keep[sg.ID] || keep[sg.CIDR] {
				f = append(f, sg)
			}
		}
		segs = f
	}
	var infos []NodeInfo
	for _, n := range nodes {
		st, _ := s.hub.Get(n.ID)
		infos = append(infos, NodeInfo{ID: n.ID, Name: n.Name, Kind: st.Kind, Env: n.Env, Arch: st.Arch, Parent: n.Parent})
	}
	involve := map[string]bool{}
	for _, n := range opts.Involve {
		involve[n] = true
	}
	for _, n := range nodes {
		if involve[n.Name] {
			involve[n.ID] = true
		}
	}
	var paths []pathRef
	for si := range segs {
		sg := &segs[si]
		for _, a := range sg.Members {
			for _, b := range sg.Members {
				if a.Node == b.Node || (len(involve) > 0 && !involve[a.Node] && !involve[b.Node]) {
					continue
				}
				src, ok1 := s.hub.Conn(a.Node)
				dst, ok2 := s.hub.Conn(b.Node)
				if !ok1 || !ok2 {
					continue
				}
				bst, _ := s.hub.Get(b.Node)
				p := pathRef{idx: len(paths), seg: sg, a: a, b: b, srcConn: src, dstConn: dst, dstPort: bst.DataPort,
					liteSide: src.Lite() || dst.Lite()}
				if a.MTU > 1500 && b.MTU > 1500 {
					p.jumbo = min(a.MTU, b.MTU, 9216)
				}
				paths = append(paths, p)
			}
		}
	}
	return segs, paths, infos, nil
}

func stepCost(kind string, o RunOptions) int {
	switch kind {
	case "ping":
		return o.PingCount*50 + 200
	case "echo":
		return 300
	case "pmtu", "jumbo":
		return 800
	case "tcp1", "tcpn", "udp", "bidir":
		return o.DurationMS + proto.WarmupMS + 700
	default:
		return 1000
	}
}

func planSteps(kind string, paths []pathRef, o RunOptions) []string {
	var steps []string
	for _, p := range paths {
		steps = append(steps, "ping", "echo")
		if kind == KindFull {
			steps = append(steps, "pmtu")
			if p.jumbo > 0 {
				steps = append(steps, "jumbo")
			}
		}
	}
	if kind == KindFull {
		for _, p := range paths {
			steps = append(steps, "tcp1", "tcpn")
			if o.UDP && !p.liteSide {
				steps = append(steps, "udp")
			}
			if o.Bidir && !p.liteSide {
				steps = append(steps, "bidir")
			}
		}
	}
	return steps
}

// Estimate returns the number of paths and the expected duration in seconds.
func (r *Runner) Estimate(ctx context.Context, opts RunOptions) (int, int, error) {
	opts.defaults(r.s.settings(ctx))
	_, paths, _, err := r.plan(ctx, opts)
	if err != nil {
		return 0, 0, err
	}
	total := 0
	steps := planSteps(opts.Kind, paths, opts)
	for _, st := range steps {
		total += stepCost(st, opts)
	}
	if opts.Kind == KindReachability {
		total /= 4 // runs in parallel
	}
	if opts.Kind == KindAggregate {
		total = (opts.DurationMS + proto.WarmupMS + 2000) * max(1, len(uniqueSegs(paths)))
	}
	return len(paths), total / 1000, nil
}

func uniqueSegs(paths []pathRef) map[string]bool {
	m := map[string]bool{}
	for _, p := range paths {
		m[p.seg.ID] = true
	}
	return m
}

// Start launches a run in the background and returns its id.
func (r *Runner) Start(ctx context.Context, opts RunOptions) (int64, error) {
	s := r.s
	opts.defaults(s.settings(ctx))
	if opts.Kind != KindFull && opts.Kind != KindReachability && opts.Kind != KindAggregate {
		return 0, fmt.Errorf("unknown run kind %q", opts.Kind)
	}
	r.mu.Lock()
	if r.cur != nil {
		r.mu.Unlock()
		return 0, ErrBusy
	}
	segs, paths, infos, err := r.plan(ctx, opts)
	if err != nil {
		r.mu.Unlock()
		return 0, err
	}
	if len(paths) == 0 {
		r.mu.Unlock()
		return 0, ErrNoPaths
	}
	params, _ := json.Marshal(opts)
	id, err := s.store.CreateRun(ctx, store.Run{Kind: opts.Kind, Status: "running", Started: time.Now().UnixMilli(), Params: params})
	if err != nil {
		r.mu.Unlock()
		return 0, err
	}
	rep := &Report{ID: id, Kind: opts.Kind, Status: "running", Started: time.Now().UnixMilli(), Options: opts,
		Nodes: infos, Segments: segs, Paths: make([]topo.PathResult, len(paths))}
	segIdx := map[string]int{}
	for i, sg := range segs {
		segIdx[sg.ID] = i
	}
	for i, p := range paths {
		paths[i].seg = &rep.Segments[segIdx[p.seg.ID]]
		rep.Paths[i] = topo.PathResult{Seg: segIdx[p.seg.ID], SegID: p.seg.ID, Src: p.a.Node, SrcIf: p.a.Iface, SrcIP: p.a.IP,
			Dst: p.b.Node, DstIf: p.b.Iface, DstIP: p.b.IP,
			ExpectedMbps: topo.ExpectedMbps(paths[i].seg, p.a, p.b, s.inheritSpeed)}
	}
	rctx, cancel := context.WithCancel(s.ctx)
	steps := planSteps(opts.Kind, paths, opts)
	costs := make([]int, len(steps))
	for i, st := range steps {
		costs[i] = stepCost(st, opts)
	}
	ar := &activeRun{rep: rep, cancel: cancel, total: len(steps), costMS: costs}
	if opts.Kind == KindAggregate {
		ar.total = len(uniqueSegs(paths))
		ar.costMS = make([]int, ar.total)
		for i := range ar.costMS {
			ar.costMS[i] = opts.DurationMS + proto.WarmupMS + 2000
		}
	}
	r.cur = ar
	r.mu.Unlock()
	s.log.Info("run started", "id", id, "kind", opts.Kind, "paths", len(paths), "steps", ar.total, "actor", opts.Actor)
	s.events.Publish("run.started", ar.progress())
	go r.execute(rctx, ar, paths)
	return id, nil
}

func (r *Runner) advance(ar *activeRun, step string) {
	ar.mu.Lock()
	ar.done++
	ar.step = step
	ar.mu.Unlock()
	r.s.events.Publish("run.progress", ar.progress())
}

func (r *Runner) execute(ctx context.Context, ar *activeRun, paths []pathRef) {
	rep := ar.rep
	defer func() {
		r.finish(ar)
		ar.cancel()
	}()
	o := rep.Options
	if o.Kind == KindAggregate {
		r.aggregate(ctx, ar, paths)
		return
	}
	// phase 1: reachability, latency and MTU in parallel (one initiator test per node at a time)
	var wg sync.WaitGroup
	nodeLocks := map[string]*sync.Mutex{}
	for _, p := range paths {
		if nodeLocks[p.a.Node] == nil {
			nodeLocks[p.a.Node] = &sync.Mutex{}
		}
	}
	sem := make(chan struct{}, r.s.cfg.Parallel)
	for _, p := range paths {
		wg.Add(1)
		go func(p pathRef) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			l := nodeLocks[p.a.Node]
			l.Lock()
			defer l.Unlock()
			pr := &rep.Paths[p.idx]
			pr.Ping = r.probe(ctx, p, "ping", 84, o.PingCount)
			r.advance(ar, "ping "+label(p))
			pr.Echo = r.echo(ctx, p)
			r.advance(ar, "echo "+label(p))
			if o.Kind == KindFull {
				pr.MTU = r.probe(ctx, p, "pmtu", 1500, 3)
				pr.MTU.Size = 1500
				r.advance(ar, "mtu "+label(p))
				if p.jumbo > 0 {
					pr.Jumbo = r.probe(ctx, p, "pmtu", p.jumbo, 3)
					pr.Jumbo.Size = p.jumbo
					r.advance(ar, "jumbo "+label(p))
				}
			}
		}(p)
	}
	wg.Wait()
	if o.Kind != KindFull {
		return
	}
	// phase 2: throughput strictly serial (one test per uplink at a time)
	for _, p := range paths {
		if ctx.Err() != nil {
			return
		}
		pr := &rep.Paths[p.idx]
		echoOK := pr.Echo != nil && pr.Echo.Status == testengine.StatusOK
		pr.TCP1 = r.throughput(ctx, ar, p, "tcp", proto.DirForward, 1, !echoOK)
		r.advance(ar, "tcp x1 "+label(p))
		t1ok := pr.TCP1.Status == testengine.StatusOK || pr.TCP1.Status == testengine.StatusCounterMismatch
		pr.TCPN = r.throughput(ctx, ar, p, "tcp", proto.DirForward, o.Streams, !t1ok)
		r.advance(ar, fmt.Sprintf("tcp x%d %s", o.Streams, label(p)))
		if o.UDP && !p.liteSide {
			pr.UDP = r.throughput(ctx, ar, p, "udp", proto.DirForward, 1, !echoOK)
			r.advance(ar, "udp "+label(p))
		}
		if o.Bidir && !p.liteSide {
			pr.Bidir = r.throughput(ctx, ar, p, "tcp", proto.DirBidir, o.Streams, !t1ok)
			r.advance(ar, "bidir "+label(p))
		}
	}
}

func label(p pathRef) string {
	return fmt.Sprintf("%s -> %s (%s)", p.a.Node, p.b.Node, p.seg.ID)
}

func (r *Runner) spec(p pathRef, kind string) TestSpec {
	return TestSpec{Kind: kind, Dev: p.a.Iface, Src: p.a.IP, Dst: p.b.IP, Port: p.dstPort}
}

func stepTimeout(o RunOptions, kind string) time.Duration {
	return time.Duration(stepCost(kind, o))*time.Millisecond + 45*time.Second
}

func (r *Runner) probe(ctx context.Context, p pathRef, kind string, size, count int) *topo.Probe {
	sp := r.spec(p, kind)
	sp.Size, sp.Count = size, count
	sp.IntervalMS = 50 // §6.1: 20 packets every 50 ms
	if kind == "pmtu" {
		sp.IntervalMS = 200
	}
	tctx, cancel := context.WithTimeout(ctx, time.Duration(count*sp.IntervalMS)*time.Millisecond+20*time.Second)
	defer cancel()
	res, err := p.srcConn.Test(tctx, sp)
	if err != nil || res.Ping == nil {
		return &topo.Probe{Status: offlineOr(err)}
	}
	pr := res.Ping
	return &topo.Probe{Status: pr.Status, Sent: pr.Sent, Recv: pr.Recv, RTTMinUS: pr.RTTMinUS, RTTAvgUS: pr.RTTAvgUS,
		RTTP95US: pr.RTTP95US, RTTMaxUS: pr.RTTMaxUS, JitterUS: pr.JitterUS}
}

func offlineOr(err error) string {
	if errors.Is(err, ErrOffline) {
		return testengine.StatusOffline
	}
	if errors.Is(err, context.Canceled) {
		return testengine.StatusSkipped
	}
	return testengine.StatusTimeout
}

// lstp grants the run on the responder and runs the test on the initiator.
func (r *Runner) lstp(ctx context.Context, p pathRef, sp TestSpec, timeout time.Duration) (*testengine.Result, error) {
	runID, tok, err := testengine.NewToken()
	if err != nil {
		return nil, err
	}
	gctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = p.dstConn.Grant(gctx, runID, tok)
	cancel()
	if err != nil {
		return nil, err
	}
	sp.RunID, sp.Token = runID, tok[:]
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := p.srcConn.Test(tctx, sp)
	if err != nil {
		return nil, err
	}
	if res.LSTP == nil {
		return nil, errors.New("no lstp result")
	}
	return res.LSTP, nil
}

func (r *Runner) echo(ctx context.Context, p pathRef) *topo.Probe {
	sp := r.spec(p, "echo")
	sp.Count, sp.Streams = 10, 1
	res, err := r.lstp(ctx, p, sp, 30*time.Second)
	if err != nil {
		return &topo.Probe{Status: offlineOr(err)}
	}
	return &topo.Probe{Status: res.Status, Sent: int(res.Sent), Recv: int(res.Recv), RTTMinUS: res.RTTMinUS,
		RTTAvgUS: res.RTTAvgUS, RTTMaxUS: res.RTTMaxUS}
}

func (r *Runner) throughput(ctx context.Context, ar *activeRun, p pathRef, kind string, dir uint8, streams int, skip bool) *topo.Throughput {
	if skip {
		return &topo.Throughput{Status: testengine.StatusSkipped, Streams: streams}
	}
	o := ar.rep.Options
	sp := r.spec(p, kind)
	sp.Dir, sp.Streams, sp.DurationMS = dir, streams, o.DurationMS
	if kind == "udp" {
		sp.UDPRateKbps, sp.PktSize = o.UDPRateKbps, 1400
	}
	res, err := r.lstp(ctx, p, sp, stepTimeout(o, "tcpn"))
	if err != nil {
		return &topo.Throughput{Status: offlineOr(err), Streams: streams, Message: err.Error()}
	}
	t := &topo.Throughput{Status: res.Status, Streams: streams, BPS: res.BPS, BPSReverse: res.BPSReverse,
		CPULocal: res.LocalCPU, CPUPeer: res.PeerCPU, PathOK: res.PathOK, Verified: res.Verified, Sent: res.Sent,
		Lost: res.Lost, LossPct: res.LossPct, JitterUS: res.JitterUS, Message: res.Message}
	if res.Status == testengine.StatusRouteMismatch {
		ar.mu.Lock()
		ar.rep.Paths[p.idx].RouteDev = res.RouteDev
		ar.mu.Unlock()
	}
	return t
}

// aggregate runs all pairs of each segment at the same time and sums the capacity.
func (r *Runner) aggregate(ctx context.Context, ar *activeRun, paths []pathRef) {
	rep := ar.rep
	bySeg := map[string][]pathRef{}
	var order []string
	for _, p := range paths {
		if _, ok := bySeg[p.seg.ID]; !ok {
			order = append(order, p.seg.ID)
		}
		bySeg[p.seg.ID] = append(bySeg[p.seg.ID], p)
	}
	for _, segID := range order {
		if ctx.Err() != nil {
			return
		}
		ps := bySeg[segID]
		var wg sync.WaitGroup
		res := make([]*topo.Throughput, len(ps))
		for i, p := range ps {
			wg.Add(1)
			go func(i int, p pathRef) {
				defer wg.Done()
				res[i] = r.throughput(ctx, ar, p, "tcp", proto.DirForward, 1, false)
			}(i, p)
		}
		wg.Wait()
		ag := AggregateSeg{SegID: segID, Seg: rep.Paths[ps[0].idx].Seg, Pairs: len(ps),
			NodeOut: map[string]uint64{}, NodeIn: map[string]uint64{}}
		for i, p := range ps {
			rep.Paths[p.idx].TCP1 = res[i]
			if res[i].Status == testengine.StatusOK {
				ag.TotalBPS += res[i].BPS
				ag.NodeOut[p.a.Node] += res[i].BPS
				ag.NodeIn[p.b.Node] += res[i].BPS
			}
		}
		rep.Aggregate = append(rep.Aggregate, ag)
		r.advance(ar, "aggregate "+segID)
	}
}

func (r *Runner) finish(ar *activeRun) {
	s := r.s
	rep := ar.rep
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep.Status = "done"
	if s.ctx.Err() != nil || ar.progress().Done < ar.total {
		rep.Status = "cancelled"
	}
	st := s.settings(ctx)
	for i := range rep.Paths {
		topo.Judge(&rep.Paths[i], uint32(st.RTTWarnMS)*1000)
	}
	rep.Problems = topo.Problems(rep.Segments, rep.Paths, proto.DataPort)
	known, _ := s.store.KnownMACs(ctx)
	rep.Anomalies = topo.Anomalies(s.hub.Nodes(false), rep.Segments, known, s.discoveredExtras())
	sort.SliceStable(rep.Problems, func(a, b int) bool { return rep.Problems[a].Kind < rep.Problems[b].Kind })
	rep.Finished = time.Now().UnixMilli()
	b, err := json.Marshal(rep)
	if err == nil {
		err = s.store.FinishRun(ctx, rep.ID, rep.Status, b)
	}
	if err != nil {
		s.log.Error("cannot store run", "id", rep.ID, "err", err)
	}
	s.metrics.ObserveReport(rep)
	r.mu.Lock()
	r.cur = nil
	r.mu.Unlock()
	s.log.Info("run finished", "id", rep.ID, "status", rep.Status, "problems", len(rep.Problems))
	s.events.Publish("run.done", map[string]any{"id": rep.ID, "kind": rep.Kind, "status": rep.Status,
		"problems": len(rep.Problems)})
	s.notifyRun(ctx, rep)
}
