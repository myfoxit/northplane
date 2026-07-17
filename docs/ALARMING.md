# Northplane Alarming Pipelines

Northplane is a full alarm server: **many inputs → event matching → many
outputs**, with acknowledgement, escalation, on-call routing, retries and
a complete audit trail. This document is the operator guide for every
alarming path.

```
INPUTS                        MATCHING                    OUTPUTS
──────                        ────────                    ───────
Phone call (IVR)  ─┐                                   ┌─ Voice call (TTS + IVR ack)
SMS               ─┤                                   ├─ SMS
Webhook           ─┤                                   ├─ E-mail
Alertmanager      ─┤   Event sources → CEL alert       ├─ Mobile push (FCM/APNs)
E-mail (IMAP)     ─┼─▶ rules (regex, labels, header ──▶├─ Web Push / ntfy
SNMP trap         ─┤   matching) → alert → escalation  ├─ Slack / Teams
MQTT              ─┤   policy (on-call schedules,      ├─ Webhook out / MQTT out
ESPA / ESPA-X     ─┤   repeats, retries)               ├─ Tickets (SNOW/Jira/Zendesk)
Heartbeat (dead-man)┤                                  └─ Alarm app (SSE + push)
Web UI / API / App ─┘
```

Everything is configured at runtime via the UI (`Alarmierung`,
`Admin → Quellen/Kanäle`) or the REST API / YAML bundles — no restarts.

## 1. Trigger an alarm by phone call (voice-inbound)

One `EventSource` of type **`voice-inbound`** per phone number or Twilio
SIP domain. Multiple numbers/SIP domains = multiple sources, each with
its own menu, allowlist and policy — this is how multi-SIP setups work.

1. Create the source: Admin → Quellen → Typ `voice-inbound`. Config:
   - `menu` — IVR menu name (below); empty = built-in menu
     (1 = raise alarm + record message, 2 = list open alarms, 3 = ack).
   - `language` — TTS language, e.g. `de-DE` (menu setting wins).
   - `allowFrom` — comma-separated E.164 prefixes (`+4915,+4930`); empty = all.
   - `escalationPolicy` — default policy for phone-raised alarms.
   - `twilioAuthToken` — `$SECRET:twilio-auth$`; when set, the
     `X-Twilio-Signature` of every webhook is verified.
   - Auth mode `token` + secret ref recommended: the webhook URL then is
     `https://<host>/api/v1/voice/inbound/<source-id>?token=<secret>`.
2. In the Twilio console, point the phone number's (or SIP domain's)
   Voice webhook at that URL (HTTP POST).
3. Calls are answered with the menu; "press 1" raises an alert
   (dedup key `call/<CallSid>`, labels `caller`, `called`, `callSid`,
   optional voicemail recording + transcript attached as labels
   `recordingUrl` / `transcript`), starts the escalation policy and
   speaks a confirmation. Known callers (contact phone numbers) are
   recorded as the acting person.

### Fully on-prem: your own Asterisk/FreePBX (no cloud)

The PBX model — a VM with Asterisk terminating your SIP trunks (A1,
Twilio Elastic SIP, any carrier) — is a first-class citizen. Northplane
drives the same IVR menus over **FastAGI**; the dialplan needs one line:

```
[alarm-line]
exten => s,1,AGI(agi://<northplane-host>:4573/<source-id-or-name>)
 same => n,Hangup()
```

1. Create an `EventSource` of type **`asterisk-inbound`** (Admin →
   Quellen). Config: `listen` (default `tcp://:4573`), `menu`,
   `language`, `escalationPolicy`, `severity`, `allowFrom` (caller-id
   prefixes), `recordDir` (PBX path for voicemails, default
   `/var/spool/asterisk/recording`), and `ttsApp`.
2. Point the trunk's inbound route at the `alarm-line` context. Multiple
   numbers/trunks: one `asterisk-inbound` source per line, addressed by
   the AGI URL path.
