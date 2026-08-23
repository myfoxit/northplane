---
title: Contacts and on-call
description: Contacts with per-period channel preferences, contact groups, time periods, on-call schedules with layers, overrides and backup, the ICS feed and statistics, and object-level contact routing without rules.
sidebar:
  order: 7
---

**Contacts** are the people (and devices) Northplane notifies. **Contact groups** bundle them, **on-call schedules** say who is on duty when, and **escalation policies** pick one of the three per step. This page covers the people side; how steps fire is on [Escalation policies](/docs/alarming/escalation-policies/), how a channel type becomes a real delivery is on [Notification channels](/docs/alarming/channels/).

| Resource | API | Permissions | UI |
|---|---|---|---|
| Contact (`contact`, bundle `Contact`) | `/api/v1/contacts` | `oncall:read` / `oncall:write` | **Admin → Contacts (Kontakte)** |
| Contact group (`contact-group`, bundle `ContactGroup`) | `/api/v1/contact-groups` | `oncall:read` / `oncall:write` | **Admin → Contact groups (Kontaktgruppen)** |
| Time period (`time-period`, bundle `TimePeriod`) | `/api/v1/time-periods` | `objects:read` / `config:write` | **Templates → Time periods (Zeiträume)** |
| Schedule (`schedule`, bundle `Schedule`) | `/api/v1/schedules` | `oncall:read` / `oncall:write` | **On-Call (Bereitschaft)** page |
| Override (`override`, not in bundles) | `/api/v1/schedules/{name}/overrides` | `oncall:read` / `oncall:write` | **On-Call** page → *Overrides* |

All of them follow the generic resource API (`GET`/`POST` list, `GET`/`PUT` (with `If-Match`)/`DELETE` by name; names **or ids** resolve everywhere). Generated reference: [post_contacts](/docs/reference/api/operations/post_contacts/), [post_contact_groups](/docs/reference/api/operations/post_contact_groups/), [post_schedules](/docs/reference/api/operations/post_schedules/), [get_oncall_now](/docs/reference/api/operations/get_oncall_now/).

## Contacts

| Field | Notes |
|---|---|
| `name` | unique per tenant; used as the actor name when this person acknowledges by ack link, SMS or phone |
| `email` | target for `email` channels |
| `phone` | E.164 (`+4366412345678`); target for `sms` and `voice`; also used to recognise **inbound** callers and SMS senders (only digits and `+` are compared, so formatting differences do not matter) |
| `userId` | the user id of a browser login **or the API-token id** of a device — target for `push` ([Mobile push](/docs/alarming/mobile-push/)) |
| `timeZone` | IANA name (`Europe/Vienna`); the zone in which the preference periods are evaluated; default UTC |
| `preferences[]` | ordered channel preferences, see below |

```yaml title="contact.yaml"
kind: Contact
metadata: { name: bereitschaft }
spec:
  email: oncall@example.com
  phone: "+4366412345678"
  userId: 0191c0de-…            # API-token id of the phone, for push
  timeZone: Europe/Vienna
  preferences:
    - { profile: worktime, period: worktime, channels: [push, email] }
    - { profile: night, period: night, channels: [voice, sms, push], severity: warning }
    - { profile: default, channels: [email] }
```

A contact's full data (the contact, the notification events that mention it and its audit entries) can be exported with `GET /api/v1/contacts/{name}:data-export` (permission `admin:audit`, format `northplane-gdpr-export/1`).

### Channel preferences

A preference is `{profile, period, channels[], severity}`:

| Field | Notes |
|---|---|
| `profile` | a free label (`default`, `worktime`, `night`, anything) — **not evaluated**, shown in the UI only |
| `period` | what is matched: the name of a stored TimePeriod (wins), or one of the built-ins `worktime` / `arbeitszeit` (Monday–Friday 08:00–17:59 in the contact's `timeZone`) and `night` / `nacht` (everything else). Empty = the fallback preference |
| `channels` | ordered list of channel **types** (`email`, `sms`, `voice`, `push`, `ntfy`, `slack`, `teams`, `webhook`, `mqtt`, `servicenow`, `zendesk`, `jira`, `ticket`; the UI picker offers the first eight) — every listed type gets its own delivery |
| `severity` | minimum severity (`critical` > `warning` > `info` > `ok`); the preference is skipped for less severe alerts. Empty = all |

Resolution (`PreferredChannels`) for an alert of severity *S* at time *now*:

1. Walk the preferences in list order, skipping every preference whose `severity` is higher than *S*.
2. A preference with a `period` whose TimePeriod (stored first, then built-in) contains *now* in the contact's time zone **wins immediately** — its `channels` are used. A stored period that does not contain *now* is skipped; a period name that is neither stored nor built-in never matches.
3. If no period matched, the **first** period-less preference (that passed the severity filter) is the fallback.
4. If nothing matched at all, Northplane falls back to `[email]`.

In the example above a `critical` alert at 03:00 Vienna time goes out as voice, SMS and push; an `info` alert at 03:00 skips the night preference (minimum `warning`) and falls back to e-mail.

:::caution[Step channels override preferences completely]
Preferences are only consulted when the escalation step has **no** `channels` list. A step with `channels: [voice, sms]` calls and texts everybody it resolves — the contact's period gating and minimum severity are not evaluated at all. Use steps without `channels` when people should decide how they are reached, and step channels when the policy must dictate it (a fire alarm that always calls). See [Escalation policies](/docs/alarming/escalation-policies/#who-and-how-target-and-channel-resolution).
:::

Preferences are also used by object-level routing (below) — there the severity is the problem's severity, and for recoveries the severity of the state recovered *from*, so whoever was paged for the outage also gets the recovery.

## Contact groups

`{name, members[], idpGroup}` — `members` are contact ids or names. Groups are referenced by escalation steps (`notify: {contactGroup: leitung}`) and by objects (`contactGroups`). `idpGroup` (an Entra/Keycloak group id) is stored for reference only — there is no automatic membership sync from the directory.

```yaml title="contact-group.yaml"
kind: ContactGroup
metadata: { name: leitung }
spec:
  members: [bereitschaft, ops-alice]
```

## Time periods

TimePeriods are shared by monitoring (check and notification periods) and alarming (contact preferences, schedule restrictions). The format — `days` with `HH:MM-HH:MM` ranges per weekday, `exceptions` by date, wrapping windows like `19:00-07:00`, `00:00-00:00` = closed, the well-known `24x7` — is documented on [Maintenance](/docs/monitoring/maintenance/). Two things matter here:

- A TimePeriod has **no time zone**. Each consumer decides: contact preferences evaluate it in the contact's `timeZone`; schedule layer restrictions in the schedule's `timeZone`; an object's `notificationPeriod` in **UTC**.
- Stored periods win over the built-in `worktime`/`night` names — define a TimePeriod called `worktime` and it replaces the built-in definition for preferences.

```yaml title="timeperiod.yaml"
kind: TimePeriod
metadata: { name: buerozeiten }
spec:
  alias: Bürozeiten
  days:
    monday: ["08:00-18:00"]
    tuesday: ["08:00-18:00"]
    wednesday: ["08:00-18:00"]
    thursday: ["08:00-18:00"]
    friday: ["08:00-18:00"]
  exceptions:
    "2026-12-24": ["00:00-00:00"]
```

## On-call schedules

A schedule is a set of **layers** (rotations); each layer cycles its participants in order from an anchor time, optionally restricted to duty windows. Overrides replace whoever is on duty for a window. Resolution always yields **one person** (the first matching override, else the first layer whose restriction covers the moment).

| Field | Notes |
|---|---|
| `name` | referenced by policies: `notify: {schedule: bereitschaft}` |
| `timeZone` | IANA; handoffs of daily/weekly rotations happen at the anchor's wall-clock time in this zone (DST-safe); restrictions are evaluated here |
| `layers[]` | at least one participant per layer (validation `422`) |

Layer (`Rotation`):

| Field | Notes |
|---|---|
| `name` | optional label (shown in `oncall/now` shifts) |
| `participants` | contact ids (names resolve too), in wheel order |
| `unit` | `daily` (24 h), `weekly` (7 days) — calendar arithmetic in `timeZone`; `custom` — plain arithmetic with `length` |
| `length` | `custom` only; default `24h` |
| `anchor` | RFC 3339 time: start of `participants[0]`'s first shift; the wheel extends backwards too |
| `restriction` | TimePeriod-style `days` map; the layer is only on duty inside these windows (e.g. nights + weekend) |

```yaml title="schedule.yaml"
kind: Schedule
metadata: { name: bereitschaft }
spec:
  timeZone: Europe/Vienna
  layers:
    - name: primary
      participants: [alice, bob, carol]
      unit: weekly
      anchor: "2026-01-05T08:00:00+01:00"      # Monday 08:00 handover
    - name: nights
      participants: [dave]
      unit: custom
      length: 12h
      anchor: "2026-01-05T19:00:00+01:00"
      restriction:
        monday: ["19:00-07:00"]
        tuesday: ["19:00-07:00"]
        wednesday: ["19:00-07:00"]
        thursday: ["19:00-07:00"]
        friday: ["19:00-07:00"]
        saturday: ["00:00-24:00"]
        sunday: ["00:00-24:00"]
```

Resolution at time *t* (`ResolveOnCall`):

1. With offset 0, the first **override** covering *t* wins (`{contactId, start, end, override: true}`).
2. Otherwise layers in order: the first layer whose `restriction` contains *t* (or has none) yields `participants[(index + offset) mod n]`, where *index* is the shift number since `anchor`.
3. **Backup** (`escalateTo: backup` in a policy step) = offset 1: the *next* person on the winning layer's wheel. Overrides are **not** applied for the backup — if Alice is overridden by Erin this week, the backup is still Bob (the person after Alice), not the person after Erin.

In the example, step 1 of a policy (`notify: {schedule: bereitschaft}`) at 03:00 on a Tuesday resolves the `primary` layer first — it has no restriction, so Dave's `nights` layer is never reached at that position. Put restricted layers **first** when they should take precedence during their windows.

### Overrides

| Method & path | Permission | Notes |
|---|---|---|
| `POST /api/v1/schedules/{name}/overrides` | `oncall:write` | body `{"contactId", "start", "end", "reason"}`; `end` must be after `start`; audited `override.create` |
| `GET /api/v1/schedules/{name}/overrides` | `oncall:read` | sorted by start |
| `DELETE /api/v1/schedules/{name}/overrides/{id}` | `oncall:write` | audited `override.delete` |

```bash
curl -X POST https://monitoring.example.net/api/v1/schedules/bereitschaft/overrides \
  -H "Authorization: Bearer np_…" -H "Content-Type: application/json" \
  -d '{"contactId":"erin","start":"2026-08-24T06:00:00Z","end":"2026-08-31T06:00:00Z","reason":"Alice on vacation"}'
```

Overrides are stored as resources of kind `override` whose `scheduleId` is the schedule **name**; they are not part of bundles. In the UI, the **Overrides** button on a schedule card opens a dialog (contact, reason, from/to) and lists the active ones.

### Who is on call — endpoints

| Method & path | Permission | Returns |
|---|---|---|
| `GET /api/v1/oncall/now?schedule=<name>` | `oncall:read` | `[{schedule, shifts: [{contactId, layer, start, end, override}], contacts: [Contact…]}]` for all (or one) schedules — the data behind the *Now* cards and the Overview widget |
| `GET /api/v1/schedules/{name}/timeline?from=&to=&days=` | `oncall:read` | resolved, merged shifts (default: last 14 days); overrides split the underlying shifts |
| `GET /api/v1/schedules/{name}/stats?from=&to=&days=` | `oncall:read` | `[{contactId, contact, hours, weekendHours, overrides}]` over the timeline (default 30 days) |
| `GET /api/v1/schedules/{name}/ics` | `oncall:read` | iCalendar feed, now − 7 days … now + 60 days; `SUMMARY: On-Call: <name>` (` (Override)` appended), `UID: <scheduleId>-<i>@northplane` |

Generated reference: [get_schedules_name_timeline](/docs/reference/api/operations/get_schedules_name_timeline/), [get_schedules_name_stats](/docs/reference/api/operations/get_schedules_name_stats/), [get_schedules_name_ics](/docs/reference/api/operations/get_schedules_name_ics/), [post_schedules_name_overrides](/docs/reference/api/operations/post_schedules_name_overrides/).

:::note[ICS needs authentication]
The ICS feed is authenticated like every API call (session cookie or bearer token). The **ICS** link on the On-Call page works in the browser because the session cookie is sent; a calendar application subscribing to the URL cannot send a bearer header, so fetch the feed with `curl -H "Authorization: Bearer np_…"` and import or re-host it.
:::

### The On-Call page

**On-Call (Bereitschaft)** in the sidebar shows, per schedule, a *Now* card (who is on duty with e-mail and phone, ICS download), the **Schedules (Dienstpläne)** manager — a dialog with name (locked when editing), time zone (default `Europe/Berlin`), and layers (optional name, rhythm `daily`/`weekly`/`custom`, participants with contact suggestions, shift length for custom, anchor, restriction as key/value rows like `mon=08:00-17:00`) — plus a 14-day timeline card per schedule (override = amber ring), the **hours per person (30 days)** table and the *Overrides* dialog. Details of the page: [Alerts, incidents, events and on-call](/docs/ui/alerts-incidents-events/).

## Per-object contact routing (no rule needed)

Nagios-style routing is built into the object model: a host or service whose **effective** spec (after templates) carries `contacts` and/or `contactGroups` notifies them directly on every **hard** state change, without any alert rule or escalation policy.

| ObjectSpec field | Notes |
|---|---|
| `contacts`, `contactGroups` | contact / group names (or ids) |
| `enableNotifications` | `false` switches routing off (default on) |
| `notifyOn` | which hard transitions notify: `warning`, `critical`, `unknown` (services), `down`, `unreachable` (hosts), `recovery`; empty = all problem states + recovery |
| `notificationPeriod` | TimePeriod name; outside the window nothing is sent (evaluated in **UTC**) |

```yaml title="host-with-contacts.yaml"
kind: Host
metadata: { name: db-01, folder: /prod }
spec:
  address: 10.0.0.5
  checkCommand: builtin:icmp
  contactGroups: [leitung]
  notifyOn: [down, unreachable, recovery]
  notificationPeriod: buerozeiten
```

Gates, in order: hard state change only; `enableNotifications` not `false`; `notifyOn` filter (a recovery counts only after a non-OK state); `notificationPeriod` contains now; the object is **not** in a downtime; an acknowledged problem stays quiet (recoveries still go out — the sticky ack is cleared on hard recovery). Each contact's channels come from its **preferences** (severity = the problem's, or the recovered-from state for recoveries), fallback `email`; there is no step channel override here. Deliveries are outbox items of kind `object-notification` — same channels, retries and dead letters as alerts — with the title `<host> / <service> is <STATE>: <output>` or `… recovered (<from> → <to>)`, `.Policy` = `object` in templates, a link to `/objects/<id>` instead of an alert page and **no ack link** (there is no alert to acknowledge). The resulting `notification` events carry an empty `alertId`. Object fields and templates: [Object model](/docs/concepts/object-model/), [Hosts and services](/docs/monitoring/hosts-and-services/); suppression details: [Checks and states](/docs/concepts/checks-and-states/).

## Putting it together

For an alert, an escalation step resolves people in this order: `notify.schedule` (current on-call, or backup) → else `notify.contactGroup` members → else `notify.contact`; for each contact the channel types are the step's `channels`, else the contact's matching preference, else `[email]`; for each type the tenant's first enabled channel of that type delivers to the contact's `email` / `phone` / `userId` or the channel's `url`. Every (contact, type) pair is one delivery with its own retries and its own `notification` event. Verify a configuration without paging anyone with `POST /api/v1/escalation-policies/{name}:simulate` or the **Simulate** button ([Escalation policies](/docs/alarming/escalation-policies/#simulating-a-policy)).
