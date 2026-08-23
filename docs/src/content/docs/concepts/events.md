---
title: Events
description: The event model, the complete event type table with emitters and payloads, how events flow through the in-memory bus, persistence and retention in monthly segments, querying, the SSE stream, NDJSON export and the Events page.
sidebar:
  order: 4
---

An **event** is the unit of history in Northplane. Every state change, every inbound alarm, every notification attempt, every ack, downtime, silence, escalation step and configuration change is appended to the event store and fanned out live. Alert rules read events; the SSE stream, outgoing webhooks and the correlator subscribe to them; the UI and reports query them.

## The event model

```json
{
  "id": "0199a8c4-5e21-7b3c-9a0e-2f1d7c8b4e55",
  "tenantId": "00000000-0000-7000-8000-000000000001",
  "ts": "2026-08-23T10:15:00.123Z",
  "type": "state_change",
  "objectId": "0199a8c0-…",
  "severity": "critical",
  "payload": { "object": "db-01", "kind": "host", "from": "UP", "to": "DOWN", "stateType": "hard", "attempt": 3, "output": "CRITICAL - no reply from 10.0.0.5 within 5s", "labels": { "env": "prod" } }
}
```

| Field | Type | Meaning |
|---|---|---|
| `id` | string | UUIDv7 — time-ordered; used as pagination cursor and as the SSE `id:` / `Last-Event-ID` |
| `tenantId` | string | every event belongs to exactly one tenant |
| `ts` | RFC 3339 | event time (UTC) |
| `type` | string | one of the types below |
| `objectId` | string, optional | the monitored host/service, when the event concerns one |
| `sourceId` | string, optional | the **event source id** for `ingress` events; the **heartbeat id** for `heartbeat_missed` |
| `severity` | `critical` \| `warning` \| `info` \| `ok` | optional |
| `payload` | JSON object | type-specific, see the table |

Events are append-only: there is no update or delete endpoint, only retention.

## Event types

