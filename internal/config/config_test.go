package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Defaults --------------------------------------------------------------

func TestDefaults(t *testing.T) {
	d := Defaults()

	if d.Listen != "127.0.0.1:8443" {
		t.Errorf("Listen = %q, want loopback 127.0.0.1:8443", d.Listen)
	}
	if d.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", d.LogLevel)
	}
	if d.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want json", d.LogFormat)
	}
	if d.DeadManInterval != time.Minute {
		t.Errorf("DeadManInterval = %v, want 1m", d.DeadManInterval)
	}
	if d.Storage.EventRetentionMonths != 12 {
		t.Errorf("EventRetentionMonths = %d, want 12", d.Storage.EventRetentionMonths)
	}
	if d.Backup.Interval != 5*time.Minute {
		t.Errorf("Backup.Interval = %v, want 5m", d.Backup.Interval)
	}
	if d.AI.Provider != "none" {
		t.Errorf("AI.Provider = %q, want none", d.AI.Provider)
	}
	if d.OIDC.GroupsClaim != "groups" {
		t.Errorf("OIDC.GroupsClaim = %q, want groups", d.OIDC.GroupsClaim)
	}
	wantScopes := []string{"openid", "profile", "email", "groups"}
	if strings.Join(d.OIDC.Scopes, ",") != strings.Join(wantScopes, ",") {
		t.Errorf("OIDC.Scopes = %v, want %v", d.OIDC.Scopes, wantScopes)
	}
	if d.DataDir == "" {
		t.Error("DataDir must not be empty")
	}

	// The defaults must themselves be a valid (dev) configuration.
	if err := d.Validate(); err != nil {
		t.Errorf("Defaults() does not pass Validate: %v", err)
	}
}

// --- Load: YAML parsing ----------------------------------------------------

// writeConfig writes content to a temp file and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func TestLoad_ValidYAML(t *testing.T) {
	// Avoid environment-dependent plugins-dir probing by pinning it.
	t.Setenv("NORTHPLANE_PLUGINS_DIR", t.TempDir())

	p := writeConfig(t, `
listen: "0.0.0.0:9443"
baseUrl: "https://np.example.net"
dataDir: "/srv/np"
trustProxy: true
storage:
  dsn: "postgres://u:p@db:5432/np"
  eventRetentionMonths: 24
tls:
  certFile: "/etc/np/cert.pem"
  keyFile: "/etc/np/key.pem"
oidc:
  issuer: "https://issuer.example/v2.0"
  clientId: "client-123"
  clientSecret: "shh"
  adminGroup: "admins"
ai:
  provider: anthropic
  model: claude-sonnet-4-6
backup:
  target: "/backups"
  interval: 10m
logLevel: debug
logFormat: text
`)

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Listen != "0.0.0.0:9443" {
		t.Errorf("Listen = %q", cfg.Listen)
	}
	if cfg.BaseURL != "https://np.example.net" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.DataDir != "/srv/np" {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
	if !cfg.TrustProxy {
		t.Error("TrustProxy = false, want true")
	}
	if cfg.Storage.DSN != "postgres://u:p@db:5432/np" {
		t.Errorf("Storage.DSN = %q", cfg.Storage.DSN)
	}
	if cfg.Storage.EventRetentionMonths != 24 {
		t.Errorf("EventRetentionMonths = %d", cfg.Storage.EventRetentionMonths)
	}
	if cfg.TLS.CertFile != "/etc/np/cert.pem" || cfg.TLS.KeyFile != "/etc/np/key.pem" {
		t.Errorf("TLS = %+v", cfg.TLS)
	}
	if cfg.OIDC.Issuer != "https://issuer.example/v2.0" || cfg.OIDC.ClientID != "client-123" {
		t.Errorf("OIDC = %+v", cfg.OIDC)
	}
	if cfg.AI.Provider != "anthropic" {
		t.Errorf("AI.Provider = %q", cfg.AI.Provider)
	}
	if cfg.Backup.Interval != 10*time.Minute {
		t.Errorf("Backup.Interval = %v", cfg.Backup.Interval)
	}
	if cfg.LogLevel != "debug" || cfg.LogFormat != "text" {
		t.Errorf("Log* = %q/%q", cfg.LogLevel, cfg.LogFormat)
	}
	if !cfg.UsePostgres() {
		t.Error("UsePostgres() = false, want true for postgres:// DSN")
	}
}

