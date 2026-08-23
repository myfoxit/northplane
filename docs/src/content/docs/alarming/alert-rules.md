---
title: Alert rules
description: AlertRule fields, the CEL event model with examples, title and dedup templates, pendingFor, auto-close and resolve-on-OK, setLabels, escalation hookup, heartbeat rules, alert groups and rule testing.
sidebar:
  order: 3
---

An alert rule turns events into alerts. Each enabled rule compiles a CEL expression; the alerting engine evaluates every event on the bus against every rule of the event's tenant. A match opens an alert — deduplicated by a key, optionally delayed by `pendingFor`, labelled, titled, and handed to an escalation policy — and an OK/resolve event closes it again. Rules are edited in **Alerting → Alert rules (Alarm-Regeln)** (with a built-in tester), through `/api/v1/alert-rules` (generic resource CRUD, `objects:read`/`config:write`, `If-Match` on `PUT`), or as bundle kind `AlertRule`. Every create/update/delete recompiles the tenant's rules immediately.

## How the engine runs

- One goroutine consumes the event queue; the queue is blocking, events are never dropped under load.
- Events that reach the rules: `state_change` and `flapping_start`/`flapping_end` from the check pipeline, `ingress` from every [event source](/docs/alarming/event-sources/), `heartbeat_missed` from heartbeats, `incident_update` with `action: "created"` from `POST /api/v1/incidents`, and `system` events from the AI service. Lifecycle events the engine or API emit themselves (`alert_opened`, `ack`, `notification`, `escalation`, `downtime`, `silence`, `config`, engine-created `incident_update`) are fan-out only and never re-enter the rules.
- A 5-second ticker runs the timed work: `pendingFor` promotion, heartbeat rules and heartbeat resources, auto-close, re-arming of suppressed alerts, and snooze wake-ups.
- Disabled rules are skipped at load time. A rule that fails to compile is rejected on save with `422 np:validation/alert-rule` (the test endpoints report `np:validation/rule`; a bundle apply stops with `422 np:bundle/apply`).

## AlertRule fields

