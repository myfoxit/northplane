---
title: np CLI
description: The np command-line client — global flags and environment, every command with the REST call and permission behind it, output formats, exit codes and known quirks.
sidebar:
  order: 2
---

`np` is a thin client of the public REST API: everything it does, any API client can do. It is a single static binary (stdlib only), shipped in every release tarball, in the Windows zip and in the Docker image next to `northplaned`. It reads no config file — the server URL and token come from flags or environment variables.

```bash
export NP_SERVER=https://northplane.example.net
export NP_TOKEN=np_…                      # e.g. from `northplaned bootstrap-admin` or Admin → API tokens
np get hosts
```

## Usage

```text
np — Northplane CLI (1.0.0-dev)

Usage: np [--server URL] [--token np_…] [--json] [--insecure] <command>

Commands:
  get hosts|services|problems|alerts|incidents|silences|downtimes|events
  describe <object-id|host-name>           object detail + effective config
  apply -f bundle.yaml [--prune] [--dry-run]
  export [> bundle.yaml]                   canonical config bundle
  ack <alert-id> [-m comment]              acknowledge (stops escalation)
  resolve <alert-id>
  downtime --selector 'k=v'|--object <id> --hours 2 -m comment
  silence  --selector 'k=v' --hours 2 -m comment
  check-now <object-id>
  oncall                                   who is on call
  audit verify | audit tail
  doctor                                   server health summary

Environment: NP_SERVER, NP_TOKEN
```

`np` with no arguments prints this text and exits 0. `np help` and `np -h` print it too — but see the `--help` quirk below.

## Global flags and environment

Global flags must come **before** the command. Both `--flag value` and `--flag=value` are accepted.

| Flag | Env var | Default | Meaning |
|---|---|---|---|
| `--server URL` | `NP_SERVER` | `https://localhost:8443` | base URL of the instance; a trailing `/` is trimmed |
| `--token np_…` | `NP_TOKEN` | empty | API token, sent as `Authorization: Bearer …` when non-empty |
| `--json` | — | off | print the server's JSON (pretty) instead of a table, for the commands that render tables |
| `--insecure` | — | off | skip TLS certificate verification |
| `--version` | — | — | print `np <version>` and exit 0 |

:::note[Default server vs. a dev instance]
The default `NP_SERVER` is `https://localhost:8443`, but a default `northplaned` listens in **plaintext** on `127.0.0.1:8443`. Against a local dev server use `np --server http://127.0.0.1:8443 …`. The Docker/Compose stacks terminate TLS in Caddy, so there `https://<domain>` is right.
:::

The HTTP client has a 60 s timeout and reads at most 16 MiB of a response.

## Commands

Every command maps to one or two REST calls. The permission column is what the token must carry (scopes or via roles); see [API tokens](/docs/administration/api-tokens/) and [Users, roles and permissions](/docs/administration/users-roles-permissions/).

