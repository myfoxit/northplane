---
title: Notification channels
description: Reference for every notification channel type — e-mail, SMS, voice, push, ntfy, Slack, Teams, webhook, MQTT and ticket systems — with all config keys, secret references, retry settings, templates and the channel selection rule.
sidebar:
  order: 4
---

A **channel** is a configured way out of Northplane: an SMTP relay, a Twilio account, an Asterisk PBX, a Slack webhook, a ServiceNow instance. Escalation policies and contact preferences never name a channel — they name a channel **type** (`sms`, `voice`, …) and Northplane picks the configured channel of that type. This page is the reference for the channel model, the selection rule, templates, retries and every type with every config key.

Channels live under **Admin → Channels (Kanäle)**, at `/api/v1/channels` and in bundles as `kind: Channel`. How channels are used by policies is on [Escalation policies](/docs/alarming/escalation-policies/); who receives what is on [Contacts and on-call](/docs/alarming/contacts-and-oncall/); the outbox, retries and dead letters are on [Reliability](/docs/alarming/reliability/).

## The channel model

| Field | Type | Notes |
|---|---|---|
| `name` | string | unique per tenant; escalation *actions* reference channels by name |
| `type` | string | required (`422 channel type required`); one of the 13 types below |
| `enabled` | bool | **no default** — see the caution below |
| `config` | map of string → string | transport settings; every value is a string, so numbers and booleans must be quoted in YAML (`"587"`, `"true"`) |
| `template` | string | optional Go template that replaces the type's default message (see [Message templates](#message-templates)) |
| `id`, `tenantId`, `version`, `createdAt`, `updatedAt` | | injected by the server; `PUT` needs `If-Match: "<version>"` |

Channel types: `email`, `sms`, `voice`, `push`, `ntfy`, `slack`, `teams`, `webhook`, `mqtt` and the four ticket types `servicenow`, `zendesk`, `jira`, `ticket`.

| Operation | Endpoint | Permission |
|---|---|---|
| list / create | `GET` / `POST /api/v1/channels` | `objects:read` / `config:write` |
| read / update / delete | `GET` / `PUT` / `DELETE /api/v1/channels/{name}` | `objects:read` / `config:write` |
| send a test | `POST /api/v1/channels/{name}:test-notification` | `config:write` |

Generated reference: [post_channels](/docs/reference/api/operations/post_channels/), [put_channels_name](/docs/reference/api/operations/put_channels_name/), [post_channels_name_test_notification](/docs/reference/api/operations/post_channels_name_test_notification/).

:::caution[`enabled` is not defaulted]
Channels are stored exactly as you send them. A channel created through the API or a bundle **without `enabled: true` is disabled** and is silently skipped by every escalation step and contact preference. Only the UI dialog pre-sets `enabled: true` for new channels. Always write `enabled: true` in bundles.
:::

## How a channel is selected

Escalation steps (`channels: [sms, voice]`) and contact preferences list channel **types**. At delivery time Northplane loads the tenant's channels ordered by name and uses the **first enabled channel of the requested type**. Consequences:

- Configure **at most one enabled channel per type** per tenant. If you keep two enabled `voice` channels, the alphabetically first one is used for every call — there is no per-step or per-contact channel choice. Disable the other one or merge them.
- Only escalation step **actions** (`action.ticket.channel`, `action.webhook`) reference a channel **by name**; a disabled channel there fails with `channel "X" is disabled`.
- The delivery target depends on the type:

| Type | Target taken from | If empty |
|---|---|---|
| `email` | the contact's `email` | delivery fails: `contact "X" has no email target` |
| `sms`, `voice` | the contact's `phone` (E.164) | delivery fails |
| `push` | the contact's `userId` (user id or API-token id; see [Mobile push](/docs/alarming/mobile-push/)) | delivery fails |
| all other types | the channel's own `config.url` | delivery fails — this is why an `ntfy` channel must carry a `url` even though the code would default to `https://ntfy.sh` |

A failed target lookup is a normal delivery failure: it is retried and eventually dead-lettered like a provider error, and it is visible as a `notification` event with `status: failed`.

