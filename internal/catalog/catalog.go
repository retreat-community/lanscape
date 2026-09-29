// Package catalog merges discovery findings from all agents into service cards: one service
// found in several ways (Ingress + Service + Deployment + HTTP fingerprint, or a container
// with its published port and the docker-proxy socket) becomes one card with all addresses.
package catalog

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/fingerprint"
	"github.com/retreat-community/lanscape/internal/monitor"
)

// Finding is an item with the agent that reported it.
type Finding struct {
	Agent  string
	Source string
	Item   discovery.Item
	Gone   bool
	First  int64
}

// AgentInfo describes the agent host (for building reachable addresses).
type AgentInfo struct {
	Name string
	IP   string // primary LAN address
}

// Ref points at one finding of a card.
type Ref struct {
	Agent  string `json:"agent"`
	Source string `json:"source"`
	Key    string `json:"key"`
	Kind   string `json:"kind"`
	Gone   bool   `json:"gone,omitempty"`
}

// Address is one way to reach the service.
type Address struct {
	Type  string `json:"type"` // internal, external, hostport, workload, container, process
	Value string `json:"value"`
	Agent string `json:"agent,omitempty"`
}

// Card is a merged, discovered service.
type Card struct {
	Key          string             `json:"key"`
	Name         string             `json:"name"`
	App          *fingerprint.Match `json:"app,omitempty"`
	Kind         string             `json:"kind"` // primary kind: k8s_deployment, container, socket …
	Namespace    string             `json:"namespace,omitempty"`
	Agents       []string           `json:"agents"`
	State        string             `json:"state,omitempty"`
	Health       string             `json:"health,omitempty"`
	InternalURL  string             `json:"internal_url,omitempty"`
	ExternalURL  string             `json:"external_url,omitempty"`
	HostPort     string             `json:"host_port,omitempty"`
	TLS          bool               `json:"tls,omitempty"`
	CertNotAfter int64              `json:"cert_not_after,omitempty"`
	Addresses    []Address          `json:"addresses"`
	Refs         []Ref              `json:"refs"`
	Gone         bool               `json:"gone,omitempty"`
	FirstSeen    int64              `json:"first_seen"`
	Monitor      *monitor.Spec      `json:"monitor,omitempty"` // recommended check
}

// ignoredProcesses are system daemons that are not services worth listing.
var ignoredProcesses = map[string]bool{
	"lanscape": true, "lanscape-agent": true, "lsm-agent": true, "lsm-server": true, "systemd-resolve": true,
	"systemd-resolved": true, "systemd-network": true, "systemd": true, "rpcbind": true, "rpc.statd": true,
	"chronyd": true, "ntpd": true, "dhclient": true, "dhcpcd": true, "containerd": true, "dockerd": true,
	"kubelet": true, "kube-proxy": true, "avahi-daemon": true, "rpc.mountd": true,
}

// kind priority for choosing the primary item of a group
var kindRank = map[string]int{
	discovery.KindDeployment: 1, discovery.KindStatefulSet: 1, discovery.KindDaemonSet: 1,
	discovery.KindContainer: 2, discovery.KindService: 3, discovery.KindIngress: 4, discovery.KindHTTPRoute: 4,
	discovery.KindSocket: 5, discovery.KindVM: 1, discovery.KindCT: 1,
}

type node struct {
	id string
	f  *Finding
}

type uf struct{ par []int }

func (u *uf) find(i int) int {
	for u.par[i] != i {
		u.par[i] = u.par[u.par[i]]
		i = u.par[i]
	}
	return i
}

func (u *uf) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.par[rb] = ra
	}
}

func isLoopback(a string) bool {
	ip := net.ParseIP(a)
	return ip != nil && ip.IsLoopback()
}

// include decides whether a finding can form or join a card.
func include(f *Finding) bool {
	it := &f.Item
	switch it.Kind {
	case discovery.KindPVC, discovery.KindLease, discovery.KindWifiClient, discovery.KindPortForward, discovery.KindSQM:
		return false
	case discovery.KindDevice:
		return it.URL != "" // only LAN devices with a web interface become service cards
	case discovery.KindSocket:
		if ignoredProcesses[it.Process] {
			return false
		}
		if it.App == nil && isLoopback(it.Addr) {
			return false
		}
		if it.Proto == "udp" && it.Port >= 32768 && it.App == nil {
			return false // ephemeral client sockets
		}
	}
	return true
}

