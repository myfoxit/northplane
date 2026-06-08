import { describe, it, expect } from 'vitest'
import { isDuration } from './duration'

describe('isDuration', () => {
  it('accepts empty string (optional field)', () => {
    expect(isDuration('')).toBe(true)
  })

  it.each([
    '30s', '5m', '1h', '1h30m', '500ms', '100us', '100µs', '10ns',
    '2h45m30s', '1.5h', '0.5s', '90m', '1h0m0s',
  ])('accepts valid Go duration %j', (v) => {
    expect(isDuration(v)).toBe(true)
  })

  it.each([
    '30', '5 m', 's', 'abc', '1d', '1w', '-5m', '5min', '1h 30m',
    '1.h', 'm5', '5sm?', ' 5s', '5s ', '1e3s',
  ])('rejects invalid duration %j', (v) => {
    expect(isDuration(v)).toBe(false)
  })

  it('rejects a plain number with no unit', () => {
    expect(isDuration('100')).toBe(false)
  })
})
