// Dashboard widget renderers (CMP Visualization-Dashboard parity, SPEC §12.3).
// Each renderer is a self-contained query so it can auto-refresh
// independently (wallboard-friendly: refetchInterval 30s). The grid
// placement + edit chrome live in Dashboards.tsx; this file is the
// read-only "what a widget shows" layer.
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { get, post, fmtAgo, type ListResponse } from '../../api'
import type {
  Overview, ProblemRow, Alert, SeriesResult, DashboardWidget,
} from '../../types'
import { stateIcon, stateColor, sevColor } from '../../types'
import { Tile, Badge, Empty, Spinner } from '../ui'
import { Chart } from '../Chart'
import { t } from '../../i18n'

// BPI tree node as returned by GET /business-services:tree.
export interface BSNode {
  service: { id: string; name: string; rule?: string; slaTarget?: number }
  state: number // model.State: 0 OK,1 WARN,2 CRIT,3 UNKNOWN
  children?: BSNode[]
  causes?: string[]
}

const REFRESH = 30_000

// bsStateDot maps a model.State number to an icon + colour. BPI nodes use
// the service-state palette (OK/WARN/CRIT/UNKNOWN).
export function bsStateMeta(state: number): { icon: string; color: string; label: string } {
  const icon = ['●', '▲', '✕', '?'][state] ?? '?'
  const color = ['text-emerald-400', 'text-amber-400', 'text-red-400', 'text-slate-400'][state] ?? 'text-slate-400'
  const label = ['OK', 'WARNING', 'CRITICAL', 'UNKNOWN'][state] ?? 'UNKNOWN'
  return { icon, color, label }
}

// rangeFrom converts a "1h"/"3h"/"24h"/"7d" token to an ISO start.
export function rangeFrom(range?: string): string {
  const now = Date.now()
  const map: Record<string, number> = {
    '1h': 3600_000, '3h': 3 * 3600_000, '24h': 24 * 3600_000, '7d': 7 * 24 * 3600_000,
  }
  const ms = map[range ?? '3h'] ?? 3 * 3600_000
  return new Date(now - ms).toISOString()
}

// CountersWidget: KPI tiles from /overview.
function CountersWidget() {
  const { data } = useQuery({
    queryKey: ['overview'],
    queryFn: () => get<Overview>('/overview'),
    refetchInterval: REFRESH,
  })
  const s = data?.summary
  const critAlerts = data?.openAlerts?.critical ?? 0
  const warnAlerts = data?.openAlerts?.warning ?? 0
  return (
    <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-3">
      <Tile label={t('hostsUp')} value={s?.hostsUp ?? '—'} tone="ok" />
      <Tile label={t('hostsDown')} value={s?.hostsDown ?? '—'} tone={s?.hostsDown ? 'crit' : 'default'} />
      <Tile label={t('servicesOk')} value={s?.servicesOk ?? '—'} tone="ok" />
      <Tile label={t('servicesWarning')} value={s?.servicesWarning ?? '—'} tone={s?.servicesWarning ? 'warn' : 'default'} />
      <Tile label={t('servicesCritical')} value={s?.servicesCritical ?? '—'} tone={s?.servicesCritical ? 'crit' : 'default'} />
      <Tile label={t('openAlerts')} value={`${critAlerts + warnAlerts}`} tone={critAlerts ? 'crit' : warnAlerts ? 'warn' : 'default'} />
    </div>
  )
}

// ProblemsWidget: compact problem list (state icon, name, output, ago).
function ProblemsWidget({ widget }: { widget: DashboardWidget }) {
  const limit = widget.limit ?? 10
  const sel = widget.selector ? `&selector=${encodeURIComponent(widget.selector)}` : ''
  const { data, isLoading } = useQuery({
    queryKey: ['problems', 'widget', limit, widget.selector],
    queryFn: () => get<ListResponse<ProblemRow>>(`/problems?limit=${limit}${sel}`),
    refetchInterval: REFRESH,
  })
  const rows = data?.items ?? []
  if (isLoading) return <Spinner />
  if (rows.length === 0) return <div className="text-emerald-500/80 text-sm p-3">✓ {t('noProblems')}</div>
  return (
    <div className="divide-y divide-slate-800/60">
      {rows.slice(0, limit).map((p) => (
        <Link
          key={p.object.id} to="/objects/$id" params={{ id: p.object.id }}
          className="flex items-center gap-2 py-1.5 px-1 hover:bg-slate-900/60 rounded text-sm"
        >
          <span className={`${stateColor(p.object.kind, p.state.state)} font-bold w-6 text-center shrink-0`}>
            {stateIcon(p.object.kind, p.state.state)}
          </span>
          <span className="text-slate-200 font-medium truncate max-w-[40%]">
            {p.object.kind === 'service' && p.object.hostName ? `${p.object.hostName} / ` : ''}{p.object.name}
          </span>
          <span className="text-slate-500 truncate flex-1">{p.state.output}</span>
          <span className="text-slate-600 text-xs tabular-nums shrink-0">{fmtAgo(p.state.lastHardChange)}</span>
        </Link>
      ))}
    </div>
  )
}

