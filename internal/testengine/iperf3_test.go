package testengine

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

// fakeIperf3 serves one test the way "iperf3 -s" does.
func fakeIperf3(t *testing.T, l net.Listener, done chan<- map[string]any) {
	ctl, err := l.Accept()
	if err != nil {
		return
	}
	defer ctl.Close()
	cookie := make([]byte, ipCookieSize)
	if _, err := io.ReadFull(ctl, cookie); err != nil {
		t.Error(err)
		return
	}
	_, _ = ctl.Write([]byte{ipParamExchange})
	var params map[string]any
	if err := ipReadJSON(ctl, &params); err != nil {
		t.Error(err)
		return
	}
	_, _ = ctl.Write([]byte{ipCreateStreams})
	n := int(params["parallel"].(float64))
	streams := make([]net.Conn, n)
	for i := range streams {
		c, err := l.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		got := make([]byte, ipCookieSize)
		if _, err := io.ReadFull(c, got); err != nil || string(got) != string(cookie) {
			t.Errorf("stream cookie %q", got)
		}
		streams[i] = c
	}
	_, _ = ctl.Write([]byte{ipTestStart, ipTestRunning})
	reverse, _ := params["reverse"].(bool)
	counts := make([]uint64, n)
	stop := make(chan struct{})
	finished := make(chan struct{}, n)
	for i, c := range streams {
		go func(i int, c net.Conn) {
			defer func() { finished <- struct{}{} }()
			buf := make([]byte, 64*1024)
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = c.SetDeadline(time.Now().Add(100 * time.Millisecond))
				var k int
				if reverse {
					k, _ = c.Write(buf)
				} else {
					k, _ = c.Read(buf)
				}
				counts[i] += uint64(k)
			}
		}(i, c)
	}
	var st [1]byte
	if _, err := io.ReadFull(ctl, st[:]); err != nil || st[0] != ipTestEnd {
		t.Errorf("test end: %v %v", st, err)
	}
	close(stop)
	for range streams {
		<-finished
	}
	_, _ = ctl.Write([]byte{ipExchangeResults})
	var theirs ipResults
	if err := ipReadJSON(ctl, &theirs); err != nil {
		t.Error(err)
	}
	mine := ipResults{HasRetransmit: 1}
	for i := range streams {
		id := 1
		if i > 0 {
			id = i + 2
		}
		if theirs.Streams[i].ID != id {
			t.Errorf("stream id %d, want %d", theirs.Streams[i].ID, id)
		}
		mine.Streams = append(mine.Streams, ipStreamResult{ID: id, Bytes: counts[i], Retransmits: 2})
	}
	_ = ipWriteJSON(ctl, mine)
	_, _ = ctl.Write([]byte{ipDisplayResults})
	_, _ = io.ReadFull(ctl, st[:])
	for _, c := range streams {
		_ = c.Close()
	}
	params["done"] = st[0] == ipIperfDone
	done <- params
}

func TestIperf3(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan map[string]any, 1)
		go fakeIperf3(t, l, done)
		port := l.Addr().(*net.TCPAddr).Port
		r, err := Iperf3(context.Background(), Iperf3Options{Host: "127.0.0.1", Port: port, Duration: time.Second, Streams: 2, Reverse: reverse})
		if err != nil {
			t.Fatalf("reverse=%v: %v", reverse, err)
		}
		params := <-done
		_ = l.Close()
		if r.BPS == 0 || r.Streams != 2 || params["parallel"] != float64(2) || params["time"] != float64(1) || params["done"] != true {
			t.Errorf("reverse=%v: %+v params %v", reverse, r, params)
		}
		if reverse && (r.Retransmits != 4 || params["reverse"] != true) {
			t.Errorf("reverse: retransmits %d, params %v", r.Retransmits, params)
		}
		b, _ := json.Marshal(r)
		t.Logf("%s", b)
	}
}

func TestIperf3Busy(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err == nil {
			_, _ = io.ReadFull(c, make([]byte, ipCookieSize))
			_, _ = c.Write([]byte{byte(0xff)}) // ACCESS_DENIED
			_ = c.Close()
		}
	}()
	_, err = Iperf3(context.Background(), Iperf3Options{Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, Duration: time.Second})
	if err == nil || err.Error() != "iperf3: the server is busy with another test" {
		t.Errorf("busy server: %v", err)
	}
}
