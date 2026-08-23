---
title: northplaned CLI
description: Every northplaned subcommand — serve, init, migrate, storage migrate, import nagios, backup, mcp, openapi, bootstrap-admin — with flags, behaviour, output and exit codes.
sidebar:
  order: 1
---

`northplaned` is the Northplane server binary. One artefact contains the HTTP server with the embedded UI and these docs, the schema migrator, the Nagios importer, the backup tool, the MCP stdio gateway and a headless admin bootstrap. This page is the complete reference for its command line. Configuration keys themselves are documented in [Configuration](/docs/administration/configuration/); installation procedures in [Installation](/docs/getting-started/installation/).

## Usage

Running `northplaned` without arguments is the same as `northplaned serve`. The built-in usage text (`northplaned help`, `--help`, `-h`):

```text
northplaned — Northplane monitoring server (<version>)

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
```

### Subcommands at a glance

| Subcommand | Purpose | Needs config / storage |
|---|---|---|
| `serve` (default) | Run the server: API, UI, scheduler, workers | yes / yes |
| `init` | Write `config.yaml`, `secret.key` and a systemd unit | no (writes a new one) / no |
| `migrate` | Apply pending schema migrations and exit | yes / yes |
| `storage migrate --to <dsn>` | Offline copy of relational data to another backend | yes / yes (source + target) |
| `import nagios --path <p>` | Convert a Nagios/Icinga config tree to a YAML bundle | no / no |
| `backup` | Consistent backup into `backup.target` | yes / yes |
| `mcp` | MCP server on stdio, authenticated by `NORTHPLANE_TOKEN` | yes / yes |
| `openapi` | Print the OpenAPI 3.1 document | no / no |
| `bootstrap-admin` | Mint the initial `*:*` API token for headless installs | yes / yes |
| `version`, `--version`, `-v` | Print `northplaned <version>` | no / no |
| `help`, `--help`, `-h` | Print the usage text | no / no |

### Flag syntax

All subcommands parse flags with Go's standard `flag` package:

- `-flag` and `--flag` are equivalent; `-flag=value` and `-flag value` both work.
- Flags must come **before** positional words. The only positional words are the subcommand selectors `storage migrate` and `import nagios` (the second word must be `migrate` / `nagios`).
- `-h` on any subcommand prints that subcommand's flag help and exits.
- `-config <path>` selects the config file for every command that loads one. Its default is the platform default path: `/etc/northplane/config.yaml` when running as root (or when that file exists), otherwise `<user config dir>/northplane/config.yaml` (`~/.config/northplane/config.yaml` on Linux, `~/Library/Application Support/northplane/config.yaml` on macOS). `init` does not take `-config`; it takes `--dir` instead.
- There are **no flags for individual config keys**. Override keys with `NORTHPLANE_*` environment variables (see [Configuration](/docs/administration/configuration/)).

### Errors and exit codes

| Exit code | When | Message (stderr) |
|---|---|---|
| 0 | normal completion; `version`, `help` | — |
| 1 | any fatal error: config invalid, storage/TSDB open failure, bind failure, missing required flag, refused overwrite, … | `northplaned: <message>` — e.g. `northplaned: config: config invalid: listen: must be set (host:port, e.g. "127.0.0.1:8443")` |
| 2 | unknown subcommand (usage is printed) | `unknown command "<x>"` |
| 2 | unknown flag (Go `flag.ExitOnError`) | flag package error + flag help |

A missing config file is **not** an error: defaults plus environment variables are used. An empty or comment-only file is also fine. Unknown YAML keys are a hard error (`config invalid: …`). The validation rules that refuse to start are listed on the [Configuration](/docs/administration/configuration/) page.

## serve

Runs the server. This is the default when no subcommand is given.

```bash
northplaned serve -config /etc/northplane/config.yaml
northplaned serve --demo                                   # plus the showcase environment
northplaned serve --demo --demo-snmp 10.0.0.1:161 --demo-traps udp://:9162
```

