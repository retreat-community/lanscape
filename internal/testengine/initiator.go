package testengine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/retreat-community/lanscape/internal/netio"
	"github.com/retreat-community/lanscape/internal/proto"
)

// Params describe one test run by the initiator.
type Params struct {
	AgentID     string
	Dev         string
	Src         net.IP
	Dst         net.IP
	Port        int
	RunID       uint32
	Token       Token
	Kind        uint8 // proto.KindTCP, KindUDP, KindEcho
	Dir         uint8 // proto.DirForward, DirReverse, DirBidir
	Streams     int
	Duration    time.Duration
	Warmup      time.Duration
	UDPRateKbps int
	PktSize     int
	EchoCount   int
	SkipRoute   bool
}

// Result of a test from the initiator's point of view. For forward tests BPS is the rate
// measured by the receiving peer; for reverse tests it is measured locally; bidirectional
// tests fill BPS (initiator -> peer) and BPSReverse (peer -> initiator).
type Result struct {
	Status     string  `json:"status"`
	Message    string  `json:"message,omitempty"`
	RouteDev   string  `json:"route_dev,omitempty"`
	BPS        uint64  `json:"bps"`
	BPSReverse uint64  `json:"bps_reverse,omitempty"`
	Bytes      uint64  `json:"bytes"`
	WindowUS   uint64  `json:"window_us"`
	LocalCPU   int     `json:"cpu_local"`
	PeerCPU    int     `json:"cpu_peer"`
	PathOK     bool    `json:"path_ok"`
	Verified   bool    `json:"verified"`
	IfDelta    uint64  `json:"if_delta"`
	Sent       uint64  `json:"sent,omitempty"`
	Recv       uint64  `json:"recv,omitempty"`
	Lost       uint64  `json:"lost,omitempty"`
	LossPct    float64 `json:"loss_pct,omitempty"`
	JitterUS   uint32  `json:"jitter_us,omitempty"`
	RTTMinUS   uint32  `json:"rtt_min_us,omitempty"`
	RTTAvgUS   uint32  `json:"rtt_avg_us,omitempty"`
	RTTMaxUS   uint32  `json:"rtt_max_us,omitempty"`
}

// CheckRoute verifies that traffic to dst leaves through dev. It returns the actual device.
func CheckRoute(dev string, src, dst net.IP) (string, bool) {
	if dev == "" {
		return "", true
	}
	rd, err := netio.RouteDev(dst, src)
	if err != nil {
		// unsupported platform or no information: rely on the device binding
		return "", true
	}
	return rd, rd == dev
}

func (p *Params) dial(ctx context.Context) (net.Conn, error) {
	port := p.Port
	if port == 0 {
		port = proto.DataPort
	}
	d := net.Dialer{Timeout: hsTimeout, Control: netio.BindControl(p.Dev, false)}
	if p.Src != nil {
		d.LocalAddr = &net.TCPAddr{IP: p.Src}
	}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(p.Dst.String(), strconv.Itoa(port)))
}

// handshake returns the session id or a status.
func (p *Params) handshake(c net.Conn, role uint8, session uint32, idx int) (uint32, string, error) {
	_ = c.SetDeadline(time.Now().Add(hsTimeout))
	defer func() { _ = c.SetDeadline(time.Time{}) }()
	n1 := nonce()
	warm := p.Warmup
	if warm == 0 {
		warm = proto.WarmupMS * time.Millisecond
	}
	b := proto.NewBuilder().String(proto.TAgentID, p.AgentID).U32(proto.TRunID, p.RunID).
		U8(proto.TTestKind, p.Kind).U8(proto.TDirection, p.Dir).U8(proto.TStreams, uint8(p.Streams)).
		U32(proto.TDurationMS, uint32(p.Duration.Milliseconds())).Bytes(proto.TNonce, n1).
		U8(proto.TRole, role).U32(proto.TSession, session).U8(proto.TStreamIdx, uint8(idx)).
		U32(proto.TWarmupMS, uint32(warm.Milliseconds()))
	if err := proto.Write(c, proto.Hello, b); err != nil {
		return 0, StatusProtocol, err
	}
	f, err := proto.Read(c)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return 0, StatusTimeout, err
		}
		if role == proto.RoleData {
			return 0, StatusAuthFail, err
		}
		return 0, StatusProtocol, err
	}
	if f.Type == proto.Error {
		return 0, StatusAuthFail, errors.New("peer rejected the run")
	}
	n2, ok := f.Payload.Get(proto.TNonce)
	if f.Type != proto.Challenge || !ok || len(n2) != proto.NonceLen {
		return 0, StatusProtocol, errors.New("unexpected reply to HELLO")
	}
	if err := proto.Write(c, proto.Auth, proto.NewBuilder().Bytes(proto.TMAC, MAC(p.Token, n1, n2))); err != nil {
		return 0, StatusProtocol, err
	}
	f, err = proto.Read(c)
	if err != nil {
		return 0, StatusAuthFail, err
	}
	if f.Type == proto.Error {
		code, _ := f.Payload.U8(proto.TErrCode)
		switch code {
		case proto.ErrLimit:
			return 0, StatusLimit, errors.New("peer limits exceeded")
		case proto.ErrUnsupported:
			return 0, StatusUnsupported, errors.New("peer does not support the test")
		}
		return 0, StatusAuthFail, errors.New("peer rejected authentication")
	}
	sid, ok := f.Payload.U32(proto.TSession)
	if f.Type != proto.Ready || !ok {
		return 0, StatusProtocol, errors.New("unexpected reply to AUTH")
	}
	return sid, StatusOK, nil
}

