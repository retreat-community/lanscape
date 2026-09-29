package discovery

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OpenWrt source and kinds.
const (
	SourceOpenWrt   = "openwrt"
	KindLease       = "dhcp_lease"
	KindWifiClient  = "wifi_client"
	KindPortForward = "port_forward"
	KindSQM         = "sqm"
)

// UCISection is one "config <type> '<name>'" block.
type UCISection struct {
	Type    string
	Name    string
	Options map[string]string
	Lists   map[string][]string
}

// ParseUCI parses a UCI configuration file.
func ParseUCI(r io.Reader) []UCISection {
	var out []UCISection
	var cur *UCISection
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := uciFields(sc.Text())
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		switch f[0] {
		case "config":
			out = append(out, UCISection{Options: map[string]string{}, Lists: map[string][]string{}})
			cur = &out[len(out)-1]
			if len(f) > 1 {
				cur.Type = f[1]
			}
			if len(f) > 2 {
				cur.Name = f[2]
			}
		case "option":
			if cur != nil && len(f) > 2 {
				cur.Options[f[1]] = f[2]
			}
		case "list":
			if cur != nil && len(f) > 2 {
				cur.Lists[f[1]] = append(cur.Lists[f[1]], f[2])
			}
		}
	}
	return out
}

// uciFields splits a UCI line honouring single and double quotes.
func uciFields(line string) []string {
	var out []string
	var b strings.Builder
	quote := byte(0)
	in := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				b.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == ' ' || c == '\t':
			if in {
				out = append(out, b.String())
				b.Reset()
				in = false
			}
		case c == '#' && !in:
			return out
		default:
			b.WriteByte(c)
			in = true
		}
	}
	if in {
		out = append(out, b.String())
	}
	return out
}

func readUCI(root, name string) []UCISection {
	f, err := os.Open(root + "/etc/config/" + name)
	if err != nil {
		return nil
	}
	defer f.Close()
	return ParseUCI(f)
}

// ParseLeases parses a dnsmasq lease file: "<expiry> <mac> <ip> <hostname> <client-id>".
func ParseLeases(r io.Reader) []Item {
	var out []Item
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || strings.Count(f[1], ":") != 5 {
			continue
		}
		mac := strings.ToLower(f[1])
		it := Item{Key: "lease/" + mac, Kind: KindLease, Name: f[3], IPs: []string{f[2]},
			Labels: map[string]string{"mac": mac}}
		if f[3] == "*" {
			it.Name = ""
		}
		if exp, err := strconv.ParseInt(f[0], 10, 64); err == nil && exp > 0 {
			it.Labels["expires"] = strconv.FormatInt(exp, 10)
		}
		out = append(out, it)
	}
	return out
}

// OpenWrtParts selects what the OpenWrt source reports (all when empty).
type OpenWrtParts struct {
	NoLeases, NoWifi, NoForwards, NoSQM bool
}

// ParseOpenWrtParts reads "leases,wifi,forwards,sqm" (empty = all).
func ParseOpenWrtParts(list []string) OpenWrtParts {
	if len(list) == 0 {
		return OpenWrtParts{}
	}
	has := map[string]bool{}
	for _, x := range list {
		has[strings.TrimSpace(x)] = true
	}
	return OpenWrtParts{NoLeases: !has["leases"], NoWifi: !has["wifi"], NoForwards: !has["forwards"], NoSQM: !has["sqm"]}
}

// OpenWrt reads the router state: DHCP leases (dynamic and static), Wi-Fi clients, port
// forwards and SQM. root is "" on the router (tests pass a directory).
func OpenWrt(ctx context.Context, root string, parts ...OpenWrtParts) ([]Item, error) {
	var pt OpenWrtParts
	if len(parts) > 0 {
		pt = parts[0]
	}
	items, err := openwrtAll(ctx, root)
	out := items[:0]
	for _, it := range items {
		switch {
		case it.Kind == KindLease && pt.NoLeases, it.Kind == KindWifiClient && pt.NoWifi,
			it.Kind == KindPortForward && pt.NoForwards, it.Kind == KindSQM && pt.NoSQM:
			continue
		}
		out = append(out, it)
	}
	return out, err
}

