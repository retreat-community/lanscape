// Package netio inspects local interfaces, link speed, counters and routes, and binds
// sockets to devices. Linux has the full implementation; other systems get a portable
// subset based on the standard library.
package netio

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
)

// Iface describes one network interface.
type Iface struct {
	Name     string   `json:"name"`
	Index    int      `json:"index"`
	MAC      string   `json:"mac"`
	MTU      int      `json:"mtu"`
	Up       bool     `json:"up"`
	Carrier  bool     `json:"carrier"`
	Kind     string   `json:"kind"` // physical, bridge, bond, vlan, macvlan, veth, wireguard, tun, loopback, ...
	Parent   string   `json:"parent,omitempty"`
	Master   string   `json:"master,omitempty"`
	Members  []string `json:"members,omitempty"`
	BondMode string   `json:"bond_mode,omitempty"`
	VLAN     int      `json:"vlan,omitempty"`
	Speed    int      `json:"speed"` // Mbit/s, 0 = unknown
	Addrs    []Addr   `json:"addrs"`
}

// Addr is an interface address with prefix length.
type Addr struct {
	IP     string `json:"ip"`
	Prefix int    `json:"prefix"`
}

// Counters are interface byte counters.
type Counters struct {
	RX uint64
	TX uint64
}

// ErrUnsupported is returned where the platform lacks a feature.
var ErrUnsupported = errors.New("netio: not supported on this platform")

// GlobMatch reports whether name matches any comma-separated glob in list.
func GlobMatch(list, name string) bool {
	for _, p := range strings.Split(list, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// Filter drops excluded and loopback interfaces.
func Filter(ifs []Iface, exclude []string) []Iface {
	out := ifs[:0:0]
	for _, i := range ifs {
		if i.Kind == "loopback" || GlobMatch(strings.Join(exclude, ","), i.Name) {
			continue
		}
		out = append(out, i)
	}
	return out
}

// CPU is a /proc/stat sample.
type CPU struct {
	Busy, Total uint64
}

// ParseStat parses the aggregate cpu line of /proc/stat.
func ParseStat(r io.Reader) (CPU, error) {
	s := bufio.NewScanner(r)
	if !s.Scan() {
		return CPU{}, errors.New("netio: empty /proc/stat")
	}
	f := strings.Fields(s.Text())
	if len(f) < 5 || f[0] != "cpu" {
		return CPU{}, fmt.Errorf("netio: unexpected /proc/stat line %q", s.Text())
	}
	var v [8]uint64
	for i := 1; i < len(f) && i <= 8; i++ {
		n, err := strconv.ParseUint(f[i], 10, 64)
		if err != nil {
			return CPU{}, fmt.Errorf("netio: /proc/stat: %w", err)
		}
		v[i-1] = n
	}
	total := v[0] + v[1] + v[2] + v[3] + v[4] + v[5] + v[6] + v[7]
	return CPU{Busy: total - v[3] - v[4], Total: total}, nil
}

// ReadCPU samples the CPU counters (Linux only; zero elsewhere).
func ReadCPU() CPU {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return CPU{}
	}
	defer f.Close()
	c, _ := ParseStat(f)
	return c
}

// Permille returns the busy share between two samples in per mille.
func Permille(a, b CPU) int {
	if b.Total <= a.Total || b.Busy < a.Busy {
		return 0
	}
	return int((b.Busy - a.Busy) * 1000 / (b.Total - a.Total))
}

// ParseVLANConfig reads /proc/net/vlan/config content: device -> (vid, parent).
func ParseVLANConfig(r io.Reader) map[string][2]string {
	out := map[string][2]string{}
	s := bufio.NewScanner(r)
	for s.Scan() {
		f := strings.FieldsFunc(s.Text(), func(c rune) bool { return c == '|' || c == ' ' || c == '\t' })
		if len(f) != 3 {
			continue
		}
		if _, err := strconv.Atoi(f[1]); err != nil {
			continue
		}
		out[f[0]] = [2]string{f[1], f[2]}
	}
	return out
}