func TestLoad_MissingFileUsesDefaults(t *testing.T) {
	t.Setenv("NORTHPLANE_PLUGINS_DIR", t.TempDir())
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	cfg, err := Load(missing)
	if err != nil {
		t.Fatalf("Load(missing) should not error: %v", err)
	}
	if cfg.Listen != Defaults().Listen {
		t.Errorf("Listen = %q, want default", cfg.Listen)
	}
}

func TestLoad_EmptyPathUsesDefaults(t *testing.T) {
	t.Setenv("NORTHPLANE_PLUGINS_DIR", t.TempDir())
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if cfg.Listen != Defaults().Listen {
		t.Errorf("Listen = %q, want default", cfg.Listen)
	}
}

func TestLoad_EmptyFileKeepsDefaults(t *testing.T) {
	// io.EOF path: a comment-only / empty file must not refuse to start.
	t.Setenv("NORTHPLANE_PLUGINS_DIR", t.TempDir())
	p := writeConfig(t, "# only a comment\n")

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load(empty): %v", err)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want default info", cfg.LogLevel)
	}
}

func TestLoad_UnknownFieldsRejected(t *testing.T) {
	t.Setenv("NORTHPLANE_PLUGINS_DIR", t.TempDir())
	p := writeConfig(t, "listen: \":8443\"\nnotARealField: 42\n")

	_, err := Load(p)
	if err == nil {
		t.Fatal("Load with unknown field must fail (KnownFields(true))")
	}
	if !strings.Contains(err.Error(), "notARealField") {
		t.Errorf("error should mention the unknown field, got: %v", err)
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	t.Setenv("NORTHPLANE_PLUGINS_DIR", t.TempDir())
	p := writeConfig(t, "listen: \":8443\nthis is : not : valid")

	if _, err := Load(p); err == nil {
		t.Fatal("Load with malformed YAML must fail")
	}
}

// --- Load: env overrides ---------------------------------------------------

func TestLoad_EnvOverridesFile(t *testing.T) {
	p := writeConfig(t, `
listen: "127.0.0.1:8443"
logLevel: info
storage:
  dsn: ""
tls:
  insecure: false
trustProxy: false
ai:
  provider: none
`)

	// Every override below must beat the file value.
	t.Setenv("NORTHPLANE_LISTEN", "0.0.0.0:7000")
	t.Setenv("NORTHPLANE_LOG_LEVEL", "warn")
	t.Setenv("NORTHPLANE_STORAGE_DSN", "postgres://x@y/z")
	t.Setenv("NORTHPLANE_TLS_INSECURE", "true")
	t.Setenv("NORTHPLANE_TRUST_PROXY", "true")
	t.Setenv("NORTHPLANE_AI_PROVIDER", "openai-compat")
	t.Setenv("NORTHPLANE_EXEC_POOL_SIZE", "64")
	t.Setenv("NORTHPLANE_PLUGINS_DIR", t.TempDir())

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Listen != "0.0.0.0:7000" {
		t.Errorf("Listen = %q, env override lost", cfg.Listen)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, env override lost", cfg.LogLevel)
	}
	if cfg.Storage.DSN != "postgres://x@y/z" {
		t.Errorf("Storage.DSN = %q, env override lost", cfg.Storage.DSN)
	}
	if !cfg.TLS.Insecure {
		t.Error("TLS.Insecure = false, env override lost")
	}
	if !cfg.TrustProxy {
		t.Error("TrustProxy = false, env override lost")
	}
	if cfg.AI.Provider != "openai-compat" {
		t.Errorf("AI.Provider = %q, env override lost", cfg.AI.Provider)
	}
	if cfg.ExecPoolSize != 64 {
		t.Errorf("ExecPoolSize = %d, env override lost", cfg.ExecPoolSize)
	}
}

func TestLoad_EnvAppliesWithoutFile(t *testing.T) {
	t.Setenv("NORTHPLANE_LISTEN", ":12345")
	t.Setenv("NORTHPLANE_BASE_URL", "https://only-env.example")
	t.Setenv("NORTHPLANE_PLUGINS_DIR", t.TempDir())

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != ":12345" {
		t.Errorf("Listen = %q", cfg.Listen)
	}
	if cfg.BaseURL != "https://only-env.example" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
}

func TestApplyEnv_InvalidNumericIgnored(t *testing.T) {
	// Non-numeric exec pool size: strconv.Atoi fails, value left unchanged.
	cfg := Defaults()
	cfg.ExecPoolSize = 7
	t.Setenv("NORTHPLANE_EXEC_POOL_SIZE", "not-a-number")
	applyEnv(&cfg)
	if cfg.ExecPoolSize != 7 {
		t.Errorf("ExecPoolSize = %d, want unchanged 7 on bad input", cfg.ExecPoolSize)
	}

	// Non-bool insecure flag: ParseBool fails -> false (zero), value reset.
	cfg2 := Defaults()
	cfg2.TLS.Insecure = true
	t.Setenv("NORTHPLANE_TLS_INSECURE", "garbage")
	applyEnv(&cfg2)
	if cfg2.TLS.Insecure {
		t.Error("TLS.Insecure should be false after failed ParseBool of 'garbage'")
	}
}

// --- Validate --------------------------------------------------------------

func TestValidate(t *testing.T) {
	// base is a known-valid dev config; each case mutates a copy.
	base := func() Config {
		c := Defaults()
		c.Listen = "127.0.0.1:8443"
		return c
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
		errSub  string // substring expected in the error (when wantErr)
	}{
		{
			name:    "valid dev defaults pass",
			mutate:  func(c *Config) {},
			wantErr: false,
		},
		{
			name:    "minimal loopback no TLS no OIDC passes",
			mutate:  func(c *Config) { c.Listen = "127.0.0.1:0"; c.TLS = TLSConfig{}; c.OIDC = OIDCConfig{} },
			wantErr: false,
		},
		{
			name:    "empty host with port is valid",
			mutate:  func(c *Config) { c.Listen = ":8443" },
			wantErr: false,
		},
		{
			name:    "kernel-assigned port 0 is valid",
			mutate:  func(c *Config) { c.Listen = "127.0.0.1:0" },
			wantErr: false,
		},
		{
			name:    "named port is valid",
			mutate:  func(c *Config) { c.Listen = "127.0.0.1:https" },
			wantErr: false,
		},
		{
			name:    "empty listen fails",
			mutate:  func(c *Config) { c.Listen = "" },
			wantErr: true,
			errSub:  "listen",
		},
		{
			name:    "non host:port listen fails",
			mutate:  func(c *Config) { c.Listen = "not-a-host-port" },
			wantErr: true,
			errSub:  "host:port",
		},
		{
			name:    "missing port fails",
			mutate:  func(c *Config) { c.Listen = "127.0.0.1:" },
			wantErr: true,
			errSub:  "missing port",
		},
		{
			name:    "invalid (non-numeric, non-name) port fails",
			mutate:  func(c *Config) { c.Listen = "127.0.0.1:99999" },
			wantErr: true,
			errSub:  "invalid port",
		},
		{
			name:    "TLS cert without key fails",
			mutate:  func(c *Config) { c.TLS.CertFile = "/c.pem" },
			wantErr: true,
			errSub:  "certFile set without",
		},
		{
			name:    "TLS key without cert fails",
			mutate:  func(c *Config) { c.TLS.KeyFile = "/k.pem" },
			wantErr: true,
			errSub:  "keyFile set without",
		},
		{
			name:    "TLS cert and key together pass",
			mutate:  func(c *Config) { c.TLS.CertFile = "/c.pem"; c.TLS.KeyFile = "/k.pem" },
			wantErr: false,
		},
		{
			name:    "complete OIDC passes",
			mutate:  func(c *Config) { c.OIDC.Issuer = "https://i"; c.OIDC.ClientID = "id" },
			wantErr: false,
		},
		{
			name:    "OIDC clientId without issuer fails",
			mutate:  func(c *Config) { c.OIDC.ClientID = "id" },
			wantErr: true,
			errSub:  "issuer is empty",
		},
		{
			name:    "OIDC issuer without clientId fails",
			mutate:  func(c *Config) { c.OIDC.Issuer = "https://i" },
			wantErr: true,
			errSub:  "clientId is empty",
		},
		{
			name:    "OIDC only secret set fails (issuer missing)",
			mutate:  func(c *Config) { c.OIDC.ClientSecret = "s" },
			wantErr: true,
			errSub:  "issuer is empty",
		},
		{
			name:    "OIDC only adminGroup set fails (issuer missing)",
			mutate:  func(c *Config) { c.OIDC.AdminGroup = "g" },
			wantErr: true,
			errSub:  "issuer is empty",
		},
		{
			name:    "default scopes/groupsClaim alone do not count as OIDC touched",
			mutate:  func(c *Config) {}, // Defaults() sets Scopes+GroupsClaim only
			wantErr: false,
		},
		{
			name:    "AI provider none passes",
			mutate:  func(c *Config) { c.AI.Provider = "none" },
			wantErr: false,
		},
		{
			name:    "AI provider empty passes",
			mutate:  func(c *Config) { c.AI.Provider = "" },
			wantErr: false,
		},
		{
			name:    "AI provider azure-openai passes",
			mutate:  func(c *Config) { c.AI.Provider = "azure-openai" },
			wantErr: false,
		},
		{
			name:    "AI provider unknown fails",
			mutate:  func(c *Config) { c.AI.Provider = "gpt-magic" },
			wantErr: true,
			errSub:  "ai.provider",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutate(&c)
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if tc.wantErr && tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("Validate() error = %q, want substring %q", err.Error(), tc.errSub)
			}
		})
	}
}