// nodeID is unique across agents; Kubernetes objects are cluster-wide, so agents of the
// same cluster (a DaemonSet) report the same object.
func nodeID(f *Finding) string {
	if f.Source == discovery.SourceK8s || f.Source == discovery.SourceProxmox {
		return f.Source + ":" + f.Item.Key // cluster-wide objects
	}
	if f.Item.Kind == discovery.KindDevice && len(f.Item.IPs) > 0 {
		return "device:" + f.Item.IPs[0] // seen by several agents and by mDNS and SSDP
	}
	return f.Source + ":" + f.Agent + ":" + f.Item.Key
}

// Build merges findings into cards.
func Build(fs []Finding, agents map[string]AgentInfo) []Card {
	var nodes []node
	idx := map[string]int{}
	for i := range fs {
		f := &fs[i]
		if !include(f) {
			continue
		}
		id := nodeID(f)
		if j, ok := idx[id]; ok {
			// the same k8s object from several agents: prefer a live, probed copy
			if nodes[j].f.Gone || (nodes[j].f.Item.App == nil && f.Item.App != nil) {
				nodes[j].f = f
			}
			continue
		}
		idx[id] = len(nodes)
		nodes = append(nodes, node{id: id, f: f})
	}
	u := &uf{par: make([]int, len(nodes))}
	for i := range u.par {
		u.par[i] = i
	}
	// index helpers
	containersByOwner := map[string]int{} // agent/containerID -> node
	containerPorts := map[string]int{}    // agent/port/proto -> node
	procByName := map[string]int{}        // agent/process -> node (host processes only)
	svcByName := map[string]int{}         // ns/name -> node
	var workloads []int
	for i, n := range nodes {
		it := &n.f.Item
		switch it.Kind {
		case discovery.KindContainer:
			containersByOwner[n.f.Agent+"/"+it.Owner] = i
			for _, p := range it.Ports {
				if p.Port > 0 {
					containerPorts[fmt.Sprintf("%s/%d/%s", n.f.Agent, p.Port, p.Proto)] = i
				}
			}
		case discovery.KindService:
			svcByName[it.Namespace+"/"+it.Name] = i
		case discovery.KindDeployment, discovery.KindStatefulSet, discovery.KindDaemonSet:
			workloads = append(workloads, i)
		}
	}
	for i, n := range nodes {
		it := &n.f.Item
		switch it.Kind {
		case discovery.KindSocket:
			if it.Owner != "" {
				if j, ok := containersByOwner[n.f.Agent+"/"+it.Owner]; ok {
					u.union(j, i)
					continue
				}
			}
			if j, ok := containerPorts[fmt.Sprintf("%s/%d/%s", n.f.Agent, it.Port, it.Proto)]; ok {
				u.union(j, i)
				continue
			}
			if it.Process != "" && it.Owner == "" && it.Process != "docker-proxy" {
				k := n.f.Agent + "/" + it.Process
				if j, ok := procByName[k]; ok {
					u.union(j, i)
				} else {
					procByName[k] = i
				}
			}
		case discovery.KindIngress, discovery.KindHTTPRoute:
			for _, b := range it.Backends {
				name, _, _ := strings.Cut(b, ":")
				if j, ok := svcByName[name]; ok {
					u.union(j, i)
				}
			}
		case discovery.KindService:
			if len(it.Selector) == 0 {
				continue
			}
			for _, w := range workloads {
				wi := &nodes[w].f.Item
				if wi.Namespace == it.Namespace && selects(it.Selector, wi.PodLabels) {
					u.union(w, i)
				}
			}
		}
	}
	groups := map[int][]int{}
	var roots []int
	for i := range nodes {
		r := u.find(i)
		if _, ok := groups[r]; !ok {
			roots = append(roots, r)
		}
		groups[r] = append(groups[r], i)
	}
	cards := make([]Card, 0, len(roots))
	for _, r := range roots {
		members := make([]*Finding, 0, len(groups[r]))
		for _, i := range groups[r] {
			members = append(members, nodes[i].f)
		}
		cards = append(cards, card(members, agents))
	}
	sort.Slice(cards, func(a, b int) bool { return cards[a].Key < cards[b].Key })
	return cards
}

