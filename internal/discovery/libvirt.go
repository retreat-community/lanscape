//go:build !lanscape_small

package discovery

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// virsh runs a read-only virsh command against the local hypervisor.
var virsh = func(ctx context.Context, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "virsh", append([]string{"-q", "-r"}, args...)...).Output() //nolint:gosec // fixed virsh subcommands
	return string(out), err
}

// LibvirtAvailable reports whether virsh and the libvirt socket exist.
func LibvirtAvailable() bool {
	if _, err := exec.LookPath("virsh"); err != nil {
		return false
	}
	_, err := os.Stat("/var/run/libvirt/libvirt-sock")
	return err == nil
}

// Libvirt lists the guests of the local libvirt/KVM hypervisor with their state, interfaces and
// addresses (guest agent, DHCP leases or the ARP table).
func Libvirt(ctx context.Context) ([]Item, error) {
	list, err := virsh(ctx, "list", "--all", "--name")
	if err != nil {
		return nil, errors.New("libvirt: " + strings.TrimSpace(err.Error()))
	}
	host, _ := os.Hostname()
	var items []Item
	for _, name := range strings.Fields(list) {
		it := Item{Key: "libvirt/" + name, Kind: KindVM, Name: name, Labels: map[string]string{"node": host, "hypervisor": "libvirt"}}
		if st, err := virsh(ctx, "domstate", name); err == nil {
			it.State = libvirtState(strings.TrimSpace(st))
		}
		if out, err := virsh(ctx, "domiflist", name); err == nil {
			it.NICs = ParseDomIfList(out)
		}
		if it.State == "running" {
			for _, src := range []string{"agent", "lease", "arp"} {
				out, err := virsh(ctx, "domifaddr", name, "--source", src)
				if err != nil {
					continue
				}
				for _, ip := range ParseDomIfAddr(out) {
					it.IPs = appendIP(it.IPs, ip)
				}
				if len(it.IPs) > 0 {
					break
				}
			}
		}
		items = append(items, it)
	}
	sortItems(items)
	return items, nil
}

// libvirtState maps "shut off", "paused" … to the states used by Proxmox guests.
func libvirtState(s string) string {
	switch s {
	case "running", "idle":
		return "running"
	case "shut off", "shutoff":
		return "stopped"
	case "":
		return "unknown"
	}
	return strings.ReplaceAll(s, " ", "_")
}

// ParseDomIfList parses "virsh -q domiflist": Interface Type Source Model MAC.
func ParseDomIfList(out string) []NIC {
	var nics []NIC
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || strings.HasPrefix(f[0], "---") || f[0] == "Interface" {
			continue
		}
		n := NIC{Name: f[0], Model: f[3], MAC: strings.ToLower(f[4])}
		if f[1] == "bridge" || f[1] == "direct" {
			n.Bridge = f[2]
		}
		nics = append(nics, n)
	}
	return nics
}

// ParseDomIfAddr parses "virsh -q domifaddr": Name MAC Protocol Address/prefix.
func ParseDomIfAddr(out string) []string {
	var ips []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || strings.HasPrefix(f[0], "---") || f[0] == "Name" {
			continue
		}
		addr, _, _ := strings.Cut(f[len(f)-1], "/")
		ips = append(ips, addr)
	}
	return ips
}
