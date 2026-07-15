// Dashboard widget renderers (CMP Visualization-Dashboard parity, SPEC §12.3).
// Each renderer is a self-contained query so it can auto-refresh independently.
// The dashboard-level time range + refresh interval come from DashViewCtx
// (Grafana's top-right time picker / refresh); a widget's own range is the
// fallback. The grid placement + edit chrome live in Dashboards.tsx; this file
// is the read-only "what a widget shows" layer.
import { type ReactNode, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Check } from 'lucide-react'
import { get, post, fmtAgo, type ListResponse } from '../../api'
import type {
  Overview, ProblemRow, Alert, SeriesResult, DashboardWidget, NPObject, ObjectsSearch,
} from '../../types'
import { stateIcon, stateColor, stateLabel, sevColor } from '../../types'
import { Tile, Empty, Spinner } from '@/components/kit'
import { Badge } from '@/components/ui/badge'
import { Chart } from '../Chart'
import { t } from '../../i18n'
import { type BSNode, bsStateMeta, rangeFrom } from './util'
import { groupByUnit, nagiosRangeStart, thresholdTone, fmtMetric } from './series'
import { useDashView } from './ctx'

// TileLink: a KPI tile that drills down into the matching filtered list
// — every dashboard number is clickable (SPEC §12.3 drill-down).
export function TileLink({ to, search, label, value, tone }: {
  to: '/objects' | '/alerts'
  search?: ObjectsSearch
  label: string; value: ReactNode; tone?: 'default' | 'ok' | 'warn' | 'crit'
}) {
  return (
    <Link to={to} search={search ?? {}} className="block hover:opacity-80 transition-opacity">
      <Tile label={label} value={value} tone={tone} />
    </Link>
  )
}

// BpiTreeLines: recursive BPI tree as state-dot lines.
export function BpiTreeLines({ nodes, depth = 0 }: { nodes: BSNode[]; depth?: number }) {
  return (
    <>
      {nodes.map((n) => {
        const m = bsStateMeta(n.state)
        return (
          <div key={n.service.id}>
            <div className="flex items-center gap-2 py-1 text-sm" style={{ paddingLeft: depth * 14 }}>
              <span className={`${m.color} font-bold w-4 text-center shrink-0`}>{m.icon}</span>
              <span className="text-foreground truncate">{n.service.name}</span>
              {typeof n.service.slaTarget === 'number' && n.service.slaTarget > 0 && (
                <span className="text-muted-foreground/70 text-xs">SLA {n.service.slaTarget}%</span>
              )}
              {n.causes && n.causes.length > 0 && depth === 0 && (
                <span className="text-muted-foreground/70 text-xs truncate">↳ {n.causes.join(', ')}</span>
              )}
            </div>
            {n.children && n.children.length > 0 && <BpiTreeLines nodes={n.children} depth={depth + 1} />}
          </div>
        )
      })}
    </>
  )
}

// CountersWidget: KPI tiles from /overview — each tile links to the
// filtered objects/alerts list.
function CountersWidget() {
  const { refreshMs } = useDashView()
  const { data } = useQuery({
    queryKey: ['overview'],
    queryFn: () => get<Overview>('/overview'),
    refetchInterval: refreshMs || false,
  })
  const s = data?.summary
  const critAlerts = data?.openAlerts?.critical ?? 0
  const warnAlerts = data?.openAlerts?.warning ?? 0
  return (
    <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-3">
      <TileLink to="/objects" search={{ kind: 'host', state: 'up' }} label={t('hostsUp')} value={s?.hostsUp ?? '—'} tone="ok" />
      <TileLink to="/objects" search={{ kind: 'host', state: 'down' }} label={t('hostsDown')} value={s?.hostsDown ?? '—'} tone={s?.hostsDown ? 'crit' : 'default'} />
      <TileLink to="/objects" search={{ kind: 'service', state: 'ok' }} label={t('servicesOk')} value={s?.servicesOk ?? '—'} tone="ok" />
      <TileLink to="/objects" search={{ kind: 'service', state: 'warning' }} label={t('servicesWarning')} value={s?.servicesWarning ?? '—'} tone={s?.servicesWarning ? 'warn' : 'default'} />
      <TileLink to="/objects" search={{ kind: 'service', state: 'critical' }} label={t('servicesCritical')} value={s?.servicesCritical ?? '—'} tone={s?.servicesCritical ? 'crit' : 'default'} />
      <TileLink to="/alerts" label={t('openAlerts')} value={`${critAlerts + warnAlerts}`} tone={critAlerts ? 'crit' : warnAlerts ? 'warn' : 'default'} />
    </div>
  )
}

