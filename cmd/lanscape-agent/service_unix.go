//go:build !windows

package main

import (
	"context"
	"errors"
	"runtime"

	"github.com/retreat-community/lanscape/internal/agent"
)

func defaultDataDir() string {
	switch runtime.GOOS {
	case "darwin":
		return "/usr/local/var/lanscape-agent"
	case "freebsd":
		return "/var/db/lanscape-agent"
	default:
		return "/var/lib/lanscape-agent"
	}
}

func runService(ctx context.Context, a *agent.Agent) error { return a.Run(ctx) }

func serviceCommand(args []string) error {
	return installUnixService(args)
}

var errNoService = errors.New("service management is not available on this system; use the package's service file")

// restartExitCode ends the process after an update; systemd, procd and launchd restart it.
const restartExitCode = 0
