// Verifies the tabbed create/edit form (FORM-1/3/5) and the compact multi-select
// pickers (FORM-2): Basis is the default tab and shows only the essentials, the
// rest of the spec lives behind tabs, the action bar is always present, and the
// notification pickers are chip comboboxes rather than two-up dual-lists.
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../../test/msw'
import { renderWithProviders } from '../../test/render'
import { ObjectFormDialog } from './ObjectForm'
import type { NPObject } from '../../types'

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

describe('<ObjectFormDialog /> — duplicate (copyFrom)', () => {
  it('seeds a CREATE from the source service: fresh name, same host/folder/labels/spec, POSTs to /services', async () => {
    mockResources()
    let posted: Record<string, unknown> | null = null
    server.use(
      http.get('/api/v1/hosts', () => HttpResponse.json({ items: [{ id: 'h1', name: 'db01' }] })),
      http.post('/api/v1/services', async ({ request }) => {
        posted = await request.json() as Record<string, unknown>
        return HttpResponse.json({ id: 'new', ...posted }, { status: 201 })
      }),
    )
    const src: NPObject = {
      id: 'svc-1', tenantId: 't1', kind: 'service', name: 'http', hostId: 'h1', hostName: 'db01',
      folder: '/web', labels: { env: 'prod' },
      spec: { checkCommand: 'builtin:http', interval: '30s' }, version: 3,
    }
    const user = userEvent.setup()
    renderWithProviders(
      <ObjectFormDialog open kind="service" copyFrom={src} existingNames={['http', 'http-copy']} onClose={() => {}} />,
    )
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/^(Duplicate|Duplizieren): http$/)).toBeInTheDocument()

    // The name skips the taken variants and stays editable (create mode, no
    // rename lock); folder and labels mirror the source.
    const name = within(dialog).getByDisplayValue('http-copy-2')
    expect(name).toBeEnabled()
    expect(within(dialog).getByDisplayValue('/web')).toBeInTheDocument()
    expect(within(dialog).getByDisplayValue('prod')).toBeInTheDocument()
    // The host is pre-selected (and, unlike edit, changeable).
    const hostPicker = await within(dialog).findByRole('combobox')
    expect(hostPicker).toHaveTextContent('db01')
    expect(hostPicker).toBeEnabled()

    // Submit is enabled straight away and creates (POST), not updates.
    const submit = within(dialog).getByRole('button', { name: /Anlegen|Create/ })
    expect(submit).toBeEnabled()
    await user.click(submit)
    await waitFor(() => expect(posted).not.toBeNull())
    expect(posted).toEqual({
      name: 'http-copy-2', host: 'h1', folder: '/web', labels: { env: 'prod' },
      spec: { checkCommand: 'builtin:http', interval: '30s' },
    })
  })

  it('a host copy pre-fills the address and spec under a "-copy" name', async () => {
    mockResources()
    const src: NPObject = {
      id: 'h-1', tenantId: 't1', kind: 'host', name: 'web01', folder: '/', labels: {},
      spec: { address: '10.0.0.1', checkCommand: 'builtin:icmp' }, version: 1,
    }
    renderWithProviders(<ObjectFormDialog open kind="host" copyFrom={src} onClose={() => {}} />)
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByDisplayValue('web01-copy')).toBeEnabled()
    expect(within(dialog).getByDisplayValue('10.0.0.1')).toBeInTheDocument()
  })
})
