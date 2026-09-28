package testengine

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/retreat-community/lanscape/internal/netio"
	"github.com/retreat-community/lanscape/internal/proto"
)

const chunk = 128 << 10

var fill = make([]byte, chunk)

type rxStats struct {
	total    uint64
	counted  uint64
	windowUS uint64
}

// receive reads all connections until EOF or deadline. Bytes arriving earlier than the
// first byte plus warm-up are not counted.
func receive(conns []net.Conn, deadline time.Time, warm time.Duration) rxStats {
	var total, counted atomic.Uint64
	var mu sync.Mutex
	var first, last time.Time
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			buf := make([]byte, chunk)
			_ = c.SetReadDeadline(deadline)
			for {
				n, err := c.Read(buf)
				if n > 0 {
					now := time.Now()
					total.Add(uint64(n))
					mu.Lock()
					if first.IsZero() {
						first = now
					}
					if now.Sub(first) >= warm {
						counted.Add(uint64(n))
						if now.After(last) {
							last = now
						}
					}
					mu.Unlock()
				}
				if err != nil {
					return
				}
			}
		}(c)
	}
	wg.Wait()
	st := rxStats{total: total.Load(), counted: counted.Load()}
	if ws := first.Add(warm); !first.IsZero() && last.After(ws) {
		st.windowUS = uint64(last.Sub(ws).Microseconds())
	}
	return st
}

// send writes on all connections until end, half-closes them and waits until the kernel
// has transmitted the queued data so that interface counters include it.
func send(conns []net.Conn, end time.Time) uint64 {
	var total atomic.Uint64
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			_ = c.SetWriteDeadline(end)
			for time.Now().Before(end) {
				n, err := c.Write(fill)
				total.Add(uint64(n))
				if err != nil {
					break
				}
			}
			if tc, ok := c.(*net.TCPConn); ok {
				_ = tc.CloseWrite()
			}
		}(c)
	}
	wg.Wait()
	drain(conns, time.Now().Add(5*time.Second))
	return total.Load()
}

func drain(conns []net.Conn, deadline time.Time) {
	for time.Now().Before(deadline) {
		pending := false
		for _, c := range conns {
			tc, ok := c.(*net.TCPConn)
			if !ok {
				continue
			}
			rc, err := tc.SyscallConn()
			if err == nil && netio.OutQueue(rc) > 0 {
				pending = true
			}
		}
		if !pending {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sendUDP paces datagrams at rateKbps until end and returns the number sent.
func sendUDP(c *net.UDPConn, to *net.UDPAddr, session uint32, rateKbps, size int, end time.Time) uint64 {
	if size < udpHeaderLen {
		size = 1200
	}
	if rateKbps <= 0 {
		rateKbps = 100_000
	}
	buf := make([]byte, size)
	interval := time.Duration(int64(size) * 8 * int64(time.Millisecond) / int64(rateKbps))
	var seq uint32
	next := time.Now()
	for time.Now().Before(end) {
		putUDPHeader(buf, session, seq, nowUS())
		var err error
		if to != nil {
			_, err = c.WriteToUDP(buf, to)
		} else {
			_, err = c.Write(buf)
		}
		if err == nil {
			seq++
		}
		next = next.Add(interval)
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		}
	}
	return uint64(seq)
}

// ctrlFrames reads control frames until deadline and dispatches SENDER_STAT packet
// counts and RESULT payloads, so that concurrent consumers never race on the reader.
func ctrlFrames(ctrl net.Conn, deadline time.Time) (<-chan uint64, <-chan proto.TLV) {
	stat := make(chan uint64, 1)
	res := make(chan proto.TLV, 1)
	go func() {
		defer close(stat)
		defer close(res)
		_ = ctrl.SetReadDeadline(deadline)
		defer func() { _ = ctrl.SetReadDeadline(time.Time{}) }()
		gotStat, gotRes := false, false
		for !gotStat || !gotRes {
			f, err := proto.Read(ctrl)
			if err != nil {
				return
			}
			switch f.Type {
			case proto.SenderStat:
				n, _ := f.Payload.U64(proto.TPktsSent)
				stat <- n
				gotStat = true
			case proto.Result:
				res <- f.Payload
				gotRes = true
			case proto.Bye:
				return
			}
		}
	}()
	return stat, res
}

// collectUDP gathers datagrams until the sender reports completion (stat) plus a short
// grace period, or until deadline.
func collectUDP(pkts <-chan udpPkt, stat <-chan uint64, deadline time.Time, warm time.Duration) udpStats {
	var st udpStats
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	var grace <-chan time.Time
	for {
		select {
		case p, ok := <-pkts:
			if !ok {
				pkts = nil
				continue
			}
			st.add(p.n, p.seq, p.ts, warm)
		case n, ok := <-stat:
			if !ok {
				stat = nil
				continue
			}
			st.sent = n
			grace = time.After(300 * time.Millisecond)
		case <-grace:
			return st
		case <-timer.C:
			return st
		}
	}
}

func udpResult(st udpStats, cpu uint16) *proto.Builder {
	sent := st.sent
	if sent == 0 && st.haveSeq {
		sent = uint64(st.maxSeq) + 1
	}
	lost := uint64(0)
	if sent > st.recv {
		lost = sent - st.recv
	}
	return proto.NewBuilder().U64(proto.TBytes, st.counted).U64(proto.TWindowUS, st.window()).
		U64(proto.TPktsSent, sent).U64(proto.TPktsRecv, st.recv).U64(proto.TLost, lost).
		U32(proto.TJitterUS, uint32(st.jitter)).U16(proto.TCPU, cpu)
}
