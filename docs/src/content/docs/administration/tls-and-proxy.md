---
title: TLS and reverse proxies
description: How northplaned decides between TLS and plaintext, how to terminate TLS directly or behind Caddy/nginx, what trustProxy really does, and the security headers every response carries.
sidebar:
  order: 7
---

Northplane serves a single HTTP listener (`listen`, default `127.0.0.1:8443`) for the API, the UI, SSE streams, MCP and the docs. It can terminate TLS itself from a certificate/key pair, or sit behind a TLS-terminating reverse proxy. What it will **not** do is silently serve plaintext on a network interface: that combination is refused at start-up unless you tell it that a trusted proxy is in front.

## How the listener decides

The decision is made once, when `serve` opens the listener (there is no certificate hot-reload and no ACME/autocert — use Caddy for automatic certificates):

| Configuration | Result |
|---|---|
| `tls.certFile` **and** `tls.keyFile` set | TLS with the loaded pair, minimum TLS 1.2, scheme `https`. If the pair cannot be loaded the server **refuses to start** (`TLS cert load failed, refusing to start insecure: …`) — it never falls back to plaintext. |
| no certificate, and `tls.insecure: true` **or** `trustProxy: true` **or** the bound address is loopback | Plaintext HTTP with the warning `server: serving plaintext HTTP (loopback/dev or behind a TLS-terminating proxy — A-15.10 requires TLS in production)`. |
| no certificate, non-loopback listener (`:8443`, `0.0.0.0:8443`, `[::]:8443`, a LAN address) | Fatal: `no TLS configured on a non-loopback listener — set tls.certFile/keyFile, or trustProxy behind a TLS-terminating proxy, or tls.insecure for dev`. |

