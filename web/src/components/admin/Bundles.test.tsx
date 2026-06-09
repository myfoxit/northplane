// Config-bundles tab: plan renders the diff table, apply uses the
// two-phase token, validation errors surface via FormError.
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../../test/msw'
import { renderWithProviders } from '../../test/render'
import { BundlesTab } from './Bundles'

const planResponse = {
  plan: [{ action: 'create', kind: 'Host', name: 'web-01', diff: { address: [null, '10.0.0.1'] } }],
  warnings: ['Host web-01 has no checks'],
  applyToken: 'ap_tok1',
}

async function typeYamlAndPlan(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByPlaceholderText(/kind: Host/), 'kind: Host')
  await user.click(screen.getByRole('button', { name: /Planen/ }))
}

describe('<BundlesTab />', () => {
  it('plans a bundle and renders actions + warnings', async () => {
    server.use(http.post('/api/v1/config/bundles\\:plan', () => HttpResponse.json(planResponse)))
    const user = userEvent.setup()
    renderWithProviders(<BundlesTab />)
    await typeYamlAndPlan(user)
    expect(await screen.findByText('web-01')).toBeInTheDocument()
    expect(screen.getByText('create')).toBeInTheDocument()
    expect(screen.getByText(/has no checks/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Anwenden \(1/ })).toBeInTheDocument()
  })

  it('applies with the plan token', async () => {
    let applyURL = ''
    server.use(
      http.post('/api/v1/config/bundles\\:plan', () => HttpResponse.json(planResponse)),
      http.post('/api/v1/config/bundles\\:apply', ({ request }) => {
        applyURL = request.url
        return HttpResponse.json({ plan: planResponse.plan })
      }),
    )
    const user = userEvent.setup()
    renderWithProviders(<BundlesTab />)
    await typeYamlAndPlan(user)
    await user.click(await screen.findByRole('button', { name: /Anwenden/ }))
    await waitFor(() => expect(screen.getByText(/angewendet \(1/)).toBeInTheDocument())
    expect(applyURL).toContain('applyToken=ap_tok1')
  })

  it('surfaces validation problems', async () => {
    server.use(http.post('/api/v1/config/bundles\\:plan', () =>
      HttpResponse.json({ code: 'np:validation', title: 'validation failed', detail: 'unknown kind Wombat' }, { status: 422 })))
    const user = userEvent.setup()
    renderWithProviders(<BundlesTab />)
    await typeYamlAndPlan(user)
    expect(await screen.findByText(/unknown kind Wombat/)).toBeInTheDocument()
  })
})
