# Northplane

**An API-first, AI-first monitoring & alerting system in a single Go binary.**

Northplane bundles the breadth of a classic monitoring suite — checks, time
series, dashboards, alerting, escalation and on-call — into one self-contained,
statically-linked binary with an embedded React UI. It speaks the Nagios plugin
protocol so thousands of existing checks run unchanged, but the model above it is
modern: labels instead of rigid host-groups, templates with inheritance,
declarative config bundles, an event stream instead of logfile tailing, and a
versioned REST API that the web UI, CLI, and LLM agents all use equally.

🇩🇪 German design spec: [SPEC.md](SPEC.md) · German README: [README.de.md](README.de.md)

> Status: pre-1.0, actively developed. The full test suite (incl. the race
> detector and a SQLite **and** PostgreSQL storage matrix) is green, and the
> server runs unprivileged on Linux and macOS.

## Why Northplane

- **One binary, batteries included.** Server, CLI, and host agent are three
  small static binaries. The server embeds the UI, an own time-series engine
  (NP-TSDB), the alerting pipeline, and an MCP server — no external datastore is
  required to get started (SQLite by default; PostgreSQL when you need it).
- **Nagios-compatible, not Nagios-shaped.** Exit codes, output, perfdata and
  macros are fully supported; `check_disk`, `check_http`, your own scripts —
  they all just run. There's also a built-in suite (icmp, tcp, http(s),
  tls-cert, dns, smtp, imap, ntp, ssh-banner, snmp, nrpe, multi-step http-flow).
- **API-first.** Every feature is a versioned REST endpoint with generated
  OpenAPI. Configuration is a database transaction, not a file plus a daemon
  restart.
- **AI-first, but optional.** A built-in MCP server and an assistant let LLM
  agents operate the system — as a *permission-scoped, audited* API client:
  monitoring data is redacted before any model call, mutating actions go through
  an approval queue, and every tool call is audit-logged. With no provider
  configured, 100% of the deterministic features still work.
- **Safe by default.** Loopback-only listener out of the box; refuses plaintext
  on a public interface without an explicit opt-in; argon2id token hashing;
  AES-256-GCM secrets at rest; RBAC enforced centrally (including on the AI tool
  surface); CSRF protection for cookie sessions. See [SECURITY.md](SECURITY.md).

## Quickstart

**One-liner (Linux/macOS):**

```bash
curl -fsSL https://raw.githubusercontent.com/northplane/northplane/main/install.sh | sh
northplaned serve        # prints the /setup URL — open it in the browser, done
```

**Docker Compose (automatic HTTPS via bundled Caddy):**

```bash
git clone https://github.com/northplane/northplane && cd northplane
docker compose up -d     # → https://localhost  (self-signed cert)
# production: DOMAIN=monitoring.example.net docker compose up -d   → Let's Encrypt
```

Either way, the first browser visit lands on a one-shot **/setup** page —
create the admin account and you're monitoring. Headless instead?
`northplaned bootstrap-admin` mints an admin API token (and retires the
setup page).

Create your first host (CLI uses an API token — from the UI or `bootstrap-admin`):

```bash
export NP_SERVER=http://127.0.0.1:8443 NP_TOKEN=np_…

cat > first.yaml <<'EOF'
kind: Host
metadata: { name: web01, labels: { env: prod } }
spec:
  address: 192.0.2.10
  checkCommand: builtin:icmp
  interval: 60s
EOF
np apply -f first.yaml
np get hosts
```

UI: <http://127.0.0.1:8443> · API docs: `/api/docs` · OpenAPI: `/api/openapi.json`
· public status page: `/status/default` · Prometheus metrics: `/metrics`

### Production install (Linux, systemd)

```bash
sudo northplaned init           # writes /etc/northplane/config.yaml + secret.key + a unit
# review the config (TLS cert/key, storage backend, OIDC), then:
sudo systemctl enable --now northplaned
# open /setup in the browser — or headless: sudo northplaned bootstrap-admin
```

The server refuses to start plaintext on a non-loopback address. Either set
`tls.certFile`/`tls.keyFile`, or terminate TLS at a reverse proxy and set
`trustProxy: true` so secure cookies and HSTS are derived from `X-Forwarded-Proto`
(the bundled Compose setup does exactly that with Caddy).

### Docker (single container)

```bash
docker run -p 8443:8443 -v northplane-data:/var/lib/northplane \
  -e NORTHPLANE_TLS_INSECURE=true \
  ghcr.io/northplane/northplane:latest    # dev only — plaintext HTTP
```

