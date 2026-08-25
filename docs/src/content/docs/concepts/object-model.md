---
title: Object model
description: Hosts and services, folders, labels and selectors, templates and effective configuration, check-command references and macros, the ObjectSpec fields with their defaults, saved state, IDs and If-Match versioning.
sidebar:
  order: 2
---

Everything Northplane monitors is an **object**: a **host** or a **service** that belongs to a host. Objects carry a small, Nagios-inspired spec (what to check, how often, how many attempts), are grouped by **folders** and **labels**, inherit settings from **templates**, and reference a **check command**. This page defines those terms precisely; the how-to for creating and editing objects is on [Hosts and services](/docs/monitoring/hosts-and-services/).

## Hosts and services

Hosts and services share one table and one JSON shape; `kind` tells them apart.

| Field | Type | Meaning |
|---|---|---|
| `id` | string | UUIDv7, assigned by the server (see [IDs, versions and If-Match](#ids-versions-and-if-match)) |
| `tenantId` | string | owning tenant |
| `kind` | `host` \| `service` | |
| `name` | string | required; **cannot be renamed** through `PUT` (recreate the object instead) |
| `hostId` | string | services only; the parent host. Deleting a host cascades to its services. |
| `folder` | string | default `/`; a path with subtree semantics |
| `labels` | map | free key/value pairs, indexed for selectors |
| `spec` | ObjectSpec | see [ObjectSpec reference](#objectspec-reference) |
| `version` | int64 | optimistic-locking version, starts at 1 |
| `createdAt`, `updatedAt` | RFC 3339 | |

Identity is `(tenant, kind, host, name)`: host names are unique per tenant, service names are unique per host. When you create a service you reference the host by **name or id** (`host` in the request body, `metadata.host` in a bundle). A service check runs against the **host's** `address`; a service's own `address` is not used as the target.

Objects are reached through `GET /api/v1/objects`, `/hosts`, `/services`, `GET|PUT|DELETE /api/v1/objects/{id}`, created with `POST /api/v1/hosts` and `POST /api/v1/services`, or in bulk with `POST /api/v1/objects:batch` (`mode: all-or-nothing` (default) or `partial`). Every create/update/delete is audited (`host.create`, `service.update`, …), emits a `config` event and updates the scheduler immediately.

## Folders

A folder is a `/`-separated path (`/`, `/prod`, `/prod/db`). Folders are purely organisational today: the object list filters by folder prefix (`GET /api/v1/objects?folder=/prod`), bundle export can be restricted to a subtree (`GET /api/v1/config/bundles:export?folder=/prod`), and the UI groups by them. Role scopes have a `folder` field, but it is **not enforced** in this version (see [Tenancy and RBAC](/docs/concepts/tenancy-rbac/)).

## Labels and selectors

Labels are the primary grouping mechanism. Everything that needs "a set of objects" — downtimes, silences, business-service leaves, dashboard widgets, metric queries, webhook subscriptions, bundle prune, the Objects list filter — takes a **label selector** instead of a static group.

```text
selector    = requirement *("," requirement)          ; comma = AND
requirement = KEY "=" VALUE | KEY "==" VALUE | KEY "!=" VALUE
            | KEY "in" "(" VALUE *("," VALUE) ")"
            | KEY "notin" "(" VALUE *("," VALUE) ")"
            | KEY                                      ; key exists
            | "!" KEY                                  ; key does not exist
KEY         = [A-Za-z0-9_.\-/]+
VALUE       = unquoted text up to the next "," or ")" (trimmed)
```

Example: `env=prod,role in (db,cache),!legacy,site!=wien`.

Semantics worth knowing:

- `!=` and `notin` also match objects that do **not** have the key at all.
- `=`, `in`, *exists* and *not-exists* are pushed down into SQL via the `object_labels` index; negations are evaluated in Go afterwards.
- Values cannot contain commas or parentheses; there is no quoting.
- The empty selector matches everything; an unparseable selector is a 422 (`np:validation/selector`) on the API and matches nothing in a webhook subscription.

Labels also travel with events and alerts: a `state_change` event carries the object's labels, alert rules can add labels (`setLabels`), and silences/downtimes with a selector are matched against an alert's labels. Event sources merge their own `labels` into every event they emit. The Objects page offers both the selector filter and a full-text filter; see [Objects (UI)](/docs/ui/objects/).

## Templates and effective configuration

A **template** (`/api/v1/templates`, bundle kind `Template`) is a reusable `ObjectSpec` fragment. An object lists templates in `spec.templates`; templates may list templates themselves.

Resolution (`EffectiveSpec`) is: **built-in defaults ⊕ templates (in declared order, later wins, recursively) ⊕ the object's own spec**. Rules:

- Scalar fields are replaced when the overlay sets them; `vars` are merged key-wise; `templates`, `parents`, `args`, `contacts`, `contactGroups` and `notifyOn` are replaced wholesale (Nagios `use` semantics).
- Unknown template names and cycles/duplicates are rejected with 422 at write time. An object whose chain breaks later (for example because a template was deleted) is still indexed with defaults and shows as a configuration error.
- `GET /api/v1/objects/{id}/effective-config` returns `{spec, templateChain}` — the fully resolved spec and the ordered template names; the object detail view shows the same ([Objects (UI)](/docs/ui/objects/)).
- Template, check-command and time-period changes trigger a full tenant catalog reload and re-schedule of all objects.

:::note[Template fields that are stored but not applied]
A template has a `kind` (`host`, `service` or `command`) and `labels`. Neither is enforced or merged anywhere in this version: a template of kind `service` can be attached to a host, and template labels are **not** copied onto objects. Put labels on the objects (or in the bundle's `metadata.labels`).
:::

The [Templates](/docs/monitoring/templates/) page shows how to build a template hierarchy; [Config bundles](/docs/administration/config-bundles/) how to ship it as YAML.

## Check commands

`spec.checkCommand` is a string whose prefix selects the execution class:

| Value | Class | Meaning |
|---|---|---|
| `passive` or empty | passive | Never executed actively; results arrive via `POST /api/v1/results` (scripts, np-agent, NSCA replacements). Only freshness probes run when `stalenessAfter` is set. |
| `builtin:<name>` | builtin | In-process Go check (`icmp`, `http`, `tcp`, `dns`, `snmp`, `tls-cert`, `agent`, … — 17 in total). `spec.args` are the check's flags. |
| `exec:<plugin>` | exec | Nagios plugin executed by the server (`<plugin>` resolved under `pluginsDir` unless absolute). `spec.args` are appended to argv. |
| `agent:exec:<plugin>` | agent | Executed by np-agent on the host; pulled via `GET /api/v1/agent/checks`. argv = `[<plugin>] + args`. |
| any other bare name | named | Looks up a stored **CheckCommand** resource of that name (what the Nagios importer produces). Unknown name → configuration error. |

A `CheckCommand` resource (`/api/v1/check-commands`, bundle kind `CheckCommand`) has `name`, `type` (`exec` \| `builtin` \| `agent` \| `passive`), `line` (argv; for `builtin` the first element is the check name, the rest are flags), `env` (export `NAGIOS_*`/`NORTHPLANE_*` environment macros to exec plugins) and `timeout`. For named `exec`/`agent` commands the object's `args` are **not** appended to `line`; they are only available as `$ARG1$…$ARG32$` inside it. Environment macro export happens only for named commands with `env: true`, never for inline `builtin:`/`exec:` references.

:::caution
`CheckCommand.timeout` is stored but the executor uses only the object's effective `spec.timeout`. `checkPeriod` is enforced: scheduled runs and freshness probes outside the period are skipped (manual check-now always runs). `zone` is resolved into the catalog but not consulted.
:::

### Macros

Arguments (inline `args` and `CheckCommand.line`) are expanded element by element — never through a shell — before execution. Unknown macros stay verbatim; `$$` is a literal `$`.

| Macro | Value |
|---|---|
| `$ARG1$` … `$ARG32$` | the object's `spec.args[n-1]`, empty when unset |
| `$SECRET:name$` | a value from the tenant's secret store (left verbatim if unresolvable; never expanded in the agent pull endpoint) |
| `$USER1$` | the plugins directory (`pluginsDir`); other `$USERn$` are not defined |
| `$_HOSTFOO$` / `$_SERVICEFOO$` | `spec.vars["foo"]` of the host / service (case-insensitive key) |
| `$HOSTNAME$`, `$HOSTALIAS$`, `$HOSTDISPLAYNAME$` | host name |
| `$HOSTADDRESS$` | effective host `address`, falling back to the host name |
| `$SERVICEDESC$`, `$SERVICEDISPLAYNAME$` | service name |
| `$MAXHOSTATTEMPTS$`, `$MAXSERVICEATTEMPTS$` | effective `maxCheckAttempts` |
| `$TIMET$`, `$LONGDATETIME$`, `$SHORTDATETIME$`, `$DATE$`, `$TIME$` | current time in the classic Nagios formats |
| `$HOSTSTATE$`, `$SERVICESTATE$`, `$HOSTOUTPUT$`, `$SERVICEPERFDATA$`, `$LASTSERVICECHECK$`, … | state-based macros; defined for contexts that carry check state, but the executor supplies none while running a check, so in check arguments they stay unexpanded |
| `$NOTIFICATIONTYPE$`, `$NOTIFICATIONNUMBER$`, `$CONTACTNAME$`, `$CONTACTEMAIL$` | notification context only |

The complete flag reference of every builtin check is on [Builtin checks](/docs/monitoring/builtin-checks/); plugin execution, exit codes and output grammar on [Plugins and Nagios](/docs/monitoring/plugins-and-nagios/); agent checks on [Agent](/docs/monitoring/agent/).

## ObjectSpec reference

All fields are optional at rest; the table lists the value after template resolution and defaults.

| Field | Type | Default | Meaning |
|---|---|---|---|
| `address` | string | — | check target (hosts); services use their host's address |
| `templates` | []string | — | template names, applied in order, later wins |
| `parents` | []string | — | parent **host names** for reachability (hosts only) |
| `checkCommand` | string | `""` = passive | see [Check commands](#check-commands) |
| `args` | []string | — | builtin flags / plugin args / `$ARGn$` values |
| `interval` | duration | `60s` | normal check cadence; clamped to 1 s … 24 h |
| `retryInterval` | duration | `15s` | recheck cadence while in a soft state |
| `maxCheckAttempts` | int | `3` | attempts before a problem becomes hard |
| `timeout` | duration | `30s` | per-execution timeout (builtin and exec) |
| `checkPeriod` | string | `24x7` | time-period name (stored, not enforced) |
| `notificationPeriod` | string | — | time-period name for direct object notifications (evaluated in UTC) |
| `enableNotifications` | bool | `true` | direct object notifications on/off |
| `contacts`, `contactGroups` | []string | — | contacts/groups notified directly on hard changes (validated to exist) |
| `notifyOn` | []string | all + recovery | subset of `warning`, `critical`, `unknown`, `down`, `unreachable`, `recovery` |
| `enableChecks` | bool | `true` | `false` removes the object from the wheel (freshness probe only if `stalenessAfter`) |
| `enableFlapDetection` | bool | `true` | |
| `flapThresholdLow` | float | `25` | % — flapping stops below |
| `flapThresholdHigh` | float | `50` | % — flapping starts at or above |
| `stalenessAfter` | duration | — | passive/agent freshness: synthetic UNKNOWN when no result arrives within this window |
| `stalenessText` | string | `UNKNOWN - check result is stale (freshness threshold exceeded)` | output text of the synthetic result |
| `thresholdMode` | `static` \| `adaptive` | `static` | reserved for AI baselines; checks use static thresholds |
| `zone` | string | — | satellite zone (stored only) |
| `runbook` | string | — | Markdown shown in the object detail |
| `vars` | map | — | custom variables (`$_HOSTFOO$`), merged key-wise across the chain; `vars.flow` feeds `builtin:http-flow` |

Durations are Go strings (`30s`, `5m`, `24h`); a bare integer in JSON means seconds.

## Saved state

Each object has exactly one `check_state` row (the `state` member of an object read with `withState=true`, the default):

| Field | Meaning |
|---|---|
| `state` | services `OK=0`, `WARNING=1`, `CRITICAL=2`, `UNKNOWN=3`; hosts `UP=0`, `DOWN=1`, `UNREACHABLE=2` |
| `stateType` | `soft` or `hard` |
| `attempt` | current attempt counter (1 … `maxCheckAttempts`) |
| `output`, `longOutput`, `perfdata` | raw plugin output of the last result |
| `latencyMs`, `execMs` | planned→started delay and execution time |
| `lastCheck`, `nextCheck`, `lastHardChange`, `lastOk` | timestamps; `lastCheck` null = PENDING (never checked) |
| `flapping`, `flapPct` | flap detector output |
| `ackedBy`, `ackComment` | sticky acknowledgement mirrored from the alert that was acked |
| `downtimeDepth` | number of active downtimes covering the object (recomputed every 30 s and on downtime changes) |

A **problem** is a hard non-OK state; `GET /api/v1/problems` lists problems and hides acknowledged or in-downtime ones unless `includeHandled=true`. The rules that drive these fields are on [Checks and states](/docs/concepts/checks-and-states/).

## IDs, versions and If-Match

- All persistent entities use **UUIDv7** ids (time-ordered, canonical lowercase `8-4-4-4-12`), minted by the server. Because ids are time-sortable they double as pagination cursors and SSE resume points.
- Objects are addressed by id (`/api/v1/objects/{id}`); config documents by **name** (`/api/v1/templates/{name}`), and every `{name}` path also accepts the document's id.
- Every object and config document has an integer `version` that starts at 1 and increases on each write. Reads return `ETag: "<version>"`.
- `PUT` on an object or config document must send `If-Match: "<version>"` (also accepted: `3`, `W/"3"`). Missing header → **428** `np:precondition/if-match`; stale version → **409** `np:conflict/version`; creating a name that exists → **409** `np:conflict/duplicate`.
- A `PUT` on an object replaces `spec` wholesale (send the full spec), replaces `labels` when present and `folder` when non-empty. Bundle apply bypasses the version check (unconditional upsert) and preserves ids.

Read the [API overview](/docs/reference/api-overview/) for the full convention set (errors, pagination, idempotency) and [Hosts and services](/docs/monitoring/hosts-and-services/) for worked examples.
