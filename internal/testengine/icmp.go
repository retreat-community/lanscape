package testengine

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"sort"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"github.com/retreat-community/lanscape/internal/netio"
)

// PingParams configure ICMP echo probes.
type PingParams struct {
	Dev      string
	Src, Dst net.IP
	Count    int
	Interval time.Duration
	Size     int  // IP packet size in bytes
	DF       bool // set Don't Fragment (PMTU probe)
	// TCPPorts are tried with TCP connect probes when no ICMP socket can be opened (no
	// CAP_NET_RAW and no ping_group_range); not used for PMTU probes.
	TCPPorts []int
}

// PingResult summarises ICMP probes.
type PingResult struct {
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
	Sent     int    `json:"sent"`
	Recv     int    `json:"recv"`
	RTTMinUS uint32 `json:"rtt_min_us"`
	RTTAvgUS uint32 `json:"rtt_avg_us"`
	RTTP95US uint32 `json:"rtt_p95_us"`
	RTTMaxUS uint32 `json:"rtt_max_us"`
	JitterUS uint32 `json:"jitter_us"`
	Method   string `json:"method,omitempty"` // "tcp" when measured with TCP connects
}

type icmpConn struct {
	pc  net.PacketConn
	raw bool
}

func openICMP(ctx context.Context, p PingParams) (*icmpConn, error) {
	src := "0.0.0.0"
	if p.Src != nil {
		src = p.Src.String()
	}
	lc := net.ListenConfig{Control: netio.BindControl(p.Dev, p.DF)}
	pc, err := lc.ListenPacket(ctx, "ip4:icmp", src)
	if err == nil {
		return &icmpConn{pc: pc, raw: true}, nil
	}
	// unprivileged ICMP datagram socket (net.ipv4.ping_group_range); no device binding
	upc, uerr := icmp.ListenPacket("udp4", src)
	if uerr != nil {
		return nil, errors.Join(err, uerr)
	}
	return &icmpConn{pc: upc}, nil
}

// Ping sends ICMP echo requests bound to the device and collects RTT statistics.
func Ping(ctx context.Context, p PingParams) PingResult {
	if p.Count <= 0 {
		p.Count = 10
	}
	if p.Interval <= 0 {
		p.Interval = 100 * time.Millisecond
	}
	if p.Size < 28+16 {
		p.Size = 84
	}
	res := PingResult{Status: StatusOK}
	c, err := openICMP(ctx, p)
	if err != nil {
		if !errors.Is(err, syscall.ENODEV) && !p.DF && len(p.TCPPorts) > 0 {
			return TCPPing(ctx, p)
		}
		res.Status, res.Message = StatusInternal, err.Error()
		if errors.Is(err, syscall.ENODEV) {
			res.Status = StatusNoDevice
		}
		return res
	}
	defer c.pc.Close()
	var idb [2]byte
	_, _ = rand.Read(idb[:])
	id := int(idb[0])<<8 | int(idb[1])
	payload := make([]byte, p.Size-28)
	for i := range payload {
		payload[i] = byte(i)
	}
	var dst net.Addr = &net.IPAddr{IP: p.Dst}
	if !c.raw {
		dst = &net.UDPAddr{IP: p.Dst}
	}
	sentAt := make([]time.Time, p.Count)
	var rtts []uint32
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 65536)
		seen := make([]bool, p.Count)
		for {
			_ = c.pc.SetReadDeadline(time.Now().Add(p.Interval*time.Duration(p.Count) + time.Second))
			n, from, err := c.pc.ReadFrom(buf)
			if err != nil {
				return
			}
			m, err := icmp.ParseMessage(1, buf[:n])
			if err != nil || m.Type != ipv4.ICMPTypeEchoReply {
				continue
			}
			e, ok := m.Body.(*icmp.Echo)
			if !ok || (c.raw && e.ID != id) || e.Seq < 0 || e.Seq >= p.Count || seen[e.Seq] {
				continue
			}
			var fip net.IP
			switch a := from.(type) {
			case *net.IPAddr:
				fip = a.IP
			case *net.UDPAddr:
				fip = a.IP
			}
			if !fip.Equal(p.Dst) || sentAt[e.Seq].IsZero() {
				continue
			}
			seen[e.Seq] = true
			rtts = append(rtts, uint32(time.Since(sentAt[e.Seq]).Microseconds()))
			if len(rtts) == p.Count {
				return
			}
		}
	}()
	for i := 0; i < p.Count; i++ {
		msg := icmp.Message{Type: ipv4.ICMPTypeEcho, Body: &icmp.Echo{ID: id, Seq: i, Data: payload}}
		b, _ := msg.Marshal(nil)
		sentAt[i] = time.Now()
		if _, err := c.pc.WriteTo(b, dst); err != nil {
			if errors.Is(err, syscall.EMSGSIZE) {
				res.Status, res.Message = StatusMsgSize, "local MTU is smaller than the probe"
				c.pc.Close()
				<-done
				return res
			}
			sentAt[i] = time.Time{}
		} else {
			res.Sent++
		}
		select {
		case <-ctx.Done():
			c.pc.Close()
			<-done
			res.Status = StatusTimeout
			return res
		case <-done:
		case <-time.After(p.Interval):
		}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		c.pc.Close()
		<-done
	}
	summarize(&res, rtts)
	return res
}

// summarize fills received count, RTT statistics and jitter (rtts in send order).
func summarize(res *PingResult, rtts []uint32) {
	res.Recv = len(rtts)
	if res.Recv == 0 {
		res.Status = StatusUnreachable
		return
	}
	var j float64
	for i := 1; i < len(rtts); i++ {
		d := float64(rtts[i]) - float64(rtts[i-1])
		if d < 0 {
			d = -d
		}
		j += d
	}
	if len(rtts) > 1 {
		res.JitterUS = uint32(j / float64(len(rtts)-1))
	}
	sorted := append([]uint32(nil), rtts...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a] < sorted[b] })
	var sum uint64
	for _, r := range sorted {
		sum += uint64(r)
	}
	res.RTTMinUS, res.RTTMaxUS = sorted[0], sorted[len(sorted)-1]
	res.RTTAvgUS = uint32(sum / uint64(len(sorted)))
	res.RTTP95US = sorted[(len(sorted)*95+99)/100-1]
}

// TCPPing measures reachability and RTT with TCP connects: a completed handshake and a refused
// connection (RST) both prove the host answers. The first port that answers is used.
func TCPPing(ctx context.Context, p PingParams) PingResult {
	res := PingResult{Status: StatusOK, Method: "tcp"}
	if p.Count <= 0 {
		p.Count = 10
	}
	if p.Interval <= 0 {
		p.Interval = 100 * time.Millisecond
	}
	var local net.Addr
	if p.Src != nil {
		local = &net.TCPAddr{IP: p.Src}
	}
	d := net.Dialer{Timeout: time.Second, LocalAddr: local, Control: netio.BindControl(p.Dev, false)}
	ports := p.TCPPorts
	var rtts []uint32
	for i := 0; i < p.Count; i++ {
		for k, port := range ports {
			start := time.Now()
			c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(p.Dst.String(), strconv.Itoa(port)))
			if err == nil || errors.Is(err, syscall.ECONNREFUSED) {
				rtt := time.Since(start)
				if c != nil {
					c.Close()
				}
				rtts = append(rtts, uint32(rtt.Microseconds()))
				ports = ports[k : k+1]
				break
			}
		}
		res.Sent++
		select {
		case <-ctx.Done():
			res.Status = StatusTimeout
			return res
		case <-time.After(p.Interval):
		}
	}
	summarize(&res, rtts)
	return res
}
