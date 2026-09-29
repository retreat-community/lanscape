//go:build !lanscape_small

package discovery

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestartDocker(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("unix sockets unavailable:", err)
	}
	var got string
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	if _, err := Restart(context.Background(), Config{DockerSocket: sock}, SourceDocker, "container/gitea"); err != nil {
		t.Fatal(err)
	}
	if got != "POST /containers/gitea/restart" {
		t.Errorf("request: %s", got)
	}
	if _, err := Restart(context.Background(), Config{DockerSocket: sock}, SourceDocker, "proc/1"); err == nil {
		t.Error("bad key accepted")
	}
}

func TestRestartWorkload(t *testing.T) {
	var path, ctype string
	var patch map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ctype = r.Method+" "+r.URL.Path, r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&patch)
		_, _ = io.WriteString(w, "{}")
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
	if _, err := Restart(context.Background(), Config{Kubeconfig: kc}, SourceK8s, "deploy/forge/gitea"); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(patch)
	if path != "PATCH /apis/apps/v1/namespaces/forge/deployments/gitea" || ctype != "application/strategic-merge-patch+json" ||
		!strings.Contains(string(b), "kubectl.kubernetes.io/restartedAt") {
		t.Errorf("%s %s %s", path, ctype, b)
	}
	if _, err := Restart(context.Background(), Config{Kubeconfig: kc}, SourceK8s, "svc/forge/gitea"); err == nil {
		t.Error("service restart accepted")
	}
}

func TestRebootGuest(t *testing.T) {
	var reboot string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api2/json/cluster/resources":
			_, _ = io.WriteString(w, `{"data":[{"type":"qemu","vmid":101,"node":"pve2"},{"type":"lxc","vmid":101,"node":"pve1"}]}`)
		case r.Method == http.MethodPost:
			reboot = r.URL.Path
			_, _ = io.WriteString(w, `{"data":"UPID:pve1:x"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg := Config{Proxmox: ProxmoxConfig{URL: srv.URL, Token: "root@pam!t=x", Insecure: true}}
	detail, err := Restart(context.Background(), cfg, SourceProxmox, "lxc/101")
	if err != nil || reboot != "/api2/json/nodes/pve1/lxc/101/status/reboot" || !strings.Contains(detail, "UPID") {
		t.Fatalf("%q %q %v", reboot, detail, err)
	}
	if _, err := Restart(context.Background(), cfg, SourceProxmox, "qemu/999"); err == nil {
		t.Error("unknown guest accepted")
	}
}
