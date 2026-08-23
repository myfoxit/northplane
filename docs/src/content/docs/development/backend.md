---
title: Backend
description: A walkthrough of the Go code — process start-up and server wiring, the API route registry and OpenAPI generation, storage and migrations, the check pipeline, alarming packages, and recipes for adding a resource kind, a builtin check or a channel type.
sidebar:
  order: 3
---

The backend is one Go module, one process, one binary. Everything the product can do exists first as a REST route under `/api/v1/`; the SPA, the `np` CLI, the AI agent and the MCP server are all clients of those routes with the same RBAC. This page explains how the code is put together and where to add things. For the operator-facing view of the same parts see [Architecture](/docs/concepts/architecture/).

## Process start-up

`cmd/northplaned/main.go` dispatches on `os.Args[1]`; no argument means `serve`. The `serve` path:

1. `config.Load(path)` — defaults → `config.yaml` (unknown keys are an error) → `NORTHPLANE_*` environment → `Validate()`. Then the logger (`slog`, JSON by default, text with `logFormat: text`). Reference: [Configuration](/docs/administration/configuration/).
2. `signal.NotifyContext` for SIGINT/SIGTERM.
3. `storage.Open` — connects (SQLite or PostgreSQL), sets WAL, **runs pending migrations**, opens the event store.
4. `tsdb.Open` at `dataDir/tsdb` with the built-in retention.
5. Demo seeding (`--demo`, or `demo: true` guarded by the real-data check) and the break-glass default admin (`seedDefaultAdmin`).
6. `server.New(...)` wires every subsystem; `srv.Run(ctx)` binds the listener, starts the workers and blocks until the context is cancelled. Shutdown has a 30 s budget for the HTTP server and the workers.

The other subcommands (`init`, `migrate`, `storage migrate`, `import nagios`, `backup`, `mcp`, `openapi`, `bootstrap-admin`) reuse the same packages without starting the HTTP server; their behaviour is documented in the [northplaned CLI reference](/docs/reference/cli-northplaned/).

## Server wiring (`internal/server`)

`server.New` constructs the subsystems in dependency order: event bus → catalog (`LoadAll`) → metrics registry → secret box + `auth.SecretsResolver` → scheduler → executor → pipeline → escalation engine → alerting engine (`ReloadAll`) → correlator → notifier (base URL, secrets, ack-link HMAC secret) → VAPID keys → SNMP trap, IMAP, MQTT and ESPA ingress managers → system-role reconcile → SSE hub → authenticator → OIDC (only with `oidc.issuer`) → LDAP syncer → the `api.API` aggregate → federation edge (edge mode only) → AI service → the API HTTP handler → FastAGI manager → `http.Server`.

### Root mux

| Pattern | Handler |
|---|---|
| `/api/` | the API handler (`api.New`): `/api/v1/...`, `GET /api/openapi.json`, `GET /api/docs` (Swagger UI) |
| `/metrics`, `/healthz`, `/readyz` | the API handler, unauthenticated |
| `/auth/`, `/status/`, `/login`, `/setup`, `/register` | server-rendered pages (`internal/web`) |
| `/mcp` | MCP Streamable HTTP, mounted only when the AI service is a `*ai.Service` |
| `/docs/` | the embedded documentation (`internal/docs`), public |
| `/` | `web.GateSPA(web.SPAHandler)` — the embedded SPA; logged-out document navigations are redirected to `/login` |

The mux is wrapped as `securityHeaders(withTimeouts(mux), trustProxy)`: every response gets `X-Content-Type-Options`, `X-Frame-Options: DENY`, `Referrer-Policy`, HSTS when the request is HTTPS, and a Content-Security-Policy for non-`/api/` paths (the docs handler sets its own). `withTimeouts` applies a 30 s `http.TimeoutHandler` to everything except the streaming paths `/api/v1/stream`, `/api/v1/events:export`, `/api/v1/ai/chat` and `/mcp`. The `http.Server` itself has `ReadHeaderTimeout` 10 s, `ReadTimeout` 60 s, `IdleTimeout` 120 s, `MaxHeaderBytes` 1 MiB and deliberately **no** `WriteTimeout` (it would cut SSE and MCP streams). TLS policy: a cert/key pair, or plaintext only on a loopback listener, behind `trustProxy`, or with `tls.insecure` — see [TLS and proxying](/docs/administration/tls-and-proxy/).

