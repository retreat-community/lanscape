package discovery

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retreat-community/lanscape/internal/fingerprint"
)

const procTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 222 1 0000000000000000 100 0 0 10 0
   2: 0100007F:1F90 0100007F:D431 01 00000000:00000000 00:00000000 00000000     0        0 333 1 0000000000000000 100 0 0 10 0
`

const procTCP6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:0BB8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 444 1 0000000000000000 100 0 0 10 0
   1: 0000000000000000FFFF00000A00000A:0016 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 555 1 0000000000000000 100 0 0 10 0
`

const procUDP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  10: 00000000:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 666 2 0000000000000000 0
  11: 0A00000A:A1B2 0A000001:0035 01 00000000:00000000 00:00000000 00000000     0        0 777 2 0000000000000000 0
`

func TestParseProcNet(t *testing.T) {
	tcp, err := ParseProcNet(strings.NewReader(procTCP), "tcp", false)
	if err != nil || len(tcp) != 2 {
		t.Fatalf("tcp: %+v %v", tcp, err)
	}
	if tcp[0].Addr != "0.0.0.0" || tcp[0].Port != 3000 || tcp[0].UID != 1000 || tcp[1].Addr != "127.0.0.1" || tcp[1].Port != 8080 {
		t.Errorf("tcp: %+v", tcp)
	}
	tcp6, err := ParseProcNet(strings.NewReader(procTCP6), "tcp", true)
	if err != nil || len(tcp6) != 2 || tcp6[0].Addr != "::" || tcp6[1].Addr != "10.0.0.10" || tcp6[1].Port != 22 {
		t.Fatalf("tcp6: %+v %v", tcp6, err)
	}
	udp, err := ParseProcNet(strings.NewReader(procUDP), "udp", false)
	if err != nil || len(udp) != 1 || udp[0].Port != 53 {
		t.Fatalf("udp: %+v %v", udp, err)
	}
	all := append(append(tcp, tcp6...), udp...)
	items := mergeListeners(all, func(inode uint64) (int, string, string) {
		switch inode {
		case 111, 444:
			return 42, "gitea", "0123456789ab"
		case 666:
			return 7, "dnsmasq", ""
		}
		return 0, "", ""
	}, map[int]string{1000: "git"})
	if len(items) != 4 {
		t.Fatalf("merged: %+v", items)
	}
	var gitea *Item
	for i := range items {
		if items[i].Key == "tcp/3000@*" {
			gitea = &items[i]
		}
	}
	if gitea == nil || gitea.Process != "gitea" || gitea.User != "git" || gitea.Owner != "0123456789ab" || gitea.Addr != "0.0.0.0" {
		t.Errorf("gitea socket: %+v", gitea)
	}
	if ep := endpoint(gitea); ep != "127.0.0.1:3000" {
		t.Errorf("endpoint %s", ep)
	}
}

func TestCgroupAndPasswd(t *testing.T) {
	cases := map[string]string{
		"0::/system.slice/docker-0123456789abcdef0123456789abcdef.scope":     "0123456789ab",
		"12:pids:/docker/abcdef0123456789abcdef0123456789":                   "abcdef012345",
		"0::/kubepods/besteffort/pod1/cri-containerd-fedcba9876543210.scope": "fedcba987654",
		"0::/user.slice/user-1000.slice":                                     "",
	}
	for in, want := range cases {
		if got := ContainerFromCgroup(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
	if m := ParsePasswd("root:x:0:0::/root:/bin/sh\ngit:x:1000:1000::/home/git:/bin/sh\n"); m[1000] != "git" || m[0] != "root" {
		t.Errorf("passwd: %v", m)
	}
}

func TestProbeCache(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			n++
		}
		_, _ = io.WriteString(w, "<title>Jellyfin</title>")
	}))
	defer srv.Close()
	c, _ := New(Config{Probe: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ep := strings.TrimPrefix(srv.URL, "http://")
	e := c.probe(context.Background(), ep)
	if e.app == nil || e.app.ID != "jellyfin" || e.url != srv.URL {
		t.Fatalf("probe: %+v", e)
	}
	c.probe(context.Background(), ep)
	if n != 1 {
		t.Errorf("probe not cached: %d requests", n)
	}
}

func TestSetSignatures(t *testing.T) {
	c, _ := New(Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	items := []Item{{Image: "registry.lan/acme/inventory:2.1"}, {Image: "grafana/grafana:11"}}
	c.identifyImages(items)
	if items[0].App != nil || items[1].App == nil {
		t.Fatalf("before: %+v %+v", items[0].App, items[1].App)
	}
	sigs, err := fingerprint.Parse([]byte(`[{"id":"acme-inventory","name":"ACME Inventory","category":"productivity","images":["acme/inventory"]},
		{"id":"grafana","name":"Grafana (ours)","category":"monitoring","images":["grafana/grafana"]}]`))
	if err != nil {
		t.Fatal(err)
	}
	c.SetSignatures(sigs)
	items = []Item{{Image: "registry.lan/acme/inventory:2.1"}, {Image: "grafana/grafana:11"}}
	c.identifyImages(items)
	if items[0].App == nil || items[0].App.ID != "acme-inventory" || items[1].App == nil || items[1].App.Name != "Grafana (ours)" {
		t.Errorf("after: %+v %+v", items[0].App, items[1].App)
	}
	if lib, _ := c.library(); len(lib.Sigs) != len(c.base.Sigs)+1 {
		t.Errorf("library has %d signatures, base %d", len(lib.Sigs), len(c.base.Sigs))
	}
}
