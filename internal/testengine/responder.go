package testengine

import (
	"context"
	"crypto/hmac"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/retreat-community/lanscape/internal/netio"
	"github.com/retreat-community/lanscape/internal/proto"
)

const hsTimeout = 3 * time.Second

// Limits restrict what remote initiators may request.
type Limits struct {
	MaxDuration time.Duration
	MaxStreams  int
	MaxUDPKbps  int
}

// DefaultLimits are used when none are configured.
var DefaultLimits = Limits{MaxDuration: 30 * time.Second, MaxStreams: 16, MaxUDPKbps: 10_000_000}

type grant struct {
	token   Token
	expires time.Time
}

// Responder accepts authenticated test sessions.
type Responder struct {
	Limits Limits
	Log    *slog.Logger

	mu       sync.Mutex
	grants   map[uint32]grant
	sessions map[uint32]*session
	udp      *net.UDPConn
}

type session struct {
	id      uint32
	runID   uint32
	token   Token
	data    chan net.Conn
	udpPkts chan udpPkt
}

type udpPkt struct {
	n    int
	seq  uint32
	ts   uint64
	from *net.UDPAddr
}

// NewResponder creates a responder with default limits.
func NewResponder(log *slog.Logger) *Responder {
	if log == nil {
		log = slog.Default()
	}
	return &Responder{Limits: DefaultLimits, Log: log, grants: map[uint32]grant{}, sessions: map[uint32]*session{}}
}

// Grant allows a run for ttl (at most 10 minutes).
func (r *Responder) Grant(runID uint32, token Token, ttl time.Duration) {
	if ttl <= 0 || ttl > proto.TokenTTL*time.Second {
		ttl = proto.TokenTTL * time.Second
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for id, g := range r.grants {
		if g.expires.Before(now) {
			delete(r.grants, id)
		}
	}
	r.grants[runID] = grant{token: token, expires: now.Add(ttl)}
}

func (r *Responder) lookup(runID uint32) (Token, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g, ok := r.grants[runID]
	if !ok || g.expires.Before(time.Now()) {
		return Token{}, false
	}
	return g.token, true
}

// Serve listens on addr (":47700") for TCP and UDP until ctx is done.
func (r *Responder) Serve(ctx context.Context, addr string) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("testengine: listen %s: %w", addr, err)
	}
	pc, err := lc.ListenPacket(ctx, "udp", addr)
	if err != nil {
		ln.Close()
		return fmt.Errorf("testengine: listen udp %s: %w", addr, err)
	}
	r.mu.Lock()
	r.udp = pc.(*net.UDPConn)
	r.mu.Unlock()
	go func() {
		<-ctx.Done()
		ln.Close()
		pc.Close()
	}()
	go r.udpLoop()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // listener closed on shutdown
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go r.handle(ctx, c)
	}
}

func (r *Responder) udpLoop() {
	buf := make([]byte, 65536)
	for {
		n, from, err := r.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		sid, seq, ts, ok := parseUDPHeader(buf[:n])
		if !ok {
			continue
		}
		r.mu.Lock()
		s := r.sessions[sid]
		r.mu.Unlock()
		if s == nil {
			continue
		}
		select {
		case s.udpPkts <- udpPkt{n: n, seq: seq, ts: ts, from: from}:
		default:
		}
	}
}

func writeErr(c net.Conn, code byte) {
	_ = c.SetWriteDeadline(time.Now().Add(time.Second))
	_ = proto.Write(c, proto.Error, proto.NewBuilder().U8(proto.TErrCode, code))
}

// auth runs CHALLENGE/AUTH for an already received HELLO.
func auth(c net.Conn, token Token, n1 []byte) bool {
	n2 := nonce()
	if proto.Write(c, proto.Challenge, proto.NewBuilder().Bytes(proto.TNonce, n2)) != nil {
		return false
	}
	f, err := proto.Read(c)
	if err != nil || f.Type != proto.Auth {
		return false
	}
	m, _ := f.Payload.Get(proto.TMAC)
	return hmac.Equal(m, MAC(token, n1, n2))
}

