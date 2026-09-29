//go:build linux

package agent

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// CollectSystem gathers routes, rules, neighbours, environment and resources.
func CollectSystem() (routes []Route, rules []string, neigh []Neighbor, env Env, res Resources) {
	if rl, err := netlink.RouteListFiltered(netlink.FAMILY_ALL, &netlink.Route{Table: unix.RT_TABLE_UNSPEC},
		netlink.RT_FILTER_TABLE); err == nil {
		for _, r := range rl {
			if r.Table == unix.RT_TABLE_LOCAL {
				continue
			}
			rt := Route{Table: r.Table, Dst: "default", Metric: r.Priority}
			if r.Dst != nil {
				rt.Dst = r.Dst.String()
			}
			if r.Gw != nil {
				rt.Gateway = r.Gw.String()
			}
			if r.Src != nil {
				rt.Src = r.Src.String()
			}
			if l, err := netlink.LinkByIndex(r.LinkIndex); err == nil {
				rt.Dev = l.Attrs().Name
			}
			routes = append(routes, rt)
		}
	}
	if rs, err := netlink.RuleList(netlink.FAMILY_V4); err == nil {
		for _, r := range rs {
			s := fmt.Sprintf("%d:", r.Priority)
			if r.Src != nil {
				s += " from " + r.Src.String()
			} else {
				s += " from all"
			}
			if r.Dst != nil {
				s += " to " + r.Dst.String()
			}
			if r.IifName != "" {
				s += " iif " + r.IifName
			}
			if r.Mark != 0 {
				s += fmt.Sprintf(" fwmark %#x", r.Mark)
			}
			s += fmt.Sprintf(" lookup %d", r.Table)
			rules = append(rules, s)
		}
	}
	if ns, err := netlink.NeighList(0, netlink.FAMILY_ALL); err == nil {
		for _, n := range ns {
			if n.HardwareAddr == nil || n.IP == nil || n.State&(netlink.NUD_FAILED|netlink.NUD_INCOMPLETE) != 0 {
				continue
			}
			nb := Neighbor{IP: n.IP.String(), MAC: n.HardwareAddr.String(), State: neighState(n.State)}
			if l, err := netlink.LinkByIndex(n.LinkIndex); err == nil {
				nb.Dev = l.Attrs().Name
			}
			neigh = append(neigh, nb)
		}
	}
	env = DetectEnv("", os.Getenv)
	res = baseResources()
	var u unix.Utsname
	if unix.Uname(&u) == nil {
		res.Kernel = unix.ByteSliceToString(u.Release[:])
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		res.OS, res.OSVersion = ParseOSRelease(string(b))
	} else if b, err := os.ReadFile("/etc/openwrt_release"); err == nil {
		res.OS, res.OSVersion = ParseOSRelease(strings.ReplaceAll(string(b), "DISTRIB_", ""))
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		res.MemTotal, res.MemAvail = ParseMeminfo(string(b))
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			v, _ := strconv.ParseFloat(f[0], 64)
			res.UptimeS = uint64(v)
		}
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			res.Load1, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && (strings.TrimSpace(k) == "model name" || strings.TrimSpace(k) == "cpu model") {
				res.CPUModel = strings.TrimSpace(v)
				break
			}
		}
	}
	for _, z := range []string{"/sys/class/thermal/thermal_zone0/temp", "/sys/class/hwmon/hwmon0/temp1_input"} {
		if b, err := os.ReadFile(z); err == nil {
			if v, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && v > 0 {
				res.TempC = float64(v) / 1000
				break
			}
		}
	}
	res.Disks = disks()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	res.AgentMemKB = ms.Sys / 1024
	return routes, rules, neigh, env, res
}

func neighState(s int) string {
	switch {
	case s&netlink.NUD_REACHABLE != 0:
		return "reachable"
	case s&netlink.NUD_STALE != 0:
		return "stale"
	case s&netlink.NUD_PERMANENT != 0:
		return "permanent"
	case s&netlink.NUD_DELAY != 0, s&netlink.NUD_PROBE != 0:
		return "probe"
	default:
		return "other"
	}
}

var diskFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true,
	"vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "f2fs": true, "overlay": false, "squashfs": false}

func disks() []Disk {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []Disk
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || !diskFS[f[2]] || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		var st unix.Statfs_t
		if unix.Statfs(f[1], &st) != nil {
			continue
		}
		total := st.Blocks * uint64(st.Bsize)
		free := st.Bfree * uint64(st.Bsize)
		out = append(out, Disk{Mount: f[1], Device: f[0], FS: f[2], Total: total, Used: total - free})
	}
	return out
}
