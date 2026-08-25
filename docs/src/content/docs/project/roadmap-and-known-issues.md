---
title: Roadmap and known issues
description: Consolidated open backlog of Northplane — remaining findings from the August 2026 UI/UX audit and the code-level fact sheets, grouped by area with severity. Everything previously listed as fixed has been verified and removed.
sidebar:
  order: 1
---

This page is the single list of things that do not (yet) work as the rest of the documentation might lead you to expect. Every entry was re-verified against the current code; items that were fixed have been **removed** (the August 2026 remediation pass closed the entire High-severity backlog — RBAC-gated Admin tabs, the SNMPv3 secret-reference mismatch, the dead second deploy target, check periods, SLA downtime exclusion, snooze/incident UI, and some forty more). The documentation describes the real current behaviour wherever an item below applies.

**Severity**: `High` = blocks a core flow, loses data, or misleads in a security-relevant way · `Medium` = wrong or missing behaviour with a workaround · `Low` = cosmetic, inconsistency, or documentation-only. **Type**: `Bug` · `UX` · `Enhancement` (missing feature) · `Doc` (behaviour is fine, expectations were wrong). IDs `OBJ-1`, `DENSITY-1`, `I18N-1`, `A11Y-1` are the August audit's; all other IDs are assigned on this page.

## Priorities at a glance

No High-severity items remain open. The Medium items:

