//go:build !lanscape_small

package discovery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// k8sClient is a minimal read-only Kubernetes REST client (see docs/decisions/0008).
type k8sClient struct {
	base  string
	token string
	hc    *http.Client
}

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

func newK8sClient(kubeconfig string) (*k8sClient, error) {
	if kubeconfig == "" {
		host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
		if host == "" {
			return nil, errors.New("k8s: not running in a cluster and no kubeconfig given")
		}
		tok, err := os.ReadFile(saDir + "/token")
		if err != nil {
			return nil, fmt.Errorf("k8s: service account token: %w", err)
		}
		ca, err := os.ReadFile(saDir + "/ca.crt")
		if err != nil {
			return nil, fmt.Errorf("k8s: service account CA: %w", err)
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(ca)
		return &k8sClient{base: "https://" + net.JoinHostPort(host, port), token: strings.TrimSpace(string(tok)),
			hc: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}}, nil
	}
	return kubeconfigClient(kubeconfig)
}

var errNotFound = errors.New("not found")

func (c *k8sClient) list(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(v)
}

type meta struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
}

type k8sList[T any] struct {
	Items []T `json:"items"`
}

type k8sService struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		Type      string            `json:"type"`
		ClusterIP string            `json:"clusterIP"`
		Selector  map[string]string `json:"selector"`
		Ports     []struct {
			Port       int             `json:"port"`
			TargetPort json.RawMessage `json:"targetPort"`
			NodePort   int             `json:"nodePort"`
			Protocol   string          `json:"protocol"`
		} `json:"ports"`
	} `json:"spec"`
	Status struct {
		LoadBalancer struct {
			Ingress []struct {
				IP       string `json:"ip"`
				Hostname string `json:"hostname"`
			} `json:"ingress"`
		} `json:"loadBalancer"`
	} `json:"status"`
}

type k8sIngress struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		TLS []struct {
			Hosts []string `json:"hosts"`
		} `json:"tls"`
		Rules []struct {
			Host string `json:"host"`
			HTTP struct {
				Paths []struct {
					Backend struct {
						Service struct {
							Name string `json:"name"`
							Port struct {
								Number int    `json:"number"`
								Name   string `json:"name"`
							} `json:"port"`
						} `json:"service"`
					} `json:"backend"`
				} `json:"paths"`
			} `json:"http"`
		} `json:"rules"`
	} `json:"spec"`
}

type k8sRoute struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		Hostnames []string `json:"hostnames"`
		Rules     []struct {
			BackendRefs []struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
				Port      int    `json:"port"`
			} `json:"backendRefs"`
		} `json:"rules"`
	} `json:"spec"`
}

type podTemplate struct {
	Metadata struct {
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Containers []struct {
			Image string `json:"image"`
		} `json:"containers"`
	} `json:"spec"`
}

type k8sWorkload struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		Replicas *int `json:"replicas"`
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
		Template podTemplate `json:"template"`
	} `json:"spec"`
	Status struct {
		ReadyReplicas          int `json:"readyReplicas"`
		Replicas               int `json:"replicas"`
		DesiredNumberScheduled int `json:"desiredNumberScheduled"`
		NumberReady            int `json:"numberReady"`
	} `json:"status"`
}

