import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from './test/msw'
import { activeTenantId, setActiveTenantId } from './tenant'
import { api } from './api'

describe('tenant context', () => {
  beforeEach(() => setActiveTenantId(null))
  afterEach(() => setActiveTenantId(null))

  it('round-trips the active customer id through localStorage', () => {
    expect(activeTenantId()).toBeNull()
    setActiveTenantId('cust-1')
    expect(activeTenantId()).toBe('cust-1')
    expect(localStorage.getItem('np.activeTenant')).toBe('cust-1')
    setActiveTenantId(null)
    expect(activeTenantId()).toBeNull()
    expect(localStorage.getItem('np.activeTenant')).toBeNull()
  })
})

describe('api() X-Northplane-Tenant header', () => {
  let seen: string | null = null
  beforeEach(() => {
    seen = null
    setActiveTenantId(null)
    server.use(
      http.get('/api/v1/whoami', ({ request }) => {
        seen = request.headers.get('X-Northplane-Tenant')
        return HttpResponse.json({
          actorType: 'user', actorId: 'u1', name: 'op', tenantId: 't0', permissions: [],
        })
      }),
    )
  })
  afterEach(() => setActiveTenantId(null))

  it('omits the header when no customer is active', async () => {
    await api('/whoami')
    expect(seen).toBeNull()
  })

  it('sends the active customer id as the header when one is selected', async () => {
    setActiveTenantId('cust-42')
    await api('/whoami')
    expect(seen).toBe('cust-42')
  })
})
