package testengine

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestTCPPing(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	open := l.Addr().(*net.TCPAddr).Port
	// a closed port answers with RST, which proves the host is up
	closed, _ := net.Listen("tcp4", "127.0.0.1:0")
	refused := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	for _, ports := range [][]int{{open}, {refused}} {
		r := TCPPing(context.Background(), PingParams{Dst: net.IPv4(127, 0, 0, 1), Count: 3, Interval: 10 * time.Millisecond, TCPPorts: ports})
		if r.Status != StatusOK || r.Recv != 3 || r.Method != "tcp" || r.RTTMaxUS < r.RTTMinUS {
			t.Errorf("ports %v: %+v", ports, r)
		}
	}
}
