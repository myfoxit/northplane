---
title: Roadmap and known issues
description: Consolidated known issues and backlog of Northplane — UI/UX audit findings (August and July 2026), code-level discrepancies found while writing these docs, and deployment items — grouped by area with severity, plus what was fixed since July and what works well.
sidebar:
  order: 1
---

This page is the single list of things that do not (yet) work as the rest of the documentation might lead you to expect. It consolidates three sources: the **August 2026 UI/UX audit** of the production instance (two roles — system admin and a tenant user — every page, all 21 Admin tabs, all Alerting tabs), the **July 2026 UI/UX audit** (admin only) with its items re-checked against the current code, and the **code-level discrepancies** recorded in the fact sheets that back this documentation (every claim there is tied to a file and line). The documentation itself already describes the real behaviour wherever one of these items applies; this page exists so you can see the whole backlog in one place and decide what matters for your deployment.

**Severity**: `High` = blocks a core flow, loses data, or misleads in a security-relevant way · `Medium` = wrong or missing behaviour with a workaround · `Low` = cosmetic, inconsistency, or documentation-only. **Type**: `Bug` · `UX` · `Enhancement` (missing feature) · `Doc` (behaviour is fine, expectations were wrong). IDs `RBAC-1/2`, `AGENT-1`, `NAV-1`, `WIDGET-1`, `EVENT-1`, `DASH-1`, `FORM-1`, `OBJ-1/2`, `TENANT-1`, `DENSITY-1`, `I18N-1`, `A11Y-1` are the August audit's; all other IDs are assigned on this page.

## Priorities at a glance

| ID | Item | Severity | Type |
|---|---|---|---|
| RBAC-1 | Admin tabs and action buttons are not aligned with the user's permissions — a tenant user sees Tenants/Sites/AI providers/System health/Config bundles with **Create** buttons that `403` | High | Bug |
| RBAC-2 | Those tabs fire `403` requests in the background (`/roles?limit=500`, `/tenants`, `/ai/policy`) and render empty tables instead of a "no access" state | High | Bug |
| NTF-7 | SNMPv3 trap credentials entered in the UI (`v3AuthSecretRef`/`v3PrivSecretRef`) are never read by the trap listener (`v3AuthPass`/`v3PrivPass`) | High | Bug |
| DEP-1 | The second production box `np-02` (Hetzner, 91.98.92.10) is gone — the IP was reassigned; the `deploy-hetzner` job fails on every run and the GitHub variables point at a stranger's host | High | Bug |
| NAV-1 | The 21-tab Admin strip runs off the right edge; it scrolls horizontally but the audit found no visible scroll affordance ("Dead-Letters…" cut off) | High | UX |
| ALM-1 | Alert groups can be configured and attached to rules but are not evaluated anywhere at runtime | Medium | Enhancement |
| ALM-2 | The rule dialog's template placeholders (`{{.ObjectID}}`, `{{.ObjectName}} ist {{.ToLabel}}`) do not match the engine's data model (`{{ .event.* }}`) and render `<no value>` | Medium | Bug |
| NTF-6 | Web push is server-ready (VAPID, subscription endpoints) but the UI has no subscription flow and no endpoint returns the VAPID public key | Medium | Enhancement |
| WIDGET-1 | The third-party Stept onboarding widget overlays buttons and table rows on every page, logs a CORS error, and cannot be switched off at runtime | Medium | Bug |
| EVENT-1 | The Events page is one unfiltered long list (200 rows, no time range/severity filter, no pagination, many rows without text) | Medium | UX |
| DASH-1 | Dashboards and Reports are empty in real-data mode with no starter content or guided first creation | Medium | UX |
| CHK-1 | `checkPeriod` is stored and shown in the effective config, but checks run regardless of the check period | Medium | Bug |
| DEP-3 | No periodic backup exists (`backup.interval` is parsed but unused) and no vzdump job is configured on the production hypervisor | Medium | Enhancement |

## RBAC and tenancy

