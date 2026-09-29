// Package discovery collects services on an agent's host: listening sockets, Docker
// containers and Kubernetes resources. Each source is enabled separately; results are
// fingerprinted and sent to the server, which merges them into the "Found" queue.
package discovery

import (
	"context"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/retreat-community/lanscape/internal/fingerprint"
)

// Source names.
const (
	SourceSockets = "sockets"
	SourceDocker  = "docker"
	SourceK8s     = "k8s"
	SourceProxmox = "proxmox"
)

// Item kinds.
const (
	KindSocket      = "socket"
	KindContainer   = "container"
	KindService     = "k8s_service"
	KindIngress     = "k8s_ingress"
	KindHTTPRoute   = "k8s_httproute"
	KindDeployment  = "k8s_deployment"
	KindStatefulSet = "k8s_statefulset"
	KindDaemonSet   = "k8s_daemonset"
	KindPVC         = "k8s_pvc"
)

// Port is a published or exposed port.
type Port struct {
	IP       string `json:"ip,omitempty"`
	Port     int    `json:"port"`
	Target   int    `json:"target,omitempty"` // container / target port
	Proto    string `json:"proto"`
	NodePort int    `json:"node_port,omitempty"`
}

// CertInfo describes a TLS certificate seen on an endpoint.
type CertInfo struct {
	Names    []string `json:"names,omitempty"`
	NotAfter int64    `json:"not_after"`
	Issuer   string   `json:"issuer,omitempty"`
}

// Item is one discovered object.
type Item struct {
	Key       string             `json:"key"` // stable within the source
	Kind      string             `json:"kind"`
	Name      string             `json:"name"`
	Namespace string             `json:"namespace,omitempty"`
	Proto     string             `json:"proto,omitempty"`
	Addr      string             `json:"addr,omitempty"` // listen address of a socket
	Port      int                `json:"port,omitempty"`
	Process   string             `json:"process,omitempty"`
	User      string             `json:"user,omitempty"`
	PID       int                `json:"pid,omitempty"`
	Image     string             `json:"image,omitempty"`
	Project   string             `json:"project,omitempty"` // compose project
	State     string             `json:"state,omitempty"`   // running, exited, ready, degraded …
	Health    string             `json:"health,omitempty"`  // healthy, unhealthy, starting
	Ready     string             `json:"ready,omitempty"`   // "2/3"
	IPs       []string           `json:"ips,omitempty"`
	Ports     []Port             `json:"ports,omitempty"`
	Hosts     []string           `json:"hosts,omitempty"`    // ingress / route host names
	TLS       bool               `json:"tls,omitempty"`      // ingress with TLS
	Backends  []string           `json:"backends,omitempty"` // "ns/service:port"
	Selector  map[string]string  `json:"selector,omitempty"`
	PodLabels map[string]string  `json:"pod_labels,omitempty"`
	Labels    map[string]string  `json:"labels,omitempty"`
	Owner     string             `json:"owner,omitempty"` // container id of a socket, workload of a pod …
	URL       string             `json:"url,omitempty"`   // probed HTTP endpoint
	Title     string             `json:"title,omitempty"`
	App       *fingerprint.Match `json:"app,omitempty"`
	Cert      *CertInfo          `json:"cert,omitempty"`
	NICs      []NIC              `json:"nics,omitempty"` // VM/CT network interfaces
}

// SourceReport is the result of one source.
type SourceReport struct {
	Source string `json:"source"`
	Error  string `json:"error,omitempty"`
	Items  []Item `json:"items"`
}

// Report is sent to the server as a "discovery" message.
type Report struct {
	At      int64          `json:"at"`
	Sources []SourceReport `json:"sources"`
}

// Config selects sources.
type Config struct {
	Sockets      bool
	Docker       bool
	DockerSocket string // default /var/run/docker.sock
	K8s          bool
	Kubeconfig   string // empty = in-cluster
	Proxmox      ProxmoxConfig
	OpenWrt      bool // DHCP leases, Wi-Fi clients, port forwards, SQM (on the router)
	Proxy        ProxyConfig
	DNS          DNSConfig
	MDNS         bool // DNS-SD browse on the local links
	SSDP         bool // UPnP search
	Probe        bool // HTTP fingerprinting of found endpoints
	Interval     time.Duration
	Signatures   []string // extra signature files
}

