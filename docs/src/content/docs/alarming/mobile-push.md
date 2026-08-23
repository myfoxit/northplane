---
title: Mobile push and the alarm app
description: The server contract for the Northplane alarm app — API token, device registration for FCM and APNs, the push channel, sound steering labels, the SSE live feed, app-triggered incidents — and the honest state of browser Web Push.
sidebar:
  order: 6
---

Northplane pushes alarms to phones through the `push` channel type. Android devices receive them through Firebase Cloud Messaging (FCM HTTP v1), iOS devices through the Apple Push Notification service (APNs); browsers would use Web Push. The companion **northplane-alarm** app (Flutter, Android/iOS) uses nothing app-specific on the server: an API token, the standard REST endpoints, the SSE stream and the push subscription endpoint described here. Anything else — a custom client, a wallboard, a script — can use the same contract.

| Piece | What it is |
|---|---|
| Authentication | an API token `np_…` sent as `Authorization: Bearer` ([API tokens](/docs/administration/api-tokens/)) |
| Device registration | `POST /api/v1/push-subscriptions` with an `fcm://` / `apns://` endpoint |
| Routing | a contact whose `userId` equals the registering identity; escalation steps with channel type `push` |
| Credentials | FCM service account and/or APNs key on the `push` channel |
| Sound steering | alert labels `np.sound`, `np.volume`, `np.overrideSilent` |
| Live feed | `GET /api/v1/stream` (SSE): `alert_opened`, `alert_resolved`, `ack`, … |
| Acting | `POST /api/v1/alerts/{id}:ack` / `:resolve` / `:snooze`, `POST /api/v1/alerts` (raise), `POST /api/v1/incidents` |

## 1. Create a token for the device

Mint an API token under **Admin → API tokens (API-Tokens)** (or `POST /api/v1/api-tokens`). The scopes the app needs: `alerts:read` (list/read alerts), `alerts:ack` (acknowledge, resolve, snooze), `events:read` (the SSE stream), `incidents:write` (raise incidents from the app), and `alerts:write` if the app should raise alerts directly. Registering a push subscription needs **no** scope — any authenticated user or token may do it. One token per device is the cleanest model: the push subscription belongs to the token, and revoking the token ends its pushes.

Note the token's `id` (shown in the token list `GET /api/v1/api-tokens`, permission `admin:tokens`; the device itself can read it from `GET /api/v1/whoami` → `actorId`). You need it in step 3.

## 2. Register the device

```bash
curl -X POST https://monitoring.example.net/api/v1/push-subscriptions \
  -H "Authorization: Bearer np_…" -H "Content-Type: application/json" \
  -d '{"endpoint": "fcm://<device-registration-token>"}'
```

| Rule | Detail |
|---|---|
| Endpoint schemes | `fcm://<token>` (Android), `apns://<token>` (iOS), `https://…` (Web Push — then `keys: {p256dh, auth}` is required) |
| Ownership | the row is stored under the caller's actor id: the **user id** for a browser session, the **API-token id** for a token |
| Re-registration | the same `(owner, endpoint)` replaces the old row — post again after every token refresh |
| Response | `201 Created`; `422` for a malformed endpoint / missing keys; `401 np:auth/required` when the principal is neither a user nor a token |
| Unregister | `DELETE /api/v1/push-subscriptions` with `{"endpoint": "…"}` → `204` |
| Cleanup | a provider answer meaning "device gone" (FCM 404 / `UNREGISTERED`, APNs 410 / `BadDeviceToken` / `Unregistered`, Web Push 404/410) deletes the row automatically |

Generated reference: [post_push_subscriptions](/docs/reference/api/operations/post_push_subscriptions/), [delete_push_subscriptions](/docs/reference/api/operations/delete_push_subscriptions/).

## 3. Link the contact

Pushes are addressed to **contacts**, not devices: the `push` channel looks up all subscriptions whose owner equals the contact's `userId`. Set `userId` (under **Admin → Contacts (Kontakte)** or `PUT /api/v1/contacts/{name}`) to the API-token id from step 1 — or to the user id for people who registered from a browser session. A contact without `userId` fails with `push: contact is not linked to a user (or API-token id)`; a linked contact without subscriptions fails with `web push: no subscriptions for user`. Both show up as `notification` events with `status: failed` and are retried like any delivery.

Then make sure the contact is reached over `push`: either list `push` in the contact's preferences or in the escalation step's `channels` (step channels override preferences completely — see [Contacts and on-call](/docs/alarming/contacts-and-oncall/)).

## 4. Configure the push channel

Create one `push` channel (**Admin → Channels (Kanäle)**, type `push`) and set `enabled: true`. The keys are provider credentials; Web Push needs none.

