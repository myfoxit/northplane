---
title: Tenancy and RBAC
description: Tenants and the default tenant, acting on another tenant with X-Northplane-Tenant, principals, the permission grammar, built-in and custom roles, tenant-scoped tokens and roles, what is instance-wide, and the known gaps.
sidebar:
  order: 6
---

Northplane is multi-tenant from the first row: every object, alert, event, configuration document, token and secret belongs to exactly one **tenant**, and every request runs as a **principal** with a tenant and a set of **permissions**. A single-company installation simply never leaves the default tenant. This page defines the model; the administrative how-to (creating users, roles, tokens, tenants) is in [Users, roles and permissions](/docs/administration/users-roles-permissions/) and [Tenants and sites](/docs/administration/tenants-and-sites/).

## Tenants

| Fact | Detail |
|---|---|
| Resource | `{id, name, slug, disabled, version, createdAt, updatedAt}`; `slug` is unique |
| Default tenant | id `00000000-0000-7000-8000-000000000001`, name `Default`, slug `default`; created by the schema migrations |
| Create | `POST /api/v1/tenants {name, slug}` → `201 {id}` (permission `admin:tenants`); the four built-in roles are seeded into the new tenant in the same transaction |
| List | `GET /api/v1/tenants` (`admin:tenants`) |
| Update / delete | **not available** — there are no `PUT`/`DELETE` routes and `disabled` is not evaluated anywhere; the Admin → Tenants (Mandanten) tab says so |
| Bundles | `Tenant` is in the bundle vocabulary but the applier skips it with a warning |

Data is isolated by a `tenant_id` column on every table that holds tenant data; reads of another tenant's object return **404**, never 403, and listings never leak. Users have a **home tenant** (`users.tenantId`, default = Default): local and LDAP logins land there, OIDC logins always land in the Default tenant.

## Acting on another tenant

Every handler resolves the request tenant like this:

```go
if t := r.Header.Get("X-Northplane-Tenant"); t != "" && p.Allow("admin:tenants") {
    return t            // the tenant *id* (UUID), not the slug
}
return p.TenantID       // the principal's own tenant
```

So a principal holding `admin:tenants` (or a wildcard such as `*:*` or `admin:*`) may act on any tenant by sending the header; everyone else is pinned to their own tenant and the header is silently ignored. Mutations done through the header are audited under the **acted-on** tenant with the operator's actor id. The UI's tenant switcher (visible only when `whoami.permissions` implies `admin:tenants`) stores the selection in `localStorage` (`np.activeTenant`) and adds the header to every call. `GET /api/v1/whoami` always reports the **home** tenant.

Known exception: `POST /api/v1/alerts/{id}:ack` uses the home tenant and ignores the header, so a central operator cannot ack a customer's alert through the switcher (`:resolve` and `:snooze` work). See [Project → Roadmap and known issues](/docs/project/roadmap-and-known-issues/).

## Principals

| Actor type | Comes from | Tenant | Permissions |
|---|---|---|---|
| `user` | `np_session` cookie (local password, LDAP or OIDC login) | session tenant = the user's home tenant (OIDC: Default) | the user's role names, expanded on **every request** — changing a role's permissions applies immediately; changing a user's role list applies at next login |
| `token` | `Authorization: Bearer np_…` | the tenant the token was minted in | `scopes` ∪ permissions of the token's `roles` (resolved in the token's tenant) |
| `ai_agent` | a token created with `aiAgent: true`; the AI tool runner | as token | as token — used for audit attribution |
| `system` | internal actions, for example the federation edge applying a pulled bundle (audit actor `federation`) | — | — |

Anonymous requests reach only routes without a permission (`/healthz`, `/readyz`, `/metrics`, `GET /api/v1/system/info|health`, OpenAPI, the docs, ingest endpoints with their own auth). The details of logins, sessions and tokens are on [Authentication](/docs/administration/authentication/) and [API tokens](/docs/administration/api-tokens/).

## Permissions

A permission is a string `resource:action`. A held permission *implies* a wanted one when:

- they are equal, or the held one is `*:*` or `*`;
- otherwise both contain `:` and `(heldResource == "*" || heldResource == wantResource) && (heldAction == "*" || heldAction == wantAction)`.

Hence `admin:*` covers `admin:users`, `*:read` covers `objects:read`, and a malformed value without a colon matches only itself. Every REST route declares at most one permission (visible as `x-required-permission` in the OpenAPI document; routes without one are either anonymous or merely require a login); the AI/MCP tools check the same names.

The families in use:

| Family | Permissions |
|---|---|
| Monitoring | `objects:read`, `objects:write`, `checks:run`, `metrics:read` |
| Alarming | `alerts:read`, `alerts:ack`, `alerts:write`, `incidents:read`, `incidents:write`, `downtimes:write`, `silences:write`, `events:read`, `oncall:read`, `oncall:write` |
| Configuration | `config:write` (all configuration documents, bundles, heartbeat definitions, branding) — reads of configuration use `objects:read` |
| Reports | `reports:render` |
| Administration | `admin:read`/`admin:write` (roles), `admin:users`, `admin:tokens`, `admin:secrets`, `admin:audit`, `admin:tenants`, `admin:ai` |
| Federation | `sites:connect` (edge heartbeat + bundle pull) |
| Present in built-in roles or UI presets but not checked by any route | `dashboards:read`, `dashboards:write`, `reports:read`, `config:propose` (roles); `maintenance:write` (MCP token preset) — harmless but inert |

