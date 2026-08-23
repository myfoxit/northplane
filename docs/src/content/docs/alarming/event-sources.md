---
title: Event sources
description: Reference for every inbound event source type — the EventSource resource, auth modes, rate limits, CEL mapping, the normalized event, and per-type configuration keys with examples.
sidebar:
  order: 2
---

An **event source** is an inbound adapter instance: it authenticates and rate-limits what comes in, normalizes it into one event shape, stamps labels, and publishes the result as an `ingress` event that your [alert rules](/docs/alarming/alert-rules/) evaluate. HTTP-style sources (webhook, Alertmanager, Twilio voice/SMS) are addressed by URL; listener-style sources (SNMP traps, ESPA, Asterisk AGI) open a port; client-style sources (IMAP, MQTT) connect out.

Event sources are managed in **Admin → Event sources (Event-Quellen)**, through `/api/v1/event-sources` (generic resource CRUD: `objects:read` to list, `config:write` to change, `If-Match` on `PUT`), or as bundle kind `EventSource`.

## The EventSource resource

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Unique per tenant. Usable in ingest URLs instead of the id. |
| `type` | string | One of `webhook`, `alertmanager`, `email` (alias `imap`), `snmp-trap`, `mqtt`, `espa`, `espa-x`, `voice-inbound`, `sms-inbound`, `asterisk-inbound`. The type is not validated on save; unknown types are simply never served. |
| `enabled` | bool | Must be `true` for the source to accept anything (see below). |
| `authMode` | `token` \| `hmac` \| `basic` \| `none` | HTTP authentication for webhook, Alertmanager, voice and SMS webhooks. Empty or unknown values behave like `token`. |
| `secretRef` | string | Name of a secret in the [secret store](/docs/administration/secrets/) (**Admin → Secrets**). Required for `token`, `hmac` and `basic` — without a resolvable secret every request is rejected. |
| `mapping` | map field → CEL | Normalization expressions for `webhook` and `mqtt` payloads (see [CEL mapping](#cel-mapping)). |
| `config` | map string → string | Transport-specific keys, listed per type below. Values are strings: quote numbers and booleans in YAML (`port: "993"`, `markSeen: "false"`). |
| `rateLimit` | float, events/s | Token bucket per source; `0` = default **50/s**. |
| `burst` | int | Bucket size; `0` = default **200**. |
| `labels` | map | Merged into every event this source produces (precedence differs per type, see [Label precedence](#label-precedence)). |

:::caution[`enabled` defaults to false]
The API and the bundle applier store the document as-is; there is no default injection. A source created through the API or a bundle without `enabled: true` is **disabled**: HTTP ingest answers `403 np:ingress/disabled`, and the IMAP/MQTT/trap/ESPA/AGI managers do not start a listener or poller for it. The UI dialog pre-sets the switch to on. One exception: the Alertmanager receiver (`/ingest/{source}/alertmanager`) does not check `enabled` at all — only authentication.
:::

### Authentication (`authMode`)

| Mode | What the request must carry |
|---|---|
| `none` | Nothing. |
| `token` (default) | `Authorization: Bearer <secret>` **or** query `?token=<secret>`; constant-time comparison against the secret named by `secretRef`. |
| `hmac` | `X-Northplane-Signature: [sha256=]<hex>` (fallback: GitHub-style `X-Hub-Signature-256`) — lowercase hex HMAC-SHA256 over the raw request body with the secret as key. |
| `basic` | HTTP Basic; only the **password** is compared with the secret, the username is ignored. |

Practical notes:

- The UI creates new sources with `authMode: none`; documents created via API/bundle without `authMode` default to `token`. Either way, decide explicitly.
- `?token=` ends up in proxy and access logs — prefer the header or `hmac`.
- Do not use an ingest secret that starts with `np_`: the API middleware would treat it as a Northplane API token and answer `401 np:auth/invalid` before the source is even looked up.
- If the secret store has no master key, every secret resolves empty and `token`/`hmac`/`basic` sources reject everything.
- Twilio sources additionally verify `X-Twilio-Signature` when `config.twilioAuthToken` is set, and can restrict callers with `allowFrom` — see [Voice and IVR](/docs/alarming/voice-and-ivr/).

### Rate limits

Every source has a token bucket keyed by its id (`rateLimit` events/s, `burst` capacity, defaults 50 and 200). HTTP ingest answers `429 np:ingress/rate` with `Retry-After: 5` and counts `np_ingress_dropped_total{reason="rate"}`; the Alertmanager receiver stops processing the remaining alerts of that request silently (still `202`); voice/SMS webhooks answer `429`; trap, ESPA and MQTT listeners drop silently (logged at debug level).

### Label precedence

Which labels an event carries, and who wins when keys collide (the right-hand side overwrites):

| Type | Labels stamped by the adapter | Precedence |
|---|---|---|
| `webhook` | only mapped `labels.*` | mapped ⊕ **source labels** |
| `alertmanager` | the Alertmanager alert's labels | AM labels ⊕ **source labels** |
| `email` / `imap` | `source` (source name), `from` | adapter ⊕ **source labels** |
| `snmp-trap` | `source`, `agent`, `trapOid`, one label per varbind OID | source labels ⊕ **trap labels** |
| `mqtt` | `topic` (+ mapped) | mapped ⊕ source labels ⊕ **`topic`** |
| `espa` / `espa-x` | `source`, `espa.*` | source labels ⊕ **adapter labels** |
| `sms-inbound` (`action: event`) | `from`, `to` | adapter ⊕ **source labels** |
| `voice-inbound` (alert labels) | `caller`, `called`, `callSid` (+ `recordingUrl`, `transcript` later) | adapter ⊕ source labels ⊕ **IVR option labels** |
| `asterisk-inbound` (alert labels) | `caller`, `via=asterisk` (+ `recordingFile`) | adapter ⊕ source labels ⊕ **IVR option labels** |

:::note[`labels.source` is not universal]
Only e-mail, SNMP-trap and ESPA sources stamp `labels.source`. For webhook, Alertmanager, MQTT and SMS sources either put an identifying label into the source's own `labels` (e.g. `labels: { source: grafana }`) or match on `event.source`, which always holds the source **id** (a UUID), never the name.
:::

## The normalized event

Every adapter produces a `NormEvent`, which is stored as the `payload` of an `Event` of type `ingress`:

| Field | Set to |
|---|---|
| `source` | the EventSource **id** |
| `receivedAt` | receive time (also the event's `ts`) |
| `dedupKey` | adapter- or mapping-specific; folds repeats into one alert when a rule uses the default dedup key |
| `severity` | `critical` \| `warning` \| `info` \| `ok` — from the adapter mapping or the source's `config.severity` default |
| `summary` | human-readable title |
| `labels` | see the precedence table |
| `payload` | the archived original body (JSON object, or a JSON string for plain text) |
| `resolve` | `true` = this event clears the open alert that holds the same dedup key |

The surrounding event carries `id`, `tenantId`, `ts`, `type: "ingress"`, `sourceId` and `severity` (an empty severity becomes `info`). In CEL rules the NormEvent's fields are addressable as `event.summary`, `event.severity`, `event.labels.<k>`, `event.dedupKey`, `event.source`, and the keys of the archived original body are hoisted to `event.payload.<key>` when it is a JSON object (NormEvent's own keys win on collision) — e.g. `event.payload.subject` for mail, `event.payload.title` for a webhook body. See the [CEL environment](/docs/alarming/alert-rules/#the-cel-environment).

All ingress events are visible in **Events** (filter type `ingress`) and via `GET /api/v1/events?types=ingress&sourceId=<id>`.

## HTTP ingest endpoint

`POST /api/v1/ingest/{source}` — `{source}` is the source **name or id**, resolved across all tenants (ingest URLs carry no tenant). The endpoint is registered outside RBAC and outside the generated OpenAPI document; the source's `authMode` is the only authentication. Processing order and outcomes:

| Step | Failure → response |
|---|---|
| 1. Resolve the source | `404 np:ingress/unknown-source` |
| 2. `enabled` check | `403 np:ingress/disabled` |
| 3. Read body (max 1 MiB) | `413 np:ingress/size` |
| 4. `authMode` check | `401 np:ingress/auth` |
| 5. Rate limit | `429 np:ingress/rate`, `Retry-After: 5` |
| 6. Normalize (identity or CEL) | `422 np:ingress/mapping` (detail names the field) |
| 7. Persist + publish | **`202 Accepted`**, empty body; metric `np_ingress_events_total{type="webhook"}` |

The handler does not check the source's `type`: any enabled source accepts normal-form posts here. Errors are RFC 9457 problem documents, see [API overview](/docs/reference/api-overview/).

Without a `mapping` the body must already be in normal form:

```bash
curl -s -X POST https://np.example.com/api/v1/ingest/ci \
  -H 'Authorization: Bearer <secret>' -H 'Content-Type: application/json' \
  -d '{"summary":"deploy failed","severity":"critical","dedupKey":"deploy-42",
       "labels":{"service":"checkout"},"resolve":false}'
# HTTP/1.1 202 Accepted
```

A missing `summary` becomes `event from <source name>`; the raw body is archived as `payload`; invalid JSON → `422`. An unknown `severity` string is stored as given in this branch (only an empty one is corrected to `info`), so send one of the four valid values.

## CEL mapping

When `mapping` is set (webhook and MQTT sources), the body must be JSON of any shape. It is bound to a single CEL variable **`payload`** and each mapping entry is evaluated per request (cost limit 5000):

| Target key | Result |
|---|---|
| `summary` | stringified |
| `severity` | string; an invalid value becomes `info` |
| `dedupKey` | stringified |
| `resolve` | must evaluate to `bool`; anything else counts as `false` |
| `labels.<key>` | stringified label value, any key |
| anything else | ignored |

A compile or evaluation error — including a missing key on the payload — fails the request with `422 np:ingress/mapping` (`mapping <field>: <error>`). The raw body is still archived in `payload`, so rules can reach fields you did not map via `event.payload.<key>`.

```json
{ "name": "grafana", "type": "webhook", "enabled": true,
  "authMode": "hmac", "secretRef": "grafana-hmac",
  "mapping": {
    "summary":  "payload.title",
    "severity": "payload.state == 'alerting' ? 'critical' : 'ok'",
    "dedupKey": "string(payload.ruleId)",
    "resolve":  "payload.state == 'ok'",
    "labels.dashboard": "payload.ruleName"
  },
  "labels": { "source": "grafana" } }
```

The UI shows the **Mapping (CEL)** editor only for `webhook` sources; for `mqtt` set `mapping` through the API or a bundle.

## Source types

### `webhook`

Generic JSON receiver. No `config` keys — only `authMode`/`secretRef`, optional `mapping`, `rateLimit`/`burst`, `labels`. Ingest URL `POST /api/v1/ingest/<name>` (shown in the UI). Normal-form bodies need no mapping; anything else needs one (see above).

```yaml
kind: EventSource
metadata: { name: grafana }
spec:
  type: webhook
  enabled: true
  authMode: hmac
  secretRef: grafana-hmac
  mapping:
    summary: payload.title
    severity: "payload.state == 'alerting' ? 'critical' : 'ok'"
    dedupKey: string(payload.ruleId)
    resolve: "payload.state == 'ok'"
  labels: { source: grafana }
```

### `alertmanager`

Prometheus Alertmanager v2 webhook receiver at `POST /api/v1/ingest/{source}/alertmanager`. No `config` keys; `authMode`/`secretRef` apply; `mapping` is not used. Body `{"alerts":[{"status","labels","annotations","fingerprint"}]}` (other top-level fields ignored); non-JSON → `422 np:ingress/format`; always `202`; metric type `alertmanager`. One event per Alertmanager alert:

| NormEvent field | Value |
|---|---|
| `dedupKey` | `am-<fingerprint>` |
| `severity` | from `labels.severity`: `critical`/`page` → critical, `info`/`none` → info, anything else → warning; forced to `ok` when resolved |
| `summary` | `annotations.summary`, else `labels.alertname` |
| `labels` | the alert's labels ⊕ source labels |
| `resolve` | `true` when `status == "resolved"` |
| `payload` | the single Alertmanager alert object |

```yaml title="alertmanager.yml (receiver)"
receivers:
  - name: northplane
    webhook_configs:
      - url: https://np.example.com/api/v1/ingest/prom/alertmanager
        send_resolved: true
        http_config:
          authorization: { credentials: <secret> }
```

:::caution
This receiver does not check the source's `enabled` flag — disabling the source does not stop Alertmanager posts; change the secret or delete the source instead.
:::

### `email` / `imap`

An IMAP poller (one goroutine per enabled source; reconciled every 30 s; restarted when the config changes). Type value `imap` or `email`, case-insensitive.

| Key | Default | Notes |
|---|---|---|
| `host` | — (required) | |
| `port` | `993` with TLS, `143` plain | |
| `tls` | `on` | `on`/`implicit`/`true` = implicit TLS; `off`/`none`/`false`/`plain` = plaintext. **`starttls` is not supported** (config error). |
| `username` | — | IMAP `LOGIN` |
| `passwordSecretRef` | — | secret name (preferred) |
| `password` | — | inline fallback |
| `folder` | `INBOX` | |
| `pollInterval` | `60s` | Go duration; minimum `15s` is enforced |
| `markSeen` | `true` | anything but `false` = true. With `false`, `SEARCH UNSEEN` returns the same messages on every poll (events repeat; alerts fold on the dedup key). |
| `severity` | `warning` | default when the subject carries no severity token |

Each poll: connect, `SEARCH UNSEEN`, fetch up to 200 messages, publish, then flag `\Seen` (only after a successful publish). Event construction: `summary` = decoded `Subject` (or `(no subject)`); severity from a whole-word subject token — `critical` → critical, `ok`/`resolved` → ok (and `resolve: true`), `warning` → warning, else the default; `dedupKey` = `<source name>/<Message-ID>` (or a 16-hex-char hash of subject + date when there is no Message-ID); labels `source=<name>`, `from=<bare address>` ⊕ source labels; `payload = {subject, from, date, messageId, body}` with `body` = the first `text/plain` part (decoded, ≤ 4000 characters). Attachments and HTML-only bodies are ignored.

```yaml
kind: EventSource
metadata: { name: ops-mailbox }
spec:
  type: imap
  enabled: true
  config: { host: imap.example.com, port: "993", tls: "on", username: ops@example.com,
            passwordSecretRef: imap-pass, folder: INBOX, pollInterval: 60s, severity: warning }
```

Typical rules: `event.payload.subject.matches('(?i)feuer|brand')`, `event.payload.from == 'leitstelle@example.org'`, `event.payload.body.contains('Zone 12')`.

### `snmp-trap`

A UDP trap listener. Northplane opens one socket per distinct `listen` address across all tenants and sources; the community string (v1/v2c) or the USM user (v3) selects the source. Reconciled every 30 s.

| Key | Default | Notes |
|---|---|---|
| `listen` | `udp://:9162` | several sources may share an address |
| `community` | `public` | v1/v2c match |
| `severity` | `warning` | for enterprise-specific / unclassified traps |
| `v3User` | — | enables SNMPv3 on that listener |
| `v3SecLevel` | inferred | `noAuthNoPriv` \| `authNoPriv` \| `authPriv`; inferred from which passphrases are set |
| `v3AuthProto` | `SHA` | `MD5`, `SHA`, `SHA224`, `SHA256`, `SHA384`, `SHA512` |
| `v3AuthPass` | — | authentication passphrase (inline) |
| `v3PrivProto` | `AES` | `DES`, `AES`, `AES192`, `AES256` |
| `v3PrivPass` | — | privacy passphrase (inline) |

Normalization: generic v1 traps are mapped per RFC 2576; `coldStart`/`warmStart`/`linkUp` → `ok`, `linkDown` → `critical`, `authenticationFailure`/`egpNeighborLoss` → `warning`, anything else → `config.severity`. `summary` = `SNMP <name> from <agent IP>` or `SNMP trap <oid> from <ip>`; labels `source`, `agent`, `trapOid` plus one label per varbind OID (max 20, values truncated to 200 chars) — trap labels win over source labels; `dedupKey` = `<source name>/<agent IP>/<trap OID>`; `payload = {version, trapOid, community, varbinds:[{oid,type,value}]}` (≈ 16 KiB cap).

:::caution[SNMPv3 passphrases: UI keys differ from the listener]
The admin dialog writes `v3AuthSecretRef` / `v3PrivSecretRef`, but the trap listener reads only `v3AuthPass` / `v3PrivPass` and never resolves secrets. Provide the passphrases under those two keys — through the dialog's **Weitere Einstellungen** key/value editor, the API, or a bundle.
:::

```yaml
kind: EventSource
metadata: { name: cisco-traps }
spec:
  type: snmp-trap
  enabled: true
  config: { listen: "udp://:9162", community: lab-traps, severity: warning }
  labels: { site: lab }
```

Polling, OIDs and Cisco examples: [SNMP](/docs/monitoring/snmp/).

### `mqtt`

One broker connection per enabled source (auto-reconnect, resubscribe on every connect, clean session, keep-alive 60 s); reconciled every 30 s and restarted when any connection-relevant key changes.

| Key | Default | Notes |
|---|---|---|
| `url` | — (required) | `tcp://`, `mqtt://`, `ssl://`, `tls://`, `mqtts://`, `ws://`, `wss://` |
| `topics` | — (required) | comma-separated topic filters, e.g. `factory/#,plant2/+/fire` |
| `qos` | `1` | 0, 1 or 2 |
| `clientId` | `northplane-<first 8 chars of the source id>` | two connections with the same id evict each other on the broker — set distinct ids when several instances subscribe |
| `username` / `password` / `passwordSecretRef` | — | secret ref preferred; re-read on every (re)connect |
| `tlsInsecure` | `false` | `true` skips certificate verification on TLS schemes |
| `severity` | `info` | default severity |

Payload handling, in order: (1) `mapping` set and the payload is JSON → CEL mapping as above; (2) no mapping and the payload is JSON with `summary` or `severity` → taken as normal form; (3) otherwise plain text — the first 200 runes become `summary` (`(empty message)` when blank). Then `labels.topic` is **always** set to the message topic (source labels win over mapped labels, `topic` wins over everything). The raw message (≤ 64 KiB) is archived as `payload` (non-JSON as a JSON string, so there is nothing to hoist). No `dedupKey` unless mapped.

```yaml
kind: EventSource
metadata: { name: factory-mqtt }
spec:
  type: mqtt
  enabled: true
  config: { url: "ssl://broker:8883", topics: "factory/#", qos: "1", username: np,
            passwordSecretRef: mqtt-pass, clientId: np-main, severity: info }
```

Rule example: `event.labels.topic == 'factory/hall3/fire'` or `event.labels.topic.startsWith('factory/') && event.severity == 'critical'`.

### `espa` and `espa-x`

TCP listeners for ESPA 4.4.4 (serial-bridge pager protocol) and ESPA-X 2.0 (XML). One listener per `listen` address; **exactly one source may own an address** (duplicates are logged and ignored). Limits per listener: 64 connections, 5-minute idle timeout, 64 KiB frame cap.

| Key | Default | Notes |
|---|---|---|
| `listen` | `tcp://:2023` (`espa`), `tcp://:8123` (`espa-x`) | |
| `severity` | `info` | severity for calls without a recognised priority |

ESPA 4.4.4: Northplane acts as the slave (ENQ → ACK, BCC check); only "call to pager" blocks (function `1`) become events. `summary` = display message (≤ 512 chars) or `ESPA call <address>`; labels `espa.address`, `espa.beep`, `espa.callType`, `espa.priority`; priority `1` → critical, `2` → warning, else default; **no `dedupKey`** (every call is a fresh event); `payload = {function:"1", records:{…}}`.

ESPA-X: answers `REQ.LOGIN`, `REQ.HEARTBEAT` and `REQ.P-CALL` with `REP.* state="ok"`; `summary` = `DISPLAY-MSG`/`TEXT-MSG`; labels `espa.callId`, `espa.address`, `espa.prio`, `espa.signal`; `CALL-PRIO` containing `alarm` or `1` → critical, `high` or `2` → warning, else default; `dedupKey = espa-x/<CALL-ID>` (retransmits fold); `payload = {callId, address, message, prio, signal, xml}`.

Both stamp `labels.source` = source name; protocol labels win over source labels.

```yaml
kind: EventSource
metadata: { name: nurse-call }
spec: { type: espa-x, enabled: true, config: { listen: "tcp://:8123", severity: warning } }
```

Rule example: `event.labels["espa.priority"] == "1"` (bracket syntax because of the dot in the key).

### `voice-inbound`

A Twilio voice webhook: callers reach an IVR menu that can raise alarms, list, acknowledge or resolve them. Webhook URL `POST /api/v1/voice/inbound/{source}` (`{source}` = id or name; the UI shows the id form with `?token=`). The alarm this source raises is a **manual alert** (no rule involved).

| Key | Default | Notes |
|---|---|---|
| `menu` | built-in | IVR menu resource name; the built-in menu is 1 = raise alarm + record, 2 = list open alarms, 3 = acknowledge |
| `language` | `en-US` | TTS language; the menu's own `language` wins; prompts are German for any `de*` value |
| `voice` | — | provider voice, e.g. `Polly.Vicki` |
| `allowFrom` | all | comma-separated E.164 prefixes; others get `403 np:ingress/caller` |
| `escalationPolicy` | — | default policy for menu-raised alarms (an IVR option's policy wins) |
| `severity` | `critical` | default for trigger-alarm options without a severity |
| `twilioAuthToken` | — | value or `$SECRET:name$`; when set, `X-Twilio-Signature` is verified against the public URL (`baseUrl`) |

Plus `authMode`/`secretRef` (use `token` + `?token=`: Twilio re-posts action URLs verbatim), `rateLimit`/`burst`, `labels` (merged into the raised alerts). Flow, menus, PIN gate, recording and transcription labels: [Voice and IVR](/docs/alarming/voice-and-ivr/).

```yaml
kind: EventSource
metadata: { name: alarm-line }
spec:
  type: voice-inbound
  enabled: true
  authMode: token
  secretRef: alarm-line-token
  config: { menu: alarm-menu, language: de-DE, allowFrom: "+49,+43", escalationPolicy: call-alarm,
            severity: critical, twilioAuthToken: $SECRET:twilio-auth$ }
```

### `sms-inbound`

A Twilio messaging webhook: `POST /api/v1/sms/inbound/{source}`. Same auth, `allowFrom`, rate limit and Twilio signature handling as voice.

| Key | Default | Notes |
|---|---|---|
| `action` | `event` | `event` = publish a normalized `ingress` event (rules decide); `alert` = raise a manual alert directly |
| `escalationPolicy` | — | policy for `action: alert` |
| `severity` | `warning` | invalid values fall back to warning |
| `ackKeyword` | `ACK` | case-insensitive **prefix** match on the message body |
| `language` | `en` | `de*` → German replies |
| `allowFrom`, `twilioAuthToken` | — | as for voice |

A body starting with the keyword from a phone number that belongs to a contact acknowledges the newest open alert (reply `Acknowledged: <title>`); from an unknown number it is rejected. Any other text: `summary` = body (≤ 200 chars, or `SMS alarm from <from>`); with `action: alert` the alert gets labels `from`, `to` ⊕ source labels and dedup key `sms/<MessageSid>`; with `action: event` the event carries labels `{from, to}` ⊕ source labels, `payload = {body, from, to}` and no dedup key. The caller gets `Alarm received.` back; metric `np_ingress_events_total{type="sms"}`.

```yaml
kind: EventSource
metadata: { name: sms-line }
spec:
  type: sms-inbound
  enabled: true
  authMode: token
  secretRef: sms-line-token
  config: { action: alert, escalationPolicy: sms-alarm, ackKeyword: OK, language: de }
```

### `asterisk-inbound`

A FastAGI listener for your own Asterisk/FreePBX — the same IVR menus as `voice-inbound`, without any cloud. One listener per `listen` address; the dialplan routes a call to the source whose **id or name** is the AGI URL path (`agi://<northplane-host>:4573/<id-or-name>`; an empty path works only when the listener serves exactly one source). Max 32 concurrent sessions, 10-minute session timeout.

| Key | Default | Notes |
|---|---|---|
| `listen` | `tcp://:4573` | |
| `menu` | built-in | IVR menu name |
| `language` | `en` | phrase language (`de*` → German); the menu's language wins |
| `ttsApp` | — | Asterisk application that speaks its argument (`Flite`, `ESpeak`, a piper wrapper …). Empty = prompt-file mode using the `np-*` sound files |
| `escalationPolicy` | — | default policy |
| `severity` | `critical` | default severity |
| `allowFrom` | all | caller-id prefixes |
| `recordDir` | `/var/spool/asterisk/recording` | PBX-side directory for recordings (`np-<alertId>.wav`, label `recordingFile`) |

Raised alarms are manual alerts with labels `caller`, `via=asterisk` ⊕ source labels ⊕ option labels and dedup key `agi/<agi_uniqueid>`. Dialplan, prompt-file list and TTS modes: [Voice and IVR](/docs/alarming/voice-and-ivr/).

```yaml
kind: EventSource
metadata: { name: pbx-line }
spec:
  type: asterisk-inbound
  enabled: true
  config: { listen: "tcp://:4573", menu: alarm-menu, language: de-DE, ttsApp: Flite,
            escalationPolicy: call-alarm, recordDir: /var/spool/asterisk/recording }
```

### Heartbeats (dead-man inputs)

Heartbeats are **not** event sources — they are a separate resource (`POST /api/v1/heartbeats`, **Admin → Heartbeats**, not bundled) whose absence of beats produces events. A job calls `POST` or `GET /api/v1/heartbeats/{name}/beat` (permission `objects:write`); when no beat arrives for `expectEvery + grace` the engine flips the heartbeat to `missing` and emits a `heartbeat_missed` event with the heartbeat's severity and `sourceId` = the heartbeat id; the next beat emits a `heartbeat_missed` event with severity `ok` and `resolve: true`. Both pass through the rules, so `event.type == "heartbeat_missed"` is all a rule needs (with the default dedup key the recovery resolves the alert). Details in [Heartbeats](/docs/monitoring/heartbeats/). The type names `heartbeat` and `agent` appear in a comment on the `EventSource` struct but have no adapter — do not create sources with those types.

## Which inputs go through the rules

| Input | Path |
|---|---|
| webhook, Alertmanager, e-mail, SNMP traps, MQTT, ESPA/ESPA-X, SMS with `action: event`, heartbeat events, `POST /api/v1/incidents` | `ingress`/`heartbeat_missed`/`incident_update` event → [alert rules](/docs/alarming/alert-rules/) |
| `voice-inbound`, `asterisk-inbound`, SMS with `action: alert`, `POST /api/v1/alerts`, the UI's **Trigger alarm** | manual alert, created directly, chain started immediately, suppression bypassed |
| check results | `state_change` events → rules (and per-object contact routing) |

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `404 np:ingress/unknown-source` | wrong name/id in the URL — names are resolved across all tenants, the first match by tenant slug wins |
| `403 np:ingress/disabled` | `enabled` is false (see the caution above) |
| `401 np:ingress/auth` | wrong mode, wrong secret, secret store without key, or `authMode` defaulted to `token` on an API-created source |
| `422 np:ingress/mapping` | mapping expression failed or body not JSON; without mapping the body must be normal-form JSON |
| `429 np:ingress/rate` | raise `rateLimit`/`burst` on the source |
| `403 np:ingress/caller` | caller not in `allowFrom` (voice/SMS) |
| events arrive but nothing alerts | the rule does not match — test it with the rule tester against recent events, check `event.labels` vs `event.source`; remember `event.source` is the id |
| listener not running (traps/ESPA/AGI/MQTT/IMAP) | source disabled, address already owned by another ESPA source, or bad config — check the server log (`traps:`, `espa:`, `mailin:`, `mqttin:`, `agi:` prefixes) |

Metrics: `np_ingress_events_total{type="webhook"|"alertmanager"|"sms"}`, `np_ingress_dropped_total{reason="rate"}` on `/metrics`; queue depths in `GET /api/v1/system/health` — see [Observability](/docs/administration/observability/).