## Secrets in channel config

Write `$SECRET:name$` instead of a literal value and store the value once under **Admin → Secrets**. The reference is resolved at send time from the **tenant's** secret store (no cross-tenant fallback); a reference to a missing secret resolves to an empty string, which the provider then rejects. See [Secrets](/docs/administration/secrets/) for the secret store itself.

Only the keys marked **secret** in the tables below are resolved. In particular `url`, `accountSid`, `apiKeySid`, `from`, `host`, `username` are read literally — a `$SECRET:…$` in a Slack webhook `url` is sent to the network as-is and fails. (The UI shows the `$SECRET` hint on a few of those fields; the backend does not honour it there.)

## Sending a test notification

In **Admin → Channels** every row has **Test senden / Send test**. The API is:

```bash
curl -X POST https://monitoring.example.net/api/v1/channels/alarm-sms:test-notification \
  -H "Authorization: Bearer np_…" -H "Content-Type: application/json" \
  -d '{"target": "+4366412345678"}'
```

- `target` is the destination for the personal channel types: an e-mail address (`email`), a phone number (`sms`, `voice`) or a user id / API-token id (`push`). URL-based types ignore it and use the channel's `url`. Without a target the personal types fail with `no target for <type> (pass one in the request)`.
- The test sends a synthetic alert `Test notification from Northplane (<your user>)` with severity `info`, alert id `test`. Voice tests therefore carry **no DTMF gather** (nothing to acknowledge), ticket tests create the ticket but do not link it to an alert.
- Success: `200 {"result":"sent","detail":"<provider id>"}` and an audit entry `channel.test`. Failure: `502` problem `np:notify/test-failed` with the transport error in `detail` — the same text you would later see in a `notification` event.

## Message templates

Every channel renders a Go `text/template` (`missingkey=zero`; functions `upper`, `lower`, `trunc N s`, `json v`). The channel's `template` field overrides the per-type default; unknown types fall back to the webhook template.

Render context (`.` in the template):

| Field | Value |
|---|---|
| `.Alert` | the full alert (`.Alert.ID`, `.Alert.Title`, `.Alert.Severity`, `.Alert.Status`, `.Alert.Labels`, `.Alert.OpenedAt`, `.Alert.Payload`, `.Alert.Ticket`, …) |
| `.Contact` | the contact being notified (empty for actions/tickets) |
| `.Severity` | upper-cased severity: `CRITICAL`, `WARNING`, `INFO`, `OK` |
| `.Title` | alert title |
| `.Labels` | alert labels (map) |
| `.Step` | 1-based escalation step |
| `.Repeat` | repeat counter of that step |
| `.Policy` | policy name (`object` for object-level contact notifications) |
| `.BaseURL` | the configured `baseUrl` |
| `.AlertURL` | `<baseUrl>/alerts/<id>` — empty when `baseUrl` is not set |
| `.AckURL` | `<baseUrl>/api/v1/ack/<token>` (24 h) — empty without `baseUrl`; always empty for object notifications |
| `.Now` | RFC 3339 UTC timestamp |

For `email`, a first line `Subject: …` becomes the subject; otherwise the subject is `[SEV] Title`. `slack`, `teams` and `ticket` templates **must render valid JSON**. For `voice`, the label `np.tts` on the alert replaces the rendered text entirely (see [Voice calls and IVR](/docs/alarming/voice-and-ivr/)).

Default templates, verbatim:

