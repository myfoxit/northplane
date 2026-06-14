// uPlot wrapper (SPEC §12.1: vendored thin wrapper, ~45 KB lib). Renders one OR
// many overlaid time-series with a colour legend and threshold bands. Bands use
// an explicit warn/crit override when given (user-/API-/AI-set), else the first
// series' Nagios perfdata range (SPEC §8.3). Pass a single `result` or an array
// of `results` to overlay (e.g. one metric across many hosts).
import { useEffect, useRef } from 'react'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import type { SeriesResult } from '../types'
import { alignSeries, effectiveBand } from './dash/series'

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
          fill: single ? 'rgba(96,165,250,0.08)' : undefined,
          points: { show: false },
          spanGaps: false,
        })),
      ],
      axes: [
        { stroke: '#475569', grid: { stroke: '#1e293b' }, ticks: { stroke: '#1e293b' } },
        { stroke: '#475569', grid: { stroke: '#1e293b' }, ticks: { stroke: '#1e293b' }, size: 60 },
      ],
      legend: { show: false },
      cursor: { y: false },
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
            if (critAt !== null) drawBand(critAt, 'rgba(248,113,113,0.07)')
            else if (warnAt !== null) drawBand(warnAt, 'rgba(251,191,36,0.06)')
          },
        ],
      },
    }
    const data = [x, ...series.map((s) => s.values)] as unknown as uPlot.AlignedData
    plotRef.current?.destroy()
    plotRef.current = new uPlot(opts, data, ref.current)
    const onResize = () => {
      if (ref.current && plotRef.current) {
        plotRef.current.setSize({ width: ref.current.clientWidth, height })
      }
    }
    window.addEventListener('resize', onResize)
    return () => {
      window.removeEventListener('resize', onResize)
      plotRef.current?.destroy()
      plotRef.current = null
    }
  }, [result, results, warn, crit, height])

  const all = results ?? (result ? [result] : [])
  const { x, series } = alignSeries(all)
  if (x.length === 0 || series.length === 0) {
    return <div className="text-muted-foreground text-xs p-4">no data</div>
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
