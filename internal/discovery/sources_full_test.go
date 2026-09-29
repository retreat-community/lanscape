//go:build !lanscape_small

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
	"testing"
	"time"
)

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
		"/api/v1/pods": `{"items":[
			{"metadata":{"name":"gitea-1","namespace":"forge","labels":{"app":"gitea","pod-template-hash":"x"}},
			 "status":{"phase":"Running","containerStatuses":[{"restartCount":3},{"restartCount":1}]}},
			{"metadata":{"name":"gitea-2","namespace":"forge","labels":{"app":"gitea"}},"status":{"phase":"Pending"}},
			{"metadata":{"name":"other","namespace":"default","labels":{"app":"gitea"}},"status":{"phase":"Pending"}}]}`,
		"/apis/apps/v1/deployments": `{"items":[{"metadata":{"name":"gitea","namespace":"forge"},"spec":{"replicas":2,"selector":{"matchLabels":{"app":"gitea"}},
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
	if d := byKey["deploy/forge/gitea"]; d.Labels["restarts"] != "4" || d.Labels["pending"] != "1" {
		t.Errorf("pod stats: %+v", d.Labels)
	}
	if d := byKey["deploy/forge/gitea"]; d.State != "degraded" || d.Ready != "1/2" || d.PodLabels["app"] != "gitea" {
		t.Errorf("deployment: %+v", d)
	}
	if p := byKey["pvc/forge/data"]; p.State != "bound" || p.Labels["size"] != "10Gi" {
		t.Errorf("pvc: %+v", p)
	}
}