### Background workers

`Run` pushes all catalog entries into the scheduler and starts each worker under `superviseWorker`: a panic is recovered, logged as `server: background worker panicked; restarting`, and the worker restarts after 1 s unless the process is shutting down. Workers (by logged name): `scheduler`, `executor`, `pipeline`, `alerting`, `correlator`, `escalation`, `notify`, `traps`, `mailin`, `mqttin`, `espa`, `agi`, `api-janitor`, `webhook-dispatcher`, `report-scheduler`, `dead-man`; conditionally `ldap-sync`, `federation-edge`, `ai`. The janitor runs downtime depth refresh every 30 s, session/idempotency cleanup every 10 min and the nightly TSDB maintenance + event retention between 02:00 and 03:59 local time. See [Observability](/docs/administration/observability/) for what operators see of this.

## The API package (`internal/api`)

### Route registration

Every route is registered through one helper, which both installs the HTTP handler and records metadata for the OpenAPI document:

```go
a.handle("POST /api/v1/alerts/{id}:ack", "Acknowledge an alert", "alerts:ack",
    ackRequest{}, model.Alert{},
    func(w http.ResponseWriter, r *http.Request, p *auth.Principal) { … })
```

`handle(pattern, summary, perm, req, resp, handler)`:

- `pattern` is Go 1.22 `ServeMux` syntax (`METHOD /path/{param}`) plus the `:action` extension. Because `ServeMux` forbids text after a wildcard, `{id}:ack` is registered as one mux pattern per `(method, parent)` — `POST /api/v1/alerts/{__seg}` — behind an `actionRouter` that splits the last segment at the last `:` and dispatches by suffix, with the plain `{id}` route as fallback. Literal `:verb` segments without a wildcard (`POST /api/v1/objects:batch`, `GET /api/v1/events:export`) are registered verbatim.
- `perm` is the required permission (`model.Permission`, `"resource:action"`); `""` means no RBAC check (the handler may still require `p != nil`).
- `req` / `resp` are prototype values whose Go types are reflected into the spec (`nil` = no body / no typed response). Use `listResponse{}` as `resp` for list endpoints — that is what makes the spec add `cursor` and `limit`.
- The returned `*routeBuilder` documents extras fluently: `.Query(oaParam{Name, Desc, Type})` for query parameters, `.Status(202)` to override the documented success code.

The per-route wrapper runs, in order: CSRF check (a cookie session plus `Sec-Fetch-Site: cross-site` → 403 `np:auth/csrf`) → authentication required when `perm != ""` (401 `np:auth/required`) → RBAC (`p.Allow(perm)`, else 403 `np:auth/forbidden` with the missing permission in `detail`) → your handler. Around the mux, `withMiddleware` sets `X-Request-Id` (a UUIDv7, also stored in the context for audit rows), recovers panics into 500 `np:internal`, authenticates (`Authorization: Bearer np_…` or the `np_session` cookie; an invalid credential is 401 `np:auth/invalid` on every path), and records `np_http_requests_total` / `np_http_request_duration_seconds` labelled with the matched mux pattern.

`registerAll()` calls one `register<Group>()` per file: objects, alerts, rules, maintenance, on-call, contacts, events, metrics query, ingress, bundles, business, reports/dashboards, admin, AI, AI chat, system, OpenAPI, discovery, webhook subscriptions, telephony, directory, sites, agent config. A handful of routes bypass `handle` on purpose (health, metrics, ingest, ack links, telephony callbacks, Swagger assets) — they have their own authentication and do **not** appear in the spec; the full list is in the [API overview](/docs/reference/api-overview/).

