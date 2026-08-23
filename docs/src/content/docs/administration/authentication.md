---
title: Authentication
description: "How people and machines authenticate to Northplane — local login, sessions and cookies, the first-run setup page and default admin, self-registration, OIDC single sign-on, LDAP directory login and sync, and where API tokens fit in."
sidebar:
  order: 2
---

Northplane has exactly two credential types: an **API token** (`Authorization: Bearer np_…`) for machines and a **session cookie** (`np_session`) for browsers. Everything else on this page — the login form, the first-run page, OIDC and LDAP — is a way to obtain one of the two. Authorization (what a principal may do) is covered in [Users, roles and permissions](/docs/administration/users-roles-permissions/).

## How a request is authenticated

For every request under `/api/` (and for `/mcp`) the server resolves a *principal* in this order:

1. If the `Authorization` header starts with `Bearer np_`, the value is looked up as an [API token](/docs/administration/api-tokens/). The principal's tenant is the token's tenant; its permissions are the token's scopes plus the permissions of the token's roles.
2. Otherwise, if the request carries the `np_session` cookie, the session is loaded, the user row is re-read, and the session's role names are expanded into permissions.
3. Otherwise the request is **anonymous** (no principal).

What you get back:

| Situation | Response |
|---|---|
| The bearer token or the cookie is present but invalid (unknown token, expired token, token used from a non-bound IP, expired session, disabled user) | `401 np:auth/invalid` — the `detail` says why (`invalid token`, `token expired`, `token not valid from this address`, `session invalid`, `user invalid`). This is decided in the middleware, so it applies to every API path, including ones that need no login. |
| Anonymous request to a route that requires a permission (or a login) | `401 np:auth/required` |
| Authenticated, but the route's permission is not held | `403 np:auth/forbidden` — `detail` is the missing permission name |

Only bearer values that start with `np_` are inspected; a `Bearer abc123` sent to an inbound webhook (`/api/v1/ingest/{source}`) is left alone for the event source's own auth mode (see [Event sources](/docs/alarming/event-sources/)).

`GET /api/v1/whoami` requires no permission (401 when anonymous) and returns `{actorType, actorId, name, tenantId, permissions[]}`. `actorType` is `user`, `token`, `ai_agent` or `system`; `tenantId` is the principal's **home** tenant, even when the request carries an `X-Northplane-Tenant` header (see [Tenants and sites](/docs/administration/tenants-and-sites/)).

```bash
curl -s https://monitoring.example.net/api/v1/whoami -H "Authorization: Bearer np_<48 hex>"
```

```json
{"actorType":"token","actorId":"0199…","name":"ci-deploy","tenantId":"00000000-0000-7000-8000-000000000001","permissions":["objects:read","objects:write"]}
```

## Local login (`/login`)

Local login is a plain HTML form, not a JSON endpoint: `GET /login` renders it, `POST /login` with the form fields `email`, `password` and optionally `remember=1` consumes it. There is no `/api/v1/login`; scripts and integrations use API tokens instead.

What `POST /login` does, in order:

1. **Rate limit** per client IP (see below). When throttled the page re-renders with the message "Zu viele Anmeldeversuche. Bitte kurz warten." and a `Retry-After: 30` header.
2. **Look up the user by e-mail.** Disabled accounts are excluded from the lookup, so they fail exactly like unknown accounts.
3. **Directory accounts** (non-local users whose subject starts with `ldap|`, when LDAP is configured) are verified against the directory with a search-then-bind (see [LDAP](#ldap-and-active-directory)). Their session roles are the roles from the last sync (fallback `viewer`); the audit action is `login.ldap`.
4. **Everyone else** goes through an argon2id verification — against the real hash for local users, against a fixed dummy hash for unknown or non-local (OIDC) accounts, so timing does not reveal whether an e-mail exists. Any failure (unknown, disabled, non-local, wrong password) produces the same "Anmeldung fehlgeschlagen." with HTTP 401.
5. **Session roles** are the user's role names (legacy rows with an empty role list fall back to `["admin"]`). The audit action is `login.local`.
6. A session is minted — 12 h, or 30 days with "remember me" — in the user's **home tenant** (empty means the Default tenant), the `np_session` cookie is set and the browser is redirected to `/`.

:::note[The login page is German]
`/login`, `/setup` and `/register` are server-rendered, JavaScript-free pages with hard-coded German labels (E-Mail, Passwort, "Angemeldet bleiben", "Anmelden"). They are not branded by [Branding and themes](/docs/administration/branding-and-themes/). The page shows a **Single Sign-On** button when OIDC is configured and a "Neu hier? Konto erstellen" link when self-registration is enabled. While the first-run gate is open, `GET /login` redirects to `/setup`.
:::

### Login rate limiter

A per-client-IP token bucket shared by `/login`, `/setup` and `/register`: burst **8**, refill **1 token every 15 s** (about 4 attempts per minute sustained). The bucket map is garbage-collected when it grows past 4096 entries (buckets idle for more than 1 h are dropped). The client IP is the host part of the TCP peer address — `X-Forwarded-For` is **not** consulted — so behind a reverse proxy all users share the proxy's bucket (see [TLS and reverse proxies](/docs/administration/tls-and-proxy/)). The limits are hard-coded.

### Password policy and hashing

- Minimum length **12 characters** (counted in Unicode runes), enforced everywhere a local password is set: `/setup`, `/register`, `POST /api/v1/users` with a password, `POST /api/v1/users/{id}:set-password`, `POST /api/v1/users/me:change-password`. There is no other complexity rule.
- Hash: **argon2id** with `time=1`, `memory=64 MiB`, `threads=4`, `keyLen=32` and a 16-byte random salt, stored as `hex(salt)$hex(hash)`; verification is constant-time. API token bodies are hashed with the same function.
- The hash is never serialised by the API (`passHash` is excluded from every user representation).

## Sessions and cookies

| Property | Value |
|---|---|
| Session id | `base64url(hex(24 random bytes))`, stored server-side in the `sessions` table (`id, user_id, tenant_id, data{roles,groups}, created_at, expires_at`) — sessions survive restarts |
| Cookie | `np_session`; `Path=/`; `HttpOnly`; `SameSite=Lax`; `Secure` when the request is HTTPS (direct TLS, or `X-Forwarded-Proto: https` with `trustProxy: true`); `Max-Age` = TTL |
| TTL | 12 h by default; 30 days when "Angemeldet bleiben" (remember me) is ticked; OIDC sessions are always 12 h. Not configurable. |
| Per-request checks | The user row is reloaded on every request: a **disabled** user is rejected immediately (`user invalid`), an expired session answers `session invalid` |
| Permission changes | Role **names** are stored in the session and expanded into permissions on every request — editing a role's permission list takes effect immediately; changing a *user's* role list takes effect at the next login |
| Last seen | `users.last_seen_at` is stamped at most once per minute (shown in **Admin → Users (Benutzer)**) |
| Cleanup | Expired sessions are purged every 10 minutes by the janitor |
| Password change | Changing or resetting a password does **not** invalidate existing sessions |
| Logout | `GET /auth/logout` deletes the server-side session, clears the cookie (`Max-Age=-1`) and redirects to `/login`. There is no IdP (RP-initiated) logout for OIDC sessions. |

## Cross-site request protection

- A **cookie-authenticated** API request whose browser sets `Sec-Fetch-Site: cross-site` is rejected with `403 np:auth/csrf` ("cross-site request blocked"). Token-authenticated requests are unaffected. The check is applied to routes registered in the API route table; raw routes such as `/api/v1/ingest/{source}`, `/api/v1/ack/{token}` and `/mcp` are not wrapped (they have their own authentication).
- The cookie is `SameSite=Lax`. The login, setup and register forms carry no CSRF token; they rely on `SameSite=Lax` by design.
- There are no CORS headers anywhere: the API cannot be called from another origin in a browser. Server-side integrations use tokens.
- The SPA shell is gated server-side: an unauthenticated *document* navigation (GET/HEAD with `Accept: text/html`, not under `/assets/`) is redirected `302 /login`; API calls are never redirected (they get a 401 problem document, on which the SPA itself navigates to `/login`).

## First run: `/setup` and the default admin

A fresh database has no accounts. Two mechanisms can create the first administrator, and they interact:

**The `/setup` page.** Its gate (`FirstRunOpen`) is open only while **no local user exists and no API token exists in the Default tenant**. SSO-provisioned (non-local) users do not close it; a storage error fails closed. `GET /setup` shows a form (Name, E-Mail, Passwort ≥ 12 characters, Bestätigen); `POST /setup` is rate-limited, re-checks the gate under a mutex (a racing second POST gets `409 setup already completed`), creates a **local user with role `admin`**, mints a 12 h session in the Default tenant, writes the audit action `setup.admin` and redirects to `/`. When the gate is closed, `/setup` redirects to `/login`.

**Default admin seeding.** `northplaned serve` runs `seedDefaultAdmin` on **every** start. It creates a local admin when all of the following hold: `NP_DEFAULT_ADMIN_DISABLED` is unset, `NP_DEFAULT_ADMIN_PASSWORD` is not set-to-empty, no *enabled local* user with role `admin` exists, and the chosen e-mail is free.

| Variable | Default | Effect |
|---|---|---|
| `NP_DEFAULT_ADMIN_DISABLED` | unset | Any non-empty value skips the seeding entirely |
| `NP_DEFAULT_ADMIN_EMAIL` | `admin@localhost` | E-mail of the seeded account |
| `NP_DEFAULT_ADMIN_NAME` | `Administrator` | Display name |
| `NP_DEFAULT_ADMIN_PASSWORD` | unset | Password to use. **Unset** → a random 32-hex-character password is generated and logged **once** at WARN level ("seeded default admin with a GENERATED password — save it now, it is not recoverable"). **Set but empty** → seeding is skipped. Set → used, logged as "seeded default admin — CHANGE THE PASSWORD". |

There is no hard-coded default password. These are process environment variables, not `config.yaml` keys — see [Configuration](/docs/administration/configuration/).

:::caution[On a default install `/setup` is closed]
Because the default admin is seeded before the HTTP listener starts, a default install already has a local user when you first open the browser — `/setup` redirects to `/login`, and the "first run: open …/setup" log line is printed only when the gate really is open. Log in with `NP_DEFAULT_ADMIN_EMAIL` and the logged or configured password. If you want the interactive `/setup` flow instead, start with `NP_DEFAULT_ADMIN_DISABLED=1` (or `NP_DEFAULT_ADMIN_PASSWORD=` empty) — this is what the E2E harness and `deploy/.env.example` do.
:::

Other things that close the gate:

- **Any API token** in the Default tenant. `northplaned bootstrap-admin` is the headless alternative to `/setup`: it mints a token named `bootstrap-admin` with scope `*:*` (see [API tokens](/docs/administration/api-tokens/)).
- **Demo mode** (`serve --demo` / `NORTHPLANE_DEMO=true`) seeds the local users `operator@demo.local` (role `operator`) and `viewer@demo.local` (role `viewer`), which are local users — see [Demo mode](/docs/getting-started/demo-mode/).

## Self-registration (`/register`)

- Enabled by `allowSignup: true` in `config.yaml` or `NORTHPLANE_ALLOW_SIGNUP=true`; otherwise `GET`/`POST /register` answer 404.
- While the first-run gate is open, `/register` redirects to `/setup` so the first visitor cannot sign up as a viewer and silently close the gate.
- A successful registration creates a **local** user with roles `["viewer"]` in the Default tenant, mints a 12 h session and writes the audit action `user.register`. E-mail addresses are unique across the instance (duplicate → "Diese E-Mail ist bereits registriert.").
- The login page shows the "Konto erstellen" link only when signup is enabled.

Registered users can only read (see the `viewer` role in [Users, roles and permissions](/docs/administration/users-roles-permissions/)); an administrator promotes them in **Admin → Users (Benutzer)**. Leave signup off unless you want a public read-only console — see [Security](/docs/administration/security/).

## OIDC single sign-on

OIDC (Authorization Code + PKCE, S256) is configured in the `oidc:` section of `config.yaml`. Microsoft Entra ID and Keycloak are the providers the code was written against.

| Key | Type | Default | Env override | Meaning |
|---|---|---|---|---|
| `oidc.issuer` | string | `""` (= OIDC off) | `NORTHPLANE_OIDC_ISSUER` | Discovery URL. Empty disables SSO entirely. |
| `oidc.clientId` | string | — | `NORTHPLANE_OIDC_CLIENT_ID` | Required as soon as any `oidc.*` key is set |
| `oidc.clientSecret` | string | — | `NORTHPLANE_OIDC_CLIENT_SECRET` | |
| `oidc.scopes` | list | `[openid, profile, email, groups]` | — | Scopes requested; explicitly emptied → `openid profile email` |
| `oidc.groupsClaim` | string | `groups` | — | ID-token claim that holds the group list |
| `oidc.adminGroup` | string | — | — | Any user carrying this group value also gets role `admin` |

```yaml title="config.yaml"
baseUrl: "https://monitoring.example.net"   # required: the redirect URL is baseUrl + /auth/callback
oidc:
  issuer: "https://login.microsoftonline.com/<tenant>/v2.0"
  clientId: "…"
  clientSecret: "…"
  adminGroup: "<entra-group-object-id>"
```

Register `https://monitoring.example.net/auth/callback` as the redirect URI at the provider.

**Flow.** `GET /auth/oidc` (the **Single Sign-On** button) stores a random `state` and PKCE verifier in the cookies `np_oidc_state` / `np_oidc_verifier` (`Path=/auth`, HttpOnly, `Secure` on HTTPS, 600 s) and redirects to the provider. `GET /auth/callback` checks the state, exchanges the code with the verifier, requires an `id_token`, verifies it against `clientId`, and reads the claims `name` (fallback `email`) and `email`. The user is provisioned or updated by **subject** = `issuer + "|" + sub` (name, e-mail, last seen). Then a 12 h session is minted and the browser lands on `/`. Any failure re-renders the login page with "SSO-Anmeldung fehlgeschlagen: …". Calling `/auth/oidc` without OIDC configured answers `501 SSO not configured`.

**Group → role mapping.** The groups in `groupsClaim` are matched (exact string) against the `idpGroups` of the roles in the **Default tenant**; every matching role is granted. A user in `adminGroup` additionally gets `admin`. With no match the user gets `["viewer"]`. Roles are recomputed at every login from the IdP groups; the user row usually keeps an empty role list. See [Roles](/docs/administration/users-roles-permissions/) for `idpGroups`.

:::note[OIDC behaviour to know]
- `baseUrl` must be set — the redirect URL is derived from it.
- OIDC sessions are always in the **Default tenant** and always 12 h (no "remember me").
- OIDC users are rows with `local: false` and no password; they cannot use the password form. A **disabled** account is never resurrected by SSO ("account disabled").
- OIDC logins and logouts are **not** written to the audit log, and there is no RP-initiated logout at the IdP.
- If discovery fails at boot, SSO is disabled with a warning and the server still starts.
- The `Secure` flag on the OIDC cookies follows `trustProxy` like the session cookie.
:::

## LDAP and Active Directory

The `ldap:` section enables two things: a background **directory sync** that provisions and disables users, and **password verification** on `/login` for synced users. LDAP is on when `ldap.url` is set.

| Key | Default | Env override | Meaning |
|---|---|---|---|
| `ldap.url` | `""` (= off) | `NORTHPLANE_LDAP_URL` | `ldap://host:389` or `ldaps://host:636` (must start with one of the two) |
| `ldap.startTls` | `false` | — | Upgrade `ldap://` with StartTLS before any bind |
| `ldap.insecureSkipVerify` | `false` | — | Skip TLS verification (TLS 1.2 minimum, `ServerName` = host of `url`) |
| `ldap.bindDn` | — | `NORTHPLANE_LDAP_BIND_DN` | Service account; when set, `bindPassword` is required |
| `ldap.bindPassword` | — | `NORTHPLANE_LDAP_BIND_PASSWORD` | |
| `ldap.baseDn` | — | `NORTHPLANE_LDAP_BASE_DN` | Required when LDAP is configured |
| `ldap.userFilter` | `(&(objectClass=person)(mail=*))` | — | User search filter |
| `ldap.userAttr` | `mail` | — | Login / e-mail attribute (AD: `userPrincipalName`) |
| `ldap.nameAttr` | `cn` | — | Display name; empty → e-mail |
| `ldap.idAttr` | `""` (= DN) | — | Stable id attribute (`entryUUID`, `objectGUID`); binary values are hex-encoded |
| `ldap.groupAttr` | `memberOf` | — | Membership attribute read from the user entry |
| `ldap.groupFilter` | — | — | Optional member search with `{dn}` / `{user}` placeholders (escaped) |
| `ldap.groupBaseDn` | = `baseDn` | — | Base for `groupFilter` |
| `ldap.syncInterval` | `15m` | — | Sync period (≤ 0 → 15 m) |
| `ldap.defaultRoles` | `[viewer]` | — | Roles when no group maps |
| `ldap.adminGroup` | — | — | Group DN or CN mapped to `admin` (lower-cased compare) |
| `ldap.disableMissing` | `true` | — | Disable `ldap\|` users that vanished from the directory |

```yaml title="config.yaml"
ldap:
  url: "ldaps://dc1.example.net:636"
  bindDn: "cn=svc-northplane,ou=service,dc=example,dc=net"
  bindPassword: "…"        # or NORTHPLANE_LDAP_BIND_PASSWORD
  baseDn: "dc=example,dc=net"
  userFilter: "(&(objectClass=person)(mail=*))"
  userAttr: mail            # AD: userPrincipalName
  idAttr: ""                # AD: objectGUID, OpenLDAP: entryUUID (stable across DN moves)
  groupAttr: memberOf
  adminGroup: "cn=northplane-admins,ou=groups,dc=example,dc=net"
  syncInterval: 15m
  defaultRoles: [viewer]
  disableMissing: true
```

**Sync pass.** The `ldap-sync` worker runs once at boot and then every `syncInterval` (concurrent runs coalesce). It searches the whole subtree under `baseDn` (paged, 500 per page) for `dn`, `userAttr`, `nameAttr`, `groupAttr` and `idAttr`. Per entry: subject = `ldap|` + lower-cased DN (or `ldap|` + the `idAttr` value), e-mail lower-cased, roles from the `idpGroups` of the roles in the Default tenant (matching is lower-cased and accepts the full group DN **or** the first RDN value, i.e. the `cn`), plus `adminGroup`, plus `defaultRoles` when nothing matched. Then it reconciles:

- creates missing users as `local: false` without a password, home tenant Default;
- updates name, e-mail and roles when they changed;
- skips a **locally disabled** account (never resurrects it) and skips an entry whose e-mail is already taken by another account (warning);
- with `disableMissing: true`, disables `ldap|` users that were not seen;
- never touches **local** (break-glass) users.

The result `{created, updated, unchanged, disabled, skipped, warnings[]}` is shown in the **Verzeichnis-Sync (LDAP)** card at the bottom of **Admin → Users (Benutzer)** (URL, interval, last run, counts, warnings, **Jetzt synchronisieren**) and returned by the API.

**Login verification.** For a user whose subject starts with `ldap|`, `POST /login` performs a search-then-bind: optional service bind, search `(&<userFilter>(<userAttr>=<email>))` which must return exactly one entry, then bind as that DN with the submitted password. An empty password is rejected before any network call. Session roles are the roles from the last sync (fallback `viewer`); audit action `login.ldap`.

**Endpoints** (both need `admin:users`):

| Endpoint | Behaviour |
|---|---|
| [`GET /api/v1/directory/status`](/docs/reference/api/operations/get_directory_status/) | `{configured, url, syncInterval, lastSyncAt, lastError, lastResult}`; `{configured:false}` when LDAP is off |
| [`POST /api/v1/directory:sync`](/docs/reference/api/operations/post_directory_sync/) | Runs a pass now and returns the result; `501 np:directory/unconfigured` without LDAP, `502 np:directory/sync` on failure; audit `directory.sync` |

:::note
LDAP-synced users always get the Default tenant as home tenant, and group mapping only looks at roles in the Default tenant. The Users tab marks them with an "ldap" badge.
:::

## Machine authentication

Everything that is not a browser authenticates with an API token. The token is created in **Admin → API tokens (API-Tokens)** or via the API and is shown once — see [API tokens](/docs/administration/api-tokens/) for scopes, expiry and IP binding.

| Client | How the token is supplied | Minimum permissions |
|---|---|---|
| REST / curl | `Authorization: Bearer np_…` | whatever the routes require |
| `np` CLI | `--token np_…` or env `NP_TOKEN` (server: `--server` / `NP_SERVER`, default `https://localhost:8443`) | per command, see [np CLI](/docs/reference/cli-np/) |
| `np-agent` | `token:` in `agent.yaml` or env `NORTHPLANE_TOKEN` | `objects:write` (+ `objects:read` when `pull: true`), see [Agent](/docs/monitoring/agent/) |
| MCP over HTTP (`/mcp`) | `Authorization: Bearer np_…` on every request; an anonymous request gets `401` with `WWW-Authenticate: Bearer resource_metadata="/api/v1/whoami"`; MCP sessions are bound to the token's actor (another token reusing an `Mcp-Session-Id` → 403) | see [MCP server](/docs/ai/mcp-server/) |
| MCP over stdio (`northplaned mcp`) | env `NORTHPLANE_TOKEN`; the session inherits exactly the token's scopes; `ipBind` is **not** evaluated on this path | see [MCP server](/docs/ai/mcp-server/) |
| Federation edge | `federation.token` in the edge's `config.yaml`, minted on the main instance | `sites:connect`, see [Tenants and sites](/docs/administration/tenants-and-sites/) |
| Heartbeat beats | `GET`/`POST /api/v1/heartbeats/{name}/beat` with a bearer token | `objects:write` |
| Swagger UI (`/api/docs`) | "Authorize" with an `np_…` token, or the logged-in cookie (`withCredentials`) | — |

Inbound webhooks, telephony callbacks and ack links do **not** use platform credentials: event sources have their own `authMode` (`token` / `hmac` / `basic` / `none`), ack links are HMAC-signed one-time URLs. See [Event sources](/docs/alarming/event-sources/) and [Acknowledge and snooze](/docs/alarming/acknowledge-and-snooze/).

## Error codes

| Code | HTTP | When |
|---|---|---|
| `np:auth/required` | 401 | No principal on a protected route (also `whoami`, `branding`, preferences, push subscriptions) |
| `np:auth/invalid` | 401 | Bad, expired or IP-bound token; invalid session; disabled user |
| `np:auth/forbidden` | 403 | Missing permission (`detail` names it) |
| `np:auth/csrf` | 403 | Cookie-authenticated request with `Sec-Fetch-Site: cross-site` |
| `np:auth/bad-password` | 403 | Wrong current password on `POST /api/v1/users/me:change-password` |
| `np:directory/unconfigured` | 501 | `directory:sync` without LDAP |
| `np:directory/sync` | 502 | LDAP sync failed |

All errors are RFC 9457 problem documents — see [API overview](/docs/reference/api-overview/).
