import { describe, it, expect } from 'vitest'
import {
  alignSeries, groupByUnit, seriesColor, shortId, SERIES_PALETTE,
  nagiosRangeStart, effectiveBand, thresholdTone,
} from './series'
import type { SeriesResult } from '../../types'

function s(
  id: number, objectId: string, metric: string,
  points: [number, number][], extra: Partial<SeriesResult['series']> = {},
): SeriesResult {
  return {
    series: { id, objectId, metric, ...extra },
    points: points.map(([t, v]) => ({ t, v })),
  }
}

describe('seriesColor', () => {
  it('is stable per index and wraps around the palette', () => {
    expect(seriesColor(0)).toBe(SERIES_PALETTE[0])
    expect(seriesColor(SERIES_PALETTE.length)).toBe(SERIES_PALETTE[0])
    expect(seriesColor(1)).toBe(SERIES_PALETTE[1])
  })
  it('handles negative indices without crashing', () => {
    expect(SERIES_PALETTE).toContain(seriesColor(-1))
  })
})

describe('shortId', () => {
  it('returns the last 6 chars of a long id', () => {
    expect(shortId('abcdef123456')).toBe('123456')
  })
  it('returns short ids unchanged', () => {
    expect(shortId('abc')).toBe('abc')
  })
})

describe('alignSeries', () => {
  it('unites timestamps onto one ascending, deduped x (in seconds)', () => {
    const a = s(1, 'o1', 'cpu', [[1000, 10], [3000, 30]])
    const b = s(2, 'o2', 'cpu', [[2000, 20], [3000, 33]])
    const { x } = alignSeries([a, b])
    expect(x).toEqual([1, 2, 3]) // ms/1000, union of {1000,3000}∪{2000,3000}
  })

  it('null-fills gaps where a series has no sample at a timestamp', () => {
    const a = s(1, 'o1', 'cpu', [[1000, 10], [3000, 30]])
    const b = s(2, 'o2', 'cpu', [[2000, 20], [3000, 33]])
    const { series } = alignSeries([a, b])
    expect(series[0].values).toEqual([10, null, 30]) // o1 missing t=2000
    expect(series[1].values).toEqual([null, 20, 33]) // o2 missing t=1000
  })

  it('labels by metric when metric names are unique', () => {
    const a = s(1, 'o1', 'cpu', [[1000, 10]])
    const b = s(2, 'o1', 'mem', [[1000, 50]])
    const { series } = alignSeries([a, b])
    expect(series.map((x) => x.label)).toEqual(['cpu', 'mem'])
  })

  it('disambiguates same-metric overlays with a short object id', () => {
    const a = s(1, 'host-aaaaaa', 'cpu', [[1000, 10]])
    const b = s(2, 'host-bbbbbb', 'cpu', [[1000, 20]])
    const { series } = alignSeries([a, b])
    expect(series.map((x) => x.label)).toEqual(['cpu·aaaaaa', 'cpu·bbbbbb'])
  })

  it('drops series with no points and keeps colours indexed', () => {
    const a = s(1, 'o1', 'cpu', [])
    const b = s(2, 'o2', 'cpu', [[1000, 20]])
    const { x, series } = alignSeries([a, b])
    expect(x).toEqual([1])
    expect(series).toHaveLength(1)
    expect(series[0].color).toBe(SERIES_PALETTE[0])
  })

  it('carries unit/warn/crit through to the aligned series', () => {
    const a = s(1, 'o1', 'cpu', [[1000, 10]], { unit: '%', warn: '80', crit: '90' })
    const { series } = alignSeries([a])
    expect(series[0]).toMatchObject({ unit: '%', warn: '80', crit: '90' })
  })

  it('returns empty for no input', () => {
    expect(alignSeries([])).toEqual({ x: [], series: [] })
  })
})

describe('groupByUnit', () => {
  it('groups same-unit series together, preserving first-seen order', () => {
    const a = s(1, 'o1', 'cpu', [[1, 1]], { unit: '%' })
    const b = s(2, 'o1', 'lat', [[1, 1]], { unit: 'ms' })
    const c = s(3, 'o2', 'cpu', [[1, 1]], { unit: '%' })
    const groups = groupByUnit([a, b, c])
    expect(groups).toHaveLength(2)
    expect(groups[0].map((g) => g.series.id)).toEqual([1, 3]) // both "%"
    expect(groups[1].map((g) => g.series.id)).toEqual([2])    // "ms"
  })

  it('treats undefined unit as one group', () => {
    const a = s(1, 'o1', 'm1', [[1, 1]])
    const b = s(2, 'o1', 'm2', [[1, 1]])
    const groups = groupByUnit([a, b])
    expect(groups).toHaveLength(1)
    expect(groups[0]).toHaveLength(2)
  })
})

describe('nagiosRangeStart', () => {
  it.each([
    ['80', 80],     // bare number
    ['10:20', 20],  // range → upper bound
    ['@10:20', 20], // @-prefixed range → upper bound (prefix stripped)
  ])('parses %s → %d', (spec, want) => {
    expect(nagiosRangeStart(spec)).toBe(want)
  })
  it('returns null for absent, empty-upper, or unparsable specs', () => {
    expect(nagiosRangeStart(undefined)).toBeNull()
    expect(nagiosRangeStart('')).toBeNull()
    expect(nagiosRangeStart('80:')).toBeNull() // colon present but no upper value
    expect(nagiosRangeStart('abc')).toBeNull()
  })
})

describe('effectiveBand', () => {
  it('prefers an explicit override over the perfdata range', () => {
    expect(effectiveBand(70, 95, '80', '90')).toEqual({ warn: 70, crit: 95 })
  })
  it('falls back to the parsed perfdata range when no override', () => {
    expect(effectiveBand(undefined, undefined, '80', '90')).toEqual({ warn: 80, crit: 90 })
  })
  it('allows overriding only one side', () => {
    expect(effectiveBand(70, undefined, '80', '90')).toEqual({ warn: 70, crit: 90 })
  })
  it('is null where neither override nor perfdata is present', () => {
    expect(effectiveBand(undefined, undefined)).toEqual({ warn: null, crit: null })
  })
})

describe('thresholdTone', () => {
  it('returns red at/above crit, amber at/above warn, else green', () => {
    expect(thresholdTone(95, 80, 90)).toBe('#f87171') // crit
    expect(thresholdTone(85, 80, 90)).toBe('#fbbf24') // warn
    expect(thresholdTone(50, 80, 90)).toBe('#34d399') // ok
  })
  it('crit takes precedence over warn', () => {
    expect(thresholdTone(100, 80, 90)).toBe('#f87171')
  })
  it('ignores null thresholds', () => {
    expect(thresholdTone(100, null, null)).toBe('#34d399')
    expect(thresholdTone(100, 80, null)).toBe('#fbbf24')
  })
})