### Helpers every handler uses

| Helper | Does |
|---|---|
| `a.problem(w, r, status, code, title, detail)` | writes an RFC 9457 `application/problem+json` body; `type` = `https://northplane.dev/problems/` + code with `:` → `/`, `instance` = request path |
| `a.fail(w, r, err)` | maps `storage.ErrNotFound` → 404 `np:not-found`, `ErrConflict` → 409 `np:conflict/version`, `ErrDuplicate` → 409 `np:conflict/duplicate`, a `validationErr` → 422 `np:validation/<code>`, anything else → logged 500 `np:internal` |
| `a.validationError(w, r, code, detail)` | 422 `np:validation/<code>` |
| `a.decode(w, r, &dst)` | JSON body through `http.MaxBytesReader` (1 MiB); decode failure → 422 `np:validation/body` |
| `a.writeJSON(w, status, v)` / `a.writeList(w, items, nextCursor)` | JSON with `SetEscapeHTML(false)`; the list envelope `{"items": [...], "nextCursor": "…"}` |
| `a.requireIfMatch(w, r)` | parses `If-Match` (`W/` and quotes stripped); missing → 428 `np:precondition/if-match` |
| `a.idempotent(w, r, p, …)` | `Idempotency-Key` replay cache (used by `POST /downtimes` only) |
| `a.audit(r, p, action, resource, before, after)` | appends to the hash-chained audit log under the acted-on tenant |
| `a.tenantOf(r, p)` | `X-Northplane-Tenant` when the principal holds `admin:tenants`, else the principal's tenant |
| `a.configChanged(ctx, tenant, kinds…)` / `a.objectChanged` / `a.objectRemoved` | propagate a mutation: emit a `config` event, recompile alert rules, reload the tenant catalog + reschedule for catalog-affecting kinds (templates, check commands, time periods, bulk object paths), or incrementally upsert/remove one object in catalog, scheduler and pipeline |
| `a.resourceCRUD(path, kind, permPrefix, proto)` | stamps the five standard routes for a config-document kind (below) |

### Generic resource CRUD

Most configuration kinds are JSON documents in the `resources` table and share one implementation: `a.resourceCRUD("templates", storage.KindTemplate, "config", model.Template{})` registers

```text
GET    /api/v1/<path>              list (q, cursor, limit ≤ 2000, default 500)   <prefix>:read
POST   /api/v1/<path>              create-only; duplicate name → 409               <prefix>:write
GET    /api/v1/<path>/{name}       by name or id; ETag                              <prefix>:read
PUT    /api/v1/<path>/{name}       If-Match required; ETag                          <prefix>:write
DELETE /api/v1/<path>/{name}       204                                              <prefix>:write
```

`permPrefix == "config"` is special-cased to read `objects:read` / write `config:write`; `oncall` (contacts, contact groups, schedules) and `admin` (roles) use `<prefix>:read/write`. Every mutation is audited as `<kind>.create|update|delete`, emits a `config` event and goes through `validateResourceDoc(kind, doc)` — the switch where kind-specific checks live (alert rules compile their CEL, escalation policies need a step, channels need a `type`, …).

## OpenAPI generation

The route registry is the single source of the OpenAPI 3.1 document; there is no hand-written spec. `buildOpenAPI()` sorts the routes by pattern and method and emits per operation:

| Field | Derived from |
|---|---|
| `summary`, `tags` | the `summary` argument; `tagOf(path)` = first segment after `/api/v1/` (`objects`, `alerts`, … or `system`) |
| `operationId` | lower-cased method + pattern with `/api/v1/` → `_`, `/` → `_`, braces removed, `:` and `-` → `_`, trimmed — `GET /api/v1/objects/{id}/effective-config` → `get_objects_id_effective_config`, `POST /api/v1/config/bundles:apply` → `post_config_bundles_apply` |
| `security` + `x-required-permission` | present when `perm != ""`; the document-level `bearerToken` scheme covers the rest |
| path parameters | every `{segment}`, type string, required |
| query parameters | `.Query(...)` declarations plus automatic `cursor`/`limit` when `resp` is `listResponse{}` |
| `requestBody` / `responses` | reflected from `req` / `resp`; `default` is always the `problemDoc` schema |
| success status | explicit `.Status()`; else DELETE → 204, POST to a pure collection (no `{param}`, no `:`) → 201, other POST → 200, everything else → 200 |

Schema reflection (`schemaRef`): named structs become `components/schemas/<TypeName>`, anonymous structs are inlined, `time.Time` → `string`/`date-time`, `[]byte` → `string`/`byte`, maps → `additionalProperties`, embedded structs are flattened, `json:"-"` is skipped, and a field is `required` when it is neither `omitempty` nor a pointer. There are no descriptions or enums on properties — reflection only — so if you want a value documented, put it in the route `summary` or the [API overview](/docs/reference/api-overview/).

The document is served at `GET /api/openapi.json` (built once, cached, `Cache-Control: no-cache`) and rendered by the vendored Swagger UI at `GET /api/docs` (`//go:embed swaggerui`, works air-gapped, `withCredentials` so a logged-in operator can "try it out"). `api.OpenAPIDocument(version)` builds the same document against nil dependencies — that is what `northplaned openapi` prints and what `make types` consumes to regenerate `web/src/types.gen.ts` and `docs/src/assets/openapi.json`; CI's `types` job fails on drift (see [Frontend](/docs/development/frontend/) and [Documentation](/docs/development/documentation/)). Tests pin the generator: `TestOpenAPIQueryParams`, `TestOpenAPIStatusCodes`, `TestDocsServeSwaggerUI`, and the server e2e asserts `"openapi": "3.1.0"`.

:::tip[After changing a route or a JSON-tagged struct]
Run `make types` and commit the regenerated `web/src/types.gen.ts` **and** `docs/src/assets/openapi.json`. A changed response shape also has to keep `web/src/types.gen.conformance.ts` compiling.
:::

## Storage (`internal/storage`)

One logical schema, two first-class backends behind a narrow `database/sql` interface:

| | SQLite (default) | PostgreSQL |
|---|---|---|
| Driver | `modernc.org/sqlite` (pure Go) | `github.com/jackc/pgx/v5/stdlib` |
| Selected by | `storage.dsn` empty (→ `dataDir/core.db`) or a file path | `postgres://` / `postgresql://` DSN |
| DDL placeholders (`dialect.DDL`) | `{{TIMESTAMP}}`→`TEXT`, `{{JSON}}`→`TEXT`, `{{BOOL}}`→`INTEGER`, `{{BLOB}}`→`BLOB`, `{{BIGINT}}`→`INTEGER`, `{{PK_AUTO}}`→`INTEGER PRIMARY KEY AUTOINCREMENT` | `timestamptz`, `jsonb`, `boolean`, `bytea`, `bigint`, `BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY` |
| Placeholders | `?` (`s.Q()` rebinds for PG → `$n`) | |
| Writes | serialised through one in-process mutex (`Store.Write`) + transaction | server-side |
| Duplicate detection | error text `UNIQUE constraint failed` | SQLSTATE `23505` |

DML stays in the shared subset (`ON CONFLICT`, partial indexes, CTEs) so a statement runs unchanged on both — the storage tests enforce that through the matrix described in [Testing](/docs/development/testing/).

### Migrations

`migrations` in `internal/storage/schema.go` is a forward-only list of `{version, name, sql []string}`; `storage.Open` applies every entry above `MAX(schema_version.version)` in its own transaction and records it. There are nine so far: `core`, `seed` (default tenant + built-in roles, code-driven), `user_roles`, `report_archive_slot`, `alert_ticket`, `hotpath_indices`, `user_tenant`, `ai_agent_chat`, `alert_snooze`.

