---
title: Federation
description: The main/edge model — a customer-site northplaned that dials out to a main instance, pulls its configuration bundle with ETag-conditional requests and reports heartbeats; the Site resource, what flows in which direction, limits, config keys and the VM104 example.
sidebar:
  order: 7
---

Federation lets one **main** instance manage the configuration of remote **edge** instances without any inbound connectivity to the customer site. An edge is a complete `northplaned` — its own scheduler, plugins, agents, channels, users and data directory — that additionally runs a `federation-edge` worker: every minute it pulls its configuration bundle from the main instance and posts a status heartbeat. Monitoring itself stays local; only configuration goes down and only status comes up.

## Main and edge

| Role | What it is | How it is configured |
|---|---|---|
| **Main** | a normal instance (no special mode) that holds one **Site** document per edge, in the tenant that owns that customer, plus an API token with scope `sites:connect` | `POST /api/v1/sites` / Admin → Sites, [Tenants and sites](/docs/administration/tenants-and-sites/) |
| **Edge** | a normal instance started with `federation.mode: edge` pointing at the main | `federation:` block in `config.yaml` or `NORTHPLANE_FEDERATION_*` |

There is no `main` mode value — `federation.mode` is either empty (standalone, which includes the main) or `edge`. The edge keeps working when the main is unreachable; it simply logs a warning per tick and continues with the last applied configuration.

## The Site resource

`kind: site`, `/api/v1/sites` (read `objects:read`, write `config:write`), tenant-scoped like every configuration document.

| Field | Meaning |
|---|---|
| `name` | the site name the edge puts into `federation.site` |
| `description`, `labels` | free |
| `bundle` | a multi-document YAML **config bundle** as a string — exactly the format of `np apply` / `bundles:apply`. It is parsed and validated on save (422 `np:validation/site` when invalid). Empty means "nothing managed centrally yet" |
| `disabled` | `true` → the edge gets 403 `np:sites/disabled` on pull and heartbeat |
| `version` | `PUT` needs `If-Match` |

Runtime status is kept separately (KV key `site_status:<tenant>:<name>`, not versioned) and merged into `GET /api/v1/sites:overview` as `SiteView = Site + connected + status`, where `status = {lastSeenAt, version, bundleEtag, applyError, stats{hosts, services, alertsOpen}, sourceIp}` and `connected` means the last heartbeat is younger than **5 minutes**. The Admin → Sites (Standorte) tab shows that table.

## The pull / heartbeat loop

```text
edge (federation-edge worker, every federation.interval, default 1m)
  1. GET  {mainUrl}/api/v1/sites/{site}:pull
        Authorization: Bearer <token with sites:connect>
        If-None-Match: "<etag of the last successfully applied bundle>"
        304 → nothing to do
        200 → body = bundle YAML (≤ 8 MiB), ETag = "<hex(first 16 bytes of sha256(bundle))>"
              empty bundle → remember the tag, apply nothing
              else ApplyBundleYAML(DefaultTenant) — same applier as `np apply`, no prune
                    success → ETag advances, audit entry federation.apply (actor system/federation)
                    failure → ETag kept (retry next tick), error reported in the heartbeat
  2. POST {mainUrl}/api/v1/sites/{site}:heartbeat
        {version, bundleEtag, applyError, stats: {hosts, services, alertsOpen}}   → 204
```

Pull runs before the heartbeat so that the heartbeat always reports the post-apply state. The HTTP client timeout is 30 s. On the main, `:heartbeat` requires that the site exists in the **token's** tenant and is not disabled, stores the status with `sourceIp = RemoteAddr`, and answers 204; `:pull` answers 304/200 as above. Both routes require the permission `sites:connect` and nothing else — a `sites:connect` token can heartbeat or pull **any** site in its tenant (there is no per-site binding).

To roll out a change: edit the Site's `bundle` on the main (`PUT /api/v1/sites/{name}` with `If-Match`, or the Admin tab); within one interval the edge fetches the new revision and applies it. A bundle that fails to apply is retried every tick and shows up as `applyError` in the overview until a new revision applies.

## What flows where

