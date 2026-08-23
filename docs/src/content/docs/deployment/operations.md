---
title: Operations runbook
description: Day-2 operations for the production stack — status and logs, health endpoints, redeploy and rollback, the demo/real switch, credentials rotation, backups, disk and retention, restarts, the agent fleet, Caddy health checks, and the lab appendix.
sidebar:
  order: 6
---

This runbook is written for the production instance `np-01` (`https://doktrace.com`, VM101 `saas1` behind the Proxmox host `51.83.96.40`) but applies to any host running one of the Compose stacks. Topology and access are on the [Proxmox VM](/docs/deployment/proxmox-vm/) page; the inventory is on [Environments](/docs/deployment/environments/).

## Where things are

| What | Where |
|---|---|
| Compose project | `/opt/northplane` on the VM (`docker-compose.yml`, `.env`, `.env.previous`, `secret.key`); owner `deploy`, 0750 |
| Container | `northplane-northplane-1` (project name `northplane`) |
| Data volume | `northplane_northplane-data` → `/var/lib/northplane` in the container; real data in `real/`, demo data in `demo/` |
| Master key | `/opt/northplane/secret.key` → `/etc/northplane/secret.key` (read-only), uid 65532, 0600 |
| Listener | `:8443` in the container, published `0.0.0.0:8443` on the VM (private bridge only) |
| TLS / domain | CT100 Caddy, `/etc/caddy/sites/saas1.caddy` → `10.10.10.11:8443` |
| Logs | container stderr (JSON, `logFormat: json`, level `info`) — `docker compose logs` |
| Who deploys | the Deploy workflow as `deploy@51.83.96.40 -p 2201` — [CI/CD](/docs/deployment/ci-cd/) |

Shell onto the VM: `ssh -J root@51.83.96.40 rocky@10.10.10.11`, then `sudo -iu deploy` (or work with `sudo docker …`) and `cd /opt/northplane`.

## Status and logs

```bash
cd /opt/northplane
docker compose ps                               # State, image, ports
docker compose logs -f --tail=200 northplane     # JSON log lines on stderr
docker compose logs northplane | grep -i 'listening\|seeded default admin\|secret-store\|warn'
docker inspect --format '{{.Config.Image}} {{.State.StartedAt}}' northplane-northplane-1
cat .env                                         # what the pipeline rendered (contains the admin password)
```

Log lines worth knowing: `northplane: listening` (with `addr`, `scheme`, `storage`, `objects`), `seeded default admin …` (only on the very first start of a fresh data dir, or whenever no enabled local admin exists), `server: configured secretKeyFile unusable — falling back to the data directory` (the `secret.key` mount is broken — fix it before anyone stores a secret), `server: background worker panicked; restarting` (a worker crashed and was restarted after 1 s). See [Observability](/docs/administration/observability/) for the full log and metrics reference.

## Health endpoints

| Endpoint | Auth | Returns | Use |
|---|---|---|---|
| `GET /healthz` | none | `ok` (200) as soon as the listener is up | liveness — what Caddy and the deploy verify probe |
| `GET /readyz` | none | `{"ready":true,"subsystems":[{"name":"storage","ok":true,"info":"sqlite"},{"name":"eventbus","ok":true},{"name":"scheduler","ok":true}]}`; 503 when any subsystem is not ok (storage ping fails, or the results queue depth is ≥ 8000) | readiness |
| `GET /api/v1/system/info` | none | `version`, `goVersion`, `goroutines`, `heapMB`, `startedAt`, `uptime`, `storage`, `aiEnabled` | which build is live — [reference](/docs/reference/api/operations/get_system_info/) |
| `GET /api/v1/system/health` | none | queue depths, scheduler lag, pipeline, alerting, notify counters, TSDB stats, catalog size, SSE clients | first look when "something is slow" — [reference](/docs/reference/api/operations/get_system_health/) |
| `GET /metrics` | none (restrict at the network) | OpenMetrics: `np_http_requests_total`, `np_queue_*_depth`, `np_notifications_total{result}`, `np_tsdb_*`, … | scraping |

