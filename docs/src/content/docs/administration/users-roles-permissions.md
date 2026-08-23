---
title: Users, roles and permissions
description: "Reference for Northplane RBAC — user accounts and their sources, built-in and custom roles, the permission syntax and wildcard semantics, the complete permission list, and which permission every API route checks."
sidebar:
  order: 3
---

Authorization in Northplane is role-based: a **user** holds role names, a **role** holds permissions (and may include other roles), and every API route checks one **permission** string. API tokens carry permissions directly as scopes and/or through roles. This page is the reference; the conceptual overview lives in [Tenancy and RBAC](/docs/concepts/tenancy-rbac/), and how credentials are obtained in [Authentication](/docs/administration/authentication/).

## Users

### The user record

| Field | Meaning |
|---|---|
| `id` | UUIDv7 |
| `name`, `email` | Display name and login e-mail. E-mail is unique **across the whole instance**, not per tenant. |
| `subject` | Identity-provider subject: `issuer\|sub` for OIDC users, `ldap\|<dn or id>` for directory users; empty for local users |
| `tenantId` | Home tenant (empty = Default). A local login lands in this tenant; OIDC and LDAP users always get the Default tenant. |
| `local` | `true` = password account (created by `/setup`, `/register`, `POST /users` or default-admin seeding); `false` = OIDC (just-in-time provisioned) or LDAP-synced |
| `roles` | Role names. Authoritative for local and LDAP users (LDAP sync writes them); OIDC users get their roles recomputed from IdP groups at every login and usually have an empty list here. |
| `disabled` | A disabled user cannot log in, and existing sessions are rejected on the next request |
| `lastSeenAt` | Stamped at most once per minute while the user is active |
| `version`, `createdAt`, `updatedAt` | |

`passHash` (argon2id) is never returned by the API.

### Managing users in the UI

**Admin → Users (Benutzer)** lists every account with Name (plus an LDAP/OIDC badge for non-local accounts), E-Mail, Rollen, Status and "zuletzt gesehen". **Benutzer anlegen** opens a dialog with Name, E-Mail, Passwort (≥ 12 characters), Rollen (with suggestions from the roles list) and a **Deaktiviert** switch. Each row offers **Passwort setzen** (local users only), **Bearbeiten** and **Löschen**. Two cards at the bottom handle the LDAP directory sync (when configured) and **Mein Passwort ändern** for the signed-in user.

### User endpoints

All routes need `admin:users` unless stated otherwise. New users are created in the caller's active tenant (the `X-Northplane-Tenant` header is honoured for `admin:tenants` holders — this is how a central admin provisions a customer login).

| Endpoint | Behaviour |
|---|---|
| [`GET /api/v1/users`](/docs/reference/api/operations/get_users/) | Lists **all users of the instance** (not filtered by tenant), ordered by name |
| [`GET /api/v1/users/{id}`](/docs/reference/api/operations/get_users_id/) | One user |
| [`POST /api/v1/users`](/docs/reference/api/operations/post_users/) | `{name, email, password?, roles?, disabled?}` → `201 User`. `password` is optional (≥ 12 characters when given); without it the account can only log in via OIDC until an admin sets one. Duplicate e-mail → `409 np:users/email-in-use`. Audit `user.create`. |
| [`PUT /api/v1/users/{id}`](/docs/reference/api/operations/put_users_id/) | Partial update `{name?, email?, roles?, disabled?}` — absent fields stay unchanged. Audit `user.update` with before/after. |
| [`POST /api/v1/users/{id}:set-password`](/docs/reference/api/operations/post_users_id_set_password/) | `{password}` (≥ 12); an empty password clears it (OIDC-only account). Audit `user.set-password` (no values logged). |
| [`POST /api/v1/users/me:change-password`](/docs/reference/api/operations/post_users_me_change_password/) | No permission, but only for a **session** principal (tokens get 401). `{oldPassword, newPassword}` → 204; wrong current password → `403 np:auth/bad-password`. Audit `user.change-password`. |
| [`DELETE /api/v1/users/{id}`](/docs/reference/api/operations/delete_users_id/) | 204. Audit `user.delete`. |

```bash
curl -s -X POST https://monitoring.example.net/api/v1/users \
  -H "Authorization: Bearer np_<48 hex>" -H "Content-Type: application/json" \
  -d '{"name":"Jane Doe","email":"jane@example.net","password":"<at least 12 characters>","roles":["operator"]}'
```

