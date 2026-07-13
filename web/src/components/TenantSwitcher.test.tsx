import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import { server } from '../test/msw'
import { renderWithProviders } from '../test/render'
import { setActiveTenantId } from '../tenant'
import { TenantSwitcher } from './TenantSwitcher'

function whoami(perms: string[]) {
  return http.get('/api/v1/whoami', () =>
    HttpResponse.json({
      actorType: 'user', actorId: 'u1', name: 'op', tenantId: 't0', permissions: perms,
    }))
}
function tenantsList(items: Array<{ id: string; name: string; slug: string }>) {
  return http.get('/api/v1/tenants', () => HttpResponse.json({ items }))
}

describe('<TenantSwitcher />', () => {
  beforeEach(() => setActiveTenantId(null))
  afterEach(() => setActiveTenantId(null))

  it('renders nothing for an operator without admin:tenants', async () => {
    let whoamiHit = false
    server.use(
      http.get('/api/v1/whoami', () => {
        whoamiHit = true
        return HttpResponse.json({
          actorType: 'user', actorId: 'u1', name: 'op', tenantId: 't0',
          permissions: ['objects:read', 'alerts:ack'],
        })
      }),
    )
    renderWithProviders(<TenantSwitcher />)
    await waitFor(() => expect(whoamiHit).toBe(true))
    // Even after the identity loads, a non-cross-tenant operator sees no switcher.
    await waitFor(() => expect(screen.queryByLabelText(/customer|kunde/i)).toBeNull())
  })

  it('shows the customer switcher for an admin:tenants operator', async () => {
    server.use(
      whoami(['admin:tenants']),
      tenantsList([{ id: 't-acme', name: 'Acme GmbH', slug: 'acme' }]),
    )
    renderWithProviders(<TenantSwitcher />)
    expect(await screen.findByLabelText(/customer|kunde/i)).toBeInTheDocument()
  })

  it('shows the switcher for a super-admin holding the "*:*" grant', async () => {
    // Regression: the built-in admin role holds "*:*", which the old literal
    // check (p === 'admin:tenants' || p === '*') missed — hiding the console
    // from the very operator allowed to use it. Wildcard matching (see
    // permissions.ts) now mirrors the backend.
    server.use(
      whoami(['*:*']),
      tenantsList([{ id: 't-acme', name: 'Acme GmbH', slug: 'acme' }]),
    )
    renderWithProviders(<TenantSwitcher />)
    expect(await screen.findByLabelText(/customer|kunde/i)).toBeInTheDocument()
  })

  it('flags the active customer with a clear indicator', async () => {
    setActiveTenantId('t-acme')
    server.use(
      whoami(['*']),
      tenantsList([{ id: 't-acme', name: 'Acme GmbH', slug: 'acme' }]),
    )
    renderWithProviders(<TenantSwitcher />)
    // The indicator names the active customer alongside the label, so match
    // both together — "Acme GmbH" alone also appears in the select trigger.
    expect(
      await screen.findByText(/(active customer|aktiver kunde).*acme gmbh/i),
    ).toBeInTheDocument()
  })
})
