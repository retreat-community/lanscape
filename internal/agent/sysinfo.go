package agent

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/retreat-community/lanscape/internal/netio"
)

// Inventory is the full report sent to the server (§5).
type Inventory struct {
	Ifaces    []netio.Iface `json:"ifaces"`
	Routes    []Route       `json:"routes,omitempty"`
	Rules     []string      `json:"rules,omitempty"`
	Neighbors []Neighbor    `json:"neighbors,omitempty"`
	Env       Env           `json:"env"`
	Resources Resources     `json:"resources"`
}

// Route is one routing table entry.
type Route struct {
	Table   int    `json:"table"`
	Dst     string `json:"dst"`
	Gateway string `json:"gateway,omitempty"`
	Dev     string `json:"dev"`
	Src     string `json:"src,omitempty"`
	Metric  int    `json:"metric"`
}

// Neighbor is an ARP/NDP entry.
type Neighbor struct {
	IP    string `json:"ip"`
	MAC   string `json:"mac"`
	Dev   string `json:"dev"`
	State string `json:"state"`
}

// Env describes where the agent runs.
type Env struct {
	Kind       string `json:"kind"` // bare-metal, vm, lxc, docker, k8s-pod, openwrt
	Virt       string `json:"virt,omitempty"`
	K8sNode    string `json:"k8s_node,omitempty"`
	Hypervisor string `json:"hypervisor,omitempty"`
	HostNet    bool   `json:"host_network"`
}

// Resources are host resources.
type Resources struct {
	OS         string  `json:"os"`
	OSVersion  string  `json:"os_version"`
	Kernel     string  `json:"kernel"`
	Arch       string  `json:"arch"`
	CPUs       int     `json:"cpus"`
	CPUModel   string  `json:"cpu_model,omitempty"`
	MemTotal   uint64  `json:"mem_total"`
	MemAvail   uint64  `json:"mem_available"`
	UptimeS    uint64  `json:"uptime_s"`
	Load1      float64 `json:"load1"`
	Disks      []Disk  `json:"disks,omitempty"`
	Updates    int     `json:"updates"`
	TempC      float64 `json:"temp_c,omitempty"`
	AgentMemKB uint64  `json:"agent_mem_kb"`
}

// Disk is a mounted filesystem.
type Disk struct {
	Mount  string `json:"mount"`
	Device string `json:"device"`
	FS     string `json:"fs"`
	Total  uint64 `json:"total"`
	Used   uint64 `json:"used"`
}

// ParseMeminfo reads MemTotal and MemAvailable (bytes).
func ParseMeminfo(s string) (total, avail uint64) {
	for _, l := range strings.Split(s, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	return total, avail
}

// ParseOSRelease returns NAME/PRETTY_NAME and VERSION_ID.
func ParseOSRelease(s string) (name, version string) {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "PRETTY_NAME":
			name = v
		case "NAME":
			if name == "" {
				name = v
			}
		case "VERSION_ID":
			version = v
		}
	}
	return name, version
}

// DetectEnv classifies the environment from well-known files (root may be "/" or a test dir).
func DetectEnv(root string, getenv func(string) string) Env {
	read := func(p string) string {
		b, _ := os.ReadFile(root + p)
		return string(b)
	}
	exists := func(p string) bool {
		_, err := os.Stat(root + p)
		return err == nil
	}
	e := Env{Kind: "bare-metal"}
	switch {
	case getenv("KUBERNETES_SERVICE_HOST") != "":
		e.Kind = "k8s-pod"
		e.K8sNode = getenv("NODE_NAME")
	case exists("/.dockerenv") || exists("/run/.containerenv"):
		e.Kind = "docker"
	case strings.Contains(read("/proc/1/environ"), "container=lxc") || exists("/dev/lxd/sock"):
		e.Kind = "lxc"
	case exists("/etc/openwrt_release"):
		e.Kind = "openwrt"
	}
	vendor := strings.TrimSpace(read("/sys/class/dmi/id/sys_vendor") + " " + read("/sys/class/dmi/id/product_name"))
	for _, v := range []struct{ match, name string }{
		{"QEMU", "kvm"}, {"KVM", "kvm"}, {"VMware", "vmware"}, {"VirtualBox", "virtualbox"},
		{"Microsoft Corporation", "hyper-v"}, {"Xen", "xen"}, {"Proxmox", "kvm"}, {"Parallels", "parallels"},
	} {
		if strings.Contains(vendor, v.match) {
			e.Virt = v.name
		}
	}
	if e.Virt == "" && strings.Contains(read("/proc/cpuinfo"), " hypervisor") {
		e.Virt = "vm"
	}
	if e.Kind == "bare-metal" && e.Virt != "" {
		e.Kind = "vm"
	}
	if exists("/etc/pve") {
		e.Hypervisor = "proxmox"
	}
	return e
}

func baseResources() Resources {
	return Resources{Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), OS: runtime.GOOS}
}
