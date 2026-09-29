//go:build !lanscape_small

package discovery

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Hardware collects UPS state, disk health and storage pools.
func Hardware(ctx context.Context, cfg HostConfig) ([]Item, error) {
	var items []Item
	var errs []string
	if cfg.NUT != "" {
		it, err := NUT(ctx, cfg.NUT)
		if err != nil {
			errs = append(errs, err.Error())
		}
		items = append(items, it...)
	}
	if cfg.SMART {
		items = append(items, smart(ctx)...)
	}
	if cfg.ZFS {
		items = append(items, zpools(ctx)...)
	}
	if cfg.IPMI {
		items = append(items, ipmiPower(ctx)...)
	}
	if len(cfg.Plugs) > 0 {
		hc := &http.Client{Timeout: 5 * time.Second}
		for _, p := range cfg.Plugs {
			items = append(items, plugPower(ctx, hc, p))
		}
	}
	sortItems(items)
	if len(items) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("host: %s", strings.Join(errs, "; "))
	}
	return items, nil
}

// NUT reads all UPS from a Network UPS Tools server.
func NUT(ctx context.Context, addr string) ([]Item, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("nut: %w", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(c)
	list := func(cmd, prefix string) ([][]string, error) {
		if _, err := fmt.Fprintf(c, "%s\n", cmd); err != nil {
			return nil, err
		}
		var out [][]string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return out, err
			}
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "ERR"):
				return nil, fmt.Errorf("nut: %s", line)
			case strings.HasPrefix(line, "END LIST"):
				return out, nil
			case strings.HasPrefix(line, prefix+" "):
				out = append(out, nutFields(line))
			}
		}
	}
	upses, err := list("LIST UPS", "UPS")
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, u := range upses {
		if len(u) < 2 {
			continue
		}
		name := u[1]
		vars, err := list("LIST VAR "+name, "VAR")
		if err != nil {
			continue
		}
		lb := map[string]string{}
		for _, v := range vars {
			if len(v) >= 4 {
				lb[v[2]] = v[3]
			}
		}
		it := Item{Key: "ups/" + name, Kind: KindUPS, Name: name, State: nutState(lb["ups.status"]), Labels: map[string]string{
			"status": lb["ups.status"], "charge": lb["battery.charge"], "load": lb["ups.load"], "runtime_s": lb["battery.runtime"],
			"model": strings.TrimSpace(lb["device.mfr"] + " " + lb["device.model"])}}
		if len(u) > 2 {
			it.Labels["description"] = u[2]
		}
		items = append(items, it)
	}
	return items, nil
}

// nutFields splits a NUT protocol line honouring double quotes.
func nutFields(line string) []string {
	var out []string
	var b strings.Builder
	inQ, have := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && inQ && i+1 < len(line):
			i++
			b.WriteByte(line[i])
		case c == '"':
			inQ, have = !inQ, true
		case c == ' ' && !inQ:
			if have {
				out = append(out, b.String())
				b.Reset()
				have = false
			}
		default:
			b.WriteByte(c)
			have = true
		}
	}
	if have {
		out = append(out, b.String())
	}
	return out
}

// nutState maps ups.status flags (OL, OB, LB, RB …) to online, on_battery, low_battery.
func nutState(s string) string {
	f := " " + s + " "
	switch {
	case strings.Contains(f, " LB "):
		return "low_battery"
	case strings.Contains(f, " OB "):
		return "on_battery"
	case strings.Contains(f, " RB "):
		return "replace_battery"
	case strings.Contains(f, " OL "):
		return "online"
	}
	return strings.ToLower(s)
}

