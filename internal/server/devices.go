package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/oui"
	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/topo"
)

// Device is a host seen on the network without an agent: ARP/NDP neighbours of agents,
// mDNS/DNS-SD and SSDP announcements, with the vendor from the MAC address.
type Device struct {
	IP       string   `json:"ip"`
	MAC      string   `json:"mac,omitempty"`
	Vendor   string   `json:"vendor,omitempty"`
	Name     string   `json:"name,omitempty"`
	Type     string   `json:"type"` // printer, media, iot, nas, computer, phone, router, ap, device
	Model    string   `json:"model,omitempty"`
	URL      string   `json:"url,omitempty"`
	Services []string `json:"services,omitempty"`
	Sources  []string `json:"sources"`
	SeenBy   []string `json:"seen_by"`         // agents that have it in their neighbour table
	Wifi     string   `json:"wifi,omitempty"`  // band and signal when associated to an OpenWrt access point
	Ports    string   `json:"ports,omitempty"` // open TCP ports from the last scan
}

// devices merges discovered hosts by IP address.
func (s *Server) devices(ctx context.Context) []Device {
	own := map[string]bool{}
	agents := s.hub.List()
	for _, a := range agents {
		for _, ifc := range a.Inv.Ifaces {
			for _, ad := range ifc.Addrs {
				own[ad.IP] = true
			}
		}
	}
	by := map[string]*Device{}
	byMAC := map[string]*Device{}
	get := func(ip string) *Device {
		d := by[ip]
		if d == nil {
			d = &Device{IP: ip, Type: "device", Sources: []string{}, SeenBy: []string{}}
			by[ip] = d
		}
		return d
	}
	addUniq := func(l []string, v string) []string {
		for _, x := range l {
			if x == v {
				return l
			}
		}
		return append(l, v)
	}
	for _, a := range agents {
		for _, n := range a.Inv.Neighbors {
			ip := net.ParseIP(n.IP)
			if ip == nil || own[n.IP] || ip.IsLinkLocalUnicast() || ip.IsMulticast() || n.MAC == "" ||
				n.MAC == "00:00:00:00:00:00" || strings.EqualFold(n.State, "failed") || strings.EqualFold(n.State, "incomplete") {
				continue
			}
			d := get(n.IP)
			d.MAC = strings.ToLower(n.MAC)
			byMAC[d.MAC] = d
			d.Sources = addUniq(d.Sources, "arp")
			d.SeenBy = addUniq(d.SeenBy, a.Name)
		}
	}
	if fs, err := s.store.Findings(ctx, "", ""); err == nil {
		for _, f := range fs {
			if f.Gone != 0 || (f.Source != discovery.SourceMDNS && f.Source != discovery.SourceSSDP && f.Source != discovery.SourceOpenWrt &&
				f.Source != discovery.SourceScan) {
				continue
			}
			var it discovery.Item
			if json.Unmarshal(f.Data, &it) != nil {
				continue
			}
			if f.Source == discovery.SourceOpenWrt {
				s.mergeRouterItem(&it, get, byMAC, addUniq)
				continue
			}
			for _, ip := range it.IPs {
				if own[ip] || net.ParseIP(ip) == nil {
					continue
				}
				d := get(ip)
				d.Sources = addUniq(d.Sources, f.Source)
				// mDNS names are chosen by the owner; prefer them over UPnP friendly names
				if (d.Name == "" && f.Source != discovery.SourceScan) || f.Source == discovery.SourceMDNS {
					d.Name = it.Name
				}
				if op := it.Labels["open_ports"]; op != "" {
					d.Ports = op
				}
				if t := it.Labels["type"]; t != "" && t != "device" {
					d.Type = t
				}
				if m := it.Labels["model"]; m != "" && d.Model == "" {
					d.Model = m
				}
				if it.URL != "" {
					d.URL = it.URL
				}
				if sv := it.Labels["services"]; sv != "" {
					for _, x := range strings.Split(sv, ",") {
						d.Services = addUniq(d.Services, x)
					}
				}
				break // the first address identifies the device
			}
		}
	}
	// names from local DNS servers for devices that have none
	if fs, err := s.store.Findings(ctx, "", ""); err == nil {
		for _, f := range fs {
			if f.Source != discovery.SourceDNS || f.Gone != 0 {
				continue
			}
			var it discovery.Item
			if json.Unmarshal(f.Data, &it) != nil {
				continue
			}
			for _, ip := range it.IPs {
				if d := by[ip]; d != nil && d.Name == "" {
					d.Name = it.Name
					d.Sources = addUniq(d.Sources, "dns")
				}
			}
		}
	}
	out := make([]Device, 0, len(by))
	for _, d := range by {
		if d.MAC != "" {
			d.Vendor = oui.Vendor(d.MAC)
		}
		sort.Strings(d.Sources)
		sort.Strings(d.SeenBy)
		out = append(out, *d)
	}
	sort.Slice(out, func(a, b int) bool { return ipLess(out[a].IP, out[b].IP) })
	return out
}