| Command | Arguments / flags | REST call | Permission | Output |
|---|---|---|---|---|
| `get hosts` | — | `GET /api/v1/hosts?limit=500` | `objects:read` | table `STATE NAME HOST LABELS` |
| `get services` | — | `GET /api/v1/services?limit=500` | `objects:read` | table `STATE NAME HOST LABELS` |
| `get problems` | — | `GET /api/v1/problems` | `objects:read` | table `STATE OBJECT OUTPUT` (output cut at 80 chars, newlines → spaces) |
| `get alerts` | — | `GET /api/v1/alerts?status=open,acked` | `alerts:read` | table `SEVERITY STATUS TITLE ID` (title cut at 60) |
| `get incidents` | — | `GET /api/v1/incidents?open=true` | `incidents:read` | table `SEVERITY STATUS TITLE ID` |
| `get silences` | — | `GET /api/v1/silences?active=true` | `objects:read` | pretty JSON `{"items":[…]}` (always) |
| `get downtimes` | — | `GET /api/v1/downtimes?active=true` | `objects:read` | pretty JSON `{"items":[…]}` (always) |
| `get events` | — | `GET /api/v1/events?limit=50` | `events:read` | table `TIME TYPE SEVERITY PAYLOAD` (payload JSON cut at 90) |
| `get <anything else>` | — | — | — | `np: unknown resource "x"`, exit 1 |
| `describe <object-id>` | object **id** | `GET /api/v1/objects/{id}`, then `GET /api/v1/objects/{id}/effective-config` | `objects:read` | pretty JSON of the object with live `state`, then `--- effective config (templates resolved) ---` and the resolved spec + template chain (errors of the second call are ignored) |
| `apply -f <file\|-> [--prune] [--dry-run]` | `-f`/`--file` (`-` = stdin), `--prune`, `--dry-run` | `POST /api/v1/config/bundles:apply` (`?dryRun=true`, `?prune=true`), body `application/yaml` | `config:write` (also for `--dry-run`) | one line per plan item: `applied` / `would apply` `<action> <Kind>/<[host/]name>`; `no changes`; `warning: …` lines; raw JSON with `--json` |
| `export` | — | `GET /api/v1/config/bundles:export` | `objects:read` | the YAML bundle on stdout |
| `ack <alert-id> [-m comment]` | `-m` | `POST /api/v1/alerts/{id}:ack` `{"comment":"…"}` | `alerts:ack` | `acknowledged: <alert title>` |
| `resolve <alert-id>` | — | `POST /api/v1/alerts/{id}:resolve` | `alerts:ack` | `resolved` |
| `downtime` | `--selector 'k=v'` or `--object <id>` (one required), `--hours N` (float, default 2), `-m comment` (required) | `POST /api/v1/downtimes` with `type: fixed`, `start` = now (UTC), `end` = now + N h | `downtimes:write` | `downtime <id> until <RFC 3339 end>` |
| `silence` | `--selector 'k=v'` (required), `--hours N` (default 2), `-m comment` (required) | `POST /api/v1/silences` with `expiresAt` = now + N h | `silences:write` | `silence <id> for <N>h` |
| `check-now <object-id>` | object id | `POST /api/v1/objects/{id}/check-now` | `checks:run` | `recheck queued` |
| `oncall` | — | `GET /api/v1/oncall/now` | `oncall:read` | table `SCHEDULE ON CALL CONTACT` (contact = email, plus ` / phone` if set); one row per contact per schedule |
| `audit verify` | — | `POST /api/v1/audit:verify` | `admin:audit` | `audit chain intact (<n> entries verified)`, or error `AUDIT CHAIN BROKEN after <n> entries: <err>` with exit 1 |
| `audit` / `audit tail` | any other word | `GET /api/v1/audit?limit=30` | `admin:audit` | table `TIME ACTOR ACTION RESOURCE` (actor `type:id`, id cut at 12, resource at 36) |
| `doctor` | — | `GET /api/v1/system/info`, `GET /api/v1/system/health` | none (both routes are anonymous) | `--- system/info ---` + JSON, `--- system/health ---` + JSON; unreachable → `np: server unreachable: <err>` |
| `help`, `-h` | — | — | — | usage text |

Sub-command flags (`-f`, `-m`, `--selector`, `--object`, `--hours`, `--prune`, `--dry-run`) are found anywhere after the command and take their value from the **next** argument (`-m "kernel upgrade"`); the `--flag=value` form is only understood by the global flags.

State columns: hosts show `UP`/`DOWN`/`UNREACH`/`UNKN`, services `OK`/`WARN`/`CRIT`/`UNKN`, and `PEND` when no result exists yet; ` (ack)` and ` (downtime)` are appended when the object is acknowledged or in downtime. `LABELS` is `k=v,k2=v2`, sorted by key.

The operation pages of the generated REST reference describe each call in detail, e.g. [get_hosts](/docs/reference/api/operations/get_hosts/), [post_config_bundles_apply](/docs/reference/api/operations/post_config_bundles_apply/), [post_alerts_id_ack](/docs/reference/api/operations/post_alerts_id_ack/), [post_downtimes](/docs/reference/api/operations/post_downtimes/), [post_silences](/docs/reference/api/operations/post_silences/), [post_objects_id_check_now](/docs/reference/api/operations/post_objects_id_check_now/), [get_oncall_now](/docs/reference/api/operations/get_oncall_now/), [post_audit_verify](/docs/reference/api/operations/post_audit_verify/).

## Output modes

