//go:build !linux

package netio

import (
	"net"
	"syscall"
)

// Interfaces lists interfaces using the standard library.
func Interfaces() ([]Iface, error) { return stdInterfaces() }

// LinkSpeed is unknown on this platform.
func LinkSpeed(string) int { return 0 }

// NormalizeSpeed maps driver "unknown" values to 0.
func NormalizeSpeed(raw uint32) int {
	if raw == 0 || raw == 0xffff || raw == 0xffffffff {
		return 0
	}
	return int(raw)
}

// ReadCounters is not supported; results are reported as unverified.
func ReadCounters(string) (Counters, error) { return Counters{}, ErrUnsupported }

// RouteDev is not supported on this platform.
func RouteDev(net.IP, net.IP) (string, error) { return "", ErrUnsupported }

// OutQueue is not available; callers skip the drain wait.
func OutQueue(syscall.RawConn) int { return 0 }

// portable fallback for interface listing
func stdInterfaces() ([]Iface, error) {
	nifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]Iface, 0, len(nifs))
	for _, n := range nifs {
		i := Iface{
			Name: n.Name, Index: n.Index, MAC: n.HardwareAddr.String(), MTU: n.MTU,
			Up: n.Flags&net.FlagUp != 0, Carrier: n.Flags&net.FlagRunning != 0, Kind: "physical",
		}
		if n.Flags&net.FlagLoopback != 0 {
			i.Kind = "loopback"
		}
		addrs, _ := n.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				ones, _ := ipn.Mask.Size()
				i.Addrs = append(i.Addrs, Addr{IP: ipn.IP.String(), Prefix: ones})
			}
		}
		out = append(out, i)
	}
	return out, nil
}
