//go:build !linux

package discovery

import "errors"

// Sockets is implemented on Linux only.
func Sockets() ([]Item, error) {
	return nil, errors.New("listening socket discovery is supported on Linux only")
}