| ID | Severity | Description | Notes |
|---|---|---|---|
| RBAC-1 | High · Bug | `/admin` renders a static list of 21 tabs regardless of permissions; only the tenant switcher (`admin:tenants`) and the Appearance controls (`config:write`) are gated client-side. | The API enforces everything server-side (a tenant user's `POST /tenants` is a clean `403`); the gap is UI-only. Fix: align tabs and action buttons with effective permissions (`admin:tenants`, `admin:*`, `sites:*` …). |
| RBAC-2 | High · Bug | Because the tabs render, the client issues `GET /roles?limit=500`, `GET /tenants`, `GET /ai/policy` that all `403`; the result is "No entries." instead of "Not authorised". | Follows from RBAC-1; additionally render `403` as an explicit not-authorised state. |
| TENANT-1 | Low · UX | August audit: the switcher reads "Eigener Mandant" without naming the tenant you are working in. | The current build appends the home tenant's name when it can resolve it from `GET /tenants` and shows an "Active customer: name" line for a non-home selection. |
| RBAC-3 | Medium · Enhancement | Role `scope.folder` / `scope.selector` / `scope.tenantId` are stored and editable but never enforced — the principal's folder scope is never populated, so `np:auth/scope` cannot trigger and `selector` is never evaluated. | Treat folder/selector scoping as not implemented; tenant scoping comes from the principal's tenant. |
| RBAC-4 | Medium · Bug | System roles are "immutable" in the model and the UI hides their edit/delete buttons, but `PUT`/`DELETE /api/v1/roles/{name}` with `admin:write` modifies or deletes them. | No server-side guard; be careful with automation that touches roles. |
| RBAC-5 | Medium · Bug | `POST /api/v1/alerts/{id}:ack` uses the principal's own tenant instead of honouring `X-Northplane-Tenant` — a cross-tenant operator cannot acknowledge through the header, while the other alert verbs do honour it. | Acknowledge with a session/token of the target tenant. |
| RBAC-6 | Medium · Bug | `GET /api/v1/users` is not tenant-filtered — it lists every user of the installation (needs `admin:users`). | Relevant when tenant admins hold `admin:users`. |
| RBAC-7 | Medium · Doc | Ingest URLs carry no tenant: `/api/v1/ingest/{source}` resolves the source by id **or name across all tenants** (first match in slug order wins), and ack links search all tenants for the alert. | Event-source names are effectively global for ingest; prefer ids in URLs. |
| RBAC-8 | Low · Enhancement | Tenants can be created but not renamed, disabled or deleted — there is no `PUT`/`DELETE /api/v1/tenants`. | The Admin tab says so explicitly. |
| RBAC-9 | Low · Doc | `dashboards:read`, `dashboards:write`, `reports:read` and `config:propose` are granted by built-in roles but no route checks them (dashboards/reports use `objects:read`/`config:write`; config tools need `config:write`). | Legacy/forward-looking permission names. |
| RBAC-10 | Low · Enhancement | OIDC logins and logouts are not audited; there is no RP-initiated (IdP) logout. | Local and LDAP logins are audited. |
| RBAC-11 | Low · Doc | Routes with an empty permission that still require a logged-in principal (whoami, branding, preferences, push subscriptions, change-password) cannot express that in `x-required-permission`. | The generated API reference shows no permission for them. |

See [Tenancy and RBAC](/docs/concepts/tenancy-rbac/) and [Users, roles and permissions](/docs/administration/users-roles-permissions/).

## Alarming: rules, escalation, suppression

| ID | Severity | Description | Notes |
|---|---|---|---|
| ALM-1 | Medium · Enhancement | Alert groups (`/alert-groups`, the Groups tab, `groupId` on rules) are configuration only — no runtime component evaluates them. | Rules behave as if no group were set. |
| ALM-2 | Medium · Bug | Rule dialog placeholders `{{.ObjectID}}` (dedup key) and `{{.ObjectName}} ist {{.ToLabel}}` (title) do not exist in the template data; they render the literal `<no value>`. The old ALARMING.md form `{{ .Payload.title }}` errors and falls back silently. | Use `{{ .event.summary }}`, `{{ .event.object }} is {{ .event.state }}`, `{{ .event.payload.title }}` … — see [Alert rules](/docs/alarming/alert-rules/). |
| ALM-3 | Medium · Bug | Suppression re-arm state and `pendingFor` drafts are in-memory and lost on restart; a pending alert restarts its timer, a suppressed alert will not re-notify after the downtime ends if the server restarted meanwhile. | See [Reliability](/docs/alarming/reliability/). |
| ALM-4 | Medium · Bug | The Alertmanager receiver `POST /ingest/{source}/alertmanager` ignores the source's `enabled` flag. | Disable by removing the source or its token. |
| ALM-5 | Medium · Bug | A heartbeat rule's `source` is compared with the event **source id**, not its name; the demo seed's `demo-heartbeat-rule` uses the heartbeat name `demo-cron` and therefore never arms. | Documented in [Alert rules](/docs/alarming/alert-rules/). |
| ALM-6 | Medium · Bug | `escalateTo: backup` in an escalation step ignores schedule overrides. | Overrides apply to the primary slot only. |
| ALM-7 | Low · Doc | A step that lists `channels` overrides the contacts' notification preferences completely — including their time-period and min-severity filters. | Intended; easy to miss. |
| ALM-8 | Low · Doc | An acknowledged alert never receives a next step or repeat regardless of `unlessAcked`; `unlessAcked: true` only suppresses the send of an already-due step. | |
| ALM-9 | Low · Doc | The correlator reuses an existing incident of any clustered alert (last seen wins) rather than always opening a new one. | |
| ALM-10 | Low · Doc | `labels.source` is stamped only by the email, snmp-trap and ESPA adapters; webhook, Alertmanager, MQTT and SMS events carry the source only as `event.source`. | Put a `source` label on the source's `labels` if rules need it. |
| ALM-11 | Low · Bug | `ack` events for SMS-keyword, IVR and outbound-DTMF acknowledgements carry `via: "ack-link"`; only the audit entries (`sms`, `voice-inbound`, `voice-dtmf`) and the AGI path (`asterisk-agi`) are precise. | |
| ALM-12 | Low · Doc | `GET /incidents/{id}` returns `{incident, alerts}` only — no timeline. `GET /alerts` documentation mentions a `firing` status; the real statuses are `open`, `acked`, `resolved`, `expired`. | |
| ALM-13 | Low · Doc | `EventSource.type` values `heartbeat` and `agent` appear in a struct comment but have no adapter. | |
| ALM-14 | Low · Doc | Default severities differ per adapter: imap/email, snmp-trap and sms-inbound `warning`; mqtt, espa, espa-x `info`; voice-inbound and asterisk-inbound `critical`; alertmanager label-driven; webhook body-driven (`info` if empty). | |

## Notifications, channels and telephony

| ID | Severity | Description | Notes |
|---|---|---|---|
| NTF-7 | High · Bug | The event-source dialog writes `v3AuthSecretRef`/`v3PrivSecretRef` for SNMPv3 traps; the listener reads `v3AuthPass`/`v3PrivPass`, so v3 authentication configured through the UI silently does not apply. | Set the inline keys via **More settings** until aligned — see [SNMP](/docs/monitoring/snmp/). |
| NTF-1 | Medium · Enhancement | Escalation steps select channel **types**; the notifier uses only the alphabetically first enabled channel of each type. Several channels of one type (for example two Twilio voice accounts) cannot be addressed from steps. | |
| NTF-6 | Medium · Enhancement | Web push: the server stores subscriptions (`POST/DELETE /push-subscriptions`) and holds VAPID keys, but no endpoint returns the public key and the SPA has no subscription flow. `fcm://`/`apns://` registration for the mobile app works. | [Mobile push](/docs/alarming/mobile-push/) |
| NTF-2 | Medium · Enhancement | IMAP/e-mail inbound: attachments are ignored and STARTTLS is not supported (implicit TLS or plaintext only). | |
| NTF-3 | Low · Bug | The webhook channel dialog offers an HTTP method field, but the backend always POSTs. | |
| NTF-4 | Low · UX | The ntfy `url` looks optional in the dialog but delivery fails with "no ntfy target" without it. | |
| NTF-5 | Low · Doc | Retry backoff jitter is ±10 % (a code comment and the old ALARMING.md say ±20 %); 30 attempts, then dead letter — correct. | [Reliability](/docs/alarming/reliability/) |
| NTF-8 | Low · Bug | `OutboxItem.channelId` is never populated; dead-letter rows show an empty channel id (the channel is only inside the payload). | |
| NTF-9 | Low · Doc | The Twilio SMS provider has no `language`; German replies to inbound SMS depend on the **source's** `language`. The `voice` (TTS voice) key is read only by the inbound IVR, not by outbound Twilio calls. | |
| NTF-10 | Low · Doc | Ack links are valid until expiry (24 h) rather than one-shot as a comment claims; re-clicking shows the same page, and only `open` alerts are acknowledged. | [Acknowledge and snooze](/docs/alarming/acknowledge-and-snooze/) |
| NTF-11 | Low · Doc | `NotifyPending`/`NotifyDead` statuses are declared but unused. | |

## Agent and command-line tools

| ID | Severity | Description | Notes |
|---|---|---|---|
| AGENT-1 | Low · Doc | The Agents tab's install one-liner 404'd while the repository was private; it works since the repository and the releases are public. Kept for the record. | — |
| AGT-2 | Medium · Doc | The Agents tab says the host "appears automatically under Objects"; the server rejects results for unknown hosts. | Create hosts/services first (UI, API, bundle, discovery). |
| AGT-3 | Medium · Doc | An agent batch that grows beyond the server's 1 MiB JSON cap is not handled in code (potential stuck buffer after a long outage). Inferred from code, not reproduced. | Keep `interval` and collector counts reasonable on flaky links. |
| AGT-4 | Low · Enhancement | No mTLS / enrollment (join/CA) flow — explicitly roadmap in the agent's source. No listener rate limiting or IP allow-listing, no SIGHUP config reload, no Windows service wrapper (`sc.exe` runs the console binary), and the systemd unit's `DynamicUser=yes` versus the readability of `/etc/northplane/agent.yaml` is not addressed. | |
| AGT-5 | Low · Bug | `np describe` usage says `object-id\|host-name`, but the route resolves by id only (a name yields `404`). | |
| AGT-6 | Low · Bug | `np --help` exits 2 with `unknown flag "--help"` (the global-flag loop consumes it); `np help` and `np -h` work. `--hours` values that do not parse silently keep `2.0`. | |
| AGT-7 | Low · Bug | `np get silences\|downtimes`, `describe`, `export` and `doctor` ignore `--json`; `np apply --dry-run` still needs `config:write` (it does not use `:plan`); list commands are not paginated (hosts/services ≤ 500, events 50, audit 30, alerts 100). | |
| AGT-8 | Low · Doc | `np` defaults to `NP_SERVER=https://localhost:8443`, while the dev server listens plaintext on `127.0.0.1:8443`. | Set `NP_SERVER=http://127.0.0.1:8443` for `make dev`. |

## Checks, objects, storage and bundles

| ID | Severity | Description | Notes |
|---|---|---|---|
| CHK-1 | Medium · Bug | `checkPeriod` (and `zone`) are stored and resolved into the effective config, but no scheduler/executor consumer exists — checks run 24x7 regardless. | Use downtimes/silences for quiet windows. |
| CHK-2 | Medium · Bug | A passive **host** result with state `DOWN`/`1` maps to UP because of the WARNING→UP host mapping; use `2`/`CRITICAL` for a down host. Flagged for verification. | [Plugins and Nagios](/docs/monitoring/plugins-and-nagios/) |
| CHK-3 | Medium · Doc | `CheckCommand.timeout` is not used by the executor (the object's `timeout` is). | |
| CHK-4 | Medium · Bug | RRULE downtimes: the active-downtime listing filters on `end_at > now`; a recurring downtime whose literal `end` lies in the past may be excluded from later occurrences. Unclear whether intended. | Keep the literal window open-ended enough. |
| CHK-5 | Medium · Bug | `excludeDowntimes` / `includeDowntimes` flags on business services and reports are stored but not applied in the SLA/availability math. | |
| CHK-6 | Medium · Bug | Bundle export lists at most 5000 objects and 2000 resources per kind — larger tenants would be truncated silently. | |
| CHK-7 | Medium · Bug | Audit-chain verification is not reliable on PostgreSQL (`TestAuditChain` fails: `before_json`/`after_json` are `jsonb`, which normalises the text the hash was computed over; timestamps round-trip at microsecond precision). The CI `postgres` job is `continue-on-error`. | SQLite is fully green. [Storage](/docs/administration/storage/) |
| CHK-8 | Low · Doc | `Template.labels` is described as merged into objects but no merge path exists; `Template.kind` (`host\|service\|command`) is not enforced by validation; `TimePeriod.exclude` and `Acknowledgement{sticky, expiresAt}` have no implementation. | |
| CHK-9 | Low · Doc | Bundle kind `Heartbeat` is listed but not applied; `Tenant` is not a bundle kind; `metadata.name` (not `spec.name`) identifies documents. Bundle apply is not transactional — earlier documents stay applied after a failure. | [Config bundles](/docs/administration/config-bundles/) |
| CHK-10 | Low · Doc | `SavedFilter` exists as a generic resource but has no schema and no UI consumer; event types `ai_action`, `comment`, `anomaly`, `forecast` are defined but never emitted; `CheckResult.source = satellite:<zone>` exists only in a comment. | |
| CHK-11 | Low · Doc | The staleness escalation (soft→hard timing) for passive checks is not spelled out in code comments. | |
| CHK-12 | Low · Doc | `northplaned backup` does not copy `dataDir/artifacts` although a comment says so. | |

## API and OpenAPI

| ID | Severity | Description | Notes |
|---|---|---|---|
| API-1 | Medium · Bug | `GET /api/v1/audit:export` is not on the streaming-exempt list, so the 30 s request deadline can cut very large exports. | Export in action-prefix slices. |
| API-2 | Medium · Bug | `X-Forwarded-For` is not honoured (despite a comment in `config.go`): `trustProxy` only trusts `X-Forwarded-Proto`; token `ipBind`, the login rate limiter (burst 8, one attempt per 15 s) and the audit `sourceIp` all use the TCP peer address. Behind a reverse proxy every client shares one login bucket and the audit shows the proxy's IP; omit `ipBind` or bind to the proxy address. | [TLS and proxy](/docs/administration/tls-and-proxy/) |
| API-3 | Low · Doc | OpenAPI success codes are heuristic in three places: `POST /schedules/{name}/overrides` returns `201` (documented 200), `POST /ai/chat` is an SSE stream (documented 201), `PUT /secrets/{name}` returns `204` (documented 200). | |
| API-4 | Low · Bug | `PUT /api/v1/ai/policy` and `PUT /api/v1/ai/chats/{id}` do not enforce `If-Match` server-side (last write wins); the SPA sends it anyway. | |
| API-5 | Low · Enhancement | Dashboard `shareToken` has no backend route — there is no public, unauthenticated wallboard link. | |
| API-6 | Low · Doc | `/metrics`, `/api/v1/system/health` and `/api/v1/system/info` are anonymous and expose version, Go version, goroutines and queue depths. Restrict at the proxy if needed. | [Observability](/docs/administration/observability/) |
| API-7 | Low · Doc | `np_*_total` scrape gauges are typed `gauge`, not `counter`; `decode()` claims a strict content-type check that does not exist; an ingest `?token=` query parameter is accepted and will appear in access logs (prefer the header or HMAC). | |
| API-8 | Low · Enhancement | No audit-log retention or purge exists. | |

## AI agent and MCP

| ID | Severity | Description | Notes |
|---|---|---|---|
| AI-1 | Medium · Bug | `POST /ai/actions/{id}:approve` only executes the action when the **server-level** `ai.provider` is configured; otherwise the action stays `approved` and never runs — including proposals from the agent chat and MCP, which themselves do not need `ai.provider`. | [Agent chat](/docs/ai/agent-chat/) |
| AI-2 | Medium · Bug | The MCP tab's **Read + operate** preset grants `maintenance:write` and `objects:write` instead of `downtimes:write`, `silences:write` and `checks:run` — the "operate" tools fail with that token. | Mint the token with the right scopes under API tokens. |
| AI-3 | Medium · Doc | `northplaned mcp` (stdio) runs without scheduler, escalation, planner, reports or resource-admin wiring: config tools and reports answer "not wired", `run_check_now`/`acknowledge_alert` are not safe there. | Use the HTTP transport for anything beyond reads. [MCP server](/docs/ai/mcp-server/) |
| AI-4 | Low · Doc | `propose_config_change` goes through the approval queue even for a dry-run plan; the built-in `ai-agent` role carries `config:propose`, which no tool checks (config tools need `config:write`). | |
| AI-5 | Low · Bug | Tools disabled by policy are still advertised to the legacy sidebar model (they are blocked on execution). | |
| AI-6 | Low · Doc | Keys for keyed providers cannot be stored without a `secretKeyFile` (SecretBox); keyless Ollama/OpenAI-compatible connections work. Incident summaries are generated in German regardless of UI language. | |

## User interface and UX

### Open findings (August 2026 audit)

| ID | Severity | Description | Notes |
|---|---|---|---|
| NAV-1 | High · UX | The Admin tab strip overflows; it has scrolled horizontally since July (thin scrollbar), but the audit still found no obvious affordance and the last tabs clipped. | Consider wrapping, edge fades or grouping. |
| WIDGET-1 | Medium · Bug | The Stept widget (tour checklist + chat bubble) sits bottom-right on every page and covers the last table rows and the **Delete** button of the last object row; the console shows a CORS error for `app.stepped.ai/widget-assets/i18n/de.json`. The key is hard-coded; removal needs a rebuild. | [Frontend](/docs/development/frontend/) |
| EVENT-1 | Medium · UX | `/events` shows up to 200 rows in one list with only a type dropdown and an object-id box; no time range, severity or pagination; `config`/`ack` rows without text inflate the list. | Use the API (`from`/`to`, `cursor`) or the NDJSON export. |
| DASH-1 | Medium · UX | Dashboards and Reports show only "No entries." + **Create** on a real-data instance; no starter dashboard or templates (demo mode seeds `demo-overview`). | |
| FORM-1 | Medium · UX | In the create-object dialog the active tab is not visibly highlighted, and the required **Name** field is outlined red without an error text before you type. | |
| OBJ-1 | Medium · UX | The objects list mixes hosts and services flat; the folder is only a text column; no grouping by host/folder or collapsing. Full tree grouping conflicts with the row virtualiser. | |
| OBJ-2 | Medium · UX | Two side-by-side inputs ("Filter (e.g. env=prod)" and "Full text…") are disambiguated only by icon and tooltip, not by labels. | [Objects](/docs/ui/objects/#filters) |
| UX-1 | Medium · Enhancement | Alert **snooze** has no UI — only acknowledge and resolve (`:snooze` is API-only). | [Acknowledge and snooze](/docs/alarming/acknowledge-and-snooze/) |
| DENSITY-1 | Low · UX | Object detail: perfdata meters are wide and mostly empty for values like `rta 0.2ms`; "Last hard change —" stays empty; the Interval & Scheduling card is sparse while Metrics/Services scroll. | |
| I18N-1 | Low · UX | Mixed German/English in the German UI ("Wallboard", "Business Services", "Reports", "Discovery", "Alle Severities", "Start"); there is no language switcher (browser language only) and the server-rendered pages are German only. | [Navigation](/docs/ui/navigation/#language) |
| A11Y-1 | Low · UX | The service donut and row states rely primarily on green/amber/red; text labels are small and grey — hard for colour-blind users. | |
| UX-2 | Low · Enhancement | Non-default public status pages (`/status/<slug>`) read a `statuspage/<slug>` KV document that no UI or API writes; only `/status/default` is usable. | |
| UX-3 | Low · Enhancement | The counters (KPI) widget cannot be scoped to a selector — it needs an `/overview` selector parameter on the backend. Per-field origin in the effective-config table is not exposed by the API. | |

### July 2026 audit — status

All July items were worked through on 2026-07-15/16; the August audit confirmed the most important one.

| July ID | Item | Status |
|---|---|---|
| DETAIL-1 | Effective config leaked the agent token in plaintext | **Fixed** — `--token`, `--password`, `--community`, API keys are redacted to `•••`; the August audit verified that `/effective-config` returns no tokens. |
| FORM-1/2/3/5 | One 2400 px single-column form; dual-list pickers two-up; no progressive disclosure; no sticky header/footer | **Fixed** — four-tab dialog (Basics · Check · Notifications · Advanced) with pinned header/footer and compact chip pickers. August FORM-1 (active-tab highlight, validation text) is the remaining polish. |
| FORM-4, FORM-6 | Label collision in the interval grid; redundant field for `passive` | **Fixed** — labels wrap, "Retry interval"; passive shows a single selector + helper. |
| NAV-1 | Admin tab bar clipped | **Partially** — the strip scrolls; the August audit still flags discoverability (see above). |
| DASH-1/2/3/4/6 | No data binding; raw counter as 100 % bar; blind add flow; dead space; plural/titles | **Fixed** — data-source pickers with live preview, humanised values (`18.5M`), icon gallery, **Tidy** re-flow, plural fix. |
| DASH-5 | KPI-tiles widget duplicates the Overview | **Partially** — a scoped `stat` widget was added; the counters widget itself is still global (UX-3). |
| DETAIL-2/3/4/5 | Metrics buried; raw JSON dump; no host/sibling navigation; low density | **Fixed** — Overview/History/Configuration tabs, key/value effective config with template chain and Raw JSON toggle, host chip + sibling services card, perfdata meters, coloured history badges. |
| LIST-1, LIST-2 | Flat list without folders; ambiguous search boxes | **Partially** — column headers and a folder column, icons + tooltips on the inputs; grouping and labels remain (OBJ-1, OBJ-2). |
| OVW-1 | Overview underused the screen | **Fixed** — "Recent events" feed; the August audit lists the Overview under "what works well". |

Further fixes from the same pass worth knowing: report preview uses `POST …:render` (was a 404), object detail shows a real 404 for bad ids, the wallboard clock ticks, the Alerting heading follows the tab, a host cannot be its own parent, unbounded counters never render as a full bar, UNKNOWN has its own badge, scheduled reports without recipients warn, friendlier empty states, SNMP `sysUpTime` humanised, monotonic counters charted as rates, `last_seen_at` stamped on session activity, SNMP community strings masked.

## Deployment and operations

| ID | Severity | Description | Notes |
|---|---|---|---|
| DEP-1 | High · Bug | `np-02` (Hetzner standalone box, 91.98.92.10) no longer exists: TCP/22 times out and the IP serves a parking page. The `deploy-hetzner` job fails at "Ship compose stack" on every Deploy run; `HETZNER_HOST`/`HETZNER_KNOWN_HOSTS` point at a stranger's host. | A red Deploy run does **not** mean production missed the rollout — `deploy` (np-01) is independent and green. Re-provision a new box and rotate the `HETZNER_*` variables/secrets, or remove the job. [Environments](/docs/deployment/environments/), [Provisioning](/docs/deployment/provisioning/) |
| DEP-2 | Low · Doc | The default-admin seeding closes `/setup` on a bare default install (the server seeds `admin@localhost` with a generated password); the root `docker-compose.yml` sets `NP_DEFAULT_ADMIN_DISABLED` so `/setup` works there. | Read the generated password from the logs, set `NP_DEFAULT_ADMIN_EMAIL/PASSWORD`, or set `NP_DEFAULT_ADMIN_DISABLED=1` to use `/setup`. [Authentication](/docs/administration/authentication/) |
| DEP-3 | Medium · Enhancement | No periodic backup: `backup.interval` is parsed but no scheduler uses it; only `northplaned backup` on demand. No vzdump job on the production Proxmox host. | Back up `secret.key` and the data volume yourself. [Operations](/docs/deployment/operations/) |
| DEP-4 | Low · Doc | Fixed: `northplaned init` now creates the service user, installs the unit into `/etc/systemd/system` and no longer sets `WatchdogSec`. Kept for the record. | [Installation](/docs/getting-started/installation/#set-up-as-a-service-with-northplaned-init) |
| DEP-5 | Low · Doc | The `TLSConfig` comment mentions autocert, but no ACME implementation exists — TLS termination in production is Caddy's job. | [TLS and proxy](/docs/administration/tls-and-proxy/) |
| DEP-6 | Low · Doc | The production Compose stack does not publish 9162/udp (traps), 2023 (ESPA), 8123 (ESPA-X) or 4573 (FastAGI); those inputs are unavailable on `np-01` until the ports are added. | [Proxmox VM](/docs/deployment/proxmox-vm/) |
| DEP-7 | Low · Doc | Cloudflare in front of doktrace.com blocks Python user agents (HTTP 403) — scripts need a curl-like `User-Agent`. | |
| DEP-8 | Low · Doc | `/register` is publicly enabled on production (`NORTHPLANE_ALLOW_SIGNUP=true`, creates viewers). | Intentional for the showcase; turn off for private instances. |

## Development and documentation

| ID | Severity | Description | Notes |
|---|---|---|---|
| DEV-1 | Medium · Doc | `internal/web/dist` is committed and stale at HEAD (last rebuilt several commits ago, no Stept snippet, old favicon). CI and the Dockerfile rebuild it, but a local `go build` without `make web` embeds the stale UI. | Run `make web` (or `make all`) before a local build. [Frontend](/docs/development/frontend/) |
| DEV-2 | Low · Doc | Code comments reference SPEC/ADR documents (§7.7, §12.4 …) that are not in the repository. | Treat them as design notes. [Backend](/docs/development/backend/) |
| DEV-3 | Low · Doc | Unused leftovers: `next-themes` and the `sonner` Toaster wrapper are declared but not used; `web/public/icons.svg`, `src/assets/hero.png`, `vite.svg` are unreferenced; a comment in `Admin.tsx` still says "19 tabs". | |
| DEV-4 | Low · Doc | The CSP allow-lists the Stept bootstrap script by SHA-256 hash; changing its bytes (new workspace key) requires recomputing the hash. | [Frontend](/docs/development/frontend/) |

## What works well

The August audit recorded these deliberately, and the production checks behind [Environments](/docs/deployment/environments/) confirm the platform side:

- **Overview** is dense and useful: KPI tiles, problem list, service donut and "On call now" with name and number live from the schedule.
- **On-Call**: rotation timeline, hours per person and overrides behaved correctly in a live test — an override visibly rerouted the alarm.
- **IVR menus, channels and escalations** are clear tables with **Send test**/**Edit**; the configuration verified in the alarm test is findable one-to-one in the UI.
- **Object detail**: Overview/History/Configuration tabs, real metric charts with warn/crit bands, linked child services.
- **Server-side tenant isolation** holds (a write attempt against another tenant returns `403`); the July token leak is fixed.
- **Production** (`np-01`, doktrace.com): `healthz`/`readyz` green, the current `main` image deployed by CI, an agent fleet reporting, real Twilio SMS, ntfy and e-mail channels exercised end-to-end.

## Suggested order

1. Quick bugs: AGENT-1 (install URL), RBAC-2 (suppress 403 requests), WIDGET-1 (widget), NAV-1 (tab overflow), NTF-7 (SNMPv3 keys), AI-2 (MCP preset scopes), DEP-1 (repoint or remove `deploy-hetzner`).
2. RBAC visibility (RBAC-1) — align Admin tabs and action buttons with effective permissions.
3. Core-feature empty states: DASH-1, then EVENT-1 (server-side filters and pagination in the UI).
4. Engine gaps with user impact: ALM-1 (groups), ALM-3 (persist re-arm/pending), CHK-1 (check periods), CHK-5 (downtime exclusion in SLA math), NTF-6 (web push flow), UX-1 (snooze in UI).
5. Polish: FORM-1, OBJ-1/2, DENSITY-1, I18N-1, A11Y-1, TENANT-1 and the Low items above.
