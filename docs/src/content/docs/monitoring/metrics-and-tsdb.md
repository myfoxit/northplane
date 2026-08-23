---
title: Metrics and NP-TSDB
description: What Northplane stores as time series, how the embedded NP-TSDB keeps and downsamples it, the metrics query API, charts in the UI, and the Prometheus /metrics endpoint.
sidebar:
  order: 8
---

Every check result that carries Nagios-style performance data becomes one or more time series in **NP-TSDB**, the time-series store embedded in `northplaned`. There is nothing to install or configure: series are created on first sight, kept for 30 days at full resolution, downsampled to 5-minute and 1-hour tiers, and served through one query endpoint that the UI charts, the dashboards, the AI tools and your own scripts all use.

This page covers what is stored, how it is stored, how to query it, and what the separate Prometheus-style `/metrics` endpoint does (and does not) export.

## What is stored

After each result the pipeline parses the perfdata part of the plugin output (`'label'=value[UOM];warn;crit;min;max`, see [Plugins and Nagios compatibility](/docs/monitoring/plugins-and-nagios/)) and appends **one sample per perfdata token**:

| Series attribute | Value |
|---|---|
| object | the host or service id the result belongs to |
| metric | the perfdata label (e.g. `time`, `rta`, `disk:/`, `load1`) |
| unit | the **normalised** unit: `us`/`ms`/`s` → `s`, `B`/`KB`/`MB`/`GB`/`TB` → `bytes` (KB = 1024 B), `%` kept, `c` = counter, anything else passed through |
| value | the normalised value (`12ms` → `0.012`, `4MB` → `4194304`) |
| warn / crit / min / max | threshold metadata from the token, stored **as written** by the plugin and updated on every result |

Additionally the pipeline appends `np_exec_time` (seconds) for every result with a non-zero execution time. The UI hides all `np_*` metrics from charts.

Series exist for every source of results: builtin checks, `exec:` plugins, np-agent collectors, passive results over `POST /api/v1/results`. Objects without perfdata (plain text output) produce no series.

:::caution[Thresholds are not normalised]
Values are converted to base units, but the `warn`/`crit` range strings are stored verbatim. A plugin that reports `rta=12.5ms;100;200` yields a series in seconds (value `0.0125`) whose stored thresholds still read `100`/`200`. The builtin `icmp`/`ping` checks emit `rta` in `ms`, so this affects them. Charts, gauges and stat widgets derive their bands and colours from these strings; when a metric is reported in `ms`, `KB`, `MB` or `GB`, set explicit `warn`/`crit` overrides on the widget (see [Dashboards](/docs/monitoring/dashboards/)). Metrics reported in `s`, `bytes`, `%` or without unit are unaffected.
:::

:::note
Series labels are always empty in the current version (the series key is object + metric + unit). `GET /api/v1/objects/{id}/metrics` returns `labels` only for completeness.
:::

## How NP-TSDB stores data

NP-TSDB lives in `<dataDir>/tsdb` (see [Storage](/docs/administration/storage/) for the data directory). It is independent of the relational backend: switching SQLite ⇄ PostgreSQL with `northplaned storage migrate` leaves it untouched, and `northplaned backup` copies the whole tree.

| File | Content |
|---|---|
| `series.jsonl` | append-only registry of series metadata (`id, objectId, metric, unit, labels, warn, crit, min, max`); later lines update thresholds in place, `deleted: true` lines are tombstones |
| `wal.log` | write-ahead log of the open (head) windows — 25-byte records, fsync batched every 1 s, replayed on start, rewritten after each flush |
| `blocks/block-<ms>.npb` | immutable raw blocks, one per **2-hour window**, Gorilla-compressed (delta-of-delta timestamps + XOR floats) |
| `agg/agg-<ms>-5m.npa`, `agg/agg-<ms>-1h.npa` | downsampled tiers, one file per UTC day and tier; each bucket keeps `count, sum, min, max` |

Geometry and limits:

| Item | Value | Configurable |
|---|---|---|
| Raw window | 2 h; a window is flushed to a block once it has been closed for 5 min | no |
| Tiers | 5-minute and 1-hour buckets, built nightly from raw blocks for fully elapsed days | no |
| Retention | raw **30 days**, 5-min tier **400 days**, 1-h tier **5 years** (whole files are deleted by window start) | no (hard-coded defaults) |
| Cardinality cap | 100 000 series per instance; new series beyond the cap are dropped and counted in `seriesDropped` | no |
| Rejected samples | NaN/Inf values, a timestamp not newer than the series' last sample, a timestamp that falls into an already flushed window | — |
| Maintenance | the janitor runs flush + downsample + retention once per night between 02:00 and 03:59 local time (at most once per 20 h); every other hour it only flushes closed windows | no |