// ProblemsWidget: compact problem list (state icon, name, output, ago).
function ProblemsWidget({ widget }: { widget: DashboardWidget }) {
  const { refreshMs } = useDashView()
  const limit = widget.limit ?? 10
  const sel = widget.selector ? `&selector=${encodeURIComponent(widget.selector)}` : ''
  const { data, isLoading } = useQuery({
    queryKey: ['problems', 'widget', limit, widget.selector],
    queryFn: () => get<ListResponse<ProblemRow>>(`/problems?limit=${limit}${sel}`),
    refetchInterval: refreshMs || false,
  })
  const rows = data?.items ?? []
  if (isLoading) return <Spinner />
  if (rows.length === 0) return <div className="text-emerald-500/80 text-sm p-3 flex items-center gap-1.5"><Check size={14} /> {t('noProblems')}</div>
  return (
    <div className="divide-y divide-border/60">
      {rows.slice(0, limit).map((p) => (
        <Link
          key={p.object.id} to="/objects/$id" params={{ id: p.object.id }}
          className="flex items-center gap-2 py-1.5 px-1 hover:bg-card/60 rounded text-sm"
        >
          <span className={`${stateColor(p.object.kind, p.state.state)} font-bold w-6 text-center shrink-0`}>
            {stateIcon(p.object.kind, p.state.state)}
          </span>
          <span className="text-foreground font-medium truncate max-w-[40%]">
            {p.object.kind === 'service' && p.object.hostName ? `${p.object.hostName} / ` : ''}{p.object.name}
          </span>
          <span className="text-muted-foreground truncate flex-1">{p.state.output}</span>
          <span className="text-muted-foreground/70 text-xs tabular-nums shrink-0">{fmtAgo(p.state.lastHardChange)}</span>
        </Link>
      ))}
    </div>
  )
}

// AlertsWidget: open alerts with severity badges.
function AlertsWidget({ widget }: { widget: DashboardWidget }) {
  const { refreshMs } = useDashView()
  const limit = widget.limit ?? 10
  const { data, isLoading } = useQuery({
    queryKey: ['alerts', 'widget', limit],
    queryFn: () => get<ListResponse<Alert>>(`/alerts?status=open&limit=${limit}`),
    refetchInterval: refreshMs || false,
  })
  const rows = data?.items ?? []
  if (isLoading) return <Spinner />
  if (rows.length === 0) return <div className="text-emerald-500/80 text-sm p-3 flex items-center gap-1.5"><Check size={14} /> {t('empty')}</div>
  return (
    <div className="divide-y divide-border/60">
      {rows.slice(0, limit).map((a) => (
        <Link
          key={a.id} to="/alerts"
          className="flex items-center gap-2 py-1.5 px-1 hover:bg-card/60 rounded text-sm"
        >
          <Badge variant="outline" className={sevColor(a.severity)}>{a.severity}</Badge>
          <span className="text-foreground truncate flex-1">{a.title}</span>
          <span className="text-muted-foreground/70 text-xs tabular-nums shrink-0">{fmtAgo(a.openedAt)}</span>
        </Link>
      ))}
    </div>
  )
}

// MetricWidget: an overlaid time-series Chart from metrics/query. Targets either
// a single object or — via a selector — one metric across MANY objects, drawn as
// overlaid lines (grouped by unit so unlike units never share a y-axis). The
// dashboard-level range overrides the widget's own when set.
function MetricWidget({ widget }: { widget: DashboardWidget }) {
  const { refreshMs, range: rangeOverride } = useDashView()
  const range = rangeOverride ?? widget.range
  const { data, isLoading } = useQuery({
    queryKey: ['metrics', 'widget', widget.object, widget.selector, widget.metric, range],
    enabled: !!(widget.object || widget.selector),
    queryFn: () => post<SeriesResult[]>('/metrics/query', {
      objectId: widget.selector ? undefined : widget.object,
      selector: widget.selector || undefined,
      metric: widget.metric || undefined,
      from: rangeFrom(range),
      to: new Date().toISOString(),
      maxPoints: 300,
    }),
    refetchInterval: refreshMs || false,
  })
  // memoised so each unit-group keeps a stable identity between refetches and
  // the Chart effect doesn't tear down/rebuild uPlot on unrelated re-renders.
  const series = useMemo(
    () => (data ?? []).filter((s) => !s.series.metric.startsWith('np_')),
    [data],
  )
  const groups = useMemo(() => groupByUnit(series), [series])
  if (!widget.object && !widget.selector) return <Empty text={t('empty')} />
  if (isLoading) return <Spinner />
  if (series.length === 0) return <Empty text="keine Daten" />
  return (
    <div className="space-y-4">
      {groups.map((g, i) => <Chart key={i} results={g} warn={widget.warn} crit={widget.crit} height={140} />)}
    </div>
  )
}