:::caution[Last-admin guard]
`PUT` (disabling or removing `admin`) and `DELETE` refuse to remove the **last enabled local admin** with `409 np:users/last-admin`. Only *local, enabled* users holding `admin` count — SSO admins are not considered durable break-glass accounts.
:::

Role names in `roles` are not validated against existing roles: an unknown name simply contributes no permissions.

### User preferences

Each actor has one preferences document: `{refreshIntervalMs?: int, extra?: map[string]string}`. `refreshIntervalMs` is `0` for "off" or `1000`–`86400000` (otherwise 422). [`GET`](/docs/reference/api/operations/get_users_id_preferences/)/[`PUT /api/v1/users/{id}/preferences`](/docs/reference/api/operations/put_users_id_preferences/) — `{id}` may be `me` or your own actor id without any permission; another id requires `admin:users`. `PUT` replaces the whole document (audit `preferences.update`). The UI uses it for the refresh presets (5 s / 10 s / 30 s / 60 s / off, default 30 s).

Two things that look like preferences but are not: the UI **language** follows `navigator.language` (German for `de*`, otherwise English) and is not stored; the colour **theme and mode** are instance-wide branding, not per user — see [Branding and themes](/docs/administration/branding-and-themes/).

## Roles

### Built-in roles

Four system roles (`system: true`) are seeded into the Default tenant by migration and into every new tenant on creation:

| Role | Permissions |
|---|---|
| `admin` | `*:*` |
| `operator` | `objects:read`, `objects:write`, `checks:run`, `alerts:read`, `alerts:ack`, `alerts:write`, `incidents:read`, `incidents:write`, `downtimes:write`, `silences:write`, `events:read`, `metrics:read`, `oncall:read`, `oncall:write`, `dashboards:read`, `dashboards:write`, `reports:read`, `reports:render` |
| `viewer` | `objects:read`, `alerts:read`, `incidents:read`, `events:read`, `metrics:read`, `oncall:read`, `dashboards:read`, `reports:read` |
| `ai-agent` | `objects:read`, `alerts:read`, `alerts:ack`, `incidents:read`, `incidents:write`, `events:read`, `metrics:read`, `oncall:read`, `checks:run`, `downtimes:write`, `silences:write`, `config:propose`, `reports:render` |

Consequences worth knowing:

- `operator` and `viewer` hold no `admin:*` permission — they cannot list roles, users, tokens, secrets, the audit log or tenants.
- `operator` holds no `config:write`: an operator manages hosts and services but cannot edit templates, check commands, alert rules, channels, event sources, dashboards or reports. Among the built-ins only `admin` can.
- A "tenant-admin" style role (everything except `admin:tenants`) is a **custom** role; see the example in [Tenants and sites](/docs/administration/tenants-and-sites/).
- At boot the server reconciles the system role `operator` to include `alerts:write` in every tenant (only roles with `system: true` are touched).

### Custom roles

A role is a tenant-scoped document (`kind: role`) at [`/api/v1/roles`](/docs/reference/api/operations/get_roles/):

```json
{
  "name": "noc-l1",
  "permissions": ["objects:read", "alerts:read", "alerts:ack", "admin:read"],
  "includes": ["viewer"],
  "idpGroups": ["np-noc-l1", "cn=noc-l1,ou=groups,dc=example,dc=net"],
  "scope": { "tenantId": "", "folder": "/", "selector": "" },
  "system": false
}
```

| Field | Meaning |
|---|---|
| `name` | Unique per tenant; used in user role lists, token `roles` and `includes` |
| `permissions` | Permission strings (see below) |
| `includes` | Names of roles whose permissions are added — expanded recursively, depth ≤ 8, cycle-safe. Unknown names are ignored. |
| `idpGroups` | Group identifiers from the IdP or directory that map onto this role at OIDC login or LDAP sync. OIDC matching is an exact string compare; LDAP matching is lower-cased and accepts the full group DN or its first RDN value (`cn`). Only roles in the **Default tenant** are consulted for mapping. |
| `scope.tenantId`, `scope.folder`, `scope.selector` | Stored and editable, **not enforced** (see below) |
| `system` | Built-in marker; the UI hides edit/delete for system roles |