func (r *Responder) handle(ctx context.Context, c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(hsTimeout))
	f, err := proto.Read(c)
	if err != nil || f.Type != proto.Hello {
		c.Close()
		return
	}
	p := f.Payload
	runID, ok1 := p.U32(proto.TRunID)
	n1, ok2 := p.Get(proto.TNonce)
	role, ok3 := p.U8(proto.TRole)
	token, granted := r.lookup(runID)
	if !ok1 || !ok2 || !ok3 || len(n1) != proto.NonceLen || !granted {
		// no valid run: close immediately without a reply
		c.Close()
		return
	}
	n1 = append([]byte(nil), n1...)
	if role == proto.RoleData {
		sid, _ := p.U32(proto.TSession)
		r.mu.Lock()
		s := r.sessions[sid]
		r.mu.Unlock()
		if s == nil || s.runID != runID || !auth(c, token, n1) {
			c.Close()
			return
		}
		if proto.Write(c, proto.Ready, proto.NewBuilder().U32(proto.TSession, sid)) != nil {
			c.Close()
			return
		}
		_ = c.SetDeadline(time.Time{})
		select {
		case s.data <- c:
		default:
			c.Close()
		}
		return
	}
	if !auth(c, token, n1) {
		r.Log.Warn("dataplane auth failed", "run", runID, "peer", c.RemoteAddr().String())
		c.Close()
		return
	}
	kind, _ := p.U8(proto.TTestKind)
	dir, _ := p.U8(proto.TDirection)
	streams, _ := p.U8(proto.TStreams)
	durMS, _ := p.U32(proto.TDurationMS)
	warmMS, ok := p.U32(proto.TWarmupMS)
	if !ok {
		warmMS = proto.WarmupMS
	}
	dur := time.Duration(durMS) * time.Millisecond
	if streams == 0 {
		streams = 1
	}
	if dur > r.Limits.MaxDuration || int(streams) > r.Limits.MaxStreams || dir > proto.DirBidir || warmMS > 5000 {
		writeErr(c, proto.ErrLimit)
		c.Close()
		return
	}
	if kind != proto.KindEcho && kind != proto.KindTCP && kind != proto.KindUDP {
		writeErr(c, proto.ErrUnsupported)
		c.Close()
		return
	}
	var sidb [4]byte
	copy(sidb[:], nonce())
	s := &session{id: binary.BigEndian.Uint32(sidb[:]), runID: runID, token: token,
		data: make(chan net.Conn, 2*int(streams)), udpPkts: make(chan udpPkt, 4096)}
	r.mu.Lock()
	r.sessions[s.id] = s
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.sessions, s.id)
		r.mu.Unlock()
		c.Close()
	}()
	if proto.Write(c, proto.Ready, proto.NewBuilder().U32(proto.TSession, s.id)) != nil {
		return
	}
	warm := time.Duration(warmMS) * time.Millisecond
	switch kind {
	case proto.KindEcho:
		r.echo(c)
	case proto.KindTCP:
		r.tcp(ctx, c, s, int(streams), dir, dur, warm)
	case proto.KindUDP:
		r.udpSession(c, s, dir, dur, warm)
	}
}

func (r *Responder) echo(c net.Conn) {
	for {
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		f, err := proto.Read(c)
		if err != nil || f.Type != proto.EchoReq {
			return
		}
		fr, err := proto.Encode(proto.EchoRep, f.Payload)
		if err != nil {
			return
		}
		if _, err := c.Write(fr); err != nil {
			return
		}
	}
}

func localDev(c net.Conn) string {
	la, ok := c.LocalAddr().(*net.TCPAddr)
	if !ok {
		return ""
	}
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, i := range ifs {
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(la.IP) {
				return i.Name
			}
		}
	}
	return ""
}

