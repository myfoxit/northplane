---
title: First steps
description: Orientation after the install — the UI in ten minutes, creating objects in the UI and with a bundle, templates, a minimal channel → contact → escalation policy → rule chain, your first API token and the np CLI, and installing np-agent.
sidebar:
  order: 4
---

You have a running instance and an admin login ([Quickstart](/docs/getting-started/quickstart/)).
This page is the guided tour that follows: where things are in the UI, how to create objects
properly, how the alarm chain fits together, and how to talk to the API.

## The UI in ten minutes

The sidebar has sixteen entries; labels follow your browser language (German or English — there is
no in-app switch). The important stops, in the order you will use them:

| Page | What it is for |
|---|---|
| **Overview (Übersicht)** `/` | Four KPI tiles (hosts up, services OK, active problems, open alerts), the open problem list, a service-status donut, open incidents, who is on call now, the last 20 events. **Wallboard** (`/?wallboard=1`) is the same page without chrome, refreshing every 10 s. |
| **Problems (Probleme)** | Every object in a hard non-OK state, with hover actions **Ack (Quittieren)**, **Downtime** and **Check now (Jetzt prüfen)**; a checkbox includes acknowledged/downtime objects. |
| **Objects (Objekte)** | All hosts and services. Two filter boxes: a **label selector** (`env=prod,role in (db,cache)`) and a **full-text** search over name and output, plus kind and state selects; filters live in the URL so views are linkable. Buttons **New host**, **New service**, **Batch add (Massenanlage)**. Click a row for the detail page with **Overview / History / Configuration** tabs. |
| **Alerts (Alarme)** | Open and acknowledged alerts with **Ack** and **Resolve**; **Trigger alarm (Alarm auslösen)** raises a manual alert through an escalation policy. **Incidents** groups alerts; **Events** is the raw, filterable event log with an NDJSON export. |
| **Alert rules (Alarm-Regeln)** `/alerting` | Tabs **Alert rules** (with a tester), **Groups**, **Escalations** (with a simulator), **IVR menus** — the alarming configuration. |
| **On-Call (Bereitschaft)** | Schedules with layers, 14-day timeline, overrides, ICS export, who is on duty now. |
| **Dashboards**, **Business services**, **Reports** | Grid dashboards with 11 widget types; BPI trees with SLA budgets; scheduled availability/SLA/alert/on-call/audit reports. |
| **Maintenance (Wartung)** | Silences and downtimes (fixed, flexible, recurring RRULE). |
| **Templates** | Tabs **Templates**, **Check commands**, **Time periods**. |
| **Discovery** | CIDR scans and one-click adoption of the suggestions. |
| **AI agent (KI-Agent)** | The agent chat workspace (needs a provider connection). |
| **Admin (Administration)** | 21 tabs: Users, Roles, Contacts, Contact groups, Channels, Event sources, Webhooks, Heartbeats, Tenants, Sites, Secrets, API tokens, MCP, Agents, Dead letters, Config bundles, Audit log, AI approvals, AI providers, System health, Appearance. |

Useful everywhere:

- **Ctrl/⌘ K** opens the command palette: jump to pages, search objects by name, open the
  Wallboard or the API docs. **Ctrl/⌘ I** toggles the assistant sidebar. Two-key chords `g o`, `g p`,
  `g h`, `g a`, `g e` go to Overview, Problems, Objects (hosts), Alerts, Events.
- The sidebar's **Refresh (Aktualisierung)** select (5 s–60 s or off, default 30 s) controls how
  often the live lists poll. The UI polls; it does not hold an SSE connection.
- **Admin → Appearance (Darstellung)** sets the instance-wide colour theme (31 to choose from) and
  light/dark mode; every user sees the same branding.
- An admin with `admin:tenants` sees a **tenant switcher** at the top of the sidebar.

The full map of every page and dialog is in the [User interface](/docs/ui/navigation/) section.

## Create objects

### In the UI

**Objects → New host (Host anlegen)** opens a dialog with four tabs:

1. **Basics (Basis)** — Name (unique per tenant, cannot be renamed later), Folder (`/` by default,
   e.g. `/prod/web`), Address, Labels (key/value chips). For a service: Host.
2. **Check (Prüfung)** — the check command as *kind + remainder*: `builtin` (e.g. `icmp`, `http`,
   `tcp`, `dns`, `snmp` — the field suggests all 17), a *named check command* from the catalog,
   `exec` (a Nagios plugin under `pluginsDir`), `agent:exec` (run by `np-agent`), or `passive`. A
   new object starts as `passive`, so pick a kind. Then Arguments (one per entry), Templates, and the
   scheduling box: Interval (60 s), Retry interval (15 s), Max attempts (3), Timeout (30 s), Check
   period (`24x7`).
