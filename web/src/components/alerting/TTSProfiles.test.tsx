// TTS-profiles tab: list rendering (engine/language/detection/voices), the
// profile editor with engine-driven config fields, lexicon/regex rows and
// the normalise/listen preview tool against the :normalize / :preview
// endpoints.
import { describe, it, expect, beforeAll } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../../test/msw'
import { renderWithProviders } from '../../test/render'
import { TTSProfilesTab } from './TTSProfiles'
import type { TTSProfile } from '../../types'

beforeAll(() => {
  Element.prototype.hasPointerCapture ??= () => false
  Element.prototype.setPointerCapture ??= () => {}
  Element.prototype.releasePointerCapture ??= () => {}
  // jsdom has no media playback
  window.HTMLMediaElement.prototype.play = () => Promise.resolve()
  window.HTMLMediaElement.prototype.pause = () => {}
})

const profileFix: TTSProfile = {
  name: 'default',
  engine: 'elevenlabs',
  config: { apiKey: '$SECRET:eleven$', model: 'eleven_flash_v2_5' },
  language: 'de-DE',
  voices: { de: 'voice-de', en: 'voice-en' },
  detect: { mode: 'segments', languages: ['de-DE', 'en-US'] },
  normalize: {
    lexicon: [{ from: 'np-01', to: 'Server eins' }],
    regex: [{ pattern: 'srv(\\d+)', replace: 'Server $1' }],
    spellOut: ['acme'],
    numbers: 'digits',
  },
  audio: { preroll: 'chime', sampleRate: 8000 },
  version: 2,
}

const engines = {
  engines: ['azure', 'command', 'edge', 'elevenlabs', 'google', 'http', 'openai', 'polly'],
  configKeys: {
    elevenlabs: [
      { key: 'apiKey', hint: '$SECRET:name$', secret: true, required: true },
      { key: 'voice', hint: 'voice id' },
      { key: 'model', hint: 'default eleven_multilingual_v2' },
    ],
    edge: [{ key: 'voice', hint: 'e.g. de-DE-KatjaNeural' }],
  },
  prerolls: ['none', 'chime', 'alert', 'gong'],
}

const baseHandlers = () => [
  http.get('/api/v1/tts-profiles', () => HttpResponse.json({ items: [profileFix] })),
  http.get('/api/v1/tts-profiles/:name', () => HttpResponse.json(profileFix, { headers: { ETag: '"2"' } })),
  http.get('/api/v1/tts/engines', () => HttpResponse.json(engines)),
]

describe('<TTSProfilesTab />', () => {
  it('lists profiles with engine, language, detection and voices', async () => {
    server.use(...baseHandlers())
    renderWithProviders(<TTSProfilesTab />)

    expect((await screen.findAllByText('default')).length).toBeGreaterThan(0)
    expect(screen.getByText('elevenlabs')).toBeInTheDocument()
    expect(screen.getByText('de-DE')).toBeInTheDocument()
    expect(screen.getByText(/segments · de-DE, en-US/)).toBeInTheDocument()
    expect(screen.getByText('de=voice-de · en=voice-en')).toBeInTheDocument()
  })

  it('shows the friendly empty state when no profiles exist', async () => {
    server.use(
      http.get('/api/v1/tts-profiles', () => HttpResponse.json({ items: [] })),
      http.get('/api/v1/tts/engines', () => HttpResponse.json(engines)),
    )
    renderWithProviders(<TTSProfilesTab />)
    expect(await screen.findByText(/no tts profiles yet|noch keine tts-profile/i)).toBeInTheDocument()
  })

  it('opens the editor with engine-driven config, lexicon/regex rows and the preview tool', async () => {
    server.use(
      ...baseHandlers(),
      http.post('/api/v1/tts\\:normalize', async ({ request }) => {
        const body = await request.json() as { text: string; profile: TTSProfile }
        // the unsaved editor state travels inline
        expect(body.profile.engine).toBe('elevenlabs')
        return HttpResponse.json({
          lang: 'de-DE',
          text: 'Festplatte voll auf Server eins.',
          segments: [{ text: 'Festplatte voll auf Server eins.', lang: 'de-DE' }],
        })
      }),
      http.post('/api/v1/tts\\:preview', () => HttpResponse.json({
        audio: 'UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQAAAAA=', format: 'audio/wav',
        lang: 'de-DE', text: 'Festplatte voll auf Server eins.', engine: 'default/elevenlabs', cached: false, durationMs: 1234,
        segments: [{ text: 'Festplatte voll auf Server eins.', lang: 'de-DE', voice: 'voice-de' }],
      })),
    )
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    renderWithProviders(<TTSProfilesTab />)

    await user.click(await screen.findByRole('button', { name: /edit|bearbeiten/i }))
    const dialog = await screen.findByRole('dialog')

    expect(within(dialog).getByText(/edit: default|bearbeiten: default/i)).toBeInTheDocument()
    expect(within(dialog).getByDisplayValue('default')).toBeDisabled()

    // engine-driven config keys (from /tts/engines) render as fields; the
    // secret key is masked, the model value is shown.
    expect(within(dialog).getByDisplayValue('$SECRET:eleven$')).toHaveAttribute('type', 'password')
    expect(within(dialog).getByDisplayValue('eleven_flash_v2_5')).toBeInTheDocument()

    // language, voices map, detection languages
    expect(within(dialog).getByDisplayValue('de-DE')).toBeInTheDocument()
    expect(within(dialog).getByDisplayValue('voice-de')).toBeInTheDocument()
    expect(within(dialog).getByText('en-US')).toBeInTheDocument()

    // lexicon / regex / spell-out rows
    expect(within(dialog).getByDisplayValue('np-01')).toBeInTheDocument()
    expect(within(dialog).getByDisplayValue('Server eins')).toBeInTheDocument()
    expect(within(dialog).getByDisplayValue('srv(\\d+)')).toBeInTheDocument()
    expect(within(dialog).getByText('acme')).toBeInTheDocument()

    // preview tool: normalise-only shows the plan
    await user.click(within(dialog).getByRole('button', { name: /normalise only|nur normalisieren/i }))
    expect(await within(dialog).findByText('Festplatte voll auf Server eins.')).toBeInTheDocument()

    // listen: renders the audio element with the returned clip
    await user.click(within(dialog).getByRole('button', { name: /^listen$|^anhören$/i }))
    const audio = await within(dialog).findByTestId('tts-preview-audio')
    expect(audio.getAttribute('src')).toMatch(/^data:audio\/wav;base64,/)
    expect(within(dialog).getByText(/default\/elevenlabs/)).toBeInTheDocument()
  })
})