To add one: append `{10, "<name>", []string{ … }}` using the dialect placeholders above, never edit an applied entry, keep it additive where you can (operators upgrade by swapping the binary, see [Upgrades](/docs/administration/upgrades/)), and run the storage suite on both backends. `northplaned migrate` is the explicit "apply and exit" command; every other subcommand that opens the store applies pending migrations too.

### What lives where

- **Dedicated tables** for hot-path entities: `objects` + `object_labels`, `check_state`, `alerts`, `incidents`, `downtimes`, `silences`, `heartbeats`, `api_tokens`, `users`, `tenants`, `sessions`, `secrets`, `idempotency`, `escalations`, `outbox`, `audit_log`, `ai_*`, `push_subscriptions`, `report_archive`, `kv`.
- **`resources`** — the generic config-document table `(id, tenant_id, kind, name, doc JSON, version)`, `UNIQUE (tenant_id, kind, name)`. Kinds are the `Kind*` constants in `internal/storage/resources.go` (`template`, `check-command`, `time-period`, `alert-rule`, `alert-group`, `escalation-policy`, `schedule`, `override`, `contact`, `contact-group`, `channel`, `event-source`, `business-service`, `dashboard`, `report`, `role`, `webhook-subscription`, `saved-filter`, `static-group`, `preference`, `site`, `ivr-menu`, `branding`). `PutResource(tenant, kind, name, doc, expectVersion)` implements create-only (`-1`), unconditional upsert (`0`) and optimistic update (`>0`, enforced atomically in SQL); the store injects `id`, `tenantId`, `name`, `version`, `createdAt`, `updatedAt`; `ResolveResource` accepts name **or** id. The full kind table with REST paths, bundle kinds and permissions is in [Object model](/docs/concepts/object-model/) and [Config bundles](/docs/administration/config-bundles/).
- **Events** — not in `core.db`: SQLite keeps one segment file per month (`events-YYYYMM.db`), PostgreSQL a range-partitioned parent with `events_YYYYMM` children; queries fan out across segments and merge in Go. Retention drops whole segments/partitions.
- **Outbox** — the notification queue with lease-based claiming, exponential backoff and the `dead` flag (dead letters); **escalations** — durable timers; **audit_log** — a SHA-256 hash chain verified by `POST /api/v1/audit:verify`.

## Checks: catalog → scheduler → executor → pipeline

```text
catalog.Entry ──► scheduler (timing wheel, splay) ──► executor (builtin | exec | freshness probe)
                                                           │
                      TSDB ◄── pipeline ◄── model.CheckResult
                                 │
                     statemachine (soft/hard, flapping) ──► state_change / flapping_* events ──► alerting
```

- **Catalog** (`internal/catalog`) holds every object with its resolved effective spec, the template chain, the command class (`builtin` / `exec` / `agent` / `passive`), argv and macro arguments — loaded once and updated incrementally, so the hot path never queries SQL. `ParseCommandRef` in `internal/model` decides the class from `checkCommand` (`builtin:<name>`, `exec:<plugin>`, `agent:exec:<plugin>`, `passive`, or a named `CheckCommand` resource).
- **Scheduler** (`internal/scheduler`) is an 86 400-slot timing wheel ticked every 250 ms. Interval = effective `interval` clamped to 1 s … 24 h; the start offset is `FNV-64a(objectID) mod interval`, so restarts do not cause check storms; the next run is planned from the previous planned time (drift-free). `CheckNow` pushes on a priority lane. Passive/agent objects with `stalenessAfter` are scheduled as freshness probes instead.
- **Executor** (`internal/executor`) runs builtin checks in-process under a semaphore (`BuiltinPoolSize` 1024) and `exec:` plugins in a bounded pool (`execPoolSize`, default `min(256, 32×NumCPU)`) — argv only, never a shell; plugins resolve under `pluginsDir`, optionally restricted by `pluginsAllow`; a fixed minimal environment; stdout capped at 64 KiB; the whole process group is killed on timeout. Every check goroutine recovers panics into an `UNKNOWN` result.
- **Pipeline** (`internal/pipeline`) batches results (250 ms / 500), maps host results (OK/WARNING → UP, CRITICAL/UNKNOWN → DOWN, DOWN with all parents down → UNREACHABLE), forces passive/agent results hard, schedules retries after soft transitions, clears sticky acks on hard recovery, emits `state_change` / `flapping_start` / `flapping_end` events, cascades reachability re-checks and appends perfdata series to the TSDB.
- **State machine** (`internal/statemachine`) is a pure transition function: `maxCheckAttempts` soft/hard logic, immediate hard recovery, weighted flapping over the last 21 checks (start ≥ 50 %, stop < 25 %), staleness input.

