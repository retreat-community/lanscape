//go:build !lanscape_small

package discovery

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type pveClient struct {
	cfg ProxmoxConfig
	hc  *http.Client
}

func (c *pveClient) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.cfg.URL, "/")+"/api2/json"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.cfg.Token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s: %s %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&env); err != nil {
		return err
	}
	return json.Unmarshal(env.Data, v)
}

type pveResource struct {
	ID     string `json:"id"`
	Type   string `json:"type"` // qemu, lxc
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Node   string `json:"node"`
	Status string `json:"status"`
	Tags   string `json:"tags"`
	Tmpl   int    `json:"template"`
}

// ParsePVENet parses a Proxmox netN value: "virtio=BC:24:11:AA:BB:CC,bridge=vmbr0,tag=30" (QEMU) or
// "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:AA:BB:CC,ip=dhcp,type=veth" (LXC).
func ParsePVENet(key, v string) NIC {
	n := NIC{Name: key}
	for _, part := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch k {
		case "bridge":
			n.Bridge = val
		case "hwaddr":
			n.MAC = strings.ToLower(val)
		case "tag":
			n.VLAN, _ = strconv.Atoi(val)
		case "name", "ip", "ip6", "gw", "gw6", "firewall", "rate", "mtu", "queues", "link_down", "type", "trunks":
		default:
			if _, err := net.ParseMAC(val); err == nil && n.MAC == "" {
				n.Model, n.MAC = k, strings.ToLower(val)
			}
		}
	}
	return n
}

// Proxmox lists VMs and containers of a Proxmox VE cluster.
func Proxmox(ctx context.Context, cfg ProxmoxConfig) ([]Item, error) {
	if cfg.URL == "" || cfg.Token == "" {
		return nil, errors.New("proxmox: url and token are required")
	}
	c := &pveClient{cfg: cfg, hc: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Insecure}}}} //nolint:gosec // opt-in for self-signed PVE certificates
	var res []pveResource
	if err := c.get(ctx, "/cluster/resources?type=vm", &res); err != nil {
		return nil, fmt.Errorf("proxmox: %w", err)
	}
	items := make([]Item, 0, len(res))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, r := range res {
		if r.Tmpl == 1 || (r.Type != "qemu" && r.Type != "lxc") {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(r pveResource) {
			defer wg.Done()
			defer func() { <-sem }()
			it := c.guest(ctx, r)
			mu.Lock()
			items = append(items, it)
			mu.Unlock()
		}(r)
	}
	wg.Wait()
	sortItems(items)
	return items, nil
}

func (c *pveClient) guest(ctx context.Context, r pveResource) Item {
	kind, api := KindVM, "qemu"
	if r.Type == "lxc" {
		kind, api = KindCT, "lxc"
	}
	it := Item{Key: fmt.Sprintf("%s/%d", api, r.VMID), Kind: kind, Name: r.Name, State: r.Status,
		Labels: map[string]string{"node": r.Node, "vmid": strconv.Itoa(r.VMID)}}
	if r.Tags != "" {
		it.Labels["tags"] = strings.ReplaceAll(r.Tags, ";", ",")
	}
	base := fmt.Sprintf("/nodes/%s/%s/%d", r.Node, api, r.VMID)
	var conf map[string]any
	if err := c.get(ctx, base+"/config", &conf); err == nil {
		if d, ok := conf["description"].(string); ok && d != "" {
			it.Labels["notes"] = strings.TrimSpace(d)
		}
		keys := make([]string, 0)
		for k := range conf {
			if strings.HasPrefix(k, "net") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			if v, ok := conf[k].(string); ok {
				if n := ParsePVENet(k, v); n.MAC != "" {
					it.NICs = append(it.NICs, n)
				}
			}
		}
	}
	if r.Status != "running" {
		return it
	}
	// addresses: QEMU guest agent or the LXC interface list
	if api == "qemu" {
		var ga struct {
			Result []struct {
				Name  string `json:"name"`
				MAC   string `json:"hardware-address"`
				Addrs []struct {
					IP   string `json:"ip-address"`
					Type string `json:"ip-address-type"`
				} `json:"ip-addresses"`
			} `json:"result"`
		}
		if c.get(ctx, base+"/agent/network-get-interfaces", &ga) == nil {
			for _, ifc := range ga.Result {
				for _, a := range ifc.Addrs {
					it.IPs = appendIP(it.IPs, a.IP)
				}
			}
		}
	} else {
		var ifs []struct {
			Name string `json:"name"`
			Inet string `json:"inet"`
		}
		if c.get(ctx, base+"/interfaces", &ifs) == nil {
			for _, ifc := range ifs {
				ip, _, _ := strings.Cut(ifc.Inet, "/")
				it.IPs = appendIP(it.IPs, ip)
			}
		}
	}
	return it
}

func appendIP(ips []string, s string) []string {
	ip := net.ParseIP(s)
	if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return ips
	}
	return append(ips, ip.String())
}