func ipLess(a, b string) bool {
	ia, ib := net.ParseIP(a), net.ParseIP(b)
	if ia == nil || ib == nil {
		return a < b
	}
	a4, b4 := ia.To4(), ib.To4()
	if (a4 == nil) != (b4 == nil) {
		return a4 != nil
	}
	if a4 != nil {
		ia, ib = a4, b4
	}
	for i := range ia {
		if ia[i] != ib[i] {
			return ia[i] < ib[i]
		}
	}
	return false
}

func (s *Server) apiDevices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.devices(r.Context()))
}

// deviceExtras feeds discovered devices into IPAM and the anomaly checks.
func (s *Server) deviceExtras(ctx context.Context) []topo.Extra {
	var out []topo.Extra
	for _, d := range s.devices(ctx) {
		name := d.Name
		if name == "" {
			name = d.Vendor
		}
		out = append(out, topo.Extra{IP: d.IP, MAC: d.MAC, Name: name, Source: strings.Join(d.Sources, "+")})
	}
	return out
}

// mergeRouterItem adds DHCP leases (names!) and Wi-Fi association data from OpenWrt routers.
func (s *Server) mergeRouterItem(it *discovery.Item, get func(string) *Device, byMAC map[string]*Device,
	addUniq func([]string, string) []string) {
	mac := it.Labels["mac"]
	switch it.Kind {
	case discovery.KindLease:
		if len(it.IPs) == 0 {
			return
		}
		d := get(it.IPs[0])
		if mac != "" {
			d.MAC = mac
			byMAC[mac] = d
		}
		if it.Name != "" && d.Name == "" {
			d.Name = it.Name
		}
		d.Sources = addUniq(d.Sources, "dhcp")
	case discovery.KindWifiClient:
		if d := byMAC[mac]; d != nil {
			d.Sources = addUniq(d.Sources, "wifi")
			d.Wifi = it.Labels["band"] + " GHz, " + it.Labels["signal"] + " dBm"
		}
	}
}

// apiScan starts an explicit port scan from one agent; results arrive as the "scan" source.
func (s *Server) apiScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent string `json:"agent"`
		discovery.ScanRequest
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.CIDRs) == 0 {
		writeError(w, http.StatusBadRequest, "cidrs are required")
		return
	}
	a, ok := s.hub.Get(req.Agent)
	c, online := s.hub.Conn(req.Agent)
	if !ok || !online || !hasCap(a.Caps, proto.MsgScan) {
		writeError(w, http.StatusBadRequest, "the agent is offline or cannot scan")
		return
	}
	s.audit(r, "discovery.scan", req.Agent, "started", strings.Join(req.CIDRs, ","))
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
		defer cancel()
		raw, err := c.Request(ctx, proto.MsgScan, req.ScanRequest)
		if err != nil {
			s.log.Warn("scan failed", "agent", req.Agent, "err", err)
			s.events.Publish("scan", map[string]any{"agent": req.Agent, "error": err.Error()})
			return
		}
		var sr discovery.SourceReport
		if json.Unmarshal(raw, &sr) != nil {
			return
		}
		sr.Source = discovery.SourceScan
		if err := s.ingestDiscovery(ctx, req.Agent, discovery.Report{At: time.Now().UnixMilli(), Sources: []discovery.SourceReport{sr}}); err != nil {
			s.log.Warn("scan results not stored", "err", err)
		}
		s.events.Publish("scan", map[string]any{"agent": req.Agent, "hosts": len(sr.Items)})
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}