The user-facing semantics are in [Checks and states](/docs/concepts/checks-and-states/); every builtin flag is in [Built-in checks](/docs/monitoring/builtin-checks/).

## Alarming: alerting → escalation → notify

- **`internal/alerting`** subscribes to the event bus: CEL rules (`match`) with `pendingFor`, `dedupKey`, `autoClose`, `setLabels`, heartbeat rules; the correlator groups alerts into incidents; `Suppressed`/`reEvaluateSuppressed` implement downtime/silence/flapping/dependency suppression and re-arm; the engine loop also runs auto-close and snooze wake-ups. Rules are recompiled on every `alert-rule` mutation (`configChanged`).
- **`internal/escalation`** persists step timers in `escalations` so chains survive restarts; steps fire `unlessAcked`, repeats are bounded, an ack cancels the chain, a snooze restarts from step 0; each step enqueues outbox items per contact × channel type.
- **`internal/notify`** is the outbox worker: it claims due rows with a 2-minute lease, renders Go templates (FuncMap whitelist), dispatches by channel type through the `senders` registry, retries with `base · 2^min(attempt,12)` (30 s base, 1 h cap, ±10 % jitter) up to 30 attempts and then marks the row dead; `channelFor(tenant, type)` picks the alphabetically first **enabled** channel of a type. Operator-level detail: [Escalation policies](/docs/alarming/escalation-policies/), [Channels](/docs/alarming/channels/), [Reliability](/docs/alarming/reliability/).

## Recipes

### Adding a resource kind with `np-gen`

`np-gen new-resource <Name>` stamps the mechanical boilerplate for a new configuration resource from one name and prints the manual wiring steps. It derives every casing from the name (`ContactGroup` → `contactGroup`, `contact-group`, `contact-groups`, `contact_group`, `KindContactGroup`) and writes five files, refusing to overwrite without `--force`:

```bash
go run ./cmd/np-gen new-resource Widget --dry-run   # plan only
go run ./cmd/np-gen new-resource Widget             # write into the tree
go run ./cmd/np-gen new-resource Widget --out /tmp/np-gen   # flat copies, tree untouched
```

| File | Content |
|---|---|
| `internal/model/gen_widget.go` | `type Widget struct` with envelope fields (`ID`, `TenantID`, `Name`, `Version`, `CreatedAt`, `UpdatedAt`) and placeholder domain fields (`Enabled`, `Notes`) |
| `internal/storage/gen_widget_kind.go` | `const KindWidget = "widget"` (fold it into the const block in `resources.go` when you like) |
| `internal/api/gen_widget.go` | `func (a *API) registerWidgets()` calling `a.resourceCRUD("widgets", storage.KindWidget, "config", model.Widget{})` |
| `web/src/types/gen_widget.ts` | a TypeScript interface to move into `web/src/types.ts` |
| `web/src/pages/Widgets.tsx` | a table + create/edit dialog page over `resourceApi('widgets')`, mirroring `Templates.tsx` |

Then wire it by hand — the generator deliberately does not edit hand-maintained registries:

1. `a.registerWidgets()` in `registerAll()` (`internal/api/api.go`).
2. Bundle support: add `"Widget"` to `bundle.KindOrder` (`internal/bundle/bundle.go`) **and** the mapping `"Widget": storage.KindWidget` to `bundleKindToStorage` in `internal/api/bundles.go` — plan warns `unsupported kind` and apply/export skip kinds missing from that map.
3. Route + navigation: a `createRoute` entry in `web/src/main.tsx` and a sidebar item in `web/src/components/Layout.tsx` plus `de`/`en` labels in `web/src/i18n.ts`.
4. Move the interface from `web/src/types/gen_widget.ts` into `web/src/types.ts`, delete the stub, replace the placeholder fields in the Go struct and the page form.
5. Optional: a `validateResourceDoc` case in `internal/api/objects.go` and a Go test next to `resources_test.go`.
6. `make types` (the new routes change the spec), then `go build ./...` and `cd web && npm run build`.

:::caution[One stale checklist item]
The printed checklist still asks you to add an "SSE invalidation key" to an `invalidations` map in `web/src/api.ts`. That map no longer exists — the UI moved from an SSE stream to interval polling, and React Query invalidation is done per mutation through `useSave({ invalidate })`. Skip that step.
:::

### Adding a builtin check

Builtin checks live in `internal/checks` and self-register in an `init()`:

```go
func init() {
    register("tcp", checkTCP)
}

// checkTCP: connect, optional send/expect, optional TLS.
func checkTCP(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
    host := a.Host(t)                         // -H/--hostname/--host beats the object address
    port := a.Int(0, "p", "port")
    if port == 0 {
        return unknownf("tcp: -p port required")
    }
    timeout := a.Duration(10*time.Second, "t", "timeout")
    // … dial, measure elapsed …
    return evalPerf("time", elapsed, "s", a.Get("w", "warning"), a.Get("c", "critical"),
        fmt.Sprintf("connected to %s in %.3fs", addr, elapsed))
}
```

What you get for free: `Args` parses monitoring-plugins style flags (`-p 80`, `--port=80`, `-p80`, repeated flags last-wins; `Get` takes alias lists, `Bool`, `Int`, `Duration` accept `5s`/`500ms`/bare seconds); `evalPerf` grades a value against Nagios ranges (`nagios.Evaluate`), renders `NAME STATE - text | label=value;warn;crit;;` and parses the perfdata; `unknownf`/`criticalf` build the standard texts. Respect `ctx` — the executor bounds every check by the object's effective `timeout`. Once registered the name is valid as `builtin:<name>`, appears in `GET /api/v1/check-commands:builtins` (see [`get_check_commands_builtins`](/docs/reference/api/operations/get_check_commands_builtins/)) and can be tried through `POST /api/v1/check-commands:test` ([`post_check_commands_test`](/docs/reference/api/operations/post_check_commands_test/)). Document the flags in [Built-in checks](/docs/monitoring/builtin-checks/) and add a test next to the existing ones in `internal/checks`.

### Adding a channel type

1. **Model** — add the constant to `ChannelType` in `internal/model/notify.go` (and to `IsTicket()` if it creates tickets).
2. **Sender** — implement a `SenderFunc` in `internal/notify` and add it to the `senders` map in `channels.go` (or call `notify.RegisterSender(type, fn)` from an `init()`): `func(m *Manager, ctx, ch *model.NotificationChannel, target, subject, body string, rc *RenderContext) (providerID string, err error)`. Use `m.hookClient` for outbound HTTP (it refuses link-local/cloud-metadata targets) and resolve secrets through the `$SECRET:name$` resolver as the existing senders do. Return an error for retry; the outbox owns backoff and dead-lettering.
3. **Target rule** — `targetFor` in `notify.go` decides what `target` is per type (e-mail address, phone, user id, or `config["url"]`); add a case if the new type needs something else.
4. **UI** — add the type to `CHANNEL_TYPES` and a field spec for its config keys in `web/src/components/admin/Channels.tsx`; add labels to `web/src/i18n.ts`.
5. **Docs and tests** — a table in [Channels](/docs/alarming/channels/), a unit test in `internal/notify` (the existing tests use `SendHook` to intercept deliveries), and a `:test-notification` run against a real endpoint.

