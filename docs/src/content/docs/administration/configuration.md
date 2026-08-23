---
title: Configuration reference
description: Every config.yaml key, environment variable, default, validation rule and hard-coded constant of the northplaned server.
sidebar:
  order: 1
---

`northplaned` reads one small YAML file plus `NORTHPLANE_*` environment variables. The file is deliberately minimal: it holds only what must exist **before the API is reachable** (listen address, storage, TLS, identity providers, the secret-store key). Everything else — hosts, checks, channels, rules, users, tokens, secrets, dashboards — is managed through the API, the UI or [config bundles](/docs/administration/config-bundles/) and never lives in `config.yaml`.

This page is the complete reference. For task-oriented guides see [TLS and reverse proxies](/docs/administration/tls-and-proxy/), [Storage](/docs/administration/storage/), [Authentication](/docs/administration/authentication/) and [Secrets](/docs/administration/secrets/).

## Load order and precedence

On every start (and for every subcommand that opens the store) `northplaned` builds its configuration in this order:

1. **Built-in defaults** (listed per key below).
2. **The config file** named by `-config <path>` (default: see [File locations](#file-locations)). The file is decoded strictly: **unknown keys are a hard error** (`config <path>: … field <name> not found`). An empty or comment-only file keeps the defaults; a missing file is fine (environment and defaults only); any other read error aborts.
3. **Environment overrides** `NORTHPLANE_*` — applied after the file, so they always win.
4. **Plugin directory auto-detection** if `pluginsDir` is still empty: the first existing directory among `/usr/lib/nagios/plugins`, `/usr/lib64/nagios/plugins`, `/usr/local/libexec/nagios`, `/opt/homebrew/libexec`, `<dataDir>/plugins`; when none exists, `<dataDir>/plugins` is used anyway.
5. **Validation** — an incoherent configuration refuses to start with `northplaned: config: config invalid: <message>` (exit code 1). See [Validation errors](#validation-errors).

**Precedence, highest first: environment variable → config file → built-in default.**

Things to know about the environment layer:

- There are **no CLI flags for individual keys**. The only config-related flag is `-config <path>`; `serve --demo`, `--demo-snmp` and `--demo-traps` are behaviour flags, not config overrides.
- Variables are read with "is set" semantics: a variable that is set to an **empty string** overrides the file value with an empty value.
- Boolean variables (`NORTHPLANE_TLS_INSECURE`, `NORTHPLANE_TRUST_PROXY`, `NORTHPLANE_DEMO`, `NORTHPLANE_ALLOW_SIGNUP`) are parsed like Go's `strconv.ParseBool` (`1`, `t`, `true`, `0`, `f`, `false`, any case); an unparsable value silently becomes `false`.
- `NORTHPLANE_EXEC_POOL_SIZE` must be an integer; a non-numeric value is ignored.
- Only a subset of keys has an environment equivalent (see [Environment variables](#environment-variables)); the rest are file-only.
- There is **no reload on SIGHUP**. Any configuration change requires a restart of `northplaned`.

## File locations

| What | Running as root (euid 0) | Running as a normal user |
|---|---|---|
| Config directory (`northplaned init --dir` default) | `/etc/northplane` | `os.UserConfigDir()/northplane` — Linux `~/.config/northplane`, macOS `~/Library/Application Support/northplane` |
| Config file (`-config` default) | `/etc/northplane/config.yaml` | `/etc/northplane/config.yaml` **if that file exists**, otherwise `<config dir>/config.yaml` |
| Agent config (`np-agent`) | `/etc/northplane/agent.yaml` | same rule, `agent.yaml` |
| `secret.key` written by `init` | `/etc/northplane/secret.key` | `<config dir>/secret.key` |

`-config` accepts any path; the default shown above applies to every subcommand (`serve`, `migrate`, `backup`, `mcp`, `bootstrap-admin`, `storage migrate`). `northplaned init` does not take `-config` — it takes `--dir` and `--data` instead (see [northplaned CLI](/docs/reference/cli-northplaned/)).

## Data directory defaults

`dataDir` is resolved at start-up when not set in the file or via `NORTHPLANE_DATA_DIR`:

| Situation | Default `dataDir` |
|---|---|
| running as root | `/var/lib/northplane` |
| non-root, Linux (any non-macOS OS), `$XDG_DATA_HOME` set | `$XDG_DATA_HOME/northplane` |
| non-root, Linux (any non-macOS OS), `$XDG_DATA_HOME` unset | `~/.local/share/northplane` |
| non-root, macOS | `~/Library/Application Support/northplane` |
| everything above failed | `/var/lib/northplane` |
| Docker image | `/var/lib/northplane` (`ENV NORTHPLANE_DATA_DIR`, declared `VOLUME`) |

Paths derived from `dataDir` (not configurable individually): `<dataDir>/core.db` (SQLite core database), `<dataDir>/events-YYYYMM.db` (monthly event segments, SQLite mode), `<dataDir>/tsdb/` (NP-TSDB), `<dataDir>/artifacts/`, `<dataDir>/plugins/` (plugin fallback candidate) and `<dataDir>/secret.key` (fallback master key location). The full layout is documented in [Storage](/docs/administration/storage/#data-directory-layout).

## Complete key reference

Types: `string`, `bool`, `int`, `int64`, `[]string`, `duration` (a Go duration string such as `60s`, `15m`, `1h`). Keys are written in YAML nesting; `storage.dsn` means:

```yaml
storage:
  dsn: ""
```

### Top level

| Key | Type | Default | Env | Notes |
|---|---|---|---|---|
| `listen` | string | `127.0.0.1:8443` | `NORTHPLANE_LISTEN` | Bind address, `host:port`. Loopback on purpose: exposing the server requires an explicit listen **and** TLS decision ([TLS and reverse proxies](/docs/administration/tls-and-proxy/)). `:8443` (all interfaces), `[::1]:8443`, named ports (`:https`) and port `0` (kernel-assigned) are accepted. |
| `baseUrl` | string | `""` | `NORTHPLANE_BASE_URL` | External URL of the instance, without trailing slash. Used for OIDC redirect (`baseUrl + /auth/callback`), links in notifications and ack links, the Web Push VAPID subject, Twilio signature verification, the first-run `/setup` hint, and AI/MCP. Required for SSO and for correct links behind a proxy. |
| `dataDir` | string | platform default (see above) | `NORTHPLANE_DATA_DIR` | Root of all persistent state. |
| `trustProxy` | bool | `false` | `NORTHPLANE_TRUST_PROXY` | Honour `X-Forwarded-Proto` (first comma-separated value, case-insensitive `https`) from a TLS-terminating reverse proxy: sets `Secure` cookies and HSTS, and allows a plaintext listener on a non-loopback address. `X-Forwarded-For` is **not** used. Enable only when the proxy is trusted and strips inbound forwarded headers. |
| `deadManUrl` | string | `""` (disabled) | `NORTHPLANE_DEADMAN_URL` | Outgoing dead-man heartbeat: the server issues `GET` to this URL every `deadManInterval` (healthchecks.io-compatible). Skipped while the results queue is saturated, so a stalled pipeline stops the pings. See [Observability](/docs/administration/observability/#dead-man-switch). |
| `deadManInterval` | duration | `1m` | — | `<= 0` is treated as `1m` at runtime. |
| `pluginsDir` | string | auto-detected | `NORTHPLANE_PLUGINS_DIR` | Root directory of Nagios-compatible plugins for `exec:` check commands; relative plugin names are resolved under it. See [Plugins and Nagios](/docs/monitoring/plugins-and-nagios/). |
| `pluginsAllow` | []string | `nil` (no allowlist) | — | Optional allowlist of plugin **basenames** (`check_http`, not a path). When set, any `exec:` plugin whose basename is not listed is refused (permission error), including absolute paths. Paths containing `..` are always refused. |
| `execPoolSize` | int | `0` → `min(256, 32 × NumCPU)` | `NORTHPLANE_EXEC_POOL_SIZE` | Maximum concurrently running external plugin processes. Builtin checks use a separate pool of 1024. |
| `logLevel` | string | `info` | `NORTHPLANE_LOG_LEVEL` | `debug`, `info`, `warn`, `error`; anything else falls back to `info`. |
| `logFormat` | string | `json` | `NORTHPLANE_LOG_FORMAT` | `json` (slog JSON handler) or `text`; anything else means `json`. Logs always go to **stderr**. `northplaned mcp` forces `text`. |
| `secretKeyFile` | string | `""` | `NORTHPLANE_SECRET_KEY_FILE` | Path of the 32-byte master key (64 hex characters) for AES-256-GCM secrets at rest. Self-provisioning: generated if the file does not exist; if the path is unusable the server falls back to `<dataDir>/secret.key` with a loud warning. See [Secrets](/docs/administration/secrets/). |
| `demo` | bool | `false` | `NORTHPLANE_DEMO` | Seed the idempotent showcase environment at start-up (same data as `serve --demo`). Guarded: never seeds on top of a database that already holds real (non-demo) hosts. See [Demo mode](/docs/getting-started/demo-mode/). |
| `allowSignup` | bool | `false` | `NORTHPLANE_ALLOW_SIGNUP` | Expose the public `/register` page. Self-registered accounts always get the `viewer` role only. See [Authentication](/docs/administration/authentication/). |
| `storage` | section | | | see [storage](#storage) |
| `tls` | section | | | see [tls](#tls) |
| `oidc` | section | | | see [oidc](#oidc) |
| `ldap` | section | | | see [ldap](#ldap) |
| `ai` | section | | | see [ai](#ai) |
| `backup` | section | | | see [backup](#backup) |
| `federation` | section | | | see [federation](#federation) |

### storage

| Key | Type | Default | Env | Notes |
|---|---|---|---|---|
| `storage.dsn` | string | `""` | `NORTHPLANE_STORAGE_DSN` | Empty ⇒ embedded SQLite at `<dataDir>/core.db`. `postgres://…` or `postgresql://…` ⇒ PostgreSQL (pgx). Any other non-empty value is treated as a **SQLite file path**. |
| `storage.eventRetentionMonths` | int | `12` | — | Months of event segments (SQLite files) or partitions (PostgreSQL) to keep; `0` keeps everything. Enforced nightly by the janitor. |

Connection pools, pragmas and partitioning are described in [Storage](/docs/administration/storage/).

### tls

| Key | Type | Default | Env | Notes |
|---|---|---|---|---|
| `tls.certFile` | string | `""` | `NORTHPLANE_TLS_CERT_FILE` | PEM certificate chain. Must be set together with `tls.keyFile`. |
| `tls.keyFile` | string | `""` | `NORTHPLANE_TLS_KEY_FILE` | PEM private key. |
| `tls.insecure` | bool | `false` | `NORTHPLANE_TLS_INSECURE` | Allow plaintext HTTP on a non-loopback listener (development only). Without it, plaintext is only allowed on loopback or with `trustProxy`. |

There is **no ACME/autocert option**; for public certificates put Caddy (or another terminating proxy) in front — see [TLS and reverse proxies](/docs/administration/tls-and-proxy/). Certificates are loaded once at start (no hot reload).

### oidc

| Key | Type | Default | Env | Notes |
|---|---|---|---|---|
| `oidc.issuer` | string | `""` (SSO off) | `NORTHPLANE_OIDC_ISSUER` | OIDC discovery issuer URL. SSO is only constructed when set; a discovery failure at boot logs a warning and disables SSO (the server still starts). |
| `oidc.clientId` | string | `""` | `NORTHPLANE_OIDC_CLIENT_ID` | Required as soon as any `oidc.*` key is set. |
| `oidc.clientSecret` | string | `""` | `NORTHPLANE_OIDC_CLIENT_SECRET` | |
| `oidc.scopes` | []string | `[openid, profile, email, groups]` | — | Scopes requested; if explicitly emptied the code falls back to `openid profile email`. |
| `oidc.groupsClaim` | string | `groups` | — | ID-token claim read for the group → role mapping. |
| `oidc.adminGroup` | string | `""` | — | Group value whose members additionally receive the `admin` role. |

The flow (Authorization Code + PKCE), cookie names, role mapping and caveats are documented in [Authentication](/docs/administration/authentication/). `baseUrl` must be set: the redirect URL is `baseUrl + /auth/callback`.

### ldap

| Key | Type | Default | Env | Notes |
|---|---|---|---|---|
| `ldap.url` | string | `""` (LDAP off) | `NORTHPLANE_LDAP_URL` | `ldap://host:389` or `ldaps://host:636`; must start with one of those prefixes. |
| `ldap.startTls` | bool | `false` | — | Upgrade an `ldap://` connection with StartTLS before any bind. |
| `ldap.insecureSkipVerify` | bool | `false` | — | Skip TLS certificate verification (TLS 1.2 minimum is always enforced). |
| `ldap.bindDn` | string | `""` | `NORTHPLANE_LDAP_BIND_DN` | Service account for sync/search. If set, `bindPassword` is required. |
| `ldap.bindPassword` | string | `""` | `NORTHPLANE_LDAP_BIND_PASSWORD` | |
| `ldap.baseDn` | string | `""` | `NORTHPLANE_LDAP_BASE_DN` | Search base; required when the block is used. |
| `ldap.userFilter` | string | `(&(objectClass=person)(mail=*))` | — | User search filter. |
| `ldap.userAttr` | string | `mail` | — | Login / e-mail attribute (Active Directory: `userPrincipalName`). |
| `ldap.nameAttr` | string | `cn` | — | Display-name attribute; empty falls back to the e-mail. |
| `ldap.idAttr` | string | `""` (= DN) | — | Stable identifier attribute (`entryUUID`, `objectGUID`); binary values are hex-encoded. |
| `ldap.groupAttr` | string | `memberOf` | — | Membership attribute read from the user entry. |
| `ldap.groupFilter` | string | `""` | — | Optional member search; `{dn}` and `{user}` placeholders are substituted (escaped). |
| `ldap.groupBaseDn` | string | `""` (= `baseDn`) | — | Base for the `groupFilter` search. |
| `ldap.syncInterval` | duration | `15m` | — | Values `<= 0` also mean `15m`. |
| `ldap.defaultRoles` | []string | `[viewer]` | — | Roles given when no group maps to a role. |
| `ldap.adminGroup` | string | `""` | — | Group DN or CN (compared lower-cased) mapped to `admin`. |
| `ldap.disableMissing` | bool | `true` | — | Disable directory users that disappeared from the directory. Local accounts are never touched. |

Sync behaviour, login verification and the directory endpoints are in [Authentication](/docs/administration/authentication/).

### ai

| Key | Type | Default | Env | Notes |
|---|---|---|---|---|
| `ai.provider` | string | `none` | `NORTHPLANE_AI_PROVIDER` | One of `none` (or empty), `anthropic`, `azure-openai`, `openai-compat`. Anything else fails validation. |
| `ai.endpoint` | string | `""` | `NORTHPLANE_AI_ENDPOINT` | Provider endpoint; `anthropic` defaults to `https://api.anthropic.com`. |
| `ai.apiKeyEnv` | string | `""` | `NORTHPLANE_AI_API_KEY_ENV` | The **name** of the environment variable that holds the API key (the key itself never goes into the file). Read when the provider is constructed. |
| `ai.apiKey` | string | `""` | — | Static key — discouraged; intended for gateways with static keys. |
| `ai.model` | string | `claude-sonnet-4-6` | `NORTHPLANE_AI_MODEL` | Default model; `openai-compat` defaults to `gpt-4o`. |
| `ai.modelDeep` | string | `""` (= `model`) | — | Model for deeper analysis tasks. |
| `ai.maxMonthlyTokens` | int64 | `0` (unlimited) | — | Monthly token budget. |
| `ai.redaction.hostnames` | string | `""` | — | `""` or `pseudonymize`. |
| `ai.redaction.customPatterns` | []string | `nil` | — | Additional redaction patterns. |

Provider connections can also be created at runtime in **Admin → AI providers**; see [Agent chat](/docs/ai/agent-chat/).

### backup

| Key | Type | Default | Env | Notes |
|---|---|---|---|---|
| `backup.target` | string | `""` (disabled) | `NORTHPLANE_BACKUP_TARGET` | Directory that receives `northplane-<timestamp>/` snapshots written by `northplaned backup`. Only directories are implemented (the code comment mentions `s3://` as a future option). |
| `backup.interval` | duration | `5m` | — | **Parsed but unused.** There is no periodic backup loop in the server; backups run only when you call `northplaned backup`. Treat this key as reserved. |

See [Storage → Backup](/docs/administration/storage/#backup).

### federation

| Key | Type | Default | Env | Notes |
|---|---|---|---|---|
| `federation.mode` | string | `""` (standalone) | `NORTHPLANE_FEDERATION_MODE` | Only `""` or `edge`. There is no `main` value — a main instance is simply a standalone instance that holds Site documents and mints `sites:connect` tokens. |
| `federation.mainUrl` | string | `""` | `NORTHPLANE_FEDERATION_MAIN_URL` | URL of the main instance; must start with `http://` or `https://` in edge mode. |
| `federation.token` | string | `""` | `NORTHPLANE_FEDERATION_TOKEN` | API token (`np_…`) minted on the main instance with scope `sites:connect`. Required in edge mode. |
| `federation.site` | string | `""` | `NORTHPLANE_FEDERATION_SITE` | Name of the Site registered on the main instance. Required in edge mode. |
| `federation.interval` | duration | `1m` | — | Pull/heartbeat tick; `<= 0` means `1m`. |
| `federation.insecureSkipVerify` | bool | `false` | — | Skip TLS verification towards the main instance. |
| `federation.applyConfig` | bool (nullable) | unset ⇒ `true` | — | Pull the site bundle from main and apply it locally. `false` gives a heartbeat-only edge. |

The edge loop, what flows in which direction and the Site resource are described in [Federation](/docs/concepts/federation/) and [Tenants and sites](/docs/administration/tenants-and-sites/).

## Environment variables

Every variable that `northplaned` maps onto a config key (naming scheme: `NORTHPLANE_` + section + `_` + field in SCREAMING_SNAKE_CASE of the YAML key):

| Variable | Key | Type |
|---|---|---|
| `NORTHPLANE_LISTEN` | `listen` | string |
| `NORTHPLANE_BASE_URL` | `baseUrl` | string |
| `NORTHPLANE_DATA_DIR` | `dataDir` | string |
| `NORTHPLANE_STORAGE_DSN` | `storage.dsn` | string |
| `NORTHPLANE_TLS_CERT_FILE` | `tls.certFile` | string |
| `NORTHPLANE_TLS_KEY_FILE` | `tls.keyFile` | string |
| `NORTHPLANE_TLS_INSECURE` | `tls.insecure` | bool |
| `NORTHPLANE_TRUST_PROXY` | `trustProxy` | bool |
| `NORTHPLANE_DEMO` | `demo` | bool |
| `NORTHPLANE_ALLOW_SIGNUP` | `allowSignup` | bool |
| `NORTHPLANE_OIDC_ISSUER` | `oidc.issuer` | string |
| `NORTHPLANE_OIDC_CLIENT_ID` | `oidc.clientId` | string |
| `NORTHPLANE_OIDC_CLIENT_SECRET` | `oidc.clientSecret` | string |
| `NORTHPLANE_LDAP_URL` | `ldap.url` | string |
| `NORTHPLANE_LDAP_BIND_DN` | `ldap.bindDn` | string |
| `NORTHPLANE_LDAP_BIND_PASSWORD` | `ldap.bindPassword` | string |
| `NORTHPLANE_LDAP_BASE_DN` | `ldap.baseDn` | string |
| `NORTHPLANE_FEDERATION_MODE` | `federation.mode` | string |
| `NORTHPLANE_FEDERATION_MAIN_URL` | `federation.mainUrl` | string |
| `NORTHPLANE_FEDERATION_TOKEN` | `federation.token` | string |
| `NORTHPLANE_FEDERATION_SITE` | `federation.site` | string |
| `NORTHPLANE_AI_PROVIDER` | `ai.provider` | string |
| `NORTHPLANE_AI_ENDPOINT` | `ai.endpoint` | string |
| `NORTHPLANE_AI_MODEL` | `ai.model` | string |
| `NORTHPLANE_AI_API_KEY_ENV` | `ai.apiKeyEnv` | string |
| `NORTHPLANE_PLUGINS_DIR` | `pluginsDir` | string |
| `NORTHPLANE_LOG_LEVEL` | `logLevel` | string |
| `NORTHPLANE_LOG_FORMAT` | `logFormat` | string |
| `NORTHPLANE_SECRET_KEY_FILE` | `secretKeyFile` | string |
| `NORTHPLANE_BACKUP_TARGET` | `backup.target` | string |
| `NORTHPLANE_DEADMAN_URL` | `deadManUrl` | string |
| `NORTHPLANE_EXEC_POOL_SIZE` | `execPoolSize` | int |

**File-only keys** (no environment equivalent): `deadManInterval`, `pluginsAllow`, `storage.eventRetentionMonths`, `oidc.scopes`, `oidc.groupsClaim`, `oidc.adminGroup`, all `ldap.*` keys except `url`/`bindDn`/`bindPassword`/`baseDn`, `ai.apiKey`, `ai.modelDeep`, `ai.maxMonthlyTokens`, `ai.redaction.*`, `backup.interval`, `federation.interval`, `federation.insecureSkipVerify`, `federation.applyConfig`.

### Non-config environment variables

These are read by the binaries directly and do not correspond to a config key:

| Variable | Read by | Effect |
|---|---|---|
| `NP_DEFAULT_ADMIN_DISABLED` | `northplaned serve` | Any non-empty value skips the break-glass admin seeding at start-up. |
| `NP_DEFAULT_ADMIN_EMAIL` | `northplaned serve` | E-mail of the seeded admin; default `admin@localhost`. |
| `NP_DEFAULT_ADMIN_NAME` | `northplaned serve` | Display name of the seeded admin; default `Administrator`. |
| `NP_DEFAULT_ADMIN_PASSWORD` | `northplaned serve` | Password of the seeded admin. **Set but empty = opt out of seeding**; unset = a random 32-hex-character password is generated and logged once at WARN level. |
| `NORTHPLANE_TOKEN` | `northplaned mcp` | The `np_…` API token that authenticates the stdio MCP session (required). Also read by `np-agent` to override `token` in `agent.yaml`. |
| `XDG_DATA_HOME` | `northplaned` | Data-directory resolution for non-root users on Linux. |
| the variable named in `ai.apiKeyEnv` | `northplaned` | Holds the AI provider API key. |
| `NP_SERVER`, `NP_TOKEN` | `np` CLI | Server URL (default `https://localhost:8443`) and token; see [np CLI](/docs/reference/cli-np/). |
| `NP_DEV_DIR`, `NP_DEV_LISTEN`, `NP_DEV_WEB_PORT`, `NP_DEV_DEMO`, `NP_DEV_POLL`, `NP_API` | `scripts/dev.sh` (`make dev`) | Development workflow knobs; see [Development setup](/docs/development/setup/). |
| `NORTHPLANE_TEST_PG_DSN` | Go test suite | Runs the storage tests against PostgreSQL (CI only). |

:::note[The default admin closes /setup]
`serve` runs the default-admin seeding on **every** start: unless `NP_DEFAULT_ADMIN_DISABLED` is set (or `NP_DEFAULT_ADMIN_PASSWORD` is set to an empty string), and as long as no enabled local admin exists, it creates a local admin account before the HTTP listener opens. Because a local user then exists, the interactive first-run page `/setup` is closed on a default install. Set `NP_DEFAULT_ADMIN_DISABLED=1` if you want to create the first admin through `/setup`. Details: [Authentication](/docs/administration/authentication/).
:::

## Validation errors

`Load` validates the merged configuration (after environment overrides). Each of these conditions refuses to start with `northplaned: config: config invalid: <message>`:

| Condition | Message |
|---|---|
| `listen` empty | `listen: must be set (host:port, e.g. "127.0.0.1:8443")` |
| `listen` not `host:port` | `listen "<value>": not a valid host:port: …` |
| `listen` has an empty port (`127.0.0.1:`) | `listen "<value>": missing port` |
| port is neither numeric nor a known service name (port `0` is exempt) | `listen "<value>": invalid port: …` |
| `tls.certFile` without `tls.keyFile` | `tls.certFile set without tls.keyFile` |
| `tls.keyFile` without `tls.certFile` | `tls.keyFile set without tls.certFile` |
| any of `oidc.issuer`, `clientId`, `clientSecret`, `adminGroup` set but `issuer` empty | `oidc configured but oidc.issuer is empty` |
| OIDC block used but `clientId` empty | `oidc configured but oidc.clientId is empty` |
| `ai.provider` not one of `none`, `anthropic`, `azure-openai`, `openai-compat` | `ai.provider "<value>": must be one of none\|anthropic\|azure-openai\|openai-compat` |
| `ldap.url`, `bindDn` or `baseDn` set but `url` empty | `ldap configured but ldap.url is empty` |
| `ldap.url` without `ldap://` or `ldaps://` | `ldap.url "<value>": must start with ldap:// or ldaps://` |
| LDAP block used but `baseDn` empty | `ldap configured but ldap.baseDn is empty` |
| `ldap.bindDn` set but `bindPassword` empty | `ldap.bindDn set without ldap.bindPassword (set it or NORTHPLANE_LDAP_BIND_PASSWORD)` |
| `federation.mode` not `""` or `edge` | `federation.mode "<value>": must be empty or "edge"` |
| edge mode, `mainUrl` not `http(s)://` | `federation.mainUrl "<value>": must be an http(s) URL` |
| edge mode, `token` empty | `federation.mode edge requires federation.token (mint on the main instance, scope sites:connect)` |
| edge mode, `site` empty | `federation.mode edge requires federation.site (the site name registered on the main instance)` |

Validation is deliberately conservative — it never rejects the development/demo defaults. Some problems are only detected later, when `serve` starts:

- **Fatal at start-up** (`northplaned: serve: …` / `storage: …` / `tsdb: …`): plaintext on a non-loopback listener without `trustProxy` or `tls.insecure`; a certificate/key pair that cannot be loaded (never falls back to plaintext); the listen address cannot be bound; the store or the TSDB cannot be opened (including failed migrations).
- **Warning only**: an unusable `secretKeyFile` (falls back to `<dataDir>/secret.key`, or disables the secret store); OIDC discovery failure (SSO disabled); VAPID key generation failure (web push disabled).

## Not configurable

Operators often look for the following knobs in `config.yaml`. They are hard-coded in this version:

| Topic | Value |
|---|---|
| NP-TSDB retention | raw samples 30 days, 5-minute aggregates 400 days, 1-hour aggregates 5 years; series cap 100 000 |
| Session lifetime | 12 h for local/LDAP/OIDC logins, 30 days with "remember me" |
| Login rate limit | per client IP, burst 8, refill 1 attempt per 15 s (about 4/min); applies to `POST /login`, `/setup`, `/register`; throttled responses carry `Retry-After: 30` |
| Minimum password length | 12 characters (everywhere a local password is set) |
| HTTP server timeouts | `ReadHeaderTimeout` 10 s, `ReadTimeout` 60 s, `IdleTimeout` 120 s, `MaxHeaderBytes` 1 MiB, no global write timeout |
| Per-request response deadline | 30 s (`503 request timeout`), except `/api/v1/stream`, `/api/v1/events:export`, `/api/v1/ai/chat`, `/mcp` and `/mcp/*` |
| Body limits | JSON bodies 1 MiB, bundle bodies 8 MiB, ingest bodies 1 MiB |
| Graceful shutdown budget | 30 s (workers drained, then the process exits) |
| Ingest rate limits | per event source (`rateLimit` / `burst` fields of the EventSource resource, defaults 50/s and 200) — API-managed, not config |
| Notification retry policy | per channel (`retryMaxAttempts`, `retryBackoffSeconds`, `retryBackoffCapSeconds` in the channel config) — API-managed |
| Outgoing e-mail / SMTP | a notification channel, not config — see [Channels](/docs/alarming/channels/) |
| Metrics endpoint | always on at `/metrics`, unauthenticated, no key to disable it |
| Security headers and CSP | fixed, see [TLS and reverse proxies](/docs/administration/tls-and-proxy/#security-headers) |
| Background worker restart back-off | 1 s after a panic |
| Janitor cadence | downtime depths every 30 s, cleanup every 10 min, nightly maintenance between 02:00 and 03:59 local time |
| Audit log retention | none — the audit log is never purged |
| Event API page sizes | default 200, max 1000; NDJSON export cap 100 000 rows |
| Default admin seeding | controlled by `NP_DEFAULT_ADMIN_*` environment variables only |

## Example config.yaml

`northplaned init` writes this file (shown as generated for root: `--dir /etc/northplane`, `--data /var/lib/northplane`; the `dataDir` and `secretKeyFile` values are interpolated from the flags). It parses with the strict decoder and is a good starting point for any install:

```yaml title="/etc/northplane/config.yaml"
# Northplane bootstrap configuration (SPEC §15.2).
# Only pre-API settings live here — everything else is managed via API,
# UI or config bundles. Environment overrides: NORTHPLANE_*.

# Loopback default. To serve the network, set listen: ":8443" AND
# configure TLS below (plaintext on non-loopback refuses to start).
listen: "127.0.0.1:8443"
#baseUrl: "https://monitoring.example.net"
dataDir: "/var/lib/northplane"
secretKeyFile: "/etc/northplane/secret.key"

storage:
  # Empty dsn = embedded SQLite under dataDir (default).
  # PostgreSQL server mode: "postgres://np:secret@db:5432/northplane"
  dsn: ""
  eventRetentionMonths: 12

tls:
  certFile: ""
  keyFile: ""
  # insecure: true   # dev only; refused on non-loopback listeners

#oidc:
#  issuer: "https://login.microsoftonline.com/<tenant>/v2.0"
#  clientId: "…"
#  clientSecret: "…"
#  adminGroup: "<entra-group-object-id>"

# Directory user sync + login (LDAP / Active Directory).
#ldap:
#  url: "ldaps://dc1.example.net:636"
#  bindDn: "cn=svc-northplane,ou=service,dc=example,dc=net"
#  bindPassword: "…"        # or NORTHPLANE_LDAP_BIND_PASSWORD
#  baseDn: "dc=example,dc=net"
#  userFilter: "(&(objectClass=person)(mail=*))"
#  userAttr: mail            # AD: userPrincipalName
#  idAttr: ""                # AD: objectGUID, OpenLDAP: entryUUID (stable across DN moves)
#  groupAttr: memberOf
#  adminGroup: "cn=northplane-admins,ou=groups,dc=example,dc=net"
#  syncInterval: 15m
#  defaultRoles: [viewer]
#  disableMissing: true

# Connect this instance to a main instance (customer-site edge mode).
#federation:
#  mode: edge
#  mainUrl: "https://main.example.net"
#  token: "np_…"             # minted on main, scope sites:connect
#  site: "customer-a"
#  interval: 60s

ai:
  provider: none     # anthropic | azure-openai | openai-compat | none
  #endpoint: "https://api.anthropic.com"
  #apiKeyEnv: ANTHROPIC_API_KEY
  #model: claude-sonnet-4-6
  #modelDeep: claude-opus-4-8
  #maxMonthlyTokens: 50000000

backup:
  target: ""          # directory for continuous backup; empty = disabled
  interval: 5m

#deadManUrl: "https://hc-ping.com/<uuid>"   # SPEC §14.2 dead-man switch
```

:::note
The comment `directory for continuous backup` on `backup.target` is aspirational: no continuous backup runs. Schedule `northplaned backup` yourself (see [Storage → Backup](/docs/administration/storage/#backup)). The `keyLine` is omitted when `init` is given an empty key path; `serve` then self-provisions `<dataDir>/secret.key`.
:::

## Typical configurations

Three minimal, complete variants. Each is valid on its own; combine with the template above as needed.

**Direct TLS on all interfaces** (the server terminates TLS itself):

```yaml title="config.yaml"
listen: ":8443"
baseUrl: "https://monitoring.example.net:8443"
dataDir: "/var/lib/northplane"
secretKeyFile: "/etc/northplane/secret.key"
tls:
  certFile: "/etc/northplane/tls/fullchain.pem"
  keyFile: "/etc/northplane/tls/privkey.pem"
```

**Behind a TLS-terminating reverse proxy** (container style, environment only — this is what the Compose stacks set):

```bash
NORTHPLANE_LISTEN=:8443
NORTHPLANE_TRUST_PROXY=true
NORTHPLANE_BASE_URL=https://monitoring.example.net
NORTHPLANE_DATA_DIR=/var/lib/northplane
NORTHPLANE_SECRET_KEY_FILE=/etc/northplane/secret.key
```

**PostgreSQL instead of SQLite** (the NP-TSDB and event segments still live under `dataDir`):

```yaml title="config.yaml"
storage:
  dsn: "postgres://np:<secret>@db.internal:5432/northplane?sslmode=require"
  eventRetentionMonths: 24
```

Restart `northplaned` after any change — there is no configuration reload.
