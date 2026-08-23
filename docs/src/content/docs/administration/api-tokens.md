---
title: API tokens
description: "Creating, scoping, rotating and revoking Northplane API tokens — token format, scopes versus roles, expiry and IP binding, the aiAgent flag, the Admin tab, and how np, np-agent, MCP and federation use them."
sidebar:
  order: 5
---

API tokens are the credential for everything that is not a browser: `curl`, the `np` CLI, `np-agent`, MCP clients, CI pipelines and federation edges. A token is a bearer secret (`Authorization: Bearer np_…`) that carries its own permissions; the server never hands out JSON logins. Browser sessions are described in [Authentication](/docs/administration/authentication/).

## Token format and storage

- Cleartext: `np_` + 48 hexadecimal characters (24 random bytes), 51 characters in total. It is **shown exactly once** — on creation and on rotation — and cannot be retrieved later.
- Stored: the first 8 hex characters after `np_` as an indexed lookup `prefix`, and an **argon2id** hash of the 48-character body (same parameters as passwords). Authentication loads all tokens with the prefix and verifies the hash constant-time.
- Metadata: `name`, `scopes[]`, `roles[]`, `ipBind[]`, `aiAgent`, `expiresAt`, `lastUsedAt`, `createdBy`, `tenantId`, `version`, `createdAt`. `lastUsedAt` is touched at most once per minute.
- A token principal has `actorType: token` (or `ai_agent`), `actorId` = the token id, `name` = the token name, `tenantId` = the token's tenant. Audit entries written under a token show that actor.

## Scopes and roles

