//go:build darwin

package netio

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// BindControl binds the socket to dev with IP_BOUND_IF.
func BindControl(dev string, df bool) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			if dev != "" {
				ifi, e := net.InterfaceByName(dev)
				if e != nil {
					serr = e
					return
				}
				serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, ifi.Index)
			}
			if df && serr == nil {
				serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_DONTFRAG, 1)
			}
		})
		if err != nil {
			return err
		}
		return serr
	}
}