| Flag | Default | Meaning |
|---|---|---|
| `-config <path>` | platform default | config file |
| `--demo` | off | seed an idempotent demo environment at startup (unconditionally — bypasses the real-data guard) |
| `--demo-snmp <host:port>` | `127.0.0.1:161` | SNMP get/walk target used by the demo checks |
| `--demo-traps <addr>` | `udp://:9162` | trap-receiver listen address for the demo event source |

Startup sequence:

1. Load the config (file → `NORTHPLANE_*` env → validation) and set up the logger (`logLevel`, `logFormat`; logs go to **stderr**).
2. Install signal handling: SIGINT and SIGTERM cancel the root context.
3. Open storage (SQLite at `<dataDir>/core.db` or PostgreSQL from `storage.dsn`), apply pending schema migrations, open the event store. Fatal: `northplaned: storage: …`.
4. Open the NP-TSDB at `<dataDir>/tsdb` (built-in retention defaults). Fatal: `northplaned: tsdb: …`.
5. Decide about demo seeding: `--demo` seeds unconditionally; `demo: true` / `NORTHPLANE_DEMO=true` seeds only if the database holds no real (non-`demo=true`) hosts — otherwise it logs `NORTHPLANE_DEMO is set but this database already holds real (non-demo) hosts — skipping demo seeding …` and continues. A failed seed is fatal (`demo seed: …`). See [Demo mode](/docs/getting-started/demo-mode/).
6. Seed the break-glass default admin if no enabled local admin exists (controlled by `NP_DEFAULT_ADMIN_DISABLED`, `NP_DEFAULT_ADMIN_EMAIL`, `NP_DEFAULT_ADMIN_NAME`, `NP_DEFAULT_ADMIN_PASSWORD`; an unset password means a random one is generated and logged **once**). Details and the interplay with the `/setup` page are in [Authentication](/docs/administration/authentication/).
7. Wire all subsystems (catalog, scheduler, executor, pipeline, alerting, escalation, notifier, listeners, AI, MCP, API) and start listening. Fatal: `server: …` or `serve: …`.

What a successful start logs (JSON by default, `logFormat: text` for key=value):

- `storage: applying migration` per pending version.
- One of `server: generated secret-store master key`, `server: configured secretKeyFile unusable — falling back to the data directory`, `server: secret store disabled (no usable master key)`.
- `server: serving plaintext HTTP (loopback/dev or behind a TLS-terminating proxy — A-15.10 requires TLS in production)` when no certificate is configured.
- `northplane: listening` with `addr`, `scheme`, `storage` (`sqlite`/`postgres`), `objects` and `ai`.
- `WARN first run: open <URL>/setup to create your admin account` — only while no local user and no API token exist (on a default install the seeded admin closes this gate, so the line is usually absent).
- With `--demo`: `demo: user ready` (name, email, password, role) per demo user, `demo: hint` lines, `demo: environment seeded`.

TLS rules at start: with `tls.certFile`/`tls.keyFile` the listener is HTTPS (an unloadable pair is fatal: `TLS cert load failed, refusing to start insecure`); without a certificate the server only starts in plaintext when the bound address is loopback, or `tls.insecure: true`, or `trustProxy: true`. Otherwise it exits with `no TLS configured on a non-loopback listener — set tls.certFile/keyFile, or trustProxy behind a TLS-terminating proxy, or tls.insecure for dev`. See [TLS and reverse proxy](/docs/administration/tls-and-proxy/).

Shutdown: SIGINT/SIGTERM → `northplane: shutting down`, HTTP server drained with a 30 s budget, background workers waited for within the same budget (`northplane: background workers drained` or `northplane: shutdown budget elapsed, workers still running`), then store and TSDB are closed. There is **no SIGHUP reload** — configuration changes need a restart.

## init

Writes a starter configuration, a secret-store master key and a systemd unit. Safe to run as root on a fresh host; it never overwrites an existing `config.yaml`.

