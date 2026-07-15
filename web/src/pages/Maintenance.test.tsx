// Maintenance page: silences tab (default) and downtimes tab, their list
// rendering + German empty states, and the silence create dialog opening.
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../test/msw'
import { renderWithProviders } from '../test/render'
import { MaintenancePage } from './Maintenance'
import type { Silence, Downtime } from '../types'

const silencesFix: Silence[] = [{
  id: 'si-1', selector: 'env=prod', textRegex: 'disk.*voll',
  comment: 'DB-Migration Fenster', createdBy: 'admin', expiresAt: '2026-06-10T00:00:00Z',
}]

const downtimesFix: Downtime[] = [{
  id: 'dt-1', objectId: 'obj-web-01', type: 'fixed',
  start: '2026-06-09T22:00:00Z', end: '2026-06-10T02:00:00Z',
  rrule: 'FREQ=WEEKLY;BYDAY=SA', comment: 'Patch-Day',
}]

const handleSilences = (items: Silence[]) =>
  http.get('/api/v1/silences', () => HttpResponse.json({ items }))
const handleDowntimes = (items: Downtime[]) =>
  http.get('/api/v1/downtimes', () => HttpResponse.json({ items }))

describe('<MaintenancePage />', () => {
  it('shows the silences tab by default with the listed silences', async () => {
    server.use(handleSilences(silencesFix), handleDowntimes(downtimesFix))
    renderWithProviders(<MaintenancePage />)

    expect(await screen.findByText('DB-Migration Fenster')).toBeInTheDocument()
    expect(screen.getByText('env=prod')).toBeInTheDocument()
    expect(screen.getByText('disk.*voll')).toBeInTheDocument()
    expect(screen.getByText('admin')).toBeInTheDocument()
    // Both tabs are offered ("Silences"/"Downtimes" are identical in DE/EN).
    expect(screen.getByRole('tab', { name: 'Silences' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Downtimes' })).toBeInTheDocument()
  })

  it('shows the empty state when no silences are active', async () => {
    server.use(handleSilences([]), handleDowntimes([]))
    renderWithProviders(<MaintenancePage />)
    expect(await screen.findByText('No active silences.')).toBeInTheDocument()
  })

  it('switches to the downtimes tab and lists scheduled downtimes', async () => {
    server.use(handleSilences(silencesFix), handleDowntimes(downtimesFix))
    const user = userEvent.setup()
    renderWithProviders(<MaintenancePage />)

    await user.click(await screen.findByRole('tab', { name: 'Downtimes' }))
    expect(await screen.findByText('Patch-Day')).toBeInTheDocument()
    expect(screen.getByText('obj-web-01')).toBeInTheDocument()
    expect(screen.getByText('fixed')).toBeInTheDocument()
    expect(screen.getByText('FREQ=WEEKLY;BYDAY=SA')).toBeInTheDocument()
  })

  it('shows the empty state when no downtimes are planned', async () => {
    server.use(handleSilences([]), handleDowntimes([]))
    const user = userEvent.setup()
    renderWithProviders(<MaintenancePage />)

    await user.click(await screen.findByRole('tab', { name: 'Downtimes' }))
    expect(await screen.findByText('No scheduled downtimes.')).toBeInTheDocument()
  })

  it('opens the silence create dialog with selector/regex/expiry fields', async () => {
    server.use(handleSilences([]), handleDowntimes([]))
    const user = userEvent.setup()
    renderWithProviders(<MaintenancePage />)

    await screen.findByText('No active silences.')
    await user.click(screen.getByRole('button', { name: /create|anlegen/i }))

    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveTextContent('Text regex')
    expect(dialog).toHaveTextContent('Expires')
    expect(screen.getByPlaceholderText('disk.*full')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('DB maintenance window')).toBeInTheDocument()
    // Quick-expiry presets are offered.
    expect(screen.getByRole('button', { name: '24h' })).toBeInTheDocument()
  })
})
