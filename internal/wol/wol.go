// Package wol sends Wake-on-LAN magic packets.
package wol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// MagicPacket builds the 102-byte payload: 6×0xFF followed by the MAC address 16 times.
func MagicPacket(mac string) ([]byte, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return nil, fmt.Errorf("wol: bad mac address %q", mac)
	}
	p := bytes.Repeat([]byte{0xff}, 6)
	for i := 0; i < 16; i++ {
		p = append(p, hw...)
	}
	return p, nil
}

// Target is one broadcast destination.
type Target struct {
	Iface     string `json:"iface"`
	Broadcast string `json:"broadcast"`
}

// Targets lists the directed broadcast addresses of the up IPv4 interfaces (loopback excluded).
// When ip is set, only the interfaces whose subnet contains it are returned.
func Targets(ip string) ([]Target, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	want := net.ParseIP(ip)
	var out []Target
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil {
				continue
			}
			if want != nil && !n.Contains(want) {
				continue
			}
			out = append(out, Target{Iface: ifc.Name, Broadcast: broadcast(n).String()})
		}
	}
	return out, nil
}

func broadcast(n *net.IPNet) net.IP {
	ip := n.IP.To4()
	mask := net.IP(n.Mask).To4()
	if mask == nil {
		mask = net.IP(n.Mask[len(n.Mask)-4:])
	}
	b := make(net.IP, 4)
	for i := range b {
		b[i] = ip[i] | ^mask[i]
	}
	return b
}

// Send broadcasts the magic packet for mac to UDP port 9 on every target (all interfaces when
// targets is empty) and returns the interfaces it went out on.
func Send(ctx context.Context, mac string, targets []Target) ([]string, error) {
	p, err := MagicPacket(mac)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		if targets, err = Targets(""); err != nil {
			return nil, err
		}
	}
	if len(targets) == 0 {
		return nil, errors.New("wol: no broadcast-capable interface")
	}
	var sent, errs []string
	for _, t := range targets {
		if err := sendTo(ctx, net.JoinHostPort(t.Broadcast, "9"), p); err != nil {
			errs = append(errs, t.Iface+": "+err.Error())
			continue
		}
		sent = append(sent, t.Iface)
	}
	if len(sent) == 0 {
		return nil, fmt.Errorf("wol: %s", strings.Join(errs, "; "))
	}
	return sent, nil
}

func sendTo(ctx context.Context, addr string, p []byte) error {
	lc := net.ListenConfig{Control: allowBroadcast}
	c, err := lc.ListenPacket(ctx, "udp4", ":0")
	if err != nil {
		return err
	}
	defer c.Close()
	dst, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}
	_, err = c.WriteTo(p, dst)
	return err
}
