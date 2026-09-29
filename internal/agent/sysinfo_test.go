package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSysinfoParsers(t *testing.T) {
	tot, av := ParseMeminfo("MemTotal:       16318412 kB\nMemFree: 1 kB\nMemAvailable:    8159206 kB\n")
	if tot != 16318412*1024 || av != 8159206*1024 {
		t.Errorf("meminfo %d %d", tot, av)
	}
	n, v := ParseOSRelease("NAME=\"Debian GNU/Linux\"\nVERSION_ID=\"12\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n")
	if n != "Debian GNU/Linux 12 (bookworm)" || v != "12" {
		t.Errorf("os-release %q %q", n, v)
	}
}

func TestDetectEnv(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(root+p), 0o755)
		_ = os.WriteFile(root+p, []byte(s), 0o644)
	}
	none := func(string) string { return "" }
	if e := DetectEnv(root, none); e.Kind != "bare-metal" {
		t.Errorf("empty: %+v", e)
	}
	write("/sys/class/dmi/id/sys_vendor", "QEMU")
	if e := DetectEnv(root, none); e.Kind != "vm" || e.Virt != "kvm" {
		t.Errorf("vm: %+v", e)
	}
	write("/etc/openwrt_release", "DISTRIB_ID='OpenWrt'")
	if e := DetectEnv(root, none); e.Kind != "openwrt" {
		t.Errorf("openwrt: %+v", e)
	}
	write("/.dockerenv", "")
	if e := DetectEnv(root, none); e.Kind != "docker" {
		t.Errorf("docker: %+v", e)
	}
	k8s := func(k string) string {
		return map[string]string{"KUBERNETES_SERVICE_HOST": "10.0.0.1", "NODE_NAME": "k8s-0"}[k]
	}
	if e := DetectEnv(root, k8s); e.Kind != "k8s-pod" || e.K8sNode != "k8s-0" {
		t.Errorf("k8s: %+v", e)
	}
}
