---
title: Plugins and Nagios compatibility
description: Run Nagios/monitoring-plugins with exec:, define named check commands, use macros, import an existing Nagios or Icinga configuration, query NRPE and submit passive results.
sidebar:
  order: 3
---

Northplane speaks the Nagios plugin protocol natively: exit codes, `text | perfdata` output,
threshold ranges, `$MACRO$` expansion and `$USER1$`. Existing plugins run unchanged with
`checkCommand: exec:<plugin>`, an existing `nagios.cfg` tree can be imported as a bundle, NRPE
daemons are queried with the builtin `nrpe` check, and external systems submit passive results
over HTTP. What is *not* emulated is listed at the end of the page.

## Running a plugin with exec:

```yaml
kind: Service
metadata: {name: disk, host: db-01}
spec:
  checkCommand: exec:check_disk
  args: ["-w", "20%", "-c", "10%", "-p", "/var/lib/postgresql"]
  timeout: 20s
```

| Aspect | Behaviour |
|---|---|
| Command line | `argv = [<plugin>] + args`, executed directly — **no shell**, no globbing, no pipes |
| Plugin path | absolute paths are used as is; a relative name is looked up under `pluginsDir`; names containing `..` are refused (`UNKNOWN - plugin not allowed`) |
| `pluginsDir` | config key `pluginsDir` / env `NORTHPLANE_PLUGINS_DIR`; default = first existing of `/usr/lib/nagios/plugins`, `/usr/lib64/nagios/plugins`, `/usr/local/libexec/nagios`, `/opt/homebrew/libexec`, `<dataDir>/plugins` |
| `pluginsAllow` | optional allowlist of plugin **basenames**; when set, everything else (absolute paths included) is refused |
| Environment | exactly `PATH=/usr/local/bin:/usr/bin:/bin` (Windows: `C:\Windows\System32;C:\Windows`), `LC_ALL=C`, `NORTHPLANE_ARTIFACT_DIR=<dataDir>/artifacts` when configured, plus the macro environment for named commands with `env: true` (below). Nothing is inherited from the server process. |
| Timeout | the object's effective `timeout` (default 30 s). The plugin runs in its own process group; on timeout the **whole group** is killed (SIGKILL on Unix, `taskkill /F /T` on Windows) and the result is `UNKNOWN - plugin timed out after 30s (killed)` |
| Exit codes | 0/1/2/3 → OK/WARNING/CRITICAL/UNKNOWN; any other code → UNKNOWN; exit error without stdout → `UNKNOWN - plugin exited …`; cannot start → `UNKNOWN - cannot execute plugin: …` |
| Output caps | stdout 64 KiB, stderr 16 KiB; stderr is appended to the long output as `[stderr] …` |
| Concurrency | exec pool of `execPoolSize` workers (default `min(256, 32 × CPUs)`, env `NORTHPLANE_EXEC_POOL_SIZE`); builtin checks have their own pool of 1024 |
| Crashes | a panic inside the executor yields `UNKNOWN - internal error executing check (panic recovered)` instead of taking the server down |

The server binary runs on Linux and macOS only (process-group handling); on Windows use
[np-agent](/docs/monitoring/agent/) to run plugins locally.

:::tip[Docker]
The container image (`gcr.io/distroless/static`) ships no plugins, no shell, no interpreter and no
libc: only statically linked plugin binaries mounted into the container (with `pluginsDir` pointing
at them) can run there. In practice, run plugins where they belong — on the target hosts through
[np-agent](/docs/monitoring/agent/) (`checks:` or `agent:exec:`) — or build a derived image that
includes the plugins and their runtime, or use the builtin checks, which need nothing.
:::

## Plugin output grammar

```text
TEXT OUTPUT | OPTIONAL PERFDATA
LONG TEXT LINE 1
LONG TEXT LINE N | PERFDATA LINE 2
PERFDATA LINE 3 …
```

- The first line up to `|` is the short output (shown in lists), the rest of that line is perfdata.
- Following lines are long output until a line contains `|`; everything after that is perfdata.
- `\r\n` is normalised, input is capped at 64 KiB, invalid UTF-8 is re-decoded as Latin-1.