```text
email:   Subject: [{{.Severity}}] {{.Title}}
         {{.Severity}}: {{.Title}}

         Alert:    {{.AlertURL}}
         Opened:   {{.Alert.OpenedAt}}
         Labels:   {{range $k,$v := .Labels}}{{$k}}={{$v}} {{end}}
         Step:     {{.Step}} (policy {{.Policy}})

         Acknowledge: {{.AckURL}}
sms:     [{{.Severity}}] {{trunc 100 .Title}} ack: {{.AckURL}}
ntfy:    {{.Title}}
push:    {{.Title}}
voice:   Northplane alert. Severity {{.Severity}}. {{.Title}}. Press 4 to acknowledge, 6 to resolve.
webhook: {"version":1,"alert":{{json .Alert}},"severity":"{{.Severity}}","title":{{json .Title}},"labels":{{json .Labels}},"step":{{.Step}},"policy":{{json .Policy}},"ackUrl":{{json .AckURL}},"alertUrl":{{json .AlertURL}}}
mqtt:    {"version":1,"alert":{{json .Alert}},"severity":"{{.Severity}}","title":{{json .Title}},"labels":{{json .Labels}},"ackUrl":{{json .AckURL}},"alertUrl":{{json .AlertURL}}}
slack:   {"text":"[{{.Severity}}] {{.Title}}","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"*[{{.Severity}}] <{{.AlertURL}}|{{.Title}}>*\nStep {{.Step}} · Policy {{.Policy}}"}},{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"Acknowledge"},"url":"{{.AckURL}}","style":"primary"}]}]}
teams:   {"type":"message","attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","content":{"type":"AdaptiveCard","$schema":"http://adaptivecards.io/schemas/adaptive-card.json","version":"1.4","body":[{"type":"TextBlock","size":"Medium","weight":"Bolder","text":"[{{.Severity}}] {{.Title}}"},{"type":"TextBlock","text":"Step {{.Step}} · Policy {{.Policy}}","wrap":true}],"actions":[{"type":"Action.OpenUrl","title":"Acknowledge","url":"{{.AckURL}}"},{"type":"Action.OpenUrl","title":"Open","url":"{{.AlertURL}}"}]}}]}
servicenow / zendesk / jira (description text):
         {{.Severity}}: {{.Title}}

         Alert:  {{.AlertURL}}
         Opened: {{.Alert.OpenedAt}}
         Labels: {{range $k,$v := .Labels}}{{$k}}={{$v}} {{end}}

         Acknowledge: {{.AckURL}}
ticket:  {"subject":{{json .Title}},"severity":"{{.Severity}}","body":{{json .Title}},"labels":{{json .Labels}},"alertUrl":{{json .AlertURL}},"alertId":{{json .Alert.ID}}}
```

:::tip[Set `baseUrl`]
Ack links, alert links and the DTMF callback of voice calls are only rendered when the server knows its public URL — config key `baseUrl` / env `NORTHPLANE_BASE_URL` (see [Configuration](/docs/administration/configuration/)). Without it notifications go out without any link.
:::

## Delivery retries per channel

Deliveries ride the outbox: by default 30 attempts with exponential backoff `30 s · 2^n` (±10 % jitter), capped at 1 h, then the item is dead-lettered. Three config keys on any channel override that for notifications sent through it:

| Key | Accepted | Default |
|---|---|---|
| `retryMaxAttempts` | integer 1–100 | `30` |
| `retryBackoffSeconds` | integer greater than 0 | `30` |
| `retryBackoffCapSeconds` | integer greater than 0; raised to the base if smaller | `3600` |

The UI shows them as the group **Zustellung / Wiederholungen** for every type. They apply to escalation and object notifications only; escalation actions, ticket auto-close and outgoing webhook subscriptions always use the defaults. Details, the formula and the dead-letter queue: [Reliability](/docs/alarming/reliability/).

## Channel reference

All keys are `config` entries (strings). "secret" means the value may be a `$SECRET:name$` reference.

### email

`provider` selects the backend: `smtp` (default when empty), `sendmail`, `resend` or `ses`; anything else is an error. Every backend sends `text/plain; charset=utf-8` with headers `From`, `To`, `Subject` (RFC 2047 when non-ASCII), `Date`, `Message-ID`, `MIME-Version`; CR/LF and control characters are stripped from the addresses and subject. A body that starts with `<!doctype html` or `<html` (scheduled reports) is sent as `text/html`.

**`provider: smtp`** — native SMTP client, STARTTLS or implicit TLS:

| Key | Default | Notes |
|---|---|---|
| `host` | required | also used as TLS server name, AUTH host and `Message-ID` domain |
| `port` | `587` | `465` switches to implicit TLS |
| `from` | `northplane@<host>` | `Display Name <box@example.com>` allowed; the envelope uses the bare address |
| `username` | — | when set, `AUTH PLAIN` |
| `password` | — | secret |
| `tls` | — | `implicit` = TLS from the first byte regardless of port; empty = STARTTLS after EHLO |
| `allowPlaintext` | — | `true` allows sending when the server offers no STARTTLS; otherwise the error is `server offers no STARTTLS (set allowPlaintext=true to override)` |
| `helo` | domain of `from`, else `localhost` | EHLO/HELO name |

Dial timeout 15 s; errors are prefixed `smtp connect:` / `smtp auth:`. For direct-to-MX delivery (no relay) set `from` to an address in a domain you control and, if needed, `helo` to the host name the MX expects — strict receivers answer an EHLO of `localhost` with 550 or without STARTTLS. The `helo` key is not in the UI form; add it under **Weitere Einstellungen** (additional settings).

**`provider: sendmail`** — hands the message to a local MTA binary:

| Key | Default | Notes |
|---|---|---|
| `sendmailPath` | `sendmail` on `PATH`, else `/usr/sbin/sendmail`, `/usr/lib/sendmail`, `/usr/bin/sendmail` | error `sendmail binary not found (set config.sendmailPath)` otherwise |
| `from` | `northplane@localhost` | |

Called as `sendmail -i -f <from> -- <to>` with the message on stdin; a non-zero exit returns the first output line. Not available in the container image (distroless, no MTA).

**`provider: resend`** — the Resend HTTP API:

| Key | Default | Notes |
|---|---|---|
| `apiKey` | required | secret |
| `from` | required | a verified sender |
| `apiBase` | `https://api.resend.com` | |

Returns the Resend message `id` as provider id.

**`provider: ses`** — AWS SES v2 HTTP API with SigV4 signing (no SDK):

| Key | Default | Notes |
|---|---|---|
| `region` | required | e.g. `eu-central-1` |
| `accessKeyId` | required | |
| `secretAccessKey` | required | secret |
| `sessionToken` | — | secret; temporary credentials |
| `from` | required | |
| `endpoint` | `https://email.<region>.amazonaws.com` | |

Returns the SES `MessageId`.

Example (relay with STARTTLS and credentials):

```yaml title="channel-email.yaml"
kind: Channel
metadata: { name: mail-relay }
spec:
  type: email
  enabled: true
  config:
    provider: smtp
    host: smtp.example.com
    port: "587"
    from: "Northplane Alarm <alarm@example.com>"
    username: alarm@example.com
    password: $SECRET:smtp-pass$
```

### sms

`provider`: `twilio` or `generic-http`; an empty provider means `generic-http`. The SMS text is the rendered template (default `[SEV] <title, max 100 chars> ack: <ackUrl>`).

**`provider: twilio`**

| Key | Default | Notes |
|---|---|---|
| `accountSid` | required | literal (not a secret reference) |
| `authToken` | — | secret; used with `accountSid` as HTTP basic auth unless an API key is set |
| `apiKeySid` + `apiKeySecret` | — | preferred: when `apiKeySid` is set, basic auth uses the key SID and `apiKeySecret` (secret) |
| `from` | required | sender number (E.164) or messaging sender |
| `apiBase` | `https://api.twilio.com` | |

Calls `POST {apiBase}/2010-04-01/Accounts/{accountSid}/Messages.json` with `To`, `From`, `Body`; the Twilio message `sid` becomes the provider id; HTTP ≥ 300 is an error with the first line of Twilio's response.

**`provider: generic-http`** — for SMS gateways and GSM modems with an HTTP API (SMSEagle, Teltonika and similar):

| Key | Notes |
|---|---|
| `url` | required; `GET` with `{to}` and `{text}` placeholders, URL-query-escaped |
| `jsonBody` | if set: `POST` with `Content-Type: application/json` and `{to}`/`{text}` replaced **unescaped** inside this template (placeholders in `url` are then not replaced) |
| `username` / `password` | HTTP basic auth; `password` is a secret |

HTTP ≥ 300 → `sms gateway: HTTP <code>`; no provider id.

