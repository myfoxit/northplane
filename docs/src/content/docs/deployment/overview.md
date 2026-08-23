---
title: Deployment overview
description: The supported ways to run Northplane — from make dev to the CI-driven production stack — and what each variant needs for TLS, ports, data and the secret key.
sidebar:
  order: 1
---

Northplane ships as one static binary, `northplaned`, with the web UI and this documentation embedded. Every deployment variant below runs that same binary. They differ only in **who terminates TLS**, **where the data directory lives**, and **who performs the rollout**.

## What you are deploying

| Item | Fact |
|---|---|
| Binary | `northplaned` (server). The release tarball also contains `np` (CLI) and `np-agent`; the container image contains `northplaned` and `np` only — the agent is installed on the monitored hosts, not in the image. |
| Listener | `listen`, default `127.0.0.1:8443` (loopback on purpose). The image sets `NORTHPLANE_LISTEN=:8443` so that port mapping works. |
| TLS policy | Plaintext on a non-loopback listener **refuses to start** unless you set `tls.certFile` + `tls.keyFile`, or `trustProxy: true` (behind a TLS-terminating proxy), or `tls.insecure: true` (dev only). There is no built-in ACME client — production TLS comes from Caddy. See [TLS and reverse proxies](/docs/administration/tls-and-proxy/). |
| Data directory | `dataDir` (`NORTHPLANE_DATA_DIR`): SQLite `core.db`, monthly `events-YYYYMM.db` segments, `tsdb/`, `artifacts/`, and the fallback `secret.key`. Default `/var/lib/northplane` when running as root; the image declares `VOLUME /var/lib/northplane`. See [Storage](/docs/administration/storage/). |
| Secret key | `secretKeyFile` (`NORTHPLANE_SECRET_KEY_FILE`): a 32-byte hex master key for secrets at rest (AES-256-GCM). Generated on first start if the file is missing; the production stacks bind-mount a persistent `./secret.key` owned by uid 65532. Lose it and every stored secret is unreadable. See [Secrets](/docs/administration/secrets/). |
| Admin bootstrap | On **every** start the server seeds a break-glass admin from `NP_DEFAULT_ADMIN_EMAIL` / `NP_DEFAULT_ADMIN_PASSWORD` (random password logged once when the variable is unset) unless `NP_DEFAULT_ADMIN_DISABLED` is set or the password is set-but-empty. Because that happens before the listener opens, `/setup` is closed on a default install; use the interactive `/setup` flow only with the seeding disabled. See [Authentication](/docs/administration/authentication/). |
| Image | `ghcr.io/myfoxit/northplane` — public, no login needed. Base `gcr.io/distroless/static-debian12:nonroot` (uid 65532), no shell, `EXPOSE 8443`, `ENTRYPOINT northplaned`, `CMD serve`. Tags: `main-<sha12>` per green build of `main`, `latest`, and semver tags from releases. |
| Configuration | `config.yaml` (`-config`, default `/etc/northplane/config.yaml` as root) or `NORTHPLANE_*` environment variables; env wins over file. The container stacks use env only. See [Configuration](/docs/administration/configuration/). |

## Deployment variants