Endpoints: `GET /api/v1/roles` (`?q=&cursor=&limit=`, default 500) and `GET /api/v1/roles/{name}` need `admin:read`; [`POST /api/v1/roles`](/docs/reference/api/operations/post_roles/), [`PUT /api/v1/roles/{name}`](/docs/reference/api/operations/put_roles_name/) (with `If-Match`) and [`DELETE /api/v1/roles/{name}`](/docs/reference/api/operations/delete_roles_name/) need `admin:write`. Mutations are audited as `role.create|update|delete`. `Role` is also a [config bundle](/docs/administration/config-bundles/) kind for apply, but bundle **export** skips roles.

**Admin → Roles (Rollen)** shows Name (with a "System" badge), Berechtigungen, Erbt von (includes) and IdP-Gruppen; the dialog edits Name, the permission list, Inherits, IdP groups and the scope fields.

:::caution[Two gaps in this version]
- **Folder and selector scope are not implemented.** `scope.folder`, `scope.selector` and `scope.tenantId` are persisted, but the authenticator never populates a folder scope on the principal, so the check on host/service create/update always passes and the selector is never evaluated. Tenant isolation comes from the principal's tenant, not from `scope.tenantId`. Treat a role as tenant-wide.
- **System roles are editable through the API.** The UI hides the controls, but `PUT`/`DELETE /api/v1/roles/{name}` with `admin:write` will change or delete `admin`, `operator`, `viewer` or `ai-agent` in the caller's tenant.
:::

## Permission model

A permission is a string `resource:action`. A held permission **implies** a wanted one when:

- they are equal, or the held one is `*:*` or `*`;
- otherwise both contain a colon and the resource part matches (`*` or equal) **and** the action part matches (`*` or equal).

| Held | Wanted | Result |
|---|---|---|
| `*:*` or `*` | anything | allowed |
| `admin:*` | `admin:users` | allowed |
| `*:read` | `objects:read` | allowed |
| `objects:read` | `objects:write` | denied |
| `objects` (no colon) | `objects:read` | denied — a malformed permission only matches itself literally |

The same logic is ported to the UI (`web/src/permissions.ts`) for hiding controls; the server decides.

### How permissions are resolved per request

- **Session principal**: the role names stored in the session at login are expanded (including `includes`) in the session's tenant on **every** request. Editing a role's permissions therefore applies immediately; changing a user's role list applies at the next login.
- **Token principal**: `scopes` ∪ permissions of the token's `roles`, roles resolved in the **token's** tenant.
- The `X-Northplane-Tenant` header changes which tenant a request acts on (for `admin:tenants` holders) but never which permissions the principal has.

## Permission reference

Every permission string that any route or AI/MCP tool checks:

| Permission | What it allows |
|---|---|
| `objects:read` | Read hosts, services, objects, problems, overview, effective config, the builtin check list; list downtimes, silences, heartbeats, discovery scans; business-service tree/impact/SLA; report archive; bundle plan and export; sites overview; agent check pull (`GET /api/v1/agent/checks`); **read of all config-document kinds** (templates, check commands, time periods, alert rules, alert groups, escalation policies, channels, event sources, business services, dashboards, reports, saved filters, webhooks, IVR menus, sites) |
| `objects:write` | Create/update/delete hosts and services, `POST /objects:batch`, passive results `POST /results`, heartbeat beats |
| `config:write` | Create/update/delete all config-document kinds listed above, heartbeat definitions, bundle apply, branding, discovery scan start, channel test-notification, report `:run`, approve AI actions |
| `checks:run` | `check-now`, `POST /check-commands:test` |
| `alerts:read` | List/get alerts, dead letters, AI action queue, alert-rule tests and escalation-policy simulation |
| `alerts:ack` | Ack/resolve/snooze alerts, dead-letter replay, deny AI actions |
| `alerts:write` | Raise alerts manually |
| `incidents:read` | List/get incidents |
| `incidents:write` | Create/update/resolve/merge/summarize incidents |
| `downtimes:write` | Create/cancel downtimes |
| `silences:write` | Create/expire silences |
| `events:read` | Event search/export, SSE stream, and all AI chat endpoints (`/ai/conversations`, `/ai/chats`, `/ai/connections`, `/ai/tools`, `/ai/providers`, `/ai/chat`) |
| `metrics:read` | Metrics query, object metric series |
| `oncall:read` | On-call now/timeline/ICS/overrides/stats; `GET` of schedules, contacts, contact groups |
| `oncall:write` | `POST/PUT/DELETE` of schedules, contacts, contact groups; schedule overrides |
| `reports:render` | `POST /reports/{name}:render` |
| `admin:read` | List/get roles |
| `admin:write` | Create/update/delete roles |
| `admin:users` | Users CRUD, set-password, directory status/sync, other users' preferences |
| `admin:tokens` | API tokens create/list/revoke/rotate |
| `admin:secrets` | Secrets put/list/delete |
| `admin:audit` | Audit search/export/verify, contact GDPR data export |
| `admin:tenants` | List/create tenants **and** the right to act on another tenant via `X-Northplane-Tenant` |
| `admin:ai` | AI tool policy get/put |
| `sites:connect` | Federation edge heartbeat and bundle pull |
| `dashboards:read`, `dashboards:write`, `reports:read` | Granted by built-in roles but **checked by no route** — dashboard and report CRUD use `objects:read` / `config:write` |
| `config:propose` | In the built-in `ai-agent` role; checked by no route and no AI tool (the `propose_config_change` tool requires `config:write`) |
| `maintenance:write` | Appears in the MCP tab's "Read + operate" scope preset; checked by no route (downtimes and silences use `downtimes:write` / `silences:write`) |

