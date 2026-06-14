// northplaned is the Northplane server binary (SPEC §7.1): server,
// satellite, MCP gateway, migrations, importer and backup in one
// artefact.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/northplane/northplane/internal/ai"
	"github.com/northplane/northplane/internal/api"
	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/bundle"
	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/demo"
	"github.com/northplane/northplane/internal/mcp"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/nagios"
	"github.com/northplane/northplane/internal/server"
	"github.com/northplane/northplane/internal/storage"
	"github.com/northplane/northplane/internal/tsdb"
)

var version = "1.0.0-dev"

func main() {
	if len(os.Args) < 2 {
		serve(nil)
		return
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "serve":
		serve(args)
	case "init":
		initCmd(args)
	case "migrate":
		migrateCmd(args)
	case "storage":
		storageCmd(args)
	case "import":
		importCmd(args)
	case "backup":
		backupCmd(args)
	case "mcp":
		mcpCmd(args)
	case "openapi":
		openapiCmd(args)
	case "bootstrap-admin":
		bootstrapAdminCmd(args)
	case "version", "--version", "-v":
		fmt.Println("northplaned", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`northplaned — Northplane monitoring server (` + version + `)

Usage:
  northplaned [serve]                      run the server (default)
  northplaned serve --demo                 …and seed a live demo environment
                    [--demo-snmp host:161] [--demo-traps udp://:9162]
  northplaned init [--dir /etc/northplane] write config.yaml, secret key, systemd unit
  northplaned migrate    [-config …]       apply pending schema migrations and exit
  northplaned storage migrate --to <dsn>   copy relational data between backends (offline)
  northplaned import nagios --path <dir>   convert a Nagios/Icinga config to a bundle
  northplaned backup     [-config …]       consistent backup to backup.target
  northplaned mcp        [-config …]       MCP server on stdio (NORTHPLANE_TOKEN auth)
  northplaned bootstrap-admin [-config …]  create the initial admin token
  northplaned openapi                      print the OpenAPI 3.1 spec to stdout (no server needed)
  northplaned version

Flags for most commands: -config /etc/northplane/config.yaml
`)
}

func loadConfig(fs *flag.FlagSet, args []string) config.Config {
	cfgPath := fs.String("config", config.DefaultConfigPath(), "config file")
	_ = fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal("config: %v", err)
	}
	return cfg
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "northplaned: "+format+"\n", args...)
	os.Exit(1)
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.LogFormat == "text" {
		handler = slog.NewTextHandler(os.Stderr, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger
}

// closeLogged closes c on shutdown and logs any error — these are deferred
// in main/command functions where there is nothing to do but record that a
// final flush/close did not complete cleanly.
func closeLogged(name string, c io.Closer, log *slog.Logger) {
	if err := c.Close(); err != nil {
		log.Error("close failed", "what", name, "err", err)
	}
}

func openStore(ctx context.Context, cfg config.Config, log *slog.Logger) *storage.Store {
	store, err := storage.Open(ctx, storage.Options{
		DSN: cfg.Storage.DSN, DataDir: cfg.DataDir, Log: log,
		RetentionMonths: cfg.Storage.EventRetentionMonths,
	})
	if err != nil {
		fatal("storage: %v", err)
	}
	return store
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	demoSeed := fs.Bool("demo", false, "seed an idempotent demo environment at startup")
	demoSNMP := fs.String("demo-snmp", "127.0.0.1:161", "SNMP get/walk target for the demo checks")
	demoTraps := fs.String("demo-traps", "udp://:9162", "trap-receiver listen address for the demo event source")
	cfg := loadConfig(fs, args)
	log := newLogger(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := openStore(ctx, cfg, log)
	defer closeLogged("store", store, log)
	ts, err := tsdb.Open(cfg.TSDBDir(), log, tsdb.Retention{})
	if err != nil {
		fatal("tsdb: %v", err)
	}
	defer closeLogged("tsdb", ts, log)

	// Demo seeding runs for the explicit --demo flag (dev/E2E) or when
	// NORTHPLANE_DEMO=true (config/env-driven deployments — the demo/real
	// switch). The env path is guarded so flipping the switch can never
	// seed the showcase environment on top of a real install.
	switch {
	case *demoSeed:
		seedDemo(ctx, store, log, *demoSNMP, *demoTraps)
	case cfg.Demo && hasRealData(ctx, store):
		log.Warn("NORTHPLANE_DEMO is set but this database already holds real (non-demo) hosts — skipping demo seeding to protect production data; use a dedicated data dir/volume for the demo, or unset NORTHPLANE_DEMO")
	case cfg.Demo:
		seedDemo(ctx, store, log, *demoSNMP, *demoTraps)
	}
	seedDefaultAdmin(ctx, store, log)

	srv, err := server.New(ctx, cfg, store, ts, log, version)
	if err != nil {
		fatal("server: %v", err)
	}
	if err := srv.Run(ctx); err != nil {
		fatal("serve: %v", err)
	}
}

// seedDemo provisions the demo environment (SPEC-Showcase): real checks
// against localhost/public targets, alerting chain, BPI tree, dashboard,
// report, demo users. Idempotent — safe on every start with --demo.
func seedDemo(ctx context.Context, store *storage.Store, log *slog.Logger, snmpTarget, trapListen string) {
	sum, err := demo.Seed(ctx, store, demo.Options{
		SNMPTarget: snmpTarget,
		TrapListen: trapListen,
		Log:        log,
		CreateUser: func(ctx context.Context, name, email, password string, roles []string) error {
			_, err := store.CreateUser(ctx, &model.User{
				Name: name, Email: email, Local: true,
				PassHash: auth.HashSecret(password), Roles: roles,
			})
			return err
		},
	})
	if err != nil {
		fatal("demo seed: %v", err)
	}
	for _, u := range sum.Users {
		log.Info("demo: user ready", "name", u.Name, "email", u.Email,
			"password", u.Password, "role", u.Role)
	}
	for _, h := range sum.Hints {
		log.Info("demo: hint", "msg", h)
	}
	log.Info("demo: environment seeded", "counts", fmt.Sprintf("%v", sum.Counts))
}

// hasRealData reports whether the default tenant already contains host
// objects the demo seeder did not create — i.e. real production data. It
// gates the NORTHPLANE_DEMO switch so flipping demo mode on can never seed
// the showcase environment on top of a real install. Conservative: any
// query error returns true (refuse to seed) so an unknown state never
// risks polluting real data.
func hasRealData(ctx context.Context, store *storage.Store) bool {
	hosts, err := store.ListObjects(ctx, storage.ObjectFilter{
		TenantID: model.DefaultTenant, Kind: model.KindHost, Limit: 5000,
	})
	if err != nil {
		return true
	}
	for _, h := range hosts {
		if h.Labels["demo"] != "true" {
			return true
		}
	}
	return false
}

// seedDefaultAdmin guarantees a break-glass local admin exists so a fresh
// install is reachable immediately, without the interactive /setup flow.
// It seeds only when no enabled local admin is present and the chosen email
// is free — so an admin you create later is never duplicated, and changing
// this account's password (or deleting it once another admin exists) sticks
// across restarts. There is NO hardcoded default password: when one is not
// supplied via NP_DEFAULT_ADMIN_PASSWORD a strong random password is
// generated and printed to the log exactly once (it is hashed, never
// recoverable), so a network-exposed fresh install is never reachable with
// known credentials. Opt out entirely with NP_DEFAULT_ADMIN_DISABLED (or an
// empty NP_DEFAULT_ADMIN_PASSWORD) — e.g. installs that provision the admin
// via /setup or OIDC.
func seedDefaultAdmin(ctx context.Context, store *storage.Store, log *slog.Logger) {
	if os.Getenv("NP_DEFAULT_ADMIN_DISABLED") != "" {
		return
	}
	email := envOr("NP_DEFAULT_ADMIN_EMAIL", "admin@localhost")
	name := envOr("NP_DEFAULT_ADMIN_NAME", "Administrator")
	password, custom := os.LookupEnv("NP_DEFAULT_ADMIN_PASSWORD")
	if custom && password == "" {
		return // explicit opt-out via empty password
	}
	generated := !custom
	if generated {
		// 32 hex chars (128 bits) of crypto-random entropy. Printed once
		// below; we keep only the argon2id hash.
		password = model.NewSecret(16)
	}

	users, err := store.ListUsers(ctx)
	if err != nil {
		log.Error("default admin: list users failed — skipping seed", "err", err)
		return
	}
	for _, u := range users {
		if u.Local && !u.Disabled && hasRole(u.Roles, "admin") {
			return // a real local admin already exists — nothing to do
		}
		if u.Email == email {
			return // address already taken — never clobber an existing account
		}
	}
	if _, err := store.CreateLocalUser(ctx, name, email, auth.HashSecret(password)); err != nil {
		log.Error("default admin: create failed", "email", email, "err", err)
		return
	}
	if generated {
		log.Warn("seeded default admin with a GENERATED password — save it now, it is not recoverable",
			"email", email, "password", password,
			"note", "set NP_DEFAULT_ADMIN_PASSWORD to choose your own, or NP_DEFAULT_ADMIN_DISABLED to skip seeding")
	} else {
		log.Warn("seeded default admin — CHANGE THE PASSWORD",
			"email", email,
			"override_env", "NP_DEFAULT_ADMIN_EMAIL / NP_DEFAULT_ADMIN_PASSWORD / NP_DEFAULT_ADMIN_NAME")
	}
}

// envOr returns the environment variable for key, or def when it is unset or
// empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// hasRole reports whether roles contains name.
func hasRole(roles []string, name string) bool {
	for _, r := range roles {
		if r == name {
			return true
		}
	}
	return false
}

func initCmd(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dir := fs.String("dir", config.DefaultConfigDir(), "config directory")
	dataDir := fs.String("data", config.DefaultDataDir(), "data directory")
	_ = fs.Parse(args)

	if err := os.MkdirAll(*dir, 0o750); err != nil {
		fatal("%v", err)
	}
	if err := os.MkdirAll(*dataDir, 0o750); err != nil {
		fatal("%v", err)
	}
	cfgPath := filepath.Join(*dir, "config.yaml")
	if _, err := os.Stat(cfgPath); err == nil {
		fatal("%s exists — refusing to overwrite", cfgPath)
	}
	keyPath := filepath.Join(*dir, "secret.key")
	if err := auth.GenerateMasterKey(keyPath); err != nil {
		fatal("secret key: %v", err)
	}
	example := config.Example(*dataDir, keyPath)
	if err := os.WriteFile(cfgPath, []byte(example), 0o640); err != nil {
		fatal("%v", err)
	}
	unit := `[Unit]
Description=Northplane monitoring server
After=network-online.target

[Service]
ExecStart=/usr/local/bin/northplaned serve -config ` + cfgPath + `
Restart=on-failure
WatchdogSec=60
User=northplane
StateDirectory=northplane
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=` + *dataDir + `

[Install]
WantedBy=multi-user.target
`
	unitPath := filepath.Join(*dir, "northplaned.service")
	_ = os.WriteFile(unitPath, []byte(unit), 0o644)
	fmt.Printf(`initialised:
  config:       %s
  secret key:   %s (0600 — back this up!)
  systemd unit: %s (copy to /etc/systemd/system/)

next steps:
  1. review %s (TLS, storage backend, OIDC)
  2. systemctl enable --now northplaned
  3. open /setup in the browser to create the admin account
     (or headless: northplaned bootstrap-admin -config %s)
`, cfgPath, keyPath, unitPath, cfgPath, cfgPath)
}

func migrateCmd(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	cfg := loadConfig(fs, args)
	log := newLogger(cfg)
	ctx := context.Background()
	store := openStore(ctx, cfg, log) // Open runs migrations with startup gate
	defer closeLogged("store", store, log)
	fmt.Println("migrations applied — schema is current")
}

func storageCmd(args []string) {
	if len(args) < 1 || args[0] != "migrate" {
		fatal("usage: northplaned storage migrate --to <dsn> [-config …]")
	}
	fs := flag.NewFlagSet("storage migrate", flag.ExitOnError)
	to := fs.String("to", "", "target DSN (postgres://… or empty=sqlite path)")
	cfgPath := fs.String("config", config.DefaultConfigPath(), "config file")
	_ = fs.Parse(args[1:])
	if *to == "" {
		fatal("--to required")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal("config: %v", err)
	}
	log := newLogger(cfg)
	ctx := context.Background()

	src := openStore(ctx, cfg, log)
	defer closeLogged("source store", src, log)
	dst, err := storage.Open(ctx, storage.Options{DSN: *to, DataDir: cfg.DataDir + "-migrated",
		Log: log, RetentionMonths: cfg.Storage.EventRetentionMonths})
	if err != nil {
		fatal("target: %v", err)
	}
	defer closeLogged("target store", dst, log)
	n, err := storage.CopyAll(ctx, src, dst)
	if err != nil {
		fatal("copy: %v", err)
	}
	fmt.Printf("copied %d rows. Point storage.dsn at the target and restart (NP-TSDB unaffected).\n", n)
}

func importCmd(args []string) {
	if len(args) < 1 || args[0] != "nagios" {
		fatal("usage: northplaned import nagios --path /etc/nagios [--out bundle.yaml]")
	}
	fs := flag.NewFlagSet("import nagios", flag.ExitOnError)
	path := fs.String("path", "", "nagios/icinga config path")
	out := fs.String("out", "northplane-import.yaml", "output bundle")
	_ = fs.Parse(args[1:])
	if *path == "" {
		fatal("--path required")
	}
	res, err := nagios.Import(*path)
	if err != nil {
		fatal("import: %v", err)
	}
	rendered, err := bundle.Render(res.Docs)
	if err != nil {
		fatal("render: %v", err)
	}
	if err := os.WriteFile(*out, rendered, 0o644); err != nil {
		fatal("write: %v", err)
	}
	fmt.Println(res.RenderReport())
	fmt.Printf("bundle written to %s — review, then: np apply -f %s\n", *out, *out)
}

func backupCmd(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	cfg := loadConfig(fs, args)
	log := newLogger(cfg)
	if cfg.Backup.Target == "" {
		fatal("backup.target not configured")
	}
	ctx := context.Background()
	store := openStore(ctx, cfg, log)
	defer closeLogged("store", store, log)
	manifest, err := server.Backup(ctx, cfg, store, version)
	if err != nil {
		fatal("backup: %v", err)
	}
	fmt.Printf("backup complete: %s\n", manifest)
}

func mcpCmd(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	cfg := loadConfig(fs, args)
	cfg.LogFormat = "text" // stdio transport owns stdout; logs to stderr
	log := newLogger(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := openStore(ctx, cfg, log)
	defer closeLogged("store", store, log)
	ts, err := tsdb.Open(cfg.TSDBDir(), log, tsdb.Retention{})
	if err != nil {
		fatal("tsdb: %v", err)
	}
	defer closeLogged("tsdb", ts, log)
	cat := catalog.New(store)
	if err := cat.LoadAll(ctx); err != nil {
		fatal("catalog: %v", err)
	}

	// Authentication: NORTHPLANE_TOKEN must be a valid API token —
	// the MCP session inherits exactly its scopes (SPEC §10.3).
	token := os.Getenv("NORTHPLANE_TOKEN")
	if token == "" {
		fatal("set NORTHPLANE_TOKEN to a Northplane API token (np_…)")
	}
	authn := &auth.Authenticator{Store: store}
	principal, err := auth.AuthenticateToken(ctx, authn, token)
	if err != nil {
		fatal("token: %v", err)
	}
	svc := ai.New(ai.Deps{Cfg: cfg.AI, Store: store, Catalog: cat,
		TSDB: ts, BaseURL: cfg.BaseURL, Log: log})
	log.Info("mcp: serving on stdio", "actor", principal.Name)
	if err := mcp.RunStdio(ctx, svc, principal, version); err != nil && ctx.Err() == nil {
		fatal("mcp: %v", err)
	}
}

// openapiCmd prints the OpenAPI 3.1 document to stdout without starting
// the server or touching any storage. It reuses the exact generator the
// server serves at /api/openapi.json (api.OpenAPIDocument), so the typed
// codegen pipeline (`make types`) can never drift from the live spec.
func openapiCmd(args []string) {
	_ = args // no flags; spec is generated purely from the route registry
	doc := api.OpenAPIDocument(version)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		fatal("openapi: %v", err)
	}
}

// bootstrapAdminCmd mints the initial admin API token for headless installs.
// Creating any token also closes the web first-run /setup gate (by design —
// whoever ran this already has admin access).
func bootstrapAdminCmd(args []string) {
	fs := flag.NewFlagSet("bootstrap-admin", flag.ExitOnError)
	cfg := loadConfig(fs, args)
	log := newLogger(cfg)
	ctx := context.Background()
	store := openStore(ctx, cfg, log)
	defer closeLogged("store", store, log)

	existing, err := store.ListAPITokens(ctx, model.DefaultTenant)
	if err != nil {
		fatal("%v", err)
	}
	for _, t := range existing {
		if t.Name == "bootstrap-admin" {
			fatal("bootstrap-admin token already exists — revoke it first via API")
		}
	}
	clear, tok := auth.MintToken(model.DefaultTenant, "bootstrap-admin",
		[]model.Permission{"*:*"}, &model.APIToken{CreatedBy: "northplaned init"})
	if err := store.CreateAPIToken(ctx, tok); err != nil {
		fatal("%v", err)
	}
	fmt.Printf(`admin token (shown once, store it safely):

  %s

use it:
  export NP_TOKEN=%s
  np --server https://localhost:8443 get hosts
`, clear, clear)
}
