package main

import (
	"log/slog"

	"github.com/retreat-community/lanscape/internal/server"
)

// registerModules wires optional modules (discovery, monitors, integrations).
func registerModules(_ *server.Server, _ *slog.Logger) {}
