// AlertsPage: list rendering with severity/status metadata, empty + error
// states, and the ack dialog flow (POST /alerts/{id}:ack) plus quick resolve.
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../test/msw'
import { renderWithProviders } from '../test/render'
import { AlertsPage } from './Alerts'
import type { Alert } from '../types'

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
})
