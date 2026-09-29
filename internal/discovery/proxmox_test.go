//go:build !lanscape_small

package discovery

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxmox(t *testing.T) {
	api := map[string]string{
		"/api2/json/cluster/resources": `{"data":[
			{"id":"qemu/100","type":"qemu","vmid":100,"name":"nas","node":"pve1","status":"running","tags":"storage;prod"},
			{"id":"lxc/200","type":"lxc","vmid":200,"name":"dns","node":"pve1","status":"running"},
			{"id":"qemu/900","type":"qemu","vmid":900,"name":"tpl","node":"pve1","status":"stopped","template":1},
			{"id":"qemu/101","type":"qemu","vmid":101,"name":"old","node":"pve2","status":"stopped"}]}`,
		"/api2/json/nodes/pve1/qemu/100/config": `{"data":{"description":"TrueNAS","net0":"virtio=BC:24:11:AA:BB:CC,bridge=vmbr0,firewall=1",
			"net1":"virtio=BC:24:11:AA:BB:DD,bridge=vmbr1,tag=30","scsi0":"local:vm-100-disk-0"}}`,
		"/api2/json/nodes/pve1/qemu/100/agent/network-get-interfaces": `{"data":{"result":[
			{"name":"lo","hardware-address":"00:00:00:00:00:00","ip-addresses":[{"ip-address":"127.0.0.1","ip-address-type":"ipv4"}]},
			{"name":"eth0","hardware-address":"bc:24:11:aa:bb:cc","ip-addresses":[{"ip-address":"192.168.1.20","ip-address-type":"ipv4"},
			{"ip-address":"fe80::1","ip-address-type":"ipv6"}]}]}}`,
		"/api2/json/nodes/pve1/lxc/200/config":     `{"data":{"net0":"name=eth0,bridge=vmbr0,hwaddr=BC:24:11:00:00:01,ip=dhcp,type=veth"}}`,
		"/api2/json/nodes/pve1/lxc/200/interfaces": `{"data":[{"name":"lo","inet":"127.0.0.1/8"},{"name":"eth0","inet":"192.168.1.53/24"}]}`,
		"/api2/json/nodes/pve2/qemu/101/config":    `{"data":{"net0":"e1000=BC:24:11:00:00:02,bridge=vmbr0"}}`,
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken=root@pam!ls=secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if b, ok := api[r.URL.Path]; ok {
			_, _ = io.WriteString(w, b)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	items, err := Proxmox(context.Background(), ProxmoxConfig{URL: srv.URL, Token: "root@pam!ls=secret", Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items: %+v", items)
	}
	by := map[string]Item{}
	for _, it := range items {
		by[it.Key] = it
	}
	nas := by["qemu/100"]
	if nas.Kind != KindVM || nas.State != "running" || len(nas.IPs) != 1 || nas.IPs[0] != "192.168.1.20" ||
		nas.Labels["notes"] != "TrueNAS" || nas.Labels["tags"] != "storage,prod" || len(nas.NICs) != 2 ||
		nas.NICs[0].MAC != "bc:24:11:aa:bb:cc" || nas.NICs[0].Bridge != "vmbr0" || nas.NICs[1].VLAN != 30 {
		t.Errorf("nas: %+v", nas)
	}
	if ct := by["lxc/200"]; ct.Kind != KindCT || len(ct.IPs) != 1 || ct.NICs[0].MAC != "bc:24:11:00:00:01" {
		t.Errorf("ct: %+v", ct)
	}
	if old := by["qemu/101"]; old.State != "stopped" || len(old.IPs) != 0 || old.NICs[0].Model != "e1000" {
		t.Errorf("stopped vm: %+v", old)
	}
	if _, err := Proxmox(context.Background(), ProxmoxConfig{URL: srv.URL, Token: "bad", Insecure: true}); err == nil {
		t.Error("bad token accepted")
	}
}