```bash
# on the VM
curl -s localhost:8443/healthz; echo
curl -s localhost:8443/readyz | python3 -m json.tool
curl -s localhost:8443/api/v1/system/info
# from anywhere (Cloudflare in front — use a curl-like User-Agent, not Python's)
curl -s https://doktrace.com/api/v1/system/info
# bypass Cloudflare and talk to the origin (valid certificate, SNI via --resolve)
curl -s --resolve doktrace.com:443:51.83.96.40 https://doktrace.com/readyz
```

The **Admin → System health** tab shows the same `/system/info` and `/system/health` data in the UI.

## Deploy, redeploy, rollback

- **Normal deploy:** merge to `main`; CI green → Deploy → `np-01` rolls forward in a few minutes. Check the `deploy` job's "Verify rollout" step and `system/info` afterwards.
- **Redeploy the same commit** (for example to re-render `.env` after changing a repository variable): **Actions → Deploy → Run workflow** with `demo = repo-default`.
- **Pipeline rollback (automatic):** if the verify loop fails, the job restores `.env.previous` and runs `docker compose up -d` — production is back on the previous image tag. The run is red; production is healthy on the old build.
- **Manual rollback to any earlier build:** every green build of `main` is an immutable tag.
  ```bash
  cd /opt/northplane
  cp .env .env.manual-$(date +%F)
  sed -i 's#^NORTHPLANE_IMAGE=.*#NORTHPLANE_IMAGE=ghcr.io/myfoxit/northplane:main-<sha12>#' .env
  echo "<PAT with read:packages>" | docker login ghcr.io -u <github-user> --password-stdin
  docker compose pull -q && docker logout ghcr.io
  docker compose up -d
  curl -s localhost:8443/api/v1/system/info     # version == main-<sha12>
  ```
  The next pipeline run overwrites `.env` again — a manual pin lasts until the next merge to `main`. Schema migrations are forward-only; rolling back to a build older than a migration that has already been applied is not supported — see [Upgrades](/docs/administration/upgrades/).
- **Hotfix without CI:** not supported by design; the image is built only by the pipeline. Fix forward on `main`.

## Switching between demo and real data

The instance runs in **real** mode since 2026-08-20 (`NORTHPLANE_DEMO=false`, `NORTHPLANE_DATA_DIR=/var/lib/northplane/real`). The demo data directory `/var/lib/northplane/demo` still exists in the same volume.

1. Durable switch: set the repository variable `NORTHPLANE_DEMO` (`gh variable set NORTHPLANE_DEMO --body true|false`), then run **Deploy** manually (or merge). The workflow derives `NORTHPLANE_DATA_DIR` from the switch (`demo` → `/var/lib/northplane/demo`, `false` → `/var/lib/northplane/real`) and re-renders `.env`.
2. One-off switch: **Run workflow** with `demo = true|false` — affects that run only; the next automatic deploy returns to the variable.
3. What happens: the container restarts against the other directory. In demo mode the seed runs idempotently (`demo-*` objects, demo users `demo-operator` / `demo-viewer`); in real mode nothing is seeded and the break-glass admin is (re)created if no enabled local admin exists in that dataset.
4. Safety: the two datasets never mix; if `NORTHPLANE_DEMO=true` is ever pointed at a directory that holds real (non-demo) hosts the server logs `NORTHPLANE_DEMO is set but this database already holds real (non-demo) hosts — skipping demo seeding …` and starts without seeding.

Each data directory has its **own** users, tokens and secrets (encrypted with the shared `secret.key`). An API token minted in demo mode does not exist in real mode and vice versa. Details: [Demo mode](/docs/getting-started/demo-mode/).

## Rotating credentials

