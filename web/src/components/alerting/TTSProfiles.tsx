// Text-to-speech profiles tab: how voice alarms are spoken. A profile names
// an engine (local command, Edge, OpenAI, ElevenLabs, Azure, Google, Polly,
// generic HTTP) with its credentials and voices, how the language of a
// message is detected, how the text is normalised before synthesis
// (lexicon, regex, numbers, acronyms, units …) and how the audio is
// finished for the phone line. The editor carries a live tool: "normalise
// only" shows what the engine will receive; "listen" synthesises a preview.
import { useEffect, useMemo, useState, type RefObject } from 'react'
import { useQuery } from '@tanstack/react-query'
import { X } from 'lucide-react'
import { get, post, resourceApi } from '../../api'
import type { TTSEngine, TTSEngines, TTSLexiconEntry, TTSPlan, TTSPreview, TTSProfile, TTSRegexRule, TTSVoice } from '../../types'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Badge } from '@/components/ui/badge'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { Empty, ErrorState, Field, FormError, SubmitRow, DeleteButton, KVEditor, ListEditor } from '@/components/kit'
import { useSave } from '@/hooks/useSave'
import { t } from '../../i18n'
import { ToggleRow } from './common'

const profilesApi = resourceApi<TTSProfile>('tts-profiles')

// Radix <SelectItem> cannot use "" — sentinel for "no selection".
const NONE = '__none__'

const ENGINES: TTSEngine[] = ['command', 'edge', 'openai', 'elevenlabs', 'azure', 'google', 'polly', 'http']
const SAMPLE_RATES = [8000, 16000, 22050, 24000] as const
const PREVIEW_DEFAULT = 'Northplane Alarm. Schweregrad KRITISCH. CPU load high on np-01 (10.0.0.12). Drücken Sie die 4 zum Quittieren.'

function emptyProfile(): TTSProfile {
  return { name: '', engine: 'edge', config: {}, detect: {}, normalize: {}, audio: {} }
}

// engineLabel: human-readable engine choice for the selector.
function engineLabel(e: TTSEngine): string {
  switch (e) {
    case 'command': return 'command — piper / espeak-ng / flite / say (lokal)'
    case 'edge': return 'edge — Microsoft Edge neural (free, unofficial)'
    case 'openai': return 'openai — OpenAI / compatible (Kokoro, LocalAI)'
    case 'elevenlabs': return 'elevenlabs'
    case 'azure': return 'azure — Azure AI Speech'
    case 'google': return 'google — Google Cloud TTS'
    case 'polly': return 'polly — Amazon Polly'
    case 'http': return 'http — generic endpoint (Piper server, MaryTTS, …)'
  }
}