- Tables are written with Go's `text/tabwriter` (space-padded columns).
- `--json` switches `get hosts|services|problems|alerts|incidents|events`, `oncall`, `audit tail` to the raw pretty-printed server response, and makes `apply` print the raw plan JSON.
- `describe`, `export`, `get silences`, `get downtimes` and `doctor` always print raw server output (JSON or YAML) — `--json` has no effect on them.
- `ack`, `resolve`, `downtime`, `silence`, `check-now` print one plain line.
- Write errors on stdout (for example a closed pipe when piping into `head`) are reported as errors.

## Errors and exit codes

| Exit | When | stderr |
|---|---|---|
| 0 | success; usage/help/version | — |
| 1 | any command error, including `audit verify` on a broken chain and `doctor` when the server is unreachable | `np: <message>` |
| 2 | unknown global flag, global flag without value, unknown command | `unknown flag "--x"` / `np: --server requires a value` / `np: unknown command "x"` + usage |

Server errors arrive as RFC 9457 problem documents and are rendered as `np: <title> (<code>) <detail>`, for example `np: missing permission (np:auth/forbidden) objects:read`. Non-problem responses are rendered as `np: HTTP <status>: <first line of body>`. The error codes are catalogued in [API overview](/docs/reference/api-overview/#error-catalog).

## Examples

```bash
export NP_SERVER=https://northplane.example.net NP_TOKEN=np_…

np get hosts
# STATE  NAME    HOST  LABELS
# UP     web-01        env=prod,role=web
# DOWN (ack)  db-01    env=prod

np --json get services | jq '.items[] | select(.state.state > 0) | .name'

np get problems
# STATE  OBJECT  OUTPUT
# CRIT   disk /  / 96.1% used, 3.2 GB free

np describe 0199a4c2-…                    # object JSON, then "--- effective config (templates resolved) ---"

np apply -f bundle.yaml --dry-run
# would apply create Host/db-02
# would apply update Service/db-02/disk
np apply -f bundle.yaml
np apply -f bundle.yaml --prune           # also delete objects not in the bundle
cat bundle.yaml | np apply -f -           # from stdin
np export > bundle.yaml

np ack 0199a4c2-… -m "working on it"     # acknowledged: disk / CRITICAL on db-01
np resolve 0199a4c2-…                     # resolved
np downtime --object 0199a4c2-… --hours 4 -m "kernel upgrade"   # downtime 0199… until 2026-08-23T14:00:00Z
np downtime --selector 'env=staging' --hours 1 -m "deploy"
np silence --selector 'role=db' --hours 0.5 -m "failover test"  # silence 0199… for 0.5h
np check-now 0199a4c2-…                   # recheck queued

np oncall
# SCHEDULE  ON CALL      CONTACT
# primary   Jane Doe     jane@example.net / +43…

np audit verify                           # audit chain intact (1234 entries verified)
np audit tail
np doctor
np --server http://127.0.0.1:8443 doctor  # local dev server (plaintext loopback)
```

Table values above are illustrative; the column headers and line formats are exact.

## Quirks and gotchas

| Observation | Consequence |
|---|---|
| `np --help` is parsed as an unknown **global flag** and exits 2 | use `np help` or `np -h` |
| Global flags after the command are not parsed: `np get services --json` silently prints the table | put `--json`, `--server`, `--token`, `--insecure` before the command |
| Usage says `describe <object-id\|host-name>`, but the server route resolves by **id** only; a host name yields `404 resource not found` | get the id from `np --json get hosts` or the UI |
| `--hours` is parsed with `%f`; an unparsable value silently keeps 2.0 | check the printed `until`/`for` value |
| `apply --dry-run` calls `:apply?dryRun=true`, which needs `config:write` — the read-only `:plan` endpoint is not used | give CI tokens `config:write` even for plan-only jobs, or call [post_config_bundles_plan](/docs/reference/api/operations/post_config_bundles_plan/) directly |
| No pagination: `get hosts`/`get services` fetch at most 500, `get events` 50, `audit tail` 30, `get alerts` the server default of 100 | use the API with `cursor` for larger listings |
| `describe`, `export`, `get silences`/`downtimes`, `doctor` ignore `--json` | they are already raw |
| `downtime`/`silence` do not send `Idempotency-Key` or use `applyToken` | re-running creates a second downtime/silence |
| `NP_TOKEN` empty → requests are anonymous | only `doctor` works; everything else returns `np: authentication required (np:auth/required)` |
