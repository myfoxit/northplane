// IVR-menus tab: list rendering (name/language/option overview) and the
// menu editor dialog with per-action option fields (trigger-alarm → severity/
// title/np.sound/record; say → text).
import { describe, it, expect, beforeAll } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../../test/msw'
import { renderWithProviders } from '../../test/render'
import { IVRMenusTab } from './IVRMenus'
import type { IVRMenu } from '../../types'

// Radix Select drives its open/select logic through the pointer-capture
// APIs, which jsdom does not implement — polyfill just for this suite.
beforeAll(() => {
  Element.prototype.hasPointerCapture ??= () => false
  Element.prototype.setPointerCapture ??= () => {}
  Element.prototype.releasePointerCapture ??= () => {}
})

const menuFix: IVRMenu = {
  name: 'alarmzentrale',
  language: 'de-DE',
  greeting: 'Willkommen bei der Alarmzentrale',
  pin: '4711',
  trustCallerId: true,
  options: [
    {
      digit: '1', action: 'trigger-alarm', severity: 'critical',
      title: 'Telefonalarm von {caller}', labels: { 'np.sound': 'np_klaxon' }, record: true,
    },
    { digit: '2', action: 'say', text: 'Bitte während der Geschäftszeiten anrufen.' },
  ],
  version: 3,
}

const baseHandlers = () => [
  http.get('/api/v1/ivr-menus', () => HttpResponse.json({ items: [menuFix] })),
  http.get('/api/v1/ivr-menus/:name', () =>
    HttpResponse.json(menuFix, { headers: { ETag: '"3"' } })),
  http.get('/api/v1/escalation-policies', () =>
    HttpResponse.json({ items: [{ name: 'standard-oncall', steps: [] }] })),
]

describe('<IVRMenusTab />', () => {
  it('lists menus with language, option count and digit overview', async () => {
    server.use(...baseHandlers())
    renderWithProviders(<IVRMenusTab />)

    expect(await screen.findByText('alarmzentrale')).toBeInTheDocument()
    expect(screen.getByText('de-DE')).toBeInTheDocument()
    expect(screen.getByText('2')).toBeInTheDocument() // option count
    expect(screen.getByText('1=trigger-alarm · 2=say')).toBeInTheDocument()
  })

  it('shows the friendly empty state when no menus exist', async () => {
    server.use(
      http.get('/api/v1/ivr-menus', () => HttpResponse.json({ items: [] })),
      http.get('/api/v1/escalation-policies', () => HttpResponse.json({ items: [] })),
    )
    renderWithProviders(<IVRMenusTab />)
    expect(await screen.findByText(/no ivr menus yet|noch keine ivr-menüs/i)).toBeInTheDocument()
  })

  it('opens the editor with menu fields and per-action option fields', async () => {
    server.use(...baseHandlers())
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    renderWithProviders(<IVRMenusTab />)

    await user.click(await screen.findByRole('button', { name: /edit|bearbeiten/i }))
    const dialog = await screen.findByRole('dialog')

    // Header + immutable name.
    expect(within(dialog).getByText(/edit: alarmzentrale|bearbeiten: alarmzentrale/i)).toBeInTheDocument()
    expect(within(dialog).getByDisplayValue('alarmzentrale')).toBeDisabled()

    // Menu-level fields: language, PIN, greeting, trustCallerId toggle.
    expect(within(dialog).getByDisplayValue('de-DE')).toBeInTheDocument()
    expect(within(dialog).getByDisplayValue('4711')).toBeInTheDocument()
    expect(within(dialog).getByDisplayValue('Willkommen bei der Alarmzentrale')).toBeInTheDocument()
    expect(within(dialog).getByRole('switch', { name: /trust caller id|anrufer-id vertrauen/i })).toBeChecked()

    // Option 1 (trigger-alarm): title input, record toggle, np.sound select
    // showing the stored label value, escalation-policy select.
    expect(within(dialog).getByText(/option 1/i)).toBeInTheDocument()
    expect(within(dialog).getByDisplayValue('Telefonalarm von {caller}')).toBeInTheDocument()
    expect(within(dialog).getByRole('switch', { name: /record a voice message|sprachnachricht aufzeichnen/i })).toBeChecked()
    // np.sound label surfaces as the selected value of one combobox (Radix
    // mirrors it into a hidden native select, so scope to the trigger).
    expect(within(dialog).getAllByRole('combobox').some((el) => el.textContent?.includes('np_klaxon'))).toBe(true)

    // Option 2 (say): the spoken text.
    expect(within(dialog).getByText(/option 2/i)).toBeInTheDocument()
    expect(within(dialog).getByDisplayValue('Bitte während der Geschäftszeiten anrufen.')).toBeInTheDocument()

    // An option can be added.
    expect(within(dialog).getByRole('button', { name: /\+ option/i })).toBeInTheDocument()
  })
})