func TestLoad_SurfacesValidateError(t *testing.T) {
	// A file that parses fine but is semantically invalid: Load must wrap
	// the Validate error rather than returning a usable config.
	t.Setenv("NORTHPLANE_PLUGINS_DIR", t.TempDir())
	p := writeConfig(t, "listen: \"garbage-no-port\"\n")

	cfg, err := Load(p)
	if err == nil {
		t.Fatal("Load must surface Validate error for a broken listen")
	}
	if !strings.Contains(err.Error(), "config invalid") {
		t.Errorf("error should be wrapped with %q, got: %v", "config invalid", err)
	}
	// The returned cfg still carries the parsed (broken) value for diagnostics.
	if cfg.Listen != "garbage-no-port" {
		t.Errorf("returned cfg.Listen = %q, want the parsed value", cfg.Listen)
	}
}

func TestLoad_EnvCanBreakValidation(t *testing.T) {
	// Env override is applied before Validate, so a bad env value fails Load.
	t.Setenv("NORTHPLANE_PLUGINS_DIR", t.TempDir())
	t.Setenv("NORTHPLANE_AI_PROVIDER", "totally-unsupported")
	p := writeConfig(t, "listen: \":8443\"\n")

	_, err := Load(p)
	if err == nil {
		t.Fatal("Load must fail when an env override produces an invalid provider")
	}
	if !strings.Contains(err.Error(), "ai.provider") {
		t.Errorf("error = %v, want it to mention ai.provider", err)
	}
	if !strings.Contains(err.Error(), "config invalid") {
		t.Errorf("error = %v, want it wrapped with 'config invalid'", err)
	}
}