```bash
sudo northplaned init                       # → /etc/northplane/
northplaned init --dir ~/np-config --data ~/np-data
```

| Flag | Default | Meaning |
|---|---|---|
| `--dir <path>` | default config dir (`/etc/northplane` as root; `<user config dir>/northplane` otherwise) | where to write `config.yaml`, `secret.key`, `northplaned.service` |
| `--data <path>` | default data dir (`/var/lib/northplane` as root; `$XDG_DATA_HOME/northplane` or `~/.local/share/northplane` on Linux; `~/Library/Application Support/northplane` on macOS) | `dataDir` value written into the config |
| `--user <name>` | `northplane` | system account in the unit; created with `useradd --system` when missing (root on Linux only) |

Files written:

| File | Mode | Content |
|---|---|---|
| `<dir>/config.yaml` | 0640 | the commented template (verbatim on the [Configuration](/docs/administration/configuration/) page) with `dataDir` and `secretKeyFile` filled in |
| `<dir>/secret.key` | 0600 | 32 random bytes, hex-encoded + newline — the AES-256-GCM master key for secrets at rest. **Back it up.** |
| `northplaned.service` | 0644 | systemd unit (below) — written to `/etc/systemd/system/` when running as root on a Linux host that has that directory, otherwise to `<dir>/` |

Both `<dir>` and `<data>` are created with mode 0750. If `<dir>/config.yaml` already exists the command stops with `northplaned: <path> exists — refusing to overwrite` (exit 1) before writing anything else. As root on Linux the command also creates the service user when it does not exist (`useradd --system --no-create-home --home-dir <data> --shell nologin <user>`) and chowns `<dir>`, `config.yaml`, `secret.key` and `<data>` to it; when `useradd` fails the output carries a `NOTE:` with the manual steps.

The generated unit (`<cfgPath>` and `<dataDir>` are interpolated):

```ini title="northplaned.service"
[Unit]
Description=Northplane monitoring server
Documentation=https://github.com/myfoxit/northplane
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=<path of the northplaned binary that ran init> serve -config <cfgPath>
Restart=on-failure
RestartSec=2
User=<user>
Group=<user>
StateDirectory=northplane
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=<dataDir>

[Install]
WantedBy=multi-user.target
```

