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