```yaml title="channel-sms.yaml"
kind: Channel
metadata: { name: alarm-sms }
spec:
  type: sms
  enabled: true
  config:
    provider: twilio
    accountSid: ACxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
    apiKeySid: SKxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
    apiKeySecret: $SECRET:twilio-api-secret$
    from: "+15551234567"
    retryMaxAttempts: "5"
    retryBackoffSeconds: "30"
```

### voice

`provider`: `twilio`, `asterisk` or `generic-http`. With an **empty** provider the channel behaves as `twilio` when `accountSid` is set, otherwise as `generic-http`. Two things apply to every provider:

- The spoken text is the rendered template (default `Northplane alert. Severity {{.Severity}}. {{.Title}}. Press 4 to acknowledge, 6 to resolve.`), **unless** the alert carries the label `np.tts` — then that label's value is spoken instead (set it with a rule's `setLabels`, an IVR option or the manual trigger).
- **Who speaks:** with a [TTS profile](/docs/alarming/text-to-speech/) — the channel key `ttsProfile`, the alert label `np.ttsProfile`, or a profile named `default` — Northplane synthesizes the text (language detection, pronunciation lexicon, chosen engine/voice) and the provider plays the clip; otherwise, or when every engine fails, the provider speaks as described per provider below.
- For real alerts (not tests) and when `baseUrl` is configured, a signed callback URL `<baseUrl>/api/v1/voice/gather/<token>` (valid 24 h) is produced; pressing **4** acknowledges, **6** resolves. The mechanics are on [Voice calls and IVR](/docs/alarming/voice-and-ivr/).

**`provider: twilio`**

| Key | Default | Notes |
|---|---|---|
| `accountSid`, `authToken` / `apiKeySid` + `apiKeySecret`, `from`, `apiBase` | as for SMS | `from` is the caller id |
| `language` | `en-US` | TwiML `<Say language>` tag, e.g. `de-DE` |
| `ttsProfile` | `default` (if it exists) | TTS profile; the clip is `<Play>`ed instead of `<Say>`d |

Places `POST {apiBase}/2010-04-01/Accounts/{sid}/Calls.json` with inline TwiML that speaks the text twice inside a one-digit `<Gather>` (10 s) pointing at the callback URL; without a callback URL the text is just spoken twice. Returns the call `sid`. A `voice` key (TTS voice name) is **not** read for outbound calls — it only applies to inbound IVR menus.

**`provider: asterisk`** — AMI `Originate` into your own dialplan, fully on-prem:

| Key | Default | Notes |
|---|---|---|
| `host` | required | AMI host |
| `port` | `5038` | |
| `username` | required | AMI manager user |
| `secret` | required | secret |
| `channel` | required | originate channel template with `{to}`, e.g. `PJSIP/{to}@trunk` |
| `context` / `exten` / `priority` | `northplane-alert` / `s` / `1` | dialplan entry point (unless `application` is set) |
| `application` / `appData` | — | run one application instead of a context, e.g. `Playback` / `alert-sound` |
| `callerId` | — | e.g. `Northplane <8000>` |
| `timeoutMs` | `30000` | ring timeout passed to `Originate` |
| `tls` | — | `on` = AMI over TLS (TLS 1.2+, Asterisk `tlsenable`) |
| `insecure` | — | `true` skips certificate verification |
| `ttsProfile` | `default` (if it exists) | TTS profile; the clip travels as `NP_AUDIO_URL` / `NP_AUDIO_FILE` |
| `ttsDir` / `ttsDirPBX` | — | directory shared with the PBX for synthesized clips, and the same directory as the PBX sees it |

The call carries the channel variables `NP_TEXT` (spoken text), `NP_SEVERITY` (`CRITICAL`, …) and `NP_ACK_URL` (the gather callback) — plus `NP_AUDIO_URL`, `NP_AUDIO_FILE`, `NP_LANG`, `NP_TEXT_SPOKEN` with a TTS profile — so the dialplan can speak and acknowledge; a dialplan example is on [Voice calls and IVR](/docs/alarming/voice-and-ivr/). Connection: 10 s dial, 20 s overall deadline, `Login` with `Events: off`, `Originate` with `Async: true`, best-effort `Logoff`; a rejected originate fails with `originate rejected: <message>`. No provider id.

