---
title: Acknowledge and snooze
description: Every way to acknowledge, resolve or snooze an alert — UI, np CLI, API, ack links, SMS keyword, IVR, DTMF, the alarm app, AI — and exactly what each path records.
sidebar:
  order: 9
---

Acknowledging an alert says "someone owns this": the alert moves from `open` to `acked`, every pending escalation step and repeat is cancelled, and the acknowledgement is recorded as an `ack` event plus an audit entry. Resolving closes the alert. Snoozing acknowledges with a deadline — if the alert is still unresolved at that time it re-opens and the escalation chain starts again from step 0. All paths below end in the same storage transitions, so it does not matter whether the on-call engineer taps a link on the phone, presses a key during the call, answers an SMS, or uses the UI.

## Status transitions

```text
                 ack / snooze                      resolve (any path) · clear event · incident resolve
   open ──────────────────────────▶ acked ─────────────────────────────────────────────▶ resolved
     │                               ▲  │  snooze deadline reached → back to open, chain restarts
     │                               └──┘
     └──────────── resolve ───────────────────────────────────────────────────────────▶ resolved
   open | acked ── rule.autoCloseAfter elapsed ───────────────────────────────────────▶ expired
```

- `ack` is only possible from `open` (an already acked alert answers `404 np:not-found`); `resolve` from `open` or `acked`; `snooze` from `open` or `acked` (so you can put a wake-up on an alert that was acked without one).
- Every ack and resolve calls `StopChain`: all `escalations` rows of the alert are marked done. A step that is already mid-delivery in the outbox still goes out; a delivery whose alert turned out to be resolved or expired is dropped silently.
- Acks and snoozes are **not** suppression: an acked alert can be refreshed by further matching events (severity may rise, title/payload update) without re-opening, and a clear event resolves it.

## Acknowledgement paths

