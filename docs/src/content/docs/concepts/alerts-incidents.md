---
title: Alerts and incidents
description: The alert entity and its life cycle (open, acked, snoozed, resolved, expired), deduplication keys, manual alarms, suppression order and re-arm, every acknowledgement path, incidents from rules and the correlator, and the np.* labels.
sidebar:
  order: 5
---

An **alert** is the thing people get paged for. Rules turn events into alerts; escalation policies decide who is notified; acknowledging an alert stops the chain. An **incident** groups alerts that belong together — created by a rule, by the alarm-storm correlator, or by a human or the AI agent. This page is the mental model; the how-to pages are under [Alarming](/docs/alarming/overview/).

## From event to alert

```text
event (ingress | state_change | heartbeat_missed | incident_update)
  └─► alert rule matches (CEL `match`)              ── or: heartbeat rule, manual POST /alerts
        └─► [pendingFor] condition must hold N seconds
              └─► UpsertAlert(dedupKey)  ── existing open/acked alert? fold in, no new chain
                    └─► alert_opened event
                          ├─► rule.incident → own incident
                          ├─► suppressed? (downtime / flapping / host down / silence) → wait, re-arm later
                          └─► StartChain(escalationPolicy) → steps → notifications
```

A matching event is a **clear** rather than an open when `event.severity == "ok"`, `event.state` is `OK`/`UP`, or the payload carries `resolve: true`; a clear resolves the open/acked alert with the same dedup key. With `resolveOnOk` (default `true`) even a *non-matching* clear event resolves — so a rule that only matches `CRITICAL` still closes its alert on the next `OK` of the same object. Rule fields, CEL and templates are on [Alert rules](/docs/alarming/alert-rules/).

## The alert entity

| Field | Meaning |
|---|---|
| `id`, `tenantId` | |
| `ruleId` | id of the rule that opened it; `"manual"` for API/phone/SMS/app alarms |
| `objectId`, `incidentId` | optional links |
| `status` | `open` → `acked` → `resolved`; `expired` when `autoCloseAfter` hit |
| `severity` | `critical` \| `warning` \| `info` \| `ok` — from the rule, else the event |
| `title` | rendered from the rule's `title` template (default: event summary, else `<object> is <state>`) |
| `dedupKey` | see below |
| `openedAt`, `ackedAt`, `ackedBy`, `resolvedAt`, `snoozedUntil` | life-cycle timestamps |
| `payload` | the triggering event payload; for manual alarms `{summary, manual: true, by, via, escalationPolicy}` |
| `labels` | event labels ⊕ the rule's `setLabels` (⊕ later merges such as `recordingUrl`, `transcript`) |
| `eventIds` | the last 50 triggering event ids |
| `ticket` | `{channel, type, ref, url, autoClose}` once an escalation ticket action created one |

API: `GET /api/v1/alerts` (filters `status`, `severity`, `objectId`, `ruleId`, `incidentId`, `since`; newest first; default 100, max 1000), `GET /api/v1/alerts/{id}`, `POST /api/v1/alerts` (manual), `POST /api/v1/alerts/{id}:ack|:resolve|:snooze`. Permissions: `alerts:read`, `alerts:write` (raise), `alerts:ack` (ack/resolve/snooze).

## Life cycle

```text
            ┌──────────── clear event / :resolve / DTMF 6 / incident :resolve ────────────┐
            │                                                                              ▼
  open ──ack──► acked ──────────────────────────────────────────────────────────────► resolved
   ▲  │         │  ▲                                                                  (final)
   │  │         │  └── :snooze {until}: acked + snoozedUntil, chain stopped
   │  └─────────┼───── autoCloseAfter (rule) ───────────────────────────────────────► expired
   │            │                                                                     (final)
   └────────────┘  snooze expires: back to open, chain restarts from step 0
```