The image is distroless and runs as a non-root user. For real HTTPS use the
Compose setup above (Caddy terminates TLS), or mount a cert and set
`NORTHPLANE_TLS_CERT_FILE`/`NORTHPLANE_TLS_KEY_FILE`.

## Configuration

Only bootstrap settings live in `config.yaml`; everything else is managed via the
API/UI/bundles. Every field has a `NORTHPLANE_*` environment override.

```yaml
listen: "127.0.0.1:8443"     # use ":8443" to serve the network (requires TLS)
dataDir: ""                  # default: per-user dir as non-root, /var/lib/northplane as root
trustProxy: false            # true behind a TLS-terminating reverse proxy

storage:
  dsn: ""                    # "" = embedded SQLite; "postgres://…" = PostgreSQL ≥ 15

tls:
  certFile: ""
  keyFile: ""
  # insecure: true           # dev only; refused on non-loopback listeners

ai:
  provider: none             # anthropic | azure-openai | openai-compat | none
```

Switch backends offline with `northplaned storage migrate --to <dsn>` (the
NP-TSDB is backend-independent and untouched).

## Migrating from Nagios / Icinga2

```bash
northplaned import nagios --path /etc/icinga2 --out import.yaml
#  → a config bundle + a deviation report + host-group→label suggestions
np apply -f import.yaml --dry-run
```

## AI & MCP

```yaml
ai:
  provider: anthropic
  apiKeyEnv: ANTHROPIC_API_KEY
  model: claude-sonnet-4-6
  maxMonthlyTokens: 50000000
  redaction: { hostnames: pseudonymize }
```

For Claude Desktop / any MCP client: `NORTHPLANE_TOKEN=np_… northplaned mcp`,
or use Streamable HTTP at `/mcp` with a normal API token. The MCP session
inherits exactly the token's RBAC scopes; mutating tools land in the approval
queue (`/api/v1/ai/actions`).

## Architecture

```
cmd/northplaned   Server (+ init, migrate, storage migrate, import nagios,
                  backup, mcp, bootstrap-admin)
cmd/np            CLI (apply/export, get, ack, downtime, oncall, audit verify…)
cmd/np-agent      Host agent (passive metrics — load, memory, disk — + local plugins)

internal/
  model           Domain model (UUIDv7, objects, templates, states, on-call)
  storage         SQLite (modernc) + PostgreSQL (pgx) behind one interface;
                  time-partitioned events; hash-chained audit log
  tsdb            NP-TSDB: Gorilla encoding, 2h chunks, WAL, downsampling tiers
  nagios          Plugin protocol, perfdata parser (fuzzed), macros, NRPE client,
                  Nagios/Icinga2 importer with a deviation report
  statemachine    soft/hard, flapping (21-check weighted), freshness
  scheduler       timing wheel with deterministic splay
  executor        exec pool (argv-only, process-group kill) + builtin checks
  pipeline        result → state machine → event → TSDB → SSE (batched commits)
  alerting        CEL rules (+ :test), heartbeats, suppression, correlation
  escalation      durable step timers, unlessAcked, repeats, on-call resolution
  notify          email, webhook (HMAC), Teams, Slack, ntfy, SMS, Web Push;
                  outbox with backoff + dead-letter queue
  api             REST + OpenAPI generated from code
  auth            np_ tokens (argon2id), RBAC, OIDC+PKCE, secret store (AES-GCM)
  ai / mcp        provider client, redaction, deterministic stats, approval gates;
                  MCP server (stdio + /mcp Streamable HTTP)
  server          wiring, TLS policy, janitor, backup, dead-man switch
  web             go:embed SPA + server-rendered login & status page
web/              React 19 + TypeScript + Vite + Tailwind
```

## Development

```bash
make                      # build the UI + all three binaries into ./bin
./bin/northplaned serve   # dev mode: SQLite per-user data dir, HTTP on loopback

make test                 # go vet + all suites (SQLite)
go test -race ./...       # race detector
NORTHPLANE_TEST_PG_DSN=postgres://np:np@localhost:5432/northplane?sslmode=disable \
  go test ./internal/storage/...        # PostgreSQL backend matrix
go test -fuzz=FuzzParsePerfdata ./internal/nagios/
```

`internal/web/dist` is committed, so `go build ./...` works without Node; run
`make web` after changing anything under `web/`. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## Roadmap (deliberately not in v1)

Satellite mTLS join (the agent uses token auth for now), SNMP-trap / email
ingress, voice provider, ServiceNow action execution, PDF report rendering,
SAML, and HA leader election. The interfaces for these already exist.

## License

[MIT](LICENSE) © 2026 Alexander Hoehne and the Northplane contributors.
