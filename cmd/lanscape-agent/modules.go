package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/buildinfo"
	"github.com/retreat-community/lanscape/internal/cli"
	"github.com/retreat-community/lanscape/internal/discovery"
	"github.com/retreat-community/lanscape/internal/fingerprint"
	"github.com/retreat-community/lanscape/internal/monitor"
	"github.com/retreat-community/lanscape/internal/proto"
	"github.com/retreat-community/lanscape/internal/testengine"
	"github.com/retreat-community/lanscape/internal/wol"
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
		// public address and download speed through each default gateway (§6.1, on request)
		a.Handle(proto.MsgInternet, func(ctx context.Context, env proto.Envelope) (any, error) {
			var m proto.InternetMsg
			if err := json.Unmarshal(env.Data, &m); err != nil {
				return nil, err
			}
			routes, _, _, _, _ := agent.CollectSystem()
			return testengine.Internet(ctx, agent.DefaultGateways(routes),
				testengine.InternetParams{IPURL: m.IPURL, DownloadURL: m.DownloadURL}), nil
		})
		// throughput to devices without an agent that run "iperf3 -s", within the agent's limits
		a.Handle(proto.MsgIperf3, func(ctx context.Context, env proto.Envelope) (any, error) {
			var m proto.Iperf3Msg
			if err := json.Unmarshal(env.Data, &m); err != nil {
				return nil, err
			}
			d := min(time.Duration(max(m.Seconds, 1))*time.Second, o.maxDuration)
			return testengine.Iperf3(ctx, testengine.Iperf3Options{Host: m.Host, Port: m.Port, Duration: d,
				Streams: min(max(m.Streams, 1), o.maxStreams), Reverse: m.Reverse})
		})
	}
	allow, err := discovery.ParsePrefixes(cli.SplitList(o.scanAllow))
	if err != nil {
		log.Error("active scanning disabled", "err", err)
	}
	if o.mode != "respond-only" && len(allow) > 0 {
		// explicit, rate-limited port scans, only inside the subnets allowed on this agent
		a.Handle(proto.MsgScan, func(ctx context.Context, env proto.Envelope) (any, error) {
			var req discovery.ScanRequest
			if err := json.Unmarshal(env.Data, &req); err != nil {
				return nil, err
			}
			if err := discovery.ScanAllowed(allow, req.CIDRs); err != nil {
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
			TechnitiumToken: o.technitiumToken, AXFR: cli.SplitList(o.axfr)}}
	cfg.Host = hostConfig(o)
	cfg.OpenWrtParts = discovery.ParseOpenWrtParts(cli.SplitList(o.openwrtParts))
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
			cfg.NetBIOS = cfg.MDNS
			cfg.OpenWrt = exists("/etc/openwrt_release")
			cfg.Libvirt = discovery.LibvirtAvailable()
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
		case discovery.SourceLibvirt:
			cfg.Libvirt = true
		case discovery.SourceNetBIOS:
			cfg.NetBIOS = true
		case "none", "off":
		default:
			log.Warn("unknown discovery source", "source", src)
		}
	}
	if o.mode != "respond-only" {
		registerActions(a, cfg, cli.SplitList(o.actions), log)
	}
	cfg.Neighbors = func() []string {
		_, _, neigh, _, _ := agent.CollectSystem()
		ips := make([]string, 0, len(neigh))
		for _, n := range neigh {
			if n.MAC != "" && !strings.EqualFold(n.State, "failed") && !strings.EqualFold(n.State, "incomplete") {
				ips = append(ips, n.IP)
			}
		}
		return ips
	}
	col, err := discovery.New(cfg, log)
	if err != nil {
		log.Error("discovery disabled", "err", err)
		return
	}
	if !col.Enabled() {
		return
	}
	log.Info("discovery enabled", "sockets", cfg.Sockets, "docker", cfg.Docker, "k8s", cfg.K8s, "mdns", cfg.MDNS, "ssdp", cfg.SSDP, "openwrt", cfg.OpenWrt, "proxmox", cfg.Proxmox.URL != "", "libvirt", cfg.Libvirt,
		"interval", col.Interval())
	// the server can ask for a fresh report (the "rescan" button)
	a.Handle(proto.MsgDiscovery, func(ctx context.Context, _ proto.Envelope) (any, error) {
		return col.Collect(ctx), nil
	})
	// signatures distributed by the server (Settings → Signatures)
	a.Handle(proto.MsgConfig, func(_ context.Context, env proto.Envelope) (any, error) {
		var m proto.ConfigMsg
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return nil, err
		}
		if len(m.Signatures) == 0 {
			return map[string]int{"signatures": 0}, nil
		}
		sigs, err := fingerprint.Parse(m.Signatures)
		if err != nil {
			return nil, err
		}
		col.SetSignatures(sigs)
		log.Info("signatures from the server", "count", len(sigs))
		return map[string]int{"signatures": len(sigs)}, nil
	})
	go col.Run(ctx, func(r discovery.Report) error { return a.Send(ctx, proto.MsgDiscovery, r) })
}

