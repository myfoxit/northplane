// Events page: event log rows with type/severity badges + payload summary,
// expandable JSON, the object-id filter wiring, empty and error states.
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../test/msw'
import { renderWithProviders } from '../test/render'
import { EventsPage } from './Events'
import type { NPEvent } from '../types'

const eventsFix: NPEvent[] = [
  {
    id: 'ev-1', ts: '2026-06-09T10:00:00Z', type: 'state_change', severity: 'critical',
    objectId: 'obj-1', payload: { summary: 'web-01/http → CRITICAL', attempt: 3 },
  },
  {
    id: 'ev-2', ts: '2026-06-09T10:05:00Z', type: 'notification',
    payload: { output: 'Mail an ops@example.com gesendet' },
  },
]

const listEvents = (items: NPEvent[]) =>
  http.get('/api/v1/events', () => HttpResponse.json({ items }))

describe('<EventsPage />', () => {
  it('renders event rows with type badge, severity and payload summary', async () => {
    server.use(listEvents(eventsFix))
    renderWithProviders(<EventsPage />)

    expect(await screen.findByText('state_change')).toBeInTheDocument()
    expect(screen.getByText('web-01/http → CRITICAL')).toBeInTheDocument()
    expect(screen.getByText('critical')).toBeInTheDocument()
    // Second row falls back to payload.output for its summary line.
    expect(screen.getByText('notification')).toBeInTheDocument()
    expect(screen.getByText('Mail an ops@example.com gesendet')).toBeInTheDocument()
    // The expandable <pre> holds the pretty-printed payload JSON.
    expect(screen.getByText(/"attempt": 3/)).toBeInTheDocument()
    // NDJSON export link is offered.
    expect(screen.getByText(/NDJSON Export/)).toBeInTheDocument()
  })

  it('shows the empty state when there are no events', async () => {
    server.use(listEvents([]))
    renderWithProviders(<EventsPage />)
    expect(await screen.findByText(/no entries|keine einträge/i)).toBeInTheDocument()
  })

  it('renders an ErrorState with retry when the request 500s', async () => {
    server.use(
      http.get('/api/v1/events', () =>
        HttpResponse.json(
          { code: 'internal', title: 'boom', detail: 'event log unavailable' },
          { status: 500 },
        )),
    )
    renderWithProviders(<EventsPage />)
    expect(await screen.findByText(/failed to load|laden fehlgeschlagen/i)).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText(/event log unavailable/)).toBeInTheDocument())
    expect(screen.getByRole('button', { name: /retry|erneut/i })).toBeInTheDocument()
  })

  it('passes the object-id filter through to the API query', async () => {
    const seen: string[] = []
    server.use(http.get('/api/v1/events', ({ request }) => {
      seen.push(new URL(request.url).searchParams.get('objectId') ?? '')
      return HttpResponse.json({ items: eventsFix })
    }))
    const user = userEvent.setup()
    renderWithProviders(<EventsPage />)

    await screen.findByText('state_change')
    await user.type(screen.getByPlaceholderText('Object-ID…'), 'web')
    await waitFor(() => expect(seen).toContain('web'))
  })
})