// Collector runs the enabled sources.
type Collector struct {
	cfg    Config
	log    *slog.Logger
	lib    *fingerprint.Library
	prober *fingerprint.Prober

	mu    sync.Mutex
	cache map[string]probeEntry // endpoint -> last probe
}

type probeEntry struct {
	at    time.Time
	url   string
	title string
	app   *fingerprint.Match
	cert  *CertInfo
}

// New creates a collector.
func New(cfg Config, log *slog.Logger) (*Collector, error) {
	if cfg.DockerSocket == "" {
		cfg.DockerSocket = "/var/run/docker.sock"
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	lib, err := fingerprint.Load(cfg.Signatures...)
	if err != nil {
		return nil, err
	}
	return &Collector{cfg: cfg, log: log, lib: lib, prober: fingerprint.NewProber(lib), cache: map[string]probeEntry{}}, nil
}

// Enabled reports whether any source is on.
func (c *Collector) Enabled() bool {
	return c.cfg.Sockets || c.cfg.Docker || c.cfg.K8s || c.cfg.Proxmox.URL != "" || c.cfg.MDNS || c.cfg.SSDP || c.cfg.OpenWrt ||
		c.cfg.Proxy.Enabled() || c.cfg.DNS.Enabled()
}

// Interval is the collection period.
func (c *Collector) Interval() time.Duration { return c.cfg.Interval }

// Collect runs all enabled sources once.
func (c *Collector) Collect(ctx context.Context) Report {
	rep := Report{At: time.Now().UnixMilli()}
	add := func(src string, items []Item, err error) {
		sr := SourceReport{Source: src, Items: items}
		if err != nil {
			sr.Error = err.Error()
			c.log.Debug("discovery source failed", "source", src, "err", err)
		}
		if sr.Items == nil {
			sr.Items = []Item{}
		}
		rep.Sources = append(rep.Sources, sr)
	}
	if c.cfg.Docker {
		items, err := Docker(ctx, c.cfg.DockerSocket)
		c.identifyImages(items)
		add(SourceDocker, items, err)
	}
	if c.cfg.K8s {
		items, err := Kubernetes(ctx, c.cfg.Kubeconfig)
		c.identifyImages(items)
		add(SourceK8s, items, err)
	}
	if c.cfg.Proxmox.URL != "" {
		items, err := Proxmox(ctx, c.cfg.Proxmox)
		add(SourceProxmox, items, err)
	}
	if c.cfg.Proxy.Enabled() {
		items, err := Proxies(ctx, c.cfg.Proxy)
		add(SourceProxy, items, err)
	}
	if c.cfg.DNS.Enabled() {
		items, err := DNSRecords(ctx, c.cfg.DNS)
		add(SourceDNS, items, err)
	}
	if c.cfg.OpenWrt {
		items, err := OpenWrt(ctx, "")
		add(SourceOpenWrt, items, err)
	}
	if c.cfg.MDNS {
		items, err := MDNS(ctx, 3*time.Second)
		add(SourceMDNS, items, err)
	}
	if c.cfg.SSDP {
		items, err := SSDP(ctx, 3*time.Second)
		add(SourceSSDP, items, err)
	}
	if c.cfg.Sockets {
		items, err := Sockets()
		add(SourceSockets, items, err)
	}
	if c.cfg.Probe {
		c.probeAll(ctx, &rep)
	}
	return rep
}

func (c *Collector) identifyImages(items []Item) {
	for i := range items {
		if items[i].Image == "" {
			continue
		}
		if m, ok := c.lib.IdentifyImage(items[i].Image); ok {
			items[i].App = &m
		}
	}
}

// endpoint returns host:port to probe for an item, or "".
func endpoint(it *Item) string {
	switch it.Kind {
	case KindSocket:
		if it.Proto != "tcp" && it.Proto != "tcp6" {
			return ""
		}
		host := it.Addr
		switch host {
		case "", "0.0.0.0", "::", "*":
			host = "127.0.0.1"
			if it.Proto == "tcp6" && it.Addr == "::" {
				host = "::1"
			}
		}
		return net.JoinHostPort(host, strconv.Itoa(it.Port))
	case KindService:
		if len(it.IPs) == 0 || it.IPs[0] == "None" {
			return ""
		}
		for _, p := range it.Ports {
			if p.Proto == "tcp" {
				return net.JoinHostPort(it.IPs[0], strconv.Itoa(p.Port))
			}
		}
	}
	return ""
}

// skipPorts are well-known non-HTTP services that are not worth an HTTP probe.
var skipPorts = map[int]bool{22: true, 25: true, 53: true, 110: true, 111: true, 139: true, 143: true, 445: true,
	465: true, 587: true, 993: true, 995: true, 2049: true, 3306: true, 5432: true, 6379: true, 11211: true,
	27017: true, 47700: true, 47701: true}

func (c *Collector) probeAll(ctx context.Context, rep *Report) {
	type job struct {
		it *Item
		ep string
	}
	var jobs []job
	seen := map[string]bool{}
	for si := range rep.Sources {
		for ii := range rep.Sources[si].Items {
			it := &rep.Sources[si].Items[ii]
			ep := endpoint(it)
			if ep == "" || seen[ep] {
				continue
			}
			_, ps, _ := net.SplitHostPort(ep)
			if p, _ := strconv.Atoi(ps); skipPorts[p] {
				continue
			}
			seen[ep] = true
			jobs = append(jobs, job{it, ep})
		}
	}
	if len(jobs) > 64 {
		jobs = jobs[:64]
	}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			e := c.probe(ctx, j.ep)
			j.it.URL, j.it.Title, j.it.Cert = e.url, e.title, e.cert
			if e.app != nil && (j.it.App == nil || e.app.Score > j.it.App.Score) {
				j.it.App = e.app
			}
		}(j)
	}
	wg.Wait()
}