AI and MCP tools check the same permission names with the same wildcard logic — see [Agent chat](/docs/ai/agent-chat/) and [MCP server](/docs/ai/mcp-server/).

## Route-to-permission table

Every API operation publishes its permission as `x-required-permission` in `/api/openapi.json`, and the generated [REST API reference](/docs/reference/api/) shows it per operation. `—` means no permission check (the handler may still require a login).

| Method and path | Permission |
|---|---|
| `GET /api/v1/whoami` | — (401 if anonymous) |
| `GET /api/v1/tenants`, `POST /api/v1/tenants` | `admin:tenants` |
| `GET /api/v1/roles`, `GET /api/v1/roles/{name}` | `admin:read` |
| `POST /api/v1/roles`, `PUT/DELETE /api/v1/roles/{name}` | `admin:write` |
| `POST/GET /api/v1/api-tokens`, `DELETE /api/v1/api-tokens/{id}`, `POST /api/v1/api-tokens/{id}:rotate` | `admin:tokens` |
| `PUT /api/v1/secrets/{name}`, `GET /api/v1/secrets`, `DELETE /api/v1/secrets/{name}` | `admin:secrets` |
| `GET /api/v1/audit`, `GET /api/v1/audit:export`, `POST /api/v1/audit:verify`, `GET /api/v1/contacts/{name}:data-export` | `admin:audit` |
| `GET /api/v1/notifications/dead-letters` / `POST …/{id}:replay` | `alerts:read` / `alerts:ack` |
| `POST/DELETE /api/v1/push-subscriptions` | — (principal required) |
| `GET /api/v1/users`, `GET /api/v1/users/{id}`, `POST /api/v1/users`, `PUT /api/v1/users/{id}`, `POST /api/v1/users/{id}:set-password`, `DELETE /api/v1/users/{id}` | `admin:users` |
| `POST /api/v1/users/me:change-password` | — (session user only) |
| `GET/PUT /api/v1/users/{id}/preferences` | — for `me`/own id; `admin:users` for others |
| `GET /api/v1/branding` / `PUT /api/v1/branding` | — (login required) / `config:write` |
| `GET /api/v1/directory/status`, `POST /api/v1/directory:sync` | `admin:users` |
| `GET /api/v1/objects`, `/hosts`, `/services`, `GET /api/v1/objects/{id}`, `GET …/effective-config`, `GET /api/v1/problems`, `GET /api/v1/check-commands:builtins`, `GET /api/v1/overview` | `objects:read` |
| `POST /api/v1/hosts`, `POST /api/v1/services`, `PUT/DELETE /api/v1/objects/{id}`, `POST /api/v1/objects:batch` | `objects:write` |
| `POST /api/v1/objects/{id}/check-now`, `POST /api/v1/check-commands:test` | `checks:run` |
| CRUD of `templates`, `check-commands`, `time-periods`, `alert-rules`, `alert-groups`, `escalation-policies`, `channels`, `event-sources`, `business-services`, `dashboards`, `reports`, `saved-filters`, `webhooks`, `ivr-menus`, `sites` | `objects:read` (GET) / `config:write` (POST/PUT/DELETE) |
| CRUD of `schedules`, `contacts`, `contact-groups` | `oncall:read` (GET) / `oncall:write` (POST/PUT/DELETE) |
| `GET /api/v1/alerts`, `GET /api/v1/alerts/{id}` | `alerts:read` |
| `POST /api/v1/alerts` | `alerts:write` |
| `POST /api/v1/alerts/{id}:ack`, `:resolve`, `:snooze` | `alerts:ack` |
| `GET /api/v1/incidents`, `GET /api/v1/incidents/{id}` | `incidents:read` |
| `POST /api/v1/incidents`, `PUT /api/v1/incidents/{id}`, `POST …/{id}:resolve`, `:merge`, `:summarize` | `incidents:write` |
| `POST /api/v1/alert-rules:test`, `POST /api/v1/alert-rules/{name}:test`, `POST /api/v1/escalation-policies/{name}:simulate` | `alerts:read` |
| `POST /api/v1/downtimes`, `DELETE /api/v1/downtimes/{id}` / `GET /api/v1/downtimes` | `downtimes:write` / `objects:read` |
| `POST /api/v1/silences`, `DELETE /api/v1/silences/{id}` / `GET /api/v1/silences` | `silences:write` / `objects:read` |
| `GET /api/v1/oncall/now`, `GET /api/v1/schedules/{name}/timeline`, `/ics`, `/overrides`, `/stats` | `oncall:read` |
| `POST /api/v1/schedules/{name}/overrides`, `DELETE …/overrides/{id}` | `oncall:write` |
| `POST /api/v1/channels/{name}:test-notification` | `config:write` |
| `GET /api/v1/events`, `GET /api/v1/events:export`, `GET /api/v1/stream` | `events:read` |
| `POST /api/v1/metrics/query`, `GET /api/v1/objects/{id}/metrics` | `metrics:read` |
| `POST /api/v1/results` | `objects:write` |
| `GET /api/v1/heartbeats` / `POST /api/v1/heartbeats`, `DELETE /api/v1/heartbeats/{name}` / `GET`/`POST /api/v1/heartbeats/{name}/beat` | `objects:read` / `config:write` / `objects:write` |
| `POST /api/v1/config/bundles:plan`, `GET /api/v1/config/bundles:export` / `POST /api/v1/config/bundles:apply` | `objects:read` / `config:write` |
| `GET /api/v1/business-services:tree`, `GET /api/v1/objects/{id}/impact`, `GET /api/v1/business-services/{name}/sla` | `objects:read` |
| `POST /api/v1/reports/{name}:render` / `GET …/archive`, `GET …/archive/{id}` / `POST …:run` | `reports:render` / `objects:read` / `config:write` |
| `POST /api/v1/discovery/scans` / `GET /api/v1/discovery/scans`, `GET …/{id}` | `config:write` / `objects:read` |
| `GET /api/v1/agent/checks` | `objects:read` |
| `GET /api/v1/system/health`, `GET /api/v1/system/info` | — (anonymous) |
| `GET /api/v1/sites:overview` | `objects:read` |
| `POST /api/v1/sites/{name}:heartbeat`, `GET /api/v1/sites/{name}:pull` | `sites:connect` |
| `/api/v1/ai/conversations`, `/ai/providers`, `/ai/connections` (+ `:test`, `/models`), `/ai/tools`, `/ai/chats` (+ messages), `POST /api/v1/ai/chat` | `events:read` |
| `GET /api/v1/ai/actions` / `POST …/{id}:approve` / `POST …/{id}:deny` | `alerts:read` / `config:write` / `alerts:ack` |
| `GET/PUT /api/v1/ai/policy` | `admin:ai` |
| Raw routes with their own auth: `POST /api/v1/ingest/{source}` (+ `/alertmanager`), `GET /api/v1/ack/{token}`, `POST /api/v1/voice/gather/{token}`, `POST /api/v1/voice/inbound/{source}` (+ `/menu`, `/transcription`), `POST /api/v1/sms/inbound/{source}`, `GET /api/openapi.json`, `GET /api/docs`, `GET /healthz`, `GET /readyz`, `GET /metrics` | — |

The full per-operation list, including request and response schemas, is in the [REST API reference](/docs/reference/api/).