| Credential | How to rotate |
|---|---|
| Break-glass admin password (`admin@doktrace.com`) | change it in the UI (**Admin → Users**, per-row **Set password (Passwort setzen)**) or `POST /api/v1/users/{id}:set-password` ([reference](/docs/reference/api/operations/post_users_id_set_password/)); the `NP_DEFAULT_ADMIN_PASSWORD` secret only matters when the account has to be re-created, so update the GitHub secret too and keep the two in step |
| Your own password | the **Change my password (Mein Passwort ändern)** card under **Admin → Users**, or `POST /api/v1/users/me:change-password` (session users only, tokens get 401); minimum 12 characters |
| API tokens (agents, CLI, MCP, federation edge) | `POST /api/v1/api-tokens/{id}:rotate` or **Admin → API tokens** — the new secret is shown once; update `agent.yaml` / `NP_TOKEN` / the edge `config.yaml` and restart the consumer. See [API tokens](/docs/administration/api-tokens/) |
| Stored secrets (`$SECRET:name$` — Twilio, SMTP, MQTT, AI keys) | `PUT /api/v1/secrets/{name}` or **Admin → Secrets**; channels re-resolve on use. See [Secrets](/docs/administration/secrets/) |
| `secret.key` (the master key) | there is no re-encryption command. Rotating it means: export/record every stored secret, replace the file (64 hex chars, uid 65532, 0600), restart, re-enter every secret. Treat the file as permanent and back it up instead |
| CI deploy key | generate a new key pair, run `provision-server.sh` again with the new `DEPLOY_PUBKEY` (it appends), update the `DEPLOY_SSH_KEY` secret, remove the old line from `~deploy/.ssh/authorized_keys` |

## Backups

What must be backed up, and why:

| Item | Why | Where |
|---|---|---|
| `/opt/northplane/secret.key` | decrypts every secret at rest; not recoverable | VM, 65 bytes |
| volume `northplane_northplane-data` | SQLite `core.db` (+ WAL), monthly `events-YYYYMM.db`, `tsdb/` (blocks, aggregates, WAL, series journal), `artifacts/` — for both `real/` and `demo/` | VM |
| `/opt/northplane/.env` | reproducible from GitHub variables/secrets; a copy saves time in a disaster | VM |
| GitHub variables and secrets | the actual source of the `.env` | repository settings |

There is **no** periodic backup in the product (`backup.interval` is parsed but unused; only `northplaned backup` on demand exists) and **no** `vzdump` job on the Proxmox host. Until one of the following is scheduled (cron on the VM, for example), there are no backups of production.

**Option A — cold copy of the volume** (simple, a minute of downtime):

```bash
cd /opt/northplane
docker compose stop northplane
docker run --rm -v northplane_northplane-data:/data:ro -v "$PWD":/backup alpine \
  tar czf "/backup/northplane-data-$(date -u +%Y%m%dT%H%M%SZ).tgz" -C /data .
docker compose start northplane
cp secret.key "secret.key.$(date -u +%Y%m%d)"
# then move the .tgz and the key copy off the VM
```

**Option B — hot, application-consistent backup with `northplaned backup`.** The subcommand uses SQLite `VACUUM INTO` for `core.db` (transaction-consistent without stopping writers), copies the event segments (hot month last) and the `tsdb/` tree, and writes `manifest.json` into `<backup.target>/northplane-<YYYYMMDD-HHMMSS>/`. It does not copy `artifacts/`. The binary is in the image, so it can be started as a second process inside the running container with the target directory inside the volume:

```bash
cd /opt/northplane
docker compose exec -e NORTHPLANE_BACKUP_TARGET=/var/lib/northplane/backups northplane \
  northplaned backup
# → backup complete: /var/lib/northplane/backups/northplane-20260823-120000/manifest.json
docker run --rm -v northplane_northplane-data:/data:ro -v "$PWD":/backup alpine \
  tar czf "/backup/northplane-backup-$(date -u +%Y%m%d).tgz" -C /data/backups .
```

:::caution[Option B has not been exercised on the production stack]
The command above composes documented behaviour (`NORTHPLANE_BACKUP_TARGET` selects `backup.target`; `NORTHPLANE_DATA_DIR` is inherited from the container environment; the data directory is writable by uid 65532), but as of 2026-08-23 it has not been run against `np-01`. Try it on the staging instance first and check the manifest. Restore is manual: stop the stack, replace `core.db`, `events-*.db` and `tsdb/` in the data directory with the backup's files, start — see [Storage](/docs/administration/storage/).
:::

Keep the `caddy-data` volume too if you run the bundled-Caddy stack (certificates); on `np-01` the certificates live in CT100.

## Disk and retention