| Key | Used by | Notes |
|---|---|---|
| `fcmServiceAccount` | FCM | the Firebase service-account JSON file content (`project_id`, `client_email`, `private_key`, `token_uri`); store it as a secret and reference it |
| `fcmEndpoint` | FCM | test override; default `https://fcm.googleapis.com/v1/projects/<project_id>/messages:send` |
| `apnsKey` | APNs | the `.p8` auth key (PKCS#8 EC PEM); store it as a secret |
| `apnsKeyId` | APNs | 10-character key id (JWT `kid`) |
| `apnsTeamId` | APNs | Apple team id (JWT `iss`) |
| `apnsTopic` | APNs | the app's bundle id → header `apns-topic` |
| `apnsSandbox` | APNs | `"true"` → `api.sandbox.push.apple.com` (development builds); default production |
| `apnsEndpoint` | APNs | test override |

```yaml title="channel-push.yaml"
kind: Channel
metadata: { name: mobile-push }
spec:
  type: push
  enabled: true
  config:
    fcmServiceAccount: $SECRET:fcm-sa$
    apnsKey: $SECRET:apns-p8$
    apnsKeyId: ABCDE12345
    apnsTeamId: TEAMID1234
    apnsTopic: com.northplane.alarm
    apnsSandbox: "false"
```

Multi-line secrets (the service-account JSON, the `.p8` file) go into the secret store verbatim; see [Secrets](/docs/administration/secrets/). FCM access tokens (OAuth2 two-legged JWT, scope `firebase.messaging`) and APNs provider tokens (ES256) are minted by Northplane and cached per channel (FCM until 5 minutes before expiry, APNs 50 minutes).

**Test it:** `POST /api/v1/channels/mobile-push:test-notification` with `{"target": "<token-id or user-id>"}` (or **Test senden** in the UI with the id as target) pushes `Test notification from Northplane (<you>)` to every subscription of that id; the response `detail` is `delivered=<n>`.

## 5. What a push contains

The body text is the channel template (default `{{.Title}}`); the title is always `[SEV] Northplane`. Every transport carries the same data keys:

| Data key | Value |
|---|---|
| `type` | `alert_opened` |
| `title` | `[CRITICAL] Northplane` (severity upper-cased) |
| `body` | rendered template |
| `severity` | `critical`, `warning`, `info` or `ok` (lower-case) |
| `url` | `<baseUrl>/alerts/<id>` (empty without `baseUrl`) |
| `ackUrl` | signed ack link, 24 h (empty without `baseUrl`) |
| `alertId` | alert id |
| `labels` | all alert labels as a JSON **string** |
| `np.sound`, `np.volume`, `np.overrideSilent` | copied from the alert labels, only when set |

- **FCM** message: `{"message": {"token": …, "notification": {"title", "body"}, "android": {"priority": "HIGH"}, "data": {…}}}`.
- **APNs** request: `POST /3/device/<token>` with headers `apns-topic`, `apns-push-type: alert`, `apns-priority: 10`, `apns-expiration: 0`; payload `{"aps": {"alert": {"title", "body"}, "interruption-level": "time-sensitive", "sound": "<np.sound>.caf" or "default"}, …data keys…}`. With `np.overrideSilent=true` the `aps` block becomes a **critical alert**: `"interruption-level": "critical"`, `"sound": {"critical": 1, "name": "<np.sound>.caf", "volume": <np.volume, 0.0–1.0, default 1.0>}` — this requires Apple's critical-alerts entitlement in the app; without it iOS treats it as a normal alert.
- **Web Push**: JSON `{"title", "body", "url", "ackUrl", "severity"}`, encrypted per RFC 8291 (`aes128gcm`), `TTL: 86400`, `Urgency: high`, VAPID `Authorization` (ES256 JWT, 12 h, audience = endpoint origin).

### Sound steering with `np.*` labels

| Label | Values | Effect |
|---|---|---|
| `np.sound` | `np_klaxon`, `np_sirene`, `np_puls` | the tone the app plays; APNs `sound` = `<value>.caf` |
| `np.volume` | `0.0`–`1.0` | playback volume; APNs critical-alert volume (only together with `np.overrideSilent`) |
| `np.overrideSilent` | `true` / `false` | `true` → APNs critical alert (breaks through silent mode / focus), FCM stays high priority |
| `np.tts` | free text | not for push — spoken text override for voice calls ([Voice calls and IVR](/docs/alarming/voice-and-ivr/)) |

The labels ride on the alert, so every path that creates an alert can set them: a rule's `setLabels` ([Alert rules](/docs/alarming/alert-rules/)), an IVR option's `labels`, the **Trigger alarm (Alarm auslösen)** dialog on the Alerts page (section *Alarm-App Sound*: tone, volume, override-silent switch), or `labels` in `POST /api/v1/alerts`. They are also part of every `alert_opened` event, so an app listening on the SSE stream can play the tone even when the push is late.

## 6. The live feed (SSE)

`GET /api/v1/stream` (permission `events:read`) streams every event of the token's tenant as Server-Sent Events:

```bash
curl -N https://monitoring.example.net/api/v1/stream?types=alert_opened,alert_resolved,ack \
  -H "Authorization: Bearer np_…" -H "Accept: text/event-stream"
```

```text
: connected 2026-08-23T10:15:00Z
event: alert_opened
id: 0191a2b4-…
data: {"id":"0191a2b4-…","tenantId":"…","ts":"2026-08-23T10:15:00Z","type":"alert_opened","severity":"critical","payload":{"alertId":"0191a2b5-…","title":"Feueralarm Halle 3","severity":"critical","rule":"manual","via":"api","labels":{"np.sound":"np_klaxon","np.volume":"1.0"}}}
```

- `types=a,b` filters by event type; `selector=<label selector>` filters on `payload.labels`. Authentication is the bearer token or the session cookie — there is **no `?token=` query parameter** for the stream.
- Send `Last-Event-ID: <last id>` on reconnect to replay what was missed (up to 500 events, from 1 s before that id). Every 15 s the server sends `: ping`, or `event: resync` with `data: {}` if the client fell behind — then re-fetch `GET /api/v1/alerts?status=open,acked`.
- `alert_opened` payloads carry `alertId`, `title`, `severity`, `rule` (rule name, or `manual`), `labels` (and `via` for manual alarms); `ack` payloads `alertId` plus `by`/`comment` or `via`; `alert_resolved` `alertId`, `title`.

Full stream reference: [API overview](/docs/reference/api-overview/).

## 7. Raising alarms from the app

Two paths:

- **Direct:** `POST /api/v1/alerts` (`alerts:write`) with `{"title", "message", "severity", "escalationPolicy", "labels": {"np.sound": "np_klaxon"}}` creates an alert, emits `alert_opened` and starts the chain immediately — no rule involved, suppression bypassed.
- **Through an incident:** `POST /api/v1/incidents` (`incidents:write`) with `{"title", "severity", "summary", "impact", "alertIds": []}` creates the incident **and publishes an `incident_update` event through the rule engine** (payload `{incidentId, title, summary, createdBy, status, action: "created", labels: {createdBy}}`). One rule turns app-created incidents into alarms:

```yaml title="rule-app-incident.yaml"
kind: AlertRule
metadata: { name: app-incident }
spec:
  match: 'event.type == "incident_update" && event.payload.action == "created"'
  severity: critical
  title: "{{ .event.payload.title }}"
  setLabels: { np.sound: np_sirene, np.overrideSilent: "true" }
  escalationPolicy: nachtdienst
```

Incidents created by the engine itself (rule `incident: true`, the correlator) are fanout-only and never re-enter the rules, so this cannot loop. Rule syntax: [Alert rules](/docs/alarming/alert-rules/).

## 8. Acting on alarms

| Action | Endpoint | Permission |
|---|---|---|
| list open/acked alerts | `GET /api/v1/alerts?status=open,acked` | `alerts:read` |
| acknowledge | `POST /api/v1/alerts/{id}:ack` `{"comment": "…"}` — stops the escalation chain | `alerts:ack` |
| resolve | `POST /api/v1/alerts/{id}:resolve` | `alerts:ack` |
| snooze | `POST /api/v1/alerts/{id}:snooze` `{"until": "<RFC 3339>"}` — acked until then, re-opens and restarts the chain at step 0 if still unresolved | `alerts:ack` |
| open the ack link from the push | `GET <ackUrl>` — no login needed | — |

Semantics, audit records and the other ack paths: [Acknowledge and snooze](/docs/alarming/acknowledge-and-snooze/).

## Web Push in the browser — current state

The server side of browser Web Push is complete: `https://` endpoints with `p256dh`/`auth` keys are accepted by `POST /api/v1/push-subscriptions`, payloads are encrypted per RFC 8291, and a VAPID key pair is generated on first boot (stored in the key-value table under `vapid`; subject = `baseUrl`, or `mailto:ops@northplane.local` when `baseUrl` is unset — a generation failure logs `VAPID generation failed, web push disabled`).

What is **missing**: no HTTP endpoint returns the VAPID public key, the web UI contains no `PushManager.subscribe` flow, and its service worker (`/sw.js`) is deliberately a kill-switch stub that unregisters itself and has no push handlers. In practice this means browser push cannot be set up end-to-end today — neither from the Northplane UI nor by a third-party PWA, which would need the public key. Use the FCM/APNs path (or [ntfy](/docs/alarming/channels/#ntfy) for a quick phone notification without an app) until that changes; the gap is tracked on [Roadmap and known issues](/docs/project/roadmap-and-known-issues/).
