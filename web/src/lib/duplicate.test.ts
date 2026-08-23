import { describe, it, expect } from 'vitest'
import { copyName, duplicateDoc, stripMeta } from './duplicate'

// The catalog resolves to EN under jsdom (navigator.language = en-US), so
// copySuffix is "(copy)" here; the DE variant is the same shape "(Kopie)".

describe('stripMeta', () => {
  it('drops the store envelope and keeps everything else', () => {
    const doc = {
      id: '01a0-…', tenantId: 't1', version: 7,
      createdAt: '2026-08-01T00:00:00Z', updatedAt: '2026-08-02T00:00:00Z',
      name: 'ops', members: ['max'], config: { url: 'https://x' },
    }
    expect(stripMeta(doc)).toEqual({ name: 'ops', members: ['max'], config: { url: 'https://x' } })
  })

  it('drops caller-named extras (e.g. the system flag on roles)', () => {
    expect(stripMeta({ name: 'admin', system: true, permissions: ['*:*'] }, ['system']))
      .toEqual({ name: 'admin', permissions: ['*:*'] })
  })

  it('does not mutate its input', () => {
    const doc = { id: 'x', name: 'a' }
    stripMeta(doc)
    expect(doc).toEqual({ id: 'x', name: 'a' })
  })
})

describe('copyName', () => {
  it.each([
    ['web01', 'web01-copy'],
    ['http', 'http-copy'],
    ['bereitschaft', 'bereitschaft-copy'],
    ['10.0.0.1', '10.0.0.1-copy'],
    ['alarm@doktrace.com', 'alarm@doktrace.com-copy'],
  ])('slug-like %j → %j', (name, want) => {
    expect(copyName(name)).toBe(want)
  })

  it.each([
    ['Ops wall', 'Ops wall (copy)'],
    ['Monthly availability', 'Monthly availability (copy)'],
    ['disk /', 'disk / (copy)'],
  ])('free-text %j → %j', (name, want) => {
    expect(copyName(name)).toBe(want)
  })

  it('numbers the suffix until it is free', () => {
    expect(copyName('web01', ['web01', 'web01-copy'])).toBe('web01-copy-2')
    expect(copyName('web01', ['web01-copy', 'web01-copy-2', 'web01-copy-3'])).toBe('web01-copy-4')
    expect(copyName('Ops wall', ['Ops wall (copy)'])).toBe('Ops wall (copy 2)')
    expect(copyName('Ops wall', ['Ops wall (copy)', 'Ops wall (copy 2)'])).toBe('Ops wall (copy 3)')
  })

  it('ignores names that are taken but irrelevant', () => {
    expect(copyName('web01', ['web02', 'web02-copy'])).toBe('web01-copy')
  })
})

describe('duplicateDoc', () => {
  it('is an envelope-free copy under the fresh name', () => {
    const src = { id: 'abc', version: 3, createdAt: 'x', updatedAt: 'y', tenantId: 't', name: 'mail', type: 'email', enabled: true }
    expect(duplicateDoc(src, ['mail'])).toEqual({ name: 'mail-copy', type: 'email', enabled: true })
  })

  it('applies extra strips and numbering together', () => {
    const src = { id: 'abc', name: 'admin', system: true, permissions: ['a'] }
    expect(duplicateDoc(src, ['admin-copy'], ['system'])).toEqual({ name: 'admin-copy-2', permissions: ['a'] })
  })
})