// AlertsWidget: open alerts with severity badges.
function AlertsWidget({ widget }: { widget: DashboardWidget }) {
  const limit = widget.limit ?? 10
  const { data, isLoading } = useQuery({
    queryKey: ['alerts', 'widget', limit],
    queryFn: () => get<ListResponse<Alert>>(`/alerts?status=open&limit=${limit}`),
    refetchInterval: REFRESH,
  })
  const rows = data?.items ?? []
  if (isLoading) return <Spinner />
  if (rows.length === 0) return <div className="text-emerald-500/80 text-sm p-3">✓ {t('empty')}</div>
  return (
    <div className="divide-y divide-slate-800/60">
      {rows.slice(0, limit).map((a) => (
        <Link
          key={a.id} to="/alerts"
          className="flex items-center gap-2 py-1.5 px-1 hover:bg-slate-900/60 rounded text-sm"
        >
          <Badge className={sevColor(a.severity)}>{a.severity}</Badge>
          <span className="text-slate-200 truncate flex-1">{a.title}</span>
          <span className="text-slate-600 text-xs tabular-nums shrink-0">{fmtAgo(a.openedAt)}</span>
        </Link>
      ))}
    </div>
  )
}

// MetricWidget: a Chart from metrics/query for one object+metric over a range.
function MetricWidget({ widget }: { widget: DashboardWidget }) {
  const { data, isLoading } = useQuery({
    queryKey: ['metrics', 'widget', widget.object, widget.metric, widget.range],
    enabled: !!widget.object,
    queryFn: () => post<SeriesResult[]>('/metrics/query', {
      objectId: widget.object,
      metric: widget.metric || undefined,
      from: rangeFrom(widget.range),
      to: new Date().toISOString(),
      maxPoints: 300,
    }),
    refetchInterval: REFRESH,
  })
  if (!widget.object) return <Empty text={t('empty')} />
  if (isLoading) return <Spinner />
  const series = (data ?? []).filter((s) => !s.series.metric.startsWith('np_'))
  if (series.length === 0) return <Empty text="keine Daten" />
  return (
    <div className="space-y-4">
      {series.map((s) => <Chart key={`${s.series.objectId}:${s.series.id}`} result={s} height={140} />)}
    </div>
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
              <span className="text-slate-200 truncate">{n.service.name}</span>
              {typeof n.service.slaTarget === 'number' && n.service.slaTarget > 0 && (
                <span className="text-slate-600 text-xs">SLA {n.service.slaTarget}%</span>
              )}
              {n.causes && n.causes.length > 0 && depth === 0 && (
                <span className="text-slate-600 text-xs truncate">↳ {n.causes.join(', ')}</span>
              )}
            </div>
            {n.children && n.children.length > 0 && <BpiTreeLines nodes={n.children} depth={depth + 1} />}
          </div>
        )
      })}
    </>
  )
}

// BpiWidget: a single business service subtree (by name) with live status.
function BpiWidget({ widget }: { widget: DashboardWidget }) {
  const { data, isLoading } = useQuery({
    queryKey: ['business-tree', 'widget'],
    queryFn: () => get<BSNode[]>('/business-services:tree'),
    refetchInterval: REFRESH,
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
    <div className="whitespace-pre-wrap text-sm text-slate-300 leading-relaxed">
      {widget.text || ''}
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
    case 'bpi': return <BpiWidget widget={widget} />
    case 'markdown': return <MarkdownWidget widget={widget} />
    default: return <Empty text={widget.type} />
  }
}

// Human label for a widget type (German-first).
export function widgetTypeLabel(type: DashboardWidget['type']): string {
  const de: Record<DashboardWidget['type'], string> = {
    counters: 'Zähler (KPIs)',
    problems: 'Probleme',
    alerts: 'Alarme',
    metric: 'Metrik-Diagramm',
    bpi: 'Business Service',
    markdown: 'Text / Markdown',
  }
  return de[type] ?? type
}
