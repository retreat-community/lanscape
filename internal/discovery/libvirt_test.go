//go:build !lanscape_small

package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLibvirt(t *testing.T) {
	out := map[string]string{
		"list --all --name": "web\nwin11\n\n",
		"domstate web":      "running\n",
		"domstate win11":    "shut off\n",
		"domiflist web":     " vnet0   bridge   br0       virtio   52:54:00:12:34:56\n -       network  default   e1000    52:54:00:AB:CD:EF\n",
		"domiflist win11":   " -       bridge   br0       e1000    52:54:00:00:00:02\n",
		"domifaddr web --source agent": " lo      00:00:00:00:00:00   ipv4   127.0.0.1/8\n" +
			" eth0    52:54:00:12:34:56   ipv4   192.168.1.50/24\n -       -                   ipv6   fe80::5054:ff:fe12:3456/64\n",
	}
	old := virsh
	defer func() { virsh = old }()
	virsh = func(_ context.Context, args ...string) (string, error) {
		if o, ok := out[strings.Join(args, " ")]; ok {
			return o, nil
		}
		return "", errors.New("error: no such command")
	}
	items, err := Libvirt(context.Background())
	if err != nil || len(items) != 2 {
		t.Fatalf("%v %+v", err, items)
	}
	web, win := items[0], items[1]
	if web.Key != "libvirt/web" || web.Kind != KindVM || web.State != "running" || len(web.IPs) != 1 || web.IPs[0] != "192.168.1.50" {
		t.Errorf("web: %+v", web)
	}
	if len(web.NICs) != 2 || web.NICs[0].Bridge != "br0" || web.NICs[0].MAC != "52:54:00:12:34:56" || web.NICs[1].Bridge != "" ||
		web.NICs[1].MAC != "52:54:00:ab:cd:ef" {
		t.Errorf("nics: %+v", web.NICs)
	}
	if win.State != "stopped" || len(win.IPs) != 0 || win.Labels["hypervisor"] != "libvirt" {
		t.Errorf("win11: %+v", win)
	}
}