func (c *Collector) probe(ctx context.Context, ep string) probeEntry {
	c.mu.Lock()
	e, ok := c.cache[ep]
	c.mu.Unlock()
	if ok && time.Since(e.at) < time.Hour {
		return e
	}
	e = probeEntry{at: time.Now()}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for _, scheme := range schemesFor(ep) {
		base := scheme + "://" + ep
		o, m, ok := c.prober.Probe(pctx, base)
		if o == nil {
			continue
		}
		// plain HTTP to a TLS port returns 400 "sent an HTTP request to an HTTPS server"
		if scheme == "http" && o.Status == 400 && strings.Contains(strings.ToLower(o.Body), "https") {
			continue
		}
		e.url, e.title = base, o.Title
		if ok {
			e.app = &m
		}
		if o.TLS != nil {
			e.cert = &CertInfo{Names: o.TLS.Names, NotAfter: o.TLS.NotAfter, Issuer: o.TLS.Issuer}
		}
		break
	}
	c.mu.Lock()
	c.cache[ep] = e
	c.mu.Unlock()
	return e
}

func schemesFor(ep string) []string {
	_, p, _ := net.SplitHostPort(ep)
	switch p {
	case "443", "8443", "9443", "6443", "5001", "8006", "10250":
		return []string{"https", "http"}
	}
	return []string{"http", "https"}
}

// Run collects periodically and hands each report to send.
func (c *Collector) Run(ctx context.Context, send func(Report) error) {
	if !c.Enabled() {
		return
	}
	t := time.NewTimer(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rep := c.Collect(ctx)
		if err := send(rep); err != nil {
			c.log.Debug("discovery report not sent", "err", err)
			t.Reset(30 * time.Second)
			continue
		}
		t.Reset(c.cfg.Interval)
	}
}

func sortItems(items []Item) {
	sort.Slice(items, func(a, b int) bool { return items[a].Key < items[b].Key })
}
