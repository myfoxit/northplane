---
title: Tenants and sites
description: "Administering multi-tenancy (tenants, the X-Northplane-Tenant header, per-tenant roles, users and tokens, isolation guarantees) and federation sites (creating a site with its config bundle, the sites:connect token, edge configuration, monitoring connected edges) — with the VM104 worked example."
sidebar:
  order: 4
---

Northplane separates customers with **tenants** (data partitions inside one instance) and connects remote, customer-site installations as **sites** (federation edges that pull their configuration from a main instance). Both are administered under **Admin (Administration)**. For the models behind them read [Tenancy and RBAC](/docs/concepts/tenancy-rbac/) and [Federation](/docs/concepts/federation/) first; this page is the operator's how-to and reference.

## Tenants

### The Default tenant

Every instance has the tenant `Default` (slug `default`, id `00000000-0000-7000-8000-000000000001`), created by migration. Single-tenant installs never see another one: all users, tokens, objects and config documents live there. Sessions for OIDC and LDAP users, `/setup`, `/register` and default-admin seeding always land in the Default tenant.

### Creating a tenant

Tenants are **create-only** in this version: there is no update, rename, disable or delete (the `disabled` flag exists in the record but is never read for access control). The UI says so — **Admin → Tenants (Mandanten)** lists Name, Slug, Status and ID, and the **Anlegen** dialog takes Name and Slug ("URL-tauglicher Kurzname") with the note "Mandanten können derzeit nicht gelöscht werden".

| Endpoint | Permission | Behaviour |
|---|---|---|
| [`GET /api/v1/tenants`](/docs/reference/api/operations/get_tenants/) | `admin:tenants` | All tenants (`{items: [{id, name, slug, disabled, version, createdAt, updatedAt}]}`) |
| [`POST /api/v1/tenants`](/docs/reference/api/operations/post_tenants/) | `admin:tenants` | `{name, slug}` — both required (`422 np:validation/tenant`), slug unique → `201 {"id": "…"}`; audit `tenant.create` |

```bash
curl -s -X POST https://monitoring.example.net/api/v1/tenants \
  -H "Authorization: Bearer np_<48 hex>" -H "Content-Type: application/json" \
  -d '{"name":"MyFoxIT","slug":"myfoxit"}'
```

Creating a tenant seeds the four built-in roles (`admin`, `operator`, `viewer`, `ai-agent`) into it in the same transaction, so role names resolve in every tenant from the start.

### Acting on another tenant: `X-Northplane-Tenant`

Every API handler scopes its reads and writes by the request's tenant, resolved as:

- the value of the `X-Northplane-Tenant` header — the tenant **id** (UUID), not the slug — **if** the principal holds `admin:tenants` (or a wildcard implying it);
- otherwise the principal's own tenant (token tenant or session tenant). The header is silently ignored for everyone else.

```bash
# as a central admin: list the hosts of tenant MyFoxIT
curl -s https://monitoring.example.net/api/v1/hosts \
  -H "Authorization: Bearer np_<48 hex>" \
  -H "X-Northplane-Tenant: <tenant-id>"
```

Mutations made with the header are **audited under the acted-on tenant** with the operator's actor id; `GET /api/v1/whoami` keeps reporting the principal's home tenant. Cross-tenant reads of objects return `404 np:not-found` (not 403), and listings never leak rows from other tenants.

:::caution[Handlers that do not honour the header]
- `POST /api/v1/alerts/{id}:ack` uses the principal's **home** tenant (while `:resolve` and `:snooze` honour the header) — a cross-tenant operator acking a customer's alert gets 404. Use the customer's own credentials or the ack link instead.
- `GET /api/v1/users` lists **all users of the instance** regardless of tenant.
- `GET`/`PUT /api/v1/branding` is always the instance document under the Default tenant.
- Inbound webhooks, telephony callbacks and ack links resolve their tenant from the event source or alert id across all tenants (see [Event sources](/docs/alarming/event-sources/)).
:::

### Tenant-scoped users, roles and tokens