| Type | Emitted by | Payload (gist) | Severity |
|---|---|---|---|
| `state_change` | pipeline — on every raw state change and whenever a hard state is entered (soft transitions included, with `stateType: "soft"`) | `{object, host?, kind, fromState, toState, from, to, stateType, attempt, output, labels, metric}` — `from`/`to` are labels such as `UP`, `CRITICAL`; `metric` is the first perfdata label | from the new state; host UNREACHABLE is forced to `warning` |
| `flapping_start` / `flapping_end` | pipeline | `{object, flapPct, labels}` | `info` |
| `ingress` | every event-source adapter: webhook, Alertmanager receiver, email/IMAP poller, SNMP-trap receiver, MQTT subscriber, ESPA / ESPA-X listeners, SMS inbound with `action: event` | the normalised event (`NormEvent`): `{source, receivedAt, dedupKey?, severity, summary, labels?, payload?, resolve?}`; `payload` archives the original body | set by the adapter mapping or the source's default severity |
| `heartbeat_missed` | alerting engine heartbeat sweep (every 5 s); the beat endpoint on recovery | `{heartbeat, labels, summary}`; on recovery additionally `resolve: true` | the heartbeat's severity; `ok` on recovery |
| `alert_opened` | alerting engine when a rule opens a new alert; `POST /api/v1/alerts` and the phone/SMS/AGI paths for manual alarms | `{alertId, title, severity, rule, labels}`; manual alarms add `via` and use `rule: "manual"` | the alert's severity |
| `alert_resolved` | alerting engine on a clear event; `POST /api/v1/alerts/{id}:resolve`, DTMF `6`, IVR/AGI resolve | `{alertId, title, rule}` or `{alertId, title}` (+ `by`, `via` for AGI) | `ok` |
| `ack` | `POST /api/v1/alerts/{id}:ack` and `:snooze`, ack link, SMS keyword, IVR digit, DTMF `4`, AGI | `{alertId, by, comment}`; snooze: `comment: "snoozed until <RFC3339>"`; link/SMS/IVR/DTMF: `{alertId, via: "ack-link"}`; AGI: `{alertId, by, via}` | `info` |
| `escalation` | escalation engine per step firing, including repeats; alerting engine when a snooze expires | `{alertId, step, repeat, contacts: [names], channels}` (channels = the step's override list, empty when contact preferences were used); wake-up: `{alertId, title, comment: "snooze expired — alarm re-armed", policy}` | `info` |
| `notification` | notifier per delivery attempt (alerts and direct object notifications); alerting engine for alerts opened while suppressed | `NotificationRecord{alertId, stepIndex, repeat?, contactId?, contact?, channel, channelId?, target? (masked), status, attempt, error?, providerId?, latencyMs?}`; `status` ∈ `pending`, `sent`, `failed`, `dead`, `suppressed` (with `error` = suppression reason) | `info` |
| `incident_update` | alerting engine (rule-created incident opened / auto-resolved), correlator (alarm storm), `POST /api/v1/incidents` | `{incidentId, alertId?, title, createdBy?, status}`; correlator: `{incidentId, title, alerts, cluster: "k=v"}`; API create: `{incidentId, title, summary, createdBy, status, action: "created", labels}` | incident severity; auto-resolve `ok`; correlator `critical` |
| `downtime` | `POST /api/v1/downtimes` | `{downtimeId, comment, start, end}` | `info` |
| `silence` | `POST /api/v1/silences` | `{silenceId, comment, expiresAt}` | `info` |
| `config` | API on any configuration mutation (objects, templates, rules, channels, bundles, …) | `{kinds: ["host"]}`, `{kinds: ["alert-rule"]}`, … | `info` |
| `system` | AI service when the monthly token budget warning fires | `{summary}` | `warning` |
| `ai_action`, `comment`, `anomaly`, `forecast` | defined in the taxonomy but **not emitted** by any code path in this version | — | — |

Two things to remember when writing rules: the alert engine receives only what is published *to* it (`ingress`, `state_change`, `flapping_*`, `heartbeat_missed`, and `incident_update` from `POST /api/v1/incidents`); engine- and API-generated lifecycle events (`alert_opened`, `ack`, `escalation`, `notification`, `config`, `downtime`, `silence`, …) are *fan-out only* so that they never re-enter the rules. And for `ingress` events the inner `payload` keys are hoisted to `event.payload.<key>` in CEL (`event.payload.subject`, `event.payload.body`, …). See [Alert rules](/docs/alarming/alert-rules/).

## How events flow

```text
producers                               bus (in-memory)           consumers
─────────────────────────────────────   ──────────────────────    ───────────────────────────
pipeline (state_change, flapping)   ──► Events queue (16384) ──► alerting engine (rules)
ingress adapters (ingress)          ──►        │
heartbeat sweep (heartbeat_missed)  ──►        │
POST /incidents (incident_update)   ──►        │
                                               ▼ fan-out
engine/API lifecycle events ──────────► subscribers only ───────► SSE hub (512)
(alert_opened, ack, notification, …)   "FanoutOnly"               correlator (1024)
                                                                  webhook dispatcher (1024)
every producer also ──────────────────► event store (segments) — persisted before/while publishing
```

- The bus does not persist anything; producers insert into the event store themselves (best-effort, failures are counted in `np_events_dropped_total`), so a slow live subscriber never loses history.
- The `Events` queue blocks producers when full rather than dropping; subscriber buffers drop and mark the subscriber for resync (the SSE stream then sends a `resync` frame).
- There are no topics: every subscriber sees every event of every tenant and filters by tenant, type and selector itself.

## Persistence and retention

| Backend | Layout |
|---|---|
| SQLite (default) | one file per month in the data directory: `events-YYYYMM.db` (+ `-wal`, `-shm`), own connection pool (4), indexes on `(tenant_id, ts)` and `(object_id, ts)`; cross-month queries fan out and merge in Go |
| PostgreSQL | parent table `events … PARTITION BY RANGE (ts)`, child partitions `events_YYYYMM` created on demand with the same indexes |

Retention is `storage.eventRetentionMonths` (default **12**, `0` = keep forever; file-only, no environment variable). The janitor enforces it once a night (between 02:00 and 03:59 local time) by deleting whole segment files or dropping whole partitions whose month is older than the cutoff. There is also a storage-level `PurgeEventPayloads(tenant, type, before)` that blanks payloads to `{}` for GDPR retention classes; it is not wired to any API route or scheduled job in this version. Events are included in `northplaned backup` (every `events-*.db` is copied, the current month last). See [Storage](/docs/administration/storage/).

## Querying events

`GET /api/v1/events` (permission `events:read`) returns `{items, nextCursor}` newest first.

| Query parameter | Meaning |
|---|---|
| `types` | comma-separated list of event types |
| `objectId`, `sourceId` | exact match |
| `severity` | exact match |
| `from`, `to` | RFC 3339 window (unparseable values are ignored) |
| `cursor` | the `id` of the last item of the previous page |
| `limit` | default 200, max 1000 |

Example:

```bash
curl -s "https://np.example.com/api/v1/events?types=state_change,alert_opened&from=2026-08-23T00:00:00Z&limit=50" \
  -H "Authorization: Bearer np_…"
```

`GET /api/v1/alerts/{id}` lists the triggering event ids of an alert (`eventIds`, last 50); reports and the Overview page's "Recent events" card are built on the same query.

## Live stream (SSE)

`GET /api/v1/stream` (permission `events:read`) is a Server-Sent-Events feed of everything fanned out on the bus for the caller's tenant. Filter with `?types=a,b` and `?selector=<label selector>` (matched against `payload.labels`); resume with `Last-Event-ID: <uuidv7>` (replays persisted events from one second before that id, up to 500). Each frame is `event: <type>`, `id: <event id>`, `data: <Event JSON>`; a `: ping` comment arrives every 15 s, or `event: resync` when the server had to drop frames for this client. Authentication is the normal bearer token or session cookie — there is no `?token=` query parameter, and the embedded UI does **not** use the stream (it polls). The full wire reference is in the [API overview](/docs/reference/api-overview/).

```bash
curl -N "https://np.example.com/api/v1/stream?types=alert_opened,alert_resolved" -H "Authorization: Bearer np_…"
```

## NDJSON export

`GET /api/v1/events:export` (permission `events:read`) streams `application/x-ndjson`, one event per line, **ascending** by time, with the same filters as the list endpoint (`objectId`, `sourceId`, `severity`, `types`, `from`, `to`); `cursor`/`limit` are ignored, pages of 1000 are read internally and the export stops after **100 000** events. The endpoint is exempt from the 30 s request deadline. Use `from`/`to` windows for SIEM shipping of large histories. (The audit log has its own export, `GET /api/v1/audit:export` — see [Observability](/docs/administration/observability/).)

```bash
curl -s "https://np.example.com/api/v1/events:export?types=notification&from=2026-08-01T00:00:00Z" \
  -H "Authorization: Bearer np_…" > notifications-august.ndjson
```

## Events in the UI

The **Events (Ereignisse)** page lists the newest 200 events with a type filter (`state_change`, `alert_opened`, `alert_resolved`, `notification`, `escalation`, `ack`, `ingress`, `config`, `downtime`, `silence`, `heartbeat_missed`, `ai_action`) and an object-id filter; each row expands to the pretty-printed payload, and the "NDJSON Export" link downloads the current type filter. The Overview page shows the 20 most recent events, and the object detail's **History (Historie)** tab shows the last 30 events of that object. See [Alerts, incidents and events (UI)](/docs/ui/alerts-incidents-events/).

## Related

- [Alerts and incidents](/docs/concepts/alerts-incidents/) — how events become alerts.
- [Event sources](/docs/alarming/event-sources/) — every adapter that produces `ingress` events, with its labels and dedup keys.
- [Outgoing webhooks](/docs/alarming/webhooks-out/) — subscribe an HTTP endpoint to event types and selectors.
- [Metrics and NP-TSDB](/docs/monitoring/metrics-and-tsdb/) — numeric history lives there, not in events.