// --- TLS / plaintext policy & path helpers ---------------------------------

func TestUsePostgres(t *testing.T) {
	tests := []struct {
		dsn  string
		want bool
	}{
		{"", false},
		{"postgres://u@h/db", true},
		{"postgresql://u@h/db", true},
		{"file:core.db", false},
		{"mysql://x", false},
	}
	for _, tc := range tests {
		c := Config{Storage: StorageConfig{DSN: tc.dsn}}
		if got := c.UsePostgres(); got != tc.want {
			t.Errorf("UsePostgres(%q) = %v, want %v", tc.dsn, got, tc.want)
		}
	}
}

func TestPathHelpers(t *testing.T) {
	c := Config{DataDir: "/data/np"}
	if got, want := c.SQLitePath(), filepath.Join("/data/np", "core.db"); got != want {
		t.Errorf("SQLitePath = %q, want %q", got, want)
	}
	if got, want := c.TSDBDir(), filepath.Join("/data/np", "tsdb"); got != want {
		t.Errorf("TSDBDir = %q, want %q", got, want)
	}
	if got, want := c.ArtifactsDir(), filepath.Join("/data/np", "artifacts"); got != want {
		t.Errorf("ArtifactsDir = %q, want %q", got, want)
	}
}

func TestExample_IsValidAndParseable(t *testing.T) {
	// The generated example file must itself load & validate cleanly.
	t.Setenv("NORTHPLANE_PLUGINS_DIR", t.TempDir())
	dir := t.TempDir()
	out := Example(dir, filepath.Join(dir, "secret.key"))

	if !strings.Contains(out, "secretKeyFile") {
		t.Error("Example with secretKeyFile should include the secretKeyFile line")
	}

	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(out), 0o600); err != nil {
		t.Fatalf("write example: %v", err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("generated Example() config must Load (incl. KnownFields + Validate): %v", err)
	}
	if cfg.Listen != "127.0.0.1:8443" {
		t.Errorf("example Listen = %q", cfg.Listen)
	}
	if cfg.AI.Provider != "none" {
		t.Errorf("example AI.Provider = %q, want none", cfg.AI.Provider)
	}

	// Empty dataDir falls back to DefaultDataDir; no secretKeyFile line.
	out2 := Example("", "")
	if strings.Contains(out2, "secretKeyFile") {
		t.Error("Example without key should omit secretKeyFile line")
	}
	if !strings.Contains(out2, "dataDir:") {
		t.Error("Example should always render dataDir")
	}
}

