import { describe, it, expect } from 'vitest'
import {
  stateLabel, stateIcon, stateColor, sevColor,
  svcStates, hostStates,
} from './types'

describe('stateLabel', () => {
  it('maps service states 0..3', () => {
    expect(svcStates.map((_, i) => stateLabel('service', i)))
      .toEqual(['OK', 'WARNING', 'CRITICAL', 'UNKNOWN'])
  })

  it('maps host states 0..3', () => {
    expect(hostStates.map((_, i) => stateLabel('host', i)))
      .toEqual(['UP', 'DOWN', 'UNREACHABLE', 'UNKNOWN'])
  })

  it('falls back to UNKNOWN for out-of-range / negative state', () => {
    expect(stateLabel('service', 9)).toBe('UNKNOWN')
    expect(stateLabel('host', 9)).toBe('UNKNOWN')
    expect(stateLabel('service', -1)).toBe('UNKNOWN')
  })
})

describe('stateIcon', () => {
  it('uses the host glyph set for hosts', () => {
    expect([0, 1, 2, 3].map((s) => stateIcon('host', s))).toEqual(['●', '✕', '◌', '?'])
  })

  it('uses the service glyph set for services', () => {
    expect([0, 1, 2, 3].map((s) => stateIcon('service', s))).toEqual(['●', '▲', '✕', '?'])
  })

  it('falls back to ? for unknown state index', () => {
    expect(stateIcon('host', 42)).toBe('?')
    expect(stateIcon('service', 42)).toBe('?')
  })
})

describe('stateColor', () => {
  it('maps host states to the host palette', () => {
    expect([0, 1, 2, 3].map((s) => stateColor('host', s))).toEqual([
      'text-success', 'text-danger', 'text-muted-foreground', 'text-muted-foreground',
    ])
  })

  it('maps service states to the service palette', () => {
    expect([0, 1, 2, 3].map((s) => stateColor('service', s))).toEqual([
      'text-success', 'text-warning', 'text-danger', 'text-muted-foreground',
    ])
  })

  it('returns an empty class for out-of-range state', () => {
    expect(stateColor('host', 99)).toBe('')
    expect(stateColor('service', 99)).toBe('')
  })
})

describe('sevColor', () => {
  it('maps each known severity to its token set', () => {
    expect(sevColor('critical')).toContain('text-danger')
    expect(sevColor('warning')).toContain('text-warning')
    expect(sevColor('ok')).toContain('text-success')
  })

  it('falls back to the info/primary tint for info and undefined', () => {
    const info = 'text-primary'
    expect(sevColor('info')).toContain(info)
    expect(sevColor(undefined)).toContain(info)
  })
})
