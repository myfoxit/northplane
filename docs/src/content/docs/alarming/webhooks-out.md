---
title: Outgoing webhooks
description: Forward Northplane events to other systems with webhook subscriptions — fields, event type and label selection, the HMAC signature header, outbox retries and dead letters, the delivered payload, and the Admin → Webhooks tab.
sidebar:
  order: 11
---

A **webhook subscription** forwards events — alert openings, acknowledgements, notifications, state changes, anything on the event bus — as HTTP `POST`s to a URL of your choice, optionally filtered by event type and label selector and signed with HMAC-SHA256. Deliveries ride the same durable outbox as notifications, so a receiver that is down for an hour gets its events when it comes back.

Do not confuse subscriptions with the **webhook channel**: the channel is a notification output used by escalation steps and contact preferences and posts a rendered alert template (with basic/token/HMAC auth from the channel config); a subscription posts the raw `Event` JSON for every matching event, independent of any escalation. Channel details are in [Channels](/docs/alarming/channels/).

Subscriptions live in **Admin → Webhooks**, at `/api/v1/webhooks` (generic resource CRUD: `objects:read` to list, `config:write` to change, `If-Match` on `PUT`), and as bundle kind `WebhookSubscription`.

## The WebhookSubscription resource

| Field | Type | Meaning |
|---|---|---|
| `name` | string | unique per tenant |
| `url` | string | `POST` target (`https://…`; plain `http://` works but sends the body in clear) |
| `types` | list of event types | deliver only these types; **empty = all** |
| `selector` | label selector | matched against the event's `payload.labels`; empty = no label filter. A non-empty selector that fails to parse matches **nothing** (rather than everything) |
| `secret` | string | literal value or `$SECRET:name$` reference; when set, every delivery carries `X-Northplane-Signature` |
| `disabled` | bool | `true` pauses the subscription. Unlike channels and event sources, a subscription created without the field is **enabled** |

```yaml title="Bundle example"
kind: WebhookSubscription
metadata: { name: siem }
spec:
  url: https://siem.example.com/hooks/northplane
  types: [alert_opened, alert_resolved, ack, escalation, notification]
  selector: env=prod
  secret: $SECRET:siem-hmac$
```

```bash title="Same subscription through the API"
curl -s -X POST "$NP/api/v1/webhooks" -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d '{
  "name": "siem",
  "url": "https://siem.example.com/hooks/northplane",
  "types": ["alert_opened", "alert_resolved", "ack", "escalation", "notification"],
  "selector": "env=prod",
  "secret": "$SECRET:siem-hmac$"
}'
```

The subscription belongs to the tenant it was created in and only ever sees that tenant's events.

## Selecting events

### By type

`types` is an exact-match list. Event types that exist:

| Type | Emitted when |
|---|---|
| `state_change` | a host/service changes state (soft and hard) |
| `flapping_start`, `flapping_end` | flap detection toggles |
| `ingress` | an event source produced a normalized event |
| `alert_opened`, `alert_resolved` | an alert opens (rule or manual) / closes |
| `ack` | acknowledge or snooze |
| `escalation` | an escalation step fires (incl. repeats), or a snooze wake-up |
| `notification` | a delivery attempt (sent/failed) or a suppressed chain |
| `incident_update` | an incident is created, resolved or clustered by the correlator |
| `heartbeat_missed` | a heartbeat is missing, or recovers (`resolve: true`) |
| `downtime`, `silence` | a downtime/silence is created |
| `config` | any configuration mutation (`{kinds: [...]}`) |
| `system` | server-side notices (e.g. AI token budget) |
| `ai_action`, `comment`, `anomaly`, `forecast` | defined in the model but not emitted by any current code path |

The payload of each type is listed in [Events](/docs/concepts/events/).

### By labels

`selector` uses the same grammar as everywhere else in Northplane — `env=prod,role in (db,cache),!legacy,site!=wien` (comma = AND; `=`/`==`, `!=`, `in (…)`, `notin (…)`, bare key = exists, `!key` = not exists). It is evaluated against the labels inside the event payload: the NormEvent labels of `ingress` events, the object labels of `state_change`/`flapping_*` events, the alert labels of `alert_opened`, the heartbeat labels of `heartbeat_missed`, and `{createdBy}` on API-created `incident_update` events. Events without labels (`ack`, `notification`, `escalation`, `config`, …) present an empty label map — positive terms (`=`, `in`, bare key) never match them, negative terms (`!key`, `!=`, `notin`) do. So a subscription meant for lifecycle events should usually filter by `types` only.

## The delivered request

