package discovery

import (
	"net"
	"net/url"
	"strings"
)

// Types shared by all builds; the sources themselves are not part of small (OpenWrt) builds.

// Proxmox kinds.
const (
	KindVM = "vm" // QEMU guest
	KindCT = "ct" // LXC container
)

// ProxmoxConfig is a read-only API token (PVEAuditor role is enough).
type ProxmoxConfig struct {
	URL      string // https://pve:8006
	Token    string // user@realm!tokenid=uuid
	Insecure bool   // self-signed PVE certificate
}

// NIC is a guest network interface from the VM/CT configuration.
type NIC struct {
	Name   string `json:"name"`             // net0
	MAC    string `json:"mac"`              // lower case
	Bridge string `json:"bridge,omitempty"` // vmbr0
	Model  string `json:"model,omitempty"`  // virtio, e1000 …
	VLAN   int    `json:"vlan,omitempty"`
}

// Reverse-proxy source and kind.
const (
	SourceProxy    = "proxy"
	KindProxyRoute = "proxy_route"
)

// ProxyConfig selects reverse proxies to read.
type ProxyConfig struct {
	TraefikURL string // http://traefik:8080 (API enabled)
	CaddyAdmin string // http://127.0.0.1:2019
	NginxDir   string // /etc/nginx
}

// Enabled reports whether any proxy is configured.
func (c ProxyConfig) Enabled() bool {
	return c.TraefikURL != "" || c.CaddyAdmin != "" || c.NginxDir != ""
}

// BackendHostPort normalises a backend ("http://127.0.0.1:3000/", "gitea:3000") to host:port.
func BackendHostPort(b string) string {
	if !strings.Contains(b, "://") {
		b = "http://" + b
	}
	u, err := url.Parse(b)
	if err != nil || u.Host == "" {
		return ""
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	return net.JoinHostPort(host, port)
}

// DNS source and kind.
const (
	SourceDNS     = "dns"
	KindDNSRecord = "dns_record"
)

// DNSConfig lists local DNS servers whose records name devices and services.
type DNSConfig struct {
	PiholeURL       string // http://pi.hole
	PiholePassword  string // v6 app password, or the v5 API token
	AdGuardURL      string
	AdGuardUser     string
	AdGuardPassword string
	TechnitiumURL   string
	TechnitiumToken string
	AXFR            []string // "server[:53]/zone" pairs whose zone transfer is allowed
}

// Enabled reports whether a DNS server is configured.
func (c DNSConfig) Enabled() bool {
	return c.PiholeURL != "" || c.AdGuardURL != "" || c.TechnitiumURL != "" || len(c.AXFR) > 0
}

// Host hardware source and kinds (dashboard widgets: UPS, SMART, storage pools).
const (
	SourceHost = "host"
	KindUPS    = "ups"
	KindDisk   = "disk"
	KindPool   = "pool"
)

// HostConfig selects hardware sources.
type HostConfig struct {
	NUT   string // upsd address, e.g. 127.0.0.1:3493
	SMART bool   // smartctl
	ZFS   bool   // zpool
}

// Enabled reports whether a hardware source is on.
func (c HostConfig) Enabled() bool { return c.NUT != "" || c.SMART || c.ZFS }
