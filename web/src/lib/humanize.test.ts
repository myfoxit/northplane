import { describe, it, expect } from 'vitest'
import { formatUptimeTicks, humanizeOutput } from './humanize'

describe('formatUptimeTicks', () => {
  it('formats TimeTicks (centiseconds) as up Xd Yh', () => {
    // 20478192 ticks = 204781.92 s ≈ 2d 8h
    expect(formatUptimeTicks(20478192)).toBe('up 2d 8h')
  })
  it('keeps the two most-significant units', () => {
    expect(formatUptimeTicks(100 * (5 * 60 + 30))).toBe('up 5m 30s')
  })
  it('falls back to seconds for tiny uptimes', () => {
    expect(formatUptimeTicks(100 * 5)).toBe('up 5s')
    expect(formatUptimeTicks(0)).toBe('up 0s')
  })
  it('rejects garbage', () => {
    expect(formatUptimeTicks(-1)).toBe('')
    expect(formatUptimeTicks(NaN)).toBe('')
  })
})

describe('humanizeOutput', () => {
  it('rewrites a raw sysUpTime OID reading', () => {
    expect(humanizeOutput('VALUE OK - 1.3.6.1.2.1.1.3.0 = 20478192'))
      .toBe('VALUE OK - sysUpTime: up 2d 8h')
  })
  it('leaves unrelated output untouched', () => {
    expect(humanizeOutput('OK - load average: 0.15, 0.10, 0.09'))
      .toBe('OK - load average: 0.15, 0.10, 0.09')
  })
  it('handles empty / missing output', () => {
    expect(humanizeOutput('')).toBe('')
    expect(humanizeOutput(undefined)).toBe('')
    expect(humanizeOutput(null)).toBe('')
  })
})
