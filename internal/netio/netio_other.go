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