3. **Notifications (Benachrichtigungen)** — contact groups and contacts notified directly on hard
   changes, which states notify (`notifyOn`), notification period.
4. **Advanced (Erweitert)** — parents (host reachability), check/notification/flap-detection
   overrides, threshold mode, staleness deadline and text for passive objects, zone, custom vars
   (`$_HOSTKEY$` macros), a Markdown runbook.

**Batch add (Massenanlage)** creates many objects at once, one per line in the grammar
`name address [tmpl,tmpl] [k=v,k=v]`, with a shared folder, check command (default `builtin:icmp`)
and mode `partial` or `all-or-nothing`; the dialog previews and validates before it posts to
`POST /api/v1/objects:batch`.

Field-by-field reference: [Hosts and services](/docs/monitoring/hosts-and-services/).

### With a bundle and `np apply`

Everything the dialog does is also a YAML document. A **bundle** is a multi-document YAML file
(`---` separated) of `kind` / `metadata` / `spec` documents; kinds are applied in dependency order
(templates before hosts before services), and re-applying the same bundle is a no-op. This one
creates a template, a host and two services:

```yaml title="web-01.yaml"
kind: Template
metadata: { name: linux-base }
spec:
  kind: host
  spec:
    checkCommand: builtin:icmp
    interval: 30s
    maxCheckAttempts: 2
---
kind: Host
metadata:
  name: web-01
  folder: /prod
  labels: { env: prod, role: web }
spec:
  address: 10.0.0.10
  templates: [linux-base]
---
kind: Service
metadata:
  name: https
  host: web-01
spec:
  checkCommand: builtin:http
  args: ["-u", "https://10.0.0.10/", "--insecure", "-w", "1", "-c", "3"]
---
kind: Service
metadata:
  name: ssh
  host: web-01
spec:
  checkCommand: builtin:tcp
  args: ["-p", "22"]
```

Note the shape of the `Template` document: its `spec` is the template resource itself, so the
inheritable object settings sit one level deeper under `spec.spec`. Host and Service documents put
the object spec directly under `spec`.