// BpiWidget: a single business service subtree (by name) with live status.
function BpiWidget({ widget }: { widget: DashboardWidget }) {
  const { refreshMs } = useDashView()
  const { data, isLoading } = useQuery({
    queryKey: ['business-tree', 'widget'],
    queryFn: () => get<BSNode[]>('/business-services:tree'),
    refetchInterval: refreshMs || false,
  })
  if (isLoading) return <Spinner />
  const tree = data ?? []
  // Find the named root subtree; fall back to the whole forest.
  const find = (nodes: BSNode[]): BSNode | undefined => {
    for (const n of nodes) {
      if (n.service.name === widget.service) return n
      const c = n.children ? find(n.children) : undefined
      if (c) return c
    }
    return undefined
  }
  const sub = widget.service ? find(tree) : undefined
  const roots = sub ? [sub] : tree
  if (roots.length === 0) return <Empty text={t('empty')} />
  return <div>{<BpiTreeLines nodes={roots} />}</div>
}

// MarkdownWidget: plain whitespace-preserving text (NO raw HTML — XSS-safe).
function MarkdownWidget({ widget }: { widget: DashboardWidget }) {
  return (
    <div className="whitespace-pre-wrap text-sm text-foreground/90 leading-relaxed">
      {widget.text || ''}
    </div>
  )
}

// GaugeWidget: SVG arc gauge of a metric's latest value with warn/crit
// zones from perfdata thresholds (or the metric's own ranges).
function GaugeWidget({ widget }: { widget: DashboardWidget }) {
  const { refreshMs } = useDashView()
  const { data, isLoading } = useQuery({
    queryKey: ['metrics', 'gauge', widget.object, widget.metric],
    enabled: !!widget.object,
    queryFn: () => post<SeriesResult[]>('/metrics/query', {
      objectId: widget.object,
      metric: widget.metric || undefined,
      from: rangeFrom('1h'),
      to: new Date().toISOString(),
      maxPoints: 20,
    }),
    refetchInterval: refreshMs || false,
  })
  if (!widget.object) return <Empty text={t('empty')} />
  if (isLoading) return <Spinner />
  const s = (data ?? []).filter((r) => !r.series.metric.startsWith('np_') && r.points.length > 0)[0]
  const last = s?.points[s.points.length - 1]
  if (!s || !last) return <Empty text="keine Daten" />
  const value = last.v
  const warn = widget.warn ?? nagiosRangeStart(s.series.warn)
  const crit = widget.crit ?? nagiosRangeStart(s.series.crit)
  const max = widget.max || crit || Math.max(100, Math.ceil(value * 1.25))
  const frac = Math.min(1, Math.max(0, value / max))
  // 240°-arc geometry: angles measured clockwise from 12 o'clock,
  // -120°…+120° leaves the gap at the bottom.
  const polar = (deg: number, r: number): [number, number] => {
    const rad = (deg * Math.PI) / 180
    return [60 + r * Math.sin(rad), 60 - r * Math.cos(rad)]
  }
  const arc = (fromDeg: number, toDeg: number, r: number) => {
    const [x1, y1] = polar(fromDeg, r)
    const [x2, y2] = polar(toDeg, r)
    const large = toDeg - fromDeg > 180 ? 1 : 0
    return `M ${x1.toFixed(2)} ${y1.toFixed(2)} A ${r} ${r} 0 ${large} 1 ${x2.toFixed(2)} ${y2.toFixed(2)}`
  }
  const START = -120, SPAN = 240
  const tone = thresholdTone(value, warn, crit)
  const zone = (v: number | null) => (v === null || v > max ? null : START + SPAN * Math.min(1, v / max))
  const warnDeg = zone(warn), critDeg = zone(crit)
  return (
    <div className="flex flex-col items-center justify-center h-full py-1">
      <svg viewBox="0 0 120 92" className="w-full max-w-[220px]">
        <path d={arc(START, START + SPAN, 46)} fill="none" stroke="#1e293b" strokeWidth="9" strokeLinecap="round" />
        {warnDeg !== null && (
          <path d={arc(warnDeg, critDeg ?? START + SPAN, 46)} fill="none" stroke="rgba(251,191,36,0.35)" strokeWidth="9" />
        )}
        {critDeg !== null && (
          <path d={arc(critDeg, START + SPAN, 46)} fill="none" stroke="rgba(248,113,113,0.4)" strokeWidth="9" />
        )}
        {frac > 0.005 && (
          <path d={arc(START, START + SPAN * frac, 46)} fill="none" stroke={tone} strokeWidth="9" strokeLinecap="round" />
        )}
        <text x="60" y="58" textAnchor="middle" fill="#e2e8f0" fontSize="20" fontWeight="700" className="tabular-nums">
          {fmtMetric(value)}
        </text>
        <text x="60" y="72" textAnchor="middle" fill="#64748b" fontSize="8">
          {s.series.metric}{s.series.unit ? ` (${s.series.unit})` : ''} / {fmtMetric(max)}
        </text>
      </svg>
      {(warn !== null || crit !== null) && (
        <div className="text-[10px] text-muted-foreground tabular-nums">
          {warn !== null && <span className="text-amber-400/80">warn {warn}</span>}
          {warn !== null && crit !== null && ' · '}
          {crit !== null && <span className="text-red-400/80">crit {crit}</span>}
        </div>
      )}
    </div>
  )
}

