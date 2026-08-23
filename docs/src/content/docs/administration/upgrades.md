---
title: Upgrades
description: How to upgrade Northplane per deployment variant, what migrations do, how to roll back, compatibility notes and the version endpoints to verify with.
sidebar:
  order: 12
---

Northplane is one binary (or one container image) with the UI, the docs and the schema migrations embedded. Upgrading means replacing that artefact and restarting; migrations run automatically on the first start. The steps differ slightly per deployment variant.

## Before you upgrade

1. **Back up**: run `northplaned backup` (or snapshot the data volume) and make sure `secret.key` and `config.yaml` are safe — see [Storage → Backup](/docs/administration/storage/#backup). Schema migrations are forward-only; the backup is your rollback path for data.
2. Note the running version (`GET /api/v1/system/info` or **Admin → System health**) so you can roll back to exactly that artefact.
3. Read the release notes of the target version (GitHub Releases for tagged versions; commit history for `main-<sha>` images).
4. Plan a short outage: a restart takes seconds, migrations on the shipped schema take well under a minute, but active checks, SSE clients and agents reconnect afterwards (agents retry pushes; scheduled checks resume from the catalog).

## How versions are identified

| Where | What you see |
|---|---|
| `northplaned version` | `northplaned <version>` (also in the `help` header) |
| `GET /api/v1/system/info` (anonymous) | `"version":"…"` together with `goVersion`, `uptime`, `storage` |
| **Admin → System health (System-Health)** | the `system/info` card |
| Login, setup and register pages | version in the footer |
| `GET /api/openapi.json` / `northplaned openapi` | `info.version` |
| MCP (stdio and `/mcp`) | server implementation version |
| Federation | each edge reports its version in the heartbeat; **Admin → Sites (Standorte)** shows it per site |
| Backup manifest | `version` field |
| `np --version` | `np <version>`; the usage text (`np help`) carries it too |

The string is injected at build time (`-ldflags -X main.version=…`): `1.0.0-dev` for local builds, the git tag without the `v` for releases (`1.2.0`), `main-<12-char sha>` for images built from `main`, `docker` for an untagged local `docker build`.

## Upgrade per variant

### Single binary (systemd)

1. Install the new binaries. Re-running the installer fetches the **latest release** and replaces `northplaned`, `np` and `np-agent` in place (`install -m 0755`), verifying the SHA-256 checksums:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/myfoxit/northplane/main/install.sh | sh
   ```

   or download a specific `northplane_<tag>_<os>_<arch>.tar.gz` + `checksums.txt` from the release page, verify, and copy the three binaries to `/usr/local/bin`. (`NP_VERSION=vX.Y.Z` pins the release — see [Installation](/docs/getting-started/installation/#installsh).)
2. Optional pre-flight: `sudo -u northplane northplaned migrate -config /etc/northplane/config.yaml` applies pending migrations while the old service is still running and prints `migrations applied — schema is current`. The old binary keeps working with the newer schema in the usual case (migrations are additive), so this shortens the restart window.
3. `systemctl restart northplaned`, then watch `journalctl -u northplaned -f` for `storage: applying migration …` lines and `northplane: listening`.
4. Upgrade agents on the hosts (`np-agent` from the same tarball), then restart them (`systemctl restart np-agent`). Agents and server ship from the same release; the push/pull protocol is plain JSON over `/api/v1/results` and `/api/v1/agent/checks` with no version handshake, so upgrading them in either order is fine ([Agent](/docs/monitoring/agent/)).

### Docker

```bash
docker pull ghcr.io/myfoxit/northplane:latest      # or a specific tag
docker stop northplane && docker rm northplane
docker run -d --name northplane -v northplane-data:/var/lib/northplane <the same ports, env and mounts as before> \
  ghcr.io/myfoxit/northplane:latest
```

State lives in the volume (`/var/lib/northplane`: `core.db`, event segments, `tsdb/`, and `secret.key` unless you mounted one), so recreating the container is safe.

### Docker Compose

```bash
cd /opt/northplane        # or wherever the stack lives
docker compose pull
docker compose up -d
docker compose logs -f northplane
```

- The root `docker-compose.yml` references `ghcr.io/myfoxit/northplane:latest`; `pull` fetches whatever `latest` is now.
- The `deploy/` stacks read the image from `NORTHPLANE_IMAGE` in `.env` — pin it to an exact tag (`ghcr.io/myfoxit/northplane:main-daa6dc518a2b` or `:1.2.0`) and change that line to upgrade. Keep the previous value (the CI keeps it as `.env.previous`) for rollback.
- Caddy is upgraded the same way (`caddy:2-alpine`); certificates persist in the `caddy-data` volume.

Details of the stacks: [Docker Compose deployment](/docs/deployment/docker-compose/).

### CI-driven production

On the reference production instance nothing is done by hand: a merge to `main` triggers CI, and a green CI run triggers the Deploy workflow, which builds and pushes `ghcr.io/myfoxit/northplane:main-<sha>` (+ `latest`), renders a fresh `.env` on the server (keeping the old one as `.env.previous`), runs `docker compose pull && docker compose up -d --remove-orphans`, and then verifies for up to 12 × 5 s that the container runs the wanted image **and** `curl http://localhost:8443/healthz` answers `ok`. If verification fails it restores `.env.previous` and brings the previous image back up — an automatic rollback. Manual runs (`workflow_dispatch`) can also flip demo mode. The whole chain, the GitHub variables/secrets and how to read a red run are documented in [CI/CD](/docs/deployment/ci-cd/); the current state of each environment is in [Environments](/docs/deployment/environments/).

Tagged releases (`v*`) additionally produce the tarballs, the Windows zip (`np`/`np-agent` only) and semver image tags ([Release process](/docs/development/release-process/)).

## Schema migrations

- Migrations are applied automatically by the first command that opens the store after the upgrade — normally `serve` — inside one transaction per migration, and logged as `storage: applying migration version=N name=…`. `northplaned migrate` does the same without starting the server.
- The migration list is embedded in the binary (9 migrations as of this version: `core`, `seed`, `user_roles`, `report_archive_slot`, `alert_ticket`, `hotpath_indices`, `user_tenant`, `ai_agent_chat`, `alert_snooze`) and tracked in the `schema_version` table; see [Storage → Schema migrations](/docs/administration/storage/#schema-migrations).
- A failing migration stops the start (`northplaned: storage: migration N "name": …`); the database is left at the last successfully committed migration. Fix the cause (disk, permissions, a PostgreSQL privilege) and start again.
- Boot also reconciles built-in roles additively (for example the `operator` role gains `alerts:write` if missing) and seeds the break-glass admin if no enabled local admin exists — both are idempotent and logged.
- The NP-TSDB has no migration step; its block and aggregate files carry format headers (`NPBLOCK1`, `NPAGGR1`) and are opened in place.

## Rollback

Northplane has no down-migrations, so the cleanest rollback restores the pre-upgrade backup together with the previous artefact:

| Variant | Artefact rollback | Data rollback |
|---|---|---|
| Compose (deploy stacks) | `mv .env.previous .env && docker compose up -d`, or set `NORTHPLANE_IMAGE` back to the previous tag | restore `core.db`, `events-*.db`, `tsdb/` from the backup into the volume while the container is stopped ([Restore](/docs/administration/storage/#restore)) |
| Compose (root stack, `:latest`) | `docker compose pull` cannot go back by itself — set `image:` to an explicit older tag and `up -d` | same |
| Docker | run the older tag | same |
| Single binary | reinstall the previous tarball's binaries, `systemctl restart northplaned` | restore the data directory from the backup |
| CI-driven | re-run the Deploy workflow for the previous green commit, or repoint `NORTHPLANE_IMAGE` on the server and `up -d`; the workflow rolls back automatically when verification fails | restore the volume on the VM |

Because the migration runner only applies versions it knows and ignores higher ones, an older binary usually **starts** against a newer schema (the added columns/tables are simply unused). That is convenient for a quick revert after a bad deploy, but it is not a supported state to run in for long — restore the backup or move forward again.

## Compatibility notes

- **UI and docs are embedded** in the binary/image, so they are always exactly in sync with the API — there is nothing to clear or redeploy separately. Browsers pick up the new assets on reload (`/assets/*` are content-hashed and cached immutably; `index.html` is `no-cache`).
- **API**: all routes live under `/api/v1`; responses are RFC 9457 problem documents; the OpenAPI document is generated from the route registry and the TypeScript client types are drift-checked in CI (`make types-check`), so the UI cannot silently lag behind the API. External clients should tolerate new fields in JSON responses.
- **Tokens, sessions, secrets** survive upgrades; sessions are stored in the database, tokens are hashed rows, secrets are sealed with `secret.key`. Never change `secret.key` as part of an upgrade.
- **Federation**: main and edge instances are independent full installations; each reports its version in the heartbeat, and **Admin → Sites** shows it. Upgrade them independently; the pull/heartbeat protocol (`sites:pull` with ETag, `sites:heartbeat` JSON) has no version negotiation, so keep both on the same major version.
- **MCP clients** connect with the same API tokens; tool lists may grow between versions.
- **Event retention, TSDB retention** and other constants may change between versions — re-read [Configuration → Not configurable](/docs/administration/configuration/#not-configurable) after major upgrades.

## Verifying an upgrade

1. `curl -fsS https://<instance>/healthz` → `ok`; `curl -fsS https://<instance>/readyz` → `"ready":true`.
2. `curl -fsS https://<instance>/api/v1/system/info` → the expected `version`.
3. Logs show the expected migration lines (or none) and no `background worker panicked` messages.
4. Log in, open **Overview** and **Admin → System health**; queue depths near zero, `scheduler.scheduled` equals your object count.
5. Trigger a check (`np check-now <object-id>`) and a test notification (`POST /api/v1/channels/{name}:test-notification`) to confirm the pipeline and the outbox.
6. `np audit verify` → `audit chain intact (N entries verified)`.
7. If you run agents or an edge, check **Admin → Agents** / **Admin → Sites** for fresh heartbeats.