All files are written as temp file + rename + directory fsync, so a crash never leaves a half-written block; the WAL covers the open windows.

:::note[Series of deleted objects]
Deleting a host or service does not remove its series from the registry or its blocks from disk; they age out with the retention tiers and count toward the cardinality cap until then. The registry grows with every distinct (object, metric, unit) that ever reported.
:::

Live statistics — `series, samplesIngested, samplesDropped, seriesDropped, blocks, walBytes` — are shown in the `tsdb` block of `GET /api/v1/system/health` and in **Admin → System health**, and three of them are exported on `/metrics` (`np_tsdb_series`, `np_tsdb_samples_total`, `np_tsdb_wal_bytes`). See [Observability](/docs/administration/observability/).

## Listing the series of an object

`GET /api/v1/objects/{id}/metrics` (permission `metrics:read`) returns the series metadata for one object — this is what the metric pickers in the UI use:

```bash
NP=https://np.example.com
TOK=np_…
curl -s "$NP/api/v1/objects/$OBJECT_ID/metrics" -H "Authorization: Bearer $TOK"
```

```json
[
  {"id": 17, "objectId": "0198…", "metric": "time", "unit": "s", "warn": "1", "crit": "3"},
  {"id": 18, "objectId": "0198…", "metric": "size", "unit": "bytes", "min": 0},
  {"id": 19, "objectId": "0198…", "metric": "np_exec_time", "unit": "s"}
]
```

Objects of another tenant return `404`. Reference page: [get_objects_id_metrics](/docs/reference/api/operations/get_objects_id_metrics/).

## Querying time series

`POST /api/v1/metrics/query` (permission `metrics:read`) returns render-ready, server-side downsampled series.

| Field | Type | Meaning |
|---|---|---|
| `objectId` | string | one target object |
| `objectIds` | string[] | several target objects |
| `selector` | string | a [label selector](/docs/concepts/object-model/) resolved against the catalog; all matching objects become targets |
| `metric` | string | metric name; empty = all metrics of the target objects |
| `from`, `to` | RFC 3339 | time range; defaults `to = now`, `from = to − 24h`; `from` must be before `to` |
| `stepSeconds` | int | explicit bucket width; default derived as `range / maxPoints`, minimum 1 s |
| `maxPoints` | int | target number of points per series; default 500, maximum 10 000 (values outside fall back to 500) |
| `agg` | string | bucket aggregation: `avg` (default), `min`, `max`, `sum`, `last`, `count` |

Rules:

- At least one of `objectId`, `objectIds`, `selector` is required (`422 np:validation/target`); an unparseable selector is `422 np:validation/selector`.
- At most **100 objects** per query; ids beyond that are dropped silently, as are ids that belong to another tenant.
- A query must not produce more than 20 000 buckets per series (`tsdb: too many buckets`) — widen `stepSeconds` or lower `maxPoints` for long ranges.
- Tier selection is automatic per sub-range: raw blocks and the open windows where they exist, else the 5-minute tier, else the 1-hour tier ("finest tier with data wins"). Aggregate tiers carry `count/sum/min/max`, so `last` on old data is approximated by the bucket average.

Response: one entry per series, sorted by object id and metric, each with the series metadata and `points` as `{t: <unix ms>, v: <float>}`:

```bash
curl -s -X POST "$NP/api/v1/metrics/query" -H "Authorization: Bearer $TOK" \
  -H 'Content-Type: application/json' \
  -d '{"selector":"role=web,env=prod","metric":"time","from":"2026-08-22T00:00:00Z","to":"2026-08-23T00:00:00Z","maxPoints":288,"agg":"max"}'
```

```json
[
  {"series": {"id": 17, "objectId": "0198…", "metric": "time", "unit": "s", "warn": "1", "crit": "3"},
   "points": [{"t": 1755820800000, "v": 0.231}, {"t": 1755821100000, "v": 0.187}]}
]
```

Reference page: [post_metrics_query](/docs/reference/api/operations/post_metrics_query/). The same endpoint is what the dashboards' `metric`, `stat`, `gauge` and `bar` widgets and the object detail charts call.