type k8sPod struct {
	Metadata meta `json:"metadata"`
	Status   struct {
		Phase             string `json:"phase"`
		ContainerStatuses []struct {
			RestartCount int `json:"restartCount"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

// podStats adds restarts and Pending pods to the workloads whose selector matches (§9.1).
func podStats(items []Item, ws []k8sWorkload, kind string, pods []k8sPod) {
	idx := map[string]int{}
	for i, it := range items {
		idx[it.Key] = i
	}
	for _, w := range ws {
		i, ok := idx[kindPrefix[kind]+w.Metadata.Namespace+"/"+w.Metadata.Name]
		sel := w.Spec.Selector.MatchLabels
		if !ok || len(sel) == 0 {
			continue
		}
		restarts, pending := 0, 0
		for _, p := range pods {
			if p.Metadata.Namespace != w.Metadata.Namespace || !matches(sel, p.Metadata.Labels) {
				continue
			}
			for _, c := range p.Status.ContainerStatuses {
				restarts += c.RestartCount
			}
			if p.Status.Phase == "Pending" {
				pending++
			}
		}
		if items[i].Labels == nil {
			items[i].Labels = map[string]string{}
		}
		items[i].Labels["restarts"] = strconv.Itoa(restarts)
		if pending > 0 {
			items[i].Labels["pending"] = strconv.Itoa(pending)
		}
	}
}

func matches(sel, labels map[string]string) bool {
	for k, v := range sel {
		if labels[k] != v {
			return false
		}
	}
	return true
}

type k8sPVC struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		StorageClassName string `json:"storageClassName"`
		Resources        struct {
			Requests map[string]string `json:"requests"`
		} `json:"resources"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

// Kubernetes lists services, ingresses, HTTP routes, workloads and PVCs.
func Kubernetes(ctx context.Context, kubeconfig string) ([]Item, error) {
	c, err := newK8sClient(kubeconfig)
	if err != nil {
		return nil, err
	}
	var items []Item
	var errs []error

	var svcs k8sList[k8sService]
	if err := c.list(ctx, "/api/v1/services", &svcs); err != nil {
		return nil, fmt.Errorf("k8s: %w", err)
	}
	items = append(items, serviceItems(svcs.Items)...)

	var ings k8sList[k8sIngress]
	if err := c.list(ctx, "/apis/networking.k8s.io/v1/ingresses", &ings); err != nil && !errors.Is(err, errNotFound) {
		errs = append(errs, err)
	}
	items = append(items, ingressItems(ings.Items)...)

	var routes k8sList[k8sRoute]
	if err := c.list(ctx, "/apis/gateway.networking.k8s.io/v1/httproutes", &routes); err != nil && !errors.Is(err, errNotFound) {
		errs = append(errs, err)
	}
	items = append(items, routeItems(routes.Items)...)

	var pods k8sList[k8sPod]
	if err := c.list(ctx, "/api/v1/pods", &pods); err != nil && !errors.Is(err, errNotFound) {
		errs = append(errs, err)
	}
	for _, w := range []struct{ path, kind string }{
		{"/apis/apps/v1/deployments", KindDeployment},
		{"/apis/apps/v1/statefulsets", KindStatefulSet},
		{"/apis/apps/v1/daemonsets", KindDaemonSet},
	} {
		var ws k8sList[k8sWorkload]
		if err := c.list(ctx, w.path, &ws); err != nil {
			errs = append(errs, err)
			continue
		}
		wi := workloadItems(ws.Items, w.kind)
		podStats(wi, ws.Items, w.kind, pods.Items)
		items = append(items, wi...)
	}

	var pvcs k8sList[k8sPVC]
	if err := c.list(ctx, "/api/v1/persistentvolumeclaims", &pvcs); err != nil {
		errs = append(errs, err)
	}
	for _, p := range pvcs.Items {
		items = append(items, Item{Key: "pvc/" + p.Metadata.Namespace + "/" + p.Metadata.Name, Kind: KindPVC,
			Name: p.Metadata.Name, Namespace: p.Metadata.Namespace, State: strings.ToLower(p.Status.Phase),
			Labels: map[string]string{"storage_class": p.Spec.StorageClassName, "size": p.Spec.Resources.Requests["storage"]}})
	}
	sortItems(items)
	return items, errors.Join(errs...)
}

func serviceItems(svcs []k8sService) []Item {
	var out []Item
	for _, s := range svcs {
		if s.Metadata.Namespace == "default" && s.Metadata.Name == "kubernetes" {
			continue
		}
		it := Item{Key: "svc/" + s.Metadata.Namespace + "/" + s.Metadata.Name, Kind: KindService, Name: s.Metadata.Name,
			Namespace: s.Metadata.Namespace, Selector: s.Spec.Selector, State: strings.ToLower(s.Spec.Type)}
		if s.Spec.ClusterIP != "" {
			it.IPs = append(it.IPs, s.Spec.ClusterIP)
		}
		for _, lb := range s.Status.LoadBalancer.Ingress {
			if lb.IP != "" {
				it.IPs = append(it.IPs, lb.IP)
			}
			if lb.Hostname != "" {
				it.Hosts = append(it.Hosts, lb.Hostname)
			}
		}
		for _, p := range s.Spec.Ports {
			pp := Port{Port: p.Port, Proto: strings.ToLower(p.Protocol), NodePort: p.NodePort}
			if pp.Proto == "" {
				pp.Proto = "tcp"
			}
			if n, err := strconv.Atoi(strings.Trim(string(p.TargetPort), `"`)); err == nil {
				pp.Target = n
			}
			it.Ports = append(it.Ports, pp)
		}
		out = append(out, it)
	}
	return out
}

func ingressItems(ings []k8sIngress) []Item {
	var out []Item
	for _, in := range ings {
		it := Item{Key: "ingress/" + in.Metadata.Namespace + "/" + in.Metadata.Name, Kind: KindIngress,
			Name: in.Metadata.Name, Namespace: in.Metadata.Namespace}
		tlsHosts := map[string]bool{}
		for _, t := range in.Spec.TLS {
			for _, h := range t.Hosts {
				tlsHosts[h] = true
			}
		}
		seen := map[string]bool{}
		for _, r := range in.Spec.Rules {
			if r.Host != "" && !seen["h"+r.Host] {
				seen["h"+r.Host] = true
				it.Hosts = append(it.Hosts, r.Host)
				if tlsHosts[r.Host] {
					it.TLS = true
				}
			}
			for _, p := range r.HTTP.Paths {
				b := p.Backend.Service
				if b.Name == "" {
					continue
				}
				ref := in.Metadata.Namespace + "/" + b.Name
				if b.Port.Number > 0 {
					ref += ":" + strconv.Itoa(b.Port.Number)
				}
				if !seen[ref] {
					seen[ref] = true
					it.Backends = append(it.Backends, ref)
				}
			}
		}
		if len(in.Spec.TLS) > 0 && len(tlsHosts) == 0 {
			it.TLS = true
		}
		out = append(out, it)
	}
	return out
}

func routeItems(routes []k8sRoute) []Item {
	var out []Item
	for _, r := range routes {
		it := Item{Key: "route/" + r.Metadata.Namespace + "/" + r.Metadata.Name, Kind: KindHTTPRoute,
			Name: r.Metadata.Name, Namespace: r.Metadata.Namespace, Hosts: r.Spec.Hostnames}
		for _, rule := range r.Spec.Rules {
			for _, b := range rule.BackendRefs {
				ns := b.Namespace
				if ns == "" {
					ns = r.Metadata.Namespace
				}
				ref := ns + "/" + b.Name
				if b.Port > 0 {
					ref += ":" + strconv.Itoa(b.Port)
				}
				it.Backends = append(it.Backends, ref)
			}
		}
		out = append(out, it)
	}
	return out
}

var kindPrefix = map[string]string{KindDeployment: "deploy/", KindStatefulSet: "sts/", KindDaemonSet: "ds/"}

func workloadItems(ws []k8sWorkload, kind string) []Item {
	var out []Item
	for _, w := range ws {
		it := Item{Key: kindPrefix[kind] + w.Metadata.Namespace + "/" + w.Metadata.Name, Kind: kind,
			Name: w.Metadata.Name, Namespace: w.Metadata.Namespace, PodLabels: w.Spec.Template.Metadata.Labels}
		if len(w.Spec.Template.Spec.Containers) > 0 {
			it.Image = w.Spec.Template.Spec.Containers[0].Image
		}
		want, ready := w.Status.Replicas, w.Status.ReadyReplicas
		if w.Spec.Replicas != nil {
			want = *w.Spec.Replicas
		}
		if kind == KindDaemonSet {
			want, ready = w.Status.DesiredNumberScheduled, w.Status.NumberReady
		}
		it.Ready = fmt.Sprintf("%d/%d", ready, want)
		switch {
		case want == 0:
			it.State = "scaled_down"
		case ready >= want:
			it.State = "ready"
		case ready == 0:
			it.State = "unavailable"
		default:
			it.State = "degraded"
		}
		out = append(out, it)
	}
	return out
}
