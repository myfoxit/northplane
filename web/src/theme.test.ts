// Branding persistence: the colour theme and the light/dark mode are per-USER
// settings, written through to /users/me/preferences (extra.theme / extra.mode)
// and adopted from there on sync — so a user's look follows them to a new
// browser instead of living only in one localStorage. Adopting a server value
// must NOT write back (that would be a pointless PUT, and two tabs syncing
// would ping-pong).
import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from './test/msw'
import { syncPreferencesFromServer } from './settings'
import { setTheme } from './theme'
import { setMode } from './mode'

// Records every PUT so a test can assert both the body and the absence of one.
function capturePuts(): { bodies: unknown[] } {
  const bodies: unknown[] = []
  server.use(http.put('/api/v1/users/me/preferences', async ({ request }) => {
    const body = await request.json()
    bodies.push(body)
    return HttpResponse.json(body as Record<string, unknown>)
  }))
  return { bodies }
}

// The write-through is fire-and-forget, so give the PUT a turn to land.
const settle = () => new Promise((r) => setTimeout(r, 0))

describe('branding persistence', () => {
  beforeEach(() => {
    localStorage.clear()
    delete document.documentElement.dataset.theme
    document.documentElement.classList.remove('light')
  })

  it('writes a theme change through to the account', async () => {
    server.use(http.get('/api/v1/users/me/preferences', () => HttpResponse.json({})))
    await syncPreferencesFromServer()
    const put = capturePuts()

    setTheme('arcticBlue')
    await settle()

    expect(document.documentElement.dataset.theme).toBe('arcticBlue')
    expect(localStorage.getItem('np.theme')).toBe('arcticBlue')
    expect(put.bodies).toEqual([{ extra: { theme: 'arcticBlue' } }])
  })

  it('writes a mode change through to the account', async () => {
    server.use(http.get('/api/v1/users/me/preferences', () => HttpResponse.json({})))
    await syncPreferencesFromServer()
    const put = capturePuts()

    setMode('light')
    await settle()

    expect(document.documentElement.classList.contains('light')).toBe(true)
    expect(localStorage.getItem('np.mode')).toBe('light')
    expect(put.bodies).toEqual([{ extra: { mode: 'light' } }])
  })

  it('adopts the account branding on sync without writing back', async () => {
    const put = capturePuts()
    server.use(http.get('/api/v1/users/me/preferences', () =>
      HttpResponse.json({ extra: { theme: 'neonMint', mode: 'light' } })))

    await syncPreferencesFromServer()
    await settle()

    expect(document.documentElement.dataset.theme).toBe('neonMint')
    expect(localStorage.getItem('np.theme')).toBe('neonMint')
    expect(document.documentElement.classList.contains('light')).toBe(true)
    expect(localStorage.getItem('np.mode')).toBe('light')
    expect(put.bodies).toEqual([])
  })

  it('preserves unrelated preference keys when branding changes', async () => {
    server.use(http.get('/api/v1/users/me/preferences', () =>
      HttpResponse.json({ refreshIntervalMs: 10_000, extra: { somethingElse: 'keep' } })))
    await syncPreferencesFromServer()
    const put = capturePuts()

    setTheme('plumGold')
    await settle()

    expect(put.bodies).toEqual([
      { refreshIntervalMs: 10_000, extra: { somethingElse: 'keep', theme: 'plumGold' } },
    ])
  })

  it('ignores an unknown theme id from the server', async () => {
    server.use(http.get('/api/v1/users/me/preferences', () =>
      HttpResponse.json({ extra: { theme: 'not-a-theme' } })))

    await syncPreferencesFromServer()

    expect(document.documentElement.dataset.theme).toBeUndefined()
  })
})