The AI tools `query_metrics`, `analyze_metric`, `forecast_capacity` and `suggest_thresholds` (see [AI agent chat](/docs/ai/agent-chat/) and [MCP server](/docs/ai/mcp-server/)) read the same store; they need `metrics:read` as well.

## Charts in the UI

**Object detail** (**Objects (Objekte) → object → Overview**):

- **Perfdata meters** — the latest perfdata tokens rendered as bars with amber warn and red crit markers (`label=value;warn;crit;min;max`); counters without bounds are shown as plain values.
- Card **Metrics (Metriken)** — one chart per unit group for the last 24 h (`maxPoints: 300`, refreshed every 60 s). Series that share a unit are overlaid on one y-axis; a monotonically increasing series (≥ 4 points, never decreasing) is treated as a counter and drawn as a per-second rate with the unit suffixed `/s`. `np_*` series are hidden.
- Threshold **bands**: translucent fill above the warn (amber) and crit (red) value of the first series, taken from the stored Nagios range (the upper bound of `start:end`, `@` stripped).

Dashboards provide the same chart plus gauges, stats and bars with selectable ranges — see [Dashboards](/docs/monitoring/dashboards/). The UI has no ad-hoc metric explorer; build a dashboard or call the query API.

## Prometheus-style `/metrics`

`GET /metrics` on the server port serves OpenMetrics text (`application/openmetrics-text; version=1.0.0`, terminated by `# EOF`). It is **unauthenticated by design** and meant to be restricted at the network or proxy layer (see [TLS and reverse proxy](/docs/administration/tls-and-proxy/)).

:::caution[Self-metrics only]
`/metrics` exports the health of the Northplane process. It does **not** export the perfdata of monitored objects — there is no per-object Prometheus exporter. To get monitored metrics out, use `POST /api/v1/metrics/query`.
:::

| Family | Type | Labels | Meaning |
|---|---|---|---|
| `np_http_requests_total` | counter | `method`, `status`, `route` | API requests (route = matched mux pattern) |
| `np_http_request_duration_seconds` | histogram | `method`, `status`, `route` | request latency, Prometheus default buckets `0.005…10` |
| `np_queue_results_depth`, `np_queue_events_depth`, `np_queue_notifications_depth` | gauge | — | internal queue depths |
| `np_sse_clients` | gauge | — | connected SSE stream clients |
| `np_scheduler_objects`, `np_scheduler_lag_ms_max` | gauge | — | scheduled objects, worst dispatch lag since last scrape |
| `np_checks_dispatched_total`, `np_results_processed_total` | gauge* | — | lifetime counters of the scheduler / pipeline |
| `np_alert_rules`, `np_alerts_opened_total` | gauge* | — | compiled rules, alerts opened |
| `np_notifications_total` | gauge* | `result` = `sent`/`failed`/`dead` | notification outcomes |
| `np_events_dropped_total` | counter / gauge* | `source` = `api`/`notify` | events that could not be persisted |
| `np_ingress_events_total` | counter | `type` = `webhook`/`alertmanager`/`sms` | accepted ingest events |
| `np_ingress_dropped_total` | counter | `reason` = `rate` | ingest events rejected by rate limiting |
| `np_tsdb_series`, `np_tsdb_samples_total`, `np_tsdb_wal_bytes` | gauge* | — | NP-TSDB series count, ingested samples, WAL size |
| `np_catalog_objects` | gauge | — | objects in the in-memory catalog |

\* Scrape-time values read from the subsystems; they are rendered with `# TYPE … gauge` even when the name ends in `_total`. Use `max_over_time`/`delta` rather than `rate()` on those in PromQL. A full description lives in [Observability](/docs/administration/observability/).

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| Object shows output but the **Metrics** card is empty | the plugin emits no perfdata after `\|`, or the tokens are malformed (they are skipped with a warning, never an error). Test with `POST /api/v1/check-commands:test` and look at `perfdata`. |
| Values look 1000× too small | `ms` → `s` and `KB`/`MB` → `bytes` normalisation; the unit in the series metadata tells you the stored unit. |
| Bands/colours in the wrong place | thresholds stored in the plugin's original unit (see the caution above); set widget overrides. |
| `tsdb: too many buckets` | range ÷ step exceeds 20 000; raise `stepSeconds` or lower `maxPoints`. |
| `seriesDropped` grows | the 100 000-series cap is reached — typically a plugin that encodes a changing value in the label name. |
| Old data coarser than expected | queries older than 30 days come from the 5-minute tier, older than 400 days from the 1-hour tier. |
