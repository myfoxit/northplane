// uPlot wrapper (SPEC §12.1: vendored thin wrapper, ~45 KB lib). Renders one OR
// many overlaid time-series with a colour legend, threshold bands and a hover
// crosshair + value tooltip. Bands use an explicit warn/crit override when
// given (user-/API-/AI-set), else the first series' Nagios perfdata range
// (SPEC §8.3). Pass a single `result` or an array of `results` to overlay.
import { useEffect, useRef } from 'react'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import type { SeriesResult } from '../types'
import { alignSeries, effectiveBand, type AlignedSeries } from './dash/series'
import { t } from '../i18n'

// fmtNum: compact value formatting for tooltip/axis — integers stay integer,
// fractionals get ≤2 decimals, big numbers get k/M suffixes.
function fmtNum(v: number | null | undefined): string {
  if (v == null || !Number.isFinite(v)) return '—'
  const a = Math.abs(v)
  if (a >= 1e6) return (v / 1e6).toFixed(1).replace(/\.0$/, '') + 'M'
  if (a >= 1e3) return (v / 1e3).toFixed(1).replace(/\.0$/, '') + 'k'
  return Number.isInteger(v) ? String(v) : v.toFixed(2).replace(/\.?0+$/, '')
}

// tooltipPlugin: a floating panel (built in the plot's `over` layer) listing the
// hovered timestamp and each series' value at that index, colour-keyed.
function tooltipPlugin(series: AlignedSeries[]): uPlot.Plugin {
  let el: HTMLDivElement | null = null
  return {
    hooks: {
      ready: (u: uPlot) => {
        el = document.createElement('div')
        el.style.cssText =
          'position:absolute;pointer-events:none;z-index:50;display:none;white-space:nowrap;' +
          'background:rgba(15,23,42,0.96);border:1px solid #334155;border-radius:8px;' +
          'padding:6px 8px;font:12px/1.45 ui-monospace,monospace;color:#e2e8f0;' +
          'box-shadow:0 8px 24px rgba(0,0,0,0.45)'
        u.over.appendChild(el)
      },
      setCursor: (u: uPlot) => {
        if (!el) return
        const { idx, left, top } = u.cursor
        if (idx == null || left == null || left < 0) { el.style.display = 'none'; return }
        const t = (u.data[0]![idx] as number) * 1000
        const head = `<div style="color:#94a3b8;margin-bottom:3px">${new Date(t).toLocaleString()}</div>`
        const rows = series.map((s, i) => {
          const v = u.data[i + 1]![idx] as number | null
          return `<div style="display:flex;gap:6px;align-items:center">`
            + `<span style="width:8px;height:8px;border-radius:2px;background:${s.color};display:inline-block"></span>`
            + `<span style="color:#cbd5e1">${s.label}</span>`
            + `<span style="margin-left:auto;font-weight:600;color:#f1f5f9">${fmtNum(v)}${s.unit ? ' ' + s.unit : ''}</span>`
            + `</div>`
        }).join('')
        el.innerHTML = head + rows
        el.style.display = 'block'
        // Place to the right of the crosshair, flip left near the edge.
        const ttw = el.offsetWidth
        const flip = left + ttw + 16 > u.over.clientWidth
        el.style.left = `${flip ? left - ttw - 12 : left + 12}px`
        el.style.top = `${Math.max(0, (top ?? 0) - 8)}px`
      },
    },
  }
}

export function Chart({ result, results, warn, crit, height = 180 }: {
  result?: SeriesResult
  results?: SeriesResult[]
  warn?: number
  crit?: number
  height?: number
}) {
  const ref = useRef<HTMLDivElement>(null)
  const plotRef = useRef<uPlot | null>(null)

  useEffect(() => {
    if (!ref.current) return
    const all = results ?? (result ? [result] : [])
    const { x, series } = alignSeries(all)
    if (x.length === 0 || series.length === 0) return

    // threshold bands: explicit override wins, else the first series' perfdata.
    const banded = series.find((s) => s.warn || s.crit)
    const { warn: warnAt, crit: critAt } = effectiveBand(warn, crit, banded?.warn, banded?.crit)
    const single = series.length === 1

    const opts: uPlot.Options = {
      width: ref.current.clientWidth,
      height,
      series: [
        {},
        ...series.map((s) => ({
          label: s.label,
          stroke: s.color,
          width: 1.5,
          fill: single ? 'rgba(96,165,250,0.10)' : undefined,
          points: { show: false },
          spanGaps: false,
          value: (_u: uPlot, v: number | null) => fmtNum(v) + (s.unit ? ' ' + s.unit : ''),
        })),
      ],
      axes: [
        { stroke: '#475569', grid: { stroke: '#1e293b' }, ticks: { stroke: '#1e293b' } },
        {
          stroke: '#475569', grid: { stroke: '#1e293b' }, ticks: { stroke: '#1e293b' }, size: 56,
          values: (_u, splits) => splits.map((v) => fmtNum(v)),
        },
      ],
      legend: { show: false },
      cursor: {
        x: true, y: true,
        points: { size: 6 },
        focus: { prox: 30 },
      },
      plugins: [tooltipPlugin(series)],
      hooks: {
        drawClear: [
          (u) => {
            // threshold bands (SPEC §8.3: Schwellenbänder)
            const ctx = u.ctx
            const drawBand = (from: number, color: string) => {
              const y = u.valToPos(from, 'y', true)
              const top = u.bbox.top
              if (y < top || !Number.isFinite(y)) return
              ctx.fillStyle = color
              ctx.fillRect(u.bbox.left, top, u.bbox.width, Math.max(0, y - top))
            }
            if (critAt !== null) drawBand(critAt, 'rgba(248,113,113,0.08)')
            else if (warnAt !== null) drawBand(warnAt, 'rgba(251,191,36,0.07)')
          },
        ],
      },
    }
    const data = [x, ...series.map((s) => s.values)] as unknown as uPlot.AlignedData
    plotRef.current?.destroy()
    plotRef.current = new uPlot(opts, data, ref.current)
    // Track the container width so the chart re-fits when its grid panel is
    // resized (not just on window resize).
    const refit = () => {
      if (ref.current && plotRef.current) {
        plotRef.current.setSize({ width: ref.current.clientWidth, height })
      }
    }
    const ro = new ResizeObserver(refit)
    ro.observe(ref.current)
    window.addEventListener('resize', refit)
    return () => {
      ro.disconnect()
      window.removeEventListener('resize', refit)
      plotRef.current?.destroy()
      plotRef.current = null
    }
  }, [result, results, warn, crit, height])

  const all = results ?? (result ? [result] : [])
  const { x, series } = alignSeries(all)
  if (x.length === 0 || series.length === 0) {
    return <div className="text-muted-foreground text-xs p-4">{t('noData')}</div>
  }
  const banded = series.find((s) => s.warn || s.crit)
  const { warn: warnAt, crit: critAt } = effectiveBand(warn, crit, banded?.warn, banded?.crit)
  return (
    <div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted-foreground mb-1 font-mono">
        {series.map((s) => (
          <span key={s.label} className="inline-flex items-center gap-1">
            <span className="inline-block w-2.5 h-0.5 rounded-sm" style={{ background: s.color }} />
            {s.label}{s.unit ? ` (${s.unit})` : ''}
          </span>
        ))}
        {warnAt !== null ? <span className="text-amber-400/70">· warn {warnAt}</span> : null}
        {critAt !== null ? <span className="text-red-400/70">· crit {critAt}</span> : null}
      </div>
      <div ref={ref} />
    </div>
  )
}