func (r *Responder) tcp(ctx context.Context, c net.Conn, s *session, streams int, dir uint8, dur, warm time.Duration) {
	want := streams
	if dir == proto.DirBidir {
		want = 2 * streams
	}
	conns := make([]net.Conn, 0, want)
	defer func() {
		for _, dc := range conns {
			dc.Close()
		}
	}()
	timeout := time.After(5 * time.Second)
	for len(conns) < want {
		select {
		case dc := <-s.data:
			conns = append(conns, dc)
		case <-timeout:
			return
		case <-ctx.Done():
			return
		}
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	f, err := proto.Read(c)
	if err != nil || f.Type != proto.Start {
		return
	}
	_ = c.SetDeadline(time.Time{})
	dev := localDev(c)
	c0, _ := netio.ReadCounters(dev)
	cpu0 := netio.ReadCPU()
	var recvConns, sendConns []net.Conn
	switch dir {
	case proto.DirForward:
		recvConns = conns
	case proto.DirReverse:
		sendConns = conns
	default:
		// bidirectional: the initiator opened its sending streams first
		recvConns, sendConns = conns[:streams], conns[streams:]
	}
	var wg sync.WaitGroup
	var rx rxStats
	var sent atomic.Uint64
	if len(recvConns) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rx = receive(recvConns, time.Now().Add(dur+warm+3*time.Second), warm)
		}()
	}
	if len(sendConns) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sent.Store(send(sendConns, time.Now().Add(dur+warm)))
		}()
	}
	wg.Wait()
	cpu := uint16(netio.Permille(cpu0, netio.ReadCPU()))
	c1, cerr := netio.ReadCounters(dev)
	_ = c.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if len(recvConns) > 0 {
		pathOK := cerr != nil || c1.RX-c0.RX >= rx.total/10*9
		_ = proto.Write(c, proto.Result, proto.NewBuilder().U64(proto.TBytes, rx.counted).
			U64(proto.TWindowUS, rx.windowUS).U64(proto.TPktsRecv, rx.total).U16(proto.TCPU, cpu).
			U64(proto.TIfRX, c1.RX-c0.RX).U8(proto.TPathOK, b2u(pathOK)))
	}
	if len(sendConns) > 0 {
		pathOK := cerr != nil || c1.TX-c0.TX >= sent.Load()/10*9
		_ = proto.Write(c, proto.SenderStat, proto.NewBuilder().U64(proto.TBytes, sent.Load()).
			U16(proto.TCPU, cpu).U64(proto.TIfTX, c1.TX-c0.TX).U8(proto.TPathOK, b2u(pathOK)))
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _ = proto.Read(c)
}

func (r *Responder) udpSession(c net.Conn, s *session, dir uint8, dur, warm time.Duration) {
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	f, err := proto.Read(c)
	if err != nil || f.Type != proto.Start {
		return
	}
	_ = c.SetDeadline(time.Time{})
	rate, _ := f.Payload.U32(proto.TUDPRateKbps)
	size, _ := f.Payload.U16(proto.TPktSize)
	port, _ := f.Payload.U16(proto.TUDPPort)
	if int(rate) > r.Limits.MaxUDPKbps {
		writeErr(c, proto.ErrLimit)
		return
	}
	cpu0 := netio.ReadCPU()
	var wg sync.WaitGroup
	if dir == proto.DirForward || dir == proto.DirBidir {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stat, _ := ctrlFrames(c, time.Now().Add(dur+warm+5*time.Second))
			st := collectUDP(s.udpPkts, stat, time.Now().Add(dur+warm+5*time.Second), warm)
			cpu := uint16(netio.Permille(cpu0, netio.ReadCPU()))
			_ = proto.Write(c, proto.Result, udpResult(st, cpu))
		}()
	}
	if dir == proto.DirReverse || dir == proto.DirBidir {
		ra, _ := c.RemoteAddr().(*net.TCPAddr)
		if ra != nil && port != 0 {
			r.mu.Lock()
			uc := r.udp
			r.mu.Unlock()
			n := sendUDP(uc, &net.UDPAddr{IP: ra.IP, Port: int(port)}, s.id, int(rate), int(size), time.Now().Add(dur+warm))
			cpu := uint16(netio.Permille(cpu0, netio.ReadCPU()))
			_ = proto.Write(c, proto.SenderStat, proto.NewBuilder().U64(proto.TPktsSent, n).U16(proto.TCPU, cpu))
		}
	}
	wg.Wait()
}

func b2u(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}
