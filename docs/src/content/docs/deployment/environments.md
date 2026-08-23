---
title: Environments
description: Inventory of every Northplane environment with the state verified on 2026-08-23 — np-01 doktrace.com (production), np-02 (decommissioned), np-staging VM104 (federation edge), the lab guests, DNS/Cloudflare, access, and open items.
sidebar:
  order: 7
---

:::note[Verified 2026-08-23]
The state below was checked live on 2026-08-23 by SSH/HTTP against the systems and with `gh` against the repository `myfoxit/northplane`. Where a detail comes from the lab notes of 2026-08-19/21 instead, it is marked as such. Re-verify before acting on anything that looks stale.
:::

## Inventory

| Environment | Role | Where | URL | Data mode | Running build | State (2026-08-23) |
|---|---|---|---|---|---|---|
| **np-01** | production | VM101 `saas1` (10.10.10.11) on Proxmox `51.83.96.40`, behind CT100 Caddy + Cloudflare | `https://doktrace.com` | **real** (since 2026-08-20) | `ghcr.io/myfoxit/northplane:main-daa6dc518a2b` (= `main` HEAD) | healthy; deploy job green |
| **np-02** | second production instance (standalone, bundled Caddy) | Hetzner `91.98.92.10` | `https://91.98.92.10` | real (by workflow) | — | **decommissioned / unreachable** — box reclaimed, IP reassigned; `deploy-hetzner` job fails on every run; action needed |
| **np-staging** | federation **edge** of production (tenant MyFoxIT, site `vm104-edge`); lab mirror | VM104 `saas4` (10.10.10.14) on the same Proxmox host | `https://10.10.10.14:8443` (private bridge; self-signed) | real (lab data) | not re-verified on 2026-08-23 | up; edge connected to prod (lab notes 2026-08-21) |
| CT110 `targets` | lab check targets | 10.10.10.20 | — | — | — | up |
| VM102 `saas2` "netlab" | network lab (containerlab, 2× Cisco IOSv, SR Linux) + `np-agent` → prod | 10.10.10.12 | — | — | — | up |
| VM103 `saas3` "alarmlab" | alarm sinks (Mosquitto, Mailpit, GreenMail, ntfy, echo sink) + `np-agent` → prod | 10.10.10.13 | — | — | — | up |
| CT100 `caddy` | TLS edge for all slots | 10.10.10.10 | — | — | — | up; one site (`doktrace.com`) |
| VM9000 / VM9001 | templates (`debian13-base`, `rocky9-base`) | — | — | — | — | stopped |
| developer loop | `make dev` on a workstation | local | `http://localhost:5173` (UI), `http://127.0.0.1:8443` (API) | demo by default | source | — |

## np-01 — doktrace.com (production)