func selects(sel, labels map[string]string) bool {
	for k, v := range sel {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func card(ms []*Finding, agents map[string]AgentInfo) Card {
	sort.SliceStable(ms, func(a, b int) bool {
		ra, rb := kindRank[ms[a].Item.Kind], kindRank[ms[b].Item.Kind]
		if ra != rb {
			return ra < rb
		}
		if ms[a].Item.Kind == discovery.KindSocket && ms[a].Item.Port != ms[b].Item.Port {
			return ms[a].Item.Port < ms[b].Item.Port
		}
		return ms[a].Item.Key < ms[b].Item.Key
	})
	p := ms[0]
	c := Card{Kind: p.Item.Kind, Namespace: p.Item.Namespace, Gone: true, Addresses: []Address{}}
	c.Key, c.Name = cardKey(p), displayName(p)
	agentSet := map[string]bool{}
	addrSeen := map[string]bool{}
	addAddr := func(typ, v, agent string) {
		if v == "" || addrSeen[typ+v] {
			return
		}
		addrSeen[typ+v] = true
		c.Addresses = append(c.Addresses, Address{Type: typ, Value: v, Agent: agent})
	}
	for _, f := range ms {
		it := &f.Item
		c.Refs = append(c.Refs, Ref{Agent: f.Agent, Source: f.Source, Key: it.Key, Kind: it.Kind, Gone: f.Gone})
		if !f.Gone {
			c.Gone = false
		}
		if c.FirstSeen == 0 || (f.First > 0 && f.First < c.FirstSeen) {
			c.FirstSeen = f.First
		}
		if f.Source != discovery.SourceK8s && f.Source != discovery.SourceProxmox {
			agentSet[f.Agent] = true
		}
		if it.App != nil && (c.App == nil || it.App.Score > c.App.Score) {
			app := *it.App
			c.App = &app
		}
		if it.Cert != nil && (c.CertNotAfter == 0 || it.Cert.NotAfter < c.CertNotAfter) {
			c.CertNotAfter = it.Cert.NotAfter
		}
		hostIP := agents[f.Agent].IP
		switch it.Kind {
		case discovery.KindIngress, discovery.KindHTTPRoute:
			scheme := "http"
			if it.TLS || it.Kind == discovery.KindHTTPRoute {
				scheme = "https"
				c.TLS = true
			}
			for _, h := range it.Hosts {
				u := scheme + "://" + h
				addAddr("external", u, "")
				if c.ExternalURL == "" {
					c.ExternalURL = u
				}
			}
		case discovery.KindService:
			for _, p := range it.Ports {
				for i, ip := range it.IPs {
					typ := "hostport"
					if i > 0 { // LoadBalancer addresses are reachable from the LAN
						typ = "internal"
					}
					hp := net.JoinHostPort(ip, strconv.Itoa(p.Port))
					addAddr(typ, hp, "")
					if typ == "internal" && c.HostPort == "" {
						c.HostPort = hp
					}
				}
			}
			addAddr("workload", "svc/"+it.Namespace+"/"+it.Name, "")
		case discovery.KindDeployment, discovery.KindStatefulSet, discovery.KindDaemonSet:
			addAddr("workload", strings.TrimPrefix(it.Kind, "k8s_")+"/"+it.Namespace+"/"+it.Name, "")
			if c.State == "" {
				c.State = it.State
			}
		case discovery.KindVM, discovery.KindCT:
			addAddr("vm", it.Key+" @ "+it.Labels["node"], "")
			for _, ip := range it.IPs {
				addAddr("ip", ip, "")
			}
			if c.State == "" {
				c.State = it.State
			}
		case discovery.KindContainer:
			addAddr("container", it.Name, f.Agent)
			if c.State == "" || it.State != "running" {
				c.State = it.State
			}
			if it.Health != "" {
				c.Health = it.Health
			}
			for _, p := range it.Ports {
				if p.Port == 0 {
					continue
				}
				ip := p.IP
				if ip == "" {
					ip = hostIP
				}
				if ip != "" && !isLoopback(ip) {
					hp := net.JoinHostPort(ip, strconv.Itoa(p.Port))
					addAddr("hostport", hp, f.Agent)
					if c.HostPort == "" && p.Proto == "tcp" {
						c.HostPort = hp
					}
				}
			}
		case discovery.KindSocket:
			if it.Process != "" && it.Owner == "" {
				addAddr("process", it.Process, f.Agent)
			}
			ip := it.Addr
			if ip == "0.0.0.0" || ip == "::" {
				ip = hostIP
			}
			if ip != "" && !isLoopback(ip) {
				hp := net.JoinHostPort(ip, strconv.Itoa(it.Port))
				if it.Proto == "udp" {
					addAddr("hostport", "udp://"+hp, f.Agent)
				} else {
					addAddr("hostport", hp, f.Agent)
					if c.HostPort == "" {
						c.HostPort = hp
					}
				}
			}
		}
		// a probed URL becomes the internal URL, rewritten from the agent's loopback to its LAN address
		if it.URL != "" && c.InternalURL == "" {
			c.InternalURL = lanURL(it.URL, hostIP)
		}
	}
	if c.InternalURL == "" && c.HostPort != "" && c.App != nil && c.App.Monitor.Type == monitor.TypeHTTP {
		c.InternalURL = "http://" + c.HostPort
	}
	if c.InternalURL != "" {
		addAddr("internal", c.InternalURL, "")
	}
	for a := range agentSet {
		c.Agents = append(c.Agents, a)
	}
	sort.Strings(c.Agents)
	if c.App != nil && p.Item.Kind == discovery.KindSocket {
		c.Name = c.App.Name // process names are technical; the application name reads better
	}
	c.Monitor = recommend(&c)
	if c.Monitor == nil {
		c.Monitor = resourceMonitor(p)
	}
	return c
}

// resourceMonitor checks the state of the primary object when there is no endpoint to probe.
func resourceMonitor(p *Finding) *monitor.Spec {
	agent := p.Agent
	if p.Source == discovery.SourceK8s || p.Source == discovery.SourceProxmox {
		agent = "*"
	}
	target := p.Source + ":" + agent + ":" + p.Item.Key
	switch p.Item.Kind {
	case discovery.KindContainer:
		return &monitor.Spec{Type: monitor.TypeContainer, Target: target}
	case discovery.KindDeployment, discovery.KindStatefulSet, discovery.KindDaemonSet:
		return &monitor.Spec{Type: monitor.TypeK8s, Target: target}
	case discovery.KindVM, discovery.KindCT:
		return &monitor.Spec{Type: monitor.TypeVM, Target: target}
	}
	return nil
}

// lanURL replaces a loopback host in a probed URL with the agent's LAN address.
func lanURL(raw, hostIP string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if isLoopback(u.Hostname()) && hostIP != "" {
		u.Host = net.JoinHostPort(hostIP, u.Port())
	}
	return u.String()
}

func cardKey(p *Finding) string {
	it := &p.Item
	switch {
	case p.Source == discovery.SourceK8s || p.Source == discovery.SourceProxmox:
		return p.Source + ":" + it.Key
	case it.Kind == discovery.KindDevice && len(it.IPs) > 0:
		return "device:" + it.IPs[0]
	case it.Kind == discovery.KindContainer:
		return "docker:" + p.Agent + ":" + it.Name
	case it.Kind == discovery.KindSocket && it.Process != "" && it.Owner == "":
		return "proc:" + p.Agent + ":" + it.Process
	}
	return p.Source + ":" + p.Agent + ":" + it.Key
}

func displayName(p *Finding) string {
	it := &p.Item
	if it.Kind == discovery.KindContainer {
		if s := it.Labels["com.docker.compose.service"]; s != "" && it.Project != "" && s != it.Project {
			return it.Project + "/" + s
		}
		return it.Name
	}
	if it.Kind == discovery.KindSocket && it.Process == "" {
		return fmt.Sprintf("%s/%d", it.Proto, it.Port)
	}
	return it.Name
}

// recommend returns the check suggested for a card.
func recommend(c *Card) *monitor.Spec {
	if c.App != nil {
		switch c.App.Monitor.Type {
		case monitor.TypeHTTP:
			base := c.InternalURL
			if base == "" {
				base = c.ExternalURL
			}
			if base != "" {
				s := &monitor.Spec{Type: monitor.TypeHTTP, Target: strings.TrimRight(base, "/") + c.App.Monitor.Path}
				if strings.HasPrefix(base, "https://") {
					s.IgnoreTLSErrors = base == c.InternalURL // internal endpoints often use self-signed certificates
				}
				return s
			}
		case monitor.TypeDNS:
			if host, _, err := net.SplitHostPort(c.HostPort); err == nil {
				return &monitor.Spec{Type: monitor.TypeDNS, Target: "localhost", Server: host}
			}
		}
	}
	if c.InternalURL != "" {
		return &monitor.Spec{Type: monitor.TypeHTTP, Target: c.InternalURL, IgnoreTLSErrors: strings.HasPrefix(c.InternalURL, "https://")}
	}
	if c.ExternalURL != "" {
		return &monitor.Spec{Type: monitor.TypeHTTP, Target: c.ExternalURL}
	}
	if c.HostPort != "" {
		return &monitor.Spec{Type: monitor.TypeTCP, Target: c.HostPort}
	}
	return nil
}