| Transition | Trigger | Effects |
|---|---|---|
| open → acked | any [ack path](#acknowledging-snoozing-and-resolving) | `ackedAt/ackedBy` set, escalation chain cancelled, sticky ack mirrored onto the object's `check_state`, `ack` event, audit `alert.ack` |
| open → acked (snoozed) | `POST :snooze {until}` | as ack plus `snoozedUntil`; object ack comment `snoozed until …` |
| acked (snoozed) → open | every 5 s the engine re-opens alerts whose `snoozedUntil` has passed | acked fields cleared, sticky ack cleared, `escalation` event `snooze expired — alarm re-armed`, chain restarts **from step 0 with `openedAt` rebased to now** |
| open/acked → resolved | clear event, `POST :resolve`, DTMF `6`, IVR/AGI resolve, `POST /incidents/{id}:resolve` | chain cancelled, `alert_resolved` event, rule-created incident auto-resolved when it was the last active alert, ticket auto-close job if `ticket.autoClose` |
| open/acked → expired | rule `autoCloseAfter` elapsed since `openedAt` (checked every 5 s) | like resolve, status `expired` |

Re-firing events for an already open or acked alert **fold in** (see dedup) and never restart the chain; only a snooze wake-up does.

## Deduplication keys

The dedup key decides whether an event opens a new alert or updates an existing one. Default when the rule has no `dedupKey` template:

1. `<objectId>/<ruleName>` when the event has an object;
2. else `<ruleName>/<event.dedupKey>` when the normalised event carries one (Alertmanager fingerprints, SNMP `source/agent/trapOid`, mail Message-ID, ESPA-X call id, …);
3. else `<ruleName>/<sourceId>`.

A custom `dedupKey` is a Go template over `{{ .event.* }}`, `{{ .object.id }}` and `{{ .rule.name }}`. Storage enforces a partial unique index on `(tenant, dedupKey)` for status `open`/`acked`; a re-fire raises the severity if the new one is higher (never lowers it), replaces title and payload with the newest event, appends the event id (last 50), and emits **no** new `alert_opened`. Heartbeat rules use `heartbeat/<ruleName>`; manual alarms use whatever `dedupKey` the caller sends (phone: `call/<CallSid>`, SMS: `sms/<MessageSid>`, AGI: `agi/<uniqueid>`).

## Manual alerts

`POST /api/v1/alerts {title, message?, severity?, escalationPolicy?, labels?, objectId?, dedupKey?}` (permission `alerts:write`) creates an alert directly — the web **Trigger alarm** dialog, the alarm app, phone/IVR (`via: voice`, `asterisk-agi`) and SMS (`action: alert`) all use it. Manual alarms have `ruleId: "manual"`, default severity `critical`, must name an existing policy if they name one, emit `alert_opened` (fan-out only, never through rules) and start the chain at once. They **ignore suppression** by design: downtimes, silences and flapping do not hold a manual alarm back. A repeated trigger with the same `dedupKey` folds into the existing open/acked alarm and returns 200 instead of 201.

## Suppression and re-arm

Suppression is evaluated for **rule-created** alerts when they open and again every 5 s while they stay open and unacked. The checks run in this order; the first hit wins and its reason is recorded in a `notification` event with `status: "suppressed"`:

| # | Condition | Reason string |
|---|---|---|
| 1 | the alert's object has `downtimeDepth > 0` | `object in downtime` |
| 2 | the object is flapping | `object flapping` |
| 3 | the object is a host in state UNREACHABLE | `host unreachable (parent down)` |
| 4 | the object is a service whose host is hard non-UP | `host down` |
| 5 | the object is a service whose host has `downtimeDepth > 0` | `host in downtime` |
| 6 | an active downtime lists the object, or its selector matches the **alert labels** | `downtime <id>` |
| 7 | an active silence whose selector (empty = all) matches the alert labels and whose `textRegex` (if set) matches the alert **title** | `silence <id>` |

While suppressed the alert **exists** and is visible as open; only the escalation chain is withheld. If the rule has an escalation policy the alert is remembered and re-checked every 5 s: once nothing suppresses it any more (downtime deleted or expired, silence expired, flapping stopped, host back UP) the chain starts — an already elapsed `after` offset fires at the next 2 s escalation poll. Acked, resolved or expired alerts are forgotten.

:::caution[In-memory state]
The re-arm set and `pendingFor` drafts live in memory. After a restart, alerts that opened while suppressed are not re-armed automatically, and a pending condition starts counting again.
:::

Not part of suppression: object acknowledgements (a sticky ack on an object does not hold back new rule alerts; it only mutes direct object notifications) and notification periods. Downtimes and silences themselves are described on [Maintenance](/docs/monitoring/maintenance/); flapping and reachability on [Checks and states](/docs/concepts/checks-and-states/).

## Acknowledging, snoozing and resolving

| Path | How | Notes |
|---|---|---|
| Web UI | Alerts page / Problems page ack dialog | `POST /alerts/{id}:ack {comment}` |
| API / CLI | `POST /api/v1/alerts/{id}:ack`, `np ack` | permission `alerts:ack`; only from `open` (otherwise 404) |
| Ack link | `GET /api/v1/ack/{token}` from e-mails/pushes | HMAC-signed token `<alertId>.<contactId>.<exp>.<sig>`, valid 24 h, needs `baseUrl`; answers an HTML "Quittiert" page, acks only `open` alerts |
| Alarm app | ack/snooze/resolve from the app | the app authenticates with its own API token (`alerts:ack`) and calls the same `:ack` / `:snooze` / `:resolve` routes |
| SMS | reply starting with the source's `ackKeyword` (default `ACK`) from a contact's phone number | acks the newest open alert |
| IVR (inbound call) | menu option `ack-alert` (default menu digit `3`) | Twilio `voice-inbound` or Asterisk FastAGI |
| Outbound voice DTMF | press `4` during a Twilio/Asterisk alarm call (`6` = resolve) | `POST /api/v1/voice/gather/{token}` |
| Snooze | `POST /alerts/{id}:snooze {until}` (future RFC 3339) | alert becomes `acked` with `snoozedUntil`; wake-up restarts the chain from step 0 |
| Resolve | `POST /alerts/{id}:resolve`, DTMF `6`, IVR `resolve-alert`, incident resolve, clear event | final |

Every path writes an `ack`/`alert_resolved` event and an audit entry (`alert.ack`, `alert.snooze`, `alert.resolve`). Note that `POST /alerts/{id}:ack` uses the caller's **home** tenant and ignores `X-Northplane-Tenant` (the `:resolve` and `:snooze` routes honour the header). Walk-throughs for every path are on [Acknowledge and snooze](/docs/alarming/acknowledge-and-snooze/).

## Incidents

| Field | Meaning |
|---|---|
| `id`, `tenantId`, `version` | `PUT /incidents/{id}` needs `If-Match` |
| `status` | `open` \| `resolved` |
| `severity`, `title`, `summary`, `impact`, `ticketUrl` | `summary` is written by humans or the AI (`POST /incidents/{id}:summarize`) |
| `createdBy` | a user name, `correlation`, `rule:<name>` or the AI agent |
| `openedAt`, `resolvedAt` | |

Three ways an incident comes into being:

- **Rule-driven** — `incident: true` on a rule gives every alert it opens its own incident (`createdBy: rule:<name>`); when the incident's last open/acked alert resolves, the incident auto-resolves (`incident_update` with `status: resolved`). Only `rule:`-created incidents auto-resolve.
- **Correlator (alarm storms)** — a bus subscriber sweeps every 10 s over `alert_opened` events of the last **120 s**; if at least **5** fresh alerts share one dominant `key=value` label pair, they are attached to an incident `Alarm storm: <n> alerts sharing <k>=<v>` (severity critical, `createdBy: correlation`; an existing incident of a clustered alert is reused) and an AI summary job is queued when a provider is configured. Manual alarms participate because they emit `alert_opened` too.
- **Manual / API / AI** — `POST /api/v1/incidents {title, severity?, summary?, impact?, ticketUrl?, alertIds?}`; this one publishes `incident_update {action: "created"}` **through the rules**, so a rule such as `event.type == "incident_update" && event.payload.action == "created"` can alarm on app-created incidents.

`POST /incidents/{id}:resolve` resolves the incident and all its open/acked alerts (chains stopped, no event emitted); `:merge {sourceIds}` moves alerts into the target and resolves the sources. The [Incidents page](/docs/ui/alerts-incidents-events/) shows cards with AI summary and resolve actions; the AI side is on [Agent chat](/docs/ai/agent-chat/).

## Alerts vs. direct object notifications

Objects can also notify **without** a rule: `spec.contacts` / `spec.contactGroups` are notified on hard state changes (gated by `enableNotifications`, `notifyOn`, `notificationPeriod`, object downtime and a sticky ack). Those deliveries go through the same outbox and produce `notification` events with an empty `alertId`, but they create no alert and have no escalation chain. Use rules + policies for anything that must be acknowledged or escalated; see [Contacts and on-call](/docs/alarming/contacts-and-oncall/).

## The `np.*` labels

A few labels on an alert are interpreted by outputs. They can come from a rule's `setLabels`, the manual trigger dialog, an IVR option's `labels` or the event itself.

| Label | Consumer | Effect |
|---|---|---|
| `np.sound` | mobile push (FCM/APNs) | tone name (`np_klaxon`, `np_sirene`, `np_puls`); APNs `sound = <name>.caf` |
| `np.volume` | mobile push | `0.0`–`1.0` critical-alert volume (APNs, with overrideSilent) |
| `np.overrideSilent` | mobile push | `"true"` → APNs critical interruption level, FCM high priority |
| `np.tts` | voice channel | spoken text override for the alarm call |

Details: [Mobile push](/docs/alarming/mobile-push/) and [Voice and IVR](/docs/alarming/voice-and-ivr/).

## Where to go next

- [Alarming overview](/docs/alarming/overview/) — the end-to-end picture and recipes.
- [Escalation policies](/docs/alarming/escalation-policies/) — steps, repeats, `unlessAcked`, persisted timers.
- [Reliability](/docs/alarming/reliability/) — outbox retries, dead letters, what is in memory.
- [Events](/docs/concepts/events/) — the event types referenced above.
