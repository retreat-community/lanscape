package server

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/netio"
	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/testengine"
)

// liteConn is a Lanscape Mini agent connected to the Mini-compatible port 47701.
type liteConn struct {
	id     string
	c      net.Conn
	wmu    sync.Mutex
	mu     sync.Mutex
	cmdID  uint32
	grants map[uint32]chan struct{}
	cmds   map[uint32]chan proto.TLV
	closed chan struct{}
	once   sync.Once
}

func (l *liteConn) ID() string { return l.id }
func (l *liteConn) Lite() bool { return true }

func (l *liteConn) Close() {
	l.once.Do(func() {
		close(l.closed)
		l.c.Close()
	})
}

func (l *liteConn) write(typ byte, b *proto.Builder) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	_ = l.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return proto.Write(l.c, typ, b)
}

func (l *liteConn) Request(context.Context, string, any) (json.RawMessage, error) {
	return nil, errors.New("lite agents only run network tests")
}

func (l *liteConn) Grant(ctx context.Context, runID uint32, token testengine.Token) error {
	ch := make(chan struct{}, 1)
	l.mu.Lock()
	l.grants[runID] = ch
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.grants, runID)
		l.mu.Unlock()
	}()
	if err := l.write(proto.CGrant, proto.NewBuilder().U32(proto.TRunID, runID).Bytes(proto.TRunToken, token[:]).
		U32(proto.TTTLS, proto.TokenTTL)); err != nil {
		return err
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-l.closed:
		return ErrOffline
	}
}

func (l *liteConn) Test(ctx context.Context, spec TestSpec) (agent.TestResult, error) {
	var res agent.TestResult
	b := proto.NewBuilder()
	src, dst := net.ParseIP(spec.Src).To4(), net.ParseIP(spec.Dst).To4()
	if src == nil || dst == nil {
		return res, errors.New("lite agents test IPv4 only")
	}
	l.mu.Lock()
	l.cmdID++
	id := l.cmdID
	ch := make(chan proto.TLV, 1)
	l.cmds[id] = ch
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.cmds, id)
		l.mu.Unlock()
	}()
	b.U32(proto.TCmdID, id).String(proto.TSrcDev, spec.Dev).Bytes(proto.TSrcAddr, src).Bytes(proto.TDstAddr, dst)
	if spec.Port != 0 {
		b.U16(proto.TDstPort, uint16(spec.Port))
	}
	switch spec.Kind {
	case "ping", "pmtu":
		kind := uint8(proto.CmdPing)
		if spec.Kind == "pmtu" {
			kind = proto.CmdPMTU
		}
		b.U8(proto.TCmdKind, kind).U16(proto.TCount, uint16(spec.Count)).U16(proto.TIntervalMS, uint16(spec.IntervalMS)).
			U16(proto.TSize, uint16(spec.Size))
	case "echo", "tcp":
		if spec.Dir != proto.DirForward {
			return agent.TestResult{LSTP: &testengine.Result{Status: testengine.StatusUnsupported,
				Message: "lite agents run forward tests only"}}, nil
		}
		k := uint8(proto.KindTCP)
		if spec.Kind == "echo" {
			k = proto.KindEcho
		}
		b.U8(proto.TCmdKind, proto.CmdLSTP).U32(proto.TRunID, spec.RunID).Bytes(proto.TRunToken, spec.Token).
			U8(proto.TTestKind, k).U8(proto.TDirection, proto.DirForward).U8(proto.TStreams, uint8(spec.Streams)).
			U32(proto.TDurationMS, uint32(spec.DurationMS)).U16(proto.TCount, uint16(spec.Count))
	default:
		return agent.TestResult{LSTP: &testengine.Result{Status: testengine.StatusUnsupported,
			Message: "not supported by lite agents"}}, nil
	}
	if err := l.write(proto.CCmd, b); err != nil {
		return res, err
	}
	var t proto.TLV
	select {
	case t = <-ch:
	case <-ctx.Done():
		return res, ctx.Err()
	case <-l.closed:
		return res, ErrOffline
	}
	st, _ := t.U8(proto.TStatus)
	status := testengine.StatusInternal
	if int(st) < len(testengine.MiniStatus) {
		status = testengine.MiniStatus[st]
	}
	sent, _ := t.U16(proto.TSent)
	recv, _ := t.U16(proto.TRecv)
	rmin, _ := t.U32(proto.TRTTMinUS)
	ravg, _ := t.U32(proto.TRTTAvgUS)
	rmax, _ := t.U32(proto.TRTTMaxUS)
	if spec.Kind == "ping" || spec.Kind == "pmtu" {
		res.Ping = &testengine.PingResult{Status: status, Message: t.Str(proto.TErrMsg), Sent: int(sent), Recv: int(recv),
			RTTMinUS: rmin, RTTAvgUS: ravg, RTTMaxUS: rmax, RTTP95US: rmax}
		if status == testengine.StatusRouteMismatch {
			res.Ping.Message = "route via " + t.Str(proto.TRouteDev)
		}
		return res, nil
	}
	r := &testengine.Result{Status: status, Message: t.Str(proto.TErrMsg), RouteDev: t.Str(proto.TRouteDev),
		Sent: uint64(sent), Recv: uint64(recv), RTTMinUS: rmin, RTTAvgUS: ravg, RTTMaxUS: rmax}
	r.BPS, _ = t.U64(proto.TBPS)
	r.Bytes, _ = t.U64(proto.TBytes)
	r.WindowUS, _ = t.U64(proto.TWindowUS)
	lc, _ := t.U16(proto.TLocalCPU)
	pc, _ := t.U16(proto.TPeerCPU)
	r.LocalCPU, r.PeerCPU = int(lc), int(pc)
	pok, _ := t.U8(proto.TPathOK)
	r.PathOK, r.Verified = pok != 0, true
	r.IfDelta, _ = t.U64(proto.TIfTX)
	res.LSTP = r
	return res, nil
}

