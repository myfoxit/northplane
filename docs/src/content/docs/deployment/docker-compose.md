---
title: Docker Compose (standalone box)
description: The standalone production recipe — deploy/docker-compose.yml with a bundled Caddy for TLS, the .env knobs, certificate modes, health probing and day-2 operations.
sidebar:
  order: 2
---

The standalone recipe runs Northplane and a TLS-terminating Caddy on one host with Docker Compose. It is the stack the Deploy workflow used to ship to the standalone box np-02 (the `deploy-hetzner` job was removed with the box, see [CI/CD](/docs/deployment/ci-cd/)), and it is fully usable by hand: copy `.env.example` to `.env`, edit, `docker compose up -d`.

Two compose files exist in the repository — pick the one that matches your situation:

| File | Purpose | TLS | Secret key | Config |
|---|---|---|---|---|
| `docker-compose.yml` (repository root) + `caddy/Caddyfile` | local trial or a simple production box | Caddy: internal cert for `localhost`, Let's Encrypt when `DOMAIN` is set | auto-generated inside the data volume | `DOMAIN` from the shell |
| `deploy/docker-compose.yml` + `deploy/Caddyfile` + `.env` | the pipeline-managed standalone box | Caddy: Let's Encrypt for `DOMAIN` **and** an internal cert on the bare `SERVER_IP` | persistent `./secret.key` bind-mounted read-only | `.env` (rendered by CI or copied from `.env.example`) |

This page documents the `deploy/` variant in full and the root file briefly at the end. The edge-proxied variant without Caddy is on the [Proxmox VM](/docs/deployment/proxmox-vm/) page.

## Files

```text
deploy/
  docker-compose.yml      # northplane + caddy, env_file .env, secret.key mount
  Caddyfile               # {$DOMAIN} → Let's Encrypt, https://{$SERVER_IP} → tls internal
  .env.example            # every knob, documented — copy to .env for manual use
  docker-compose.vm.yml   # edge-proxied variant (no caddy) — see the Proxmox VM page
  provision-server.sh     # one-time host provisioning — see Provisioning
```

On a provisioned host the compose project lives in `/opt/northplane` (owned by the `deploy` user, mode 0750) as `docker-compose.yml`, `Caddyfile`, `.env`, `.env.previous` and `secret.key`.

## The compose file

```yaml title="deploy/docker-compose.yml"
# Northplane — production stack (Docker Compose + Caddy TLS).
#
# This file and .env are MANAGED BY THE DEPLOY PIPELINE
# (.github/workflows/deploy.yml). Do not hand-edit on the server: every
# deploy overwrites this file and re-renders .env from GitHub repo
# Variables/Secrets. Change configuration there (see deploy/README.md).
#
# Caddy terminates TLS on :80/:443 — Let's Encrypt for $DOMAIN once its DNS
# A-record points here, plus an internal self-signed cert on the bare IP so
# the box is reachable before DNS is live — and reverse-proxies to
# northplaned on :8443 inside the compose network.

name: northplane

services:
  northplane:
    image: ${NORTHPLANE_IMAGE:-ghcr.io/myfoxit/northplane:latest}
    restart: unless-stopped
    # .env carries the runtime config (base URL, the demo/real switch, the
    # per-mode data dir, the break-glass admin). Rendered by the pipeline.
    env_file: [.env]
    environment:
      NORTHPLANE_LISTEN: ":8443"
      NORTHPLANE_TRUST_PROXY: "true" # Caddy terminates TLS + sets X-Forwarded-*
      NORTHPLANE_SECRET_KEY_FILE: "/etc/northplane/secret.key"
    volumes:
      - northplane-data:/var/lib/northplane
      # Persistent AES-256 master key (secrets-at-rest). Provisioned once by
      # deploy/provision-server.sh; survives image swaps and mode flips.
      - ./secret.key:/etc/northplane/secret.key:ro
    expose:
      - "8443" # compose network only; Caddy publishes 80/443
    # distroless image has no shell — Caddy health-probes it instead.

  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    depends_on:
      - northplane
    ports:
      - "80:80"
      - "443:443"
    environment:
      DOMAIN: "${DOMAIN:-localhost}"
      SERVER_IP: "${SERVER_IP:-127.0.0.1}"
      ACME_EMAIL: "${ACME_EMAIL:-}"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy-data:/data # ACME account + issued certificates (persist!)
      - caddy-config:/config
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://northplane:8443/healthz"]
      interval: 30s
      timeout: 5s
      retries: 3

volumes:
  northplane-data: # app data: SQLite + TSDB, isolated per mode by NORTHPLANE_DATA_DIR
  caddy-data:
  caddy-config:
```

