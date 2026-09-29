// Command lanscape is the Lanscape server: web UI, API, agent gateway and scheduler.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/retreat-community/lanscape/internal/buildinfo"
	"github.com/retreat-community/lanscape/internal/cli"
	"github.com/retreat-community/lanscape/internal/server"
	"github.com/retreat-community/lanscape/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "lanscape:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: lanscape [command] [flags]

commands:
  serve            run the server (default)
  backup FILE      write a consistent copy of the database to FILE
  restore FILE     replace the database with FILE (server must be stopped)
  user-password U  set a new password for user U (reads LANSCAPE_NEW_PASSWORD)
  config export    print the declarative configuration (YAML) of a running panel
  config apply     apply a configuration file: -f FILE [--dry-run] [--prune]
  version          print the version

Flags can also be set with LANSCAPE_<FLAG> environment variables.
`)
}

type flags struct {
	listen, gatewayListen, miniListen, miniToken, dataDir, db, gatewayHosts, expect string
	tlsCert, tlsKey, metricsToken, adminUser, adminPassword, publicURL, logLevel    string
	logFormat, configFile, secretKey                                                string
	configPrune                                                                     bool
	oidcIssuer, oidcClientID, oidcClientSecret, oidcName, oidcRoleClaim             string
	oidcAdminGroups, oidcOperatorGroups, oidcDefaultRole                            string
	parallel                                                                        int
	secureCookies                                                                   bool
}

func parse(name string, args []string) (*flag.FlagSet, *flags, error) {
	f := &flags{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = usage
	fs.StringVar(&f.listen, "listen", ":8080", "UI and API address")
	fs.StringVar(&f.gatewayListen, "gateway-listen", ":8443", "agent gateway address (TLS, mTLS after registration)")
	fs.StringVar(&f.miniListen, "mini-listen", ":47701", "Lanscape Mini agent port; empty disables")
	fs.StringVar(&f.miniToken, "mini-token", "", "shared token for Mini agents (can be changed in settings)")
	fs.StringVar(&f.dataDir, "data-dir", "data", "data directory (database, pki)")
	fs.StringVar(&f.db, "db", "", "database: SQLite file (default data-dir/lanscape.db) or postgres:// URL")
	fs.StringVar(&f.gatewayHosts, "gateway-hosts", "", "comma separated names/IPs for the gateway certificate (default: hostname and local addresses)")
	fs.StringVar(&f.expect, "expect", "", "expected speed per network, e.g. 10.30.0.0/24=2500,10.31.0.0/24=1000")
	fs.StringVar(&f.tlsCert, "tls-cert", "", "TLS certificate for the UI (optional)")
	fs.StringVar(&f.tlsKey, "tls-key", "", "TLS key for the UI")
	fs.StringVar(&f.metricsToken, "metrics-token", "", "bearer token required for /metrics")
	fs.StringVar(&f.adminUser, "admin-user", "admin", "bootstrap administrator name")
	fs.StringVar(&f.adminPassword, "admin-password", "", "bootstrap administrator password (only when there are no users)")
	fs.StringVar(&f.publicURL, "public-url", "", "external URL of the panel")
	fs.StringVar(&f.logLevel, "log-level", "info", "debug, info, warn or error")
	fs.StringVar(&f.logFormat, "log-format", "text", "text or json")
	fs.IntVar(&f.parallel, "parallel", 8, "parallel reachability tests")
	fs.BoolVar(&f.secureCookies, "secure-cookies", false, "mark session cookies Secure (behind a TLS proxy)")
	fs.StringVar(&f.secretKey, "secret-key", "", "base64 32-byte key for monitor and channel secrets at rest (default: data-dir/secret.key)")
	fs.StringVar(&f.configFile, "config", "", "declarative configuration (YAML) applied on start; ${VAR} reads the environment")
	fs.BoolVar(&f.configPrune, "config-prune", false, "with -config: delete objects of the listed sections that the file does not contain")
	fs.StringVar(&f.oidcIssuer, "oidc-issuer", "", "OpenID Connect issuer URL (enables single sign-on)")
	fs.StringVar(&f.oidcClientID, "oidc-client-id", "", "OpenID Connect client id")
	fs.StringVar(&f.oidcClientSecret, "oidc-client-secret", "", "OpenID Connect client secret (empty for public clients with PKCE)")
	fs.StringVar(&f.oidcName, "oidc-name", "SSO", "label of the sign-in button")
	fs.StringVar(&f.oidcRoleClaim, "oidc-role-claim", "groups", "ID token claim with group names")
	fs.StringVar(&f.oidcAdminGroups, "oidc-admin-groups", "", "groups whose members become administrators")
	fs.StringVar(&f.oidcOperatorGroups, "oidc-operator-groups", "", "groups whose members become operators")
	fs.StringVar(&f.oidcDefaultRole, "oidc-default-role", "viewer", `role of other users: viewer, operator or "none" to refuse them`)
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}
	if err := cli.EnvDefaults(fs, "LANSCAPE"); err != nil {
		return nil, nil, err
	}
	return fs, f, nil
}

func (f *flags) config() (server.Config, error) {
	cfg := server.Config{Listen: f.listen, GatewayListen: f.gatewayListen, MiniListen: f.miniListen,
		MiniToken: f.miniToken, DataDir: f.dataDir, DB: f.db, TLSCert: f.tlsCert, TLSKey: f.tlsKey,
		MetricsToken: f.metricsToken, AdminUser: f.adminUser, AdminPassword: f.adminPassword, PublicURL: f.publicURL,
		Parallel: f.parallel, SecureCookies: f.secureCookies, SecretKey: f.secretKey, Version: buildinfo.Version, Expect: map[string]int{}}
	cfg.OIDC = server.OIDCConfig{Issuer: f.oidcIssuer, ClientID: f.oidcClientID, ClientSecret: f.oidcClientSecret,
		Name: f.oidcName, RoleClaim: f.oidcRoleClaim, AdminGroups: cli.SplitList(f.oidcAdminGroups),
		OperatorGroups: cli.SplitList(f.oidcOperatorGroups), DefaultRole: f.oidcDefaultRole}
	switch f.oidcDefaultRole {
	case "none", "":
		cfg.OIDC.DefaultRole = ""
	case server.RoleViewer, server.RoleOperator:
	default:
		return cfg, fmt.Errorf("invalid -oidc-default-role %q", f.oidcDefaultRole)
	}
	cfg.GatewayHosts = cli.SplitList(f.gatewayHosts)
	if len(cfg.GatewayHosts) == 0 {
		cfg.GatewayHosts = defaultHosts()
	}
	for _, e := range cli.SplitList(f.expect) {
		k, v, ok := strings.Cut(e, "=")
		n, err := strconv.Atoi(v)
		if !ok || err != nil || n <= 0 {
			return cfg, fmt.Errorf("invalid -expect entry %q", e)
		}
		cfg.Expect[k] = n
	}
	return cfg, nil
}

func defaultHosts() []string {
	hosts := []string{"localhost"}
	if h, err := os.Hostname(); err == nil {
		hosts = append(hosts, h)
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLinkLocalUnicast() {
			hosts = append(hosts, ipn.IP.String())
		}
	}
	return hosts
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version":
		fmt.Println("lanscape", buildinfo.Version)
		return nil
	case "help":
		usage()
		return nil
	case "config":
		return configCmd(args)
	case "serve", "backup", "restore", "user-password":
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
	fs, f, err := parse(cmd, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	cfg, err := f.config()
	if err != nil {
		return err
	}
	if cfg.DB == "" {
		cfg.DB = filepath.Join(cfg.DataDir, "lanscape.db")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := cli.Logger(f.logLevel, f.logFormat)
	switch cmd {
	case "backup":
		if fs.NArg() != 1 {
			return errors.New("usage: lanscape backup FILE")
		}
		st, err := store.Open(ctx, cfg.DB)
		if err != nil {
			return err
		}
		defer st.Close()
		return st.Backup(ctx, fs.Arg(0))
	case "restore":
		if fs.NArg() != 1 {
			return errors.New("usage: lanscape restore FILE")
		}
		return store.Restore(fs.Arg(0), cfg.DB)
	case "user-password":
		if fs.NArg() != 1 || os.Getenv("LANSCAPE_NEW_PASSWORD") == "" {
			return errors.New("usage: LANSCAPE_NEW_PASSWORD=... lanscape user-password USER")
		}
		st, err := store.Open(ctx, cfg.DB)
		if err != nil {
			return err
		}
		defer st.Close()
		u, err := st.UserByName(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		if u.PasswordHash, err = server.HashPassword(os.Getenv("LANSCAPE_NEW_PASSWORD")); err != nil {
			return err
		}
		u.TOTPSecret = ""
		return st.UpdateUser(ctx, u)
	}
	srv, err := server.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	registerModules(srv, log)
	if f.configFile != "" {
		if err := applyConfigFile(ctx, srv, f.configFile, f.configPrune, log); err != nil {
			return err
		}
	}
	err = srv.Run(ctx)
	time.Sleep(100 * time.Millisecond)
	return err
}
