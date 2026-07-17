// AlertsPage: list rendering with severity/status metadata, empty + error
// states, the ack dialog flow (POST /alerts/{id}:ack) plus quick resolve,
// and the manual trigger-alarm dialog (POST /alerts with np.* labels).
import { describe, it, expect, beforeAll } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../test/msw'
import { renderWithProviders } from '../test/render'
import { AlertsPage } from './Alerts'
import type { Alert } from '../types'

// Radix Select drives its open/select logic through the pointer-capture
// APIs, which jsdom does not implement — polyfill just for this suite.
beforeAll(() => {
  Element.prototype.hasPointerCapture ??= () => false
  Element.prototype.setPointerCapture ??= () => {}
  Element.prototype.releasePointerCapture ??= () => {}
})

const alertsFix: Alert[] = [
  {
    id: 'al-1', status: 'open', severity: 'critical', title: 'Disk voll auf web-01',
    dedupKey: 'disk-web-01', openedAt: '2026-06-09T09:00:00Z',
  },
  {
    id: 'al-2', status: 'acked', severity: 'warning', title: 'Zertifikat läuft bald ab',
    openedAt: '2026-06-08T09:00:00Z', ackedBy: 'admin',
  },
]

const listAlerts = (items: Alert[]) =>
  http.get('/api/v1/alerts', () => HttpResponse.json({ items }))