| Data | Retention | Enforced by |
|---|---|---|
| Events (`events-YYYYMM.db`) | `storage.eventRetentionMonths`, default **12** (`0` = keep all) — whole month segments are deleted | janitor, nightly between 02:00 and 03:59 local time |
| NP-TSDB | raw 30 days, 5-minute aggregates 400 days, 1-hour aggregates 5 years; max 100 000 series — hard-coded | TSDB maintenance in the same nightly window |
| Report archive | last 12 renderings per report and format | on insert |
| Sessions, idempotency keys | expired sessions; idempotency rows older than 24 h | every 10 minutes |
| Audit log | never purged | — |
| Docker images | every deploy pulls a new `main-<sha12>` image; old ones accumulate | you: `docker image prune -a --filter "until=720h"` (keeps the running image) |

```bash
docker system df -v                                     # images, volumes, sizes
docker volume inspect northplane_northplane-data --format '{{.Mountpoint}}'
sudo du -sh /var/lib/docker/volumes/northplane_northplane-data/_data/{real,demo}
df -h /var/lib/docker
```

The VM has 4 vCPU and 8 GB RAM; watch `df -h` on the Docker volume file system when adding many hosts or keeping long event retention.

## Restarting and reboots

- `docker compose restart northplane` — graceful: SIGTERM, the server drains workers with a 30 s budget, then exits; `restart: unless-stopped` brings it back. Expect brief 502/503 answers from Caddy until its next successful health probe (see below).
- `docker compose up -d` after editing `.env` — recreates the container only if something changed.
- VM reboot: Docker starts the stack automatically (`unless-stopped`). The `np-agent` service on the VM restarts too.
- Hypervisor reboot: during the reboot on 2026-08-19 the domain was back within a minute (Caddy's 30 s health check briefly 503'd the upstream while `northplaned` booted, then self-cleared). Re-check the guests' onboot flags before relying on it.
- Config changes need a restart — there is no SIGHUP reload. Everything in `.env` is read at start.
- Hung process: `docker compose kill northplane && docker compose up -d`. SQLite in WAL mode recovers on open; the TSDB replays its WAL.

## The agent fleet

`np-agent` pushes passive results (`load`, `memory`, `disk /`, `processes`, `network`, heartbeat) over HTTPS every 60 s to `POST /api/v1/results`. Fleet as of 2026-08-23:

| Agent host | Runs on | Reports to | Token (name, scope) |
|---|---|---|---|
| `np-prod` | VM101 `saas1` (the production VM itself) | `https://doktrace.com` | `np-agent-np-prod`, `objects:write` (verified) |
| `pve-host` | the Proxmox hypervisor | `https://doktrace.com` | `np-agent-<host>` pattern, `objects:write` (lab notes) |
| `netlab` | VM102 | `https://doktrace.com` | `np-agent-<host>` pattern, `objects:write` (lab notes) |
| `alarmlab` | VM103 | `https://doktrace.com` | `np-agent-<host>` pattern, `objects:write` (lab notes) |
| `np-staging` | VM104 | its **local** edge instance on VM104 — not production | minted on the edge |

Each agent is a systemd service `np-agent` with the binary at `/usr/local/bin/np-agent` and the config at `/etc/northplane/agent.yaml` (verified on `saas1`: `server: https://doktrace.com`, `hostname: np-prod`, `insecure: false`, `interval: 60s`, `disk: [/]`, token `np-agent-np-prod`).

```bash
systemctl status np-agent
journalctl -u np-agent -n 50 --no-pager        # "np-agent: started" / "submit failed, buffering"
sudo cat /etc/northplane/agent.yaml             # token is in here — keep 0600 or root-readable only
sudo systemctl restart np-agent
```

