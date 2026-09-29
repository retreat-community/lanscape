package discovery

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

const dockerJSON = `[{"Id":"0123456789abcdef","Names":["/gitea"],"Image":"gitea/gitea:1.22","State":"running",
"Status":"Up 2 hours (healthy)","Labels":{"com.docker.compose.project":"forge","com.docker.compose.service":"gitea","other":"x"},
"Ports":[{"IP":"0.0.0.0","PrivatePort":3000,"PublicPort":3000,"Type":"tcp"},{"IP":"::","PrivatePort":3000,"PublicPort":3000,"Type":"tcp"},{"PrivatePort":22,"Type":"tcp"}],
"NetworkSettings":{"Networks":{"forge_default":{"IPAddress":"172.18.0.2"}}}},
{"Id":"fedcba9876543210","Names":["/old"],"Image":"postgres:16","State":"exited","Status":"Exited (0) 3 days ago","Labels":{},"Ports":[]}]`

func TestDocker(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("unix sockets unavailable:", err)
	}
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" || r.URL.Query().Get("all") != "1" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, dockerJSON)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	items, err := Docker(context.Background(), sock)
	if err != nil || len(items) != 2 {
		t.Fatalf("docker: %+v %v", items, err)
	}
	g := items[0]
	if g.Name != "gitea" || g.Health != "healthy" || g.Project != "forge" || len(g.Ports) != 2 || g.Ports[1].Port != 3000 ||
		g.Owner != "0123456789ab" || g.IPs[0] != "172.18.0.2" || g.Labels["other"] != "" {
		t.Errorf("gitea: %+v", g)
	}
	c, err := New(Config{Docker: true, DockerSocket: sock}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	rep := c.Collect(context.Background())
	if len(rep.Sources) != 1 || rep.Sources[0].Items[0].App == nil || rep.Sources[0].Items[0].App.ID != "gitea" ||
		rep.Sources[0].Items[1].App == nil || rep.Sources[0].Items[1].App.ID != "postgresql" {
		t.Errorf("collect: %+v", rep)
	}
}

func TestKubernetes(t *testing.T) {
	api := map[string]string{
		"/api/v1/services": `{"items":[
			{"metadata":{"name":"kubernetes","namespace":"default"},"spec":{"clusterIP":"10.96.0.1"}},
			{"metadata":{"name":"gitea-http","namespace":"forge"},"spec":{"type":"ClusterIP","clusterIP":"10.96.5.5",
			 "selector":{"app":"gitea"},"ports":[{"port":3000,"targetPort":3000,"protocol":"TCP"}]}},
			{"metadata":{"name":"lb","namespace":"forge"},"spec":{"type":"LoadBalancer","clusterIP":"10.96.5.6",
			 "ports":[{"port":443,"targetPort":"https","nodePort":30443,"protocol":"TCP"}]},"status":{"loadBalancer":{"ingress":[{"ip":"192.168.1.240"}]}}}]}`,
		"/apis/networking.k8s.io/v1/ingresses": `{"items":[{"metadata":{"name":"gitea","namespace":"forge"},
			"spec":{"tls":[{"hosts":["git.example"]}],"rules":[{"host":"git.example","http":{"paths":[
			{"backend":{"service":{"name":"gitea-http","port":{"number":3000}}}}]}}]}}]}`,
		"/apis/apps/v1/deployments": `{"items":[{"metadata":{"name":"gitea","namespace":"forge"},"spec":{"replicas":2,
			"template":{"metadata":{"labels":{"app":"gitea"}},"spec":{"containers":[{"image":"gitea/gitea:1.22"}]}}},
			"status":{"replicas":2,"readyReplicas":1}}]}`,
		"/apis/apps/v1/statefulsets":     `{"items":[]}`,
		"/apis/apps/v1/daemonsets":       `{"items":[]}`,
		"/api/v1/persistentvolumeclaims": `{"items":[{"metadata":{"name":"data","namespace":"forge"},"spec":{"storageClassName":"longhorn","resources":{"requests":{"storage":"10Gi"}}},"status":{"phase":"Bound"}}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, ok := api[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	kc := filepath.Join(t.TempDir(), "config")
	_ = os.WriteFile(kc, []byte(`apiVersion: v1
current-context: test
contexts:
- name: test
  context: {cluster: c, user: u}
clusters:
- name: c
  cluster: {server: "`+srv.URL+`"}
users:
- name: u
  user: {token: tok}
`), 0o600)
	items, err := Kubernetes(context.Background(), kc)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Item{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	if len(items) != 5 {
		t.Errorf("items: %d %+v", len(items), items)
	}
	if s := byKey["svc/forge/gitea-http"]; s.Selector["app"] != "gitea" || s.Ports[0].Target != 3000 || s.IPs[0] != "10.96.5.5" {
		t.Errorf("service: %+v", s)
	}
	if s := byKey["svc/forge/lb"]; len(s.IPs) != 2 || s.IPs[1] != "192.168.1.240" || s.Ports[0].NodePort != 30443 {
		t.Errorf("lb: %+v", s)
	}
	if in := byKey["ingress/forge/gitea"]; !in.TLS || in.Hosts[0] != "git.example" || in.Backends[0] != "forge/gitea-http:3000" {
		t.Errorf("ingress: %+v", in)
	}
	if d := byKey["deploy/forge/gitea"]; d.State != "degraded" || d.Ready != "1/2" || d.PodLabels["app"] != "gitea" {
		t.Errorf("deployment: %+v", d)
	}
	if p := byKey["pvc/forge/data"]; p.State != "bound" || p.Labels["size"] != "10Gi" {
		t.Errorf("pvc: %+v", p)
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
