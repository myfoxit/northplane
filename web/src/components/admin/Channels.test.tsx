// Channels admin tab: list with type/status badges, empty state, the
// provider-aware e-mail config form (smtp | sendmail | resend | ses switch
// the visible fields) and the per-row test-notification action.
import { describe, it, expect, beforeAll } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../../test/msw'
import { renderWithProviders } from '../../test/render'
import { ChannelsTab } from './Channels'
import type { Channel } from '../../types'

// Radix Select drives its open/select logic through the pointer-capture
// APIs, which jsdom does not implement — polyfill just for this suite.
beforeAll(() => {
  Element.prototype.hasPointerCapture ??= () => false
  Element.prototype.setPointerCapture ??= () => {}
  Element.prototype.releasePointerCapture ??= () => {}
})

const channelsFix: Channel[] = [
  { name: 'mail-ops', type: 'email', enabled: true, config: { host: 'smtp.example.com' }, template: 'mail-std' },
  { name: 'chat-ops', type: 'slack', enabled: false, config: {} },
]

const listChannels = (items: Channel[]) =>
  http.get('/api/v1/channels', () => HttpResponse.json({ items }))

// The provider <Select> is the second combobox in the dialog (the first
// selects the channel type); options render into a body-level portal.
async function switchProvider(user: ReturnType<typeof userEvent.setup>, dialog: HTMLElement, provider: string) {
  await user.click(within(dialog).getAllByRole('combobox')[1]!)
  await user.click(await screen.findByRole('option', { name: provider }))
}

describe('<ChannelsTab />', () => {
  it('lists channels with type, status and template', async () => {
    server.use(listChannels(channelsFix))
    renderWithProviders(<ChannelsTab />)

    expect(await screen.findByText('mail-ops')).toBeInTheDocument()
    expect(screen.getByText('email')).toBeInTheDocument()
    expect(screen.getByText('chat-ops')).toBeInTheDocument()
    expect(screen.getByText('slack')).toBeInTheDocument()
    // Status badges carry their word (never color-only).
    expect(screen.getByText(/^(enabled|aktiv)$/i)).toBeInTheDocument()
    expect(screen.getByText(/^(disabled|deaktiviert)$/i)).toBeInTheDocument()
    expect(screen.getByText('mail-std')).toBeInTheDocument()
  })

  it('shows the empty state when no channels exist', async () => {
    server.use(listChannels([]))
    renderWithProviders(<ChannelsTab />)
    expect(await screen.findByText(/no entries|keine einträge/i)).toBeInTheDocument()
  })

  it('switches the e-mail config fields with the provider select', async () => {
    server.use(listChannels([]))
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    renderWithProviders(<ChannelsTab />)

    await screen.findByText(/no entries|keine einträge/i)
    await user.click(screen.getByRole('button', { name: /create|anlegen/i }))
    const dialog = await screen.findByRole('dialog')

    // New channels default to type=email, provider=smtp → SMTP key set.
    expect(within(dialog).getByText('Konfiguration (email)')).toBeInTheDocument()
    expect(within(dialog).getByText('SMTP-Host')).toBeInTheDocument()
    expect(within(dialog).getByText('Port')).toBeInTheDocument()
    expect(within(dialog).getByText('Absender (From)')).toBeInTheDocument()

    // provider → resend: HTTP-API key set replaces the SMTP fields.
    await switchProvider(user, dialog, 'resend')
    expect(await within(dialog).findByText('API-Key')).toBeInTheDocument()
    expect(within(dialog).queryByText('SMTP-Host')).not.toBeInTheDocument()

    // provider → ses: AWS credential fields appear.
    await switchProvider(user, dialog, 'ses')
    expect(await within(dialog).findByText('AWS-Region')).toBeInTheDocument()
    expect(within(dialog).getByText('Secret-Access-Key')).toBeInTheDocument()
    expect(within(dialog).queryByText('API-Key')).not.toBeInTheDocument()

    // The KVEditor fallback for unknown keys is always reachable.
    expect(within(dialog).getByText('Weitere Einstellungen')).toBeInTheDocument()
  })

  it('sends a test notification for a channel row', async () => {
    let tested = ''
    server.use(
      listChannels(channelsFix),
      http.post('/api/v1/channels/:name\\:test-notification', ({ params }) => {
        tested = String(params.name)
        return HttpResponse.json({ result: 'sent', detail: 'OK' })
      }),
    )
    const user = userEvent.setup()
    renderWithProviders(<ChannelsTab />)

    const buttons = await screen.findAllByRole('button', { name: /send test|test senden/i })
    await user.click(buttons[0]!) // first row = mail-ops
    await waitFor(() => expect(screen.getByText(/✓ sent — OK/)).toBeInTheDocument())
    expect(tested).toBe('mail-ops')
  })
})