The project name is fixed to `northplane`, so the container is `northplane-northplane-1` and the data volume is `northplane_northplane-data` regardless of the directory name — the Deploy workflow relies on both names.

### Services, volumes and mounts

| Element | What it is | Notes |
|---|---|---|
| service `northplane` | the app container, `${NORTHPLANE_IMAGE}` (default `ghcr.io/myfoxit/northplane:latest`), `restart: unless-stopped` | listens on `:8443` inside the compose network only (`expose`, no `ports`); `NORTHPLANE_TRUST_PROXY=true` because Caddy terminates TLS and sets `X-Forwarded-Proto` |
| service `caddy` | `caddy:2-alpine`, publishes 80 and 443, `depends_on: northplane` | its `healthcheck` probes the **app** (`wget -qO- http://northplane:8443/healthz`) every 30 s, 5 s timeout, 3 retries — the app image has no shell, so this is where the app's health becomes visible in `docker compose ps` |
| volume `northplane-data` → `/var/lib/northplane` | SQLite `core.db`, event segments, `tsdb/`, artifacts | both mode directories (`demo/`, `real/`) live inside it |
| bind mount `./secret.key` → `/etc/northplane/secret.key:ro` | the AES-256 master key | must be readable by uid 65532 (the provisioner sets owner `65532:65532`, mode 0600). A missing source file makes Docker create a **directory** at that path — the server then logs `configured secretKeyFile unusable — falling back to the data directory`, and secrets encrypted with the fallback key are lost if you later fix the mount |
| bind mount `./Caddyfile` → `/etc/caddy/Caddyfile:ro` | the Caddy site config | |
| volume `caddy-data` → `/data` | ACME account and issued certificates | keep it — losing it means re-issuing certificates (Let's Encrypt rate limits apply) |
| volume `caddy-config` → `/config` | Caddy's autosaved config | |

## `.env` reference

`.env` is read by the `northplane` service (`env_file`) and by compose variable substitution (`${NORTHPLANE_IMAGE}`, `${DOMAIN}`, `${SERVER_IP}`, `${ACME_EMAIL}`). Every knob from `deploy/.env.example`:

| Key | Example value | Meaning |
|---|---|---|
| `NORTHPLANE_IMAGE` | `ghcr.io/myfoxit/northplane:latest` | image to run; the pipeline pins the immutable `main-<sha12>` tag, `latest` is fine for manual use |
| `DOMAIN` | `doktrace.com` | hostname Caddy obtains a Let's Encrypt certificate for (needs a DNS A record pointing here); `localhost` for a pure local trial |
| `SERVER_IP` | the box's public IPv4 | bare-IP fallback endpoint with an internal certificate, reachable before DNS is live |
| `ACME_EMAIL` | `admin@doktrace.com` | ACME account e-mail (Let's Encrypt notifications) |
| `NORTHPLANE_BASE_URL` | `https://doktrace.com` | external base URL used for links, ack URLs and OIDC redirects |
| `NORTHPLANE_DEMO` | `true` / `false` | the demo/real switch — see [Demo mode](/docs/getting-started/demo-mode/) |
| `NORTHPLANE_DATA_DIR` | `/var/lib/northplane/demo` or `/var/lib/northplane/real` | per-mode data directory inside the volume; keep it in step with `NORTHPLANE_DEMO` |
| `NP_DEFAULT_ADMIN_EMAIL` | `admin@doktrace.com` | break-glass admin e-mail (seeded when absent) |
| `NP_DEFAULT_ADMIN_PASSWORD` | `<secret>` | its password; **leave empty to disable seeding and use the interactive `/setup` page instead** |

Set by the compose file itself (not in `.env`): `NORTHPLANE_LISTEN=:8443`, `NORTHPLANE_TRUST_PROXY=true`, `NORTHPLANE_SECRET_KEY_FILE=/etc/northplane/secret.key`. Any other `NORTHPLANE_*` variable from the [configuration reference](/docs/administration/configuration/) (for example `NORTHPLANE_STORAGE_DSN`, `NORTHPLANE_LOG_LEVEL`, `NORTHPLANE_ALLOW_SIGNUP`) can be added to `.env` — it is passed through unchanged.

:::caution[.env.example defaults are for a showcase]
The example ships with `NORTHPLANE_DEMO=true`, the matching `/demo` data directory and `NP_DEFAULT_ADMIN_PASSWORD=change-me`. For a real instance set `NORTHPLANE_DEMO=false`, `NORTHPLANE_DATA_DIR=/var/lib/northplane/real`, a strong admin password (or an empty one to use `/setup`), and keep `.env` at mode 0600 — the admin password is plain text in it and visible in `docker inspect`.
:::

For comparison, the removed `deploy-hetzner` job rendered this `.env` for the standalone box: `NORTHPLANE_IMAGE=ghcr.io/myfoxit/northplane:main-<sha12>`, `DOMAIN=localhost`, `SERVER_IP=<HETZNER_HOST>`, `ACME_EMAIL=admin@doktrace.com`, `NORTHPLANE_BASE_URL=https://<HETZNER_HOST>`, `NORTHPLANE_DEMO=false`, `NORTHPLANE_DATA_DIR=/var/lib/northplane/real`, `NP_DEFAULT_ADMIN_EMAIL=root@localhost`, `NP_DEFAULT_ADMIN_PASSWORD=<secret HETZNER_ADMIN_PASSWORD>` — that is, a real-data instance on a bare IP; a domain is added later by changing `DOMAIN` in the workflow.

## Caddy and certificates

```text title="deploy/Caddyfile"
# Northplane reverse proxy / TLS termination (managed by the deploy pipeline).
#
# Two independent site blocks share :443 by SNI:
#   • {$DOMAIN}      → automatic Let's Encrypt, the moment a DNS A-record for
#                      the domain resolves to this host. Until then Caddy just
#                      retries issuance in the background — harmless.
#   • https://{$SERVER_IP} → internal (self-signed) cert, so the stack is
#                      reachable at https://<ip> immediately, before DNS, and
#                      as a stable internal endpoint. Browsers show a one-time
#                      warning; that's expected for an IP cert.
#
# default_sni is essential: clients hitting the bare IP send NO TLS SNI (an
# IP literal is not allowed as a server name), so without this Caddy can't
# pick a certificate and aborts the handshake. Pinning the default SNI to the
# IP makes SNI-less connections serve the internal IP cert. Real visitors to
# {$DOMAIN} always send SNI, so they're unaffected.

{
	# ACME account email for Let's Encrypt expiry/issue notifications.
	email {$ACME_EMAIL}
	default_sni {$SERVER_IP}
}

{$DOMAIN} {
	reverse_proxy northplane:8443
}

https://{$SERVER_IP} {
	tls internal
	reverse_proxy northplane:8443
}
```

Two certificate modes run side by side:

| Mode | Site block | Certificate | When it works |
|---|---|---|---|
| Domain | `{$DOMAIN}` | automatic Let's Encrypt (Caddy's automatic HTTPS; ports 80 and 443 must be reachable from the internet, `ACME_EMAIL` is the account contact) | as soon as the A record resolves to this host; until then Caddy retries issuance in the background. With `DOMAIN=localhost` Caddy uses its internal CA instead |
| Bare IP | `https://{$SERVER_IP}` with `tls internal` | self-signed by Caddy's internal CA; served to SNI-less clients because of `default_sni {$SERVER_IP}` | immediately — the box is reachable at `https://<ip>` before DNS exists; browsers show a one-time warning |

Caddy forwards `X-Forwarded-Proto: https` to the app, which is why `NORTHPLANE_TRUST_PROXY=true` is set: the app then marks cookies `Secure` and sends HSTS. Northplane does not use `X-Forwarded-For` for anything — see [TLS and reverse proxies](/docs/administration/tls-and-proxy/).

## Health probing through Caddy

The app image is distroless and has **no shell**, so neither `docker compose exec northplane …` nor a compose `healthcheck` on the app container can run a probe. Two patterns work:

```bash
# from the host, through Caddy's container (what the Deploy workflow does)
cd /opt/northplane
docker compose exec -T caddy wget -qO- http://northplane:8443/healthz   # → ok

# from anywhere, through TLS
curl -fsS https://<DOMAIN-or-IP>/healthz      # -k for the internal IP cert
curl -fsS https://<DOMAIN-or-IP>/readyz       # {"ready":true,"subsystems":[…]}
```

`docker compose ps` shows the `caddy` service as `healthy`/`unhealthy` — that status reflects the **app's** `/healthz`, because the health check targets `http://northplane:8443/healthz`. The endpoints are described under [Observability](/docs/administration/observability/).

## Running the stack by hand

1. Provision the host — Docker CE, the `deploy` user, `/opt/northplane` and `secret.key` — with `deploy/provision-server.sh` (see [Provisioning](/docs/deployment/provisioning/)), or create the equivalent by hand: a directory for the project and a `secret.key` containing 64 hex characters (`openssl rand -hex 32 > secret.key`), owned by `65532:65532`, mode 0600.
2. Copy the files and create `.env`:
   ```bash
   cd /opt/northplane
   cp /path/to/repo/deploy/docker-compose.yml docker-compose.yml
   cp /path/to/repo/deploy/Caddyfile Caddyfile
   cp /path/to/repo/deploy/.env.example .env && chmod 600 .env
   # edit .env: DOMAIN, SERVER_IP, ACME_EMAIL, NORTHPLANE_BASE_URL,
   #            NORTHPLANE_DEMO + NORTHPLANE_DATA_DIR, admin e-mail/password
   ```
4. Start and watch:
   ```bash
   docker compose up -d
   docker compose logs -f northplane   # "northplane: listening" — and the seeded admin line
   docker compose logs -f caddy        # certificate issuance
   ```
5. Open `https://<SERVER_IP>` (accept the internal certificate) or `https://<DOMAIN>`. Log in as the break-glass admin; if you left `NP_DEFAULT_ADMIN_PASSWORD` empty, `/setup` is open and creates the first admin instead.
6. Continue with [First steps](/docs/getting-started/first-steps/) — and read the [security checklist](/docs/administration/security/) before exposing the box.

## Day-2 operations

```bash
cd /opt/northplane
docker compose ps                          # caddy healthy == app /healthz ok
docker compose logs -f northplane          # JSON logs on stderr
docker compose logs -f caddy

# upgrade to a newer image
docker compose pull && docker compose up -d --remove-orphans

# roll back to a known-good build: pin the immutable tag in .env …
sed -i 's#^NORTHPLANE_IMAGE=.*#NORTHPLANE_IMAGE=ghcr.io/myfoxit/northplane:main-<oldsha12>#' .env
docker compose up -d
# … or restore the previous rendering kept by the pipeline
mv .env.previous .env && docker compose up -d
```

Back up `/opt/northplane/secret.key` and the `northplane_northplane-data` volume (and `northplane_caddy-data` to keep certificates). The server has no periodic backup loop; see [Operations → Backups](/docs/deployment/operations/#backups) and [Storage](/docs/administration/storage/).

:::note[Pipeline-managed files]
When the Deploy workflow targets the host, every run overwrites `docker-compose.yml`, `Caddyfile` and `.env` (keeping the previous `.env` as `.env.previous`) and runs `docker compose pull` / `up -d`. Make durable changes in the GitHub variables and secrets, not on the server. The only standalone box that was ever wired this way, `np-02`, is gone (see [Environments](/docs/deployment/environments/)); the recipe remains the canonical standalone install.
:::

## The root `docker-compose.yml` (trial stack)

The repository-root compose file is the same shape without `.env` and without the secret-key mount:

```text title="caddy/Caddyfile"
# DOMAIN=localhost (default) → Caddy issues an internal self-signed cert.
# DOMAIN=monitoring.example.net (public DNS → this host) → automatic Let's Encrypt.
{$DOMAIN:localhost} {
	reverse_proxy northplane:8443
}
```

```bash
docker compose up -d                                       # https://localhost (self-signed)
DOMAIN=monitoring.example.net docker compose up -d         # Let's Encrypt
```

Differences to the `deploy/` stack: the image is fixed to `ghcr.io/myfoxit/northplane:latest` (uncomment `build: .` to build from source), `NORTHPLANE_BASE_URL` is derived as `https://${DOMAIN:-localhost}`, the secret key is auto-generated inside the data volume (`/var/lib/northplane/secret.key`), there is no bare-IP site, and an optional commented `db: postgres:16` service plus `NORTHPLANE_STORAGE_DSN` line show how to switch to PostgreSQL. It sets `NP_DEFAULT_ADMIN_DISABLED: "1"`, so the first visit lands on `/setup` to create the admin account; swap that for `NP_DEFAULT_ADMIN_EMAIL`/`NP_DEFAULT_ADMIN_PASSWORD` for unattended installs. Details in [Installation](/docs/getting-started/installation/).