Rules that bite: the Host object **and** the passive Service objects (named exactly like the agent's services, e.g. `disk /`) must exist on the server before results are accepted — `POST /api/v1/results` rejects unknown hosts; give passive services a `stalenessAfter` so a silent agent turns the service stale instead of staying green forever; an agent that cannot reach the server buffers and retries (store-and-forward). Installing, `agent.yaml` keys, collectors and troubleshooting: [Monitoring → Agent](/docs/monitoring/agent/). Token minting: **Admin → Agents** or [API tokens](/docs/administration/api-tokens/).

## Caddy health check behaviour

CT100's site block probes `http://10.10.10.11:8443/healthz` every 30 s with a 5 s timeout (`health_uri`, `health_interval`, `health_timeout`). When the probe fails — during a deploy, a restart, or an outage — Caddy marks the only upstream unhealthy and answers 502/503 for the domain until a probe succeeds again; nothing has to be done by hand. The bundled-Caddy stack has no active health check in its `Caddyfile`; its compose `healthcheck` only colours `docker compose ps`. On `np-01` a restart is therefore visible externally as up to ~30 s of errors after the app is already up. Caddy renews the Let's Encrypt certificate automatically; `journalctl -u caddy` inside CT100 (`pct enter 100`) shows issuance and renewal.

## When alarming misbehaves

- Notifications not arriving: **Admin → Dead letters** (`GET /api/v1/notifications/dead-letters`, replay with `:replay`) and the `notification`/`escalation` events on the Events page show every delivery attempt and suppression reason. Outbox backoff and the dead-letter queue are explained under [Alarming → Reliability](/docs/alarming/reliability/).
- Channel credentials: stored secrets must exist in the **current** data directory (real vs demo) — see [switching modes](/docs/deployment/operations/#switching-between-demo-and-real-data).
- Who did what: **Admin → Audit log** (`GET /api/v1/audit`, `POST /api/v1/audit:verify` checks the hash chain).
- Performance: `GET /api/v1/system/health` — queue depths and `maxLagMs`; `/readyz` turns 503 only when the results queue exceeds 8000.

## Appendix: the Proxmox lab

The other guests on `51.83.96.40` form the monitoring/alarm lab that feeds production with real targets. The guest list was re-verified on 2026-08-23; the service details below come from the lab notes of 2026-08-19/21 and were not re-checked individually.

| Guest | Address | What it provides |
|---|---|---|
| CT110 `targets` | 10.10.10.20 | nginx `:80` (ok), `:8081` slow (3 s), `:8082` always 500; `:443`/`:8443`/`:9443` with valid / 8-day / expired certificates; `snmpd` (community `nplane-ro`); NRPE `:5666`; BIND zone `lab.local`; chrony; an `np-agent` in listener mode on `:5693` — one live target for every builtin check |
| VM103 `alarmlab` | 10.10.10.13 | `/opt/alarmlab` compose: Mosquitto `:1883`, Mailpit `:1025`/`:8025`, GreenMail `:3025`/`:3143`, ntfy `:8080`, an echo sink `:9000` (`/_count`, `/_log`, `/_reset`) standing in for Slack/Teams/ServiceNow/Jira/Zendesk/SMS; ESPA drivers in `/opt/drivers/` |
| VM102 `netlab` | 10.10.10.12 | containerlab `/opt/netlab`: 2× Cisco IOSv 15.8(3)M2 (`172.30.0.31`/`.32`, real OSPF link r1–r2), 2× Nokia SR Linux, Alpine + snmpd; `trap-relay.service`; routes to `172.30.0.0/24` are pinned on `.11` and `.14`. `containerlab deploy --reconfigure` **wipes** the IOSv configs (trap host, VRF fix, OSPF link) — re-run `/opt/netlab/configure-cisco.sh` afterwards |
| VM104 `np-staging` | 10.10.10.14 | second Northplane (real data, self-signed TLS, local admin); publishes `8443` plus `9162/udp`, `2023`, `8123`, `4573`; federation **edge** of production for tenant MyFoxIT (site `vm104-edge`, edge config mounted at `/etc/northplane/config.yaml`, owned by uid 65532); its `mqtt-in` source is disabled because two instances with the same MQTT client id evict each other. Reach the UI with `ssh -N -L 18443:10.10.10.14:8443 root@51.83.96.40` → `https://localhost:18443` |

The lab configuration is one bundle (`/opt/lab/lab-bundle.yaml` on the VMs, a copy in `/root/` on the hypervisor), applied to production idempotently with `POST /api/v1/config/bundles:apply` (`Content-Type: application/x-yaml`) — see [Config bundles](/docs/administration/config-bundles/). SNMP and trap specifics for the Cisco routers are under [Monitoring → SNMP](/docs/monitoring/snmp/); the edge set-up under [Federation](/docs/concepts/federation/).