export function TTSProfilesTab({ createRef }: { createRef?: RefObject<() => void> }) {
  const { data: profiles, isError, error, refetch } = useQuery({ queryKey: profilesApi.queryKey, queryFn: profilesApi.list })
  const { data: engines } = useQuery({ queryKey: ['tts-engines'], queryFn: () => get<TTSEngines>('/tts/engines') })
  const [editing, setEditing] = useState<{ profile: TTSProfile; etag: number } | null>(null)

  const open = async (name?: string) => {
    if (!name) { setEditing({ profile: emptyProfile(), etag: 0 }); return }
    const { data, etag } = await profilesApi.get(name)
    setEditing({
      profile: { ...data, config: data.config ?? {}, detect: data.detect ?? {}, normalize: data.normalize ?? {}, audio: data.audio ?? {} },
      etag,
    })
  }
  useEffect(() => { if (createRef) createRef.current = () => void open() })

  if (isError && !profiles) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }

  return (
    <div className="space-y-3">
      {(profiles?.length ?? 0) === 0 ? <Empty text={t('noTtsProfilesFriendly')} /> : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('name')}</TableHead>
              <TableHead>{t('ttsEngine')}</TableHead>
              <TableHead>{t('languageField')}</TableHead>
              <TableHead>{t('ttsDetect')}</TableHead>
              <TableHead>{t('ttsVoice')}</TableHead>
              <TableHead>{t('actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {profiles!.map((p) => (
              <TableRow key={p.name}>
                <TableCell className="font-medium text-foreground">
                  {p.name}
                  {p.name === 'default' && <Badge variant="outline" className="ml-2">default</Badge>}
                </TableCell>
                <TableCell className="text-xs font-mono">{p.engine}</TableCell>
                <TableCell className="text-xs text-muted-foreground">{p.language ?? '—'}</TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {p.detect?.mode ?? 'message'}
                  {p.detect?.languages?.length ? ` · ${p.detect.languages.join(', ')}` : ''}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground font-mono truncate max-w-72">
                  {p.voice || Object.entries(p.voices ?? {}).map(([k, v]) => `${k}=${v}`).join(' · ') || '—'}
                </TableCell>
                <TableCell className="text-right">
                  <Button size="sm" variant="outline" onClick={() => open(p.name)}>{t('edit')}</Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {editing && (
        <ProfileDialog state={editing} engines={engines} profiles={profiles ?? []} onClose={() => setEditing(null)} />
      )}
    </div>
  )
}

function ProfileDialog({ state, engines, profiles, onClose }: {
  state: { profile: TTSProfile; etag: number }; engines?: TTSEngines; profiles: TTSProfile[]; onClose: () => void
}) {
  const isNew = state.etag === 0 && !state.profile.name
  const [p, setP] = useState<TTSProfile>(state.profile)
  const set = (patch: Partial<TTSProfile>) => setP((prev) => ({ ...prev, ...patch }))
  const setCfg = (k: string, v: string) => setP((prev) => {
    const config = { ...(prev.config ?? {}) }
    if (v === '') delete config[k]; else config[k] = v
    return { ...prev, config }
  })
  const setNorm = (patch: Partial<TTSProfile['normalize']>) => setP((prev) => ({ ...prev, normalize: { ...prev.normalize, ...patch } }))
  const setAudio = (patch: Partial<TTSProfile['audio']>) => setP((prev) => ({ ...prev, audio: { ...prev.audio, ...patch } }))
  const setDetect = (patch: Partial<TTSProfile['detect']>) => setP((prev) => ({ ...prev, detect: { ...prev.detect, ...patch } }))

  const keys = useMemo(() => engines?.configKeys?.[p.engine] ?? [], [engines, p.engine])
  // keys the typed form does not know → generic KV editor so nothing is unreachable
  const extraConfig = useMemo(() => {
    const known = new Set(keys.map((k) => k.key))
    return Object.fromEntries(Object.entries(p.config ?? {}).filter(([k]) => !known.has(k)))
  }, [keys, p.config])

  const save = useSave(
    (doc: TTSProfile) => isNew ? profilesApi.create(doc) : profilesApi.update(doc.name, doc, state.etag),
    { invalidate: [profilesApi.queryKey], onDone: onClose },
  )
  const remove = useSave((name: string) => profilesApi.remove(name),
    { invalidate: [profilesApi.queryKey], onDone: onClose })
  const onSubmit = (e: React.FormEvent) => { e.preventDefault(); save.mutate(p) }

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-3xl max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{isNew ? t('create') : `${t('edit')}: ${state.profile.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} className="space-y-4">
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('name')} required hint={t('ttsProfileHint')}>
              <Input value={p.name} disabled={!isNew} onChange={(e) => set({ name: e.target.value })} placeholder="default" />
            </Field>
            <Field label={t('description')}>
              <Input value={p.description ?? ''} onChange={(e) => set({ description: e.target.value || undefined })} />
            </Field>
          </div>

          {/* Engine */}
          <Section title={t('ttsEngine')}>
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('ttsEngine')} hint={t('ttsEngineHint')}>
                <Select value={p.engine} onValueChange={(v) => set({ engine: v as TTSEngine })}>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {ENGINES.map((e) => <SelectItem key={e} value={e}>{engineLabel(e)}</SelectItem>)}
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('ttsFallback')} hint={t('ttsFallbackHint')}>
                <Select value={p.fallback ?? NONE} onValueChange={(v) => set({ fallback: v === NONE ? undefined : v })}>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NONE}>— {t('none')} —</SelectItem>
                    {profiles.filter((o) => o.name !== p.name).map((o) => <SelectItem key={o.name} value={o.name}>{o.name}</SelectItem>)}
                  </SelectContent>
                </Select>
              </Field>
            </div>
            <div className="grid grid-cols-2 gap-3">
              {keys.map((k) => (
                <Field key={k.key} label={k.key} required={k.required}
                  hint={k.secret ? `${k.hint ?? ''} ($SECRET:name$)`.trim() : k.hint}
                  className={k.key === 'command' || k.key === 'url' || k.key === 'body' || k.key === 'instructions' ? 'col-span-2' : ''}>
                  <Input value={p.config?.[k.key] ?? ''} onChange={(e) => setCfg(k.key, e.target.value)}
                    className="font-mono" type={k.secret ? 'password' : 'text'} autoComplete="off" />
                </Field>
              ))}
            </div>
            <details className="text-sm">
              <summary className="cursor-pointer text-xs text-muted-foreground">{t('moreSettings')}</summary>
              <div className="mt-2">
                <KVEditor value={extraConfig} onChange={(v) => setP((prev) => {
                  const known = Object.fromEntries(Object.entries(prev.config ?? {}).filter(([k]) => keys.some((kk) => kk.key === k)))
                  return { ...prev, config: { ...known, ...v } }
                })} />
              </div>
            </details>
          </Section>

          {/* Language & voices */}
          <Section title={t('ttsVoices')}>
            <div className="grid grid-cols-3 gap-3">
              <Field label={t('ttsDefaultLanguage')} hint={t('ttsDefaultLanguageHint')}>
                <Input value={p.language ?? ''} onChange={(e) => set({ language: e.target.value || undefined })} placeholder="de-DE" />
              </Field>
              <Field label={t('ttsVoice')} hint={t('ttsVoiceHint')}>
                <Input value={p.voice ?? ''} onChange={(e) => set({ voice: e.target.value || undefined })} className="font-mono" />
              </Field>
              <Field label={t('ttsRate')} hint={t('ttsRateHint')}>
                <Input type="number" step="0.05" min="0.5" max="2" value={p.rate ?? ''}
                  onChange={(e) => set({ rate: e.target.value === '' ? undefined : Number(e.target.value) })} placeholder="1" />
              </Field>
            </div>
            <Field label={t('ttsVoices')} hint={t('ttsVoicesHint')}>
              <KVEditor value={p.voices ?? {}} onChange={(v) => set({ voices: Object.keys(v).length ? v : undefined })}
                keyPlaceholder="de" valuePlaceholder="de-DE-KatjaNeural" />
            </Field>
            <VoiceBrowser profile={p} onPick={(lang, id) => {
              if (lang) set({ voices: { ...(p.voices ?? {}), [lang]: id } })
              else set({ voice: id })
            }} />
          </Section>

          {/* Detection */}
          <Section title={t('ttsDetect')}>
            <div className="grid grid-cols-3 gap-3">
              <Field label={t('ttsDetectMode')}>
                <Select value={p.detect.mode ?? 'message'} onValueChange={(v) => setDetect({ mode: v as TTSProfile['detect']['mode'] })}>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="off">{t('ttsDetectOff')}</SelectItem>
                    <SelectItem value="message">{t('ttsDetectMessage')}</SelectItem>
                    <SelectItem value="segments">{t('ttsDetectSegments')}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('ttsDetectLanguages')} hint={t('ttsDetectLanguagesHint')} className="col-span-2">
                <ListEditor value={p.detect.languages ?? []} onChange={(v) => setDetect({ languages: v.length ? v : undefined })}
                  placeholder="de-DE" />
              </Field>
            </div>
          </Section>

          {/* Normalisation */}
          <Section title={t('ttsNormalize')}>
            <ToggleRow label={t('ttsNormalizeDisabled')} checked={p.normalize.disabled ?? false}
              onChange={(v) => setNorm({ disabled: v || undefined })} />
            {!p.normalize.disabled && (
              <>
                <LexiconEditor value={p.normalize.lexicon ?? []} onChange={(v) => setNorm({ lexicon: v.length ? v : undefined })} />
                <RegexEditor value={p.normalize.regex ?? []} onChange={(v) => setNorm({ regex: v.length ? v : undefined })} />
                <Field label={t('ttsSpellOut')} hint={t('ttsSpellOutHint')}>
                  <ListEditor value={p.normalize.spellOut ?? []} onChange={(v) => setNorm({ spellOut: v.length ? v : undefined })} placeholder="acme" />
                </Field>
                <div className="grid grid-cols-3 gap-3">
                  <Field label={t('ttsNumbers')}>
                    <Select value={p.normalize.numbers ?? 'auto'} onValueChange={(v) => setNorm({ numbers: v as TTSProfile['normalize']['numbers'] })}>
                      <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem value="auto">{t('ttsNumbersAuto')}</SelectItem>
                        <SelectItem value="digits">{t('ttsNumbersDigits')}</SelectItem>
                        <SelectItem value="words">{t('ttsNumbersWords')}</SelectItem>
                        <SelectItem value="native">{t('ttsNumbersNative')}</SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                  <Field label={t('ttsDigitsFrom')}>
                    <Input type="number" min="2" max="20" value={p.normalize.digitsFrom ?? ''}
                      onChange={(e) => setNorm({ digitsFrom: e.target.value === '' ? undefined : Number(e.target.value) })} placeholder="5" />
                  </Field>
                  <Field label={t('ttsURLs')}>
                    <Select value={p.normalize.urls ?? 'host'} onValueChange={(v) => setNorm({ urls: v as TTSProfile['normalize']['urls'] })}>
                      <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem value="host">{t('ttsURLsHost')}</SelectItem>
                        <SelectItem value="drop">{t('ttsURLsDrop')}</SelectItem>
                        <SelectItem value="keep">{t('ttsURLsKeep')}</SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                </div>
                <div className="grid grid-cols-2 gap-x-6">
                  <ToggleRow label={t('ttsAcronyms')} checked={p.normalize.acronyms !== 'off'}
                    onChange={(v) => setNorm({ acronyms: v ? undefined : 'off' })} />
                  <ToggleRow label={t('ttsUnits')} checked={p.normalize.units !== 'native'}
                    onChange={(v) => setNorm({ units: v ? undefined : 'native' })} />
                  <ToggleRow label={t('ttsSymbols')} checked={p.normalize.symbols !== 'native'}
                    onChange={(v) => setNorm({ symbols: v ? undefined : 'native' })} />
                  <ToggleRow label={t('ttsIPs')} checked={p.normalize.ipAddresses !== 'native'}
                    onChange={(v) => setNorm({ ipAddresses: v ? undefined : 'native' })} />
                  <ToggleRow label={t('ttsIdentifiers')} checked={p.normalize.identifiers !== 'keep'}
                    onChange={(v) => setNorm({ identifiers: v ? undefined : 'keep' })} />
                  <ToggleRow label={t('ttsBuiltinLexicon')} checked={!p.normalize.noBuiltinLexicon}
                    onChange={(v) => setNorm({ noBuiltinLexicon: v ? undefined : true })} />
                </div>
              </>
            )}
          </Section>

          {/* Audio */}
          <Section title={t('ttsAudio')}>
            <div className="grid grid-cols-4 gap-3">
              <Field label={t('ttsSampleRate')}>
                <Select value={String(p.audio.sampleRate ?? 8000)} onValueChange={(v) => setAudio({ sampleRate: Number(v) === 8000 ? undefined : Number(v) })}>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {SAMPLE_RATES.map((r) => <SelectItem key={r} value={String(r)}>{r} Hz</SelectItem>)}
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('ttsFormat')}>
                <Select value={p.audio.format ?? 'wav'} onValueChange={(v) => setAudio({ format: v === 'wav' ? undefined : 'ulaw' })}>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="wav">WAV PCM 16-bit</SelectItem>
                    <SelectItem value="ulaw">WAV G.711 µ-law (8 kHz)</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('ttsPreroll')} hint={t('ttsPrerollHint')}>
                <Select value={p.audio.preroll ?? 'none'} onValueChange={(v) => setAudio({ preroll: v === 'none' ? undefined : v })}>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {(engines?.prerolls ?? ['none', 'chime', 'alert', 'gong']).map((n) => <SelectItem key={n} value={n}>{n}</SelectItem>)}
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('ttsGain')}>
                <Input type="number" step="1" min="-20" max="20" value={p.audio.gainDb ?? ''}
                  onChange={(e) => setAudio({ gainDb: e.target.value === '' ? undefined : Number(e.target.value) })} placeholder="0" />
              </Field>
            </div>
            <div className="grid grid-cols-4 gap-3">
              <Field label={t('ttsLeadSilence')}>
                <Input type="number" min="0" max="5000" value={p.audio.leadSilenceMs ?? ''}
                  onChange={(e) => setAudio({ leadSilenceMs: e.target.value === '' ? undefined : Number(e.target.value) })} placeholder="300" />
              </Field>
              <Field label={t('ttsTrailSilence')}>
                <Input type="number" min="0" max="5000" value={p.audio.trailSilenceMs ?? ''}
                  onChange={(e) => setAudio({ trailSilenceMs: e.target.value === '' ? undefined : Number(e.target.value) })} placeholder="200" />
              </Field>
              <div className="col-span-2 space-y-1 pt-4">
                <ToggleRow label={t('ttsNormalizeLoudness')} checked={!p.audio.noNormalize} onChange={(v) => setAudio({ noNormalize: v ? undefined : true })} />
                <ToggleRow label={t('ttsTrimSilence')} checked={!p.audio.keepSilence} onChange={(v) => setAudio({ keepSilence: v ? undefined : true })} />
              </div>
            </div>
            <ToggleRow label={t('ttsCacheDisabled')} hint={t('ttsCacheHint')} checked={p.cacheDisabled ?? false}
              onChange={(v) => set({ cacheDisabled: v || undefined })} />
          </Section>

          {/* Preview tool */}
          <PreviewTool profile={p} />

          <FormError error={save.error} />
          <div className="flex items-center justify-between pt-2">
            {!isNew ? <DeleteButton onDelete={() => remove.mutate(state.profile.name)} /> : <span />}
            <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!p.name} />
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Card className="border-border/80 py-3 gap-3">
      <CardContent className="px-3 space-y-3">
        <div className="text-xs font-semibold text-muted-foreground">{title}</div>
        {children}
      </CardContent>
    </Card>
  )
}

// LexiconEditor: rows of from → to with match-case / substring flags.
function LexiconEditor({ value, onChange }: { value: TTSLexiconEntry[]; onChange: (v: TTSLexiconEntry[]) => void }) {
  const update = (i: number, patch: Partial<TTSLexiconEntry>) =>
    onChange(value.map((e, j) => (j === i ? { ...e, ...patch } : e)))
  return (
    <Field label={t('ttsLexicon')} hint={t('ttsLexiconHint')}>
      <div className="space-y-1.5">
        {value.map((e, i) => (
          <div key={i} className="flex gap-1.5 items-center">
            <Input value={e.from} onChange={(ev) => update(i, { from: ev.target.value })} placeholder={t('ttsLexiconFrom')} className="h-8 font-mono flex-1" aria-label={`${t('ttsLexiconFrom')} ${i + 1}`} />
            <span className="text-muted-foreground text-xs">→</span>
            <Input value={e.to} onChange={(ev) => update(i, { to: ev.target.value })} placeholder={t('ttsLexiconTo')} className="h-8 flex-1" aria-label={`${t('ttsLexiconTo')} ${i + 1}`} />
            <label className="text-[11px] text-muted-foreground inline-flex items-center gap-1 whitespace-nowrap">
              <input type="checkbox" checked={e.matchCase ?? false} onChange={(ev) => update(i, { matchCase: ev.target.checked || undefined })} />
              {t('ttsMatchCase')}
            </label>
            <label className="text-[11px] text-muted-foreground inline-flex items-center gap-1 whitespace-nowrap">
              <input type="checkbox" checked={e.substring ?? false} onChange={(ev) => update(i, { substring: ev.target.checked || undefined })} />
              {t('ttsSubstring')}
            </label>
            <Button size="sm" variant="ghost" type="button" aria-label={t('remove')} onClick={() => onChange(value.filter((_, j) => j !== i))}><X size={13} /></Button>
          </div>
        ))}
        <Button size="sm" variant="outline" type="button" onClick={() => onChange([...value, { from: '', to: '' }])}>+ {t('ttsLexiconFrom')}</Button>
      </div>
    </Field>
  )
}

function RegexEditor({ value, onChange }: { value: TTSRegexRule[]; onChange: (v: TTSRegexRule[]) => void }) {
  const update = (i: number, patch: Partial<TTSRegexRule>) =>
    onChange(value.map((e, j) => (j === i ? { ...e, ...patch } : e)))
  return (
    <Field label={t('ttsRegex')} hint={t('ttsRegexHint')}>
      <div className="space-y-1.5">
        {value.map((e, i) => (
          <div key={i} className="flex gap-1.5 items-center">
            <Input value={e.pattern} onChange={(ev) => update(i, { pattern: ev.target.value })} placeholder={t('ttsPattern')} className="h-8 font-mono flex-1" aria-label={`${t('ttsPattern')} ${i + 1}`} />
            <span className="text-muted-foreground text-xs">→</span>
            <Input value={e.replace} onChange={(ev) => update(i, { replace: ev.target.value })} placeholder={t('ttsReplace')} className="h-8 font-mono flex-1" aria-label={`${t('ttsReplace')} ${i + 1}`} />
            <Button size="sm" variant="ghost" type="button" aria-label={t('remove')} onClick={() => onChange(value.filter((_, j) => j !== i))}><X size={13} /></Button>
          </div>
        ))}
        <Button size="sm" variant="outline" type="button" onClick={() => onChange([...value, { pattern: '', replace: '' }])}>+ {t('ttsPattern')}</Button>
      </div>
    </Field>
  )
}

// VoiceBrowser loads the engine's catalogue (POST /tts:voices with the
// unsaved profile) and lets the operator pick a voice for a language.
function VoiceBrowser({ profile, onPick }: { profile: TTSProfile; onPick: (lang: string, id: string) => void }) {
  const [voices, setVoices] = useState<TTSVoice[] | null>(null)
  const [lang, setLang] = useState('')
  const [err, setErr] = useState<unknown>(null)
  const [loading, setLoading] = useState(false)
  const load = async () => {
    setLoading(true); setErr(null)
    try {
      setVoices(await post<TTSVoice[]>('/tts:voices', { profile, language: lang || undefined }))
    } catch (e) { setErr(e) } finally { setLoading(false) }
  }
  return (
    <div className="space-y-2">
      <div className="flex gap-2 items-end">
        <Field label={t('languageField')}>
          <Input value={lang} onChange={(e) => setLang(e.target.value)} placeholder="de" className="h-8 w-28" />
        </Field>
        <Button size="sm" variant="outline" type="button" onClick={load} disabled={loading}>{t('ttsLoadVoices')}</Button>
      </div>
      <FormError error={err} />
      {voices && voices.length === 0 && <div className="text-xs text-muted-foreground">{t('ttsVoicesEmpty')}</div>}
      {voices && voices.length > 0 && (
        <div className="max-h-40 overflow-y-auto border border-border/80 rounded-md divide-y divide-border/60">
          {voices.map((v) => (
            <div key={v.id} className="flex items-center justify-between px-2 py-1 text-xs">
              <span className="font-mono truncate">{v.id}</span>
              <span className="text-muted-foreground truncate px-2">{v.name}{v.lang ? ` · ${v.lang}` : ''}{v.gender ? ` · ${v.gender}` : ''}</span>
              <Button size="sm" variant="ghost" type="button" onClick={() => onPick(lang.trim() || (v.lang ? v.lang.split('-')[0]! : ''), v.id)}>{t('add')}</Button>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// PreviewTool: normalise-only (cheap) or synthesize and play (uses the
// engine/credits) with the profile as currently edited (unsaved state).
function PreviewTool({ profile }: { profile: TTSProfile }) {
  const [text, setText] = useState(PREVIEW_DEFAULT)
  const [plan, setPlan] = useState<TTSPlan | null>(null)
  const [preview, setPreview] = useState<TTSPreview | null>(null)
  const [err, setErr] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const run = async (listen: boolean) => {
    setBusy(true); setErr(null)
    try {
      if (listen) {
        const res = await post<TTSPreview>('/tts:preview', { text, profile, preroll: !!profile.audio.preroll && profile.audio.preroll !== 'none' })
        setPreview(res); setPlan(null)
      } else {
        setPlan(await post<TTSPlan>('/tts:normalize', { text, profile })); setPreview(null)
      }
    } catch (e) { setErr(e) } finally { setBusy(false) }
  }
  const shown = preview ?? plan
  return (
    <Section title={t('ttsPreview')}>
      <Field label={t('ttsPreviewText')}>
        <Textarea value={text} rows={2} onChange={(e) => setText(e.target.value)} />
      </Field>
      <div className="flex gap-2">
        <Button size="sm" variant="outline" type="button" disabled={busy} onClick={() => run(false)}>{t('ttsPreviewNormalize')}</Button>
        <Button size="sm" type="button" disabled={busy} onClick={() => run(true)}>{t('ttsPreviewListen')}</Button>
      </div>
      <FormError error={err} />
      {shown && (
        <div className="text-xs space-y-1">
          <div><span className="text-muted-foreground">{t('ttsPreviewDetected')}:</span> <span className="font-mono">{shown.lang}</span>
            {preview && <span className="text-muted-foreground"> · {preview.engine} · {preview.durationMs} ms{preview.cached ? ' · cache' : ''}</span>}</div>
          <div className="text-muted-foreground">{t('ttsPreviewResult')}:</div>
          <ul className="space-y-0.5">
            {shown.segments.map((s, i) => (
              <li key={i} className="flex gap-2"><Badge variant="outline" className="font-mono shrink-0">{s.lang}</Badge><span>{s.text}</span></li>
            ))}
          </ul>
          {preview && (
            <audio controls autoPlay src={`data:${preview.format};base64,${preview.audio}`} className="w-full h-8" data-testid="tts-preview-audio" />
          )}
        </div>
      )}
    </Section>
  )
}
