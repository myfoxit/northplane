// End-to-end harness proof: a real render() of a fetching page, served by MSW,
// asserting the success / empty / error branches. Exercises jsdom + RTL +
// TanStack Query + Router + MSW together.
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import { server, sampleProblems } from '../test/msw'
import { renderWithProviders } from '../test/render'
import { ProblemsPage } from './Problems'

describe('<ProblemsPage />', () => {
  it('renders a row per problem on a successful fetch', async () => {
    renderWithProviders(<ProblemsPage />)

    // web-01 / http (service) and db-01 (host) come from the default handlers.
    expect(await screen.findByText(/web-01 \/ http/)).toBeInTheDocument()
    expect(screen.getByText('db-01')).toBeInTheDocument()

    // State label/output from the canned data is rendered. The state span is
    // "<icon> CRITICAL", so match on a substring rather than the exact node.
    expect(screen.getByText(/CRITICAL/)).toBeInTheDocument()
    expect(screen.getByText('HTTP 500 from backend')).toBeInTheDocument()

    // Header count reflects the number of rows.
    expect(screen.getByRole('heading')).toHaveTextContent(`(${sampleProblems.length})`)
  })

  it('shows the all-green empty state when there are no problems', async () => {
    server.use(http.get('/api/v1/problems', () => HttpResponse.json({ items: [] })))
    renderWithProviders(<ProblemsPage />)

    expect(await screen.findByText(/all green|alles grün/i)).toBeInTheDocument()
    expect(screen.getByRole('heading')).toHaveTextContent('(0)')
  })

  it('shows an ErrorState (with retry) when the request 500s', async () => {
    server.use(
      http.get('/api/v1/problems', () =>
        HttpResponse.json(
          { code: 'internal', title: 'boom', detail: 'db unavailable' },
          { status: 500 },
        ),
      ),
    )
    renderWithProviders(<ProblemsPage />)

    // ErrorState surfaces the loadError heading + RFC 9457 detail + Retry.
    expect(await screen.findByText(/failed to load|laden fehlgeschlagen/i)).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText(/db unavailable/)).toBeInTheDocument())
    expect(screen.getByRole('button', { name: /retry|erneut/i })).toBeInTheDocument()
  })
})
