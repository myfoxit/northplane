---
title: Reliability
description: How deliveries and escalations survive failures — the outbox with exponential retries, dead letters and replay, persisted escalation timers, supervised workers, suppression and re-arming, what lives only in memory, idempotent downtimes, and the delivery audit trail.
sidebar:
  order: 10
---

An alarm server is judged by what happens when things go wrong: the SMS gateway times out, the PBX is rebooting, Northplane itself restarts in the middle of an escalation. This page describes the mechanisms that make delivery at-least-once and escalation restart-safe, and — just as important — the few pieces of state that are deliberately kept in memory and what that means for you.

## The outbox

Every outbound action is first written to the `outbox` table and only then sent by the notifier worker. Nothing is sent from a request handler or directly from the escalation engine.

| Outbox kind | Enqueued by | Delivered as |
|---|---|---|
| `notification` | escalation engine, one per (contact, channel type) per step and repeat | the channel send (e-mail, SMS, voice, push, …) |
| `object-notification` | per-object contact routing on hard state changes | channel send without ack link |
| `webhook-sub` | outgoing [webhook subscriptions](/docs/alarming/webhooks-out/) | `POST` of the event JSON |
| `action` | escalation step `action:` (ticket, legacy ServiceNow, webhook) | ticket creation / webhook post |
| `ticket-close` | resolve/expire of an alert whose ticket has `autoClose` | ticket close/comment |

The worker ticks every **3 s** and is woken immediately when something is enqueued. Each tick **claims** up to 100 due items by pushing their `nextTry` two minutes into the future (a lease): a concurrent tick or a second node cannot pick the same item up, and a crash mid-send simply lets the lease expire so the item becomes due again. A delivery whose alert is meanwhile resolved, expired or deleted is dropped as delivered. On success the row is deleted — the `notification` event is the durable record (see below).

### Retries and backoff

On failure the item is rescheduled with exponential backoff:

```text
wait after the n-th failed attempt = base · 2^min(n, 12), capped at cap, then jittered by ±10 %
defaults: base 30 s, cap 1 h, dead after 30 attempts
```

With the defaults that gives: 1 min, 2 min, 4 min, 8 min, 16 min, 32 min, then one hour between attempts; the 30th consecutive failure moves the item to the dead-letter queue — roughly a day after the first attempt (the exact total depends on jitter). The jitter is −10 % … +10 % around the computed wait (the code comment says ±20 %; the implementation is ±10 %).