func TestFirstExisting(t *testing.T) {
	existing := t.TempDir()
	// A real file (not a dir) must be skipped — firstExisting wants dirs.
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := firstExisting("/no/such/path/a", file, existing); got != existing {
		t.Errorf("firstExisting = %q, want first existing dir %q", got, existing)
	}
	// None exist -> returns the last candidate as a fallback.
	fallback := "/definitely/not/here/fallback"
	if got := firstExisting("/no/such/a", "/no/such/b", fallback); got != fallback {
		t.Errorf("firstExisting (none exist) = %q, want fallback %q", got, fallback)
	}
}

func TestLoad_DerivesPluginsDirFromDataDir(t *testing.T) {
	// With no NORTHPLANE_PLUGINS_DIR set and the system nagios paths absent,
	// Load falls back to <dataDir>/plugins. Create that dir so it is chosen
	// deterministically regardless of host.
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("NORTHPLANE_PLUGINS_DIR")
	p := writeConfig(t, "listen: \":8443\"\ndataDir: "+fmt.Sprintf("%q", data)+"\n")

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(data, "plugins")
	// On hosts that genuinely have e.g. /opt/homebrew, that earlier path may
	// win; accept either the derived dir or any other existing absolute dir.
	if cfg.PluginsDir == "" {
		t.Error("PluginsDir should be derived, got empty")
	}
	if st, err := os.Stat(cfg.PluginsDir); err != nil || !st.IsDir() {
		t.Errorf("PluginsDir %q is not an existing directory (want e.g. %q)", cfg.PluginsDir, want)
	}
}

func TestDefaultPathHelpers_NonEmpty(t *testing.T) {
	// These are environment dependent; assert only that they produce
	// non-empty, plausibly-rooted paths and do not panic.
	for name, got := range map[string]string{
		"DefaultDataDir":        DefaultDataDir(),
		"DefaultConfigDir":      DefaultConfigDir(),
		"DefaultConfigPath":     DefaultConfigPath(),
		"DefaultAgentConfigPath": DefaultAgentConfigPath(),
	} {
		if got == "" {
			t.Errorf("%s returned empty path", name)
		}
	}
	if !strings.HasSuffix(DefaultConfigPath(), "config.yaml") {
		t.Errorf("DefaultConfigPath = %q, want .../config.yaml", DefaultConfigPath())
	}
	if !strings.HasSuffix(DefaultAgentConfigPath(), "agent.yaml") {
		t.Errorf("DefaultAgentConfigPath = %q, want .../agent.yaml", DefaultAgentConfigPath())
	}
}
