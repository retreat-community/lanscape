//go:build !linux && !darwin

package netio

import "syscall"

// BindControl cannot bind to a device on this platform; the source address bind is used.
func BindControl(string, bool) func(network, address string, c syscall.RawConn) error {
	return func(string, string, syscall.RawConn) error { return nil }
}