| Direction | Content | Not included |
|---|---|---|
| main → edge | the bundle: hosts, services, templates, check commands, time periods, rules, alert groups, policies, schedules, contacts, contact groups, channels, event sources, IVR menus, business services, dashboards, reports, webhook subscriptions, saved filters, static groups, roles (`Role` is allowed in apply) | `Tenant` and `Heartbeat` kinds (applier warns `unsupported kind`), secrets, users, API tokens, sites, branding, overrides |
| edge → main | status only: edge version, applied bundle ETag, apply error, counters `hosts`/`services`/`alertsOpen` (counted in the edge's Default tenant), source IP | **no** check results, alerts, events, metrics or notifications — the main does not see the edge's monitoring data |

Consequences:

- Bundles are applied into the edge's **Default tenant** only.
- Channels referenced by the bundle need their secrets on the edge: `$SECRET:name$` values, SMTP passwords, tokens and the like must be created on the edge (`PUT /api/v1/secrets/{name}` there), because secrets are not bundle kinds.
- Agents at the customer site talk to the **edge** (`server: https://<edge>`) with a token minted **on the edge**; nothing in federation provisions edge credentials.
- Applying is not transactional: a bundle that fails halfway leaves earlier documents applied (the same rule as `np apply`), and prune is never used by the edge, so documents removed from the bundle stay on the edge until deleted there.
- `applyConfig: false` turns the edge into a heartbeat-only reporter (useful to show "connected" without central configuration).

## Edge configuration

| Key | Env | Default | Meaning |
|---|---|---|---|
| `federation.mode` | `NORTHPLANE_FEDERATION_MODE` | `""` | `""` (standalone/main) or `edge`; anything else fails validation |
| `federation.mainUrl` | `NORTHPLANE_FEDERATION_MAIN_URL` | — | `https://…` (or `http://…`) of the main; required in edge mode |
| `federation.token` | `NORTHPLANE_FEDERATION_TOKEN` | — | `np_…` token minted on the main **in the site's tenant** with scope `sites:connect`; required |
| `federation.site` | `NORTHPLANE_FEDERATION_SITE` | — | the Site name on the main; required |
| `federation.interval` | — (file only) | `1m` | tick interval; values ≤ 0 fall back to 1 m |
| `federation.insecureSkipVerify` | — | `false` | skip TLS verification towards the main |
| `federation.applyConfig` | — | `true` | `false` = heartbeat only |

```yaml title="config.yaml (edge)"
federation:
  mode: edge
  mainUrl: "https://main.example.net"
  token: "np_…"             # minted on main, scope sites:connect
  site: "customer-a"
  interval: 60s
```

The start-up log shows `federation: edge mode`; the worker is listed as `federation-edge`. Validation messages are listed on [Configuration](/docs/administration/configuration/).

## Limits and caveats

- One tenant on the main ↔ many sites; each edge serves exactly one site name. There is no multi-level topology: an edge is a standalone instance with the edge worker enabled, and because `Site` documents are not a bundle kind a main cannot configure an edge's own sites — nothing propagates across more than one hop.
- Bundle size limit 8 MiB; export on the main lists at most 5000 objects / 2000 documents per kind, so very large central bundles should be authored rather than exported.
- The edge is an independent security domain: its admin users, `secret.key`, tokens and audit log are its own. Back them up separately.
- A disabled site stops both pull and heartbeat; the edge keeps its last configuration.
- Status is only as fresh as the last heartbeat; `connected` flips to false 5 minutes after the edge stops calling in.

## Worked example: VM104 as an edge of doktrace.com

The reference setup (see [Environments](/docs/deployment/environments/)): the production main runs on VM101 behind Caddy as `https://doktrace.com`; a second instance, `np-staging`, runs on VM104 (`10.10.10.14`) in the same Proxmox host and is configured as the edge of the tenant **MyFoxIT**.

1. On the main, in the MyFoxIT tenant (central admin with `X-Northplane-Tenant: <tenant id>`): create the Site `vm104-edge` whose `bundle` declares the hosts to monitor at the site (`np-staging`, `lab-web`), the passive services the local np-agent fills, a notification channel, a contact and an escalation policy; mint a token with scope `sites:connect` in the same tenant.
2. On VM104, put the `federation:` block into `/opt/northplane/config.yaml` (`mode: edge`, `mainUrl: https://doktrace.com`, `site: vm104-edge`, the token, `interval: 60s`). Because the container runs as uid 65532, a bind-mounted config file must be readable by that uid (`chown 65532 config.yaml && chmod 640 config.yaml`); a `0600 root:root` file fails with *permission denied*.
3. The edge pulls the bundle on its first tick, applies it into its Default tenant and starts heartbeating; `GET /api/v1/sites:overview` with the tenant header on the main shows `connected: true`, the edge version and `stats`.
4. An np-agent on VM104 pushes to the **edge** (`server: https://localhost:8443`, a token minted on the edge, hostname `np-staging`) and turns the bundle's passive services green.
5. Changing the monitoring at the site = `PUT` the Site document on the main; the edge picks it up within 60 s.

## Where to go next

- [Tenants and sites](/docs/administration/tenants-and-sites/) — creating sites, tokens and the Admin tab.
- [Config bundles](/docs/administration/config-bundles/) — the bundle format carried in `Site.bundle`.
- [Configuration](/docs/administration/configuration/) — the `federation:` keys in context.
- [Deployment overview](/docs/deployment/overview/) — the edge-proxied VM variant used for VM104.
