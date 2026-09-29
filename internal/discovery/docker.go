//go:build !lanscape_small

package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// dockerContainer holds the fields of GET /containers/json that Lanscape uses.
type dockerContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

// dockerClient talks to the Docker Engine API over a Unix socket (also Podman's).
func dockerClient(sock string) *http.Client {
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}}
}

// Docker lists containers (running and stopped).
func Docker(ctx context.Context, sock string) ([]Item, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json?all=1", nil)
	if err != nil {
		return nil, err
	}
	resp, err := dockerClient(sock).Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("docker: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var cs []dockerContainer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&cs); err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}
	return dockerItems(cs), nil
}

func dockerItems(cs []dockerContainer) []Item {
	out := make([]Item, 0, len(cs))
	for _, c := range cs {
		name := strings.TrimPrefix(firstOr(c.Names, c.ID), "/")
		id := c.ID
		if len(id) > 12 {
			id = id[:12]
		}
		it := Item{Key: "container/" + name, Kind: KindContainer, Name: name, Image: c.Image, State: c.State,
			Health: dockerHealth(c.Status), Owner: id, Labels: pickLabels(c.Labels),
			Project: c.Labels["com.docker.compose.project"]}
		seen := map[string]bool{}
		for _, p := range c.Ports {
			pp := Port{IP: p.IP, Port: p.PublicPort, Target: p.PrivatePort, Proto: p.Type}
			k := fmt.Sprintf("%d/%d/%s", pp.Port, pp.Target, pp.Proto)
			if seen[k] {
				continue // published on both 0.0.0.0 and ::
			}
			seen[k] = true
			if wildcard(pp.IP) {
				pp.IP = ""
			}
			it.Ports = append(it.Ports, pp)
		}
		sort.Slice(it.Ports, func(a, b int) bool { return it.Ports[a].Target < it.Ports[b].Target })
		for _, n := range c.NetworkSettings.Networks {
			if n.IPAddress != "" {
				it.IPs = append(it.IPs, n.IPAddress)
			}
		}
		sort.Strings(it.IPs)
		out = append(out, it)
	}
	sortItems(out)
	return out
}

func dockerHealth(status string) string {
	switch {
	case strings.Contains(status, "(healthy)"):
		return "healthy"
	case strings.Contains(status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(status, "(health: starting)"):
		return "starting"
	}
	return ""
}

func firstOr(s []string, def string) string {
	if len(s) > 0 {
		return s[0]
	}
	return def
}

// pickLabels keeps labels that are useful for grouping and presentation.
func pickLabels(l map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range l {
		if strings.HasPrefix(k, "com.docker.compose.") || strings.HasPrefix(k, "lanscape.") ||
			strings.HasPrefix(k, "org.opencontainers.image.title") ||
			(strings.HasPrefix(k, "traefik.http.routers.") && strings.HasSuffix(k, ".rule")) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
