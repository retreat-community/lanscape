package discovery

import (
	"bufio"
	"bytes"
	"context"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// LAN device sources and kinds.
const (
	SourceMDNS = "mdns"
	SourceSSDP = "ssdp"
	KindDevice = "device"
)

var (
	mdnsAddr = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	ssdpAddr = &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
)

// mdnsHost collects what one host announced.
type mdnsHost struct {
	name     string
	ips      map[string]bool
	services map[string]int // "_ipp._tcp" -> port
	txt      map[string]string
	instance string
}

// MDNS browses DNS-SD services on the local links for the given time.
func MDNS(ctx context.Context, wait time.Duration) ([]Item, error) {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer pc.Close()
	query := func(name string, t dnsmessage.Type) error {
		n, err := dnsmessage.NewName(name)
		if err != nil {
			return err
		}
		// class IN with the unicast-response bit: answers come back to our port
		msg := dnsmessage.Message{Questions: []dnsmessage.Question{{Name: n, Type: t, Class: dnsmessage.ClassINET | 1<<15}}}
		b, err := msg.Pack()
		if err != nil {
			return err
		}
		_, err = pc.WriteToUDP(b, mdnsAddr)
		return err
	}
	if err := query("_services._dns-sd._udp.local.", dnsmessage.TypePTR); err != nil {
		return nil, err
	}
	hosts := map[string]*mdnsHost{} // host name -> host
	instHost := map[string]string{} // instance -> host name
	instType := map[string]string{} // instance -> service type
	instTXT := map[string]map[string]string{}
	asked := map[string]bool{}
	deadline := time.Now().Add(wait)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	buf := make([]byte, 9000)
	host := func(name string) *mdnsHost {
		h := hosts[name]
		if h == nil {
			h = &mdnsHost{name: name, ips: map[string]bool{}, services: map[string]int{}, txt: map[string]string{}}
			hosts[name] = h
		}
		return h
	}
	for time.Now().Before(deadline) {
		_ = pc.SetReadDeadline(deadline)
		n, from, err := pc.ReadFromUDP(buf)
		if err != nil {
			break
		}
		var p dnsmessage.Parser
		if _, err := p.Start(buf[:n]); err != nil {
			continue
		}
		_ = p.SkipAllQuestions()
		answers, err := p.AllAnswers()
		if err != nil {
			continue
		}
		_ = p.SkipAllAuthorities()
		adds, _ := p.AllAdditionals()
		for _, r := range append(answers, adds...) {
			name := r.Header.Name.String()
			switch b := r.Body.(type) {
			case *dnsmessage.PTRResource:
				target := b.PTR.String()
				if name == "_services._dns-sd._udp.local." {
					if !asked[target] {
						asked[target] = true
						_ = query(target, dnsmessage.TypePTR)
					}
					continue
				}
				instType[target] = strings.TrimSuffix(name, ".local.")
				if !asked[target] {
					asked[target] = true
					_ = query(target, dnsmessage.TypeSRV)
					_ = query(target, dnsmessage.TypeTXT)
				}
			case *dnsmessage.SRVResource:
				hn := b.Target.String()
				instHost[name] = hn
				hh := host(hn)
				if hh.instance == "" {
					hh.instance = instanceLabel(name)
				}
				t := instType[name]
				if t == "" {
					t = serviceOf(name)
				}
				hh.services[t] = int(b.Port)
				hh.ips[from.IP.String()] = true
			case *dnsmessage.TXTResource:
				m := map[string]string{}
				for _, kv := range b.TXT {
					k, v, _ := strings.Cut(kv, "=")
					m[strings.ToLower(k)] = v
				}
				instTXT[name] = m
			case *dnsmessage.AResource:
				host(name).ips[net.IP(b.A[:]).String()] = true
			}
		}
	}
	for inst, hn := range instHost {
		if h := hosts[hn]; h != nil {
			for k, v := range instTXT[inst] {
				if _, ok := h.txt[k]; !ok {
					h.txt[k] = v
				}
			}
		}
	}
	var items []Item
	for _, h := range hosts {
		if len(h.services) == 0 {
			continue
		}
		it := Item{Key: "mdns/" + strings.TrimSuffix(h.name, "."), Kind: KindDevice, Name: h.instance,
			Labels: map[string]string{"hostname": strings.TrimSuffix(h.name, ".")}}
		if it.Name == "" {
			it.Name = strings.TrimSuffix(strings.TrimSuffix(h.name, "."), ".local")
		}
		for ip := range h.ips {
			it.IPs = append(it.IPs, ip)
		}
		sort.Strings(it.IPs)
		var svcs []string
		for s, port := range h.services {
			svcs = append(svcs, s)
			proto := "tcp"
			if strings.HasSuffix(s, "._udp") {
				proto = "udp"
			}
			it.Ports = append(it.Ports, Port{Port: port, Proto: proto})
		}
		sort.Strings(svcs)
		sort.Slice(it.Ports, func(a, b int) bool { return it.Ports[a].Port < it.Ports[b].Port })
		it.Labels["services"] = strings.Join(svcs, ",")
		for _, k := range []string{"md", "model", "ty", "usb_mdl", "manufacturer", "usb_mfg"} {
			if v := h.txt[k]; v != "" {
				it.Labels["model"] = v
				break
			}
		}
		it.Labels["type"] = DeviceType(svcs, it.Labels["model"])
		items = append(items, it)
	}
	sortItems(items)
	return items, nil
}

func instanceLabel(inst string) string {
	// "Office Printer._ipp._tcp.local." -> "Office Printer"
	if i := strings.Index(inst, "._"); i > 0 {
		return strings.ReplaceAll(inst[:i], `\ `, " ")
	}
	return inst
}

func serviceOf(inst string) string {
	if i := strings.Index(inst, "._"); i > 0 {
		return strings.TrimSuffix(strings.TrimSuffix(inst[i+1:], ".local."), ".")
	}
	return inst
}

// DeviceType guesses a device class for the map icon from announced services.
func DeviceType(services []string, model string) string {
	has := func(p ...string) bool {
		for _, s := range services {
			for _, x := range p {
				if strings.HasPrefix(s, x) {
					return true
				}
			}
		}
		return false
	}
	m := strings.ToLower(model)
	switch {
	case has("_ipp", "_ipps", "_printer", "_pdl-datastream", "_scanner", "_uscan"):
		return "printer"
	case has("_googlecast", "_airplay", "_raop", "_spotify-connect", "_sonos") || strings.Contains(m, "tv"):
		return "media"
	case has("_hap", "_homekit", "_matter", "_hue", "_esphomelib", "_shelly"):
		return "iot"
	case has("_smb", "_afpovertcp", "_adisk", "_nfs"):
		return "nas"
	case has("_rfb", "_ssh", "_sftp-ssh", "_workstation"):
		return "computer"
	case has("_companion-link", "_apple-mobdev2"):
		return "phone"
	}
	return "device"
}

type upnpDesc struct {
	Device struct {
		DeviceType, FriendlyName, Manufacturer, ModelName, Presentation string
	}
}

// xmlText returns the text of the first <tag>…</tag> (a full XML decoder is not worth its size here).
func xmlText(doc, tag string) string {
	i := strings.Index(doc, "<"+tag+">")
	if i < 0 {
		return ""
	}
	rest := doc[i+len(tag)+2:]
	j := strings.Index(rest, "</"+tag+">")
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(rest[:j]))
}