3. Speech output, two modes:
   - **`ttsApp` set** (e.g. `Flite`, `ESpeak`, or a wrapper around
     [piper](https://github.com/rhasspy/piper) for good German voices):
     all prompts and alert titles are spoken dynamically via
     `EXEC <app> "<text>"`.
   - **`ttsApp` empty**: northplane plays pre-recorded prompt files and
     says digits via `SAY NUMBER` (alert titles are skipped). Record or
     generate these sounds once (e.g.
     `piper -m de_DE-thorsten-medium.onnx -f np-greeting.wav` …) and
     drop them into `/var/lib/asterisk/sounds/`:
     `np-greeting, np-pin, np-pin-bad, np-invalid, np-bye,
     np-alarm-raised, np-record-now, np-recorded, np-no-alerts,
     np-ack-confirm, np-resolve-confirm, np-list-intro, np-opt-trigger,
     np-opt-list, np-opt-ack, np-opt-resolve, np-choose,
     np-sev-critical, np-sev-warning, np-sev-info`.
4. Voicemail recordings stay on the PBX (`recordingFile` label on the
   alert points at the wav).

Outbound stays symmetric: the voice channel's `asterisk` provider
(AMI `Originate`) rings people through the same trunks — end to end
without any cloud dependency.

### IVR menus (`Alarmierung → IVR-Menüs`, kind `ivr-menu`)

```yaml
kind: IVRMenu
spec:
  name: nachtwache
  language: de-DE
  greeting: "Leitstelle Nordwerk."
  pin: "4711"            # optional DTMF gate
  trustCallerId: true    # known contact numbers skip the PIN
  options:
    - digit: "1"
      action: trigger-alarm
      severity: critical
      title: "Telefonalarm von {caller}"
      escalationPolicy: nachtdienst
      record: true                    # voicemail after the beep
      labels: { np.sound: np_klaxon } # alarm-app sound steering
    - digit: "2"
      action: list-alerts
    - digit: "3"
      action: ack-alert               # reads open alarms, ack by digit
    - digit: "4"
      action: resolve-alert
    - digit: "5"
      action: say
      text: "Bereitschaft diese Woche: Team Blau."
```

Actions: `trigger-alarm`, `list-alerts`, `ack-alert`, `resolve-alert`,
`say`. Acknowledging by phone stops the escalation chain — same
semantics as the web UI.

## 2. Trigger an alarm by SMS (sms-inbound)

`EventSource` type **`sms-inbound`**, webhook
`/api/v1/sms/inbound/<source-id>?token=…` as the number's Messaging
webhook. Behaviour:

- Body starting with the `ackKeyword` (default `ACK`) from a phone
  number that belongs to a **contact** acknowledges the newest open
  alarm and answers with a confirmation SMS.
- Any other SMS becomes an alarm: `action=event` (default) publishes a
  normal ingress event (alert rules decide, full matching power);
  `action=alert` raises directly with `escalationPolicy`.

## 3. Trigger an alarm by webhook

Existing generic ingress: `POST /api/v1/ingest/<source>` (auth token /
HMAC / basic per source, CEL field mapping, rate limits), plus the
Prometheus Alertmanager receiver at `/api/v1/ingest/<source>/alertmanager`.

## 4. Trigger an alarm from the web UI or API (with a message)

- UI: **Alarme → „Alarm auslösen"** — title, message, severity,
  escalation policy, and alarm-app sound (np.sound/np.volume).
- API: `POST /api/v1/alerts` (permission `alerts:write`):

```json
{ "title": "Feueralarm Halle 3", "message": "Rauchmelder Zone 12",
  "severity": "critical", "escalationPolicy": "nachtdienst",
  "labels": { "np.sound": "np_klaxon", "np.volume": "1.0" } }
```

Manual alarms bypass downtime/silence suppression on purpose and start
the chain immediately. A `dedupKey` folds repeated triggers.

## 5. Trigger an alarm from the mobile alarm app

The [northplane-alarm](https://github.com/myfoxit/northplane-alarm) app
creates incidents (`POST /api/v1/incidents`, scope `incidents:write`).
Incident creation now publishes an `incident_update` event **through the
rule engine**, so one seeded rule makes app-triggered alarms ring:

```yaml
kind: AlertRule
spec:
  name: app-alarm
  match: event.type == "incident_update" && event.payload.action == "created"
  severity: critical
  title: "{{ .Payload.title }}"
  escalationPolicy: nachtdienst
```

## 6. Machine inputs: MQTT, ESPA, ESPA-X, e-mail, SNMP, heartbeats

| Type | Transport | Config |
|---|---|---|
| `mqtt` | subscribes broker topics | `url`, `topics` (comma), `qos`, `username`/`passwordSecretRef`, `tlsInsecure`, `severity`; payloads: normal-form JSON, CEL `mapping`, or plain text |
| `espa` | ESPA 4.4.4 over TCP (serial bridges) | `listen` (default `tcp://:2023`), `severity`; call address/message/beep/priority → labels, priority 1→critical 2→warning |
| `espa-x` | ESPA-X 2.0 XML over TCP | `listen` (default `tcp://:8123`); LOGIN/HEARTBEAT/P-CALL handled, retransmits dedup on call id |
| `email`/`imap` | IMAP poller | host, credentials, folder, poll interval; subject/from/body land in the event payload |
| `snmp-trap` | v1/v2c/v3 listener | existing |
| heartbeats | dead-man switch | `POST/GET /api/v1/heartbeats/<name>/beat`; missing beat → event |

## 7. Event matching (regex, e-mail headers, anything)

Alert rules match with CEL over the normalised event. The original
payload's fields are addressable directly:

```
event.payload.subject.matches('(?i)feuer|brand')     # mail subject regex
event.payload.from == 'leitstelle@example.org'       # mail header
event.payload.body.contains('Zone 12')               # mail body
event.labels.topic == 'factory/hall3/fire'           # MQTT topic
event.labels["espa.priority"] == "1"                 # ESPA priority
event.severity == 'critical' && event.labels.env == 'prod'
```

Rules add `pendingFor` (condition must hold), dedup templates,
auto-close, `setLabels` (e.g. np.sound for the app, np.tts for calls)
and `escalationPolicy`. Test rules with `POST /api/v1/alert-rules:test`.

## 8. Outputs

Channels (Admin → Kanäle): `email` (SMTP/sendmail/Resend/SES), `sms`
(Twilio/generic HTTP gateway), `voice` (Twilio TTS / Asterisk AMI /
generic HTTP), `push` (Web Push + **FCM/APNs mobile**), `ntfy`, `slack`,
`teams`, `webhook`, **`mqtt`** (publish to topic, `{severity}`/`{alertId}`
placeholders), tickets (`servicenow`, `zendesk`, `jira`, generic
`ticket`).

### Voice calls (TTS + IVR ack)

- Twilio: `accountSid`, `authToken` **or** `apiKeySid`+`apiKeySecret`
  (API keys recommended; secrets as `$SECRET:name$`), `from`, `language`.
  The call reads the alarm text twice and gathers a digit: **4 = ack,
  6 = resolve** — both stop the chain and are audited.
- Per-alarm spoken text: set the **`np.tts`** label on the alert (via
  rule `setLabels` or the manual trigger) to override the template.
- On-prem alternative: Asterisk/FreePBX via AMI (`voice_asterisk`), or
  any HTTP voice gateway (`generic-http`).
- Multiple Twilio accounts/SIP setups: create several voice channels —
  escalation steps pick channels per step, contacts per preference.

### Mobile push for the alarm app (FCM/APNs)

1. The app registers its device token: `POST /api/v1/push-subscriptions`
   with `{"endpoint": "fcm://<token>"}` or `{"endpoint": "apns://<token>"}`
   (works with the app's `np_…` API token; re-registration replaces).
2. Configure the `push` channel: `fcmServiceAccount` (`$SECRET` ref to
   the Firebase service-account JSON) and/or `apnsKey` (`$SECRET` ref,
   .p8), `apnsKeyId`, `apnsTeamId`, `apnsTopic` (bundle id),
   `apnsSandbox`.
3. Point the contact's `userId` at the registering identity (user id for
   browser logins, API-token id for the app).
4. Alarm-app sound steering rides labels per the app contract:
   `np.sound` (`np_klaxon`|`np_sirene`|`np_puls`), `np.volume`
   (`0.0`–`1.0`), `np.overrideSilent` (`true` → APNs critical alert).
   The labels flow into `alert_opened` SSE events and push payloads.

## 9. Who gets alarmed, when (on-call, time profiles)

- **Contacts** carry e-mail/phone/user and ordered channel preferences
  per time profile (`worktime`/`night` built-in, any stored TimePeriod).
- **Schedules** (rotations, layers, overrides, backup) drive
  **escalation policies**:

```yaml
kind: EscalationPolicy
spec:
  name: nachtdienst
  steps:
    - after: 0s
      notify: { schedule: bereitschaft }        # whoever is on call
      channels: [push, voice]                   # override preferences
      repeatEvery: 5m                           # re-ring until acked
      maxRepeats: 3
    - after: 15m
      unlessAcked: true
      notify: { schedule: bereitschaft, escalateTo: backup }
      channels: [voice, sms]
    - after: 30m
      notify: { contactGroup: leitung }
      action: { ticket: { channel: snow, autoClose: true } }
```

Ack (web, app, ack link, SMS `ACK`, IVR digit) stops the chain. Snooze
(`POST /api/v1/alerts/{id}:snooze {"until": …}`) acks **with a wake-up**:
if the alarm is still unresolved at `until`, it re-opens and the chain
restarts from step 0.

## 10. Retries & reliability

- Deliveries ride a durable outbox: exponential backoff (30 s·2ⁿ ±20 %,
  cap 1 h), 30 attempts, then dead-letter queue with UI surfacing and
  `:replay`. Per channel configurable: `retryMaxAttempts`,
  `retryBackoffSeconds`, `retryBackoffCapSeconds`.
- Escalation timers are persisted (`escalations` table) — restarts lose
  nothing. Every attempt is an immutable `notification` event; every
  step an `escalation` event (full audit).
- Suppression (downtimes, silences with regex, flapping) gates
  rule-created alarms; when suppression lifts while the alarm is still
  open, the chain starts then. Manual/phone alarms bypass suppression.
- Alarm storms correlate into incidents automatically (Correlator).

## 11. Outgoing webhooks & integrations

`/api/v1/webhooks` subscriptions forward selected event types
(`alert_opened`, `notification`, …) — now honouring the label
`selector` — HMAC-signed, retried via the outbox. MCP server and the AI
agent chat can operate on alerts with the same RBAC.