func openwrtAll(ctx context.Context, root string) ([]Item, error) {
	var items []Item
	leaseFile := "/tmp/dhcp.leases"
	for _, d := range readUCI(root, "dhcp") {
		if d.Type == "dnsmasq" && d.Options["leasefile"] != "" {
			leaseFile = d.Options["leasefile"]
		}
	}
	byMAC := map[string]int{}
	if f, err := os.Open(root + leaseFile); err == nil {
		for _, it := range ParseLeases(f) {
			byMAC[it.Labels["mac"]] = len(items)
			items = append(items, it)
		}
		f.Close()
	}
	// static leases: "config host" with name, mac (possibly several) and ip
	for _, h := range readUCI(root, "dhcp") {
		if h.Type != "host" {
			continue
		}
		macs := strings.Fields(strings.ToLower(h.Options["mac"]))
		macs = append(macs, h.Lists["mac"]...)
		for _, mac := range macs {
			mac = strings.ToLower(mac)
			if i, ok := byMAC[mac]; ok {
				items[i].Labels["static"] = "1"
				if items[i].Name == "" {
					items[i].Name = h.Options["name"]
				}
				continue
			}
			it := Item{Key: "lease/" + mac, Kind: KindLease, Name: h.Options["name"], Labels: map[string]string{"mac": mac, "static": "1"}}
			if ip := h.Options["ip"]; ip != "" {
				it.IPs = []string{ip}
			}
			byMAC[mac] = len(items)
			items = append(items, it)
		}
	}
	items = append(items, wifiClients(ctx, root)...)
	for _, r := range readUCI(root, "firewall") {
		if r.Type != "redirect" || r.Options["enabled"] == "0" || r.Options["target"] == "SNAT" {
			continue
		}
		name := r.Options["name"]
		if name == "" {
			name = r.Name
		}
		it := Item{Key: "fwd/" + name + "/" + r.Options["src_dport"], Kind: KindPortForward, Name: name,
			Labels: map[string]string{"src": r.Options["src"], "src_dport": r.Options["src_dport"], "dest_ip": r.Options["dest_ip"],
				"dest_port": r.Options["dest_port"], "proto": r.Options["proto"]}}
		if ip := r.Options["dest_ip"]; ip != "" {
			it.IPs = []string{ip}
		}
		if p, err := strconv.Atoi(strings.Split(firstNonEmpty(r.Options["dest_port"], r.Options["src_dport"]), "-")[0]); err == nil {
			for _, proto := range strings.Fields(firstNonEmpty(r.Options["proto"], "tcp udp")) {
				it.Ports = append(it.Ports, Port{Port: p, Proto: proto})
			}
		}
		items = append(items, it)
	}
	for _, q := range readUCI(root, "sqm") {
		if q.Type != "queue" || q.Options["enabled"] != "1" {
			continue
		}
		items = append(items, Item{Key: "sqm/" + q.Options["interface"], Kind: KindSQM, Name: q.Options["interface"],
			Labels: map[string]string{"download_kbit": q.Options["download"], "upload_kbit": q.Options["upload"],
				"qdisc": q.Options["qdisc"], "script": q.Options["script"]}})
	}
	sortItems(items)
	return items, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// wifiClients asks hostapd over ubus for associated stations.
func wifiClients(ctx context.Context, root string) []Item {
	if root != "" {
		// tests: canned "ubus call hostapd.* get_clients" outputs
		matches, _ := globFiles(root+"/ubus", "hostapd.")
		var out []Item
		for _, m := range matches {
			b, err := os.ReadFile(m)
			if err == nil {
				out = append(out, parseClients(strings.TrimPrefix(m[strings.LastIndex(m, "/")+1:], "hostapd."), b)...)
			}
		}
		return out
	}
	if _, err := exec.LookPath("ubus"); err != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := exec.CommandContext(cctx, "ubus", "list", "hostapd.*").Output()
	if err != nil {
		return nil
	}
	var out []Item
	for _, obj := range strings.Fields(string(list)) {
		b, err := exec.CommandContext(cctx, "ubus", "call", obj, "get_clients").Output() //nolint:gosec // object names from ubus list
		if err != nil {
			continue
		}
		out = append(out, parseClients(strings.TrimPrefix(obj, "hostapd."), b)...)
	}
	return out
}

func globFiles(dir, prefix string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, dir+"/"+e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func parseClients(iface string, b []byte) []Item {
	var resp struct {
		Freq    int `json:"freq"`
		Clients map[string]struct {
			Signal int  `json:"signal"`
			Auth   bool `json:"authorized"`
			Rate   struct {
				RX int `json:"rx"`
				TX int `json:"tx"`
			} `json:"rate"`
			Bytes struct {
				RX int64 `json:"rx"`
				TX int64 `json:"tx"`
			} `json:"bytes"`
		} `json:"clients"`
	}
	if json.Unmarshal(b, &resp) != nil {
		return nil
	}
	var out []Item
	for mac, c := range resp.Clients {
		mac = strings.ToLower(mac)
		band := "2.4"
		if resp.Freq >= 5000 {
			band = "5"
		}
		if resp.Freq >= 5925 {
			band = "6"
		}
		out = append(out, Item{Key: "wifi/" + mac, Kind: KindWifiClient, Name: mac,
			Labels: map[string]string{"mac": mac, "iface": iface, "signal": strconv.Itoa(c.Signal), "band": band,
				"rx_kbit": strconv.Itoa(c.Rate.RX), "tx_kbit": strconv.Itoa(c.Rate.TX)}})
	}
	return out
}
