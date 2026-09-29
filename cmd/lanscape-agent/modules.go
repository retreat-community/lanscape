package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"runtime"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/cli"
	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/monitor"
	"github.com/retreat-community/lanscape/internal/proto"
)

// registerModules wires optional agent modules: service discovery and availability checks.
func registerModules(ctx context.Context, a *agent.Agent, o *options, log *slog.Logger) {
	if o.mode != "respond-only" {
		a.Handle(proto.MsgCheck, func(ctx context.Context, env proto.Envelope) (any, error) {
			var s monitor.Spec
			if err := json.Unmarshal(env.Data, &s); err != nil {
				return nil, err
			}
			if err := s.Validate(); err != nil {
				return nil, err
			}
			return monitor.Run(ctx, s), nil
		})
	}
	if o.mode != "respond-only" {
		// explicit, rate-limited port scans of subnets named by an administrator
		a.Handle(proto.MsgScan, func(ctx context.Context, env proto.Envelope) (any, error) {
			var req discovery.ScanRequest
			if err := json.Unmarshal(env.Data, &req); err != nil {
				return nil, err
			}
			items, err := discovery.Scan(ctx, req)
			if err != nil && len(items) == 0 {
				return nil, err
			}
			return discovery.SourceReport{Source: discovery.SourceScan, Items: items}, nil
		})
	}
	cfg := discovery.Config{DockerSocket: o.dockerSocket, Kubeconfig: o.kubeconfig, Probe: !o.noProbe,
		Interval: o.discoverInterval, Signatures: cli.SplitList(o.signatures),
		Proxmox: discovery.ProxmoxConfig{URL: o.proxmoxURL, Token: o.proxmoxToken, Insecure: o.proxmoxInsecure},
		Proxy:   discovery.ProxyConfig{TraefikURL: o.traefikURL, CaddyAdmin: o.caddyAdmin, NginxDir: o.nginxDir},
		DNS: discovery.DNSConfig{PiholeURL: o.piholeURL, PiholePassword: o.piholePassword, AdGuardURL: o.adguardURL,
			AdGuardUser: o.adguardUser, AdGuardPassword: o.adguardPassword, TechnitiumURL: o.technitiumURL,
			TechnitiumToken: o.technitiumToken}}
	if cfg.Proxy.NginxDir == "auto" {
		cfg.Proxy.NginxDir = ""
		if exists("/etc/nginx/nginx.conf") {
			cfg.Proxy.NginxDir = "/etc/nginx"
		}
	}
	for _, src := range cli.SplitList(o.discover) {
		switch src {
		case "auto":
			cfg.Sockets = runtime.GOOS == "linux"
			cfg.Docker = exists(orDefault(o.dockerSocket, "/var/run/docker.sock"))
			cfg.K8s = os.Getenv("KUBERNETES_SERVICE_HOST") != "" || o.kubeconfig != ""
			// multicast discovery belongs to hosts on the LAN, not to pods
			cfg.MDNS = os.Getenv("KUBERNETES_SERVICE_HOST") == ""
			cfg.SSDP = cfg.MDNS
			cfg.OpenWrt = exists("/etc/openwrt_release")
		case discovery.SourceSockets:
			cfg.Sockets = true
		case discovery.SourceDocker:
			cfg.Docker = true
		case discovery.SourceK8s:
			cfg.K8s = true
		case discovery.SourceMDNS:
			cfg.MDNS = true
		case discovery.SourceOpenWrt:
			cfg.OpenWrt = true
		case discovery.SourceSSDP:
			cfg.SSDP = true
		case "none", "off":
		default:
			log.Warn("unknown discovery source", "source", src)
		}
	}
	col, err := discovery.New(cfg, log)
	if err != nil {
		log.Error("discovery disabled", "err", err)
		return
	}
	if !col.Enabled() {
		return
	}
	log.Info("discovery enabled", "sockets", cfg.Sockets, "docker", cfg.Docker, "k8s", cfg.K8s, "mdns", cfg.MDNS, "ssdp", cfg.SSDP, "openwrt", cfg.OpenWrt, "proxmox", cfg.Proxmox.URL != "",
		"interval", col.Interval())
	// the server can ask for a fresh report (the "rescan" button)
	a.Handle(proto.MsgDiscovery, func(ctx context.Context, _ proto.Envelope) (any, error) {
		return col.Collect(ctx), nil
	})
	go col.Run(ctx, func(r discovery.Report) error { return a.Send(ctx, proto.MsgDiscovery, r) })
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