// smartctlJSON holds the fields of "smartctl -j -H -A -i" that the dashboard shows.
type smartctlJSON struct {
	Device struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"device"`
	ModelName    string `json:"model_name"`
	SerialNumber string `json:"serial_number"`
	UserCapacity struct {
		Bytes int64 `json:"bytes"`
	} `json:"user_capacity"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature struct {
		Current int `json:"current"`
	} `json:"temperature"`
	PowerOnTime struct {
		Hours int `json:"hours"`
	} `json:"power_on_time"`
	ATA struct {
		Table []struct {
			ID  int `json:"id"`
			Raw struct {
				Value int64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NVMe struct {
		PercentageUsed int   `json:"percentage_used"`
		MediaErrors    int64 `json:"media_errors"`
	} `json:"nvme_smart_health_information_log"`
}

// ParseSmart converts smartctl JSON output into a disk item.
func ParseSmart(b []byte) (Item, bool) {
	var s smartctlJSON
	if json.Unmarshal(b, &s) != nil || s.Device.Name == "" {
		return Item{}, false
	}
	it := Item{Key: "disk/" + firstNonEmpty(s.SerialNumber, s.Device.Name), Kind: KindDisk, Name: s.Device.Name,
		Labels: map[string]string{"model": s.ModelName, "serial": s.SerialNumber, "bytes": strconv.FormatInt(s.UserCapacity.Bytes, 10),
			"temp_c": strconv.Itoa(s.Temperature.Current), "power_on_hours": strconv.Itoa(s.PowerOnTime.Hours)}}
	it.State = "passed"
	if s.SmartStatus != nil && !s.SmartStatus.Passed {
		it.State = "failing"
	}
	for _, a := range s.ATA.Table {
		switch a.ID {
		case 5: // reallocated sectors
			it.Labels["reallocated"] = strconv.FormatInt(a.Raw.Value, 10)
			if a.Raw.Value > 0 && it.State == "passed" {
				it.State = "warning"
			}
		case 197: // pending sectors
			it.Labels["pending"] = strconv.FormatInt(a.Raw.Value, 10)
			if a.Raw.Value > 0 && it.State == "passed" {
				it.State = "warning"
			}
		}
	}
	if s.NVMe.PercentageUsed > 0 {
		it.Labels["wear_pct"] = strconv.Itoa(s.NVMe.PercentageUsed)
		if s.NVMe.PercentageUsed >= 90 && it.State == "passed" {
			it.State = "warning"
		}
	}
	if s.NVMe.MediaErrors > 0 && it.State == "passed" {
		it.State = "warning"
	}
	return it, true
}

func smart(ctx context.Context) []Item {
	if _, err := exec.LookPath("smartctl"); err != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	scan, err := exec.CommandContext(cctx, "smartctl", "--scan-open", "-j").Output()
	if err != nil && len(scan) == 0 {
		return nil
	}
	var devs struct {
		Devices []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"devices"`
	}
	if json.Unmarshal(scan, &devs) != nil {
		return nil
	}
	var out []Item
	for _, d := range devs.Devices {
		// smartctl uses exit bits for disk states; the JSON is valid anyway
		b, _ := exec.CommandContext(cctx, "smartctl", "-j", "-H", "-A", "-i", "-d", d.Type, d.Name).Output() //nolint:gosec // device list from smartctl
		if it, ok := ParseSmart(b); ok {
			out = append(out, it)
		}
	}
	return out
}

// ParseZpool parses "zpool list -Hp -o name,size,alloc,free,health".
func ParseZpool(s string) []Item {
	var out []Item
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 5 {
			continue
		}
		out = append(out, Item{Key: "pool/" + f[0], Kind: KindPool, Name: f[0], State: strings.ToLower(f[4]),
			Labels: map[string]string{"size": f[1], "alloc": f[2], "free": f[3]}})
	}
	return out
}

func zpools(ctx context.Context) []Item {
	if _, err := exec.LookPath("zpool"); err != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	b, err := exec.CommandContext(cctx, "zpool", "list", "-Hp", "-o", "name,size,alloc,free,health").Output()
	if err != nil {
		return nil
	}
	return ParseZpool(string(b))
}