Perfdata tokens follow `'label'=value[UOM];[warn];[crit];[min];[max]`, separated by whitespace
outside single quotes (`''` escapes a quote inside a label). Broken tokens are skipped with a
warning, never turned into a check error; NaN/Inf are rejected. Units are normalised before
storage: `s`, `ms`, `us`/`µs` → seconds; `B`, `KB`, `MB`, `GB`, `TB` → bytes (×1024 steps);
`%` is kept; `c` marks a counter (charts rate-ify it); unknown units pass through. Warn and crit
must be valid [range strings](/docs/monitoring/builtin-checks/#thresholds-the-nagios-range-grammar)
or the token is dropped. Every token becomes a series in [NP-TSDB](/docs/monitoring/metrics-and-tsdb/),
plus `np_exec_time` per result.

## Named check commands

A `CheckCommand` resource lets you define a command once and reference it by bare name — the
Nagios `define command` equivalent. Manage them under **Templates → Check commands** (Templates &
Konfiguration → Check-Kommandos), via `/api/v1/check-commands`
([reference](/docs/reference/api/operations/get_check_commands/)) or as bundle kind `CheckCommand`.

```yaml
kind: CheckCommand
metadata: {name: check_http_vhost}
spec:
  type: exec
  line: ["$USER1$/check_http", "-H", "$HOSTADDRESS$", "-u", "$ARG1$", "-e", "$ARG2$"]
  env: true
---
kind: Service
metadata: {name: shop, host: web-01}
spec:
  checkCommand: check_http_vhost      # bare name = named command
  args: ["/shop", "200"]              # $ARG1$, $ARG2$
```

| Field | Meaning |
|---|---|
| `name` | referenced as a bare `checkCommand` |
| `type` | `exec` (plugin), `builtin` (`line[0]` is the builtin name, the rest are its flags), `agent` (executed by np-agent), `passive` |
| `line` | argv; for `exec` the first element is resolved against `pluginsDir` unless absolute |
| `env` | export `NAGIOS_*` / `NORTHPLANE_*` environment macros to the plugin |
| `timeout` | stored, **not used** — the executor applies the object's `timeout` |

:::caution[args are not appended to named exec/agent commands]
For `exec:`/`agent:exec:` references the object's `args` are appended to the argv. For a **named**
command of type `exec` or `agent` the stored `line` is executed as is and the object's `args` are
available only through `$ARG1$`…`$ARG32$`. (For a named `builtin` command the flags after `line[0]`
are used; `args` are again macros only.)
:::

Changing a check command reloads the tenant's catalog and reschedules the objects that use it.
An object that references an unknown command is kept with default settings and surfaces as a
configuration error.

## Macros

`$NAME$` tokens in `args` (and in named command lines) are expanded per argv element — no shell,
the argv stays an array. Unknown macros are left verbatim; `$$` is a literal dollar sign.

| Macro | Value |
|---|---|
| `$HOSTNAME$`, `$HOSTALIAS$`, `$HOSTDISPLAYNAME$` | host name |
| `$HOSTADDRESS$` | effective host `address`, else the host name |
| `$SERVICEDESC$`, `$SERVICEDISPLAYNAME$` | service name |
| `$ARG1$` … `$ARG32$` | the object's `args[n-1]`; unset → empty |
| `$USER1$` | `pluginsDir` (other `$USERn$` are not defined) |
| `$_HOSTFOO$`, `$_SERVICEFOO$` | `vars.foo` of the host / service (case-insensitive key) |
| `$SECRET:name$` | value from the [secret store](/docs/administration/secrets/), tenant-scoped; unresolvable → left verbatim |
| `$MAXHOSTATTEMPTS$`, `$MAXSERVICEATTEMPTS$` | from the effective spec |
| `$TIMET$`, `$LONGDATETIME$`, `$SHORTDATETIME$`, `$DATE$`, `$TIME$` | current time (`Mon Jan 2 15:04:05 MST 2006`, `01-02-2006 15:04:05`, `01-02-2006`, `15:04:05`) |
| `$HOSTSTATE$`, `$HOSTOUTPUT$`, `$SERVICESTATE$`, `$LASTSERVICECHECK$` … | state macros — **not available during check execution** (the executor has no state context, they stay verbatim) |
| `$NOTIFICATIONTYPE$`, `$CONTACTNAME$`, `$CONTACTEMAIL$` … | notification context only |

Environment export (`env: true` on a named command): `NAGIOS_<NAME>` and `NORTHPLANE_<NAME>` for
`HOSTNAME`, `HOSTADDRESS`, `SERVICEDESC`, `TIMET` and the state macros that resolve — during a check
that means the name/address/service variables. Inline `exec:`/`builtin:` references never export
the environment.

:::note
There is no Go-template (`{{ … }}`) expansion in check arguments; templates are used only in
alert rules. Agent-pulled checks (`agent:exec:`) are expanded server-side **without** the secret
resolver, so `$SECRET:…$` never leaves the server — put secrets into the agent's local environment
or plugin configuration instead.
:::

## Importing a Nagios or Icinga configuration

```bash
northplaned import nagios --path /etc/nagios            # or --path /etc/nagios/nagios.cfg
northplaned import nagios --path /etc/icinga2/conf.d --out icinga-import.yaml
```

The importer reads the configuration, writes a bundle (default `northplane-import.yaml`), prints
statistics plus a **deviation report** (in German: "Abweichungsbericht"), and ends with
`bundle written to … — review, then: np apply -f …`. Review the bundle, then apply it with
`np apply -f northplane-import.yaml --dry-run` followed by `np apply`.

**File discovery**: a `nagios.cfg`/`icinga.cfg` is expanded through its `cfg_file=` and `cfg_dir=`
directives (recursively `.cfg`); a directory without a main file → every `.cfg` below it; every
`.conf` file is parsed as Icinga 2 DSL (`object|template|apply <Type> "<name>" { … }` with
`vars.x`, `import "tmpl"`). Comments (`#`, ` ;`) are stripped.

### Object type mapping

| Nagios / Icinga | Northplane |
|---|---|
| `host` | `Host`; `register 0` → `Template` with `templateKind: host` |
| `service` | one `Service` per `host_name` entry; a template or a service without `host_name` → `Template` (`templateKind: service`) |
| `command` | `CheckCommand` (`type: exec`, `line` = whitespace-split `command_line`); command lines containing shell metacharacters (pipe, `&`, `;`, `<`, `>`, backticks or `$(`) are rejected with the advice to create a wrapper script |
| `timeperiod` | `TimePeriod` with `alias` and `days` (monday…sunday → ranges) |
| `contact` | `Contact {email}` |
| `contactgroup` | `ContactGroup {members}` |
| `hostgroup` | `StaticGroup {members}` + label hints (`linux→os=linux`, `windows→os=windows`, `prod→env=prod`, `test/stag→env=test`, `db/sql→role=database`, `web→role=web`) |
| `hostdependency`, `servicedependency` | deviation: model as host `parents` (reachability) or a business-service tree |
| `hostescalation`, `serviceescalation` | deviation: model as an `EscalationPolicy` (steps with `after`/`unlessAcked`) |
| Icinga `apply` rules | deviation: service template + label selector |
| `servicegroup`, `hostextinfo`, `serviceextinfo`, Icinga `CheckCommand`, unknown types | skipped (counted) |

### Directive mapping (hosts and services)

| Nagios directive | Spec field |
|---|---|
| `address` | `address` |
| `use` / Icinga `import` | `templates` |
| `parents` | `parents` |
| `check_command cmd!a1!a2` | `checkCommand: cmd`, `args: [a1, a2]` |
| `check_interval` / `normal_check_interval` | `interval` = n × 60 s (assumes `interval_length=60`) |
| `retry_interval` / `retry_check_interval` | `retryInterval` = n × 60 s |
| `max_check_attempts` | `maxCheckAttempts` |
| `check_period`, `notification_period` | `checkPeriod`, `notificationPeriod` |
| `notifications_enabled`, `active_checks_enabled`, `flap_detection_enabled` | `enableNotifications`, `enableChecks`, `enableFlapDetection` |
| `passive_checks_enabled 1` + `active_checks_enabled 0` (services) | `checkCommand: passive` |
| `freshness_threshold` (> 0) | `stalenessAfter: <n>s` (`check_freshness` itself is ignored) |
| `_CUSTOMVAR` / Icinga `vars.x` | `vars.customvar` (lower-cased) |
| `hostgroups`, `contact_groups`, `contacts`, `notification_interval`, `notification_options`, `icon_image*`, `statusmap_image`, `servicegroups`, `process_perf_data` | dropped silently |
| `hostgroup_name` on a service | deviation (service on hostgroup → template + selector) |
| `obsess_over_*`, `event_handler`, `stalking_options`, `failure_prediction_enabled`, `retain_*`, `parallelize_check`, `is_volatile`, `low/high_flap_threshold`, `first_notification_delay` | deviation with advice |
| anything else | deviation "no Northplane equivalent — review" |

Things to check after an import: contacts need channels and preferences before they can be
notified ([Contacts and on-call](/docs/alarming/contacts-and-oncall/)); `contact_groups` on hosts
and services are **not** carried over (add `contactGroups` or an escalation policy);
`checkPeriod` is stored but not enforced by the scheduler (see
[Checks and states](/docs/concepts/checks-and-states/)); plugin commands are written as
`$USER1$/check_x`, so set `pluginsDir` to your plugin directory.

## NRPE

Northplane contains an NRPE **client** (builtin `nrpe`, v2 fixed packets or v3/v4), not a daemon.
Flags and examples are in the [builtin checks reference](/docs/monitoring/builtin-checks/#nrpe);
the two things that catch Nagios users: the command is `-C` (not `-c`), and TLS mode needs a daemon
with real certificates (no anonymous DH) — otherwise run the daemon in plaintext mode and add `-n`.

## Passive results API

Anything that can make an HTTP request can submit check results: cron jobs, batch pipelines,
other monitoring systems, and [np-agent](/docs/monitoring/agent/).

```bash
curl -sS -X POST "$NP_SERVER/api/v1/results" \
  -H "Authorization: Bearer $NP_TOKEN" -H "Content-Type: application/json" \
  -d '{"results":[
        {"host":"batch-01","service":"nightly-export","state":0,
         "output":"OK - 12345 rows exported in 42s | rows=12345;;;0; time=42s;300;600;0;"},
        {"host":"batch-01","state":"UP","output":"agent alive"}
      ]}'
# HTTP 202 {"accepted":2}
```

| Item | Value |
|---|---|
| Endpoint / permission | `POST /api/v1/results`, `objects:write` ([reference](/docs/reference/api/operations/post_results/)) |
| Body | `{"results":[{"host","service"?,"state","output"}]}`, max 1 MiB |
| `service` | omitted or empty → the result is for the **host** object |
| `state` | `0`…`3` or `OK`, `WARNING`/`WARN`, `CRITICAL`/`CRIT`, `UNKNOWN`, `UP`, `DOWN`, `UNREACHABLE` (any case) |
| `output` | first line is split at the first `\|` into text and perfdata, further lines become long output |
| Matching | exact names in the token's tenant: host by name, service by (host, name). Nothing is auto-created |
| Response | `202 {"accepted":n,"rejected":["unknown host x","unknown object h/s","h: invalid state \"X\"", …]}`; `503` with `server busy` if the result queue is full |
| Semantics | passive results are **hard** immediately (`maxCheckAttempts` forced to 1); hosts map OK/WARNING → UP, everything else → DOWN (→ UNREACHABLE when all parents are down) |
| Freshness | set `stalenessAfter` on the object so a missing feed turns it UNKNOWN (text from `stalenessText`) |

The object's `checkCommand` does not have to be `passive` — results are accepted for active
objects too — but `passive` (or empty) is what stops the scheduler from running anything itself.

:::caution[Host state DOWN = 1 is mapped to UP]
Symbolic `DOWN` parses to state 1 and `UNREACHABLE` to 2, while the host mapping treats 1 (the
WARNING slot) as UP and only 2/3 as DOWN. To report a host as down, send `state: 2` (or
`CRITICAL`); `"DOWN"` or `1` currently results in UP.
:::

## What is not supported

- **NSCA** and the Nagios **external command file** (`nagios.cmd`) — submit passive results over
  HTTP instead.
- **Event handlers**, `obsess_over_*` / OCSP, `stalking_options`, `is_volatile`, notification
  intervals and escalations in the Nagios sense — use [escalation policies](/docs/alarming/escalation-policies/)
  and outgoing [webhooks](/docs/alarming/webhooks-out/).
- Shell constructs in command lines — argv only; wrap pipes in a script.
- `check_period` enforcement by the scheduler and `TimePeriod.exclude` (stored, not applied).
- A Nagios-style NRPE **server** and `satellite` zones (`zone` is stored only).
