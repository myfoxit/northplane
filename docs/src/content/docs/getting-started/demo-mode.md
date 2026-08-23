---
title: Demo mode
description: northplaned serve --demo and NORTHPLANE_DEMO seed a complete, idempotent showcase — hosts, checks, alarm chain, on-call, BPI, dashboard, report, two demo users — guarded against real data and kept in its own data directory.
sidebar:
  order: 5
---

Demo mode seeds a self-contained showcase environment into the default tenant so you can click
through a populated instance: real built-in checks against loopback and public targets, a passive
job with a heartbeat, the full notification/escalation/on-call stack, a business-service tree with
an SLA, a dashboard, a scheduled report, inbound event sources, a recurring downtime and two demo
users. Seeding only writes configuration — the scheduler and executor then run the checks live.

## Enabling it

| How | Behaviour |
|---|---|
| `northplaned serve --demo` | seeds **unconditionally** on every start (idempotent). Optional flags: `--demo-snmp host:161` (target of the SNMP demo checks, default `127.0.0.1:161`) and `--demo-traps udp://:9162` (listen address of the demo SNMP-trap source). This is what `make dev` and the e2e suite use. |
| `demo: true` in `config.yaml` or `NORTHPLANE_DEMO=true` | seeds on start, but only after the **real-data guard** passes (below). This is the demo/real switch of the production stacks. |

Seeding runs before the HTTP listener comes up; a failure is fatal (`demo seed: …`). The log
reports what happened:

```text
demo: user ready name=demo-operator email=operator@demo.local password=operator-demo-2026! role=operator
demo: user ready name=demo-viewer email=viewer@demo.local password=viewer-demo-2026! role=viewer
demo: hint msg="passive service demo-batchjob & heartbeat demo-cron have no live feeder — …"
demo: hint msg="channel demo-email points at a mock SMTP sink on 127.0.0.1:2525 and demo-hook at http://127.0.0.1:18081/hook — …"
demo: hint msg="event-source demo-hook-in uses authMode=token with secretRef \"demo-hook-in-token\" — …"
demo: environment seeded counts=map[alert-rule:2 business-service:4 channel:2 …]
```

## Demo users

| Login | Password | Role | Can |
|---|---|---|---|
| `operator@demo.local` (name `demo-operator`) | `operator-demo-2026!` | `operator` | read everything, create and edit hosts/services, ack/resolve/raise alerts, incidents, downtimes, silences, on-call, dashboards, reports — but **no** Admin tabs and no config documents (templates, rules, channels need `config:write`). |
| `viewer@demo.local` (name `demo-viewer`) | `viewer-demo-2026!` | `viewer` | read only. |