// Run executes an LSTP test as the initiator.
func Run(ctx context.Context, p Params) Result {
	if p.Streams <= 0 {
		p.Streams = 1
	}
	if p.Warmup == 0 {
		p.Warmup = proto.WarmupMS * time.Millisecond
	}
	res := Result{Status: StatusOK, PathOK: true}
	if !p.SkipRoute {
		if rd, ok := CheckRoute(p.Dev, p.Src, p.Dst); !ok {
			res.Status, res.RouteDev, res.PathOK = StatusRouteMismatch, rd, false
			res.Message = fmt.Sprintf("route to %s leaves via %s, not %s", p.Dst, rd, p.Dev)
			return res
		}
	}
	ctrl, err := p.dial(ctx)
	if err != nil {
		res.Status, res.Message = classify(err), err.Error()
		return res
	}
	defer ctrl.Close()
	sid, st, err := p.handshake(ctrl, proto.RoleCtrl, 0, 0)
	if st != StatusOK {
		res.Status, res.Message = st, errString(err)
		return res
	}
	switch p.Kind {
	case proto.KindEcho:
		p.runEcho(ctrl, &res)
	case proto.KindTCP:
		p.runTCP(ctx, ctrl, sid, &res)
	case proto.KindUDP:
		p.runUDP(ctx, ctrl, sid, &res)
	default:
		res.Status = StatusUnsupported
	}
	_ = ctrl.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	_ = proto.Write(ctrl, proto.Bye, proto.NewBuilder())
	return res
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (p *Params) runEcho(c net.Conn, res *Result) {
	n := p.EchoCount
	if n <= 0 {
		n = 10
	}
	var sum uint64
	res.RTTMinUS = ^uint32(0)
	for i := 0; i < n; i++ {
		t0 := time.Now()
		_ = c.SetDeadline(t0.Add(time.Second))
		if proto.Write(c, proto.EchoReq, proto.NewBuilder().U32(proto.TSeq, uint32(i)).U64(proto.TTsUS, nowUS())) != nil {
			break
		}
		res.Sent++
		f, err := proto.Read(c)
		if err != nil || f.Type != proto.EchoRep {
			break
		}
		if seq, _ := f.Payload.U32(proto.TSeq); seq != uint32(i) {
			break
		}
		rtt := uint64(time.Since(t0).Microseconds())
		res.Recv++
		sum += rtt
		res.RTTMinUS = min(res.RTTMinUS, uint32(rtt))
		res.RTTMaxUS = max(res.RTTMaxUS, uint32(rtt))
	}
	_ = c.SetDeadline(time.Time{})
	if res.Recv == 0 {
		res.RTTMinUS = 0
		res.Status = StatusTimeout
		return
	}
	res.RTTAvgUS = uint32(sum / res.Recv)
	if res.Recv < res.Sent {
		res.Status = StatusTimeout
	}
}

func (p *Params) openData(ctx context.Context, sid uint32, n, offset int) ([]net.Conn, string, error) {
	conns := make([]net.Conn, 0, n)
	for i := 0; i < n; i++ {
		c, err := p.dial(ctx)
		if err != nil {
			closeAll(conns)
			return nil, classify(err), err
		}
		if _, st, err := p.handshake(c, proto.RoleData, sid, offset+i); st != StatusOK {
			c.Close()
			closeAll(conns)
			return nil, st, err
		}
		conns = append(conns, c)
	}
	return conns, StatusOK, nil
}

func closeAll(cs []net.Conn) {
	for _, c := range cs {
		c.Close()
	}
}

func (p *Params) runTCP(ctx context.Context, ctrl net.Conn, sid uint32, res *Result) {
	out, st, err := p.openData(ctx, sid, p.Streams, 0)
	if st != StatusOK {
		res.Status, res.Message = st, errString(err)
		return
	}
	defer closeAll(out)
	var in []net.Conn
	if p.Dir == proto.DirBidir {
		in, st, err = p.openData(ctx, sid, p.Streams, p.Streams)
		if st != StatusOK {
			res.Status, res.Message = st, errString(err)
			return
		}
		defer closeAll(in)
	}
	c0, cerr := netio.ReadCounters(p.Dev)
	cpu0 := netio.ReadCPU()
	if err := proto.Write(ctrl, proto.Start, proto.NewBuilder()); err != nil {
		res.Status = StatusProtocol
		return
	}
	var sent uint64
	var rx rxStats
	switch p.Dir {
	case proto.DirForward:
		sent = send(out, time.Now().Add(p.Duration+p.Warmup))
	case proto.DirReverse:
		rx = receive(out, time.Now().Add(p.Duration+p.Warmup+3*time.Second), p.Warmup)
	default:
		done := make(chan struct{})
		go func() {
			rx = receive(in, time.Now().Add(p.Duration+p.Warmup+3*time.Second), p.Warmup)
			close(done)
		}()
		sent = send(out, time.Now().Add(p.Duration+p.Warmup))
		<-done
	}
	res.LocalCPU = netio.Permille(cpu0, netio.ReadCPU())
	// peer reports: RESULT for data it received, SENDER_STAT for data it sent
	var peerRes, peerStat proto.TLV
	wantRes := p.Dir != proto.DirReverse
	wantStat := p.Dir != proto.DirForward
	_ = ctrl.SetReadDeadline(time.Now().Add(8 * time.Second))
	for (wantRes && peerRes == nil) || (wantStat && peerStat == nil) {
		f, err := proto.Read(ctrl)
		if err != nil {
			break
		}
		switch f.Type {
		case proto.Result:
			peerRes = f.Payload
		case proto.SenderStat:
			peerStat = f.Payload
		}
	}
	_ = ctrl.SetReadDeadline(time.Time{})
	c1, cerr2 := netio.ReadCounters(p.Dev)
	res.Verified = cerr == nil && cerr2 == nil
	peerOK := true
	if wantRes {
		if peerRes == nil {
			res.Status = StatusTimeout
			return
		}
		res.Bytes, _ = peerRes.U64(proto.TBytes)
		res.WindowUS, _ = peerRes.U64(proto.TWindowUS)
		cpu, _ := peerRes.U16(proto.TCPU)
		res.PeerCPU = int(cpu)
		if ok, present := peerRes.U8(proto.TPathOK); present && ok == 0 {
			peerOK = false
		}
		if res.WindowUS > 0 {
			res.BPS = res.Bytes * 8 * 1_000_000 / res.WindowUS
		}
		res.IfDelta = c1.TX - c0.TX
		if res.Verified && res.IfDelta < sent/10*9 {
			peerOK = false
		}
	}
	if wantStat {
		if peerStat != nil {
			cpu, _ := peerStat.U16(proto.TCPU)
			res.PeerCPU = max(res.PeerCPU, int(cpu))
			if ok, present := peerStat.U8(proto.TPathOK); present && ok == 0 {
				peerOK = false
			}
		}
		bps := uint64(0)
		if rx.windowUS > 0 {
			bps = rx.counted * 8 * 1_000_000 / rx.windowUS
		}
		if res.Verified && c1.RX-c0.RX < rx.total/10*9 {
			peerOK = false
		}
		if p.Dir == proto.DirReverse {
			res.BPS, res.Bytes, res.WindowUS, res.IfDelta = bps, rx.counted, rx.windowUS, c1.RX-c0.RX
		} else {
			res.BPSReverse = bps
		}
	}
	res.PathOK = peerOK
	switch {
	case res.Bytes == 0:
		res.Status = StatusTimeout
	case !res.PathOK:
		res.Status = StatusCounterMismatch
	}
}

func (p *Params) runUDP(ctx context.Context, ctrl net.Conn, sid uint32, res *Result) {
	lc := net.ListenConfig{Control: netio.BindControl(p.Dev, false)}
	pc, err := lc.ListenPacket(ctx, "udp", net.JoinHostPort(ipString(p.Src), "0"))
	if err != nil {
		res.Status, res.Message = StatusNoDevice, err.Error()
		return
	}
	uc := pc.(*net.UDPConn)
	defer uc.Close()
	port := p.Port
	if port == 0 {
		port = proto.DataPort
	}
	rate := p.UDPRateKbps
	if rate <= 0 {
		rate = 100_000
	}
	size := p.PktSize
	if size <= 0 {
		size = 1200
	}
	localPort := uc.LocalAddr().(*net.UDPAddr).Port
	if err := proto.Write(ctrl, proto.Start, proto.NewBuilder().U32(proto.TUDPRateKbps, uint32(rate)).
		U16(proto.TPktSize, uint16(size)).U16(proto.TUDPPort, uint16(localPort))); err != nil {
		res.Status = StatusProtocol
		return
	}
	cpu0 := netio.ReadCPU()
	to := &net.UDPAddr{IP: p.Dst, Port: port}
	deadline := time.Now().Add(p.Duration + p.Warmup + 10*time.Second)
	stat, peerRes := ctrlFrames(ctrl, deadline)
	recvDone := make(chan udpStats, 1)
	if p.Dir != proto.DirForward {
		pkts := make(chan udpPkt, 4096)
		go func() {
			buf := make([]byte, 65536)
			for {
				n, from, err := uc.ReadFromUDP(buf)
				if err != nil {
					close(pkts)
					return
				}
				if s, seq, ts, ok := parseUDPHeader(buf[:n]); ok && s == sid {
					select {
					case pkts <- udpPkt{n: n, seq: seq, ts: ts, from: from}:
					default:
					}
				}
			}
		}()
		go func() { recvDone <- collectUDP(pkts, stat, deadline, p.Warmup) }()
	}
	if p.Dir != proto.DirReverse {
		n := sendUDP(uc, to, sid, rate, size, time.Now().Add(p.Duration+p.Warmup))
		res.Sent = n
		_ = proto.Write(ctrl, proto.SenderStat, proto.NewBuilder().U64(proto.TPktsSent, n))
		t, ok := <-peerRes
		if !ok {
			res.Status = StatusTimeout
			return
		}
		fillUDP(res, t)
	}
	if p.Dir != proto.DirForward {
		st := <-recvDone
		bps := uint64(0)
		if w := st.window(); w > 0 {
			bps = st.counted * 8 * 1_000_000 / w
		}
		if p.Dir == proto.DirReverse {
			sent := st.sent
			if sent == 0 && st.haveSeq {
				sent = uint64(st.maxSeq) + 1
			}
			res.BPS, res.Bytes, res.WindowUS = bps, st.counted, st.window()
			res.Sent, res.Recv, res.JitterUS = sent, st.recv, uint32(st.jitter)
			if sent > st.recv {
				res.Lost = sent - st.recv
			}
		} else {
			res.BPSReverse = bps
		}
	}
	res.LocalCPU = netio.Permille(cpu0, netio.ReadCPU())
	if res.Sent > 0 {
		res.LossPct = float64(res.Lost) * 100 / float64(res.Sent)
	}
	if res.Bytes == 0 {
		res.Status = StatusTimeout
	}
}

func fillUDP(res *Result, t proto.TLV) {
	res.Bytes, _ = t.U64(proto.TBytes)
	res.WindowUS, _ = t.U64(proto.TWindowUS)
	sent, _ := t.U64(proto.TPktsSent)
	if sent > 0 {
		res.Sent = sent
	}
	res.Recv, _ = t.U64(proto.TPktsRecv)
	res.Lost, _ = t.U64(proto.TLost)
	res.JitterUS, _ = t.U32(proto.TJitterUS)
	cpu, _ := t.U16(proto.TCPU)
	res.PeerCPU = int(cpu)
	if res.WindowUS > 0 {
		res.BPS = res.Bytes * 8 * 1_000_000 / res.WindowUS
	}
}

func ipString(ip net.IP) string {
	if ip == nil {
		return ""
	}
	return ip.String()
}