// DonutWidget: state distribution (services or hosts) as an SVG donut.
// Legend rows (and slices) drill down into the filtered objects list.
function DonutWidget({ widget }: { widget: DashboardWidget }) {
  const { refreshMs } = useDashView()
  const { data, isLoading } = useQuery({
    queryKey: ['overview'],
    queryFn: () => get<Overview>('/overview'),
    refetchInterval: refreshMs || false,
  })
  if (isLoading) return <Spinner />
  const s = data?.summary
  if (!s) return <Empty text="keine Daten" />
  const kind = widget.scope === 'hosts' ? 'host' : 'service'
  const segs = widget.scope === 'hosts'
    ? [
      { label: 'Up', state: 'up', value: s.hostsUp, color: '#34d399' },
      { label: 'Down', state: 'down', value: s.hostsDown, color: '#f87171' },
      { label: 'Unreachable', state: 'unreachable', value: s.hostsUnreachable, color: '#a78bfa' },
    ]
    : [
      { label: 'OK', state: 'ok', value: s.servicesOk, color: '#34d399' },
      { label: 'Warning', state: 'warning', value: s.servicesWarning, color: '#fbbf24' },
      { label: 'Critical', state: 'critical', value: s.servicesCritical, color: '#f87171' },
      { label: 'Unknown', state: 'unknown', value: s.servicesUnknown, color: '#94a3b8' },
    ]
  const total = segs.reduce((n, x) => n + x.value, 0)
  if (total === 0) return <Empty text="keine Daten" />
  const R = 40, C = 2 * Math.PI * R
  let offset = 0
  return (
    <div className="flex items-center justify-center gap-5 h-full">
      <svg viewBox="0 0 100 100" className="w-[120px] shrink-0 -rotate-90">
        {segs.filter((x) => x.value > 0).map((x) => {
          const len = (x.value / total) * C
          const el = (
            <Link key={x.label} to="/objects" search={{ kind, state: x.state }}>
              <circle cx="50" cy="50" r={R} fill="none" stroke={x.color}
                strokeWidth="12" strokeDasharray={`${len} ${C - len}`}
                strokeDashoffset={-offset} className="cursor-pointer" />
            </Link>
          )
          offset += len
          return el
        })}
        <text x="50" y="46" textAnchor="middle" fill="#e2e8f0" fontSize="18" fontWeight="700"
          transform="rotate(90 50 50)" className="tabular-nums">{total}</text>
        <text x="50" y="60" textAnchor="middle" fill="#64748b" fontSize="8"
          transform="rotate(90 50 50)">{widget.scope === 'hosts' ? 'Hosts' : 'Services'}</text>
      </svg>
      <div className="space-y-1">
        {segs.map((x) => (
          <Link key={x.label} to="/objects" search={{ kind, state: x.state }}
            className="flex items-center gap-2 text-xs hover:bg-muted/60 rounded px-1 -mx-1">
            <span className="w-2.5 h-2.5 rounded-sm shrink-0" style={{ background: x.color }} />
            <span className="text-muted-foreground w-24">{x.label}</span>
            <span className="text-foreground tabular-nums font-semibold">{x.value}</span>
            <span className="text-muted-foreground/70 tabular-nums">{total ? Math.round((x.value / total) * 100) : 0}%</span>
          </Link>
        ))}
      </div>
    </div>
  )
}