There is no `WatchdogSec` on purpose: the binary does not send sd_notify keep-alives, and a watchdog without them would restart the service every interval. The full procedure is in [Installation](/docs/getting-started/installation/#set-up-as-a-service-with-northplaned-init).

Stdout on success:

```text
initialised:
  config:       <cfgPath>
  secret key:   <keyPath> (0600 — back this up!)
  systemd unit: <unitPath> (copy to /etc/systemd/system/)

next steps:
  1. review <cfgPath> (TLS, storage backend, OIDC)
  2. systemctl enable --now northplaned
  3. open /setup in the browser to create the admin account
     (or headless: northplaned bootstrap-admin -config <cfgPath>)
```

## migrate

```bash
northplaned migrate -config /etc/northplane/config.yaml
```

Opens the configured store — which applies every pending schema migration, each in its own transaction, recording the version in `schema_version` — then closes it and prints `migrations applied — schema is current`. `serve` does the same automatically on start, so `migrate` is for operators who want the schema step separate (for example in a deploy pipeline, or to upgrade PostgreSQL before the new server version starts). Background: [Storage](/docs/administration/storage/), [Upgrades](/docs/administration/upgrades/).

## storage migrate

```bash
# SQLite → PostgreSQL
northplaned storage migrate --to 'postgres://np:secret@db:5432/northplane' -config /etc/northplane/config.yaml
# PostgreSQL → SQLite file
northplaned storage migrate --to /var/lib/northplane/core.db -config /etc/northplane/config.yaml
```

| Flag | Meaning |
|---|---|
| `--to <dsn>` | **required** — target DSN: `postgres://…`/`postgresql://…` for PostgreSQL; any other non-empty value is treated as a SQLite file path |
| `-config <path>` | config of the **source** instance |

Behaviour: opens the source store from the config, opens the target with the given DSN (for a SQLite target the event segments are written under `<dataDir>-migrated`), then copies all relational tables — tenants, users, objects, labels, check state, alerts, incidents, resources, downtimes, silences, heartbeats, API tokens, sessions, secrets, idempotency rows, escalations, outbox, AI tables, push subscriptions, report archive, KV — followed by the audit log (preserving sequence numbers and the hash chain) and all events across partitions. Prints `copied <n> rows. Point storage.dsn at the target and restart (NP-TSDB unaffected).`

:::note
This is an **offline** copy: stop `northplaned serve` first; downtime equals copy time. The NP-TSDB directory is backend-independent and is not touched. A wrong second word prints `usage: northplaned storage migrate --to <dsn> [-config …]`; a missing `--to` prints `--to required`.
:::

## import nagios

```bash
northplaned import nagios --path /etc/nagios --out northplane-import.yaml
northplaned import nagios --path /etc/icinga/objects/       # a directory without nagios.cfg: all *.cfg recursively
```

| Flag | Default | Meaning |
|---|---|---|
| `--path <p>` | **required** | a Nagios/Icinga main config file, or a directory. A directory containing `nagios.cfg`/`icinga.cfg` is expanded through that main file; otherwise every `*.cfg` below it is read recursively |
| `--out <file>` | `northplane-import.yaml` | output bundle (written with mode 0644) |

Output: the rendered YAML bundle, then a deviation report on stdout (German wording: `Import: N Dateien — … Hosts, … Services …`, label suggestions, `Abweichungsbericht (N Einträge):` or `Keine Abweichungen — vollständig mappbar.`), ending with `bundle written to <out> — review, then: np apply -f <out>`. No server or storage is needed. Mapping tables and the list of unsupported Nagios features are on [Plugins and Nagios](/docs/monitoring/plugins-and-nagios/); applying the bundle is described in [Config bundles](/docs/administration/config-bundles/).

## backup

```bash
northplaned backup -config /etc/northplane/config.yaml
```

Requires `backup.target` (or `NORTHPLANE_BACKUP_TARGET`) to point at a directory; otherwise exits with `backup.target not configured`. Writes `<backup.target>/northplane-<YYYYMMDD-HHMMSS UTC>/` containing a transaction-consistent `core.db` (`VACUUM INTO`, no writer stop needed), all `events-*.db` segments, the `tsdb/` tree and a `manifest.json`; in PostgreSQL mode only the schema version is recorded (dumping the database is the PostgreSQL operator's job). Prints `backup complete: <manifest path>`. There is **no periodic backup loop** in the server — schedule this command yourself (cron, systemd timer). What exactly is and is not included, and the restore procedure, are in [Storage](/docs/administration/storage/).

## mcp

```bash
export NORTHPLANE_TOKEN=np_…        # an API token — the session inherits exactly its scopes
northplaned mcp -config /etc/northplane/config.yaml
```

Serves the Model Context Protocol over **stdio** for a local MCP client (Claude Desktop, Claude Code, …). The command opens the store, the NP-TSDB and the object catalog **directly** from the config — it does not talk to a running server — and forces `logFormat: text` with logs on stderr, because stdout belongs to the MCP transport. `NORTHPLANE_TOKEN` must hold a valid API token; missing → `northplaned: set NORTHPLANE_TOKEN to a Northplane API token (np_…)`, invalid → `northplaned: token: …`. On success it logs `mcp: serving on stdio` with the token's actor name and runs until SIGINT/SIGTERM.

:::caution[Reduced tool set on stdio]
In this mode no scheduler, escalation engine, bundle planner, report renderer or resource administration is wired. Read and statistics tools and the prompts work; `list_config_resources`/`get_config_resource`/`upsert_config_resource`/`delete_config_resource` answer `resource administration is not wired in this deployment`, `propose_config_change`/`apply_config_change` answer `bundle planner not wired`, `render_report` answers `report renderer not wired`, and `run_check_now`/`acknowledge_alert` must not be used here. Prefer the Streamable HTTP transport at `/mcp` of a running server. Everything about MCP — both transports, tools, client configuration — is on [MCP server](/docs/ai/mcp-server/).
:::

## openapi

```bash
northplaned openapi > openapi.json
```

Prints the OpenAPI 3.1 document as indented JSON to stdout — generated from the route registry, without starting the server or touching storage, and identical to what a running instance serves at `/api/openapi.json`. `make types` uses it for the typed frontend client and for the REST reference in these docs. See [API overview](/docs/reference/api-overview/).

## bootstrap-admin

```bash
northplaned bootstrap-admin -config /etc/northplane/config.yaml
```

Headless alternative to the `/setup` page: mints an API token named `bootstrap-admin` in the default tenant with scope `*:*` (created by `northplaned init`) and prints it **once**:

```text
admin token (shown once, store it safely):

  np_<48 hex>

use it:
  export NP_TOKEN=np_<48 hex>
  np --server https://localhost:8443 get hosts
```

The store is opened (migrations run) so this works on a fresh data directory before the first `serve`. If a token named `bootstrap-admin` already exists: `northplaned: bootstrap-admin token already exists — revoke it first via API` (exit 1). Creating any API token closes the web first-run `/setup` gate by design. Revoke or rotate the token later via [API tokens](/docs/administration/api-tokens/).

## version and help

`northplaned version` (also `--version`, `-v`) prints `northplaned <version>`; the version string is `1.0.0-dev` for local builds and is injected at build time via `-ldflags "-X main.version=…"` (release tags, `main-<sha>` for CD images). The same value is exposed by `GET /api/v1/system/info`, the OpenAPI `info.version`, the MCP server implementation and the login page. `help`, `--help`, `-h` print the usage text above.

## Environment variables read by northplaned

| Variable | Used by | Effect |
|---|---|---|
| `NORTHPLANE_*` | all commands that load a config | override the corresponding `config.yaml` key — full index on [Configuration](/docs/administration/configuration/) |
| `NP_DEFAULT_ADMIN_DISABLED` | `serve` | any non-empty value skips break-glass admin seeding |
| `NP_DEFAULT_ADMIN_EMAIL`, `NP_DEFAULT_ADMIN_NAME` | `serve` | seed identity (defaults `admin@localhost`, `Administrator`) |
| `NP_DEFAULT_ADMIN_PASSWORD` | `serve` | seed password; **set but empty** = opt out; unset = generate and log once |
| `NORTHPLANE_TOKEN` | `mcp` | the API token that authenticates the stdio MCP session |
| `XDG_DATA_HOME` | all | default data dir on non-root Linux |
| the variable named by `ai.apiKeyEnv` | `serve`, `mcp` | API key of the legacy server-level AI provider |

## Examples

```bash
# First install on a Linux server (creates the service user + installs the unit)
sudo northplaned init
sudo systemctl enable --now northplaned
sudo journalctl -u northplaned -f          # watch for "northplane: listening"

# Headless admin token, then use the CLI
sudo northplaned bootstrap-admin -config /etc/northplane/config.yaml
export NP_TOKEN=np_…
np --server http://127.0.0.1:8443 get hosts

# Pre-apply migrations during an upgrade, then start
northplaned migrate -config /etc/northplane/config.yaml && systemctl start northplaned

# Nightly backup via cron (config has backup.target set)
0 2 * * * /usr/local/bin/northplaned backup -config /etc/northplane/config.yaml

# Convert a Nagios tree and apply it
northplaned import nagios --path /etc/nagios --out import.yaml
np apply -f import.yaml --dry-run
```
