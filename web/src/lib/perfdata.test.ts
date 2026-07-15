import { describe, it, expect } from 'vitest'
import { parsePerfdata } from './perfdata'

describe('parsePerfdata', () => {
  it('parses a single metric with empty trailing fields', () => {
    expect(parsePerfdata('load1=1.09;3;6;;')).toEqual([
      { label: 'load1', value: 1.09, unit: '', warn: 3, crit: 6, min: null, max: null },
    ])
  })
  it('parses value units and multiple space-separated metrics', () => {
    const p = parsePerfdata('rta=0.5ms;100;500;0; pl=0%;20;60;0;100')
    expect(p).toHaveLength(2)
    expect(p[0]).toMatchObject({ label: 'rta', value: 0.5, unit: 'ms', warn: 100, crit: 500, min: 0 })
    expect(p[1]).toMatchObject({ label: 'pl', value: 0, unit: '%', crit: 60, max: 100 })
  })
  it('handles quoted labels containing spaces', () => {
    const p = parsePerfdata("'disk usage'=45%;80;90;0;100")
    expect(p[0]).toMatchObject({ label: 'disk usage', value: 45, unit: '%', warn: 80, crit: 90 })
  })
  it('returns empty for undefined/empty/garbage input', () => {
    expect(parsePerfdata(undefined)).toEqual([])
    expect(parsePerfdata('')).toEqual([])
    expect(parsePerfdata('not-perfdata')).toEqual([])
  })
})
