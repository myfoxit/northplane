// Multi-series helpers for the chart layer: align N time-series onto one shared
// x-axis (so they overlay in a single uPlot), group by unit, and assign stable
// per-series colours/labels. Pure (no JSX/DOM) so it is unit-tested directly.
import type { SeriesResult } from '../../types'

// Categorical palette for overlaid series (Grafana-like). Indexed deterministi-
// cally so a series keeps its colour across refreshes.
export const SERIES_PALETTE = [
  '#60a5fa', // blue-400
  '#34d399', // emerald-400
  '#fbbf24', // amber-400
  '#f87171', // red-400
  '#a78bfa', // violet-400
  '#22d3ee', // cyan-400
  '#f472b6', // pink-400
  '#a3e635', // lime-400
]

export function seriesColor(i: number): string {
  const n = SERIES_PALETTE.length
  return SERIES_PALETTE[((i % n) + n) % n]!
}

// shortId is a compact, stable suffix of an object id, used to disambiguate
// overlaid series that share a metric name (e.g. one metric across many hosts).
export function shortId(objectId: string): string {
  return objectId.length > 6 ? objectId.slice(-6) : objectId
}

// AlignedSeries is one overlay-ready line; values line up 1:1 with the shared x
// array (null = the series has no sample at that timestamp).
export interface AlignedSeries {
  label: string
  unit?: string
  warn?: string
  crit?: string
  color: string
  values: (number | null)[]
}

export interface Aligned {
  x: number[] // epoch SECONDS, ascending, deduped union across all series
  series: AlignedSeries[]
}

// alignSeries unites the timestamps of every input series onto one sorted x axis
// and maps each series' values onto it (null where a series has no sample in
// that bucket). Labels are disambiguated: the metric name when it is unique
// within the set, otherwise "metric·<shortId>" so overlaid same-metric lines
// (one per object) stay distinguishable. Series with no points are dropped.
export function alignSeries(results: SeriesResult[]): Aligned {
  const withPoints = results.filter((r) => r.points && r.points.length > 0)

  // union of timestamps (ms), deduped + ascending → x in seconds for uPlot.
  const tset = new Set<number>()
  for (const r of withPoints) for (const p of r.points) tset.add(p.t)
  const xms = [...tset].sort((a, b) => a - b)
  const x = xms.map((t) => t / 1000)

  // is each metric name unique across the set? (drives label disambiguation)
  const metricCounts = new Map<string, number>()
  for (const r of withPoints) {
    metricCounts.set(r.series.metric, (metricCounts.get(r.series.metric) ?? 0) + 1)
  }

  const series: AlignedSeries[] = withPoints.map((r, i) => {
    const byT = new Map<number, number>()
    for (const p of r.points) byT.set(p.t, p.v)
    const base = r.series.metric || shortId(r.series.objectId)
    const ambiguous = (metricCounts.get(r.series.metric) ?? 0) > 1
    return {
      label: ambiguous ? `${base}·${shortId(r.series.objectId)}` : base,
      unit: r.series.unit,
      warn: r.series.warn,
      crit: r.series.crit,
      color: seriesColor(i),
      values: xms.map((t) => (byT.has(t) ? byT.get(t)! : null)),
    }
  })
  return { x, series }
}

// groupByUnit splits series so only same-unit series share an axis (mixing units
// on one y-axis is misleading). Insertion order is preserved; series without a
// unit group together under "".
export function groupByUnit(results: SeriesResult[]): SeriesResult[][] {
  const order: string[] = []
  const groups = new Map<string, SeriesResult[]>()
  for (const r of results) {
    const u = r.series.unit ?? ''
    if (!groups.has(u)) {
      groups.set(u, [])
      order.push(u)
    }
    groups.get(u)!.push(r)
  }
  return order.map((u) => groups.get(u)!)
}

// nagiosRangeStart returns the numeric threshold for a chart's warn/crit band:
// the upper bound when the spec is a range (after stripping a leading "@"), e.g.
// "10:20" or "@10:20" → 20; otherwise the bare number, e.g. "80" → 80. Returns
// null when the spec is absent or has no parsable value (e.g. "80:" → null).
// (Behaviour preserved verbatim from the original chart/widget helpers.)
export function nagiosRangeStart(spec?: string): number | null {
  if (!spec) return null
  const body = spec.startsWith('@') ? spec.slice(1) : spec
  const parts = body.split(':')
  const start = (body.includes(':') ? parts[1] : parts[0]) ?? parts[0] ?? ''
  const v = parseFloat(start)
  return Number.isFinite(v) ? v : null
}

// effectiveBand resolves the warn/crit thresholds for a panel: an explicit,
// user-/API-/AI-set numeric override wins; otherwise fall back to the metric's
// own Nagios perfdata range. Either side may be null (no threshold).
export function effectiveBand(
  widgetWarn: number | undefined, widgetCrit: number | undefined,
  seriesWarn?: string, seriesCrit?: string,
): { warn: number | null; crit: number | null } {
  return {
    warn: widgetWarn ?? nagiosRangeStart(seriesWarn),
    crit: widgetCrit ?? nagiosRangeStart(seriesCrit),
  }
}

// thresholdTone colours a value green/amber/red against warn/crit (crit wins).
export function thresholdTone(v: number, warn: number | null, crit: number | null): string {
  if (crit !== null && v >= crit) return '#f87171' // red-400
  if (warn !== null && v >= warn) return '#fbbf24' // amber-400
  return '#34d399' // emerald-400
}

// fmtMetric humanises a metric value for a compact label: SI suffixes k/M/G/T
// so a raw SNMP counter like 18490823 reads "18.5M" instead of a wall of
// digits (DASH-2). The unit is rendered separately by the caller. Trailing
// zeros are dropped; sub-unit values keep two significant digits.
export function fmtMetric(v: number): string {
  if (!Number.isFinite(v)) return '—'
  const abs = Math.abs(v)
  const scale = (f: number, suf: string) => {
    const n = v / f
    const digits = Math.abs(n) >= 100 ? 0 : Math.abs(n) >= 10 ? 1 : 2
    return `${Number(n.toFixed(digits))}${suf}`
  }
  if (abs >= 1e12) return scale(1e12, 'T')
  if (abs >= 1e9) return scale(1e9, 'G')
  if (abs >= 1e6) return scale(1e6, 'M')
  if (abs >= 1e3) return scale(1e3, 'k')
  if (abs >= 100 || Number.isInteger(v)) return String(Math.round(v))
  if (abs >= 1) return String(Number(v.toFixed(2)))
  if (abs === 0) return '0'
  return String(Number(v.toPrecision(2)))
}
