//go:build !lanscape_small

package discovery

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Restart restarts a discovered object: a Docker container ("container/<name>"), a Kubernetes
// workload ("deploy|sts|ds/<namespace>/<name>", rolling restart like kubectl rollout restart)
// or a Proxmox guest ("qemu|lxc/<vmid>", reboot).
func Restart(ctx context.Context, cfg Config, source, key string) (string, error) {
	switch source {
	case SourceDocker:
		name, ok := strings.CutPrefix(key, "container/")
		if !ok || name == "" {
			return "", fmt.Errorf("docker: bad key %q", key)
		}
		return "restarted container " + name, restartDocker(ctx, orDefault(cfg.DockerSocket, "/var/run/docker.sock"), name)
	case SourceK8s:
		return restartWorkload(ctx, cfg.Kubeconfig, key)
	case SourceProxmox:
		return rebootGuest(ctx, cfg.Proxmox, key)
	case SourceLibvirt:
		return rebootLibvirt(ctx, key)
	}
	return "", fmt.Errorf("restart is not supported for %s", source)
}

// rebootLibvirt reboots a libvirt guest ("libvirt/<name>").
func rebootLibvirt(ctx context.Context, key string) (string, error) {
	name, ok := strings.CutPrefix(key, "libvirt/")
	if !ok || name == "" {
		return "", fmt.Errorf("libvirt: bad key %q", key)
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := exec.CommandContext(cctx, "virsh", "reboot", "--domain", name).CombinedOutput() //nolint:gosec // name of a discovered guest
	if err != nil {
		return "", fmt.Errorf("libvirt: %s", strings.TrimSpace(string(out)))
	}
	return "rebooting " + name, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func restartDocker(ctx context.Context, sock, name string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/containers/"+url.PathEscape(name)+"/restart?t=10", nil)
	if err != nil {
		return err
	}
	cl := dockerClient(sock)
	cl.Timeout = 60 * time.Second
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("docker: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

var workloadResource = map[string]string{"deploy": "deployments", "sts": "statefulsets", "ds": "daemonsets"}

func restartWorkload(ctx context.Context, kubeconfig, key string) (string, error) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 || workloadResource[parts[0]] == "" || parts[1] == "" || parts[2] == "" {
		return "", fmt.Errorf("k8s: only deployments, statefulsets and daemonsets can be restarted, not %q", key)
	}
	c, err := newK8sClient(kubeconfig)
	if err != nil {
		return "", err
	}
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{"kubectl.kubernetes.io/restartedAt": time.Now().UTC().Format(time.RFC3339)}}}}})
	path := fmt.Sprintf("/apis/apps/v1/namespaces/%s/%s/%s", url.PathEscape(parts[1]), workloadResource[parts[0]], url.PathEscape(parts[2]))
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.base+path, bytes.NewReader(patch))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/strategic-merge-patch+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("k8s: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("k8s: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return "rolling restart of " + parts[0] + "/" + parts[1] + "/" + parts[2], nil
}

func rebootGuest(ctx context.Context, cfg ProxmoxConfig, key string) (string, error) {
	api, id, _ := strings.Cut(key, "/")
	vmid, err := strconv.Atoi(id)
	if (api != "qemu" && api != "lxc") || err != nil {
		return "", fmt.Errorf("proxmox: bad key %q", key)
	}
	if cfg.URL == "" || cfg.Token == "" {
		return "", errors.New("proxmox: url and token are required")
	}
	c := &pveClient{cfg: cfg, hc: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Insecure}}}} //nolint:gosec // opt-in for self-signed PVE certificates
	var res []pveResource
	if err := c.get(ctx, "/cluster/resources?type=vm", &res); err != nil {
		return "", fmt.Errorf("proxmox: %w", err)
	}
	node := ""
	for _, r := range res {
		if r.VMID == vmid && r.Type == api {
			node = r.Node
		}
	}
	if node == "" {
		return "", fmt.Errorf("proxmox: guest %s not found", key)
	}
	var task string
	if err := c.post(ctx, fmt.Sprintf("/nodes/%s/%s/%d/status/reboot", url.PathEscape(node), api, vmid), &task); err != nil {
		return "", fmt.Errorf("proxmox: %w", err)
	}
	return "reboot task " + task, nil
}

func (c *pveClient) post(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.URL, "/")+"/api2/json"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.cfg.Token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s: %s %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env); err != nil {
		return err
	}
	return json.Unmarshal(env.Data, v)
}
