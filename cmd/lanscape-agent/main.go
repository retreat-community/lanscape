// Command lanscape-agent is the Lanscape agent: inventory, network tests, discovery and checks.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/retreat-community/lanscape/internal/agent"
	"github.com/retreat-community/lanscape/internal/buildinfo"
	"github.com/retreat-community/lanscape/internal/cli"
	"github.com/retreat-community/lanscape/internal/testengine"
)

const defaultExclude = "wan*,pppoe-*,tailscale*,docker*,veth*,cni*,flannel*,cilium_*,lxc*,virbr*,kube-ipvs*,vxlan*"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "lanscape-agent:", err)
		os.Exit(1)
	}
}

type options struct {
	server, token, name, dataDir, caFingerprint, exclude, dataListen, mode, logLevel, logFormat string
	maxDuration                                                                                 time.Duration
	maxStreams, maxUDPMbps                                                                      int
}

func parse(args []string) (*flag.FlagSet, *options, error) {
	o := &options{}
	fs := flag.NewFlagSet("lanscape-agent", flag.ContinueOnError)
	fs.StringVar(&o.server, "server", "", "server gateway address host:8443")
	fs.StringVar(&o.token, "token", "", "registration token (first start)")
	fs.StringVar(&o.name, "name", "", "agent name (default hostname)")
	fs.StringVar(&o.dataDir, "data-dir", defaultDataDir(), "state directory (certificates)")
	fs.StringVar(&o.caFingerprint, "ca-fingerprint", "", "SHA-256 fingerprint of the server CA to pin on registration")
	fs.StringVar(&o.exclude, "exclude", defaultExclude, "interfaces never used for tests (globs)")
	fs.StringVar(&o.dataListen, "data-listen", ":47700", "data-plane address for incoming tests")
	fs.StringVar(&o.mode, "mode", "", `"" (default), "checks-only" (no test responder) or "respond-only" (never initiate tests)`)
	fs.DurationVar(&o.maxDuration, "max-duration", 30*time.Second, "maximum test duration")
	fs.IntVar(&o.maxStreams, "max-streams", 16, "maximum parallel streams per test")
	fs.IntVar(&o.maxUDPMbps, "max-udp-mbps", 10000, "maximum UDP test rate")
	fs.StringVar(&o.logLevel, "log-level", "info", "debug, info, warn or error")
	fs.StringVar(&o.logFormat, "log-format", "text", "text or json")
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}
	if err := cli.EnvDefaults(fs, "LANSCAPE"); err != nil {
		return nil, nil, err
	}
	return fs, o, nil
}

func run(args []string) error {
	if len(args) > 0 && (args[0] == "version" || args[0] == "-version" || args[0] == "--version") {
		fmt.Println("lanscape-agent", buildinfo.Version)
		return nil
	}
	if len(args) > 0 && args[0] == "service" {
		return serviceCommand(args[1:])
	}
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}
	_, o, err := parse(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if o.server == "" {
		return errors.New("-server is required (LANSCAPE_SERVER)")
	}
	log := cli.Logger(o.logLevel, o.logFormat)
	cfg := agent.Config{Server: o.server, Token: o.token, Name: o.name, DataDir: o.dataDir, CAFingerprint: o.caFingerprint,
		Exclude: cli.SplitList(o.exclude), DataAddr: o.dataListen, Mode: o.mode, Version: buildinfo.Version,
		Limits: testengine.Limits{MaxDuration: o.maxDuration, MaxStreams: o.maxStreams, MaxUDPKbps: o.maxUDPMbps * 1000}}
	if !strings.Contains(cfg.Server, ":") {
		cfg.Server += ":8443"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := agent.New(cfg, log)
	registerModules(a, log)
	log.Info("lanscape-agent starting", "version", buildinfo.Version, "server", cfg.Server, "data_dir", cfg.DataDir)
	return runService(ctx, a)
}
