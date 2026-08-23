---
title: Checks and states
description: Active, passive and agent checks; scheduling with interval, retry, attempts and splay; soft and hard states; host UP/DOWN/UNREACHABLE and reachability; the flapping algorithm; freshness; acknowledgements; dependencies; check-now; and the events a check emits.
sidebar:
  order: 3
---

A check produces a **result** (state 0–3, output text, optional perfdata). The pipeline folds results into the object's saved state using Nagios-style rules: a problem is *soft* until it has been confirmed `maxCheckAttempts` times, then *hard*; hosts map to UP/DOWN/UNREACHABLE; repeated state changes mark an object as *flapping*. This page explains those rules exactly. The check types themselves are documented on [Builtin checks](/docs/monitoring/builtin-checks/), [Plugins and Nagios](/docs/monitoring/plugins-and-nagios/), [Agent](/docs/monitoring/agent/) and [SNMP](/docs/monitoring/snmp/).

## Active, passive and agent checks

| Class | `checkCommand` | Who runs it | Result source |
|---|---|---|---|
| active builtin | `builtin:<name>` | `northplaned`, in-process (pool 1024) | `scheduler` |
| active exec | `exec:<plugin>` or a named CheckCommand of type `exec` | `northplaned`, child process (pool `execPoolSize`) | `scheduler` |
| agent | `agent:exec:<plugin>` or a named CheckCommand of type `agent` | `np-agent` on the host (pulled from `GET /api/v1/agent/checks`), pushed back as results | `agent` |
| passive | `passive` or empty | anything that can `POST /api/v1/results` (scripts, np-agent collectors, NSCA-style bridges) | `passive` |