| ID | Item | Severity | Type |
|---|---|---|---|
| ALM-1 | Alert groups can be configured and attached to rules but are not evaluated anywhere at runtime | Medium | Enhancement |
| ALM-3 | Suppression re-arm state and `pendingFor` drafts are in-memory and lost on restart | Medium | Bug |
| NTF-1 | Escalation steps select channel **types**; several channels of one type cannot be addressed | Medium | Enhancement |
| NTF-6 | Web push is server-ready (VAPID, subscription endpoints) but the UI has no subscription flow | Medium | Enhancement |
| NTF-2 | IMAP/e-mail inbound: attachments ignored, no STARTTLS | Medium | Enhancement |
| API-2 | `X-Forwarded-For` is not honoured — rate limiter, `ipBind` and audit `sourceIp` see the proxy | Medium | Bug |
| CHK-7 | Audit-chain verification is not reliable on PostgreSQL (`jsonb` normalisation) | Medium | Bug |
| CHK-2 | A passive **host** result with state `DOWN`/`1` maps to UP (WARNING→UP host mapping) | Medium | Bug |
| CHK-4 | RRULE downtimes: active-downtime listing filters on the literal `end_at` | Medium | Bug |
| DEP-3 | No periodic backup (`backup.interval` parsed but unused); no vzdump job on the hypervisor | Medium | Enhancement |
| RBAC-3 | Role `scope.folder`/`scope.selector`/`scope.tenantId` stored but never enforced | Medium | Enhancement |
| OBJ-1 | The objects list mixes hosts and services flat; no grouping by host/folder | Medium | UX |
| AI-3 | `northplaned mcp` (stdio) runs without scheduler/escalation/planner wiring | Medium | Doc |
| AGT-2 | The Agents tab says the host "appears automatically under Objects"; the server rejects unknown hosts | Medium | Doc |
| AGT-3 | An agent batch beyond the server's 1 MiB JSON cap is not handled (potential stuck buffer) | Medium | Doc |
| CHK-3 | `CheckCommand.timeout` is not used by the executor (the object's `timeout` is) | Medium | Doc |

## RBAC and tenancy

| ID | Severity | Description | Notes |
|---|---|---|---|
| RBAC-3 | Medium · Enhancement | Role `scope.folder` / `scope.selector` / `scope.tenantId` are stored and editable but never enforced — the principal's folder scope is never populated, so `np:auth/scope` cannot trigger and `selector` is never evaluated. | Treat folder/selector scoping as not implemented; tenant scoping comes from the principal's tenant. |
| RBAC-7 | Medium · Doc | Ingest URLs carry no tenant: `/api/v1/ingest/{source}` resolves the source by id **or name across all tenants** (first match in slug order wins), and ack links search all tenants for the alert. | Event-source names are effectively global for ingest; prefer ids in URLs. |
| RBAC-8 | Low · Enhancement | Tenants can be created but not renamed, disabled or deleted — there is no `PUT`/`DELETE /api/v1/tenants`. | The Admin tab says so explicitly. |
| RBAC-9 | Low · Doc | `dashboards:read`, `dashboards:write`, `reports:read` and `config:propose` are granted by built-in roles but no route checks them (dashboards/reports use `objects:read`/`config:write`; config tools need `config:write`). | Legacy/forward-looking permission names. |
| RBAC-10 | Low · Enhancement | No RP-initiated (IdP) logout — `LogoutURL` exists in the OIDC client but nothing calls it. | Local, LDAP and OIDC logins and logouts are audited. |
| RBAC-11 | Low · Doc | Routes with an empty permission that still require a logged-in principal (whoami, branding, preferences, push subscriptions, change-password) cannot express that in `x-required-permission`. | The generated API reference shows no permission for them. |

See [Tenancy and RBAC](/docs/concepts/tenancy-rbac/) and [Users, roles and permissions](/docs/administration/users-roles-permissions/).

## Alarming: rules, escalation, suppression

| ID | Severity | Description | Notes |
|---|---|---|---|
| ALM-1 | Medium · Enhancement | Alert groups (`/alert-groups`, the Groups tab, `groupId` on rules) are configuration only — no runtime component evaluates them. | Rules behave as if no group were set. |
| ALM-3 | Medium · Bug | Suppression re-arm state and `pendingFor` drafts are in-memory and lost on restart; a pending alert restarts its timer, a suppressed alert will not re-notify after the downtime ends if the server restarted meanwhile. | See [Reliability](/docs/alarming/reliability/). |
| ALM-7 | Low · Doc | A step that lists `channels` overrides the contacts' notification preferences completely — including their time-period and min-severity filters. | Intended; easy to miss. |
| ALM-8 | Low · Doc | An acknowledged alert never receives a next step or repeat regardless of `unlessAcked`; `unlessAcked: true` only suppresses the send of an already-due step. | |
| ALM-9 | Low · Doc | The correlator reuses an existing incident of any clustered alert (last seen wins) rather than always opening a new one. | |
| ALM-10 | Low · Doc | `labels.source` is stamped only by the email, snmp-trap and ESPA adapters; webhook, Alertmanager, MQTT and SMS events carry the source only as `event.source`. | Put a `source` label on the source's `labels` if rules need it. |
| ALM-12 | Low · Doc | `GET /incidents/{id}` returns `{incident, alerts}` only — no timeline. `GET /alerts` documentation mentions a `firing` status; the real statuses are `open`, `acked`, `resolved`, `expired`. | |
| ALM-13 | Low · Doc | `EventSource.type` values `heartbeat` and `agent` appear in a struct comment but have no adapter. | |
| ALM-14 | Low · Doc | Default severities differ per adapter: imap/email, snmp-trap and sms-inbound `warning`; mqtt, espa, espa-x `info`; voice-inbound and asterisk-inbound `critical`; alertmanager label-driven; webhook body-driven (`info` if empty). | |

## Notifications, channels and telephony

| ID | Severity | Description | Notes |
|---|---|---|---|
| NTF-1 | Medium · Enhancement | Escalation steps select channel **types**; the notifier uses only the alphabetically first enabled channel of each type. Several channels of one type (for example two Twilio voice accounts) cannot be addressed from steps. | |
| NTF-2 | Medium · Enhancement | IMAP/e-mail inbound: attachments are ignored and STARTTLS is not supported (implicit TLS or plaintext only). | |
| NTF-6 | Medium · Enhancement | Web push: the server stores subscriptions (`POST/DELETE /push-subscriptions`) and holds VAPID keys, but no endpoint returns the public key and the SPA has no subscription flow. `fcm://`/`apns://` registration for the mobile app works. | [Mobile push](/docs/alarming/mobile-push/) |
| NTF-9 | Low · Doc | The Twilio SMS provider has no `language`; German replies to inbound SMS depend on the **source's** `language`. The `voice` (Twilio TTS voice) key is read only by the inbound IVR, not by outbound Twilio calls — with a [TTS profile](/docs/alarming/text-to-speech/) voices are chosen per language for both directions. | |
| NTF-10 | Low · Doc | Ack links are valid until expiry (24 h) rather than one-shot as a comment claims; re-clicking shows the same page, and only `open` alerts are acknowledged. | [Acknowledge and snooze](/docs/alarming/acknowledge-and-snooze/) |
| NTF-11 | Low · Doc | `NotifyPending`/`NotifyDead` statuses are declared but unused. | |

## Agent and command-line tools

| ID | Severity | Description | Notes |
|---|---|---|---|
| AGT-2 | Medium · Doc | The Agents tab says the host "appears automatically under Objects"; the server rejects results for unknown hosts. | Create hosts/services first (UI, API, bundle, discovery). |
| AGT-3 | Medium · Doc | An agent batch that grows beyond the server's 1 MiB JSON cap is not handled in code (potential stuck buffer after a long outage). Inferred from code, not reproduced. | Keep `interval` and collector counts reasonable on flaky links. |
| AGT-4 | Low · Enhancement | No mTLS / enrollment (join/CA) flow — explicitly roadmap in the agent's source. No listener rate limiting or IP allow-listing, no SIGHUP config reload, no Windows service wrapper (`sc.exe` runs the console binary), and the systemd unit's `DynamicUser=yes` versus the readability of `/etc/northplane/agent.yaml` is not addressed. | |
| AGT-7 | Low · Bug | `np get silences\|downtimes`, `describe`, `export` and `doctor` ignore `--json`; `np apply --dry-run` still needs `config:write` (it does not use `:plan`); list commands are not paginated (hosts/services ≤ 500, events 50, audit 30, alerts 100). | |
| AGT-8 | Low · Doc | `np` defaults to `NP_SERVER=https://localhost:8443`, while the dev server listens plaintext on `127.0.0.1:8443`. | Set `NP_SERVER=http://127.0.0.1:8443` for `make dev`. |

## Checks, objects, storage and bundles

| ID | Severity | Description | Notes |
|---|---|---|---|
| CHK-2 | Medium · Bug | A passive **host** result with state `DOWN`/`1` maps to UP because of the WARNING→UP host mapping; use `2`/`CRITICAL` for a down host. Flagged for verification. | [Plugins and Nagios](/docs/monitoring/plugins-and-nagios/) |
| CHK-3 | Medium · Doc | `CheckCommand.timeout` is not used by the executor (the object's `timeout` is). | |
| CHK-4 | Medium · Bug | RRULE downtimes: the active-downtime listing filters on `end_at > now`; a recurring downtime whose literal `end` lies in the past may be excluded from later occurrences. Unclear whether intended. | Keep the literal window open-ended enough. |
| CHK-7 | Medium · Bug | Audit-chain verification is not reliable on PostgreSQL (`TestAuditChain` fails: `before_json`/`after_json` are `jsonb`, which normalises the text the hash was computed over; timestamps round-trip at microsecond precision). The CI `postgres` job is `continue-on-error`. | SQLite is fully green. [Storage](/docs/administration/storage/) |
| CHK-8 | Low · Doc | `Template.labels` is described as merged into objects but no merge path exists; `Template.kind` (`host\|service\|command`) is not enforced by validation; `TimePeriod.exclude` and `Acknowledgement{sticky, expiresAt}` have no implementation. | |
| CHK-9 | Low · Doc | Bundle kind `Heartbeat` is listed but not applied; `Tenant` is not a bundle kind; `metadata.name` (not `spec.name`) identifies documents. Bundle apply is not transactional — earlier documents stay applied after a failure. | [Config bundles](/docs/administration/config-bundles/) |
| CHK-10 | Low · Doc | `SavedFilter` exists as a generic resource but has no schema and no UI consumer; event types `comment`, `anomaly`, `forecast` are defined but never emitted; `CheckResult.source = satellite:<zone>` exists only in a comment. | |
| CHK-11 | Low · Doc | The staleness escalation (soft→hard timing) for passive checks is not spelled out in code comments. | |

## API and OpenAPI

| ID | Severity | Description | Notes |
|---|---|---|---|
| API-2 | Medium · Bug | `X-Forwarded-For` is not honoured (despite a comment in `config.go`): `trustProxy` only trusts `X-Forwarded-Proto`; token `ipBind`, the login rate limiter (burst 8, one attempt per 15 s) and the audit `sourceIp` all use the TCP peer address. Behind a reverse proxy every client shares one login bucket and the audit shows the proxy's IP; omit `ipBind` or bind to the proxy address. | [TLS and proxy](/docs/administration/tls-and-proxy/) |
| API-5 | Low · Enhancement | Dashboard `shareToken` has no backend route — there is no public, unauthenticated wallboard link. | |
| API-6 | Low · Doc | `/metrics`, `/api/v1/system/health` and `/api/v1/system/info` are anonymous and expose version, Go version, goroutines and queue depths. Restrict at the proxy if needed. | [Observability](/docs/administration/observability/) |
| API-7 | Low · Doc | `np_*_total` scrape gauges are typed `gauge`, not `counter`; `decode()` claims a strict content-type check that does not exist; an ingest `?token=` query parameter is accepted and will appear in access logs (prefer the header or HMAC). | |
| API-8 | Low · Enhancement | No audit-log retention or purge exists. | |

## AI agent and MCP

| ID | Severity | Description | Notes |
|---|---|---|---|
| AI-3 | Medium · Doc | `northplaned mcp` (stdio) runs without scheduler, escalation, planner, reports or resource-admin wiring: config tools and reports answer "not wired", `run_check_now`/`acknowledge_alert` are not safe there. | Use the HTTP transport for anything beyond reads. [MCP server](/docs/ai/mcp-server/) |
| AI-4 | Low · Doc | `propose_config_change` goes through the approval queue even for a dry-run plan; the built-in `ai-agent` role carries `config:propose`, which no tool checks (config tools need `config:write`). | |
| AI-6 | Low · Doc | Keys for keyed providers cannot be stored without a `secretKeyFile` (SecretBox); keyless Ollama/OpenAI-compatible connections work. Incident summaries are generated in German regardless of UI language. | |

## User interface and UX

| ID | Severity | Description | Notes |
|---|---|---|---|
| OBJ-1 | Medium · UX | The objects list mixes hosts and services flat; the folder is only a text column; no grouping by host/folder or collapsing. Full tree grouping conflicts with the row virtualiser. | |
| DENSITY-1 | Low · UX | Object detail: perfdata meters are wide and mostly empty for values like `rta 0.2ms`; "Last hard change —" stays empty; the Interval & Scheduling card is sparse while Metrics/Services scroll. | |
| I18N-1 | Low · UX | Mixed German/English in the German UI ("Wallboard", "Business Services", "Reports", "Discovery", "Alle Severities", "Start"); there is no language switcher (browser language only) and the server-rendered pages are German only. | [Navigation](/docs/ui/navigation/#language) |
| A11Y-1 | Low · UX | The service donut and row states rely primarily on green/amber/red; text labels are small and grey — hard for colour-blind users. | |
| UX-2 | Low · Enhancement | Non-default public status pages (`/status/<slug>`) read a `statuspage/<slug>` KV document that no UI or API writes; only `/status/default` is usable. | |
| UX-3 | Low · Enhancement | The counters (KPI) widget cannot be scoped to a selector — it needs an `/overview` selector parameter on the backend. Per-field origin in the effective-config table is not exposed by the API. | |
| INC-1 | Low · Enhancement | Incident **merge** has no UI — `POST /api/v1/incidents/{id}:merge` is API-only (creating incidents manually is in the UI). | |
| DIALOG-1 | Low · Enhancement | Radix modals set `pointer-events: none` on the body — third-party overlays (product tours) cannot be operated while a dialog is open. | |
| TOURS-1 | Low · UX | Tour content depends on data: the alert tour anchors *Acknowledge/Resolve* (only present with open alerts); the objects tour (`/objects*`) also matches detail pages. | |
| ROLES-1 | Low · Check | The Roles tab lists only the active tenant's system roles; the Users tab names `tenant-admin`, which is missing from the role list of tenant *Default* — verify whether intended. | |

## Deployment and operations

| ID | Severity | Description | Notes |
|---|---|---|---|
| DEP-2 | Low · Doc | The default-admin seeding closes `/setup` on a bare default install (the server seeds `admin@localhost` with a generated password); the root `docker-compose.yml` sets `NP_DEFAULT_ADMIN_DISABLED` so `/setup` works there. | Read the generated password from the logs, set `NP_DEFAULT_ADMIN_EMAIL/PASSWORD`, or set `NP_DEFAULT_ADMIN_DISABLED=1` to use `/setup`. [Authentication](/docs/administration/authentication/) |
| DEP-3 | Medium · Enhancement | No periodic backup: `backup.interval` is parsed but no scheduler uses it; only `northplaned backup` on demand. No vzdump job on the production Proxmox host. | Back up `secret.key` and the data volume yourself. [Operations](/docs/deployment/operations/) |
| DEP-5 | Low · Doc | The `TLSConfig` comment mentions autocert, but no ACME implementation exists — TLS termination in production is Caddy's job. | [TLS and proxy](/docs/administration/tls-and-proxy/) |
| DEP-7 | Low · Doc | Cloudflare in front of doktrace.com blocks Python user agents (HTTP 403) — scripts need a curl-like `User-Agent`. | |
| DEP-8 | Low · Doc | `/register` on production follows the repo variable `NORTHPLANE_ALLOW_SIGNUP` (default off). | Set the variable to re-enable the showcase signup; turn off for private instances. |

## Development and documentation

| ID | Severity | Description | Notes |
|---|---|---|---|
| DEV-1 | Low · Doc | `internal/web/dist` is committed; a local `go build` without `make web` embeds whatever is committed, which can lag behind `web/src`. CI and the Dockerfile always rebuild it. | Run `make web` (or `make all`) before a local build. [Frontend](/docs/development/frontend/) |
| DEV-2 | Low · Doc | Code comments reference SPEC/ADR documents (§7.7, §12.4 …) that are not in the repository. | Treat them as design notes. [Backend](/docs/development/backend/) |
| DEV-4 | Low · Doc | The server-rendered login/register pages allow-list their inline Stept bootstrap script by SHA-256 hash; changing its bytes (new workspace key) requires recomputing the hash. The SPA injects the widget at runtime and needs no hash. | [Frontend](/docs/development/frontend/) |

## What works well

The August audit recorded these deliberately, and the production checks behind [Environments](/docs/deployment/environments/) confirm the platform side:

- **Overview** is dense and useful: KPI tiles, problem list, service donut and "On call now" with name and number live from the schedule.
- **On-Call**: rotation timeline, hours per person and overrides behaved correctly in a live test — an override visibly rerouted the alarm (and since the remediation pass, `escalateTo: backup` honours overrides too).
- **IVR menus, channels and escalations** are clear tables with **Send test**/**Edit**; the configuration verified in the alarm test is findable one-to-one in the UI.
- **Object detail**: Overview/History/Configuration tabs, real metric charts with warn/crit bands, linked child services.
- **Server-side tenant isolation** holds (a write attempt against another tenant returns `403`), and the Admin surface now only offers what the operator's permissions can actually use.
- **Production** (`np-01`, doktrace.com): `healthz`/`readyz` green, the current `main` image deployed by CI, an agent fleet reporting, real Twilio SMS, ntfy and e-mail channels exercised end-to-end.

## Suggested order

1. Reliability first: ALM-3 (persist re-arm/pending timers), CHK-7 (PostgreSQL audit chain), API-2 (honour `X-Forwarded-For` behind `trustProxy`).
2. Alarming reach: NTF-6 (web push subscription flow), NTF-1 (address channel instances, not types), ALM-1 (evaluate alert groups or remove the surface).
3. Operations: DEP-3 (periodic backup driven by `backup.interval` + a vzdump job).
4. Decide RBAC-3 (implement folder/selector scoping or drop the fields from the role dialog).
5. Polish: OBJ-1, DENSITY-1, I18N-1, A11Y-1 and the Low items above.
