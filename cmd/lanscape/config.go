package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/retreat-community/lanscape/internal/cli"
	"github.com/retreat-community/lanscape/internal/server"
)

// configCmd exports or applies the declarative configuration through the API:
//
//	lanscape config export [-o lanscape.yaml]
//	lanscape config apply -f lanscape.yaml [--dry-run] [--prune]
func configCmd(args []string) error {
	if len(args) == 0 || (args[0] != "export" && args[0] != "apply") {
		return errors.New("usage: lanscape config export|apply [flags] (see lanscape config apply -h)")
	}
	sub := args[0]
	fs := flag.NewFlagSet("config "+sub, flag.ContinueOnError)
	base := fs.String("url", "http://localhost:8080", "panel URL")
	token := fs.String("api-token", "", "API token of an administrator (Settings → API tokens)")
	file := fs.String("f", "", "configuration file (apply; - for stdin)")
	out := fs.String("o", "", "output file (export; default stdout)")
	dry := fs.Bool("dry-run", false, "show the plan without changing anything")
	prune := fs.Bool("prune", false, "delete objects of the listed sections that the file does not contain")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := cli.EnvDefaults(fs, "LANSCAPE"); err != nil {
		return err
	}
	if *token == "" {
		return errors.New("an API token is required (--api-token or LANSCAPE_API_TOKEN)")
	}
	hc := &http.Client{Timeout: 2 * time.Minute}
	endpoint := strings.TrimRight(*base, "/") + "/api/v1/config"
	if sub == "export" {
		req, _ := http.NewRequest(http.MethodGet, endpoint, nil)
		req.Header.Set("Authorization", "Bearer "+*token)
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("export: %s: %s", resp.Status, strings.TrimSpace(string(b)))
		}
		if *out == "" {
			_, err = os.Stdout.Write(b)
			return err
		}
		return os.WriteFile(*out, b, 0o600)
	}
	if *file == "" {
		return errors.New("apply needs -f FILE")
	}
	var b []byte
	var err error
	if *file == "-" {
		b, err = io.ReadAll(os.Stdin)
	} else {
		b, err = os.ReadFile(*file)
	}
	if err != nil {
		return err
	}
	if b, err = server.ExpandVars(b, os.LookupEnv); err != nil {
		return err
	}
	q := url.Values{}
	if *dry {
		q.Set("dry_run", "true")
	}
	if *prune {
		q.Set("prune", "true")
	}
	req, _ := http.NewRequest(http.MethodPost, endpoint+"?"+q.Encode(), bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+*token)
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var res struct {
		Error string                `json:"error"`
		Plan  []server.ConfigChange `json:"plan"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&res); err != nil {
		return fmt.Errorf("apply: %s", resp.Status)
	}
	printPlan(res.Plan, *dry)
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}

func printPlan(plan []server.ConfigChange, dry bool) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	changed := 0
	for _, c := range plan {
		if c.Action != "unchanged" {
			changed++
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", c.Action, c.Kind, c.Name)
		}
	}
	_ = w.Flush()
	verb := "applied"
	if dry {
		verb = "planned"
	}
	fmt.Printf("%d changes %s, %d unchanged\n", changed, verb, len(plan)-changed)
}

// applyConfigFile applies --config before the server starts serving.
func applyConfigFile(ctx context.Context, srv *server.Server, path string, prune bool, log *slog.Logger) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if b, err = server.ExpandVars(b, os.LookupEnv); err != nil {
		return err
	}
	cf, err := server.ParseConfig(b)
	if err != nil {
		return err
	}
	plan, err := srv.ApplyConfig(ctx, cf, false, prune)
	for _, c := range plan {
		if c.Action != "unchanged" {
			log.Info("config", "action", c.Action, "kind", c.Kind, "name", c.Name)
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	log.Info("configuration applied", "file", path, "objects", len(plan))
	return nil
}
