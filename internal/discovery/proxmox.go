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
	c := &pveClient{cfg: cfg, hc: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{IdleConnTimeout: 30 * time.Second,
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
	var st []pveStorage
	if err := c.get(ctx, "/cluster/resources?type=storage", &st); err == nil {
		items = append(items, StorageItems(st)...)
	}
	sortItems(items)
	return items, nil
}

// pveStorage is a storage of a node in /cluster/resources?type=storage.
type pveStorage struct {
	Storage string `json:"storage"`
	Node    string `json:"node"`
	Status  string `json:"status"` // available, unknown …
	Disk    int64  `json:"disk"`
	MaxDisk int64  `json:"maxdisk"`
	Type    string `json:"plugintype"`
	Shared  int    `json:"shared"`
}

// StorageItems turns storages into items; a shared storage is reported once.
func StorageItems(st []pveStorage) []Item {
	var out []Item
	seen := map[string]bool{}
	for _, s := range st {
		key := "storage/" + s.Node + "/" + s.Storage
		if s.Shared == 1 {
			key = "storage/" + s.Storage
		}
		if seen[key] || s.Storage == "" {
			continue
		}
		seen[key] = true
		it := Item{Key: key, Kind: KindStorage, Name: s.Storage, State: "online", Labels: map[string]string{
			"node": s.Node, "type": s.Type, "size": strconv.FormatInt(s.MaxDisk, 10), "alloc": strconv.FormatInt(s.Disk, 10)}}
		if s.Shared == 1 {
			it.Labels["shared"] = "1"
		}
		switch pct := usedPct(s.Disk, s.MaxDisk); {
		case s.Status != "" && s.Status != "available":
			it.State = "unavailable"
		case pct >= StorageFullPct:
			it.State = "full"
		case pct >= 90:
			it.State = "warning"
		}
		out = append(out, it)
	}
	return out
}

func usedPct(used, total int64) int64 {
	if total <= 0 {
		return 0
	}
	return used * 100 / total
}

// diskKey matches the configuration keys of guest disks.
func diskKey(k string) bool {
	for _, p := range []string{"scsi", "virtio", "sata", "ide", "efidisk", "tpmstate", "unused", "mp"} {
		if strings.HasPrefix(k, p) && len(k) > len(p) && k[len(p)] >= '0' && k[len(p)] <= '9' {
			return true
		}
	}
	return k == "rootfs"
}

// GuestStorages lists the storages that hold a guest's disks ("local-lvm:vm-100-disk-0,size=32G").
func GuestStorages(conf map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	for k, v := range conf {
		val, ok := v.(string)
		if !ok || !diskKey(k) || strings.Contains(val, "media=cdrom") {
			continue
		}
		st, _, found := strings.Cut(strings.Split(val, ",")[0], ":")
		if !found || st == "" || strings.HasPrefix(st, "/") || seen[st] {
			continue
		}
		seen[st] = true
		out = append(out, st)
	}
	sort.Strings(out)
	return out
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
		if st := GuestStorages(conf); len(st) > 0 {
			it.Labels["storages"] = strings.Join(st, ",")
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