// SSDP searches UPnP devices and reads their descriptions.
func SSDP(ctx context.Context, wait time.Duration) ([]Item, error) {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer pc.Close()
	req := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: ssdp:all\r\n\r\n"
	for i := 0; i < 2; i++ {
		if _, err := pc.WriteToUDP([]byte(req), ssdpAddr); err != nil {
			return nil, err
		}
	}
	type resp struct {
		ip, location, server string
	}
	byIP := map[string]*resp{}
	deadline := time.Now().Add(wait)
	buf := make([]byte, 4096)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		_ = pc.SetReadDeadline(deadline)
		n, from, err := pc.ReadFromUDP(buf)
		if err != nil {
			break
		}
		r, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(buf[:n])), nil)
		if err != nil {
			continue
		}
		_ = r.Body.Close()
		ip := from.IP.String()
		if byIP[ip] == nil {
			byIP[ip] = &resp{ip: ip}
		}
		if loc := r.Header.Get("Location"); loc != "" && byIP[ip].location == "" {
			byIP[ip].location = loc
		}
		if sv := r.Header.Get("Server"); sv != "" {
			byIP[ip].server = sv
		}
	}
	hc := &http.Client{Timeout: 3 * time.Second}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var items []Item
	for _, r := range byIP {
		wg.Add(1)
		go func(r *resp) {
			defer wg.Done()
			it := Item{Key: "ssdp/" + r.ip, Kind: KindDevice, Name: r.ip, IPs: []string{r.ip},
				Labels: map[string]string{"server": r.server}}
			if u, err := url.Parse(r.location); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() == r.ip {
				if d, err := fetchDesc(ctx, hc, r.location); err == nil {
					if d.Device.FriendlyName != "" {
						it.Name = d.Device.FriendlyName
					}
					it.Labels["manufacturer"] = d.Device.Manufacturer
					it.Labels["model"] = d.Device.ModelName
					it.Labels["upnp_type"] = d.Device.DeviceType
					it.Labels["type"] = upnpType(d.Device.DeviceType)
					if p := d.Device.Presentation; strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
						it.URL = p
					}
				}
			}
			mu.Lock()
			items = append(items, it)
			mu.Unlock()
		}(r)
	}
	wg.Wait()
	sortItems(items)
	return items, nil
}

func fetchDesc(ctx context.Context, hc *http.Client, loc string) (upnpDesc, error) {
	var d upnpDesc
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
	if err != nil {
		return d, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return d, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	doc := string(b)
	d.Device.DeviceType, d.Device.FriendlyName = xmlText(doc, "deviceType"), xmlText(doc, "friendlyName")
	d.Device.Manufacturer, d.Device.ModelName = xmlText(doc, "manufacturer"), xmlText(doc, "modelName")
	d.Device.Presentation = xmlText(doc, "presentationURL")
	return d, err
}

func upnpType(t string) string {
	t = strings.ToLower(t)
	switch {
	case strings.Contains(t, "internetgatewaydevice"):
		return "router"
	case strings.Contains(t, "mediarenderer"), strings.Contains(t, "mediaserver"):
		return "media"
	case strings.Contains(t, "printer"):
		return "printer"
	case strings.Contains(t, "wlanaccesspoint"):
		return "ap"
	}
	return "device"
}