// TableWidget: a live host/service table on the dashboard — state,
// name, output, last check; rows drill down to the object detail.
function TableWidget({ widget }: { widget: DashboardWidget }) {
  const { refreshMs } = useDashView()
  const limit = widget.limit ?? 15
  const path = widget.scope === 'hosts' ? '/hosts' : widget.scope === 'services' ? '/services' : '/objects'
  const params = new URLSearchParams({ limit: String(limit) })
  if (widget.selector) params.set('selector', widget.selector)
  if (widget.query) params.set('q', widget.query)
  const { data, isLoading } = useQuery({
    queryKey: ['objects', 'table-widget', widget.scope, widget.selector, widget.query, limit],
    queryFn: () => get<ListResponse<NPObject>>(`${path}?${params.toString()}`),
    refetchInterval: refreshMs || false,
  })
  const rows = data?.items ?? []
  if (isLoading) return <Spinner />
  if (rows.length === 0) return <Empty text={t('empty')} />
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left text-xs text-muted-foreground uppercase tracking-wider">
          <th className="font-medium pb-1.5 pr-2">{t('state')}</th>
          <th className="font-medium pb-1.5 pr-2">{t('name')}</th>
          <th className="font-medium pb-1.5 pr-2">{t('output')}</th>
          <th className="font-medium pb-1.5 text-right">Check</th>
        </tr>
      </thead>
      <tbody className="divide-y divide-border/60">
        {rows.map((o) => (
          <tr key={o.id} className="hover:bg-card/60 group">
            <td className="py-1.5 pr-2 whitespace-nowrap">
              <Link to="/objects/$id" params={{ id: o.id }}
                className={`font-semibold ${o.state?.lastCheck ? stateColor(o.kind, o.state.state) : 'text-muted-foreground'}`}>
                {o.state?.lastCheck
                  ? `${stateIcon(o.kind, o.state.state)} ${stateLabel(o.kind, o.state.state)}`
                  : `○ ${t('pending')}`}
              </Link>
            </td>
            <td className="py-1.5 pr-2 max-w-[16rem]">
              <Link to="/objects/$id" params={{ id: o.id }}
                className="text-foreground font-medium truncate block group-hover:text-primary">
                {o.kind === 'service' && o.hostName ? `${o.hostName} / ` : ''}{o.name}
              </Link>
            </td>
            <td className="py-1.5 pr-2 text-muted-foreground truncate max-w-[20rem]">{o.state?.output}</td>
            <td className="py-1.5 text-muted-foreground/70 text-xs tabular-nums text-right whitespace-nowrap">
              {fmtAgo(o.state?.lastCheck)}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

// BarWidget: horizontal bars of an object's current metric values,
// threshold-coloured — e.g. disk usage per mount at a glance.
function BarWidget({ widget }: { widget: DashboardWidget }) {
  const { refreshMs } = useDashView()
  const limit = widget.limit ?? 8
  const { data, isLoading } = useQuery({
    queryKey: ['metrics', 'bar', widget.object, widget.metric],
    enabled: !!widget.object,
    queryFn: () => post<SeriesResult[]>('/metrics/query', {
      objectId: widget.object,
      metric: widget.metric || undefined,
      from: rangeFrom('1h'),
      to: new Date().toISOString(),
      maxPoints: 20,
    }),
    refetchInterval: refreshMs || false,
  })
  if (!widget.object) return <Empty text={t('empty')} />
  if (isLoading) return <Spinner />
  const rows = (data ?? [])
    .filter((r) => !r.series.metric.startsWith('np_') && r.points.length > 0)
    .map((r) => ({
      metric: r.series.metric, unit: r.series.unit ?? '',
      // points is non-empty (filtered above); default guards the index lookup.
      value: r.points[r.points.length - 1]?.v ?? 0,
      warn: widget.warn ?? nagiosRangeStart(r.series.warn), crit: widget.crit ?? nagiosRangeStart(r.series.crit),
    }))
    .sort((a, b) => b.value - a.value)
    .slice(0, limit)
  if (rows.length === 0) return <Empty text="keine Daten" />
  const scaleMax = Math.max(...rows.map((r) => Math.max(r.value, r.crit ?? 0, r.warn ?? 0))) || 1
  return (
    <div className="space-y-2 py-1">
      {rows.map((r) => {
        const tone = thresholdTone(r.value, r.warn, r.crit)
        const pct = Math.max(1.5, (r.value / scaleMax) * 100)
        return (
          <div key={r.metric} className="text-xs">
            <div className="flex items-baseline justify-between mb-0.5">
              <span className="text-foreground/90 font-mono truncate">{r.metric}</span>
              <span className="text-foreground tabular-nums font-semibold shrink-0 ml-2">
                {fmtMetric(r.value)}{r.unit}
              </span>
            </div>
            <div className="relative h-2.5 bg-slate-800/80 rounded overflow-hidden">
              <div className="absolute inset-y-0 left-0 rounded" style={{ width: `${pct}%`, background: tone }} />
              {r.warn !== null && r.warn <= scaleMax && (
                <div className="absolute inset-y-0 w-px bg-amber-400/70" style={{ left: `${(r.warn / scaleMax) * 100}%` }} />
              )}
              {r.crit !== null && r.crit <= scaleMax && (
                <div className="absolute inset-y-0 w-px bg-red-400/80" style={{ left: `${(r.crit / scaleMax) * 100}%` }} />
              )}
            </div>
          </div>
        )
      })}
    </div>
  )
}

// StatWidget: one scoped metric as a big number + tiny sparkline — a focused,
// object/metric-bound alternative to the global KPI counters (DASH-5).
// Threshold-coloured against the metric's warn/crit (or an explicit override).
function StatWidget({ widget }: { widget: DashboardWidget }) {
  const { refreshMs, range: rangeOverride } = useDashView()
  const range = rangeOverride ?? widget.range ?? '6h'
  const { data, isLoading } = useQuery({
    queryKey: ['metrics', 'stat', widget.object, widget.metric, range],
    enabled: !!widget.object,
    queryFn: () => post<SeriesResult[]>('/metrics/query', {
      objectId: widget.object,
      metric: widget.metric || undefined,
      from: rangeFrom(range),
      to: new Date().toISOString(),
      maxPoints: 60,
    }),
    refetchInterval: refreshMs || false,
  })
  if (!widget.object) return <Empty text={t('empty')} />
  if (isLoading) return <Spinner />
  const s = (data ?? []).filter((r) => !r.series.metric.startsWith('np_') && r.points.length > 0)[0]
  const last = s?.points[s.points.length - 1]
  if (!s || !last) return <Empty text="keine Daten" />
  const value = last.v
  const warn = widget.warn ?? nagiosRangeStart(s.series.warn)
  const crit = widget.crit ?? nagiosRangeStart(s.series.crit)
  const tone = thresholdTone(value, warn, crit)
  const vals = s.points.map((p) => p.v)
  const lo = Math.min(...vals), span = (Math.max(...vals) - lo) || 1
  const spark = s.points
    .map((p, i) => `${((i / (s.points.length - 1)) * 100).toFixed(1)},${(26 - ((p.v - lo) / span) * 24).toFixed(1)}`)
    .join(' ')
  return (
    <div className="flex flex-col h-full justify-center gap-1 px-1">
      <div className="flex items-baseline gap-1.5">
        <span className="text-3xl font-bold tabular-nums leading-none" style={{ color: tone }}>{fmtMetric(value)}</span>
        {s.series.unit && <span className="text-sm text-muted-foreground">{s.series.unit}</span>}
      </div>
      <div className="text-xs text-muted-foreground truncate">{widget.metric || s.series.metric}</div>
      {s.points.length > 1 && (
        <svg viewBox="0 0 100 28" preserveAspectRatio="none" className="w-full h-7 mt-0.5">
          <polyline points={spark} fill="none" stroke={tone} strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
        </svg>
      )}
    </div>
  )
}

// WidgetBody dispatches to the right renderer for a widget type.
export function WidgetBody({ widget }: { widget: DashboardWidget }) {
  switch (widget.type) {
    case 'counters': return <CountersWidget />
    case 'problems': return <ProblemsWidget widget={widget} />
    case 'alerts': return <AlertsWidget widget={widget} />
    case 'metric': return <MetricWidget widget={widget} />
    case 'gauge': return <GaugeWidget widget={widget} />
    case 'stat': return <StatWidget widget={widget} />
    case 'donut': return <DonutWidget widget={widget} />
    case 'bar': return <BarWidget widget={widget} />
    case 'table': return <TableWidget widget={widget} />
    case 'bpi': return <BpiWidget widget={widget} />
    case 'markdown': return <MarkdownWidget widget={widget} />
    default: return <Empty text={widget.type} />
  }
}
