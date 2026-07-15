// Overview page: KPI tiles from /overview, problem list, open-incidents and
// on-call widgets. (t() strings are asserted DE|EN-hedged like Problems.test —
// jsdom reports en-US, hardcoded German literals stay German.)
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import { server } from '../test/msw'
import { renderWithProviders } from '../test/render'
import { OverviewPage } from './Overview'
import type { Overview, OnCallNow } from '../types'

const overviewFix: Overview = {
  summary: {
    hostsUp: 12, hostsDown: 1, hostsUnreachable: 0,
    servicesOk: 80, servicesWarning: 3, servicesCritical: 2,
    servicesUnknown: 0, acked: 1, inDowntime: 0, flapping: 0,
  },
  openAlerts: { critical: 2, warning: 3 },
  openIncidents: [{
    id: 'inc-1', status: 'open', severity: 'critical', title: 'DB-Cluster ausgefallen',
    createdBy: 'admin', openedAt: '2026-06-09T08:00:00Z', version: 1,
  }],
}

const oncallFix: OnCallNow[] = [{
  schedule: 'primary',
  shifts: [],
  contacts: [{ id: 'c1', name: 'Max Mustermann', phone: '+49 170 1234567' }],
}]

// /oncall/now returns a bare array (no { items } envelope).
const handleOncall = (items: OnCallNow[]) =>
  http.get('/api/v1/oncall/now', () => HttpResponse.json(items))

describe('<OverviewPage />', () => {
  it('renders KPI tiles, problems, incidents and on-call from the API', async () => {
    server.use(
      http.get('/api/v1/overview', () => HttpResponse.json(overviewFix)),
      handleOncall(oncallFix),
    )
    renderWithProviders(<OverviewPage />)

    // Tiles render their labels before data arrives — await a summary value
    // (12 = hostsUp) so the /overview query has resolved before asserting.
    expect(await screen.findByText('12')).toBeInTheDocument()
    expect(screen.getByText('Hosts UP')).toBeInTheDocument()
    expect(screen.getByText('80')).toBeInTheDocument() // servicesOk
    // Open-alerts tile sums critical (2) + warning (3).
    expect(screen.getByText('5')).toBeInTheDocument()

    // Problem list rows come from the default /problems handler.
    expect(await screen.findByText(/web-01 \/ http/)).toBeInTheDocument()
    expect(screen.getByText('HTTP 500 from backend')).toBeInTheDocument()

    // Open incidents (same /overview payload) + on-call widget content.
    expect(screen.getByText('DB-Cluster ausgefallen')).toBeInTheDocument()
    expect(await screen.findByText('Max Mustermann')).toBeInTheDocument()
    expect(screen.getByText('primary')).toBeInTheDocument()
    expect(screen.getByText('+49 170 1234567')).toBeInTheDocument()
  })

  it('shows all-green problems and empty widget states when nothing is open', async () => {
    server.use(
      http.get('/api/v1/overview', () => HttpResponse.json({
        summary: {
          hostsUp: 0, hostsDown: 0, hostsUnreachable: 0,
          servicesOk: 0, servicesWarning: 0, servicesCritical: 0,
          servicesUnknown: 0, acked: 0, inDowntime: 0, flapping: 0,
        },
        openAlerts: {},
        openIncidents: [],
      } satisfies Overview)),
      handleOncall([]),
      http.get('/api/v1/problems', () => HttpResponse.json({ items: [] })),
    )
    renderWithProviders(<OverviewPage />)

    expect(await screen.findByText(/all green|alles grün/i)).toBeInTheDocument()
    // Incidents, on-call and the recent-events feed all fall back to the
    // generic empty text when nothing is open (OVW-1 added the feed).
    await waitFor(() =>
      expect(screen.getAllByText(/no entries|keine einträge/i)).toHaveLength(3))
  })

  it('renders an ErrorState with retry when /overview fails', async () => {
    server.use(
      http.get('/api/v1/overview', () =>
        HttpResponse.json(
          { code: 'internal', title: 'boom', detail: 'db unavailable' },
          { status: 500 },
        )),
      handleOncall([]),
    )
    renderWithProviders(<OverviewPage />)

    expect(await screen.findByText(/failed to load|laden fehlgeschlagen/i)).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText(/db unavailable/)).toBeInTheDocument())
    expect(screen.getByRole('button', { name: /retry|erneut/i })).toBeInTheDocument()
  })
})
