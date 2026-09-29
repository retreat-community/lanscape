package main

import (
	"log/slog"

	"github.com/retreat-community/lanscape/internal/agent"
)

// registerModules wires optional agent modules (discovery sources, checks, actions).
func registerModules(_ *agent.Agent, _ *slog.Logger) {}