describe('<AlertsPage />', () => {
  it('renders one row per alert with severity, status and dedup key', async () => {
    server.use(listAlerts(alertsFix))
    renderWithProviders(<AlertsPage />)

    expect(await screen.findByText('Disk voll auf web-01')).toBeInTheDocument()
    expect(screen.getByText('Zertifikat läuft bald ab')).toBeInTheDocument()
    // Severity badges carry the severity word (never color-only).
    expect(screen.getByText('critical')).toBeInTheDocument()
    expect(screen.getByText('warning')).toBeInTheDocument()
    // Status line: acked alerts name the acker, dedup key is shown.
    expect(screen.getByText(/acked by admin/)).toBeInTheDocument()
    expect(screen.getByText(/disk-web-01/)).toBeInTheDocument()
    // Header count reflects the filtered row count.
    expect(screen.getByRole('heading')).toHaveTextContent('(2)')
  })

  it('shows the empty state when no alerts match', async () => {
    // Default /alerts handler already returns { items: [] }.
    renderWithProviders(<AlertsPage />)
    expect(await screen.findByText(/no active alerts|caught up/i)).toBeInTheDocument()
    expect(screen.getByRole('heading')).toHaveTextContent('(0)')
  })

  it('renders an ErrorState with retry when the request 500s', async () => {
    server.use(
      http.get('/api/v1/alerts', () =>
        HttpResponse.json(
          { code: 'internal', title: 'boom', detail: 'alert store down' },
          { status: 500 },
        )),
    )
    renderWithProviders(<AlertsPage />)
    expect(await screen.findByText(/failed to load|laden fehlgeschlagen/i)).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText(/alert store down/)).toBeInTheDocument())
    expect(screen.getByRole('button', { name: /retry|erneut/i })).toBeInTheDocument()
  })

  it('acks an open alert through the dialog with a comment', async () => {
    let ackedId = ''
    let ackedBody: Record<string, unknown> | undefined
    server.use(
      listAlerts(alertsFix),
      http.post('/api/v1/alerts/:id\\:ack', async ({ params, request }) => {
        ackedId = String(params.id)
        ackedBody = await request.json() as Record<string, unknown>
        return HttpResponse.json({})
      }),
    )
    // pointerEventsCheck off: Radix sets pointer-events:none on <body> while
    // the modal is open, which would block userEvent's hit-testing in jsdom.
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    renderWithProviders(<AlertsPage />)

    // Only the open alert offers the ack button.
    await user.click(await screen.findByRole('button', { name: /acknowledge|quittieren/i }))
    const dialog = await screen.findByRole('dialog')
    // The dialog names the alert and states the consequence.
    expect(dialog).toHaveTextContent('Disk voll auf web-01')
    expect(dialog).toHaveTextContent(/escalations will stop|eskalationen werden gestoppt/i)

    await user.type(within(dialog).getByPlaceholderText(/comment|kommentar/i), 'wird bearbeitet')
    await user.click(within(dialog).getByRole('button', { name: /acknowledge|quittieren/i }))

    await waitFor(() => expect(ackedId).toBe('al-1'))
    expect(ackedBody).toMatchObject({ comment: 'wird bearbeitet' })
    // Successful ack closes the dialog.
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('resolves an alert via the row button', async () => {
    let resolvedId = ''
    server.use(
      listAlerts(alertsFix),
      http.post('/api/v1/alerts/:id\\:resolve', ({ params }) => {
        resolvedId = String(params.id)
        return HttpResponse.json({})
      }),
    )
    const user = userEvent.setup()
    renderWithProviders(<AlertsPage />)

    // Both rows have a resolve button; the first belongs to al-1.
    const buttons = await screen.findAllByRole('button', { name: /resolve|schließen/i })
    await user.click(buttons[0]!)
    await waitFor(() => expect(resolvedId).toBe('al-1'))
  })

  it('raises a manual alarm through the trigger dialog incl. np.* labels', async () => {
    let posted: Record<string, unknown> | undefined
    server.use(
      listAlerts([]),
      http.get('/api/v1/escalation-policies', () =>
        HttpResponse.json({ items: [{ name: 'standard-oncall', steps: [] }] })),
      http.post('/api/v1/alerts', async ({ request }) => {
        posted = await request.json() as Record<string, unknown>
        return HttpResponse.json(
          { id: 'al-new', status: 'open', severity: 'critical', title: 'Feueralarm Halle 2', openedAt: '2026-07-17T09:00:00Z' },
          { status: 201 },
        )
      }),
    )
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    renderWithProviders(<AlertsPage />)

    await user.click(await screen.findByRole('button', { name: /trigger alarm|alarm auslösen/i }))
    const dialog = await screen.findByRole('dialog')

    // Title is required: the submit button stays disabled without it.
    const submit = within(dialog).getByRole('button', { name: /trigger alarm|alarm auslösen/i })
    expect(submit).toBeDisabled()

    await user.type(within(dialog).getByPlaceholderText(/feueralarm/i), 'Feueralarm Halle 2')
    expect(submit).toBeEnabled()

    // Pick the escalation policy (2nd combobox: severity is the 1st).
    await user.click(within(dialog).getAllByRole('combobox')[1]!)
    await user.click(await screen.findByRole('option', { name: 'standard-oncall' }))

    // Open the collapsible alarm-app sound section → np.* label fields.
    await user.click(within(dialog).getByRole('button', { name: /alarm app sound|alarm-app sound/i }))
    await user.click(within(dialog).getAllByRole('combobox')[2]!) // np.sound
    await user.click(await screen.findByRole('option', { name: 'np_klaxon' }))
    await user.click(within(dialog).getAllByRole('combobox')[3]!) // np.volume
    await user.click(await screen.findByRole('option', { name: '0.7' }))
    await user.click(within(dialog).getByRole('switch', { name: /np\.overrideSilent/i }))

    await user.click(submit)

    await waitFor(() => expect(posted).toBeDefined())
    expect(posted).toMatchObject({
      title: 'Feueralarm Halle 2',
      severity: 'critical',
      escalationPolicy: 'standard-oncall',
      labels: { 'np.sound': 'np_klaxon', 'np.volume': '0.7', 'np.overrideSilent': 'true' },
    })
    // Success closes the dialog and confirms with the inline toast.
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.getByText(/alarm raised|alarm ausgelöst/i)).toBeInTheDocument()
  })

  it('omits message, policy and labels when only the title is given', async () => {
    let posted: Record<string, unknown> | undefined
    server.use(
      listAlerts([]),
      http.get('/api/v1/escalation-policies', () => HttpResponse.json({ items: [] })),
      http.post('/api/v1/alerts', async ({ request }) => {
        posted = await request.json() as Record<string, unknown>
        return HttpResponse.json(
          { id: 'al-min', status: 'open', severity: 'warning', title: 'Testalarm', openedAt: '2026-07-17T09:00:00Z' },
          { status: 201 },
        )
      }),
    )
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    renderWithProviders(<AlertsPage />)

    await user.click(await screen.findByRole('button', { name: /trigger alarm|alarm auslösen/i }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByPlaceholderText(/feueralarm/i), 'Testalarm')
    await user.click(within(dialog).getByRole('button', { name: /trigger alarm|alarm auslösen/i }))

    await waitFor(() => expect(posted).toBeDefined())
    expect(posted).toEqual({ title: 'Testalarm', severity: 'critical' })
  })
})