The complete permission → route table is on [Users, roles and permissions](/docs/administration/users-roles-permissions/).

## Roles

A **role** is a configuration document (`kind: role`, `/api/v1/roles`, permissions `admin:read`/`admin:write`) with `name`, `permissions[]`, `includes[]` (nested role names, expanded recursively up to depth 8), `idpGroups[]` (OIDC / LDAP group identifiers mapped onto this role at login or sync), `scope {tenantId, folder, selector}` and `system`. Roles are **per tenant** and resolved in the principal's tenant.

Built-in roles, seeded into every tenant with `system: true`:

| Role | Summary |
|---|---|
| `admin` | `*:*` — everything, including tenant switching |
| `operator` | day-to-day operations: objects read/write, checks, alerts (read/ack/write), incidents, downtimes, silences, events, metrics, on-call read/write, dashboards, reports (read/render). **No** `config:write` and no `admin:*` — an operator manages hosts and services but not templates, rules, channels or users |
| `viewer` | read-only: objects, alerts, incidents, events, metrics, on-call, dashboards, reports |
| `ai-agent` | what the AI tool runner needs: reads plus `alerts:ack`, `incidents:write`, `checks:run`, `downtimes:write`, `silences:write`, `config:propose`, `reports:render` |

On every start the server reconciles the system role `operator` to include `alerts:write`. Custom roles (for example a tenant-administrator role that adds `admin:users`, `admin:tokens`, `config:write`) are ordinary documents, also deliverable through bundles (`kind: Role`; bundle export omits roles).

:::note[Stored but not enforced]
`scope.folder`, `scope.selector` and `scope.tenantId` on a role are persisted and editable, but the authenticator never populates a folder scope and no code evaluates the selector — treat them as reserved. Likewise `system: true` is honoured by the UI (no edit/delete buttons) but not by the API: `PUT`/`DELETE /api/v1/roles/{name}` with `admin:write` will change a built-in role.
:::

## Tenant-scoped tokens and roles

- An API token is minted in the tenant of its creator (or the `X-Northplane-Tenant` target) and stays bound to it; its `roles` are resolved **in that tenant**. A token with `admin:tenants` can still switch via the header.
- Roles exist per tenant. A central administrator sees a customer tenant's roles only through the header; creating a tenant seeds the four built-ins, nothing else.
- `POST /api/v1/users` creates the user in the request tenant, which is how a central admin provisions a customer login (send the header).
- Secrets (`$SECRET:name$`), event sources, channels, policies and every other document are per tenant; secrets are keyed `(tenant, name)`.
- Ingest URLs carry no tenant: `POST /api/v1/ingest/{source}` resolves the source by name or id **across all tenants** (first match by tenant slug order), so event-source names are effectively global for ingest purposes. Ack links likewise search all tenants for the alert id.

## What is tenant-scoped and what is instance-wide

| Tenant-scoped | Instance-wide |
|---|---|
| objects, check state, alerts, incidents, events, downtimes, silences, heartbeats | the process configuration (`config.yaml`, `NORTHPLANE_*`: TLS, listen, OIDC/LDAP, federation, AI provider, `allowSignup`, demo mode) |
| all configuration documents: templates, check commands, time periods, rules, policies, schedules, contacts, channels, event sources, dashboards, reports, sites, IVR menus, webhooks, saved filters, roles | **branding** (theme + mode): one document under the Default tenant; the tenant header is ignored on `GET`/`PUT /api/v1/branding` |
| API tokens, secrets, idempotency keys, user preferences (per tenant and actor) | **users**: `GET /api/v1/users` lists every account of the installation (no tenant filter), e-mail addresses are unique globally, and the Admin → Users tab shows them all even to a tenant-scoped `admin:users` holder |
| audit search and export (`GET /api/v1/audit`, `:export`) | audit **verify** (walks the whole chain); `secret.key`; push subscriptions (keyed by actor id); the SSE hub and bus (filtered per connection) |

## Known gaps

- The Admin page renders all 21 tabs regardless of permissions; a tenant user without `admin:tenants` sees a Tenants tab whose actions 403, and the page fires a few requests (`/roles`, `/tenants`, `/ai/policy`) that 403. Only the tenant switcher and the Appearance controls are permission-gated client-side.
- `POST /alerts/{id}:ack` ignores the tenant header (above).
- Tenants cannot be renamed, disabled or deleted through the API.
- Role folder/selector scopes are not enforced; system roles are editable through the API.

All of these are tracked on [Roadmap and known issues](/docs/project/roadmap-and-known-issues/).

## Where to go next

- [Users, roles and permissions](/docs/administration/users-roles-permissions/) — the full permission list, route table and role JSON.
- [Tenants and sites](/docs/administration/tenants-and-sites/) — creating tenants, provisioning customer logins and tokens.
- [Authentication](/docs/administration/authentication/) — local login, OIDC, LDAP, sessions.
- [Federation](/docs/concepts/federation/) — when one tenant owns a remote edge instance.
