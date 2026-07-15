// TemplatesPage: the three config tabs (Templates, Check-Kommandos,
// Zeiträume) list their named resource documents from /api/v1/<base>.
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../test/msw'
import { renderWithProviders } from '../test/render'
import { TemplatesPage } from './Templates'
import type { Template, CheckCommand, TimePeriod } from '../types'

const templatesFix: Template[] = [
  { name: 'generic-host', kind: 'host', labels: { env: 'prod' }, spec: { templates: ['base'] } },
  { name: 'web-defaults', spec: {} }, // kindless → badge "beide"
]

const commandsFix: CheckCommand[] = [
  { name: 'check_postgres', type: 'exec', line: ['check_postgres', '-H', '$HOSTADDRESS$'], env: true, timeout: '30s' },
]

const periodsFix: TimePeriod[] = [
  {
    name: 'business-hours', alias: 'Geschäftszeiten',
    days: { monday: ['09:00-17:00'], tuesday: ['09:00-17:00'] },
    exceptions: { '2026-12-24': ['00:00-00:00'] },
  },
]

const allHandlers = () => [
  http.get('/api/v1/templates', () => HttpResponse.json({ items: templatesFix })),
  http.get('/api/v1/check-commands', () => HttpResponse.json({ items: commandsFix })),
  http.get('/api/v1/time-periods', () => HttpResponse.json({ items: periodsFix })),
]

describe('<TemplatesPage />', () => {
  it('lists templates with kind badge, labels and template chain', async () => {
    server.use(...allHandlers())
    renderWithProviders(<TemplatesPage />)

    expect(await screen.findByText('generic-host')).toBeInTheDocument()
    expect(screen.getByText('host')).toBeInTheDocument()       // kind badge
    expect(screen.getByText('env=prod')).toBeInTheDocument()   // labels cell
    expect(screen.getByText('base')).toBeInTheDocument()       // spec.templates cell
    // A template without kind renders the "beide" (host & service) badge.
    expect(screen.getByText('web-defaults')).toBeInTheDocument()
    expect(screen.getByText('both')).toBeInTheDocument()
    // All three config tabs are offered.
    expect(screen.getByRole('tab', { name: 'Check commands' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Time periods' })).toBeInTheDocument()
  })

  it('shows the empty state when no templates exist', async () => {
    server.use(http.get('/api/v1/templates', () => HttpResponse.json({ items: [] })))
    renderWithProviders(<TemplatesPage />)
    expect(await screen.findByText(/no entries|keine einträge/i)).toBeInTheDocument()
  })

  it('switches to Check-Kommandos and lists commands with line/env/timeout', async () => {
    server.use(...allHandlers())
    const user = userEvent.setup()
    renderWithProviders(<TemplatesPage />)

    await screen.findByText('generic-host')
    await user.click(screen.getByRole('tab', { name: 'Check commands' }))

    expect(await screen.findByText('check_postgres')).toBeInTheDocument()
    expect(screen.getByText('exec')).toBeInTheDocument()
    // argv tokens joined into one command line cell.
    expect(screen.getByText('check_postgres -H $HOSTADDRESS$')).toBeInTheDocument()
    expect(screen.getByText('yes')).toBeInTheDocument()   // env macros enabled
    expect(screen.getByText('30s')).toBeInTheDocument()  // timeout
  })

  it('switches to Zeiträume and lists time periods with alias and counts', async () => {
    server.use(...allHandlers())
    const user = userEvent.setup()
    renderWithProviders(<TemplatesPage />)

    await screen.findByText('generic-host')
    await user.click(screen.getByRole('tab', { name: 'Time periods' }))

    expect(await screen.findByText('business-hours')).toBeInTheDocument()
    expect(screen.getByText('Geschäftszeiten')).toBeInTheDocument()
    expect(screen.getByText('2')).toBeInTheDocument() // two weekdays configured
    expect(screen.getByText('1')).toBeInTheDocument() // one exception date
    // Column headers of the periods table.
    expect(screen.getByText('days')).toBeInTheDocument()
    expect(screen.getByText('Exceptions')).toBeInTheDocument()
  })
})
