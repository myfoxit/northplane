// uPlot wrapper (SPEC §12.1: vendored thin wrapper, ~45 KB lib) with
// threshold bands from perfdata warn/crit ranges (SPEC §8.3).
import { useEffect, useRef } from 'react'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import type { SeriesResult } from '../types'

// parseRangeStart extracts the numeric start of a Nagios range spec for
// the threshold band ("80", "80:", "@10:20" → 80/80/10).
function rangeStart(spec?: string): number | null {
  if (!spec) return null
  let body = spec.startsWith('@') ? spec.slice(1) : spec
  body = body.split(':')[body.includes(':') ? 1 : 0] || body.split(':')[0]
  const v = parseFloat(body)
  return Number.isFinite(v) ? v : null
}

export function Chart({ result, height = 180 }: { result: SeriesResult; height?: number }) {
  const ref = useRef<HTMLDivElement>(null)
  const plotRef = useRef<uPlot | null>(null)

  useEffect(() => {
    if (!ref.current || !result.points?.length) return
    const xs = result.points.map((p) => p.t / 1000)
    const ys = result.points.map((p) => p.v)
    const warn = rangeStart(result.series.warn)
    const crit = rangeStart(result.series.crit)

    const opts: uPlot.Options = {
      width: ref.current.clientWidth,
      height,
      series: [
        {},
        {
          label: result.series.metric,
          stroke: '#60a5fa',
          width: 1.5,
          fill: 'rgba(96,165,250,0.08)',
          points: { show: false },
        },
      ],
      axes: [
        { stroke: '#475569', grid: { stroke: '#1e293b' }, ticks: { stroke: '#1e293b' } },
        { stroke: '#475569', grid: { stroke: '#1e293b' }, ticks: { stroke: '#1e293b' },
          size: 60 },
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
            if (crit !== null) drawBand(crit, 'rgba(248,113,113,0.07)')
            else if (warn !== null) drawBand(warn, 'rgba(251,191,36,0.06)')
          },
        ],
      },
    }
    plotRef.current?.destroy()
    plotRef.current = new uPlot(opts, [xs, ys], ref.current)
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
  }, [result, height])

  if (!result.points?.length) {
    return <div className="text-slate-500 text-xs p-4">no data</div>
  }
  return (
    <div>
      <div className="text-xs text-slate-400 mb-1 font-mono">
        {result.series.metric}
        {result.series.unit ? ` (${result.series.unit})` : ''}
        {result.series.warn ? ` · warn ${result.series.warn}` : ''}
        {result.series.crit ? ` · crit ${result.series.crit}` : ''}
      </div>
      <div ref={ref} />
    </div>
  )
}
