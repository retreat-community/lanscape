package agent

import (
	"context"
	"time"

	"github.com/retreat-community/lanscape/internal/netio"
	"github.com/retreat-community/lanscape/internal/proto"
)

// trafficKinds are the interfaces whose load is worth reporting (not veth, tun of containers …).
var trafficKinds = map[string]bool{"physical": true, "bridge": true, "bond": true, "vlan": true, "wireguard": true, "ppp": true,
	"macvlan": true}

// RunTraffic reports the throughput of the interfaces every period (dashboard "WAN/LAN traffic",
// /metrics). It samples byte counters, so it costs nothing on the links themselves.
func (a *Agent) RunTraffic(ctx context.Context, every time.Duration) {
	if every <= 0 {
		return
	}
	type sample struct {
		at time.Time
		c  netio.Counters
	}
	last := map[string]sample{}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ifs, err := netio.Interfaces()
		if err != nil {
			continue
		}
		now := time.Now()
		msg := proto.TrafficMsg{At: now.UnixMilli()}
		for _, ifc := range netio.Filter(ifs, a.cfg.Exclude) {
			if !ifc.Up || !trafficKinds[ifc.Kind] {
				continue
			}
			c, err := netio.ReadCounters(ifc.Name)
			if err != nil {
				continue
			}
			prev, ok := last[ifc.Name]
			last[ifc.Name] = sample{now, c}
			secs := now.Sub(prev.at).Seconds()
			if !ok || secs <= 0 || c.RX < prev.c.RX || c.TX < prev.c.TX {
				continue // first sample or counter reset
			}
			msg.Ifaces = append(msg.Ifaces, proto.IfaceTraffic{Name: ifc.Name,
				RXbps: uint64(float64(c.RX-prev.c.RX) * 8 / secs), TXbps: uint64(float64(c.TX-prev.c.TX) * 8 / secs)})
		}
		if len(msg.Ifaces) > 0 {
			_ = a.Send(ctx, proto.MsgTraffic, msg)
		}
	}
}