| Field | Type / default | Semantics |
|---|---|---|
| `name` | string | Unique per tenant; used in default dedup keys and in `alert_opened.rule` |
| `disabled` | bool, `false` | Skip the rule |
| `match` | CEL string | Required unless `heartbeat` is set; mutually exclusive with it |
| `heartbeat` | `{source, expectEvery}` | Silence detection for an event source **id** — see [Heartbeat rules](#heartbeat-rules) |
| `pendingFor` | duration, `0` | Delay opening until the condition has been pending this long |
| `dedupKey` | Go template, empty | Overrides the default dedup key — see [Dedup key](#dedup-key) |
| `severity` | `critical` \| `warning` \| `info` \| `ok`, empty | Alert severity; empty = the triggering event's severity |
| `title` | Go template, empty | Alert title; defaults described under [Title template](#title-template) |
| `autoCloseAfter` | duration, `0` | Open **and acked** alerts of this rule older than this are flipped to `expired` (checked every 5 s); auto-close tickets are closed too; no `alert_resolved` event is emitted |
| `resolveOnOk` | bool, `true` | When `false`, a clear event that does *not* match the rule does not resolve the alert (a clear event that does match still resolves) |
| `escalationPolicy` | policy name or id | Chain started when a new alert opens (unless suppressed) — see [Escalation policies](/docs/alarming/escalation-policies/) |
| `groupId` | alert-group name | Stored only; see [Alert groups](#alert-groups-configuration-only) |
| `setLabels` | map | Merged over the event labels onto the alert (rule labels win) |
| `incident` | bool, `false` | Every alert this rule opens gets its own incident (`createdBy: rule:<name>`); it auto-resolves when the last member alert resolves |

Durations are Go strings (`30s`, `5m`, `24h`); JSON also accepts a bare integer of seconds.

```yaml title="A complete rule in bundle form"
kind: AlertRule
metadata: { name: host-down-critical }
spec:
  match: 'event.type == "state_change" && event.stateType == "hard" && (event.state == "CRITICAL" || event.state == "DOWN")'
  severity: critical
  title: "{{ .event.object }} is {{ .event.state }}"
  pendingFor: 2m
  autoCloseAfter: 24h
  escalationPolicy: ops-page
  setLabels: { np.sound: np_klaxon, team: sre }
  incident: true
```

## The CEL environment

Rules see exactly one variable, **`event`**, in a sandboxed CEL environment (no I/O, optimizer on, cost limit 10 000). Evaluation semantics:

- A missing key, field or index is a legitimate **no-match** — event shapes vary by type, so `event.payload.subject` on a `state_change` event just does not match.
- Any other runtime error is logged and the event is neither a match nor a clear.
- A non-boolean result is a no-match.

| Path | Value |
|---|---|
| `event.type` | event type string: `ingress`, `state_change`, `flapping_start`, `flapping_end`, `heartbeat_missed`, `incident_update`, `system` |
| `event.severity` | `critical` \| `warning` \| `info` \| `ok` |
| `event.ts` | RFC 3339 timestamp string |
| `event.objectId` | object id, or `""` |
| `event.source` | `sourceId` — the **EventSource id** for ingress events, the heartbeat id for `heartbeat_missed`, otherwise `""` |
| `event.labels.<k>`, `event.labels["k.v"]` | labels from the payload (NormEvent labels, object labels on state changes); `{}` when absent. Use the bracket form for keys with dots |
| `event.payload.<k>` | the raw payload map. For ingress events the inner archived body's keys are hoisted to the top level (NormEvent keys win), so `event.payload.subject`, `event.payload.from`, `event.payload.body` (mail) or any field of a webhook/MQTT/SMS JSON body are addressable directly |
| `event.object`, `event.host`, `event.kind` | object name, host name, `host`/`service` — `state_change` only |
| `event.fromState`, `event.state` | `OK`, `WARNING`, `CRITICAL`, `UNKNOWN`, `UP`, `DOWN`, `UNREACHABLE` on state changes; for every other event `state` is derived from severity: `CRITICAL`, `WARNING`, `OK` or `INFO` |
| `event.stateType` | `soft` \| `hard` (`state_change`) |
| `event.attempt` | check attempt number (`state_change`) |
| `event.output`, `event.summary` | check output / event summary (`summary` falls back to `output`, else `""`) |
| `event.metric` | dominant perfdata label (`state_change`) |
| `event.dedupKey` | the NormEvent's dedup key (ingress) |

Examples that are known to work:

```text
event.type == "state_change" && event.stateType == "hard" && (event.state == "CRITICAL" || event.state == "DOWN")
event.type == "state_change" && event.kind == "service" && event.labels.env == "prod" && event.state != "OK"
event.payload.subject.matches('(?i)feuer|brand')            # mail subject, case-insensitive regex
event.payload.from == 'leitstelle@example.org'              # mail header
event.payload.body.contains('Zone 12')                      # mail body
event.labels.source == 'mail-line' && event.severity == 'critical'
event.summary.matches('FEUER.*Halle')
event.labels.topic == 'factory/hall3/fire'                  # MQTT topic
event.labels["espa.priority"] == "1"                        # ESPA priority (dotted key)
event.type == "incident_update" && event.payload.action == "created"
event.type == "heartbeat_missed"
event.type == "ingress" && event.source == "0199a0b1-…"     # a specific source by id
```

CEL string functions you will use most: `==`, `contains()`, `startsWith()`, `endsWith()`, `matches()` (RE2 regex), `in` for list membership, `&&`/`||`/`!`, `has(event.labels.env)` to test presence without triggering a no-match.

:::tip[Match on labels, not on source names]
`event.source` is a UUID. To address a source readably, put a label on the source (`labels: { source: grafana }`) and match `event.labels.source == "grafana"`. Only e-mail, SNMP-trap and ESPA sources stamp `labels.source` automatically.
:::

## Open and clear semantics

A matching event is a **clear** when `event.severity == "ok"`, or `event.state` is `OK`/`UP`, or `payload.resolve == true`. A clear resolves the open or acked alert with the same dedup key (and discards a pending draft), stops its chain, emits `alert_resolved`, and may auto-resolve a rule-created incident. Any other match **opens**:

1. The alert draft is upserted by dedup key. If an open/acked alert with that key exists, it is refreshed instead — severity can only rise, title and payload are replaced by the newest event, the event id is appended (last 50 kept) — and **no** new chain starts.
2. A genuinely new alert emits `alert_opened` `{alertId, title, severity, rule, labels}`, opens its incident when `incident: true`, then passes the suppression gate (downtime, silence, flapping, parent host down — see [Reliability](/docs/alarming/reliability/#suppression-and-re-arming)). Not suppressed → `StartChain` with `escalationPolicy`; suppressed → a `notification` event with `status: suppressed` and the reason, and the chain starts later if suppression lifts while the alert is still open.

Resolve-on-OK: a clear event that does **not** match the rule also resolves — the engine recomputes the dedup key for that event and resolves whatever is open under it — unless `resolveOnOk: false`. This is what lets a rule that only matches problem states (`event.state != "OK"`) close its alerts on recovery.

## Dedup key

The dedup key decides whether an event refreshes an existing alert or opens a new one. Open and acked alerts are unique per `(tenant, dedupKey)`.

Default, when `dedupKey` is empty:

1. `<objectId>/<ruleName>` if the event has an object (one alert per object and rule);
2. else `<ruleName>/<event.dedupKey>` if the normalized event carries a dedup key (webhook mapping, Alertmanager fingerprint, mail Message-ID, trap, ESPA-X call id …);
3. else `<ruleName>/<sourceId>` (one alert per rule and source).

A custom `dedupKey` is a Go `text/template` with data `{{ .event.* }}` (the CEL view, lowercase), `{{ .object.id }}` and `{{ .rule.name }}`, rendered with `missingkey=zero`. Examples: `{{ .rule.name }}/{{ .event.labels.host }}`, `{{ .event.labels.topic }}`, `fire/{{ .event.payload.zone }}`. A template that fails to parse yields `<ruleName>/badtemplate` (every event folds into one alert — check the tester). Heartbeat rules always use `heartbeat/<ruleName>`.

## Title template

`title` is a Go template whose only data is **`{{ .event.* }}`** — the same view the CEL expression sees. Correct forms: `{{ .event.summary }}`, `{{ .event.object }} is {{ .event.state }}`, `{{ .event.payload.subject }}`, `{{ .event.labels.host }}: {{ .event.output }}`. If the template is empty, fails, or renders empty, the title falls back to the event summary, then `<object> is <state>`, then the rule name.

:::caution[Wrong placeholders fail silently]
The data is `{{ .event.* }}` — lowercase. `{{ .Payload.title }}` (from the old operator guide) errors at execution time and silently produces the fallback title; `{{ .ObjectID }}` or `{{ .ObjectName }} ist {{ .ToLabel }}` (the placeholders shown in the rule dialog's input fields) render the literal text `<no value>`. Use `{{ .event.payload.title }}`, `{{ .object.id }}` (dedup only) and `{{ .event.object }} is {{ .event.state }}`.
:::

## Pending, auto-close, labels and severity

- **`pendingFor`** — the first matching event stores a pending draft keyed by dedup key; further matches refresh it (newest title/payload win); every 5 s drafts whose first match is at least `pendingFor` old are opened. A clear event for the same dedup key before that deletes the draft and nothing opens. The pending map is in memory: a restart forgets drafts, and they are rebuilt from the next matching event.
- **`autoCloseAfter`** — every 5 s, open and acked alerts of the rule with `openedAt` older than the duration become `expired` (not resolved, no event; the escalation engine treats `expired` like resolved and marks remaining timers done). Useful for "informational" rules whose alerts never get an explicit clear.
- **`setLabels`** — merged over the event labels; this is where you steer outputs: `np.sound`, `np.volume`, `np.overrideSilent` for the [alarm app](/docs/alarming/mobile-push/), `np.tts` to override the spoken text of [voice calls](/docs/alarming/channels/), and any label your escalation actions, silences (`selector`) or outgoing webhooks (`selector`) should see. Alert labels are also what the correlator clusters on.
- **`severity`** — empty means "inherit from the event". Because a refreshed alert's severity can only rise, a rule without a fixed severity can escalate a warning alert to critical when a critical event with the same dedup key arrives.
- **`incident`** — one incident per alert; see [Alerts and incidents](/docs/concepts/alerts-incidents/).

## Escalation hookup

`escalationPolicy` names (or ids) an [escalation policy](/docs/alarming/escalation-policies/). The chain starts when a new alert opens and is not suppressed; step offsets count from `openedAt`. Refreshes of an existing alert never restart the chain; an acknowledgement ends it; a snooze restarts it from step 0 at the wake-up time. A rule without a policy still opens visible alerts (UI, API, SSE, outgoing webhooks) — nobody is paged.

## Heartbeat rules

Instead of `match`, a rule can watch for **silence** of an event source:

```yaml
kind: AlertRule
metadata: { name: sensor-gateway-silent }
spec:
  heartbeat:
    source: 0199a0b1-2c3d-7e4f-8a9b-0c1d2e3f4a5b   # EventSource *id*
    expectEvery: 10m
  severity: warning
```

The engine records the last time it saw **any** event carrying a `sourceId` (every ingress event counts as a beat). A heartbeat rule arms after the first such event; from then on, if no event arrived for longer than `expectEvery`, it opens an alert with dedup key `heartbeat/<ruleName>` and title `No event from "<source>" for <duration> (expected every <expectEvery>)`; as soon as events resume, the alert is resolved by that key. The last-seen map is in memory — after a restart the rule re-arms only once the source sends again.

:::caution[`heartbeat.source` is the EventSource id]
It is compared with the event's `sourceId`, which is the source's UUID — not its name. The rule dialog's placeholder (`backup-job`) suggests a name; that never arms. For a dead-man check on a job that is not an event source, use a [Heartbeat resource](/docs/monitoring/heartbeats/) plus a normal rule on `event.type == "heartbeat_missed"`.
:::

## Testing rules

Both test endpoints are side-effect free and need only `alerts:read`:

| Endpoint | Body | Result |
|---|---|---|
| `POST /api/v1/alert-rules:test` | `{"rule": {…AlertRule…}, "demoEvents": [Event…]?, "from"?, "to"?}` — without `demoEvents` and `from`, zero events are evaluated | `{"matched": n, "wouldOpen": [alert drafts, one per dedup key], "sampleViews": [≤ 5 CEL views]}` |
| `POST /api/v1/alert-rules/{name}:test` | optional `{"demoEvents"?, "from"?, "to"?}`; default window = the last 24 h of stored events (max 1000) | same |

`matched` counts every matching event, including clear events; `wouldOpen` is deduplicated by dedup key; `sampleViews` shows the exact `event` object the CEL expression saw — the fastest way to discover field names. Compile errors return `422 np:validation/rule`.

```bash
curl -s -X POST "$NP/api/v1/alert-rules:test" -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d '{
  "rule": {"name":"mail-fire","match":"event.payload.subject.matches(\"(?i)feuer|brand\")","severity":"critical",
           "title":"{{ .event.payload.subject }}"},
  "demoEvents": [{"type":"ingress","severity":"warning","sourceId":"mail-1",
                  "payload":{"summary":"FEUER Halle 3","labels":{"source":"ops-mailbox"},
                             "payload":{"subject":"FEUER Halle 3","from":"leitstelle@example.org","body":"Zone 12"}}}]}'
```

In the UI, every rule row has a **Test rule (Regel testen)** button that evaluates the stored rule against the last 24 hours and lists what would open; the edit dialog has a test panel with editable demo-event JSON. Escalation policies have their own simulator (`:simulate`) — see [Escalation policies](/docs/alarming/escalation-policies/).

## Alert groups (configuration only)

`/api/v1/alert-groups` (bundle kind `AlertGroup`, **Alerting → Groups**) stores `{name, groupBy: [label keys], window, aggregate: count|min|max|avg|sum|median, valuePath, minCount}` and a rule can reference one via `groupId`. **No runtime code evaluates alert groups today** — they are stored, bundled and shown, nothing more. Storm handling that actually runs is the correlator (five alerts sharing a label pair within 120 s become one incident), described in [Alerts and incidents](/docs/concepts/alerts-incidents/). Treat alert groups as reserved configuration.

## Related

- Every event field you can match on, per source type: [Event sources](/docs/alarming/event-sources/)
- What happens to an alert after it opens: [Acknowledge and snooze](/docs/alarming/acknowledge-and-snooze/), [Escalation policies](/docs/alarming/escalation-policies/)
- Suppression, re-arming, what a restart forgets: [Reliability](/docs/alarming/reliability/)
- The event model and type catalog: [Events](/docs/concepts/events/)
- REST reference: [`post_alert_rules`](/docs/reference/api/operations/post_alert_rules/), [`post_alert_rules_test`](/docs/reference/api/operations/post_alert_rules_test/), [`post_alert_rules_name_test`](/docs/reference/api/operations/post_alert_rules_name_test/)
