// Package cli contains helpers shared by the lanscape and lanscape-agent commands.
package cli

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// EnvDefaults lets PREFIX_FLAG_NAME environment variables set flags that were not given
// on the command line (dashes become underscores).
func EnvDefaults(fs *flag.FlagSet, prefix string) error {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	var err error
	fs.VisitAll(func(f *flag.Flag) {
		if set[f.Name] || err != nil {
			return
		}
		key := prefix + "_" + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		if v, ok := os.LookupEnv(key); ok && v != "" {
			if e := fs.Set(f.Name, v); e != nil {
				err = fmt.Errorf("%s: %w", key, e)
			}
		}
	})
	return err
}

// Logger returns a structured logger at the given level (debug, info, warn, error).
func Logger(level, format string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: l}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

// SplitList splits a comma separated flag value.
func SplitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