**`provider: generic-http`** — HTTP voice gateways: `url` with `{to}`/`{text}`/`{audioUrl}` (GET, escaped) or `jsonBody` (POST JSON, unescaped), `username`/`password` (secret) basic auth; HTTP ≥ 300 is an error. `{audioUrl}` is the synthesized clip's signed URL with a TTS profile (empty otherwise), and `{text}` then carries the normalised text.

```yaml title="channel-voice.yaml"
kind: Channel
metadata: { name: alarm-voice }
spec:
  type: voice
  enabled: true
  config:
    provider: twilio
    accountSid: ACxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
    apiKeySid: SKxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
    apiKeySecret: $SECRET:twilio-api-secret$
    from: "+15551234567"
    language: de-DE
---
kind: Channel
metadata: { name: pbx-voice }
spec:
  type: voice
  enabled: false          # only one enabled voice channel is ever used
  config:
    provider: asterisk
    host: pbx.example.internal
    username: northplane
    secret: $SECRET:ami-secret$
    channel: "PJSIP/{to}@trunk"
    context: northplane-alert
    callerId: "Northplane <8000>"
```

### push

One channel type serves three transports, chosen per device by the registered endpoint: `fcm://<token>` → Firebase Cloud Messaging (HTTP v1), `apns://<token>` → Apple Push Notification service, `https://…` → Web Push (RFC 8291, VAPID). The target is the contact's `userId`; all subscriptions registered under that id receive the push. Device registration, payloads and the alarm-app contract are on [Mobile push](/docs/alarming/mobile-push/).

