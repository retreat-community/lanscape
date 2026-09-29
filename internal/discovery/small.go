//go:build lanscape_small

package discovery

import (
	"context"
	"errors"
)

// Small (OpenWrt) builds leave out the sources that routers do not need.
var errNotInBuild = errors.New("this source is not included in the OpenWrt build")

// Kubernetes is not included in small builds.
func Kubernetes(context.Context, string) ([]Item, error) { return nil, errNotInBuild }

// Docker is not included in small builds.
func Docker(context.Context, string) ([]Item, error) { return nil, errNotInBuild }

// Proxmox is not included in small builds.
func Proxmox(context.Context, ProxmoxConfig) ([]Item, error) { return nil, errNotInBuild }

// Proxies is not included in small builds.
func Proxies(context.Context, ProxyConfig) ([]Item, error) { return nil, errNotInBuild }

// DNSRecords is not included in small builds.
func DNSRecords(context.Context, DNSConfig) ([]Item, error) { return nil, errNotInBuild }

// Hardware is not included in small builds.
func Hardware(context.Context, HostConfig) ([]Item, error) { return nil, errNotInBuild }

// Libvirt is not included in small builds.
func Libvirt(context.Context) ([]Item, error) { return nil, errNotInBuild }

// LibvirtAvailable is false in small builds.
func LibvirtAvailable() bool { return false }

// Restart is not included in small builds.
func Restart(context.Context, Config, string, string) (string, error) { return "", errNotInBuild }
