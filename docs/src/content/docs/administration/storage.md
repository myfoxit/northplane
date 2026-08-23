---
title: Storage
description: SQLite versus PostgreSQL, the data directory layout, schema migrations, moving between backends, backup and restore, retention and sizing.
sidebar:
  order: 8
---

Northplane keeps three kinds of state: the **relational core** (objects, state, alerts, config documents, users, tokens, secrets, audit log, outbox), the **event log** (append-only, partitioned by month) and the **NP-TSDB** (perfdata time series, plain files). The relational core and the event log live in SQLite by default or in PostgreSQL; the TSDB always lives as files under `dataDir`, regardless of backend.

Configuration keys: `storage.dsn`, `storage.eventRetentionMonths`, `dataDir` — see the [Configuration reference](/docs/administration/configuration/#storage).

## Backends at a glance

| Aspect | SQLite (default) | PostgreSQL |
|---|---|---|
| Selected by | `storage.dsn` empty (⇒ `<dataDir>/core.db`) or a file path | `storage.dsn` starting with `postgres://` or `postgresql://` |
| Driver | `modernc.org/sqlite` (pure Go, no CGO) | `github.com/jackc/pgx/v5` (`pgx` stdlib driver) |
| Core database | one file `core.db` (+ `core.db-wal`, `core.db-shm`) | one database, tables created by migrations |
| Events | monthly segment files `events-YYYYMM.db` in `dataDir` | parent table `events` partitioned by range on `ts`, child partitions `events_YYYYMM` created on demand |
| Journal / durability | `PRAGMA journal_mode=WAL` set once on the file; per connection `synchronous(NORMAL)`, `busy_timeout(5000)`, `foreign_keys(ON)` | server-side |
| Connection pool | 16 open, 16 idle, no idle/lifetime expiry (so the per-connection pragmas are not re-run under load) | 16 open, 8 idle, `ConnMaxLifetime` 30 min, `ConnMaxIdleTime` 5 min |
| Write concurrency | all writes serialised through one in-process mutex + `BEGIN`/`COMMIT`; WAL lets readers run concurrently | server-side MVCC |
| Types | timestamps as RFC 3339 text (UTC), JSON as text, booleans as integers | `timestamptz`, `jsonb`, `boolean`, `bytea`, `bigint` identity columns |
| Backup | `northplaned backup` (`VACUUM INTO` + segment copies + TSDB) | your PITR/`pg_dump` job for the relational part; `northplaned backup` still copies the TSDB and writes a manifest |
| Test status | the shipped default, fully green in CI | storage test suite runs against PostgreSQL 16 in CI, non-blocking (see caveat below) |

Which to choose: SQLite is the tested, zero-dependency default and is what the production instance in [Environments](/docs/deployment/environments/) runs. Choose PostgreSQL when you already operate it, need its backup/replication tooling, or want the relational data off the local disk. Northplane has no multi-node/HA mode; a PostgreSQL backend does not change that.

## SQLite

- The core database path is always `<dataDir>/core.db` when `storage.dsn` is empty. A non-URL, non-empty `storage.dsn` is treated as a SQLite **file path** — useful for tests or for placing `core.db` on a different filesystem than the TSDB.
- The directory of the database file is created (`0750`) if missing.
- WAL mode is persistent in the file header; you will always see `core.db-wal` and `core.db-shm` next to the database while the server runs. Do not delete them.
- `busy_timeout` is 5 s: a second process holding a long write lock (for example a manual `sqlite3` session in a write transaction) makes requests wait up to 5 s and then fail. Use read-only tools while the server runs, or stop it first.
- Event segments are separate databases (own handle, 4 connections each, same pragmas) so that dropping a month is a file deletion, not a `DELETE`.

### Known PostgreSQL caveat

:::caution[Audit chain verification on PostgreSQL]
`before`/`after` snapshots of audit entries are stored as `jsonb` on PostgreSQL, which normalises JSON text, while the row hash is computed over the original text; timestamps also round-trip at microsecond precision. As a result `POST /api/v1/audit:verify` (and `np audit verify`) can report a broken chain on PostgreSQL even though nothing was tampered with. This is a known, pre-existing failure (`TestAuditChain`) and the reason the PostgreSQL CI job is non-blocking. SQLite is unaffected.
:::

## PostgreSQL

```yaml title="config.yaml"
storage:
  dsn: "postgres://np:<secret>@db.internal:5432/northplane?sslmode=require"
```

or `NORTHPLANE_STORAGE_DSN=postgres://…` in a container (the Compose files carry a commented `postgres:16` service and DSN for exactly this).

- The database and the role must exist; migrations create all tables on first open (including the `events` parent table and its monthly partitions).
- The pool is bounded to 16 connections and actively recycles idle ones, which keeps it healthy behind pgbouncer or a load balancer with idle timeouts.
- Event partitions `events_YYYYMM` are created on demand by the event store and dropped by retention (`DROP TABLE`), discovered via `pg_tables LIKE 'events_2%'`.
- `dataDir` is still required: the NP-TSDB (`<dataDir>/tsdb/`), the fallback `secret.key`, artifacts and plugins live there.

## Data directory layout

`dataDir` defaults to `/var/lib/northplane` as root (and in the container), `~/.local/share/northplane` / `$XDG_DATA_HOME/northplane` on Linux as a user, `~/Library/Application Support/northplane` on macOS.

| Path | Content |
|---|---|
| `core.db`, `core.db-wal`, `core.db-shm` | SQLite core database (SQLite mode only) |
| `events-YYYYMM.db` (+ `-wal`, `-shm`) | one event segment per month (SQLite mode only); the current month's segment is created at open |
| `tsdb/series.jsonl` | NP-TSDB series registry (append-only JSONL with in-place threshold updates and `deleted` tombstones) |
| `tsdb/wal.log` | NP-TSDB write-ahead log (25-byte records, fsync batched every 1 s, rewritten after each flush) |
| `tsdb/blocks/block-<ms>.npb` | immutable 2-hour raw blocks (Gorilla-compressed) |
| `tsdb/agg/agg-<ms>-5m.npa`, `tsdb/agg/agg-<ms>-1h.npa` | daily 5-minute and 1-hour downsampled tiers |
| `secret.key` | AES master key (64 hex characters) — **fallback location** used when `secretKeyFile` is unset or unusable; `northplaned init` writes the key to the config directory instead (`/etc/northplane/secret.key`) |
| `artifacts/` | check artifacts directory (reserved for E2E check artefacts) |
| `plugins/` | last candidate of the plugin-directory auto-detection (see [Configuration](/docs/administration/configuration/#load-order-and-precedence)) |

Not files: the Web Push VAPID key pair and the ack-link signing secret are stored in the `kv` table of the core database (keys `vapid`, `ack_secret`), as are site status records and the AI tool policy.

Ownership: the systemd unit written by `init` uses `StateDirectory=northplane` and `ReadWritePaths=<dataDir>`; the container runs as uid 65532 (distroless `nonroot`) and expects the volume at `/var/lib/northplane` to be writable by that uid.

## Schema migrations

Migrations are embedded in the binary, forward-only, and applied automatically by **every** command that opens the store — `serve`, `migrate`, `storage migrate`, `backup`, `mcp`, `bootstrap-admin`. Each migration runs in its own transaction and is recorded in `schema_version (version, name, applied_at)`; pending ones are logged as `storage: applying migration version=N name=…`. A failing migration aborts the command (`storage: migration N "name": …`) and the server does not start.

| # | Name | Content |
|---|---|---|
| 1 | `core` | all base tables |
| 2 | `seed` | default tenant `Default`/`default` and the built-in roles `admin`, `operator`, `viewer`, `ai-agent` |
| 3 | `user_roles` | `users.roles` JSON column |
| 4 | `report_archive_slot` | `report_archive` recreated with a `slot` column |
| 5 | `alert_ticket` | `alerts.ticket_url`, `alerts.ticket_meta` |
| 6 | `hotpath_indices` | partial index on problem states, alert indexes by object and rule |
| 7 | `user_tenant` | `users.tenant_id` (home tenant, defaults to the Default tenant) |
| 8 | `ai_agent_chat` | `ai_provider_connections`, `ai_chats`, `ai_chat_messages` |
| 9 | `alert_snooze` | `alerts.snoozed_until` + partial index |

`northplaned migrate -config <path>` opens the store, applies whatever is pending and prints `migrations applied — schema is current`. Use it as a pre-flight step during [upgrades](/docs/administration/upgrades/) when you want the schema change to happen before the service restarts. There are no down-migrations; the migration runner simply skips versions it already sees in `schema_version`, so an **older** binary started against a newer database does not fail the migration step — whether it runs correctly depends on the change. Restore from backup for a clean rollback.

## Moving between backends

`northplaned storage migrate --to <dsn>` copies the relational data from the backend named in your config to another one. It is an **offline** operation: the downtime equals the copy time.

1. Stop `northplaned` (and any `np-agent` push traffic can simply wait — results are retried).
2. Make sure the target exists (an empty PostgreSQL database, or a path for a new SQLite file). Migrations are applied to the target automatically.
3. Run the copy with the **current** config (it defines the source):

   ```bash
   northplaned storage migrate -config /etc/northplane/config.yaml --to 'postgres://np:<secret>@db.internal:5432/northplane?sslmode=require'
   ```

   What is copied, in order: the 23 generic tables (`tenants, users, objects, object_labels, check_state, alerts, incidents, resources, downtimes, silences, heartbeats, api_tokens, sessions, secrets, idempotency, escalations, outbox, ai_actions, ai_conversations, ai_usage, push_subscriptions, report_archive, kv`) with `INSERT … ON CONFLICT DO NOTHING` (the target's seeded default tenant/roles are kept), then `audit_log` with explicit sequence numbers so the hash chain stays intact, then all events per tenant oldest-first in pages of 1000 into the target's partitioning. Booleans and timestamps are converted between the dialects. On success it prints `copied <n> rows. Point storage.dsn at the target and restart (NP-TSDB unaffected).`
4. Set `storage.dsn` (or `NORTHPLANE_STORAGE_DSN`) to the target DSN and start the server.

Notes:

- The NP-TSDB is not touched — it is backend-independent and stays under `dataDir`.
- `--to` may also be a SQLite file path. In that case the target's event segments are written under `<dataDir>-migrated/`, because SQLite segments are placed relative to a data directory, not the DSN; move them into the active `dataDir` before switching.
- Secrets are copied as ciphertext; the target instance needs the **same** `secret.key`.
- The command applies target migrations itself; you do not need to run `migrate` separately.

## Retention

| Data | Retention | Enforced by |
|---|---|---|
| Events | `storage.eventRetentionMonths`, default **12**; `0` = keep all. Whole months are dropped (SQLite: segment file + `-wal`/`-shm` deleted; PostgreSQL: partition dropped) when the month key is older than now − N months | janitor, nightly window 02:00–03:59 local time, at most once per 20 h |
| NP-TSDB raw samples | 30 days (fixed) | nightly `TSDB.Maintain` (flush, downsample, delete expired files by window start) |
| NP-TSDB 5-minute aggregates | 400 days (fixed) | same |
| NP-TSDB 1-hour aggregates | 5 years (fixed) | same |
| NP-TSDB series | cap 100 000 series; new series beyond the cap are dropped and counted (`seriesDropped`) | at ingest |
| Sessions | deleted once expired | janitor, every 10 min |
| Idempotency keys | 24 h | janitor, every 10 min |
| Report archive | `keep` distinct slots per report, default 12 | on insert |
| Alerts, incidents, objects, config documents | kept until deleted/resolved by you or a rule (`autoCloseAfter` expires alerts) | — |
| Audit log | **never purged** | — |
| Outbox | rows deleted on successful delivery; dead letters stay until replayed/deleted | notify worker |

The TSDB retention values are not configurable in this version — see [Configuration → Not configurable](/docs/administration/configuration/#not-configurable). Formats, downsampling and the query API are described in [Metrics and NP-TSDB](/docs/monitoring/metrics-and-tsdb/).

## Backup

### `northplaned backup`

Set `backup.target` (or `NORTHPLANE_BACKUP_TARGET`) to a directory and run the command; it may run while the server is serving traffic:

```bash
NORTHPLANE_BACKUP_TARGET=/var/backups/northplane northplaned backup -config /etc/northplane/config.yaml
# backup complete: /var/backups/northplane/northplane-20260823-020000/manifest.json
```

It creates `<target>/northplane-<YYYYMMDD-HHMMSS>/` (UTC timestamp, mode `0750`) containing:

| File | Included | Notes |
|---|---|---|
| `core.db` | SQLite mode | produced with `VACUUM INTO` — transaction-consistent without stopping writers |
| `events-YYYYMM.db` | SQLite mode | every segment copied; the hot current-month segment last (worst case: the last seconds of events are missing) |
| `tsdb/` | always | whole tree copied; blocks and aggregates are immutable, WAL and series journal are replay-safe |
| `manifest.json` | always | `{"format":"northplane-backup/1","version":"<server version>","createdAt":"<RFC 3339 UTC>","storage":"sqlite"\|"postgres", …}` plus `eventSegments` (SQLite) or `schemaVersion` + a note (PostgreSQL) |

**Not** included — back these up separately:

- `secret.key` — without it every sealed value (secrets, AI provider keys, MQTT/IMAP passwords) is unrecoverable. See [Secrets](/docs/administration/secrets/).
- `config.yaml` and your TLS files.
- The relational data on PostgreSQL (use `pg_dump`/PITR; the manifest records the `schemaVersion` so you can validate a restore).
- `<dataDir>/artifacts/` (the doc comment mentions artefacts, the code does not copy them).

:::caution[No periodic backup loop]
`backup.interval` is parsed but not used: the server never runs backups on its own, and the production Proxmox host has no vzdump job either. Schedule `northplaned backup` with cron/systemd timers and ship the resulting directory off-host. A minimal cron line:

```text
15 2 * * * northplane NORTHPLANE_BACKUP_TARGET=/var/backups/northplane /usr/local/bin/northplaned backup -config /etc/northplane/config.yaml
```
:::

In the container (distroless, no shell) run the binary directly with the same data volume and an extra environment variable:

```bash
docker compose exec -e NORTHPLANE_BACKUP_TARGET=/var/lib/northplane/backups northplane northplaned backup
```

and copy `/var/lib/northplane/backups/…` out of the volume afterwards. The alternative is a volume-level snapshot; with SQLite in WAL mode, stop the container first or accept a crash-consistent copy.

### Restore

There is no restore command; a restore is a file operation:

1. Stop `northplaned`.
2. Put `core.db` and the `events-*.db` segments from the backup into `dataDir` (remove stale `core.db-wal`/`core.db-shm` files left by the stopped instance). On PostgreSQL restore the database with your tooling, then check that its `schema_version` matches the manifest's `schemaVersion`.
3. Replace `<dataDir>/tsdb/` with the `tsdb/` tree from the backup.
4. Make sure the **same** `secret.key` is in place (config `secretKeyFile` or `<dataDir>/secret.key`), otherwise sealed values fail to decrypt.
5. Start the server (or run `northplaned migrate` first if the backup is from an older version — migrations are applied automatically either way) and verify with `/readyz`, the Objects page and `np audit verify`.

## Sizing notes

- SQLite runs the reference production instance (agent fleet, SNMP polling, traps, alarming pipelines) on a 4 vCPU / 8 GB VM; the shipped defaults are tuned for it (pool warm and non-expiring, 250 ms pipeline flushes, `busy_timeout` 5 s).
- Event volume is the main disk driver on the relational side: one row per state change, notification, ingress event, ack, config change … per month file. Lower `storage.eventRetentionMonths` if disk is tight; the NDJSON export (`GET /api/v1/events:export`) lets you archive before dropping.
- TSDB growth is bounded by retention and the 100 000-series cap; each check result appends one sample per perfdata label plus `np_exec_time`. Raw blocks are compact (Gorilla encoding) and downsampled tiers are small.
- The outbox and escalation tables are small and self-cleaning; the audit log grows forever (plan for it, or export and truncate manually — there is no built-in purge).
- Keep `dataDir` on local or low-latency storage: SQLite WAL and the TSDB WAL fsync frequently.