Only the two active classes are dispatched by the scheduler. Passive and agent objects are never executed by the server; if they have `stalenessAfter` set, the server sends a periodic **freshness probe** instead (see [Freshness and staleness](#freshness-and-staleness)). Results with source `passive` or `agent` are treated as **hard immediately** (`maxCheckAttempts` is forced to 1 for them — the classic `passive_*_checks_are_soft=0`).

Passive results are posted as `{"results":[{"host":"web01","service":"http","state":2,"output":"CRITICAL - … | t=1s"}]}`; omit `service` for a host result; `state` may be numeric (0–3) or symbolic (`OK`, `WARNING`, `CRITICAL`, `UNKNOWN`, `UP`, `DOWN`, `UNREACHABLE`). The first output line is split at the first `|` into text and perfdata; further lines become long output. Unknown objects are listed under `rejected`; the call returns 202.

:::caution[Passive host results: use 2 / CRITICAL for DOWN]
`DOWN` parses to the numeric value 1 and `UNREACHABLE` to 2. Host results are mapped with the same table as active checks (see below), where 1 (WARNING) counts as **UP**. Submit `2` or `CRITICAL` for a down host.
:::

## Scheduling

The scheduler is a timing wheel with 86 400 one-second slots (a 24 h ring) ticked every 250 ms.

| Parameter | Default | Rule |
|---|---|---|
| `interval` | `60s` | cadence of an active object; truncated to whole seconds and clamped to **1 s … 24 h** |
| splay | — | deterministic offset `FNV-64a(objectId) mod interval`; the first due time is the next grid point `now.Truncate(interval) + splay`. No random jitter, stable across restarts |
| `retryInterval` | `15s` | after a **soft** result from the scheduler, a one-shot timer triggers a recheck after `retryInterval` (the wheel cadence is unchanged). Passive/agent/freshness results never trigger retries |
| `maxCheckAttempts` | `3` | attempts before a problem becomes hard (see next section) |
| `timeout` | `30s` | context deadline for builtin checks; process-group kill for plugins (`UNKNOWN - plugin timed out after … (killed)`) |
| `enableChecks: false` | — | object removed from the wheel; with `stalenessAfter` it becomes a freshness-probe entry |
| check-now | — | `POST /api/v1/objects/{id}/check-now` (permission `checks:run`) puts the object on a priority lane (cap 256); it does **not** reset the regular cadence |

Due times are drift-free (next = planned + interval, catching up after stalls). The output queue holds 4096 jobs; when it is full the entry is postponed by one second instead of blocking the wheel. `check_state.nextCheck` shows the next planned run.

Every catalog change (object create/update/delete, template/check-command/time-period change) is pushed into the scheduler immediately; there is no reload command.

## Soft and hard states

The state machine runs per result with `maxCheckAttempts` (≤ 0 → 3) and the flap thresholds from the effective spec.

| Situation | Outcome |
|---|---|
| result OK | state OK, **hard**, `attempt = 1`, `lastOk` set. If the previous state was a *hard* problem: **recovery** (hard change, `lastHardChange` set, sticky acknowledgement cleared). Recovery from a *soft* problem is silent — no hard change, hence no notification |
| OK → problem | `maxCheckAttempts == 1` → hard immediately; otherwise **soft**, `attempt = 1` |
| soft problem continues (same or other severity) | `attempt++`; when `attempt >= maxCheckAttempts` → **hard**, `attempt = maxCheckAttempts`, `lastHardChange` set |
| hard problem → *different* problem severity (e.g. WARNING → CRITICAL) | immediate hard change, `attempt = 1`, `lastHardChange` set |
| same hard problem continues | stays hard, `attempt = maxCheckAttempts` |

Every result updates `output`, `longOutput`, `perfdata`, `latencyMs`, `execMs` and `lastCheck`. Only **hard** transitions drive direct object notifications and — through the `stateType == "hard"` condition you put in alert rules — alerts. `GET /api/v1/problems` lists hard non-OK states.

## Host states and reachability

Hosts reuse the numeric state space as `UP=0`, `DOWN=1`, `UNREACHABLE=2`. A host check result is mapped before it enters the state machine:

1. result **OK or WARNING → UP**; **CRITICAL or UNKNOWN → DOWN** (the classic Nagios default: a slow ping is still up).
2. a DOWN host that lists `spec.parents` (host names) and whose parents are **all** non-UP in a **hard** state → **UNREACHABLE** (`allParentsDown`: at least one parent, none UP, none soft).
3. a hard host transition immediately schedules a check-now for every host that lists it as a parent, so dependents flip to DOWN/UNREACHABLE quickly.

UNREACHABLE is deliberately quiet: the `state_change` event it emits carries severity `warning` instead of `critical`, rule-created alerts on an UNREACHABLE host are suppressed with reason `host unreachable (parent down)`, and service alerts are suppressed while the host is hard non-UP (`host down`) — see [Alerts and incidents](/docs/concepts/alerts-incidents/). There is no separate dependency resource; `parents` is the dependency graph.

Severity mapping used for events: host UP → `ok`, DOWN → `critical`, UNREACHABLE → `warning`; service OK → `ok`, WARNING → `warning`, CRITICAL → `critical`, UNKNOWN → `warning`.

## Flapping

The detector keeps a 21-bit history per object; a bit is set when a result's **raw** state differs from the previous raw state (soft/hard does not matter). The flap percentage is a weighted change rate with newer checks weighing more:

```text
weight(i) = 0.8 + 0.4 · i / 20        i = 0 (oldest) … 20 (newest)
flapPct   = 100 · Σ weight(i)·changed(i) / Σ weight(i)
```

- flapping **starts** when `flapPct >= flapThresholdHigh` (default **50 %**),
- flapping **stops** when `flapPct < flapThresholdLow` (default **25 %**),
- `enableFlapDetection: false` (per object or template) disables it; turning it off while flapping emits a stop.

Strict alternation gives ~100 %; 21 stable checks bring it back to 0. The pipeline emits `flapping_start` / `flapping_end` events (severity `info`); while `check_state.flapping` is set, rule-created alerts for the object are suppressed (`object flapping`) and direct object notifications are withheld. See [Maintenance](/docs/monitoring/maintenance/) for how suppression interacts with downtimes.

## Freshness and staleness

Passive and agent objects can declare `stalenessAfter`. The wheel then fires a **freshness probe** every `stalenessAfter`; the pipeline ignores it if `lastCheck` is younger than `stalenessAfter`, otherwise it applies a synthetic `UNKNOWN` result with `stalenessText` (default `UNKNOWN - check result is stale (freshness threshold exceeded)`).

Implications, read straight from the implementation:

- Detection latency lies between 1× and 2× `stalenessAfter` because the probe cadence is not re-armed from the last real result.
- The synthetic result carries source `freshness`, which is **not** forced hard: with the default `maxCheckAttempts: 3` a stale object goes soft first and becomes hard after further probes. Set `maxCheckAttempts: 1` on passive objects if staleness should be hard at once.
- The probe updates `lastCheck`, and no retry timer applies (retries are scheduler-sourced only).
- A real result clears the condition on arrival (passive results are hard immediately).

`heartbeat` resources are the simpler tool for "something should call in every N minutes" without an object — see [Heartbeats](/docs/monitoring/heartbeats/).

## Acknowledgements

There is no object-level ack endpoint. You acknowledge an **alert** (UI, `POST /api/v1/alerts/{id}:ack`, `:snooze`, ack link, SMS keyword, IVR digit, DTMF, app), and the API mirrors `ackedBy`/`ackComment` onto the object's `check_state` when the alert has an `objectId`. The ack is **sticky**: it is cleared only on a hard recovery (or when a snooze wakes up). Effects on the object side:

- acknowledged problems disappear from `GET /api/v1/problems` unless `includeHandled=true`;
- direct object notifications for problems are skipped while acked (recoveries still go out);
- on the alert side an ack ends the escalation chain.

`Acknowledgement{sticky, expiresAt}` exists in the model but expiring acks are not implemented. All ack paths are listed on [Acknowledge and snooze](/docs/alarming/acknowledge-and-snooze/).

## Events emitted by a check

| Event | When | Payload (gist) | Severity |
|---|---|---|---|
| `state_change` | raw state changed **or** a hard state was entered — soft transitions are emitted too, with `stateType: "soft"` | `{object, host?, kind, fromState, toState, from, to, stateType, attempt, output, labels, metric}` | from the new state (host UNREACHABLE → `warning`) |
| `flapping_start` / `flapping_end` | flap edges | `{object, flapPct, labels}` | `info` |

That is why rules for "page me" conditions should test `event.stateType == "hard"`. The event catalogue is on [Events](/docs/concepts/events/).

## Timing and limits quick reference

| Item | Value |
|---|---|
| `interval` / `retryInterval` / `maxCheckAttempts` / `timeout` defaults | 60 s / 15 s / 3 / 30 s |
| interval clamp | 1 s … 24 h, whole seconds |
| wheel tick / slots | 250 ms / 86 400 |
| queues | scheduler out 4096, priority 256, results 8192 |
| pipeline batch | every 250 ms or 500 results |
| exec pool / builtin pool | `min(256, 32 × CPU)` (config `execPoolSize`) / 1024 |
| plugin stdout / stderr cap | 64 KiB / 16 KiB |
| flap window / thresholds | 21 checks / 25 % low, 50 % high |
| passive results | 202, unknown objects reported in `rejected`; 503 when the pipeline is stalled |
