// Instance branding: the console look is a property of the INSTALLATION, not
// of the user and not of the tenant. syncBrandingFromServer() adopts
// GET /branding on sign-in; changing either axis writes the whole document
// back with PUT /branding; adopting a server value must never bounce a write
// back at the server.
import { describe, it, expect, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from './test/msw'
import { syncBrandingFromServer } from './branding'
import { setTheme } from './theme'
import { setMode } from './mode'

// Records every PUT so a test can assert both the body and the absence of one.
function capturePuts(): { bodies: unknown[] } {
  const bodies: unknown[] = []
  server.use(http.put('/api/v1/branding', async ({ request }) => {
    const body = await request.json()
    bodies.push(body)
    return HttpResponse.json(body as Record<string, unknown>)
  }))
  return { bodies }
}

const branding = (b: Record<string, string>) =>
  http.get('/api/v1/branding', () => HttpResponse.json(b))

// The write-through is fire-and-forget, so give the PUT a turn to land.
const settle = () => new Promise((r) => setTimeout(r, 0))

describe('instance branding', () => {
  beforeEach(() => {
    localStorage.clear()
    delete document.documentElement.dataset.theme
    document.documentElement.classList.remove('light')
  })

  it('adopts the instance branding on sync, without writing back', async () => {
    const put = capturePuts()
    server.use(branding({ theme: 'neonMint', mode: 'light' }))

    await syncBrandingFromServer()
    await settle()

    expect(document.documentElement.dataset.theme).toBe('neonMint')
    expect(document.documentElement.classList.contains('light')).toBe(true)
    expect(put.bodies).toEqual([])
  })

  it('writes both axes through when the theme changes', async () => {
    server.use(branding({ theme: 'neonMint', mode: 'dark' }))
    await syncBrandingFromServer()
    const put = capturePuts()

    setTheme('arcticBlue')
    await settle()

    expect(document.documentElement.dataset.theme).toBe('arcticBlue')
    // The document holds both axes, so a partial write would drop the mode.
    expect(put.bodies).toEqual([{ theme: 'arcticBlue', mode: 'dark' }])
  })

  it('writes both axes through when the mode changes', async () => {
    server.use(branding({ theme: 'plumGold', mode: 'dark' }))
    await syncBrandingFromServer()
    const put = capturePuts()

    setMode('light')
    await settle()

    expect(put.bodies).toEqual([{ theme: 'plumGold', mode: 'light' }])
  })

  it('does not re-write a value that already matches the server', async () => {
    server.use(branding({ theme: 'plumGold', mode: 'dark' }))
    await syncBrandingFromServer()
    const put = capturePuts()

    setTheme('plumGold') // same as the instance value
    await settle()

    expect(put.bodies).toEqual([])
  })

  it('ignores an unusable document from the server', async () => {
    server.use(branding({ theme: 'not-a-theme', mode: 'sideways' }))

    await syncBrandingFromServer()

    expect(document.documentElement.dataset.theme).toBeUndefined()
    expect(document.documentElement.classList.contains('light')).toBe(false)
  })

  it('keeps the local look when the instance has no branding yet', async () => {
    server.use(branding({}))
    const put = capturePuts()

    await syncBrandingFromServer()
    await settle()

    expect(document.documentElement.dataset.theme).toBeUndefined()
    expect(put.bodies).toEqual([])
  })
})
