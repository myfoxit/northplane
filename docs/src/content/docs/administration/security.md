---
title: Security
description: Hardening checklist for a Northplane instance — transport, accounts and tokens, secrets, ingest authentication, browser protections, audit — plus the list of unauthenticated endpoints and the known gaps to be aware of.
sidebar:
  order: 13
---

Northplane ships with safe defaults — loopback listener, no plaintext on the network without an explicit decision, argon2id everywhere, encrypted secrets, a hash-chained audit log, strict CSP — but a production instance still needs a few deliberate choices. This page is the checklist; each item links to the page that explains the mechanism.

## Hardening checklist

1. **Terminate TLS** — either a certificate pair in `tls.*` or a trusted reverse proxy with `trustProxy: true`; never `tls.insecure` outside development. Set `baseUrl` to the public `https://` URL. → [TLS and reverse proxies](/docs/administration/tls-and-proxy/)
2. **Harden host SSH by identity, not source IP** — `deploy/harden-access.sh` (key-only auth, rescue sshd on 2222, opt-in port-based firewall with auto-rollback) is described in [Provisioning](/docs/deployment/provisioning/#host-ssh-hardening-anti-lockout).
3. **Expose only 443** — keep 8443 (the listener) reachable from the proxy only; open 9162/udp, 2023, 8123 or 4573 only when you actually run the trap receiver, ESPA, ESPA-X or FastAGI listeners, and only towards the devices that need them. → [Deployment overview](/docs/deployment/overview/)
4. **Decide how the first admin is created** — default-admin seeding with `NP_DEFAULT_ADMIN_EMAIL`/`NP_DEFAULT_ADMIN_PASSWORD` (and rotate that password after first login), or `NP_DEFAULT_ADMIN_DISABLED=1` and the interactive `/setup`, or `northplaned bootstrap-admin` headless. Never leave a generated password only in the logs. → [Authentication](/docs/administration/authentication/)
5. **Keep self-service signup off** unless you want it: `allowSignup: false` is the default; the production instance in [Environments](/docs/deployment/environments/) follows the repo variable `NORTHPLANE_ALLOW_SIGNUP` (default off). Self-registered users only get `viewer`.
6. **Use SSO or directory login** for people (OIDC with `adminGroup`/`idpGroups` mapping, or LDAP with `disableMissing: true`) and keep one local break-glass admin. → [Authentication](/docs/administration/authentication/), [Users, roles and permissions](/docs/administration/users-roles-permissions/)
7. **Scope API tokens** to the permissions they need, set `expiresAt`, use `ipBind` where the caller address is stable (remember it is the TCP peer, i.e. the proxy's address behind a proxy), rotate with `:rotate`, and never reuse the `*:*` bootstrap token for integrations. Agents need `objects:write` only; MCP tokens get the `aiAgent` flag. → [API tokens](/docs/administration/api-tokens/)
8. **Protect and back up `secret.key`** — mode `0600`, owned by the service user (uid 65532 in containers), copied to a safe place; without it every sealed secret is lost. Put credentials into the secret store and reference them as `$SECRET:name$` instead of pasting them into channel or source configs. → [Secrets](/docs/administration/secrets/)
9. **Authenticate every event source** — `authMode: token` (header, not query string) or `hmac`; `none` only for sources that are reachable from trusted networks alone. Set the Twilio auth token for inbound telephony so signatures are verified, and `allowFrom` for caller allow-lists. → [Event sources](/docs/alarming/event-sources/), [Voice and IVR](/docs/alarming/voice-and-ivr/)
10. **Sign outgoing webhooks** — give webhook subscriptions a `secret` and verify `X-Northplane-Signature` on the receiving side. → [Outgoing webhooks](/docs/alarming/webhooks-out/)
11. **Restrict the anonymous endpoints** at the proxy if your threat model requires it — `/metrics`, `/api/v1/system/health`, `/api/v1/system/info`, `/api/docs`, `/status/default`. See [Unauthenticated endpoints](#unauthenticated-endpoints).
12. **Keep the audit chain verifiable** — run `np audit verify` (or `POST /api/v1/audit:verify`) on a schedule and export the log to your SIEM with `GET /api/v1/audit:export`. → [Observability](/docs/administration/observability/#audit-log)
13. **Back up regularly** — `northplaned backup` plus `secret.key` and `config.yaml`; there is no built-in schedule. → [Storage → Backup](/docs/administration/storage/#backup)
14. **Upgrade deliberately** — pin image tags, read release notes, back up first. → [Upgrades](/docs/administration/upgrades/)
15. **Review the known gaps** below and decide whether they matter for your deployment.

## Transport

- Plaintext on a non-loopback listener is refused unless `trustProxy` or `tls.insecure` is set; with a certificate pair the minimum protocol is TLS 1.2 and an unloadable pair is fatal (no silent fallback).
- `trustProxy` trusts **only** `X-Forwarded-Proto` (first value). It must be enabled only when the proxy is the sole path to the listener and overwrites inbound forwarded headers — otherwise a client could obtain `Secure` cookies and HSTS over plaintext by sending the header itself.
- HSTS (`max-age=31536000; includeSubDomains`) is sent on HTTPS responses; `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin` always; a strict CSP on the UI and pages; `/api/*` carries no CSP. Details and the verbatim policies: [Security headers](/docs/administration/tls-and-proxy/#security-headers).
- Cloudflare or another CDN in front is fine: Caddy should declare it as a trusted proxy for its own logs; Northplane does not read client IPs from headers at all.

## Accounts and credentials

- Passwords: argon2id (`time=1`, 64 MiB, 4 threads, 32-byte key, 16-byte salt), minimum 12 characters, constant-time verification against a dummy hash for unknown accounts; login attempts are rate-limited per client IP (burst 8, one per 15 s). A disabled account cannot log in or keep using a session.
- Sessions: server-side rows, `np_session` cookie `HttpOnly` + `SameSite=Lax` + `Secure` (on HTTPS), 12 h or 30 days with "remember me"; logout deletes the row. Password changes do **not** invalidate other sessions — disable the user or wait for expiry if a session must die now.
- The break-glass admin is re-seeded on every start when no enabled local admin exists; the last enabled local admin cannot be disabled or deleted (`409 np:users/last-admin`).
- Roles: `operator` and `viewer` hold no `admin:*`; `config:write` (templates, rules, channels, bundles) is admin-only among the built-ins; custom roles can nest (`includes`) and map IdP groups (`idpGroups`). Token permissions = scopes ∪ role permissions.
- API tokens: `np_` + 48 hex characters, stored as prefix + argon2id hash, shown once; `expiresAt`, `ipBind`, `aiAgent`, `lastUsedAt`; `:rotate` issues a new secret and deletes the old one immediately. MCP over HTTP and stdio both authenticate with these tokens and inherit exactly their permissions.
- Secrets: AES-256-GCM under the 32-byte master key, write-only through the API (`GET /api/v1/secrets` returns names only), tenant-scoped, never logged (audit `secret.put` carries no value). The agent check pull (`GET /api/v1/agent/checks`) does not expand `$SECRET` references.

## Ingest and integrations

- Generic webhook ingest (`POST /api/v1/ingest/{source}`) authenticates per source: `token` (default — `Authorization: Bearer <secret>` **or** `?token=`; the query form leaks into access logs, prefer the header or HMAC), `hmac` (`X-Northplane-Signature` or `X-Hub-Signature-256`, HMAC-SHA256 over the raw body, hex, optional `sha256=` prefix), `basic` (password compared, username ignored), `none`. An empty secret makes `token`/`hmac`/`basic` fail closed; a disabled secret store (no master key) has the same effect. Rate limits are per source (`rateLimit` default 50/s, `burst` 200).
- Avoid ingest secrets starting with `np_` — the API middleware would treat them as API tokens and answer `401`.
- Source names are resolved across all tenants for ingest URLs (first match by slug order), so treat event-source names as globally unique.
- The Alertmanager receiver (`…/alertmanager`) does **not** check the source's `enabled` flag (only its auth) — disable by removing the secret or the source if you need to stop it.
- Inbound telephony: Twilio signatures are verified only when `config.twilioAuthToken` is set (as `$SECRET:…$`); `allowFrom` restricts callers (`403 np:ingress/caller`). `baseUrl` must match the public URL or signatures fail.
- Ack links (`GET /api/v1/ack/{token}`) are HMAC-signed with a server secret, valid 24 h, act only on open alerts and remain valid until expiry (re-clicking shows the same confirmation); the DTMF callback uses the same token. Do not forward notification e-mails containing ack links to untrusted recipients.
- Outgoing webhooks are signed with `X-Northplane-Signature: sha256=<hex>` when the subscription has a `secret`; deliveries go through the outbox with retries and a dead-letter queue ([Reliability](/docs/alarming/reliability/)).
- Bundle apply tokens (`ap_…`) expire after 10 minutes and are single-use; bundle bodies are capped at 8 MiB.
- Discovery scans refuse loopback, link-local and multicast ranges and anything larger than a /20.

## Browser protections

- The SPA is gated server-side: unauthenticated document navigations redirect to `/login`; API calls never redirect (they get `401` problem documents).
- CSRF: session-cookie requests whose browser sends `Sec-Fetch-Site: cross-site` are rejected (`403 np:auth/csrf`); cookies are `SameSite=Lax`; there is **no CORS** — the API cannot be called from another origin with the user's cookie, and token clients are expected to be server-side. Raw routes (ingest, ack link, health) are not CSRF-checked because they do not use sessions.
- `frame-ancestors 'none'` / `X-Frame-Options: DENY` prevent clickjacking; the only third-party origin in the CSP is `app.stepped.ai` (the embedded assistant widget).
- Every API response carries `X-Request-Id`, also stored in audit entries — useful for incident forensics.

## Unauthenticated endpoints

Reachable without any credential (restrict at the proxy if needed):

| Endpoint | What it exposes |
|---|---|
| `GET /healthz`, `GET /readyz` | liveness/readiness; `/readyz` names the storage dialect |
| `GET /metrics` | server self-metrics (request counts, queue depths, TSDB stats) |
| `GET /api/v1/system/health` | queue depths and subsystem counters |
| `GET /api/v1/system/info` | **version**, Go version, goroutines, heap, uptime, storage dialect, `aiEnabled` |
| `GET /api/openapi.json`, `GET /api/docs`, `GET /api/docs/{asset}` | the API specification and Swagger UI |
| `GET /docs/` | this documentation |
| `GET /status/default` (and any `/status/{slug}` configured as public) | a public status page for the default tenant: business-service root names with a coarse state, or an aggregate "Infrastruktur" row; German text, `Cache-Control: max-age=30`. Non-public pages require `?token=`; there is no API or UI to configure status pages in this version, so only `default` exists unless the `kv` entry `statuspage/<slug>` is written directly |
| `GET/POST /login`, `/setup` (while open), `/register` (when `allowSignup`), `/auth/oidc`, `/auth/callback`, `/auth/logout` | authentication pages |
| `POST /api/v1/ingest/{source}`, `POST /api/v1/ingest/{source}/alertmanager`, `POST /api/v1/voice/inbound/{source}[/menu\|/transcription]`, `POST /api/v1/sms/inbound/{source}` | per-source authentication (see above), not platform RBAC |
| `GET /api/v1/ack/{token}`, `POST /api/v1/voice/gather/{token}` | signed tokens |

Requires a token but no session: `/mcp` (Bearer `np_…`, 401 with `WWW-Authenticate: Bearer resource_metadata="/api/v1/whoami"` otherwise).

A Caddy snippet that keeps the operational endpoints internal:

```text title="Caddyfile (excerpt)"
monitoring.example.net {
	@internal {
		path /metrics /api/v1/system/* /api/docs* /api/openapi.json /status/*
		not remote_ip 10.0.0.0/8 192.168.0.0/16
	}
	respond @internal 403
	reverse_proxy 10.10.10.11:8443
}
```

Do not block `/healthz` if the proxy (or CI) probes it; the deploy workflow probes `http://localhost:8443/healthz` on the VM directly.

## Audit

Mutations, logins (`login.local`, `login.ldap`, `setup.admin`, `user.register`), token and secret operations, bundle applies, AI actions and federation applies are recorded with actor, tenant, source IP (TCP peer), request id and before/after snapshots in a SHA-256 hash chain. Verify with `np audit verify`; export NDJSON for long-term retention — the table itself is never purged. Not audited: reads, OIDC logins, failed logins, logouts. Full description: [Observability → Audit log](/docs/administration/observability/#audit-log).

## Known gaps — be aware

These are real behaviours of this version, documented so you can compensate; they are tracked in [Roadmap and known issues](/docs/project/roadmap-and-known-issues/).

| Area | Gap | Mitigation |
|---|---|---|
| RBAC | Role `scope.folder` / `scope.selector` / `scope.tenantId` are stored and editable but **not enforced** — a role's permissions apply to the whole tenant. | Use separate tenants for isolation; do not rely on folder scopes. |
| RBAC | System roles (`admin`, `operator`, `viewer`, `ai-agent`) are marked immutable in the UI but can be modified or deleted via `PUT`/`DELETE /api/v1/roles/{name}` with `admin:write`. | Restrict `admin:write`; watch `role.update`/`role.delete` audit entries. |
| Tenancy | `GET /api/v1/users` lists users of **all** tenants; e-mail addresses are globally unique. | Grant `admin:users` only to instance administrators. |
| Tenancy | `POST /api/v1/alerts/{id}:ack` ignores `X-Northplane-Tenant` (uses the caller's home tenant); `:resolve`/`:snooze` honour it. | Cross-tenant operators ack from a token/user in the customer tenant. |
| Proxy | `X-Forwarded-For` is not used: audit `sourceIp`, token `ipBind`, the login rate limiter and site heartbeat `sourceIp` see the proxy address. | Keep client-IP logs at the proxy; bind tokens to the proxy or omit `ipBind`. |
| Audit | No retention/purge; failed logins are not audited (OIDC logins and logouts are, as `login.oidc`/`logout`); on PostgreSQL chain verification can report false breaks (`jsonb`). `audit:export` streams without the 30 s request deadline. | Export regularly and page with `afterSeq`. |
| Sessions | Password change or admin reset does not invalidate existing sessions; no IdP (RP-initiated) logout. | Disable the user to cut access immediately. |
| Setup | Default-admin seeding closes `/setup` on default installs; the generated password is printed to the logs once. | Set `NP_DEFAULT_ADMIN_PASSWORD` or `NP_DEFAULT_ADMIN_DISABLED=1` explicitly. |
| Ingest | Alertmanager receiver ignores `enabled`; `?token=` accepted for `token` sources; ingest source names are global. | Remove the source/secret to stop a receiver; use headers or HMAC. |
| Tokens | `ipBind` is not evaluated for `northplaned mcp` (stdio) — only expiry. | Give MCP tokens narrow scopes and short expiry. |
| UI | The Admin page renders all 21 tabs regardless of permissions (non-admins see tabs whose calls return 403); the login/setup/register pages and the status page are German-only and unbranded. | Cosmetic; permissions are enforced server-side. |
| Status page | `/status/default` is public by default and cannot be configured or disabled through the API/UI. | Block `/status/*` at the proxy if you do not want a public page. |
| Web push | Web push subscription flow is incomplete in the UI (server side is ready). | Use FCM/APNs through the alarm app ([Mobile push](/docs/alarming/mobile-push/)). |