| Key | Used by | Notes |
|---|---|---|
| `fcmServiceAccount` | FCM | Firebase service-account JSON (`project_id`, `client_email`, `private_key`, `token_uri`) — secret recommended |
| `fcmEndpoint` | FCM | override for tests; default `https://fcm.googleapis.com/v1/projects/<project_id>/messages:send` |
| `apnsKey` | APNs | the `.p8` key (PKCS#8 EC PEM) — secret recommended |
| `apnsKeyId` | APNs | 10-character key id |
| `apnsTeamId` | APNs | Apple team id |
| `apnsTopic` | APNs | the app's bundle id |
| `apnsSandbox` | APNs | `true` → `api.sandbox.push.apple.com`; default production |
| `apnsEndpoint` | APNs | override for tests |

Web Push needs no config (the VAPID key pair is generated server-side). Result string `delivered=<n>`; an error is only returned when no subscription could be delivered. Provider answers meaning "device gone" (FCM 404 / `UNREGISTERED`, APNs 410 / `BadDeviceToken` / `Unregistered`, Web Push 404/410) delete the subscription.

### ntfy

| Key | Default | Notes |
|---|---|---|
| `url` | **set it** (`https://ntfy.sh` or your server) — an empty `url` fails the target check before the code default applies | server base |
| `topic` | required | path-escaped |
| `token` | — | secret; `Authorization: Bearer` |

`POST <url>/<topic>` with the rendered body (default `{{.Title}}`) and headers `Title: [SEV] Northplane`; `Priority: urgent` + `Tags: rotating_light` for CRITICAL, `Priority: high` + `Tags: warning` for WARNING; `Actions: view, Acknowledge, <ackUrl>` when an ack URL exists. HTTP ≥ 300 → `ntfy: HTTP <code>`.

### slack and teams

| Key | Notes |
|---|---|
| `url` | required; the incoming-webhook URL (literal — see [Secrets in channel config](#secrets-in-channel-config)) |

Body = rendered template, which **must be valid JSON** (default: a Slack Block Kit message with an *Acknowledge* button / a Teams Adaptive Card with *Acknowledge* and *Open* actions), `Content-Type: application/json`, no extra auth. Errors include the HTTP status and the first line of the response.

### webhook

| Key | Notes |
|---|---|
| `url` | required |
| `username` / `password` | HTTP basic auth; `password` is a secret |
| `token` | secret; `Authorization: Bearer <token>` |
| `secret` | secret; adds `X-Northplane-Signature: sha256=<hex HMAC-SHA256(secret, body)>` |

Always `POST`, `Content-Type: application/json`, `User-Agent: Northplane-Webhook/1.0`, body = rendered template (default: the JSON document shown under [Message templates](#message-templates)); HTTP ≥ 300 → `webhook: HTTP <code>`; the response header `X-Request-Id` becomes the provider id. The `method` field offered by the UI is **not read** by the server — it always posts.

:::note[Webhook channel vs. webhook subscription]
A `webhook` **channel** is a notification target for escalation steps and posts the channel template per contact/step. An outgoing **webhook subscription** (`/api/v1/webhooks`, Admin → Webhooks) forwards raw events (`alert_opened`, `notification`, …) independent of any policy. See [Outgoing webhooks](/docs/alarming/webhooks-out/).
:::

### mqtt

| Key | Default | Notes |
|---|---|---|
| `url` | required | `tcp://host:1883`, `ssl://host:8883`, `ws://…`, `wss://…` |
| `topic` | required | `{severity}` (lower-case) and `{alertId}` expand |
| `username` / `password` | — | `password` is a secret |
| `qos` | `1` | `0`, `1` or `2` |
| `retain` | — | `true` retains the message |
| `clientId` | `northplane-notify` | same default for every channel and instance — give each publisher its own id, two clients with the same id evict each other on the broker |
| `tlsInsecure` | — | `true` skips certificate verification |

One-shot publish per delivery (10 s connect/write timeout, 15 s wait, no auto-reconnect — the outbox retries), body = rendered template (default JSON), provider id = the resolved topic. Consuming MQTT is a separate feature — the `mqtt` event source on [Event sources](/docs/alarming/event-sources/).

### Ticket channels: servicenow, zendesk, jira, ticket

Ticket channels can be used in two ways:

1. as a plain channel type in a step or preference (`channels: [servicenow]`) — one ticket per (contact, step), no parameters, `autoClose` from the channel config;
2. through an escalation **action**, referenced by name with parameters — the recommended way:

```yaml
action:
  ticket: { channel: snow, autoClose: true, params: { assignment_group: NOC, urgency: "1" } }
  # legacy shorthand: ticket action against the tenant's first enabled servicenow channel
  servicenow: { assignmentGroup: NOC, autoClose: true }
```

Common behaviour: subject = `[SEV] Title` (or the template's `Subject:` line); body = the rendered template (text for the three SaaS providers, JSON for `ticket`); the created reference is stored on the alert (`alert.ticket = {channel, type, ref, url, autoClose}`) and mirrored into the linked incident's `ticketUrl` if that is empty; a repeat step does not open a second ticket while `alert.ticket.ref` is set; an action's `autoClose` **replaces** the channel's `autoClose=true`. When an alert with `autoClose` is resolved by any path, a close call is queued (outbox kind `ticket-close`) with the note `Resolved by Northplane: <title> (alert <id>)`.

Authentication keys (all four types): `username` + `password` (secret) → HTTP basic; `email` + `apiToken` (secret) → basic `email/token:apiToken` (Zendesk style); `token` (secret) → `Authorization: Bearer` (set last, wins). Requests carry `Content-Type`/`Accept: application/json` and `User-Agent: Northplane-Ticket/1.0`.

| Type | Create | Reference / URL | Close (`autoClose`) | Type-specific keys |
|---|---|---|---|---|
| `servicenow` | `POST <url>/api/now/table/<table>` with `short_description`, `description`, `urgency`/`impact` (`1` CRITICAL, `2` WARNING, else `3`), `correlation_id=<alertId>`, `assignment_group` and all `params` | `sys_id`; `<url>/<table>.do?sys_id=<sys_id>` | `PATCH …/<table>/<sys_id>` with `state`, `close_code`, `close_notes` | `url` (required, `https://<instance>.service-now.com`), `table` (`incident`), `closeState` (`6`), `closeCode` (`Solution provided`) |
| `zendesk` | `POST <url>/api/v2/tickets.json` with `subject`, `priority` (`urgent`/`high`/`normal`), `comment.body`, `tags: [northplane]`, `external_id=<alertId>`, plus `params` | numeric id; `<url>/agent/tickets/<id>` | `PUT …/tickets/<id>.json` with `status` and a private comment | `url` (required, `https://<subdomain>.zendesk.com`), `email` + `apiToken`, `closeStatus` (`solved`) |
| `jira` | `POST <url>/rest/api/2/issue` with `project.key`, `summary`, `description`, `issuetype.name`, `labels: [northplane]`, plus `params` | issue key; `<url>/browse/<key>` | optional `POST …/issue/<key>/transitions` (`closeTransitionId`), then always a comment | `url`, `project` (both required), `issueType` (`Task`), `closeTransitionId` |
| `ticket` (generic) | `POST <url>` with the rendered JSON template (must be valid JSON) | id read from the response via `refField` (dot path, default `id`, string or number); URL from `ticketUrlTemplate` with `{ref}` | `closeUrl` with `{ref}` (unset → no close call), `closeMethod` (`POST`), `closeBody` (default `{"status":"closed","note":…}`; `{ref}`/`{note}` placeholders) | `url` (required) |

All four accept `autoClose` (`"true"`). Example:

```yaml title="channel-servicenow.yaml"
kind: Channel
metadata: { name: snow }
spec:
  type: servicenow
  enabled: true
  config:
    url: https://acme.service-now.com
    username: northplane
    password: $SECRET:snow-pass$
    table: incident
    autoClose: "true"
```

## Complete bundle example

```yaml title="channels.yaml"
kind: Channel
metadata: { name: alarm-sms }
spec:
  type: sms
  enabled: true                  # REQUIRED — omitted = disabled
  config:
    provider: twilio
    accountSid: ACxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
    apiKeySid: SKxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
    apiKeySecret: $SECRET:twilio-api-secret$
    from: "+15551234567"
    retryMaxAttempts: "5"
    retryBackoffSeconds: "30"
---
kind: Channel
metadata: { name: ntfy-push }
spec:
  type: ntfy
  enabled: true
  config: { url: https://ntfy.sh, topic: my-alarm-topic, token: $SECRET:ntfy$ }
---
kind: Channel
metadata: { name: mqtt-out }
spec:
  type: mqtt
  enabled: true
  config: { url: "tcp://broker:1883", topic: "northplane/alerts/{severity}", qos: "1", retain: "false", clientId: np-main }
---
kind: Channel
metadata: { name: ops-slack }
spec:
  type: slack
  enabled: true
  config: { url: "https://hooks.slack.com/services/T000/B000/XXXX" }
---
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
---
kind: Channel
metadata: { name: snow }
spec:
  type: servicenow
  enabled: true
  config: { url: https://acme.service-now.com, username: np, password: $SECRET:snow$, table: incident, autoClose: "true" }
```

Apply with `np apply -f channels.yaml` or **Admin → Config bundles**; see [Config bundles](/docs/administration/config-bundles/). Bundle `Channel` documents are applied after `Contact`/`ContactGroup` and before `Schedule`, `IVRMenu`, `EscalationPolicy`.

## Seeing what was delivered

Every delivery attempt — escalation notification, object notification, executed action — writes an immutable event of type `notification` whose payload is a `NotificationRecord`: `alertId`, `stepIndex`, `repeat`, `contactId`, `contact`, `channel` (type), `channelId`, `target` (masked: first 3 + `…` + last 3 characters), `status` (`sent`, `failed`, or `suppressed` when the alerting engine blocked the chain), `attempt`, `error`, `providerId`, `latencyMs`. Filter the **Events** page by type `notification`, or query `GET /api/v1/events?types=notification`; each escalation step additionally writes an `escalation` event. There is no per-channel health endpoint — the events (plus `np_notifications_total{result="sent|failed|dead"}` on `/metrics` and the `notify` block of `GET /api/v1/system/health`) are the delivery audit. Items that exhausted their retries appear under **Admin → Dead letters** and can be replayed; see [Reliability](/docs/alarming/reliability/).