Apply it with the CLI (needs an API token with `config:write`, see
[below](#create-an-api-token-and-use-np)):

```bash
np apply -f web-01.yaml --dry-run     # would apply create Template/linux-base …
np apply -f web-01.yaml               # applied create Host/web-01 …
np export > everything.yaml           # canonical bundle of the whole tenant
```

The same YAML can be pasted into **Admin → Config bundles (Config-Bundles)**, which shows the plan
(create/update/delete with field diffs) and applies it in a second step. Fields absent from a
bundle are left unmanaged; `--prune` deletes what the bundle no longer contains. Full format,
kinds and semantics: [Config bundles](/docs/administration/config-bundles/).

### Templates

A template is an `ObjectSpec` fragment that objects (and other templates) inherit. Resolution is
`built-in defaults ⊕ templates in declared order (later wins) ⊕ the object's own spec`; `vars` are
merged key by key, list fields are replaced wholesale. The object detail page shows the resolved
result under **Configuration → Effective configuration** together with the template chain, and the
API returns it from `GET /api/v1/objects/{id}/effective-config`. Manage templates, reusable named
check commands (`exec`/`builtin`/`agent`/`passive` with `$ARGn$`) and time periods under
**Templates**. Details: [Templates](/docs/monitoring/templates/) and
[Object model](/docs/concepts/object-model/).

## A minimal alarm chain

State changes alone do not notify anyone. Northplane notifies through a chain of four resources —
channel → contact → escalation policy → alert rule. The minimal version, in the UI:

1. **Channel** — **Admin → Channels (Kanäle) → Create (Anlegen)**. Type `ntfy`, name `ntfy`,
   **Enabled (Aktiv)** on, Server URL `https://ntfy.sh`, a private topic name. Save and click
   **Send test (Test senden)**. (Any other type works the same; e-mail needs `provider`, `host`,
   `from`, credentials — see [Channels](/docs/alarming/channels/).)
2. **Contact** — **Admin → Contacts (Kontakte) → Create**. Name `alice`, E-Mail, optional phone
   (E.164, for SMS/voice), time zone. Preferences (which channel types at which times and
   severities) are optional when the policy names its channels explicitly, as below.
3. **Escalation policy** — **Alerting → Escalations (Eskalationen) → Create**. Name `default`; one
   step: after `0s`, notify **Contact** `alice`, Channels `ntfy`. Add a second step `after 15m`,
   **unless acked**, to a contact group or the on-call schedule with `voice`/`sms` later. Save;
   **Simulate (Simulieren)** shows who would be paged when.
4. **Alert rule** — **Alerting → Alert rules (Alarm-Regeln) → New rule (Regel anlegen)**. Name
   `critical`, source **CEL match**:

   ```text
   event.type == "state_change" && event.stateType == "hard" && (event.state == "CRITICAL" || event.state == "DOWN")
   ```

   Severity `critical`, Escalation `default`, optional Title `{{ .event.object }} is {{ .event.state }}`.
   **Test rule (Regel testen)** replays the last 24 h of events and lists the alerts that would open.
5. **Try it** — **Alerts → Trigger alarm (Alarm auslösen)**: title, severity, escalation policy
   `default` → the step fires immediately and ntfy shows the alert. Or break the `https` service
   (point `-u` at a closed port): after `maxCheckAttempts` × `retryInterval` (3 × 15 s by default)
   the state goes hard CRITICAL, the rule opens an alert, the chain starts. **Ack** stops the chain;
   the **Events** page shows the `alert_opened`, `escalation` and `notification` records, and
   **Admin → Dead letters** collects deliveries that failed permanently.

The same chain as a bundle:

```yaml title="alarm-chain.yaml"
kind: Channel
metadata: { name: ntfy }
spec:
  type: ntfy
  enabled: true                       # required — a channel without it is disabled
  config: { url: https://ntfy.sh, topic: northplane-7f3a9c2d }
---
kind: Contact
metadata: { name: alice }
spec:
  email: alice@example.org
  timeZone: Europe/Vienna
---
kind: EscalationPolicy
metadata: { name: default }
spec:
  steps:
    - after: 0s
      notify: { contact: alice }
      channels: [ntfy]
    - after: 15m
      unlessAcked: true
      notify: { contact: alice }
      channels: [email]               # needs an enabled email channel
---
kind: AlertRule
metadata: { name: critical }
spec:
  match: 'event.type == "state_change" && event.stateType == "hard" && (event.state == "CRITICAL" || event.state == "DOWN")'
  severity: critical
  title: "{{ .event.object }} is {{ .event.state }}"
  escalationPolicy: default
```

Three things that trip up first-time setups:

- **Channels are selected by type, not by name.** A step or preference says `ntfy` or `email`, and
  the notifier uses the first *enabled* channel of that type in name order. Keep one enabled
  channel per type unless you know why not.
- **`enabled` is not defaulted.** Channels and event sources created through the API or a bundle
  without `enabled: true` are disabled; the UI sets it for you.
- **A step's `channels` list overrides the contact's preferences completely**, including their
  time-period and minimum-severity gating. Leave the list empty to route by preferences.

Suppression (downtimes, silences, flapping, dependencies), incidents, ack paths and the full
pipeline: [Alarming overview](/docs/alarming/overview/).

## Create an API token and use `np`

Browser sessions use a cookie; everything else — `np`, `np-agent`, scripts, MCP clients,
federation edges — authenticates with an API token `np_` + 48 hex characters, sent as
`Authorization: Bearer np_…`.

- **Admin → API tokens (API-Tokens)**: Name plus a comma-separated list of scopes (default
  `objects:read,alerts:read`). The token is shown **once**. Scopes are `resource:action`
  permissions with wildcards: `objects:read,alerts:read` for a read-only client, `objects:write`
  for an agent, `config:write` for `np apply`, `*:*` for an admin automation. The table lists prefix,
  scopes and last use; **Revoke (Widerrufen)** deletes. Via the API: `POST /api/v1/api-tokens`
  also takes `roles`, `ipBind` CIDRs and `expiresAt`
  ([API tokens](/docs/administration/api-tokens/)).
- **Headless**: `northplaned bootstrap-admin -config /etc/northplane/config.yaml` (on the server,
  against the same data directory) mints a token named `bootstrap-admin` with scope `*:*` and prints
  it once; it refuses if that token already exists. Minting any token closes the `/setup` page.

Then point the CLI at the instance:

```bash
export NP_SERVER=https://monitoring.example.net   # default: https://localhost:8443
export NP_TOKEN=np_0123456789abcdef…
np doctor                 # /system/info + /system/health, works without a token
np get hosts              # STATE NAME HOST LABELS
np get problems
np get alerts
np describe <object-id>   # object JSON + effective config
np apply -f web-01.yaml --dry-run
np ack <alert-id> -m "looking into it"
np oncall
```

Global flags (`--server`, `--token`, `--json`, `--insecure`) must come **before** the command. A
development server speaks plain HTTP on loopback, so use `--server http://127.0.0.1:8443` there.
`np -h` or `np help` prints usage (`np --help` is rejected as an unknown flag). Every command maps
to one or two API calls — the table is in [CLI: np](/docs/reference/cli-np/); the raw API is
browsable on the instance at `/api/docs` and documented in the
[API overview](/docs/reference/api-overview/):

```bash
curl -s -H "Authorization: Bearer $NP_TOKEN" "$NP_SERVER/api/v1/hosts?limit=5"
```

## Install an agent

`np-agent` runs on the monitored host and **pushes** results to `POST /api/v1/results` every
`interval` (60 s): a host heartbeat plus the services `load`, `memory`, `disk /` (one per configured
mount), `processes`, `network` on Linux/macOS (`cpu` instead of `load`/`network` on Windows), and
any local Nagios plugins you list under `checks:`. No inbound port on the host is needed.

The server **does not create objects from agent results** — results for an unknown host or
service are rejected (`unknown host …`, `unknown object …`). The Admin → Agents tab says the host
"appears automatically"; it does not. Create the host and the services you want first, as passive
objects with a staleness deadline so a silent agent turns them UNKNOWN:

```yaml title="agent-web-01.yaml"
kind: Host
metadata: { name: web-01, labels: { agent: "true" } }
spec:
  address: 10.0.0.10
  checkCommand: passive
  stalenessAfter: 3m
---
kind: Service
metadata: { name: load, host: web-01 }
spec: { checkCommand: passive, stalenessAfter: 3m }
---
kind: Service
metadata: { name: memory, host: web-01 }
spec: { checkCommand: passive, stalenessAfter: 3m }
---
kind: Service
metadata: { name: "disk /", host: web-01 }
spec: { checkCommand: passive, stalenessAfter: 3m }
---
kind: Service
metadata: { name: processes, host: web-01 }
spec: { checkCommand: passive, stalenessAfter: 3m }
---
kind: Service
metadata: { name: network, host: web-01 }
spec: { checkCommand: passive, stalenessAfter: 3m }
```

Then, on **Admin → Agents**:

1. Install the binary on the host with the tab's one-liner (`curl … install.sh | sh`; set
   `NP_BINARIES=np-agent` to skip the server and CLI), or take `np-agent` from the release tarball
   or your source build and put it in `/usr/local/bin` (Windows: `np-agent.exe` from the zip).
2. **Create token** with the host name filled in — it mints a token with exactly the scope
   `objects:write` and pastes it into the `agent.yaml` shown below it.
3. Write `/etc/northplane/agent.yaml` (Windows: `C:\ProgramData\northplane\agent.yaml`):

   ```yaml title="/etc/northplane/agent.yaml"
   server: https://monitoring.example.net
   token: np_…
   hostname: web-01          # must equal the Host object's name; default: OS hostname
   interval: 60s
   disk: ["/"]
   # insecure: true          # only for a self-signed server certificate
   ```

4. Start it with the unit snippet from the tab (`systemctl enable --now np-agent`, a launchd plist on
   macOS, `sc.exe create np-agent …` on Windows) or by hand:
   `np-agent -config /etc/northplane/agent.yaml`. The log line `np-agent: started host=web-01 …`
   appears, and within a minute the objects leave **PENDING**. A wrong token shows as
   `submit failed, buffering … err="HTTP 401"` on the agent and nothing on the server.

The agent keeps up to 10 000 results in memory while the server is unreachable and replays them.
Pull mode (the server hands out `agent:exec:` checks; needs `objects:read` too and a `pullAllow`
list on the agent) and the NCPA-style listener mode are described in [Agent](/docs/monitoring/agent/).

## Where to go next

- [Demo mode](/docs/getting-started/demo-mode/) — seed a complete showcase to click through.
- [Hosts and services](/docs/monitoring/hosts-and-services/) and
  [Built-in checks](/docs/monitoring/builtin-checks/) — every field and every check flag.
- [Alarming overview](/docs/alarming/overview/), then
  [Event sources](/docs/alarming/event-sources/), [Escalation policies](/docs/alarming/escalation-policies/),
  [Contacts and on-call](/docs/alarming/contacts-and-oncall/), [Voice and IVR](/docs/alarming/voice-and-ivr/).
- [Users, roles and permissions](/docs/administration/users-roles-permissions/) and
  [Authentication](/docs/administration/authentication/) — before you invite colleagues.
- [Security](/docs/administration/security/) — the hardening checklist for anything that faces a network.
- [Agent chat](/docs/ai/agent-chat/) and [MCP server](/docs/ai/mcp-server/) — when you want an
  assistant on top of the API.