Per-channel overrides (only for kind `notification`; looked up by the job's channel **type**, i.e. the first enabled channel of that type, see [Channels](/docs/alarming/channels/)):

| Channel config key | Accepted | Effect |
|---|---|---|
| `retryMaxAttempts` | integer 1–100 | attempts before dead-lettering |
| `retryBackoffSeconds` | integer > 0 | base |
| `retryBackoffCapSeconds` | integer > 0 | cap (raised to the base if smaller) |

An alarm SMS that should be retried every 30 s and given up after five tries is a configuration choice: `retryMaxAttempts: "5"`, `retryBackoffSeconds: "15"`, `retryBackoffCapSeconds: "30"`. The UI exposes the three keys in every channel dialog under **Delivery / retries (Zustellung / Wiederholungen)**; lookup errors fall back to the defaults. Webhook subscriptions, actions and ticket closes always use the defaults.

Semantics to keep in mind:

- Delivery is **at-least-once**: if the provider accepted the message but Northplane crashed before deleting the row, the item is retried after the lease expires.
- There is no notification throttling or de-duplication beyond alert dedup and the drop of deliveries whose alert is already closed.
- Transport timeouts are shorter than the lease: HTTP providers 20 s, SMTP dial 15 s, AMI 20 s.

### Dead letters

| | |
|---|---|
| List | `GET /api/v1/notifications/dead-letters?limit=100` — permission `alerts:read`, newest first, tenant-scoped; items carry `kind`, `attempts`, `lastError`, `payload` (the channel type and alert id are inside the payload; `channelId` is empty) |
| Replay | `POST /api/v1/notifications/dead-letters/{id}:replay` — permission `alerts:ack`; resets `attempts` to 0, clears the error, makes the item due now; `202 Accepted`; audit `dlq.replay` |
| UI | **Admin → Dead letters (Dead-Letters)**: time, kind, attempts, last error, **Replay** |
| Metrics | `np_notifications_total{result="sent"}` / `{result="failed"}` / `{result="dead"}`, `np_queue_notifications_depth`, `np_events_dropped_total{source="notify"}` on `/metrics`; `GET /api/v1/system/health` → `notify: {sent, failed, dead, dropped}` |

A replayed item starts the backoff schedule from scratch. Fix the channel first (wrong credentials, dead URL, disabled channel) — otherwise it will die again after another round.

## Persisted escalation timers

Escalation chains are not goroutines or in-memory timers; they are rows in the `escalations` table (`alert_id, policy_name, step_index, repeats_done, next_at, done`). The escalation worker polls due rows every **2 s** (200 per tick) and fires them. Consequences:

- A restart loses nothing: due steps fire on the first tick after start-up. Steps that became due while the server was down fire in quick succession — a step whose computed time is already in the past is rescheduled for five seconds after the previous one.
- Acknowledge and resolve mark all rows of the alert done; an acked alert never gets a next step or repeat (see [Escalation policies](/docs/alarming/escalation-policies/)).
- A snooze wake-up re-arms step 0 with the wake-up time as the new origin.
- Timers are global, not tenant-scoped; the worker finds the alert by its id across tenants.

## Supervised workers

All background workers run under a supervisor: a panic inside one worker is recovered, logged as `server: background worker panicked; restarting` with the stack trace, and the worker is restarted after one second — without taking down the process or the other workers. The supervised set: `scheduler`, `executor`, `pipeline`, `alerting`, `correlator`, `escalation`, `notify`, `traps`, `mailin`, `mqttin`, `espa`, `agi`, `api-janitor`, `webhook-dispatcher`, `report-scheduler`, `dead-man`, plus `ldap-sync`, `federation-edge` and `ai` when configured. Graceful shutdown stops the HTTP listener first and waits for the workers to drain in-flight work (pipeline writes, notify and escalation deliveries).

The in-process event bus is the other moving part. The alerting queue (events → rule engine) is **blocking and never drops** (16 384 slots); ingress handlers persist the event before publishing it, so the event log is complete even under back-pressure. Subscribers — the SSE hub, the correlator and the webhook dispatcher — have bounded buffers and **can lose messages when they fall behind**; SSE clients are told with a `resync` frame, the webhook dispatcher simply misses the event (the event itself is still stored and queryable). If you need guaranteed delivery of every event to an external system, poll `GET /api/v1/events` or `/api/v1/events:export` with a cursor rather than relying on a subscription alone.

## Suppression and re-arming

Rule-created alerts pass a suppression gate when they open and on every 5-second re-evaluation while they stay open. In order of evaluation (the reason string lands in the `notification` event):

1. The alert's object, if any: `object in downtime`, `object flapping`, `host unreachable (parent down)`; for services `host down` (parent host hard non-UP) and `host in downtime`.
2. Selector downtimes that match the alert's `objectId` or whose selector matches the **alert labels**: `downtime <id>`.
3. Active silences whose selector (empty = all) matches the alert labels and whose `textRegex`, if set, matches the alert **title**: `silence <id>`.

A suppressed alert still **exists** — it is open, visible in the UI and API, emitted `alert_opened`, and recorded a `notification` event with `status: "suppressed"` and `error: <reason>` — but no escalation chain starts. If the rule has an `escalationPolicy`, the alert id and policy are remembered; every 5 s the engine re-checks: gone/acked/resolved/expired → forgotten; still suppressed → wait; suppression lifted → `StartChain` now (`escalation started after suppression lifted` in the log). Because the chain computes step 0 from `openedAt + after`, an already-elapsed offset fires at the next escalation tick.

What suppression does **not** do: it does not stop manual alarms (UI, API, phone, SMS `action: alert`, AGI — they bypass the gate by design); it does not look at object acknowledgements (a sticky object ack only suppresses per-object notifications); it does not consult notification time periods (those apply to contact preferences and per-object routing). Downtimes, silences, time periods and flapping thresholds themselves are documented in [Maintenance](/docs/monitoring/maintenance/) and [Checks and states](/docs/concepts/checks-and-states/).

:::caution[The re-arm set is in memory]
The "start the chain once suppression lifts" bookkeeping is an in-memory map. After a process restart, alerts that opened while suppressed are **not** re-armed when their downtime or silence ends — they stay open and silent until someone acts on them or the rule's clear event resolves them. After a restart during a maintenance window, review open alerts (`GET /api/v1/alerts?status=open`) before you rely on the chain.
:::

## What survives a restart and what does not

| Survives (database) | In memory only (lost on restart) |
|---|---|
| alerts, incidents, their statuses and `snoozedUntil` deadlines | `pendingFor` drafts — rebuilt from the next matching event; a condition that was pending for 4 of 5 minutes starts over |
| escalation timers (`escalations`) | the suppressed-alert re-arm set (see above) |
| outbox items and dead letters | heartbeat-rule "last seen" timestamps — a heartbeat rule re-arms only after its source sends again |
| events (the audit of everything that happened) | ingest rate-limit buckets (start full) |
| heartbeat resources (`lastBeat`, `missing`) | the webhook-subscription cache (refreshed within 30 s) and bundle apply tokens |
| idempotency records for `POST /downtimes` (24 h) | discovery scans |

Sessions, secrets, the ack-link secret and VAPID keys are persisted as well.

## Idempotent downtime creation

Scripts that put systems into downtime before maintenance often run in retry loops. `POST /api/v1/downtimes` is the one endpoint that honours an `Idempotency-Key` header: the key is scoped to the tenant and bound to the SHA-256 of the body; repeating the same key with the same body replays the stored response with `Idempotency-Replayed: true` instead of creating a second downtime; the same key with a different body is refused with `409 np:conflict/idempotency`. Records are purged after 24 hours.

```bash
curl -s -X POST "$NP/api/v1/downtimes" -H "Authorization: Bearer $TOK" \
  -H 'Idempotency-Key: maint-2026-08-30-db' -H 'Content-Type: application/json' \
  -d '{"selector":"env=prod,role=db","type":"fixed","start":"2026-08-30T22:00:00Z","end":"2026-08-31T02:00:00Z","comment":"planned maintenance"}'
```

A downtime takes effect for the suppression gate as soon as it is created (the server recomputes the objects' downtime depth synchronously and again every 30 s). Silences, by contrast, are looked up live and need no recomputation. See [Maintenance](/docs/monitoring/maintenance/).

## Delivery audit via events

Every attempt leaves an immutable trace — you never have to guess whether a page went out:

| Event | Emitted by | Payload |
|---|---|---|
| `escalation` | escalation engine, once per step firing and repeat | `{alertId, step, repeat, contacts: [names], channels}` (`channels` is the step's override list — empty when contact preferences were used) |
| `notification` | notifier, once per (contact, channel, attempt) | the delivery record: `{alertId, stepIndex, repeat, contactId, contact, channel, channelId, target (masked), status: sent\|failed, attempt, error, providerId, latencyMs}` |
| `notification` with `status: suppressed` | alerting engine | `{alertId, status: "suppressed", error: <reason>}` |
| `notification` | notifier, per executed escalation action | `status: sent`, `providerId` = ticket reference |
| `escalation` | alerting engine at snooze wake-up | `{alertId, title, comment: "snooze expired — alarm re-armed", policy}` |

Query them with `GET /api/v1/events?types=notification,escalation&from=<RFC 3339>`, filter by `objectId`, stream them live on `/api/v1/stream`, or forward them with a webhook subscription. `target` is masked (first three and last three characters) and `providerId` is the upstream id (Twilio SID, Resend id, ticket key …). Human actions — ack, resolve, snooze, raise, dead-letter replay, channel tests — are additionally written to the hash-chained audit log (`GET /api/v1/audit`, `np audit verify`); escalation steps and delivery attempts are events only.

There is no per-channel health endpoint; a channel's recent success rate is derived from its `notification` events. Use **Send test** in **Admin → Channels** (`POST /api/v1/channels/{name}:test-notification`) to verify a channel end to end.

## Related operational knobs

- **Dead-man switch for Northplane itself** — `deadManUrl` (`NORTHPLANE_DEADMAN_URL`, interval `deadManInterval`, default 1 min) makes the server ping an external healthchecks-style URL while it is healthy; the ping is skipped when the results queue is saturated. Wire it into a service that alarms *you* when Northplane goes quiet. See [Observability](/docs/administration/observability/).
- **Queues and lag** — `GET /api/v1/system/health` (anonymous) exposes queue depths (`eventsDepth`, `notifyDepth`), alerting stats (`rules`, `pending`, `matched`, `opened`) and notify counters; `/readyz` turns 503 when the results queue backs up.
- **Retention** — events are kept for `storage.eventRetentionMonths` (default 12) and pruned nightly; dead letters are never purged automatically — they stay in the outbox table until replayed. See [Storage](/docs/administration/storage/).