// miniInventory converts a Mini LC_INVENTORY payload.
func miniInventory(p proto.TLV) agent.Inventory {
	inv := agent.Inventory{Env: agent.Env{Kind: "unknown"}}
	for _, v := range p.All(proto.TIface) {
		t := proto.TLV(v)
		name := t.Str(proto.TIfName)
		ab, _ := t.Get(proto.TIfAddr4)
		if name == "" || len(ab) != 4 {
			continue
		}
		prefix, _ := t.U8(proto.TIfPrefix)
		vlan, _ := t.U16(proto.TIfVLAN)
		speed, _ := t.U32(proto.TIfSpeed)
		mtu, _ := t.U32(proto.TIfMTU)
		flags, _ := t.U32(proto.TIfFlags)
		kind := t.Str(proto.TIfKind)
		if kind == "" {
			kind = "physical"
		}
		mac, _ := t.Get(proto.TIfMAC)
		addr := netip.AddrFrom4([4]byte(ab))
		i := netio.Iface{Name: name, MTU: int(mtu), Up: flags&1 != 0, Carrier: flags&2 != 0, Kind: kind,
			Parent: t.Str(proto.TIfParent), VLAN: int(vlan), Speed: int(speed),
			Addrs: []netio.Addr{{IP: addr.String(), Prefix: int(prefix)}}}
		if len(mac) == 6 {
			i.MAC = net.HardwareAddr(mac).String()
		}
		merged := false
		for k := range inv.Ifaces {
			if inv.Ifaces[k].Name == name {
				inv.Ifaces[k].Addrs = append(inv.Ifaces[k].Addrs, i.Addrs...)
				merged = true
			}
		}
		if !merged {
			inv.Ifaces = append(inv.Ifaces, i)
		}
	}
	return inv
}

// serveMini accepts Lanscape Mini agents on the Mini control port.
func (s *Server) serveMini(ctx context.Context, addr string) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("server: mini listener %s: %w", addr, err)
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		go s.handleMini(ctx, c)
	}
}

