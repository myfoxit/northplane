// Verifies the tabbed create/edit form (FORM-1/3/5) and the compact multi-select
// pickers (FORM-2): Basis is the default tab and shows only the essentials, the
// rest of the spec lives behind tabs, the action bar is always present, and the
// notification pickers are chip comboboxes rather than two-up dual-lists.
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../../test/msw'
import { renderWithProviders } from '../../test/render'
import { ObjectFormDialog } from './ObjectForm'

// The spec sections lazily query resource names as their tab mounts; MSW errors
// on unhandled requests, so stub every endpoint any tab might hit.
function mockResources() {
  server.use(
    http.get(/check-commands:builtins/, () => HttpResponse.json(['icmp'])),
    http.get('/api/v1/templates', () => HttpResponse.json({ items: [{ name: 'generic-host' }] })),
    http.get('/api/v1/check-commands', () => HttpResponse.json({ items: [{ name: 'check_disk' }] })),
    http.get('/api/v1/contact-groups', () => HttpResponse.json({ items: [{ name: 'ops' }] })),
    http.get('/api/v1/contacts', () => HttpResponse.json({ items: [{ name: 'max' }] })),
    http.get('/api/v1/time-periods', () => HttpResponse.json({ items: [{ name: '24x7' }] })),
    http.get('/api/v1/hosts', () => HttpResponse.json({ items: [{ name: 'db01' }] })),
  )
}

describe('<ObjectFormDialog /> — tabbed create form', () => {
  it('defaults to Basis with only the essentials; action bar stays, deep config is behind tabs', async () => {
    mockResources()
    const user = userEvent.setup()
    renderWithProviders(<ObjectFormDialog open kind="host" onClose={() => {}} />)

    const dialog = await screen.findByRole('dialog')
    for (const name of ['Basics', 'Check', 'Notifications', 'Advanced']) {
      expect(within(dialog).getByRole('tab', { name })).toBeInTheDocument()
    }

    // Essentials visible on the default tab…
    expect(within(dialog).getByPlaceholderText('web01')).toBeInTheDocument() // name
    expect(within(dialog).getByPlaceholderText('10.0.0.1 / host.example.com')).toBeInTheDocument() // address
    // …but the interval/runbook config is NOT — progressive disclosure (FORM-3).
    expect(within(dialog).queryByText(/Retry-Intervall|Retry interval/)).not.toBeInTheDocument()
    expect(within(dialog).queryByText('Runbook')).not.toBeInTheDocument()

    // The action bar is present and gated on the required name (FORM-5).
    const submit = within(dialog).getByRole('button', { name: /Anlegen|Create/ })
    expect(submit).toBeDisabled()
    await user.type(within(dialog).getByPlaceholderText('web01'), 'web-42')
    expect(submit).toBeEnabled()
  })

  it('the check tab reveals the check-command selector + interval block', async () => {
    mockResources()
    const user = userEvent.setup()
    renderWithProviders(<ObjectFormDialog open kind="host" onClose={() => {}} />)
    const dialog = await screen.findByRole('dialog')

    await user.click(within(dialog).getByRole('tab', { name: 'Check' }))
    // The interval grid moved here (its retry label is the FORM-4 case).
    expect(await within(dialog).findByText(/Retry-Intervall|Retry interval/)).toBeInTheDocument()
    // …and the check-command kind selector (a new object defaults to passive).
    expect(within(dialog).getAllByRole('combobox').length).toBeGreaterThan(0)
  })

  it('notification pickers are compact chip comboboxes, not two-up dual-lists (FORM-2)', async () => {
    mockResources()
    const user = userEvent.setup()
    renderWithProviders(<ObjectFormDialog open kind="host" onClose={() => {}} />)
    const dialog = await screen.findByRole('dialog')

    await user.click(within(dialog).getByRole('tab', { name: 'Notifications' }))
    // Compact combobox triggers (their placeholders) are present…
    expect(await within(dialog).findByRole('combobox', { name: /Add group/ })).toBeInTheDocument()
    expect(within(dialog).getByRole('combobox', { name: /Add contact/ })).toBeInTheDocument()
    // …and the DualListPicker's move controls (› » « ‹) are NOT.
    expect(within(dialog).queryByRole('button', { name: '›' })).not.toBeInTheDocument()
  })
})