"Loopback" is determined from the **bound** address: `127.0.0.1:8443` and `[::1]:8443` are loopback; `:8443`, `0.0.0.0:8443` and `[::]:8443` are not, even on a single-host machine. Setting only one of `tls.certFile`/`tls.keyFile` is rejected already at [config validation](/docs/administration/configuration/#validation-errors).

:::caution[Docker images listen on all interfaces]
The container image sets `NORTHPLANE_LISTEN=:8443`. It therefore refuses to start until you provide `NORTHPLANE_TLS_CERT_FILE`/`NORTHPLANE_TLS_KEY_FILE`, or `NORTHPLANE_TRUST_PROXY=true` (behind Caddy, the Compose default), or `NORTHPLANE_TLS_INSECURE=true` (trial only). See [Installation](/docs/getting-started/installation/).
:::

## Terminating TLS in northplaned

Use this when nothing sits in front of the server — a small site, an edge instance on a customer LAN, an appliance.

1. Obtain a PEM certificate chain and key (from your CA, or a self-signed pair for a LAN). The files must be readable by the user running `northplaned` (the systemd unit written by `init` runs as user `northplane` with `ProtectSystem=strict`; keep the files outside the data directory or add them to `ReadWritePaths`/make them world-readable as appropriate).
2. Configure the listener and the pair:

   ```yaml title="/etc/northplane/config.yaml"
   listen: ":8443"
   baseUrl: "https://monitoring.example.net:8443"
   tls:
     certFile: "/etc/northplane/tls/fullchain.pem"
     keyFile: "/etc/northplane/tls/privkey.pem"
   ```

   or, in a container, `NORTHPLANE_TLS_CERT_FILE=/certs/fullchain.pem` and `NORTHPLANE_TLS_KEY_FILE=/certs/privkey.pem` with the files bind-mounted.
3. Restart. The log line `northplane: listening addr=:8443 scheme=https …` confirms TLS is active.
4. Renewals: the pair is read once at start. After replacing the files, restart `northplaned` (`systemctl restart northplaned`).

With direct TLS, `r.TLS` is set on every request, so `Secure` cookies and HSTS are emitted without any further configuration, and `trustProxy` must stay `false`.

## Behind a TLS-terminating reverse proxy

This is the reference deployment: Caddy (or nginx, Traefik, Cloudflare + Caddy …) terminates TLS on 443 and forwards plaintext HTTP to `northplaned` on 8443. Configure Northplane with:

```yaml title="config.yaml"
listen: ":8443"            # or 127.0.0.1:8443 if the proxy runs on the same host
baseUrl: "https://monitoring.example.net"
trustProxy: true
```

(or `NORTHPLANE_LISTEN=:8443`, `NORTHPLANE_TRUST_PROXY=true`, `NORTHPLANE_BASE_URL=https://…` as the Compose stacks do).

### What trustProxy does — and does not do

`trustProxy: true` changes exactly one thing: `auth.RequestIsHTTPS` treats a request as HTTPS when the **first** value of `X-Forwarded-Proto` equals `https` (case-insensitive), in addition to a real TLS connection. That flag drives:

- the `Secure` attribute on the session cookie `np_session` and the OIDC state/verifier cookies;
- the `Strict-Transport-Security` header;
- the start-up rule above (plaintext on a non-loopback listener is allowed).

It does **not**:

- read `X-Forwarded-For`, `X-Real-IP` or `Forwarded`. The client address used for audit `sourceIp`, API-token `ipBind`, the login rate limiter and site heartbeat `sourceIp` is always the TCP peer (`RemoteAddr`) — behind a proxy that is the proxy's address. Consequences: token IP binding must target the proxy's address (or be omitted), the login rate limit is shared by everyone behind the same proxy, and audit entries show the proxy IP. (The config comment mentions `X-Forwarded-For`; the implementation does not use it.)
- rewrite URLs. Links in notifications, ack links and the OIDC redirect come from `baseUrl`, so set it to the public URL.

Enable `trustProxy` **only** when the proxy is the sole path to the listener and strips/overwrites inbound `X-Forwarded-Proto`; otherwise a client could claim `https` and receive `Secure` cookies over plaintext. Bind the listener to the proxy-facing interface or firewall 8443 so that only the proxy reaches it.

### Caddy

The bundled Compose stack uses this two-line `Caddyfile`: with `DOMAIN` unset Caddy issues an internal self-signed certificate for `localhost`; with a public DNS name it obtains a Let's Encrypt certificate automatically.

```text title="caddy/Caddyfile"
# DOMAIN=localhost (default) → Caddy issues an internal self-signed cert.
# DOMAIN=monitoring.example.net (public DNS → this host) → automatic Let's Encrypt.
{$DOMAIN:localhost} {
	reverse_proxy northplane:8443
}
```

A stand-alone Caddy in front of a VM (the production pattern described in [Proxmox VM deployment](/docs/deployment/proxmox-vm/)) adds compression and an active health check against `/healthz`:

```text title="/etc/caddy/sites/monitoring.caddy"
monitoring.example.net {
	encode zstd gzip
	reverse_proxy 10.10.10.11:8443 {
		health_uri      /healthz
		health_interval 30s
		health_timeout  5s
	}
}
```

Caddy sets `X-Forwarded-Proto` and `X-Forwarded-For` by default and streams responses without buffering, so SSE (`/api/v1/stream`), the NDJSON export and MCP work unchanged. If Cloudflare or another proxy sits in front of Caddy, declare it with `servers { trusted_proxies static <ranges> }` so Caddy keeps the original client address in its own logs (Northplane itself does not use it).

### nginx

A minimal server block. The important parts are the forwarded-proto header, disabled buffering and a long read timeout for the streaming paths, and a request-body limit large enough for bundle uploads (8 MiB):

```text title="/etc/nginx/conf.d/northplane.conf"
server {
    listen 443 ssl http2;
    server_name monitoring.example.net;

    ssl_certificate     /etc/letsencrypt/live/monitoring.example.net/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/monitoring.example.net/privkey.pem;

    client_max_body_size 9m;   # bundles are capped at 8 MiB by the server

    location / {
        proxy_pass         http://127.0.0.1:8443;
        proxy_http_version 1.1;
        proxy_set_header   Host              $host;
        proxy_set_header   X-Forwarded-Proto $scheme;
        proxy_set_header   Connection        "";
        # long-lived responses: SSE stream, NDJSON export, agent chat, MCP
        proxy_buffering    off;
        proxy_read_timeout 1h;
        proxy_send_timeout 1h;
    }
}
```

The SSE hub sends a `: ping` comment every 15 s, which keeps idle proxies from closing the stream; if your proxy has a shorter idle timeout, raise it or the UI's live updates (and `curl -N …/api/v1/stream`) will reconnect constantly.

### Health checks through the proxy

`/healthz` (plain `ok`) and `/readyz` (JSON, 503 when a subsystem is down) need no credentials and are the right probes for proxies and orchestrators. Do not send an `Authorization: Bearer np_…` header from a probe: an invalid `np_` token is rejected with 401 on every path served by the API handler, including `/healthz`. See [Observability](/docs/administration/observability/).

## Listen address examples

| `listen` | Meaning | Plaintext allowed without TLS? |
|---|---|---|
| `127.0.0.1:8443` (default) | IPv4 loopback only | yes |
| `[::1]:8443` | IPv6 loopback only | yes |
| `:8443` | all interfaces, IPv4 and IPv6 | no — needs TLS, `trustProxy` or `tls.insecure` |
| `0.0.0.0:8443` / `[::]:8443` | all interfaces | no |
| `10.10.10.11:8443` | one interface | no |
| `:https` | named port (443) | no |
| `127.0.0.1:0` | kernel-assigned port (tests) | yes |

Ports below 1024 need `CAP_NET_BIND_SERVICE` or root; the reference deployments keep 8443 and let the proxy own 80/443. The other network ports Northplane may open (trap receiver 9162/udp, ESPA 2023, ESPA-X 8123, FastAGI 4573) are unrelated to the HTTP listener and are configured on the respective event sources — see [Deployment overview](/docs/deployment/overview/).

## Security headers

Every response carries hardening headers; they are fixed in code and cannot be configured:

| Header | Value | When |
|---|---|---|
| `X-Content-Type-Options` | `nosniff` | always |
| `X-Frame-Options` | `DENY` | always |
| `Referrer-Policy` | `same-origin` | always |
| `Strict-Transport-Security` | `max-age=31536000; includeSubDomains` | only when the request is HTTPS (direct TLS, or `trustProxy` + `X-Forwarded-Proto: https`) |
| `Content-Security-Policy` | see below | all paths except `/api/*` (which carry no CSP); `/docs/*` has its own policy |

The SPA / server-rendered pages policy (verbatim):

```text
default-src 'self'; img-src 'self' data: https://app.stepped.ai; style-src 'self' 'unsafe-inline'; script-src 'self' https://app.stepped.ai 'sha256-HlAiISfjqhgIiTh24Wt2L3bd5wG1TYbHlnpS0PMuIA8='; connect-src 'self' https://app.stepped.ai wss://app.stepped.ai; frame-src 'self' https://app.stepped.ai; frame-ancestors 'none'; base-uri 'self'
```

The `app.stepped.ai` origin and the script hash exist for the embedded Stept assistant (chat widget and product tours) loaded by the SPA and by the login/setup/register pages. Everything else is locked to the own origin; `frame-ancestors 'none'` prevents embedding the UI in another site.

The embedded documentation under `/docs/` uses a separate policy, because Starlight needs inline bootstrap scripts and its search runs WebAssembly:

```text
default-src 'self'; script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'
```

If your proxy adds its own security headers, make sure it does not duplicate or contradict these (two `Content-Security-Policy` headers are combined restrictively by browsers).

## Cookies

| Cookie | Attributes | Lifetime |
|---|---|---|
| `np_session` | `Path=/`, `HttpOnly`, `SameSite=Lax`, `Secure` iff the request is HTTPS | 12 h; 30 days with "remember me" (`Max-Age` = TTL) |
| `np_oidc_state`, `np_oidc_verifier` | `Path=/auth`, `HttpOnly`, `SameSite=Lax`, `Secure` iff HTTPS | 600 s |

Behind a proxy without `trustProxy`, the `Secure` flag is missing and HSTS is not sent even though users connect over HTTPS — the usual symptom of a forgotten `trustProxy: true`. Session-cookie API requests with `Sec-Fetch-Site: cross-site` are rejected (403 `np:auth/csrf`); there is no CORS support, so browser calls must come from the same origin. Details in [Authentication](/docs/administration/authentication/) and [API overview](/docs/reference/api-overview/).

## Timeouts a proxy should respect

The server itself uses `ReadHeaderTimeout` 10 s, `ReadTimeout` 60 s, `IdleTimeout` 120 s and a 30 s response deadline for ordinary requests; the streaming paths `/api/v1/stream`, `/api/v1/events:export`, `/api/v1/ai/chat`, `/mcp` and `/mcp/*` have **no** deadline and can stay open indefinitely. Configure proxy read timeouts accordingly (see the nginx example) and keep response buffering off for those paths. The full list of constants is in [Configuration → Not configurable](/docs/administration/configuration/#not-configurable).

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `northplaned: serve: no TLS configured on a non-loopback listener …` | You set `listen` to a non-loopback address without a certificate pair. Add `tls.certFile`/`tls.keyFile`, set `trustProxy: true` behind a proxy, or (dev only) `tls.insecure: true`. |
| `TLS cert load failed, refusing to start insecure: …` | Unreadable or mismatched PEM files. Check paths, permissions of the `northplane` user, and that cert and key belong together. |
| `config invalid: tls.certFile set without tls.keyFile` | Both keys of the pair are required. |
| Users are logged out after a browser restart / cookies lack `Secure`, no HSTS header | `trustProxy` is `false` behind a TLS-terminating proxy, or the proxy does not send `X-Forwarded-Proto: https`. |
| SSO redirect goes to `http://…` or the wrong host | `baseUrl` is unset or wrong; it must be the public `https://` URL. |
| Live updates stall behind the proxy | Proxy buffering or idle timeout on `/api/v1/stream`; disable buffering and raise read timeouts. |
| Bundle upload returns 413 from the proxy | Raise the proxy body limit to at least 8 MiB (`client_max_body_size 9m` in nginx). |
| Audit log shows the proxy's IP | Expected — `X-Forwarded-For` is not evaluated. |
