import { describe, it, expect, vi, afterEach } from 'vitest'
import { rangeFrom, bsStateMeta, widgetTypeLabel } from './util'
import type { DashboardWidget } from '../../types'

describe('rangeFrom', () => {
  afterEach(() => vi.useRealTimers())

  it.each([
    ['1h', 3600_000],
    ['3h', 3 * 3600_000],
    ['24h', 24 * 3600_000],
    ['7d', 7 * 24 * 3600_000],
  ])('subtracts the right window for %s', (range, ms) => {
    const now = new Date('2026-06-08T12:00:00.000Z')
    vi.useFakeTimers()
    vi.setSystemTime(now)
    expect(rangeFrom(range)).toBe(new Date(now.getTime() - ms).toISOString())
  })

  it('defaults to 3h when range is undefined', () => {
    const now = new Date('2026-06-08T12:00:00.000Z')
    vi.useFakeTimers()
    vi.setSystemTime(now)
    expect(rangeFrom(undefined)).toBe(new Date(now.getTime() - 3 * 3600_000).toISOString())
  })

  it('defaults to 3h for an unrecognised token', () => {
    const now = new Date('2026-06-08T12:00:00.000Z')
    vi.useFakeTimers()
    vi.setSystemTime(now)
    expect(rangeFrom('bogus')).toBe(new Date(now.getTime() - 3 * 3600_000).toISOString())
  })

  it('returns a valid ISO timestamp in the past', () => {
    const out = rangeFrom('1h')
    expect(() => new Date(out).toISOString()).not.toThrow()
    expect(new Date(out).getTime()).toBeLessThan(Date.now())
  })
})

describe('bsStateMeta', () => {
  it('maps each BPI state to icon/color/label', () => {
    expect(bsStateMeta(0)).toEqual({ icon: '●', color: 'text-emerald-400', label: 'OK' })
    expect(bsStateMeta(1)).toEqual({ icon: '▲', color: 'text-amber-400', label: 'WARNING' })
    expect(bsStateMeta(2)).toEqual({ icon: '✕', color: 'text-red-400', label: 'CRITICAL' })
    expect(bsStateMeta(3)).toEqual({ icon: '?', color: 'text-slate-400', label: 'UNKNOWN' })
  })

  it('falls back to the UNKNOWN meta for out-of-range state', () => {
    expect(bsStateMeta(7)).toEqual({ icon: '?', color: 'text-slate-400', label: 'UNKNOWN' })
  })
})

describe('widgetTypeLabel', () => {
  const types: DashboardWidget['type'][] = [
    'counters', 'problems', 'alerts', 'metric', 'gauge',
    'donut', 'bar', 'table', 'bpi', 'markdown',
  ]

  it('returns a non-empty label for every widget type', () => {
    for (const type of types) {
      const label = widgetTypeLabel(type)
      expect(label).toBeTruthy()
      expect(typeof label).toBe('string')
    }
  })

  it('returns a specific known label', () => {
    expect(widgetTypeLabel('counters')).toBe('Counters (KPIs)')
    expect(widgetTypeLabel('markdown')).toBe('Text / Markdown')
  })

  it('echoes back an unknown type string (fallback)', () => {
    // Force a value outside the union to exercise the `?? type` fallback.
    const unknown = 'mystery' as DashboardWidget['type']
    expect(widgetTypeLabel(unknown)).toBe('mystery')
  })
})