Remember the `enabled` gotcha: a channel created through the API or a bundle without `enabled: true` is disabled — the Go zero value — and `channelFor` skips it.

## Conventions

| Convention | Where it is enforced |
|---|---|
| API-first: every capability is a route; UI/CLI/AI/MCP are clients | `internal/api` is the only functional surface; `mcp` and `ai` call the same store/engines with the caller's permissions |
| Errors are RFC 9457 problems with a stable `code` (`np:area/detail`) | `a.problem`, `a.fail`; catalog in the [API overview](/docs/reference/api-overview/) |
| Identifiers are UUIDv7 | `model.NewID()`; time-sortable, used as pagination cursors and SSE resume points |
| Optimistic concurrency: `version` + `ETag` + `If-Match` (428 when missing, 409 when stale) | `requireIfMatch`, `PutResource(expectVersion)`, `UpdateObject` |
| Timestamps RFC 3339 UTC (ms precision), durations as Go strings (`"90s"`, bare integer = seconds on input) | `model.Now()`, `model.Duration` |
| Permissions are `resource:action` with `*` wildcards; `Implies` matches resource and action independently | `model.Permission`, `auth.Principal.Allow`; port in `web/src/permissions.ts` |
| Every mutation is audited (`<kind>.<verb>`) and emits an event | `a.audit`, `emitConfigEvent`; hash chain in `storage/audit.go` |
| Tenant scoping via `tenantOf`; a few documented exceptions | [Tenancy and RBAC](/docs/concepts/tenancy-rbac/) |
| Secrets never in config docs: `$SECRET:name$` references resolved at send time | `auth.SecretsResolver`, `notify` |
| No shell, no CGO, no third-party router | `executor` (argv + process groups), `modernc.org/sqlite`, stdlib `ServeMux` |

## SPEC, ADR and requirement references in comments

You will see comments such as `SPEC §7.4`, `ADR-10`, `F-04.04`, `P1` and `A-15.10` throughout the Go code. They cite the Northplane system specification (sections), its architecture decision records, functional requirements and principles — design documents that were removed from the repository tree and are not part of this documentation. The ones you will meet most often:

| Reference | Meaning (as used in code) |
|---|---|
| `ADR-02` | dual storage backend: SQLite (modernc) + PostgreSQL (pgx), one schema, dialect-generated DDL |
| `ADR-04` | SSE realtime hub with `Last-Event-ID` resume |
| `ADR-06` | sandboxed CEL environment for rules (no I/O) |
| `ADR-09` | UUIDv7 identifiers everywhere |
| `ADR-10` | OpenAPI generated from the route registry; typed codegen drift gate |
| `ADR-11` | PDF reports via a Chromium sidecar (returns 501 until configured) |
| `ADR-12` | Web Push / VAPID (and FCM/APNs) for the `push` channel |
| `ADR-13` | append-only, time-partitioned events |
| `P1` … `P5` | principles as cited: `P1` API-first (UI, CLI, MCP and AI are clients of the REST routes), `P2` the AI stays a privilege-less, audited API client, `P3`/`P5` no framework where a thin client suffices (the AI provider layer has "no LangChain-style framework") |
| `A-15.10` | no plaintext listener in production — the TLS policy in `server.tlsConfig` |
| `SPEC §n.m`, `F-nn.nn` | specification sections and feature requirements; read the surrounding comment for the behaviour they justify |

When you change behaviour that a comment pins to one of these, keep the reference and update the comment; when you add behaviour, a plain explanatory comment is enough — there is no SPEC to extend in this repository.
