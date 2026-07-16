// Overview hero cards: richer KPI stat cards (corner badge + big value/total +
// sublabel + a live sparkline) and an SVG service-status donut. Built to read
// like a NOC board while staying token-driven so every colour theme + light/
// dark mode themes correctly. No chart dependency — the sparkline and donut are
// inline SVG.
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
  ok: 'text-success',
  warn: 'text-warning',
  crit: 'text-danger',
}

function Sparkline({ data, color, width = 240, height = 40 }: { data: number[]; color: string; width?: number; height?: number }) {
  const pts = data.length ? data : [0]
  const min = Math.min(...pts), max = Math.max(...pts)
  const span = max - min || 1
  const n = Math.max(pts.length - 1, 1)
  const x = (i: number) => (i / n) * width
  const y = (v: number) => height - 4 - ((v - min) / span) * (height - 8)
  const line = pts.map((v, i) => `${i === 0 ? 'M' : 'L'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(' ')
  const area = `${line} L${width},${height} L0,${height} Z`
  const id = `sg-${color.replace(/[^a-z0-9]/gi, '')}`
  return (
    <svg width="100%" height={height} viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" className="block" aria-hidden>
      <defs>
        <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity="0.28" />
          <stop offset="100%" stopColor={color} stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={area} fill={`url(#${id})`} />
      <path d={line} fill="none" stroke={color} strokeWidth="1.75" strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
    </svg>
  )
}

export function StatCard({
  label, value, total, unit, badge, badgeTone = 'default', sublabel, tone = 'default', series, to, search,
}: {
  label: string; value: ReactNode; total?: ReactNode; unit?: string
  badge?: ReactNode; badgeTone?: Tone; sublabel?: ReactNode; tone?: Tone
  series?: number[]; to?: '/objects' | '/alerts' | '/problems'; search?: ObjectsSearch
}) {
  const inner = (
    <div className="relative overflow-hidden rounded-xl border border-border bg-card px-4 pt-3 pb-9 shadow-sm transition-colors hover:border-input">
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
        <span className={cn('text-3xl font-bold tabular-nums leading-none', toneText[tone])}>{value}</span>
        {total != null && <span className="text-sm text-muted-foreground tabular-nums">/ {total}</span>}
        {unit && <span className="text-xs text-muted-foreground">{unit}</span>}
      </div>
      {sublabel != null && <div className="mt-1 text-xs text-muted-foreground truncate">{sublabel}</div>}
      {series && series.length > 0 && (
        <div className="pointer-events-none absolute inset-x-0 bottom-0 h-10 opacity-90">
          <Sparkline data={series} color={toneVar[tone]} />
        </div>
      )}
    </div>
  )
  return to ? <Link to={to} search={search ?? {}} className="block">{inner}</Link> : inner
}

// ── Service-status donut ───────────────────────────────────────────────
export interface Segment { label: string; value: number; color: string }

function Donut({ segments, size = 150, stroke = 15, children }: { segments: Segment[]; size?: number; stroke?: number; children?: ReactNode }) {
  const r = (size - stroke) / 2
  const C = 2 * Math.PI * r
  const shown = segments.filter((s) => s.value > 0)
  const total = shown.reduce((a, s) => a + s.value, 0) || 1
  // Prefix sums for arc offsets — computed without render-time mutation.
  const before = shown.map((_, i) => shown.slice(0, i).reduce((a, x) => a + x.value, 0))
  const single = shown.length <= 1
  return (
    <div className="relative shrink-0" style={{ width: size, height: size }}>
      <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} className="-rotate-90">
        <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke="var(--muted)" strokeWidth={stroke} />
        {shown.map((s, i) => {
          const len = (s.value / total) * C
          const off = ((before[i] ?? 0) / total) * C
          return (
            <circle key={i} cx={size / 2} cy={size / 2} r={r} fill="none" stroke={s.color} strokeWidth={stroke}
              strokeDasharray={`${len} ${C - len}`} strokeDashoffset={-off} strokeLinecap={single ? 'round' : 'butt'} />
          )
        })}
      </svg>
      <div className="absolute inset-0 flex flex-col items-center justify-center">{children}</div>
    </div>
  )
}

export function StatusDonut({ segments, total, healthyPct }: { segments: Segment[]; total: number; healthyPct: number }) {
  return (
    <div className="flex items-center gap-5">
      <Donut segments={segments}>
        <div className="text-2xl font-bold tabular-nums text-foreground">{healthyPct}%</div>
        <div className="text-[11px] text-muted-foreground">{total} total</div>
      </Donut>
      <div className="min-w-0 flex-1 space-y-1.5">
        {segments.map((s) => (
          <div key={s.label} className="flex items-center gap-2 text-sm">
            <span className="size-2.5 shrink-0 rounded-full" style={{ backgroundColor: s.color }} />
            <span className="text-muted-foreground">{s.label}</span>
            <span className="ml-auto font-semibold tabular-nums text-foreground/90">{s.value}</span>
          </div>
        ))}
      </div>
    </div>
  )
}
