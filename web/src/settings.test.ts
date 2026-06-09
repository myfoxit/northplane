// Server-backed refresh-cadence setting: the localStorage cache boots the
// value, syncPreferencesFromServer() adopts the authoritative server copy,
// and setRefreshInterval() writes through (PUT /users/me/preferences).
import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from './test/msw'
import { syncPreferencesFromServer, setRefreshInterval } from './settings'

describe('settings: server-backed preferences', () => {
  beforeEach(() => localStorage.clear())

  it('adopts the server refresh interval on sync', async () => {
    server.use(http.get('/api/v1/users/me/preferences', () =>
      HttpResponse.json({ refreshIntervalMs: 10_000 })))
    await syncPreferencesFromServer()
    expect(localStorage.getItem('np.refreshInterval')).toBe('10000')
  })

  it('maps server 0 to "off"', async () => {
    server.use(http.get('/api/v1/users/me/preferences', () =>
      HttpResponse.json({ refreshIntervalMs: 0 })))
    await syncPreferencesFromServer()
    expect(localStorage.getItem('np.refreshInterval')).toBe('off')
  })

  it('keeps the cached value when the server has no preference', async () => {
    localStorage.setItem('np.refreshInterval', '5000')
    server.use(http.get('/api/v1/users/me/preferences', () => HttpResponse.json({})))
    await syncPreferencesFromServer()
    expect(localStorage.getItem('np.refreshInterval')).toBe('5000')
  })

  it('writes a change through to the server, preserving other keys', async () => {
    server.use(http.get('/api/v1/users/me/preferences', () =>
      HttpResponse.json({ refreshIntervalMs: 30_000, extra: { theme: 'dark' } })))
    await syncPreferencesFromServer()

    let sent: unknown
    const done = new Promise<void>((resolve) => {
      server.use(http.put('/api/v1/users/me/preferences', async ({ request }) => {
        sent = await request.json()
        resolve()
        return HttpResponse.json(sent as Record<string, unknown>)
      }))
    })
    setRefreshInterval(false)
    await done
    expect(sent).toEqual({ refreshIntervalMs: 0, extra: { theme: 'dark' } })
    expect(localStorage.getItem('np.refreshInterval')).toBe('off')
  })
})