| Variant | What runs | TLS | Ports on the host | Data and key | Rollout | Details |
|---|---|---|---|---|---|---|
| Developer loop (`make dev`) | Vite HMR on `:5173` + `northplaned` on `127.0.0.1:8443`, demo data seeded | none (loopback) | 5173, 8443 — loopback only | `.dev/data`, `.dev/secret.key` | auto rebuild on source change | [Development setup](/docs/development/setup/) |
| Single binary + systemd | `northplaned serve -config /etc/northplane/config.yaml` | your cert pair in `config.yaml`, or a reverse proxy + `trustProxy` | whatever `listen` says (`:8443`) | `/var/lib/northplane`, `/etc/northplane/secret.key` | tarball or `install.sh`, `northplaned init`, `systemctl` | [Installation](/docs/getting-started/installation/) |
| Docker, single container | `ghcr.io/myfoxit/northplane` | mount a cert pair, or `NORTHPLANE_TRUST_PROXY=true` behind your own proxy, or `NORTHPLANE_TLS_INSECURE=true` (dev) | 8443 | named volume on `/var/lib/northplane` | `docker pull` + recreate | [Quickstart](/docs/getting-started/quickstart/) |
| Compose + bundled Caddy | `northplane` + `caddy:2-alpine` — root `docker-compose.yml` for trials, `deploy/docker-compose.yml` + `.env` + `secret.key` for a production box | Caddy: Let's Encrypt for `DOMAIN`, internal cert for `localhost` and the bare IP | 80, 443 | volumes `northplane-data`, `caddy-data`, `caddy-config`; `./secret.key` | `docker compose pull && docker compose up -d`, or the Deploy workflow | [Docker Compose](/docs/deployment/docker-compose/) |
| Edge-proxied VM behind a central Caddy | `northplane` only (`deploy/docker-compose.vm.yml`), `8443` published on a private bridge; a shared Caddy LXC terminates TLS for all VMs | central Caddy (Let's Encrypt, HTTP-01) | 8443 on the VM; 80/443 on the edge | volume `northplane-data`, `./secret.key` | Deploy workflow over SSH (DNAT port) | [Proxmox VM](/docs/deployment/proxmox-vm/) |
| CI-driven production | the two Compose variants above, rolled forward by GitHub Actions on every green build of `main` | as above | as above | as above | `.github/workflows/deploy.yml` | [CI/CD](/docs/deployment/ci-cd/) |

Federation is orthogonal to all of these: an **edge** instance at a customer site is any of the variants above with `federation.mode: edge` in its config, dialling out to the main instance. See [Federation](/docs/concepts/federation/).

## Decision guide

- **Trying it on a laptop** — `docker compose up -d` with the root `docker-compose.yml` and open `https://localhost` (self-signed), or `northplaned serve --demo` on loopback. [Quickstart](/docs/getting-started/quickstart/).
- **One on-prem server you own DNS for** — single binary behind your existing reverse proxy (`trustProxy: true`), or the Compose stack with bundled Caddy (`DOMAIN=monitoring.example.net` → Let's Encrypt). Both keep everything on one box.
- **A server that must be reachable before DNS exists** — `deploy/docker-compose.yml` + `deploy/Caddyfile`: Caddy serves `https://<ip>` with an internal certificate immediately and picks up Let's Encrypt for `DOMAIN` as soon as the record resolves.
- **Several SaaS-style VMs on one hypervisor** — one central Caddy container per box (TLS, all domains) and the edge-proxied Compose file per VM. This is the production layout of `doktrace.com`.
- **Many customer sites** — one main instance plus an edge instance per site; the edge pulls its bundle and reports status, no inbound firewall rule at the customer. [Federation](/docs/concepts/federation/), [Tenants and sites](/docs/administration/tenants-and-sites/).
- **Rollouts from git** — any of the Compose variants plus the Deploy workflow; needs a host provisioned with `deploy/provision-server.sh`. [CI/CD](/docs/deployment/ci-cd/), [Provisioning](/docs/deployment/provisioning/).

## What every production deployment needs

1. **A TLS decision.** Either Caddy in front (bundled or central) with `NORTHPLANE_TRUST_PROXY=true`, or a certificate pair in `config.yaml`. Never `tls.insecure` outside dev. Only enable `trustProxy` when the proxy strips inbound `X-Forwarded-*` headers — Northplane honours `X-Forwarded-Proto` for Secure cookies and HSTS.
2. **Persistent storage.** A volume or directory for `dataDir`, and a persistent `secret.key`. The production stacks keep `secret.key` next to the compose file (`/opt/northplane/secret.key`, 0600, uid 65532) and bind-mount it read-only.
3. **The public URL.** `NORTHPLANE_BASE_URL` (= `baseUrl`) is used for links in notifications, ack links, the OIDC redirect and the Web Push VAPID subject. Set it to the URL users will open.
4. **An admin.** Either the break-glass admin (`NP_DEFAULT_ADMIN_EMAIL` + `NP_DEFAULT_ADMIN_PASSWORD`) or `/setup` with seeding disabled. Change the seeded password after the first login.
5. **Reachable ports** — only the ones you use (table below). The app container itself publishes nothing but 8443; everything else is opt-in.
6. **Backups.** There is no periodic backup loop in the server (`backup.interval` is parsed but unused); back up `secret.key` and the data volume yourself, or run `northplaned backup` on demand. See the [operations runbook](/docs/deployment/operations/).
7. **The image** — `docker pull ghcr.io/myfoxit/northplane:latest` (public), or build it yourself with `docker build --build-arg VERSION=… .` / `make docker`.

## The demo / real switch

One switch, `NORTHPLANE_DEMO` (config key `demo`), decides whether the showcase environment is seeded at startup:

- `true` → idempotent `demo-*` hosts, services, alerts, an escalation chain, an on-call schedule, a BPI tree with an SLA, a dashboard, a scheduled report and two demo users.
- `false` → nothing is seeded; log in as the break-glass admin (or via `/setup`) and add real hosts.

The production stacks make the switch safe with **separate data directories inside the same volume** — `NORTHPLANE_DATA_DIR=/var/lib/northplane/demo` for demo mode and `/var/lib/northplane/real` for real mode — so flipping never mixes datasets. As a second guard the server refuses to seed demo data on top of a database that already holds real (non-demo) hosts and logs a warning instead. In CI the switch is the repository variable `NORTHPLANE_DEMO` plus a per-run override on manual dispatch. Details: [Demo mode](/docs/getting-started/demo-mode/) and [switching between demo and real data](/docs/deployment/operations/#switching-between-demo-and-real-data).

## Ports

| Port | Protocol | Direction | Used by | When it is open |
|---|---|---|---|---|
| 8443 | TCP | inbound | `northplaned` listener (`listen`; `:8443` in the image) | always; loopback-only by default on a bare install |
| 80, 443 | TCP | inbound | Caddy — bundled (`caddy:2-alpine`) or the central LXC; 80 also serves the ACME HTTP-01 challenge | Compose and edge variants |
| 9162 | UDP | inbound | SNMP trap receiver of an `snmp-trap` event source (`listen: udp://:9162`); `serve --demo-traps` defaults to the same | only when such a source exists and the port is published |
| 2023 | TCP | inbound | ESPA 4.4.4 event source (`listen: tcp://:2023`) | when configured and published |
| 8123 | TCP | inbound | ESPA-X event source (`listen: tcp://:8123`) | when configured and published |
| 4573 | TCP | inbound | FastAGI listener for `asterisk-inbound` sources (`listen: tcp://:4573`) | when configured and published |
| 1883 / 8883 | TCP | outbound | MQTT channel and `mqtt` event source to your broker | outbound only |
| 5693 | TCP | outbound (server → agent) | the builtin `agent` check polling an np-agent in listener mode | optional |
| 25 / 465 / 587, 443 | TCP | outbound | SMTP, Twilio, ntfy, Slack/Teams, webhooks, ticket systems | outbound only |
| 2201 | TCP | inbound (hypervisor) | DNAT to the VM's sshd for the CI deploy user | `doktrace.com` topology only |
| 8006 | TCP | inbound (hypervisor) | Proxmox web UI, IP-allowlisted | `doktrace.com` topology only |

:::note[Published ports on the production VM]
The production compose file (`deploy/docker-compose.vm.yml`) publishes `8443:8443` plus `9162:9162/udp`, `2023:2023`, `8123:8123` and `4573:4573` — on the VM's private bridge address only; the hypervisor forwards nothing but 80/443 (to Caddy) and the CI SSH port.
:::

## Where to go next

- [Docker Compose (standalone box)](/docs/deployment/docker-compose/) — the recipe with bundled Caddy.
- [Proxmox VM behind a central Caddy](/docs/deployment/proxmox-vm/) — the live `doktrace.com` topology.
- [CI/CD](/docs/deployment/ci-cd/) — how a merge to `main` becomes a deploy.
- [Provisioning](/docs/deployment/provisioning/) — preparing a new host.
- [Operations runbook](/docs/deployment/operations/) and [Environments](/docs/deployment/environments/).
- [Upgrades](/docs/administration/upgrades/) and the [security checklist](/docs/administration/security/).
