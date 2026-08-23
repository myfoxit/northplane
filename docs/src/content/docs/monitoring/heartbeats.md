---
title: Heartbeats
description: Dead-man monitoring of cron jobs, backups and pipelines with Heartbeat resources and beat URLs, alert rules for heartbeat_missed events, and the separate outbound dead-man URL that watches Northplane itself.
sidebar:
  order: 7
---

A heartbeat monitors something that must *happen regularly* rather than something you can poll:
a nightly backup, a cron job, a data pipeline, a remote site pushing results. The job calls a beat
URL when it succeeds; if the beats stop, Northplane emits a `heartbeat_missed` event and — through
an alert rule — opens an alert. Recovery is automatic with the next beat.

Three related things are easy to confuse:

| Mechanism | Direction | What it watches | Where it lives |
|---|---|---|---|
| **Heartbeat resource** (this page) | job → Northplane (`/api/v1/heartbeats/{name}/beat`) | external jobs | **Admin → Heartbeats**, `/api/v1/heartbeats` |
| `AlertRule.heartbeat` | events → rule | an **event source** that falls silent | [Alert rules](/docs/alarming/alert-rules/) |
| `deadManUrl` | Northplane → external service | Northplane itself (the checks pipeline) | `config.yaml` ([last section](#the-outbound-dead-man-url)) |

## The Heartbeat resource

| Field | Type | Default | Meaning |
|---|---|---|---|
| `name` | string | required | unique per tenant; part of the beat URL |
| `expectEvery` | duration | required (> 0) | maximum gap between beats |
| `grace` | duration | `0` | extra slack added to `expectEvery` before a miss is declared |
| `severity` | `critical`, `warning`, `info`, `ok` | `warning` | severity of the `heartbeat_missed` event (and, via rules, the alert) |
| `labels` | map | — | copied into every `heartbeat_missed` event (`event.labels.<key>`), e.g. `team`, `job` |
| `lastBeat` | timestamp | — | runtime: last accepted beat |
| `missing` | bool | `false` | runtime: currently overdue |

Heartbeats are stored in their own table (not a generic resource); there is no version/`If-Match`
and `POST` upserts by name.

| Endpoint | Permission | Notes |
|---|---|---|
| `GET /api/v1/heartbeats` | `objects:read` | list with `lastBeat` and `missing` ([reference](/docs/reference/api/operations/get_heartbeats/)) |
| `POST /api/v1/heartbeats` | `config:write` | create **or update** by name: `{"name","expectEvery","grace"?,"severity"?,"labels"?}` → `201` ([reference](/docs/reference/api/operations/post_heartbeats/)) |
| `POST /api/v1/heartbeats/{name}/beat` | `objects:write` | record a beat → `200 {"status":"ok"}` ([reference](/docs/reference/api/operations/post_heartbeats_name_beat/)) |
| `GET /api/v1/heartbeats/{name}/beat` | `objects:write` | same, for `curl`/`wget` in cron ([reference](/docs/reference/api/operations/get_heartbeats_name_beat/)) |
| `DELETE /api/v1/heartbeats/{name}` | `config:write` | delete ([reference](/docs/reference/api/operations/delete_heartbeats_name/)) |

Creating one:

```bash
curl -sS -X POST "$NP_SERVER/api/v1/heartbeats" \
  -H "Authorization: Bearer $NP_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"nightly-backup","expectEvery":"24h","grace":"1h","severity":"critical",
       "labels":{"team":"ops","job":"backup"}}'
```

In the UI: **Admin → Heartbeats** (Heartbeats) lists name, expected interval, grace, severity,
last beat and a status badge (*missing*), and the *Create* dialog takes name, *Expected every*,
*Grace (optional)*, severity, labels and shows the copy-ready beat command
`curl -H "Authorization: Bearer np_…" /api/v1/heartbeats/<name>/beat`. Pick the severity
explicitly in the dialog — when the field is left untouched the server stores its default,
`warning`. The heartbeat name is fixed after creation.

:::caution[Not in bundles]
`Heartbeat` is listed in the bundle kind order, but the applier does not support it: `np apply`
prints `warning: unsupported kind Heartbeat` and skips the document, and `np export` never
includes heartbeats. Create them through the API (idempotent `POST`) or the Admin tab — for
example from the same script that deploys the job.
:::

## Sending beats

Beats are authenticated API calls; mint a token with **only** `objects:write` for the job
([API tokens](/docs/administration/api-tokens/)) and call the URL at the *end* of a successful run:

```bash
# cron: 30 3 * * *  /usr/local/bin/backup.sh && curl -fsS -m 10 -X POST \
#   -H "Authorization: Bearer $NP_TOKEN" https://northplane.example.net/api/v1/heartbeats/nightly-backup/beat
```

```bash
# plain GET works too (for wget, health-check hooks, HTTP-only schedulers)
wget -qO- --header="Authorization: Bearer np_…" https://northplane.example.net/api/v1/heartbeats/nightly-backup/beat
```

- Unknown name → `404` — beats never create a heartbeat (`np:not-found`).
- Bearer tokens are the intended credential. A browser session cookie is **not** usable for the GET
  form: session-authenticated requests that the browser marks cross-site are rejected with
  `403 np:auth/csrf`, so a beat URL pasted into an external page or bookmark will not work; use a token.
- Beat early rather than late: `expectEvery` counts from the *last* beat, so a job that runs at
  03:30 every night with `expectEvery: 24h` needs a `grace` that covers its own runtime jitter.

## Miss detection and recovery

The alerting engine checks every heartbeat of every tenant every **5 s**:

1. A heartbeat is **overdue** when it has never been beaten (`lastBeat` is empty) **or**
   `now − lastBeat > expectEvery + grace`.
2. The first time it is found overdue, `missing` flips to `true` (once — no repeat while it stays
   missing) and a `heartbeat_missed` event is emitted with the heartbeat's `severity`,
   `sourceId` = the heartbeat's id, and payload `{"heartbeat":"<name>","labels":{…},"summary":"Heartbeat \"<name>\" missing"}`.
   The event is persisted (visible on the Events page and the SSE stream) and fed through the
   alert rules.
3. The next beat resets `lastBeat` and `missing`. If the heartbeat *was* missing, the beat endpoint
   additionally emits a `heartbeat_missed` event with severity **`ok`** and payload
   `{"heartbeat","labels","summary":"Heartbeat <name> recovered","resolve":true}`, again through
   the rules — which resolves the alert (next section).

:::caution[A new heartbeat is overdue immediately]
A heartbeat that has never received a beat counts as overdue from the moment it is created
(within 5 s). Create it right before the job's first run, send an initial beat from the deployment
script, or expect one alert that the first real beat resolves.
:::

Timing: detection latency is at most 5 s after `expectEvery + grace` has elapsed. Everything here
is persistent (`lastBeat`/`missing` are stored), so a server restart does not lose the state — but
a server that was *down* for longer than a heartbeat's window will declare misses right after it
comes back if the jobs could not beat meanwhile.

## Alerting on missed heartbeats

A `heartbeat_missed` event alone is just an event. To page someone, add an alert rule
([Alert rules](/docs/alarming/alert-rules/)); in CEL the event looks like this:

| Path | Value |
|---|---|
| `event.type` | `heartbeat_missed` |
| `event.severity` | the heartbeat's severity; `ok` on recovery |
| `event.source` | the heartbeat **id** |
| `event.summary` | `Heartbeat "nightly-backup" missing` / `Heartbeat nightly-backup recovered` |
| `event.labels.<key>` | the heartbeat's labels |
| `event.payload.heartbeat` | the heartbeat **name** |
| `event.payload.resolve` | `true` on recovery |

```yaml
kind: AlertRule
metadata: {name: heartbeat-missed}
spec:
  match: 'event.type == "heartbeat_missed"'
  title: '{{ .event.summary }}'
  escalationPolicy: ops-default
  setLabels: {kind: heartbeat}
---
kind: AlertRule
metadata: {name: backup-heartbeat-critical}
spec:
  match: 'event.type == "heartbeat_missed" && event.labels.job == "backup"'
  severity: critical
  title: 'Backup heartbeat {{ .event.payload.heartbeat }} missing'
  escalationPolicy: ops-phone
```

Leave `severity` empty to inherit the heartbeat's own severity, as in the first rule. The default
dedup key for these events is `<rule name>/<heartbeat id>`, and the recovery event carries
`severity: ok` and `resolve: true` — so the same rule that opened the alert resolves it on the
next beat, and open alerts are folded per heartbeat rather than duplicated. Test a rule with
**Alerting → Rules → Test** or `POST /api/v1/alert-rules/{name}:test` against the last 24 h of
events.

:::note[AlertRule.heartbeat is something else]
`spec.heartbeat: {source, expectEvery}` on an alert rule watches an **event source** (webhook,
MQTT, SNMP traps …) and alerts when no event from that source id arrived for `expectEvery`; it
arms only after the first event and is in-memory. It does not use Heartbeat resources, and
`source` must be the EventSource **id**, not its name. Use it for "the PLC stopped sending";
use Heartbeat resources for "the job stopped running".
:::

## The outbound dead-man URL

The opposite direction: Northplane pings an external watchdog (healthchecks.io, Uptime Kuma push
monitors, a heartbeat in *another* Northplane) so that *someone notices when Northplane itself is
down or stuck*.

```yaml title="config.yaml"
deadManUrl: "https://hc-ping.com/<uuid>"   # env NORTHPLANE_DEADMAN_URL
deadManInterval: 1m                          # file-only; <= 0 is treated as 1m
```

- When `deadManUrl` is empty (default) nothing happens.
- Otherwise `northplaned` sends `GET <deadManUrl>` every `deadManInterval` with a 10 s timeout.
- The ping is **skipped** while the results queue holds more than 7000 entries (the bus capacity is
  8192) — logged as `deadman: skipping ping, results queue saturated`. A saturated pipeline
  therefore stops the heartbeat and the external watchdog fires, which is the point: "process up but
  not processing" counts as down.
- Point it at a Heartbeat resource on a second Northplane (`https://other/api/v1/heartbeats/np-main/beat`
  — the GET needs a bearer token, which this plain GET cannot add, so use a proxy or a watchdog
  service that accepts unauthenticated URLs).

See [Observability](/docs/administration/observability/) for health endpoints and metrics that
complement the dead-man URL.

## Quick reference

| Item | Value |
|---|---|
| Miss check cadence | every 5 s |
| Overdue condition | `lastBeat` empty, or `now − lastBeat > expectEvery + grace` |
| Event | `heartbeat_missed`, severity = heartbeat severity; recovery: severity `ok` + `resolve: true` |
| Default severity | `warning` (server); dialog shows `critical` until you pick one |
| Beat auth | bearer token with `objects:write`; POST or GET |
| Bundle support | none (API/UI only) |
| Dead-man ping | every `deadManInterval` (1 m), skipped when results queue > 7000 |
