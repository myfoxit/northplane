// Non-component helpers + types for the dashboard widget layer. Split out of
// widgets.tsx so that file (and the small linking components TileLink /
// BpiTreeLines) can stay component-only — react-refresh/only-export-components
// needs a module to export components OR helpers, not a mix. JSX-returning
// pieces live in widgets.tsx; this file is pure (.ts, no JSX).
import type { DashboardWidget } from '../../types'
import { t } from '../../i18n'

// BPI tree node as returned by GET /business-services:tree.
export interface BSNode {
  service: { id: string; name: string; rule?: string; slaTarget?: number }
  state: number // model.State: 0 OK,1 WARN,2 CRIT,3 UNKNOWN
  children?: BSNode[]
  causes?: string[]
}

// bsStateMeta maps a model.State number to an icon + colour. BPI nodes use
// the service-state palette (OK/WARN/CRIT/UNKNOWN).
export function bsStateMeta(state: number): { icon: string; color: string; label: string } {
  const icon = ['●', '▲', '✕', '?'][state] ?? '?'
  const color = ['text-emerald-400', 'text-amber-400', 'text-red-400', 'text-slate-400'][state] ?? 'text-slate-400'
  const label = ['OK', 'WARNING', 'CRITICAL', 'UNKNOWN'][state] ?? 'UNKNOWN'
  return { icon, color, label }
}

// rangeFrom converts a range token ("1h".."30d") to an ISO start.
export function rangeFrom(range?: string): string {
  const now = Date.now()
  const H = 3600_000
  const map: Record<string, number> = {
    '1h': H, '3h': 3 * H, '6h': 6 * H, '12h': 12 * H,
    '24h': 24 * H, '7d': 7 * 24 * H, '30d': 30 * 24 * H,
  }
  const ms = map[range ?? '3h'] ?? 3 * H
  return new Date(now - ms).toISOString()
}

// Human label for a widget type (German-first).
export function widgetTypeLabel(type: DashboardWidget['type']): string {
  const de: Record<DashboardWidget['type'], string> = {
    counters: t('widgetCounters'),
    problems: t('problems'),
    alerts: t('alerts'),
    metric: t('widgetMetric'),
    gauge: t('widgetGauge'),
    stat: t('widgetStat'),
    donut: t('widgetDonut'),
    bar: t('widgetBar'),
    table: t('widgetTable'),
    bpi: t('widgetBpi'),
    markdown: t('widgetMarkdown'),
  }
  return de[type] ?? type
}