```http
POST /hooks/northplane HTTP/1.1
Host: siem.example.com
Content-Type: application/json
X-Northplane-Signature: sha256=9f2c…e1a0

{"id":"0191a2b3-7c4d-7e5f-8a9b-0c1d2e3f4a5b","tenantId":"00000000-0000-7000-8000-000000000001",
 "ts":"2026-08-23T10:15:00.123Z","type":"alert_opened","objectId":"0191a2b2-…","severity":"critical",
 "payload":{"alertId":"0191a2b4-…","title":"db1 is CRITICAL","severity":"critical",
            "rule":"demo-critical","labels":{"env":"prod","host":"db1","demo":"true"}}}
```

The body is the full `Event` document exactly as `GET /api/v1/events` returns it: `id` (UUIDv7, time-ordered — use it to de-duplicate redeliveries), `tenantId`, `ts`, `type`, optional `objectId`, `sourceId`, `severity`, and the type-specific `payload`. One request per event; there is no batching. Headers: `Content-Type: application/json` and, when a secret is configured, `X-Northplane-Signature`. No `User-Agent` is set beyond Go's default, and no other authentication is available — put the secret in the signature, or use a URL only your receiver knows.

### Verifying the signature

`X-Northplane-Signature: sha256=<hex>` where `<hex>` is the lowercase hex HMAC-SHA256 of the **raw request body** with the secret as key. Compute it over the bytes you received, not over re-serialized JSON, and compare in constant time:

```python
import hashlib, hmac

def verify(secret: bytes, body: bytes, header: str) -> bool:
    expected = "sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, header or "")
```

Northplane's own ingest endpoint accepts the same header in `hmac` mode, so two instances can forward events to each other (`url: https://other/api/v1/ingest/<source>` with a `webhook` source whose mapping reads `payload.payload.title` and friends) — see [Event sources](/docs/alarming/event-sources/).

## Delivery, retries and dead letters

1. A dispatcher worker subscribes to the event bus and keeps the tenant's subscriptions in a 30-second cache — a new or changed subscription becomes effective within 30 s. For each event it checks `disabled`, `types` and `selector` and enqueues an outbox item of kind `webhook-sub` (`{url, secret, body}`).
2. The notifier posts it with a 20-second timeout through the shared HTTP client, which refuses link-local and cloud-metadata addresses (`169.254.0.0/16`, IPv6 link-local) and re-checks the resolved peer; RFC 1918 targets are allowed.
3. Any response status `≥ 300`, a connection error or a timeout counts as failure. The item is retried with the default policy — `30 s · 2^n` capped at 1 h, ±10 % jitter, dead after 30 attempts (roughly a day) — and then lands in the dead-letter queue (`GET /api/v1/notifications/dead-letters`, **Admin → Dead letters**, `:replay`). Per-channel retry overrides do not apply to subscriptions.
4. Delivery is at-least-once: a crash between the provider's `2xx` and the outbox bookkeeping repeats the post after the two-minute lease. De-duplicate on the event `id`.

Two caveats: the dispatcher's bus buffer is bounded (1024 events) — if it falls behind under a burst, events are skipped for subscriptions (they are still stored and queryable); and subscription deliveries write no `notification` event of their own (which also prevents feedback loops for subscriptions on `notification`). For a guaranteed, replayable feed combine a subscription with periodic polling of `GET /api/v1/events?from=…` or the NDJSON export. Everything about the outbox is in [Reliability](/docs/alarming/reliability/).

## The Admin → Webhooks tab

**Admin → Webhooks** lists subscriptions with name, URL, event types (or "all"), selector and status. The dialog has **Name** (fixed after creation), **URL**, **Event types** (comma-separated, hint `state_change, alert_opened, alert_resolved, notification, …`), **Selector** (e.g. `env=prod`), **HMAC secret** (literal or `$SECRET:name$`) and an **Enabled** switch that maps to `disabled`. Edits use the ETag for optimistic concurrency. There is no "send test" for subscriptions — trigger a harmless event instead (for example a `config` event by saving any resource, or an `ack` on a test alert) and watch the receiver or the dead-letter tab.

## Alternatives

- **Live stream instead of push** — `GET /api/v1/stream` (SSE, `events:read`, `?types=`/`?selector=` filters, `Last-Event-ID` resume) when your consumer can hold a connection; see [API overview](/docs/reference/api-overview/).
- **Per-alert webhooks with a rendered template** — the `webhook` notification channel in an escalation step, or an escalation `action: { webhook: <channel name> }`; see [Channels](/docs/alarming/channels/) and [Escalation policies](/docs/alarming/escalation-policies/).
- **Bulk export** — `GET /api/v1/events:export` (NDJSON, ascending, up to 100 000 events).

REST reference: [`get_webhooks`](/docs/reference/api/operations/get_webhooks/), [`post_webhooks`](/docs/reference/api/operations/post_webhooks/), [`put_webhooks_name`](/docs/reference/api/operations/put_webhooks_name/), [`delete_webhooks_name`](/docs/reference/api/operations/delete_webhooks_name/).