| Item | Verified value |
|---|---|
| Host | VM101 `saas1`, Rocky Linux 9.8, 4 vCPU / 8 GB, `10.10.10.11` on the hypervisor `ns3147660` (Proxmox VE 9.2.11) |
| Stack | `deploy/docker-compose.vm.yml` (edge-proxied, no bundled Caddy) in `/opt/northplane`; container `northplane-northplane-1`; volume `northplane_northplane-data`; `secret.key` 65 bytes, uid 65532, 0600 |
| Image | `ghcr.io/myfoxit/northplane:main-daa6dc518a2b`, started `2026-08-23T08:50:57Z`; `/api/v1/system/info` → `version: main-daa6dc518a2b`, `storage: sqlite`, `goVersion: go1.25.14`, `aiEnabled: false` |
| Health | `/healthz` → `ok`; `/readyz` → `ready: true` (storage sqlite, eventbus, scheduler all ok) |
| Data mode | **real** — `NORTHPLANE_DEMO=false`, `NORTHPLANE_DATA_DIR=/var/lib/northplane/real`, switched on 2026-08-20 (repo variable `NORTHPLANE_DEMO=false` set the same day); `/var/lib/northplane/demo` still present in the volume |
| Env keys | `NORTHPLANE_IMAGE`, `NORTHPLANE_BASE_URL=https://doktrace.com`, `NORTHPLANE_DEMO=false`, `NORTHPLANE_ALLOW_SIGNUP=true`, `NORTHPLANE_DATA_DIR`, `NP_DEFAULT_ADMIN_EMAIL=admin@doktrace.com`, `NP_DEFAULT_ADMIN_PASSWORD` (secret); compose-set `NORTHPLANE_LISTEN=:8443`, `NORTHPLANE_TRUST_PROXY=true`, `NORTHPLANE_SECRET_KEY_FILE=/etc/northplane/secret.key` |
| Signup | **on** — `/register` is publicly reachable; self-registered accounts get the `viewer` role |
| Break-glass admin | `admin@doktrace.com` (password = repo secret `NP_DEFAULT_ADMIN_PASSWORD`; not printed here) |
| Tenants | default tenant plus tenant **MyFoxIT** (own users and a `tenant-admin` role; site `vm104-edge` lives here) — see [Tenants and sites](/docs/administration/tenants-and-sites/) |
| TLS / ingress | Cloudflare-proxied A record → `51.83.96.40` → DNAT 443 → CT100 Caddy (`/etc/caddy/sites/saas1.caddy`, Let's Encrypt HTTP-01) → `10.10.10.11:8443` |
| Published ports | `8443` only (trap/ESPA/ESPA-X/FastAGI ports are not mapped — unmerged branch) |
| Agents reporting here | `np-prod` (VM101), `pve-host` (hypervisor), `netlab` (VM102), `alarmlab` (VM103) |
| Last deploy | Deploy run `32629242562`, 2026-08-23 08:48 UTC, for merge `daa6dc5`: `publish` and `deploy` succeeded, `deploy-hetzner` failed (np-02) |
| Docs | `/docs/` is served by every image built from the commit that introduced the embedded documentation onwards; the build verified at 08:50 UTC (`main-daa6dc518a2b`) predates it, the next Deploy run after the merge carries it ([CI/CD](/docs/deployment/ci-cd/)) |
| Backups | none scheduled (no `vzdump` job on the host, no periodic app backup) — [Operations → Backups](/docs/deployment/operations/#backups) |

Topology, access and the compose file: [Proxmox VM](/docs/deployment/proxmox-vm/). Day-2 handling: [Operations](/docs/deployment/operations/).

## np-02 — Hetzner standalone (decommissioned, unreachable)

| Item | Verified value |
|---|---|
| Address | `91.98.92.10` (repo variable `HETZNER_HOST`) |
| 2026-08-23 | TCP/22 times out; `https://91.98.92.10/` answers 404 with a parking page (earlier observation: a Yahoo parking page). The Hetzner box has been reclaimed and the IP reassigned to a stranger |
| Pipeline effect | `deploy-hetzner` fails at "Ship compose stack" (`ssh: connect to host 91.98.92.10 port 22: Connection timed out`) on **every** Deploy run — observed 2026-08-14, 2026-08-21, 2026-08-23. The `deploy` job for np-01 is independent and succeeds, so the red run does not mean production missed the rollout |
| Design | the standalone recipe: `deploy/docker-compose.yml` + `deploy/Caddyfile` (bundled Caddy, bare-IP internal certificate, Let's Encrypt once `DOMAIN` is set), `.env` rendered by CI with `DOMAIN=localhost`, `SERVER_IP=91.98.92.10`, `NORTHPLANE_BASE_URL=https://91.98.92.10`, `NORTHPLANE_DEMO=false`, `NORTHPLANE_DATA_DIR=/var/lib/northplane/real`, `NP_DEFAULT_ADMIN_EMAIL=root@localhost`; provisioned with `deploy/provision-server.sh` as root on Rocky 10 |
| Stale configuration | variables `HETZNER_HOST`, `HETZNER_KNOWN_HOSTS`; secrets `HETZNER_SSH_KEY`, `HETZNER_ADMIN_PASSWORD` — all refer to the lost box |
| Data | whatever lived in its `northplane-data` volume is gone with the box; no backup existed |

**Action needed:** either re-create the box and repoint the pipeline (new key pair, `provision-server.sh`, new `HETZNER_HOST`/`HETZNER_KNOWN_HOSTS`/`HETZNER_SSH_KEY`/`HETZNER_ADMIN_PASSWORD`) — the step-by-step is the [np-02 recreation checklist](/docs/deployment/provisioning/#np-02-recreation-checklist) — or remove the `deploy-hetzner` job and the four `HETZNER_*` entries so that Deploy runs are green again.

## np-staging — VM104 (federation edge of production)

| Item | Value (lab notes 2026-08-21 unless marked) |
|---|---|
| Host | VM104 `saas4`, `10.10.10.14`, Rocky 9 (guest verified 2026-08-23) |
| Role | second Northplane instance: real data, self-signed TLS, a local admin; **federation edge** of production for tenant MyFoxIT, site `vm104-edge` (role verified 2026-08-23) |
| Edge config | `federation: mode: edge`, `mainUrl: https://doktrace.com`, `site: vm104-edge`, `interval: 60s`, token minted on main with scope `sites:connect` (tenant-scoped) — in `/opt/northplane/config.yaml`, mounted read-only at `/etc/northplane/config.yaml`, owned by uid 65532 |
| What flows | the edge pulls the site bundle from main (ETag-conditional) and applies it locally (hosts `np-staging`, `lab-web`, passive agent services, an ntfy channel, a policy and a contact); it heartbeats status and counters back — `GET /api/v1/sites:overview` on main (with the tenant header) shows `connected` plus stats |
| Agent | its own `np-agent` pushes to the **local** edge instance with a token minted on the edge — not to production (verified 2026-08-23) |
| Ports | publishes `8443` plus `9162/udp`, `2023`, `8123`, `4573` (lab notes) |
| Caveats | its `mqtt-in` event source is disabled (two instances sharing an MQTT client id on one broker evict each other); mounted config files must be owned by uid 65532 |
| Reach it | `ssh -N -L 18443:10.10.10.14:8443 root@51.83.96.40` → `https://localhost:18443`; shell via `ssh -J root@51.83.96.40 rocky@10.10.10.14` |

The federation mechanics are described under [Federation](/docs/concepts/federation/) and [Tenants and sites](/docs/administration/tenants-and-sites/).

## Lab guests

Guest list verified 2026-08-23; service details from the lab notes (see the [operations appendix](/docs/deployment/operations/#appendix-the-proxmox-lab) for the full picture).

| Guest | Address | Purpose |
|---|---|---|
| CT110 `targets` | 10.10.10.20 | one live target per builtin check: nginx variants (ok / slow / 500), valid / expiring / expired certificates, `snmpd`, NRPE, BIND `lab.local`, chrony, an `np-agent` in listener mode |
| VM102 `saas2` "netlab" | 10.10.10.12 | containerlab with 2× Cisco IOSv (real OSPF link, SNMP polling + traps into prod), SR Linux, Alpine + snmpd; `np-agent` → prod |
| VM103 `saas3` "alarmlab" | 10.10.10.13 | Mosquitto, Mailpit, GreenMail, ntfy, echo sink `:9000` for Slack/Teams/ticket channels; ESPA drivers; `np-agent` → prod |
| VM104 `saas4` | 10.10.10.14 | `np-staging` — see above |
| VM9000 / VM9001 | — | `debian13-base` / `rocky9-base` templates, stopped |

## DNS and Cloudflare

- `doktrace.com` — Cloudflare-proxied A record → `51.83.96.40`. Cloudflare terminates the visitor's TLS and connects to the origin on 443; the origin (CT100 Caddy, Let's Encrypt certificate via HTTP-01) also answers direct connections (`curl --resolve doktrace.com:443:51.83.96.40 …`).
- Cloudflare blocks Python user agents (403 / error 1010) and replaces 5xx bodies with its own error page — script with a curl-like `User-Agent`, and read API error details from the origin.
- CT100's Caddyfile lists the Cloudflare IPv4/IPv6 ranges as `trusted_proxies`; Northplane only consumes `X-Forwarded-Proto` (`NORTHPLANE_TRUST_PROXY=true`), never `X-Forwarded-For`.
- `91.98.92.10` (np-02) has no DNS name and is no longer ours.
- No DNS name exists for the staging/lab guests; they are reached through the hypervisor.

## Who has access

| Path | Who / how |
|---|---|
| Hypervisor `51.83.96.40` | `root` by SSH key (the operator's key); Proxmox web UI on 8006 is IP-allowlisted in `cluster.fw` — use `ssh -N -L 8006:127.0.0.1:8006 root@51.83.96.40` → `https://localhost:8006`, realm `pam` |
| VMs (`saas1`–`saas4`) | user `rocky` with sudo, via the jump host: `ssh -J root@51.83.96.40 rocky@10.10.10.1X` (the VMs trust a different key than the hypervisor) |
| CT100 Caddy | no SSH key inside; `ssh root@51.83.96.40 -t 'pct enter 100'` |
| CI → VM101 | `deploy@51.83.96.40 -p 2201` (DNAT → `10.10.10.11:22`) with the `DEPLOY_SSH_KEY` secret; `deploy` is in the `docker` group (root-equivalent on that VM) |
| GitHub repository | private; whoever administers `myfoxit/northplane` controls variables/secrets and therefore production (`.env` is rendered from them). `NORTHPLANE_DEMO` is the only variable expected to change |
| GHCR image | private package; a PAT with `read:packages` pulls it; the pipeline uses its `GITHUB_TOKEN` |
| Application admin | break-glass `admin@doktrace.com` on np-01 (instance-wide admin); tenant MyFoxIT has its own users; `/register` creates `viewer` accounts for anyone |
| Agents | API tokens with scope `objects:write` in `/etc/northplane/agent.yaml` on each host (root-readable files) |

## Open items

1. **np-02 is gone** — repoint `deploy-hetzner` to a new box or remove it; the `HETZNER_*` variables/secrets are stale. Until then every Deploy run is red although production deploys fine. ([Provisioning](/docs/deployment/provisioning/#np-02-recreation-checklist))
2. **No backups of production** — no `vzdump` job on the Proxmox host, no periodic application backup; `secret.key` and the data volume are single copies. ([Operations → Backups](/docs/deployment/operations/#backups))
3. **Trap/ESPA/FastAGI ports not published on np-01** — `9162/udp`, `2023`, `8123`, `4573` exist only in an unmerged branch of `deploy/docker-compose.vm.yml`; event sources listening on them cannot be reached from the lab network.
4. **Confirm `/docs/` on doktrace.com after the next deploy** — the image verified on 2026-08-23 08:50 UTC predates the docs embedding; the first Deploy run after the docs merge serves it (check `https://doktrace.com/docs/`).
5. **Agent install one-liner 404s** — **Admin → Agents** shows `curl -fsSL https://raw.githubusercontent.com/myfoxit/northplane/main/install.sh | sh`, which fails for anonymous users because the repository is private (known issue AGENT-1). ([Roadmap and known issues](/docs/project/roadmap-and-known-issues/))
6. **Public signup is on** (`NORTHPLANE_ALLOW_SIGNUP=true`, hard-coded in the deploy workflow) — intentional for the showcase; revisit for a customer-facing instance. ([Security](/docs/administration/security/))
7. **Cloudflare is bypassable** — the origin answers direct connections; Cloudflare's WAF is not a security boundary for the instance.
8. **`DEPLOY_DOMAIN` variable is unused** by any workflow (informational only).
9. **Proxmox UI allowlist churns** — the operator's source IP changes; the SSH tunnel is the durable access path.
10. **Known CI flake** — the `postgres` job's `TestAuditChain` failure (jsonb normalisation) is non-blocking; re-run failed jobs with `gh run rerun --failed <id>` when other jobs flake.

Related: [Deployment overview](/docs/deployment/overview/), [Proxmox VM](/docs/deployment/proxmox-vm/), [CI/CD](/docs/deployment/ci-cd/), [Provisioning](/docs/deployment/provisioning/), [Operations](/docs/deployment/operations/).
