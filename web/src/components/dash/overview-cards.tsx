// Overview hero cards (Polaris): KPI stat cards (label + big value/total +
// corner badge + sublabel on a neutral card — health reads from the value and
// the labeled badge, not from tinted borders) and a service-status STACKED BAR
// with a labeled legend. The old thick donut is gone: close-together values
// compare poorly on arcs, and a thin bar with 2px surface gaps plus counts in
// the legend reads instantly on a NOC board. Token-driven so every colour
// theme + light/dark mode themes correctly. No chart dependency — inline SVG
// and flexbox only.
import { type ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { cn } from '@/lib/utils'
import type { ObjectsSearch } from '../../types'

export type Tone = 'default' | 'ok' | 'warn' | 'crit'

// tone → CSS variable colour (themes recolour these automatically).
const toneVar: Record<Tone, string> = {
  default: 'var(--primary)',
  ok: 'var(--success)',
  warn: 'var(--warning)',
  crit: 'var(--danger)',
}
const toneText: Record<Tone, string> = {
  default: 'text-foreground',
  ok: 'text-foreground',
  warn: 'text-warning',
  crit: 'text-danger',
}

export function StatCard({
  label, value, total, unit, badge, badgeTone = 'default', sublabel, tone = 'default', to, search,
}: {
  label: string; value: ReactNode; total?: ReactNode; unit?: string
  badge?: ReactNode; badgeTone?: Tone; sublabel?: ReactNode; tone?: Tone
  to?: '/objects' | '/alerts' | '/problems'; search?: ObjectsSearch
}) {
  const inner = (
    <div className="rounded-xl border border-border bg-card px-4 pt-3 pb-3.5 transition-colors hover:border-ring/40">
      <div className="flex items-start justify-between gap-2">
        <div className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">{label}</div>
        {badge != null && (
          <span
            className="shrink-0 rounded-md px-1.5 py-0.5 text-[11px] font-semibold tabular-nums"
            style={{ color: toneVar[badgeTone], backgroundColor: 'color-mix(in oklab, currentColor 12%, transparent)' }}
          >
            {badge}
          </span>
        )}
      </div>
      <div className="mt-1.5 flex items-baseline gap-1.5">
        <span className={cn('text-[26px] font-semibold tabular-nums leading-none', toneText[tone])}>{value}</span>
        {total != null && <span className="text-sm text-muted-foreground tabular-nums">/ {total}</span>}
        {unit && <span className="text-xs text-muted-foreground">{unit}</span>}
      </div>
      {sublabel != null && <div className="mt-1 text-xs text-muted-foreground truncate">{sublabel}</div>}
    </div>
  )
  return to ? <Link to={to} search={search ?? {}} className="block">{inner}</Link> : inner
}

// ── Service-status stacked bar ─────────────────────────────────────────
export interface Segment { label: string; value: number; color: string }

export function StatusDonut({ segments, total, healthyPct }: { segments: Segment[]; total: number; healthyPct: number }) {
  const shown = segments.filter((s) => s.value > 0)
  return (
    <div className="space-y-3">
      <div className="flex items-baseline gap-1.5">
        <span className="text-2xl font-semibold tabular-nums text-foreground">{healthyPct}%</span>
        <span className="text-xs text-muted-foreground">· {total} total</span>
      </div>
      {/* Thin stacked bar; the 2px gaps are the card surface doing the
          separating (never a stroke around the marks). */}
      <div className="flex h-3 overflow-hidden rounded-md" aria-hidden>
        {shown.map((s, i) => (
          <span
            key={s.label}
            className={cn('h-full', i > 0 && 'ml-0.5', i === 0 && 'rounded-l-md', i === shown.length - 1 && 'rounded-r-md')}
            style={{ backgroundColor: s.color, flexGrow: s.value, flexBasis: 6, minWidth: 6 }}
          />
        ))}
        {shown.length === 0 && <span className="h-full w-full rounded-md bg-muted" />}
      </div>
      <div className="space-y-1.5">
        {segments.map((s) => (
          <div key={s.label} className="flex items-center gap-2 text-sm">
            <span className="size-2 shrink-0 rounded-full" style={{ backgroundColor: s.color }} aria-hidden />
            <span className="text-muted-foreground">{s.label}</span>
            <span className="ml-auto font-semibold tabular-nums text-foreground/90">{s.value}</span>
            <span className="w-12 text-right text-xs tabular-nums text-muted-foreground/70">
              {total > 0 ? `${Math.round((s.value / total) * 1000) / 10}%` : '—'}
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}