- **Users** have a home tenant (`tenantId`); `POST /api/v1/users` creates the account in the request's tenant, so a central admin provisions a customer login by sending `X-Northplane-Tenant`. A local login lands in the home tenant. E-mail addresses are unique instance-wide.
- **Roles** live per tenant. `admin:tenants` holders see and edit other tenants' roles only through the header. Role names in sessions and tokens are resolved in the session's/token's tenant.
- **API tokens** belong to the tenant they were minted in (the creator's active tenant at creation) and resolve their `roles` there — see [API tokens](/docs/administration/api-tokens/).
- **Secrets** are stored per `(tenant, name)` — see [Secrets](/docs/administration/secrets/).
- **Preferences** are stored per (tenant, actor).

**Worked example — provisioning a customer administrator.** This is how the tenant *MyFoxIT* on the reference instance was set up: a custom role that can do everything inside the tenant except leave it.

```bash
NP=https://monitoring.example.net; TOK=np_<central admin token>
TENANT=$(curl -s -X POST $NP/api/v1/tenants -H "Authorization: Bearer $TOK" \
  -H "Content-Type: application/json" -d '{"name":"MyFoxIT","slug":"myfoxit"}' | jq -r .id)

# 1. a tenant-admin role inside the tenant: everything except admin:tenants
curl -s -X POST $NP/api/v1/roles -H "Authorization: Bearer $TOK" -H "X-Northplane-Tenant: $TENANT" \
  -H "Content-Type: application/json" -d '{
  "name": "tenant-admin",
  "permissions": ["objects:*","config:write","checks:run","alerts:*","incidents:*",
                  "downtimes:write","silences:write","events:read","metrics:read","oncall:*",
                  "reports:render","admin:read","admin:write","admin:users","admin:tokens",
                  "admin:secrets","admin:audit","admin:ai"],
  "scope": {"tenantId": "'"$TENANT"'"}
}'

# 2. the customer's first login, created IN the tenant
curl -s -X POST $NP/api/v1/users -H "Authorization: Bearer $TOK" -H "X-Northplane-Tenant: $TENANT" \
  -H "Content-Type: application/json" \
  -d '{"name":"Alexander Hoehne","email":"info@myfoxit.com","password":"<at least 12 characters>","roles":["tenant-admin"]}'
```

The customer logs in at `/login`, lands in tenant MyFoxIT and never sees the tenant switcher (no `admin:tenants`). Because `admin:users` is instance-global for listing, that tenant admin still *sees* every account's name and e-mail in **Admin → Users** — keep that in mind when you hand out `admin:users`.

### What is isolated and what is not

| Tenant-scoped (by `tenant_id`) | Instance-wide |
|---|---|
| Hosts, services, check state, alerts, incidents, events (and the SSE stream), downtimes, silences, heartbeats | The user **list** (`GET /api/v1/users`) and e-mail uniqueness |
| All config documents: templates, check commands, time periods, rules, policies, channels, event sources, contacts, schedules, dashboards, reports, webhooks, IVR menus, **roles**, **sites** | Branding (theme and mode) |
| API tokens, secrets, sessions, preferences, idempotency keys | `config.yaml`: TLS, OIDC, LDAP, federation, `allowSignup`, demo mode, `secret.key` |
| Audit search and export (`GET /api/v1/audit`, `:export`) | Audit chain verification (`POST /api/v1/audit:verify` walks the whole table) |
| | Push subscriptions (keyed by actor id) |
| | Event-source ingest URLs (`/api/v1/ingest/{source}` is looked up across all tenants by id or name — first match in slug order wins) and ack links |

### The tenant switcher in the UI

The sidebar shows a tenant switcher only when `whoami.permissions` implies `admin:tenants`. It lists "Eigener Mandant / Your tenant · `<home tenant>`" plus all tenants from `GET /api/v1/tenants`; selecting a customer stores the id in `localStorage` (`np.activeTenant`), clears the query cache, navigates to the overview, tints the sidebar accent and labels it "Aktiver Kunde / Active customer: `<name>`". From then on every API call from the UI sends `X-Northplane-Tenant`. Branding is not re-fetched on a switch (it is instance-wide anyway).

:::caution[Known UI gap]
The Admin page renders all 21 tabs regardless of permissions. A tenant user without `admin:tenants` still sees the **Tenants** tab (its create button answers 403) and the page issues `GET /roles`, `/tenants` and `/ai/policy` requests that 403 for roles without `admin:*`. Only the tenant switcher and the **Appearance** controls are permission-gated client-side. Tracked in [Roadmap and known issues](/docs/project/roadmap-and-known-issues/).
:::

## Sites (federation edges)

A **site** is a tenant-scoped document on the *main* instance that describes one remote edge installation and embeds the [config bundle](/docs/administration/config-bundles/) that edge should run. The edge is a full `northplaned` (its own scheduler, plugins, agents, notifications, users, tokens and secrets) that **dials out only**: every tick it pulls its bundle and posts a status heartbeat. Nothing on the main instance connects inbound to the customer network.

### Create a site

**Admin → Sites (Standorte)** lists Name, Status (Verbunden / Getrennt), zuletzt gesehen, Version, Hosts/Services, offene Alarme and Konfiguration (Angewendet / Apply-Fehler). The dialog has Name, Beschreibung, **Config-Bundle (YAML)** ("Wird von der Edge-Instanz gezogen und angewendet; Validierung beim Speichern.") and the checkbox **Deaktiviert (Edge-Zugriffe ablehnen)**.

The API is the generic config-document CRUD at [`/api/v1/sites`](/docs/reference/api/operations/get_sites/) (`objects:read` for GET, `config:write` for POST/PUT/DELETE; `PUT` requires `If-Match`):

```bash
curl -s -X POST https://main.example.net/api/v1/sites \
  -H "Authorization: Bearer np_<48 hex>" -H "X-Northplane-Tenant: <tenant-id>" \
  -H "Content-Type: application/json" -d @- <<'EOF'
{
  "name": "customer-a",
  "description": "Edge in the customer A data centre",
  "labels": {"region": "eu-central"},
  "bundle": "kind: Host\nmetadata:\n  name: edge-gw\nspec:\n  address: 10.20.0.1\n  checkCommand: builtin:icmp\n---\nkind: Service\nmetadata:\n  host: edge-gw\n  name: https\nspec:\n  checkCommand: builtin:http -S -p 443\n"
}
EOF
```

| Field | Meaning |
|---|---|
| `name` | Site name; the edge references it as `federation.site` |
| `description`, `labels` | Free text / key-value labels for the overview |
| `bundle` | Multi-document YAML bundle, **parsed and validated on save** (`422` when invalid). May be empty ("nothing managed centrally yet"). |
| `disabled` | When `true`, the edge's heartbeat and pull are refused with `403 np:sites/disabled` |

### Mint the edge token

The edge authenticates with an ordinary [API token](/docs/administration/api-tokens/) minted **on the main instance, in the site's tenant**, with the single scope `sites:connect`:

```bash
curl -s -X POST https://main.example.net/api/v1/api-tokens \
  -H "Authorization: Bearer np_<48 hex>" -H "X-Northplane-Tenant: <tenant-id>" \
  -H "Content-Type: application/json" \
  -d '{"name":"site-customer-a","scopes":["sites:connect"]}'
```

The UI hint under the Sites table says the same: "Edge connection: create a token with scope `sites:connect` and add it to the customer instance in config.yaml". A `sites:connect` token can heartbeat and pull **any** site in its tenant — there is no per-site binding — so mint one token per site and keep sites of different customers in different tenants.

### Configure the edge instance

On the edge, set the `federation:` section of `config.yaml` (or the environment equivalents) and restart. The full key reference is in [Configuration](/docs/administration/configuration/).

| Key | Default | Env | Meaning |
|---|---|---|---|
| `federation.mode` | `""` (standalone) | `NORTHPLANE_FEDERATION_MODE` | Only `""` or `edge`. There is no `main` mode — a main instance is just an instance that has sites and tokens. |
| `federation.mainUrl` | — | `NORTHPLANE_FEDERATION_MAIN_URL` | `http(s)://…` of the main instance (required in edge mode) |
| `federation.token` | — | `NORTHPLANE_FEDERATION_TOKEN` | The `np_…` token with `sites:connect` (required) |
| `federation.site` | — | `NORTHPLANE_FEDERATION_SITE` | The site name registered on main (required) |
| `federation.interval` | `1m` | — | Tick interval (≤ 0 → 1 m) |
| `federation.insecureSkipVerify` | `false` | — | Skip TLS verification towards main |
| `federation.applyConfig` | `true` | — | `false` = heartbeat only, never pull/apply the bundle |

```yaml title="config.yaml (edge)"
federation:
  mode: edge
  mainUrl: "https://main.example.net"
  token: "np_…"             # minted on main, scope sites:connect
  site: "customer-a"
  interval: 60s
```

The edge logs `federation: edge mode` at start. Misconfiguration is caught at load time: `federation.mode edge requires federation.token (mint on the main instance, scope sites:connect)`, `… requires federation.site …`, `federation.mainUrl "…": must be an http(s) URL`.

Each tick the `federation-edge` worker does, in this order:

1. `GET {mainUrl}/api/v1/sites/{site}:pull` with `Authorization: Bearer <token>` and `If-None-Match: <last applied ETag>`. `304` → nothing to do. `200` → the body (≤ 8 MiB, `application/yaml`) is applied into the edge's **Default tenant** through the same applier as `np apply` / `bundles:apply`, **without prune**. On success the ETag advances and an audit entry `federation.apply` (actor `system` / `federation`, resource = site name) is written; on failure the old ETag is kept, the error is reported as `applyError` and the pull is retried every tick until a new revision applies. An **empty** bundle is remembered but not applied.
2. `POST {mainUrl}/api/v1/sites/{site}:heartbeat` with `{version, bundleEtag, applyError, stats: {hosts, services, alertsOpen}}` (counted in the edge's Default tenant). A non-2xx answer is logged as a warning.

The HTTP client timeout is 30 s. If main is unreachable the edge keeps running with its last applied configuration and logs a warning per tick.

### Monitor sites

[`GET /api/v1/sites:overview`](/docs/reference/api/operations/get_sites_overview/) (`objects:read`) returns every site of the request's tenant merged with its last status:

```json
{"items":[{"name":"customer-a","description":"…","labels":{},"bundle":"…","disabled":false,"version":3,
  "connected":true,
  "status":{"lastSeenAt":"2026-08-23T08:51:12Z","version":"main-daa6dc518a2b","bundleEtag":"\"3f9a…\"",
            "applyError":"","stats":{"hosts":2,"services":7,"alertsOpen":0},"sourceIp":"10.10.10.14"}}]}
```

`connected` is `true` when the last heartbeat is younger than **5 minutes**. The status is stored in the key-value store (`site_status:<tenant>:<name>`), not versioned, with `sourceIp` = the TCP peer address of the heartbeat (the proxy's address when main sits behind one). The Sites tab renders the same data.

The two edge-facing endpoints need `sites:connect`: [`POST /api/v1/sites/{name}:heartbeat`](/docs/reference/api/operations/post_sites_name_heartbeat/) (site must exist in the token's tenant → 404 otherwise; disabled → `403 np:sites/disabled`; `204`) and [`GET /api/v1/sites/{name}:pull`](/docs/reference/api/operations/get_sites_name_pull/) (`ETag` = quoted hex of the first 16 bytes of SHA-256 of the bundle; `If-None-Match` equal → `304`; otherwise `200 application/yaml`).

### Update a site's bundle

Change the document on main — in the dialog, or with `PUT /api/v1/sites/{name}` and the current `If-Match` — and the edge picks it up on its next tick (≤ `federation.interval`). Because the edge applies **without prune**, documents you remove from the bundle stay on the edge until someone deletes them there. `Tenant` and `Heartbeat` are not applied by the bundle applier (warning `unsupported kind`); users, tokens, secrets and sites themselves are not bundle kinds at all — provision those on the edge directly.

### Disable a site

Tick **Deaktiviert (Edge-Zugriffe ablehnen)** or set `"disabled": true`. The edge's pull and heartbeat then get `403 np:sites/disabled`; the edge keeps its last configuration and keeps running locally. Deleting the site (`DELETE /api/v1/sites/{name}`) makes the edge's calls 404 with the same local effect. Revoke the site's token as well if the edge is decommissioned.

### What flows where

| Direction | Content | Not transported |
|---|---|---|
| Main → edge | The site bundle: hosts, services, templates, check commands, time periods, rules, alert groups, policies, channels, contacts, contact groups, schedules, IVR menus, event sources, business services, dashboards, reports, webhooks, saved filters, static groups, roles | Secrets, users, tokens, sites, tenants, heartbeat definitions |
| Edge → main | Heartbeat: edge version, bundle ETag, apply error, counters (hosts, services, open alerts), source IP | Check results, alerts, events, metrics, notifications — the edge alerts locally with its own channels |

Agents at the customer site talk to the **edge** (`server: https://<edge>`) with a token minted on the edge (`objects:write`); see [Agent](/docs/monitoring/agent/). Nothing in federation provisions edge credentials or secrets.

### Worked example: the VM104 edge of doktrace.com

The reference production instance ([Environments](/docs/deployment/environments/)) runs this setup: **main** is `https://doktrace.com` (np-01); the **edge** is the VM `np-staging` (VM104, 10.10.10.14 on the same Proxmox host, see [Proxmox VM deployment](/docs/deployment/proxmox-vm/)) running its own `northplaned` container with its own local admin.

1. On main, tenant **MyFoxIT** (slug `myfoxit`) was created and a site **`vm104-edge`** registered in that tenant (all calls with `X-Northplane-Tenant: <MyFoxIT id>`). Its bundle holds the hosts `np-staging` and `lab-web`, passive agent services for them, a channel `ntfy-edge` (ntfy.sh topic), an escalation policy `edge-alarm` and a contact `edge-ops`.
2. On main, an API token `site-vm104-edge` with scope `sites:connect` was minted in tenant MyFoxIT.
3. On VM104, `/opt/northplane/config.yaml` (bind-mounted into the container) got `federation: {mode: edge, mainUrl: https://doktrace.com, token: np_…, site: vm104-edge, interval: 60s}`. The mounted file must be readable by the container user **uid 65532** — a `0600 root:root` file fails with "permission denied"; `chown 65532 config.yaml && chmod 640 config.yaml` fixes it.
4. The edge pulled and applied the bundle into its Default tenant and started heartbeating; `GET /api/v1/sites:overview` on main (with the tenant header) shows `connected: true` with the host/service counters.
5. The `np-agent` on VM104 pushes to the **edge** (`server: https://localhost:8443`, `insecure: true`, token `np-agent-local` minted on the edge, hostname `np-staging`), which fills the passive services from the bundle.

To change what the edge monitors, edit the `vm104-edge` site on main (`PUT` with `If-Match`); the edge applies it within 60 s.
