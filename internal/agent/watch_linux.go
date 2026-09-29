//go:build linux

package agent

import (
	"context"

	"github.com/vishvananda/netlink"
)

// watchChanges signals link and address changes reported by netlink.
func watchChanges(ctx context.Context) <-chan struct{} {
	out := make(chan struct{}, 1)
	links := make(chan netlink.LinkUpdate, 16)
	addrs := make(chan netlink.AddrUpdate, 16)
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(done)
	}()
	_ = netlink.LinkSubscribe(links, done)
	_ = netlink.AddrSubscribe(addrs, done)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-links:
				if !ok {
					return
				}
			case _, ok := <-addrs:
				if !ok {
					return
				}
			}
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out
}
