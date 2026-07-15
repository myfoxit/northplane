// Dead-letter queue tab: list, empty state, replay action.
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../../test/msw'
import { renderWithProviders } from '../../test/render'
import { DeadLettersTab } from './DeadLetters'

const sample = [{
  id: 'dl-1', kind: 'notification', channelId: 'ch-1', attempts: 30,
  lastError: 'smtp connect: connection refused', createdAt: '2026-06-01T10:00:00Z',
}]

describe('<DeadLettersTab />', () => {
  it('lists dead letters with their last error', async () => {
    server.use(http.get('/api/v1/notifications/dead-letters', () =>
      HttpResponse.json({ items: sample })))
    renderWithProviders(<DeadLettersTab />)
    expect(await screen.findByText(/connection refused/)).toBeInTheDocument()
    expect(screen.getByText('30')).toBeInTheDocument()
  })

  it('shows the empty state when the queue is clean', async () => {
    server.use(http.get('/api/v1/notifications/dead-letters', () =>
      HttpResponse.json({ items: [] })))
    renderWithProviders(<DeadLettersTab />)
    expect(await screen.findByText(/No dead letters/)).toBeInTheDocument()
  })

  it('replays an item and reports success', async () => {
    let replayed = ''
    server.use(
      http.get('/api/v1/notifications/dead-letters', () => HttpResponse.json({ items: sample })),
      http.post('/api/v1/notifications/dead-letters/:id\\:replay', ({ params }) => {
        replayed = String(params.id)
        return new HttpResponse(null, { status: 202 })
      }),
    )
    const user = userEvent.setup()
    renderWithProviders(<DeadLettersTab />)
    await user.click(await screen.findByRole('button', { name: 'Replay' }))
    await waitFor(() => expect(screen.getByText(/requeued/)).toBeInTheDocument())
    expect(replayed).toBe('dl-1')
  })
})