func (s *Server) miniToken(ctx context.Context) string {
	var st Settings
	_ = s.store.GetSetting(ctx, "settings", &st)
	if st.MiniToken != "" {
		return st.MiniToken
	}
	return s.cfg.MiniToken
}

func (s *Server) handleMini(ctx context.Context, c net.Conn) {
	token := []byte(s.miniToken(ctx))
	if len(token) < 8 {
		c.Close()
		return
	}
	br := bufio.NewReader(c)
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	f, err := proto.Read(br)
	if err != nil || f.Type != proto.CHello {
		c.Close()
		return
	}
	agentID := f.Payload.Str(proto.TAgentID)
	n1, _ := f.Payload.Get(proto.TNonce)
	if agentID == "" || len(n1) != proto.NonceLen {
		c.Close()
		return
	}
	n1 = append([]byte(nil), n1...)
	n2 := make([]byte, proto.NonceLen)
	if _, err := rand.Read(n2); err != nil {
		c.Close()
		return
	}
	if proto.Write(c, proto.CChallenge, proto.NewBuilder().Bytes(proto.TNonce, n2)) != nil {
		c.Close()
		return
	}
	af, err := proto.Read(br)
	if err != nil || af.Type != proto.CAuth {
		c.Close()
		return
	}
	mac, _ := af.Payload.Get(proto.TMAC)
	if !hmac.Equal(mac, testengine.LabelledMAC(token, "agent", n1, n2)) {
		s.log.Warn("mini agent: bad token", "agent", agentID, "remote", c.RemoteAddr().String())
		c.Close()
		return
	}
	if proto.Write(c, proto.CWelcome, proto.NewBuilder().Bytes(proto.TServerMAC,
		testengine.LabelledMAC(token, "server", n2, n1))) != nil {
		c.Close()
		return
	}
	_ = c.SetDeadline(time.Time{})
	id := "mini-" + agentID
	dataPort, ok := f.Payload.U16(proto.TDstPort)
	if !ok {
		dataPort = proto.DataPort
	}
	l := &liteConn{id: id, c: c, grants: map[uint32]chan struct{}{}, cmds: map[uint32]chan proto.TLV{},
		closed: make(chan struct{})}
	st := AgentState{ID: id, Name: agentID, Kind: "lite", Version: f.Payload.Str(proto.TVersion),
		Arch: f.Payload.Str(proto.TArch), OS: "linux", Hostname: f.Payload.Str(proto.THostname),
		HostID: f.Payload.Str(proto.THostID), DataPort: int(dataPort)}
	s.hub.Connected(st, l)
	s.persistLite(ctx, st)
	s.log.Info("lite agent connected", "id", id, "arch", st.Arch)
	defer func() {
		s.hub.Disconnected(id, l)
		l.Close()
	}()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-l.closed:
				return
			case <-t.C:
				if l.write(proto.CPing, proto.NewBuilder()) != nil {
					l.Close()
					return
				}
			}
		}
	}()
	for {
		_ = c.SetReadDeadline(time.Now().Add(90 * time.Second))
		f, err := proto.Read(br)
		if err != nil {
			return
		}
		s.hub.Touch(id)
		switch f.Type {
		case proto.CInventory:
			inv := miniInventory(f.Payload)
			s.hub.SetInventory(id, inv)
			st.Inv = inv
			s.persistLite(ctx, st)
		case proto.CPing:
			_ = l.write(proto.CPong, proto.NewBuilder())
		case proto.CGrantAck:
			rid, _ := f.Payload.U32(proto.TRunID)
			l.mu.Lock()
			if ch := l.grants[rid]; ch != nil {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
			l.mu.Unlock()
		case proto.CCmdResult:
			cid, _ := f.Payload.U32(proto.TCmdID)
			l.mu.Lock()
			if ch := l.cmds[cid]; ch != nil {
				select {
				case ch <- f.Payload:
				default:
				}
			}
			l.mu.Unlock()
		}
	}
}
