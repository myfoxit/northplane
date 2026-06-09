import { describe, it, expect } from 'vitest'
import * as z from 'zod'
import {
  APIError, parseError, parseEtag, validate,
} from './api'

// Minimal Response stand-in: parseError/validate only touch .status,
// .statusText and .json(), so a typed cast of a plain object is enough and
// avoids needing a real fetch.
function fakeRes(opts: { status: number; statusText?: string; json: () => Promise<unknown> }): Response {
  return {
    status: opts.status,
    statusText: opts.statusText ?? '',
    json: opts.json,
  } as unknown as Response
}

describe('APIError', () => {
  it('carries status/code/detail and uses title as the message', () => {
    const e = new APIError(409, 'conflict', 'version conflict', 'etag mismatch')
    expect(e).toBeInstanceOf(Error)
    expect(e.status).toBe(409)
    expect(e.code).toBe('conflict')
    expect(e.message).toBe('version conflict')
    expect(e.detail).toBe('etag mismatch')
  })
})

describe('parseError', () => {
  it('maps an RFC 9457 problem body to APIError fields', async () => {
    const res = fakeRes({
      status: 422,
      json: async () => ({ code: 'validation', title: 'bad input', detail: 'name required' }),
    })
    const e = await parseError(res)
    expect(e.status).toBe(422)
    expect(e.code).toBe('validation')
    expect(e.message).toBe('bad input')
    expect(e.detail).toBe('name required')
  })

  it('falls back to statusText + unknown code when the body is not JSON', async () => {
    const res = fakeRes({
      status: 500,
      statusText: 'Internal Server Error',
      json: async () => { throw new SyntaxError('unexpected token') },
    })
    const e = await parseError(res)
    expect(e.status).toBe(500)
    expect(e.code).toBe('unknown')
    expect(e.message).toBe('Internal Server Error')
    expect(e.detail).toBe('')
  })

  it('defaults missing problem fields', async () => {
    const res = fakeRes({ status: 400, statusText: 'Bad Request', json: async () => ({}) })
    const e = await parseError(res)
    expect(e.code).toBe('unknown')
    expect(e.message).toBe('Bad Request')
    expect(e.detail).toBe('')
  })
})

describe('parseEtag', () => {
  it.each([
    ['"7"', 7],
    ['7', 7],
    ['W/"12"', 12],
    ['"0"', 0],
  ])('extracts %s -> %d', (header, expected) => {
    expect(parseEtag(header)).toBe(expected)
  })

  it('returns 0 for a missing header', () => {
    expect(parseEtag(null)).toBe(0)
  })

  it('returns 0 for a non-numeric header', () => {
    expect(parseEtag('"abc"')).toBe(0)
    expect(parseEtag('')).toBe(0)
  })
})

describe('validate', () => {
  const schema = z.object({ name: z.string(), n: z.number() })

  it('returns the parsed data on success', () => {
    expect(validate({ name: 'a', n: 1 }, schema)).toEqual({ name: 'a', n: 1 })
  })

  it('passes the value through unchanged when no schema is given', () => {
    const v = { anything: true }
    expect(validate(v)).toBe(v)
  })

  it('throws a 502 invalid_response APIError on a schema mismatch', () => {
    try {
      validate({ name: 'a', n: 'oops' }, schema)
      throw new Error('expected validate to throw')
    } catch (err) {
      expect(err).toBeInstanceOf(APIError)
      const e = err as APIError
      expect(e.status).toBe(502)
      expect(e.code).toBe('invalid_response')
      expect(e.detail).toContain('n')
    }
  })
})