// registerActions enables the actions the administrator allowed on this agent: Wake-on-LAN
// (default) and restarts of containers, workloads and guests the agent discovers.
func registerActions(a *agent.Agent, cfg discovery.Config, allowed []string, log *slog.Logger) {
	allow := map[string]bool{}
	for _, x := range allowed {
		switch x {
		case proto.ActionWake, proto.ActionRestart:
			allow[x] = true
			a.AddCaps("action:" + x)
		case proto.MsgUpdate:
			registerUpdate(a, log)
		case "none", "off":
		default:
			log.Warn("unknown action", "action", x)
		}
	}
	if len(allow) == 0 {
		return
	}
	a.Handle(proto.MsgAction, func(ctx context.Context, env proto.Envelope) (any, error) {
		var m proto.ActionMsg
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return nil, err
		}
		if !allow[m.Action] {
			return nil, fmt.Errorf("action %q is not allowed on this agent (see --actions)", m.Action)
		}
		log.Info("action", "action", m.Action, "mac", m.MAC, "source", m.Source, "key", m.Key)
		switch m.Action {
		case proto.ActionWake:
			var targets []wol.Target
			if m.IP != "" {
				targets, _ = wol.Targets(m.IP)
			}
			ifs, err := wol.Send(ctx, m.MAC, targets)
			if err != nil {
				return nil, err
			}
			return proto.ActionResultMsg{Detail: "magic packet sent on " + strings.Join(ifs, ", ")}, nil
		default:
			detail, err := discovery.Restart(ctx, cfg, m.Source, m.Key)
			if err != nil {
				return nil, err
			}
			return proto.ActionResultMsg{Detail: detail}, nil
		}
	})
}

// registerUpdate lets the panel replace the agent binary with a release (§13.1); the service
// manager (systemd, procd, launchd, Windows recovery) starts the new binary after the exit.
func registerUpdate(a *agent.Agent, log *slog.Logger) {
	a.AddCaps(proto.MsgUpdate)
	a.Handle(proto.MsgUpdate, func(ctx context.Context, env proto.Envelope) (any, error) {
		var m proto.UpdateMsg
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return nil, err
		}
		log.Info("update requested", "from", buildinfo.Version, "to", m.Version)
		res, err := agent.SelfUpdate(ctx, &http.Client{Timeout: 5 * time.Minute}, buildinfo.Version, m)
		if err != nil {
			log.Warn("update failed", "err", err)
			return nil, err
		}
		go func() {
			time.Sleep(2 * time.Second) // let the reply reach the server
			log.Info("restarting into the new version", "version", m.Version)
			os.Exit(restartExitCode)
		}()
		return res, nil
	})
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

func hostConfig(o *options) discovery.HostConfig {
	var c discovery.HostConfig
	switch o.nut {
	case "auto":
		if conn, err := net.DialTimeout("tcp", "127.0.0.1:3493", 300*time.Millisecond); err == nil {
			conn.Close()
			c.NUT = "127.0.0.1:3493"
		}
	case "", "off":
	default:
		c.NUT = o.nut
	}
	_, errSmart := exec.LookPath("smartctl")
	c.SMART = o.smart == "on" || (o.smart == "auto" && errSmart == nil && os.Geteuid() == 0)
	_, errZfs := exec.LookPath("zpool")
	c.ZFS = o.zfs == "on" || (o.zfs == "auto" && errZfs == nil)
	_, errIPMI := exec.LookPath("ipmitool")
	c.IPMI = o.ipmi == "on" || (o.ipmi == "auto" && errIPMI == nil && os.Geteuid() == 0)
	c.Plugs = discovery.ParsePlugs(o.plugs)
	return c
}