A token's effective permissions are its **scopes** ∪ the permissions of its **roles** (roles resolved in the token's tenant, nested `includes` expanded). Scopes use the same `resource:action` strings and wildcard rules as role permissions — `*:*`, `*`, `resource:*`, `*:action` — see the [permission reference](/docs/administration/users-roles-permissions/#permission-reference).

Prefer scopes for machines (least privilege, self-describing) and roles when a token should track a role that administrators maintain. Typical scope sets:

| Purpose | Scopes |
|---|---|
| `np-agent` pushing results | `objects:write` (+ `objects:read` when `pull: true`) |
| Heartbeat beat URL (cron job) | `objects:write` |
| Federation edge | `sites:connect` |
| Read-only dashboard / export | `objects:read`, `alerts:read`, `incidents:read`, `events:read`, `metrics:read` |
| MCP "Read only" preset | `objects:read,alerts:read,incidents:read,events:read,oncall:read,metrics:read,reports:render` |
| MCP "Read + operate" preset | read set + `alerts:ack,objects:write,maintenance:write` — note that `maintenance:write` is inert (no route or tool checks it), so with this preset only acknowledging works among the mutating tools; add `downtimes:write`, `silences:write` and `checks:run` yourself (or use the `ai-agent` role) for downtimes, silences and rechecks |
| MCP "Read + configure" preset | read set + `config:write,oncall:write` |
| CI applying bundles | `objects:read`, `config:write` (bundle plan needs read, apply needs write) |
| Break-glass | `*:*` (what `northplaned bootstrap-admin` mints) |

## Create a token

**UI:** **Admin → API tokens (API-Tokens)** has a **Token erstellen / Create token** card with Name and a comma-separated scopes field (default `objects:read,alerts:read`). After creation the token is displayed once in an amber box ("Einmalig sichtbar — jetzt sichern" / "Shown once — save it now"). The UI supports only name and scopes; roles, IP binding, expiry and rotation are API-only. Two other tabs mint tokens for you: **Admin → Agents** (scope `objects:write`, prefilled into an `agent.yaml`) and **Admin → MCP** (a scope preset with `aiAgent: true`).

**API:** [`POST /api/v1/api-tokens`](/docs/reference/api/operations/post_api_tokens/) (`admin:tokens`):

```bash
curl -s -X POST https://monitoring.example.net/api/v1/api-tokens \
  -H "Authorization: Bearer np_<admin token>" -H "Content-Type: application/json" \
  -d '{"name":"ci-deploy","scopes":["objects:read","objects:write"],"ipBind":["10.0.0.0/8"],"expiresAt":"2027-01-01T00:00:00Z"}'
```

```json
{"token":"np_<48 hex>",
 "meta":{"id":"0199…","tenantId":"00000000-0000-7000-8000-000000000001","name":"ci-deploy","prefix":"<8 hex>",
         "scopes":["objects:read","objects:write"],"ipBind":["10.0.0.0/8"],"expiresAt":"2027-01-01T00:00:00Z",
         "createdBy":"Administrator","version":1,"createdAt":"…"}}
```

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | Label shown in the list and used as the principal's name; `northplaned bootstrap-admin` refuses to run if a token named `bootstrap-admin` already exists |
| `scopes` | `scopes` or `roles` | Permission strings |
| `roles` | `scopes` or `roles` | Role names, resolved in the token's tenant |
| `ipBind` | no | List of IPs or CIDRs the token may be used from (see below) |
| `aiAgent` | no | Marks the token as an AI agent credential (see below) |
| `expiresAt` | no | RFC 3339 timestamp after which the token is rejected |

A missing `name`, or neither `scopes` nor `roles`, yields `422 np:validation/token`. The response is `201` with `token` (cleartext) and `meta` (what `GET` will show later). Audit action `token.create` (name, scopes, roles, aiAgent).

## Use a token

```bash
# REST
curl -s https://monitoring.example.net/api/v1/hosts -H "Authorization: Bearer np_<48 hex>"

# np CLI — flag or environment
np --server https://monitoring.example.net --token np_<48 hex> get hosts
export NP_SERVER=https://monitoring.example.net NP_TOKEN=np_<48 hex>
np get problems

# np-agent — agent.yaml `token:` or environment
NORTHPLANE_TOKEN=np_<48 hex> np-agent

# MCP over stdio
NORTHPLANE_TOKEN=np_<48 hex> northplaned mcp
```

| Consumer | Where the token goes | Notes |
|---|---|---|
| `np` | `--token` / `NP_TOKEN` | [np CLI](/docs/reference/cli-np/) |
| `np-agent` | `token` in `agent.yaml` or `NORTHPLANE_TOKEN` | needs `objects:write`; [Agent](/docs/monitoring/agent/) |
| MCP over HTTP (`/mcp`) | `Authorization: Bearer` on every request | MCP sessions are bound to the token's actor; [MCP server](/docs/ai/mcp-server/) |
| MCP over stdio | `NORTHPLANE_TOKEN` | the session inherits exactly the token's scopes; **`ipBind` is not evaluated** on this path (only expiry) |
| Federation edge | `federation.token` in `config.yaml` | scope `sites:connect`; [Tenants and sites](/docs/administration/tenants-and-sites/) |
| Heartbeats | `curl -H "Authorization: Bearer np_…" <beat URL>` | [Heartbeats](/docs/monitoring/heartbeats/) |
| Swagger UI `/api/docs` | "Authorize" | or the logged-in session cookie |

Inbound webhooks do **not** use platform tokens; an event source has its own `authMode` and secret — see [Event sources](/docs/alarming/event-sources/).

## List, rotate, revoke

| Endpoint | Behaviour |
|---|---|
| [`GET /api/v1/api-tokens`](/docs/reference/api/operations/get_api_tokens/) | Metadata of the tokens in the request's tenant (never the secret or hash) |
| [`POST /api/v1/api-tokens/{id}:rotate`](/docs/reference/api/operations/post_api_tokens_id_rotate/) | Mints a **new** token with the same name, scopes, roles, `ipBind`, `aiAgent` and `expiresAt` (`createdBy` = the caller), deletes the old one immediately and returns `200 {"token": "np_…", "meta": {…}}`. Audit `token.rotate` with `newId`. |
| [`DELETE /api/v1/api-tokens/{id}`](/docs/reference/api/operations/delete_api_tokens_id/) | Revokes: `204`; the token stops working on the next request. Audit `token.revoke`. |

All need `admin:tokens`. The Admin tab lists Name (with a sparkle marker for AI agent tokens), Prefix (`np_<prefix>…`), Scopes and "Zuletzt / Last used", with a **Widerrufen / Revoke** button per row.

```bash
# rotate: the new cleartext is in .token, the old token is gone at once
curl -s -X POST https://monitoring.example.net/api/v1/api-tokens/<id>:rotate \
  -H "Authorization: Bearer np_<admin token>"
```

Rotation is atomic from the API's point of view, but the consumer must be updated immediately — plan it like any credential rollover (mint → deploy → verify → revoke is the alternative when you need an overlap window: create a second token, switch consumers, then delete the first).

## Expiry and IP binding

- `expiresAt` in the past → `401 np:auth/invalid` with detail `token expired`. There is no grace period and no notification before expiry; the `lastUsedAt` column tells you which tokens are still in use.
- `ipBind` is a list of IPs or CIDRs. A request from an address outside the list → `401` with detail `token not valid from this address`. The address compared is the **TCP peer address** (`RemoteAddr`) — `X-Forwarded-For` is never consulted — so behind a reverse proxy every client appears as the proxy: either bind to the proxy's address or do not bind at all. See [TLS and reverse proxies](/docs/administration/tls-and-proxy/).
- `ipBind` is ignored on the bare-token path used by `northplaned mcp` (stdio), where there is no TCP peer.

## AI agent tokens

`"aiAgent": true` makes requests with the token authenticate as actor type **`ai_agent`** instead of `token`. Nothing else changes (permissions still come from scopes and roles), but audit entries, the **Audit log** tab (purple badge) and the API tokens list (sparkle) single these tokens out, and AI tools check the same permission names. The **Admin → MCP** tab always mints with `aiAgent: true`. See [Agent chat](/docs/ai/agent-chat/) and [MCP server](/docs/ai/mcp-server/).

## Tokens and tenants

A token belongs to the tenant it was minted in — the creator's active tenant, so a central admin mints a customer's token by sending `X-Northplane-Tenant: <tenant-id>` with the `POST`. The token then reads and writes that tenant, resolves its `roles` there, and `GET /api/v1/api-tokens` lists it only under that tenant. A token with `admin:tenants` (for example `*:*`) can itself switch tenants with the header. See [Tenants and sites](/docs/administration/tenants-and-sites/).

## The bootstrap token

`northplaned bootstrap-admin -config <path>` is the headless way to get a first credential without the browser `/setup` page: it mints a token named `bootstrap-admin` with scope `*:*` in the Default tenant (`createdBy: "northplaned init"`), prints it once together with `export NP_TOKEN=np_…`, and refuses to run if a token with that name exists ("revoke it first via API"). Creating any token closes the `/setup` first-run gate. See [northplaned CLI](/docs/reference/cli-northplaned/) and [Authentication](/docs/administration/authentication/).

## Security advice

- One token per consumer, named after it (`np-agent-<host>`, `site-<name>`, `ci-deploy`), with the smallest scope set that works. Revoke instead of sharing.
- Set `expiresAt` on everything that is not a long-lived agent, and review `lastUsedAt` periodically; unused tokens are the ones to delete.
- Use `ipBind` only when the server sees real client addresses (no proxy in front, or the proxy is the only allowed source).
- Treat `*:*` tokens (`bootstrap-admin`) as break-glass: use them to create scoped tokens, then revoke them.
- Token use is not rate-limited and not CSRF-checked (that protection is for cookies) — keep tokens out of browsers and query strings; the `?token=` form exists only for inbound event-source webhooks.
- Every create/rotate/revoke is in the audit log (`token.create`, `token.rotate`, `token.revoke`) — see [Observability](/docs/administration/observability/). The hardening checklist is in [Security](/docs/administration/security/).
