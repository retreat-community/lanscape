package testengine

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/retreat-community/lanscape/internal/proto"
)

func startResponder(t *testing.T) (*Responder, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	r := NewResponder(nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = r.Serve(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port))) }()
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return r, port
}

func params(t *testing.T, r *Responder, port int, kind, dir uint8) Params {
	t.Helper()
	id, tok, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	r.Grant(id, tok, time.Minute)
	return Params{AgentID: "test", Src: net.IPv4(127, 0, 0, 1), Dst: net.IPv4(127, 0, 0, 1), Port: port,
		RunID: id, Token: tok, Kind: kind, Dir: dir, Streams: 2, Duration: 400 * time.Millisecond,
		Warmup: 100 * time.Millisecond, SkipRoute: true, UDPRateKbps: 20_000, PktSize: 1000}
}

func TestEcho(t *testing.T) {
	r, port := startResponder(t)
	res := Run(context.Background(), params(t, r, port, proto.KindEcho, proto.DirForward))
	if res.Status != StatusOK || res.Recv != 10 || res.RTTAvgUS == 0 {
		t.Fatalf("%+v", res)
	}
}

func TestTCPDirections(t *testing.T) {
	r, port := startResponder(t)
	for _, dir := range []uint8{proto.DirForward, proto.DirReverse, proto.DirBidir} {
		res := Run(context.Background(), params(t, r, port, proto.KindTCP, dir))
		if res.Status != StatusOK || res.BPS == 0 {
			t.Errorf("dir %d: %+v", dir, res)
		}
		if dir == proto.DirBidir && res.BPSReverse == 0 {
			t.Errorf("bidir without reverse rate: %+v", res)
		}
	}
}

func TestUDPDirections(t *testing.T) {
	r, port := startResponder(t)
	for _, dir := range []uint8{proto.DirForward, proto.DirReverse, proto.DirBidir} {
		res := Run(context.Background(), params(t, r, port, proto.KindUDP, dir))
		if res.Status != StatusOK || res.BPS == 0 || res.Sent == 0 {
			t.Errorf("dir %d: %+v", dir, res)
			continue
		}
		// 20 Mbit/s offered: measured rate must be in the right ballpark
		if res.BPS < 10_000_000 || res.BPS > 30_000_000 {
			t.Errorf("dir %d: unexpected rate %d", dir, res.BPS)
		}
	}
}

func TestAuthFailures(t *testing.T) {
	r, port := startResponder(t)
	p := params(t, r, port, proto.KindEcho, proto.DirForward)
	p.Token[0] ^= 1
	if res := Run(context.Background(), p); res.Status != StatusAuthFail {
		t.Errorf("wrong token: %+v", res)
	}
	p = params(t, r, port, proto.KindEcho, proto.DirForward)
	p.RunID++ // no grant: connection is closed without a reply
	if res := Run(context.Background(), p); res.Status != StatusProtocol {
		t.Errorf("unknown run: %+v", res)
	}
	p = params(t, r, port, proto.KindTCP, proto.DirForward)
	p.Duration = time.Hour
	if res := Run(context.Background(), p); res.Status != StatusLimit {
		t.Errorf("limits: %+v", res)
	}
}

func TestRefused(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	res := Run(context.Background(), Params{Dst: net.IPv4(127, 0, 0, 1), Port: port, Kind: proto.KindEcho, SkipRoute: true})
	if res.Status != StatusRefused {
		t.Errorf("%+v", res)
	}
}
