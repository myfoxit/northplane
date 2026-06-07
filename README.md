# Northplane

**API-first / AI-first Monitoring- und Alarmierungssystem** — die Implementierung
der [Systemspezifikation v0.4](SPEC.md).

Ein Go-Single-Binary (`northplaned`) mit eingebetteter React-UI, eigener
Zeitreihen-Engine (NP-TSDB), vollständiger Nagios-Plugin-Kompatibilität,
CEL-basierter Alarmierung mit Eskalationsketten und Bereitschaftsplänen,
eingebautem MCP-Server und einem AI-Assistenten als auditiertem API-Client.

## Quickstart (15 Minuten, SPEC §3.1)

```bash
make            # baut UI + alle drei Binaries nach bin/

# Initialisieren (Config, Secret-Key, systemd-Unit)
sudo bin/northplaned init --dir /etc/northplane --data /var/lib/northplane

# Dev-Modus ohne Setup: SQLite unter ./data, HTTP auf Loopback
cat > config.yaml <<EOF
listen: "127.0.0.1:8443"
dataDir: "./data"
tls: { insecure: true }
EOF
bin/northplaned serve -config config.yaml &

# Admin-Token erzeugen, ersten Host anlegen
bin/northplaned bootstrap-admin -config config.yaml
export NP_SERVER=http://127.0.0.1:8443 NP_TOKEN=np_…

cat > first.yaml <<EOF
kind: Host
metadata: { name: web01, labels: { env: prod } }
spec:
  address: 192.0.2.10
  checkCommand: builtin:icmp
  interval: 60s
EOF
bin/np apply -f first.yaml
bin/np get hosts
```

UI: <http://127.0.0.1:8443> · API-Doku: `/api/docs` · OpenAPI: `/api/openapi.json`
· Status-Page: `/status/default` · Metrics: `/metrics`

## Architektur

```
cmd/northplaned     Server (+ init, migrate, storage migrate, import nagios,
                    backup, mcp, bootstrap-admin)
cmd/np              CLI (apply/export, get, ack, downtime, oncall, audit verify…)
cmd/np-agent        Host-Agent (passive Metriken + lokale Plugins)

internal/
  model             Domänenmodell (UUIDv7, Objekte, Templates, States, On-Call)
  storage           SQLite (modernc) + PostgreSQL (pgx) hinter einer Schnittstelle;
                    zeitpartitionierte Events (ADR-13); Hash-Ketten-Audit (§13.5)
  tsdb              NP-TSDB: Gorilla-Encoding, 2h-Chunks, WAL, Downsampling-Tiers
  nagios            Plugin-Protokoll, Perfdata-Parser (fuzz-getestet), Makros,
                    NRPE-Client, Nagios/Icinga2-Importer mit Abweichungsbericht
  statemachine      soft/hard, Flapping (21-Check-gewichtet), Freshness
  scheduler         Timing-Wheel mit deterministischem Splay (§7.4)
  executor          exec-Pool (argv-only, Prozessgruppen-Kill) + builtin-Checks
  checks            icmp, tcp, http(s), tls-cert, dns, smtp, imap, ntp,
                    ssh-banner, snmp, http-flow, nrpe
  pipeline          Result → StateMachine → Event → TSDB → SSE (Batch-Commits)
  alerting          CEL-Regeln (+ :test), Heartbeats, Suppression, Korrelation
  escalation        durable Step-Timer, unlessAcked, Repeats, On-Call-Auflösung
  notify            E-Mail, Webhook (HMAC), Teams, Slack, ntfy, SMS, Web Push
                    (RFC 8291/8292); Outbox mit Backoff + DLQ
  api               REST (§11.3) + OpenAPI-Generierung aus Code (ADR-10)
  auth              np_-Tokens (argon2id), RBAC, OIDC+PKCE, Secret-Store (AES-GCM)
  ai                Provider-Client (Anthropic/OpenAI-kompatibel), Redaction,
                    deterministische Statistik (EWMA/MAD/Forecast), Approval-Gates
  mcp               MCP-Server (stdio + /mcp Streamable HTTP, offizielles SDK)
  server            Wiring, TLS-Policy, Janitor, Backup, Dead-Man-Switch
  web               go:embed-SPA + server-gerendertes Login + Status-Page
web/                React 19 + TS + Vite + Tailwind v4 (vendored UI-Primitives)
```

## Storage-Backends (SPEC §7.3)

```yaml
# SQLite (Default): keine Konfiguration nötig
storage: { dsn: "" }

# PostgreSQL ≥ 15:
storage: { dsn: "postgres://np:secret@db:5432/northplane" }
```

Wechsel: `northplaned storage migrate --to <dsn>` (offline, NP-TSDB bleibt).
Beide Backends laufen in der CI-Matrix: `NORTHPLANE_TEST_PG_DSN=… go test ./...`

## Nagios-Migration

```bash
northplaned import nagios --path /etc/icinga2 --out import.yaml
# → Bundle + Abweichungsbericht + Hostgruppen→Label-Vorschläge
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

MCP für Claude Desktop: `NORTHPLANE_TOKEN=np_… northplaned mcp -config …`
oder Streamable HTTP auf `/mcp` mit normalem API-Token. Mutierende Tools
landen in der Approval-Queue (`/api/v1/ai/actions`); ohne Provider bleiben
alle deterministischen Features aktiv.

## Tests

```bash
make test                                # vet + alle Suiten (SQLite)
NORTHPLANE_TEST_PG_DSN=postgres://… make test   # + PostgreSQL-Matrix
go test -fuzz=FuzzParsePerfdata ./internal/nagios/
```

## Scope-Hinweise (v1-Implementierung)

Bewusst gemäß Spec-Roadmap noch offen: Satelliten-mTLS-Join (M4; np-agent
nutzt Token-Auth), SNMP-Trap-/E-Mail-Ingress (v2), Voice-Provider (v2),
ServiceNow-Action-Ausführung (v2), PDF-Rendering (Chromium-Sidecar, ADR-11),
SAML (v2), HA-Leader-Election (M4). Die zugehörigen Schnittstellen
(EventSource-Typen, Outbox-Kind `action`, Reports-API) existieren bereits.