| Path | Who | What it does | Recorded as |
|---|---|---|---|
| **UI** — **Alerts (Alarme)** row → **Acknowledge (Quittieren)**, or **Problems (Probleme)** row when an open alert exists; dialog with optional comment ("Running escalations will be stopped") | logged-in user with `alerts:ack` | `POST /api/v1/alerts/{id}:ack {"comment"}` | audit `alert.ack {"comment"}`, event `ack {alertId, by, comment}` |
| **CLI** — `np ack <alert-id> [-m comment]` → prints `acknowledged: <title>` | API token with `alerts:ack` | same endpoint | same |
| **API** — [`POST /api/v1/alerts/{id}:ack`](/docs/reference/api/operations/post_alerts_id_ack/), body optional `{"comment":"…"}` → `200` Alert | `alerts:ack` | ack, stop chain, mirror a sticky ack onto the object when the alert has an `objectId` | same |
| **Ack link** — `GET /api/v1/ack/{token}` in e-mail, SMS (`ack: <url>`), ntfy action, Slack/Teams **Acknowledge** button, push `ackUrl` | anyone holding the link (no login) | acks only if still `open`; actor = contact name; answers an HTML page "✓ Quittiert — Der Alarm wurde übernommen. Die Eskalationskette ist gestoppt." even when the alert was already acked; invalid/expired → `403`, unknown alert → `404` | audit `alert.ack {"via":"ack-link"}`, event `ack {alertId, via:"ack-link"}` |
| **Outbound call, DTMF** — during a voice notification press **4** | the called contact | `POST /api/v1/voice/gather/{token}` (Twilio); 4 = ack (open only), 6 = resolve (open or acked); other digits: "Not acknowledged. Goodbye." | audit `alert.ack` / `alert.resolve {"via":"voice-dtmf"}`, event `ack {alertId, via:"ack-link"}` / `alert_resolved` |
| **SMS keyword** — text `ACK` (or the source's `ackKeyword`) to an `sms-inbound` number | a phone number that matches a contact's `phone` | acks the **newest open alert** of the tenant; reply `Acknowledged: <title>`; unknown numbers get "Unknown number — not acknowledged." | audit `alert.ack {"via":"sms"}`, event `ack {alertId, via:"ack-link"}` |
| **IVR (inbound call)** — `ack-alert` / `resolve-alert` option of a `voice-inbound` menu | caller (after PIN gate, if any) | one open alert → acted on immediately; several → choose by digit (1–9, newest five) | audit `alert.ack {"via":"voice-inbound"}`, event `ack {alertId, via:"ack-link"}` |
| **Asterisk AGI** — the same menu over FastAGI | caller | ack/resolve via the server's internal path; also sets the object's sticky ack (`acknowledged via asterisk-agi`) | audit `alert.ack {"via":"asterisk-agi"}`, event `ack {alertId, by, via:"asterisk-agi"}` |
| **Alarm app / any client** | API token with `alerts:ack` | `POST /api/v1/alerts/{id}:ack`, `:resolve`, `:snooze` | as API |
| **AI agent / MCP** — tool `acknowledge_alert` | principal with `alerts:ack` (auto-approved mutating tool) | acks as `ai:<name>`, stops the chain; no `ack` event and no object ack | audit `ai.execute.acknowledge_alert` |

Details of the telephony paths (menus, PIN, keyword, languages) are in [Voice and IVR](/docs/alarming/voice-and-ivr/); how the app registers and what it receives is in [Mobile push](/docs/alarming/mobile-push/).

:::note[Ack links]
The link is `<baseUrl>/api/v1/ack/<alertId>.<contactId>.<expiry>.<hmac>`, signed with a server-generated secret and valid for **24 hours** (the DTMF gather callback uses the same token). It is rendered into notifications only when `baseUrl` (`NORTHPLANE_BASE_URL`) is configured. The token is not consumed on first use — re-clicking is a no-op — and it always acknowledges, never resolves. Object notifications (per-object contact routing) carry no ack link.
:::

:::caution[`:ack` ignores the tenant header]
`:ack`, `:resolve` and `:snooze` all honour `X-Northplane-Tenant` for `admin:tenants` operators (the ack verb used to act on the principal's own tenant only).
:::

## Comments

The optional ack comment is not stored on the alert itself. It lands in three places: the `ack` event payload (`comment`), the audit entry (`after: {"comment":…}`), and — when the alert belongs to a host or service — the object's sticky acknowledgement (`ackedBy`/`ackComment` in the object's state, shown on the Problems page as "acknowledged: <name>"). That sticky object ack suppresses further object-level notifications until the next hard recovery clears it; it does not suppress rule-based alerts. A snooze writes `snoozed until <RFC 3339>` as the object comment.

## Resolve

| Path | Notes |
|---|---|
| UI **Resolve (Schließen)** on the Alerts page, `np resolve <alert-id>`, [`POST /api/v1/alerts/{id}:resolve`](/docs/reference/api/operations/post_alerts_id_resolve/) | from `open` or `acked`; `alerts:ack`; stops the chain; emits `alert_resolved {alertId, title}`; audit `alert.resolve` |
| A clear event (`severity: ok`, state `OK`/`UP`, or `resolve: true`) with the same dedup key | the engine resolves it (`alert_resolved {alertId, title, rule}`), unless the rule has `resolveOnOk: false` and the event does not match |
| DTMF **6**, IVR `resolve-alert`, AGI | same transitions, audited `{"via":"voice-dtmf"}`, `{"via":"voice-inbound"}` or `{"via":"asterisk-agi"}` |
| `POST /api/v1/incidents/{id}:resolve` | resolves the incident **and** all its open/acked alerts (chains stopped); no event is emitted for the incident itself |
| `autoCloseAfter` on the rule | flips to `expired` instead of `resolved`; no event |

If the alert owns a ticket created with `autoClose`, any resolve enqueues the ticket close (outbox kind `ticket-close`, retried like a notification). If the alert was opened by a rule with `incident: true`, the incident auto-resolves once its last member alert is gone.

## Snooze

`POST /api/v1/alerts/{id}:snooze` with `{"until":"2026-08-24T08:00:00Z"}` (RFC 3339, must be in the future, otherwise `422 np:validation/until`; permission `alerts:ack`):

1. The alert becomes `acked` with `snoozedUntil` set; the chain is stopped; the object (if any) gets the sticky ack comment `snoozed until …`; audit `alert.snooze {"until"}`; event `ack {alertId, by, comment:"snoozed until …"}`.
2. Every 5 s the engine looks for acked alerts whose `snoozedUntil` has passed and flips them back to `open` (acked fields cleared, one conditional update per row so a second node cannot double-fire), clears the object's sticky ack, and emits an `escalation` event `{alertId, title, comment:"snooze expired — alarm re-armed", policy}`.
3. The escalation chain restarts **from step 0**, with `openedAt` treated as the wake-up time — so a first step with `after: 0s` fires within seconds, and later offsets count from the wake-up, not from the original opening. The policy is the rule's `escalationPolicy`, or for manual alarms the policy stored with the alert.
4. If the alert was resolved in the meantime, nothing happens (`resolvedAt` clears the snooze). A plain ack on a snoozed alert is not possible (it is already `acked`); to change the deadline snooze again, to cancel the wake-up resolve the alert.

In the web UI, open alerts offer **Snooze** (1 h / 4 h / 24 h) next to acknowledge and resolve; other durations via the API.

```bash
curl -s -X POST "$NP/api/v1/alerts/$ALERT:snooze" -H "Authorization: Bearer $TOK" \
  -H 'Content-Type: application/json' -d '{"until":"2026-08-24T08:00:00Z"}'
```

## What gets recorded

| Action | Audit entry (`action`, `after`) | Event |
|---|---|---|
| API/UI/CLI ack | `alert.ack {"comment"}` | `ack {alertId, by, comment}` |
| Ack link, SMS keyword, IVR, DTMF 4 | `alert.ack {"via":"ack-link"\|"sms"\|"voice-inbound"\|"voice-dtmf"}` | `ack {alertId, via:"ack-link"}` (no `by`; all four reuse the link-style event) |
| Asterisk AGI ack | `alert.ack {"via":"asterisk-agi"}` | `ack {alertId, by, via:"asterisk-agi"}` |
| Resolve (API/UI/CLI) | `alert.resolve` | `alert_resolved {alertId, title}` |
| Resolve by clear event | — | `alert_resolved {alertId, title, rule}` |
| Snooze | `alert.snooze {"until"}` | `ack {alertId, by, comment:"snoozed until …"}`; at wake-up `escalation {…, comment:"snooze expired — alarm re-armed"}` |
| Manual alarm | `alert.raise` | `alert_opened {…, rule:"manual", via}` |

Audit entries are hash-chained and exportable (`GET /api/v1/audit`, `np audit tail`); events are queryable with `GET /api/v1/events?types=ack,alert_resolved,escalation` and streamed on `/api/v1/stream`. Every ack, resolve and snooze also shows up in the **Events** page and, through outgoing [webhook subscriptions](/docs/alarming/webhooks-out/), in your own systems.

## Related

- [Alerts and incidents](/docs/concepts/alerts-incidents/) — the lifecycle in context, dedup, incidents, correlation
- [Escalation policies](/docs/alarming/escalation-policies/) — what `unlessAcked` means and why an ack always ends the chain
- [Reliability](/docs/alarming/reliability/) — suppression versus acknowledgement, what a restart forgets
- [Users, roles and permissions](/docs/administration/users-roles-permissions/) — `alerts:ack` versus `alerts:write`