The demo does **not** create an administrator. The admin is the break-glass account that
`northplaned serve` seeds on every start unless `NP_DEFAULT_ADMIN_DISABLED` is set
(`admin@localhost` with a generated password in the log, or `NP_DEFAULT_ADMIN_EMAIL` /
`NP_DEFAULT_ADMIN_PASSWORD`) — see the [Quickstart](/docs/getting-started/quickstart/#2-create-the-admin-account).

:::caution[Demo users close /setup]
The demo users are local accounts, so after `--demo` the first-run `/setup` page is closed even if
you disabled the default-admin seeding. If you run `NP_DEFAULT_ADMIN_DISABLED=1 northplaned serve --demo`
you have no admin at all; create one headlessly like the e2e suite does — `northplaned bootstrap-admin`
for a `*:*` token, then `POST /api/v1/users` with `roles: ["admin"]` — or keep the default-admin
seeding on.
:::

## What is seeded

Every artefact is named `demo-…`, labelled `demo=true`, and lives in the default tenant.

| Kind | Names and key settings |
|---|---|
| Templates | `demo-host-base` (host: interval 30 s, retry 10 s, 2 attempts, timeout 10 s), `demo-web-service` (service: 60 s, timeout 10 s) |
| Hosts (folder `/demo`) | `demo-gateway` (`127.0.0.1`, `builtin:icmp`, 15 s; labels `role=gateway`, `site=demo`); `demo-web` (`builtin:https` against `https://example.org`, parent `demo-gateway`, template `demo-host-base`); `demo-dns` (`builtin:dns -H example.org`, parent `demo-gateway`); `demo-snmp-device` (`builtin:snmp` sysUpTime against the `--demo-snmp` target, 30 s) |
| Services | `demo-snmp-ifwalk` (`snmp-walk` ifOperStatus, 60 s) and `demo-tls` (`tls-cert example.org:443 -w 21 -c 7`, every 6 h) on `demo-snmp-device`; `demo-web-latency` (`https -w 1.0 -c 3.0`, 30 s) on `demo-web`; `demo-batchjob` (passive, `stalenessAfter` 10 m, contact group `demo-ops`, `notifyOn` critical+recovery) on `demo-gateway` |
| Heartbeat | `demo-cron` — expect every 5 m, grace 1 m, severity warning |
| Contacts, group | `demo-alice` (`alice@demo.local`, e-mail), `demo-bob` (`bob@demo.local`, webhook + e-mail), group `demo-ops` |
| Channels | `demo-email` (SMTP `127.0.0.1:2525`, from `northplane@demo.local`, `allowPlaintext`), `demo-hook` (webhook `http://127.0.0.1:18081/hook`) |
| Alert group | `demo-storm` (group by host, 5 m window, min count 3) |
| Escalation policy | `demo-escalation`: step 0 → `demo-ops` by e-mail; +15 m unless acked → `demo-bob` by webhook |
| Alert rules | `demo-critical` (CEL: hard `state_change` to CRITICAL/DOWN; severity critical; title `demo: {{ .event.object }} is {{ .event.state }}`; policy `demo-escalation`; group `demo-storm`; sets label `demo=true`), `demo-heartbeat-rule` (heartbeat rule on `demo-cron`, every 5 m, warning) |
| On-call schedule | `demo-oncall` (Europe/Vienna; layer `primary`, weekly alice → bob, anchored Monday 2026-01-05 08:00) |
| Business services | root `demo-webshop` (rule worst, SLA 99.9 % monthly) with leaves `demo-webshop-web`, `demo-webshop-dns`, `demo-webshop-gateway` bound by selectors such as `role=web,demo=true` |
| Dashboard | `demo-overview` (shared): counters, problems, metric chart of `demo-web-latency` (`time`, 3 h), BPI `demo-webshop`, table with selector `demo=true` |
| Report | `demo-availability`: availability over 30 days for `demo=true`, folder `/demo`, schedule `daily@07:00`, e-mailed to alice, keep 7 |
| Event sources | `demo-hook-in` (webhook, token auth, `secretRef: demo-hook-in-token`), `demo-traps` (SNMP trap listener on the `--demo-traps` address, community `public`, severity warning), `demo-imap` (IMAP `127.0.0.1:3143`, **disabled**) |
| Downtime | `demo-batchjob-nightly`: fixed, next 03:00 Europe/Vienna for 1 h, `RRULE FREQ=DAILY;BYHOUR=3;BYMINUTE=0` |
| Users | `demo-operator`, `demo-viewer` (above) |

What you will see after a minute: `demo-gateway` UP (if ICMP works for the server's user),
`demo-web`, `demo-dns`, `demo-tls` and `demo-web-latency` OK when the host has internet access,
the SNMP objects CRITICAL/UNKNOWN unless `--demo-snmp` points at a reachable SNMP agent,
`demo-batchjob` turning UNKNOWN (stale) after 10 minutes, and the `demo-cron` heartbeat reported
missing right away — which opens a warning alert through `demo-heartbeat-rule`, so the alarm
pipeline has something to show.

### Parts that need a helping hand

The seeder writes configuration only; a few pieces point at infrastructure it does not start:

- `demo-email` and `demo-hook` deliver to a mock SMTP sink on `127.0.0.1:2525` and a webhook sink on
  `127.0.0.1:18081`. Nothing listens there by default, so their deliveries fail, retry with backoff
  and end up under **Admin → Dead letters** — a realistic demonstration of the outbox, but run a
  sink (any SMTP test server, any HTTP echo) on those ports if you want green deliveries.
- `demo-hook-in` authenticates inbound webhooks with the secret `demo-hook-in-token`, which is not
  created. Store it (`PUT /api/v1/secrets/demo-hook-in-token`, or **Admin → Secrets**) and then
  `POST /api/v1/ingest/demo-hook-in` with `Authorization: Bearer <that value>`.
- `demo-batchjob` and `demo-cron` have no feeder. Submit a result
  (`POST /api/v1/results` with `{"results":[{"host":"demo-gateway","service":"demo-batchjob","state":0,"output":"batch ok"}]}`)
  and beat the heartbeat (`POST /api/v1/heartbeats/demo-cron/beat`), both with a token holding
  `objects:write`, to watch them recover. (The log hint names `/checks/results`; the real path is
  `/api/v1/results`.)
- The SNMP demo wants an SNMP agent: `--demo-snmp 10.0.0.1:161` targets a real device with
  community `public`; traps sent to the `--demo-traps` port (`9162/udp`, publish it in Docker) show up
  as events.

## Idempotency and the real-data guard

- **Idempotent.** Re-running the seed updates in place: configuration resources are upserted by
  name, objects are matched by kind, host and name, and ids are derived deterministically from the
  names (SHA-256-based, UUID-shaped), so cross-references such as BPI parents stay valid. Existing
  demo users are reported again instead of failing. You can leave `--demo` on permanently.
- **Guarded.** With `demo: true` / `NORTHPLANE_DEMO=true` the server first checks whether the
  default tenant already contains **any host without the label `demo=true`** (up to 5000 hosts; a
  query error counts as "real data"). If so it logs
  `NORTHPLANE_DEMO is set but this database already holds real (non-demo) hosts — skipping demo
  seeding to protect production data; use a dedicated data dir/volume for the demo, or unset
  NORTHPLANE_DEMO` and starts without seeding. The explicit `--demo` flag bypasses the guard — do not
  use it on a production data directory.
- **No teardown command.** Demo artefacts are easy to find (label `demo=true`, prefix `demo-`, the
  Objects page filter `demo=true`), but the clean way to get rid of them is the one the production
  stacks use: a separate data directory you can delete.

## Demo and real data directories in the production stack

The CI-managed stacks under `deploy/` treat `NORTHPLANE_DEMO` as a switch that also selects the data
directory inside the same volume:

```ini title="deploy/.env (rendered by the deploy workflow, excerpt)"
NORTHPLANE_DEMO=true
NORTHPLANE_DATA_DIR=/var/lib/northplane/demo     # false → /var/lib/northplane/real
```

Demo mode uses `/var/lib/northplane/demo`, real mode `/var/lib/northplane/real`, so flipping the
switch never mixes the datasets and each side keeps its own database, events, TSDB and
`secret.key`. The GitHub variable `NORTHPLANE_DEMO` (and the `demo` dropdown of the manual Deploy
run: `repo-default` / `true` / `false`) controls it; the public showcase instance has run in real
mode since 2026-08-20 with its demo directory kept alongside. Details:
[CI/CD](/docs/deployment/ci-cd/) and [Operations](/docs/deployment/operations/).

For a hand-run container the same idea is `-e NORTHPLANE_DEMO=true -e NORTHPLANE_DATA_DIR=/var/lib/northplane/demo`,
or simply a second named volume.

## Development and tests use it too

- `make dev` starts the backend with `-demo` (set `NP_DEV_DEMO=0` to skip) and prints the demo
  credentials; the generated break-glass admin password appears in the `[api]` log lines.
- The Playwright end-to-end suite (`make e2e`) boots an isolated `northplaned serve --demo` with
  `NP_DEFAULT_ADMIN_DISABLED=1`, mints a token with `bootstrap-admin`, creates its own admin through
  `POST /api/v1/users`, and pins the browser locale to `de-DE` — so the demo data is what the e2e
  tests click through ([Testing](/docs/development/testing/)).
- The CI `e2e` job does the same against every commit.

## Related

- [Quickstart](/docs/getting-started/quickstart/) and [First steps](/docs/getting-started/first-steps/)
- [Configuration](/docs/administration/configuration/) — the `demo` key and `NORTHPLANE_DEMO`
- [CLI: northplaned](/docs/reference/cli-northplaned/) — `serve --demo`, `--demo-snmp`, `--demo-traps`
- [Storage](/docs/administration/storage/) — data directory layout and backups
