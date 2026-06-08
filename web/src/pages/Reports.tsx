// Reports (CMP Reports parity, SPEC §9.8): define/edit report definitions,
// render (HTML preview / CSV / JSON), run-now (render+archive+send), and
// browse the scheduled-render archive. Schedule is composed into the
// backend grammar: daily[@HH:MM] | weekly:<weekday>[@HH:MM] | monthly[:day][@HH:MM].
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Check } from 'lucide-react'
import { get, post, resourceApi, fmtTime, type ListResponse } from '../api'
import type { Report, ReportType } from '../types'
import { Button, Card, Dialog, Empty, Spinner, Table, Badge, ErrorState } from '../components/ui'
import { Input } from '../components/ui'
import { Field, Select, ListEditor, FormError, SubmitRow, useSave, DeleteButton } from '../components/forms'
import { t } from '../i18n'

const reportApi = resourceApi<Report>('reports')

const REPORT_TYPES: ReportType[] = ['availability', 'sla', 'alert-stats', 'oncall', 'audit']
const WEEKDAYS = ['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday']
const WEEKDAY_DE: Record<string, string> = {
  monday: 'Montag', tuesday: 'Dienstag', wednesday: 'Mittwoch', thursday: 'Donnerstag',
  friday: 'Freitag', saturday: 'Samstag', sunday: 'Sonntag',
}
const WINDOWS = [
  { days: 1, label: '24h' }, { days: 7, label: '7 Tage' },
  { days: 30, label: '30 Tage' }, { days: 90, label: '90 Tage' },
]

function typeLabel(ty: ReportType): string {
  const de: Record<ReportType, string> = {
    availability: 'Verfügbarkeit', sla: 'SLA', 'alert-stats': 'Alarm-Statistik',
    oncall: 'Bereitschaft', audit: 'Berechtigungen',
  }
  return de[ty] ?? ty
}

// download triggers a browser download of a :render result blob.
async function downloadRender(name: string, format: 'csv' | 'json') {
  const res = await fetch(
    `/api/v1/reports/${encodeURIComponent(name)}:render?format=${format}`,
    { credentials: 'same-origin' },
  )
  if (!res.ok) throw new Error(`render failed (${res.status})`)
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${name}.${format}`
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

export function ReportsPage() {
  const { data, isLoading, isError, error, refetch } = useQuery({ queryKey: reportApi.queryKey, queryFn: reportApi.list })
  const [editing, setEditing] = useState<Report | null>(null) // null = closed
  const [creating, setCreating] = useState(false)
  const [previewName, setPreviewName] = useState<string | null>(null)
  const [archiveName, setArchiveName] = useState<string | null>(null)
  const [toast, setToast] = useState<string | null>(null)

  const remove = useSave((name: string) => reportApi.remove(name),
    { invalidate: [reportApi.queryKey as unknown as string[]] })
  const runNow = useSave(
    (name: string) => post(`/reports/${encodeURIComponent(name)}:run`),
    {
      invalidate: [reportApi.queryKey as unknown as string[]],
      onDone: () => { setToast(t('saved')); setTimeout(() => setToast(null), 2500) },
    },
  )

  const rows = data ?? []
  if (isError && !data) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-bold">{t('reports')}</h1>
        <Button variant="primary" onClick={() => setCreating(true)}>{t('create')}</Button>
      </div>
      {toast && (
        <div className="text-sm text-emerald-400 bg-emerald-500/10 border border-emerald-500/30 rounded-lg px-3 py-2 flex items-center gap-1.5">
          <Check size={14} /> {toast}
        </div>
      )}

      {isLoading && <Spinner />}
      {!isLoading && rows.length === 0 && <Empty text={t('empty')} />}
      {rows.length > 0 && (
        <Card>
          <Table head={[t('name'), t('type'), t('schedule'), t('recipients'), 'Keep', t('actions')]}>
            {rows.map((r) => (
              <tr key={r.name} className="hover:bg-card/40">
                <td className="px-3 py-2 font-medium text-foreground">{r.name}</td>
                <td className="px-3 py-2">
                  <Badge className="bg-muted text-foreground/90 border-input">{typeLabel(r.type)}</Badge>
                </td>
                <td className="px-3 py-2 text-muted-foreground font-mono text-xs">{r.schedule || '—'}</td>
                <td className="px-3 py-2 text-muted-foreground tabular-nums">{r.email?.length ?? 0}</td>
                <td className="px-3 py-2 text-muted-foreground tabular-nums">{r.keep ?? '—'}</td>
                <td className="px-3 py-2">
                  <div className="flex flex-wrap gap-1">
                    <Button size="sm" onClick={() => setPreviewName(r.name)}>{t('preview')}</Button>
                    <Button size="sm" variant="ghost" onClick={() => downloadRender(r.name, 'csv')}>CSV</Button>
                    <Button size="sm" variant="ghost" onClick={() => downloadRender(r.name, 'json')}>JSON</Button>
                    <Button size="sm" onClick={() => runNow.mutate(r.name)} disabled={runNow.isPending}>
                      {t('run')}
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setArchiveName(r.name)}>{t('archive')}</Button>
                    <Button size="sm" variant="ghost" onClick={() => setEditing(r)}>{t('edit')}</Button>
                    <DeleteButton onDelete={() => remove.mutate(r.name)} />
                  </div>
                </td>
              </tr>
            ))}
          </Table>
        </Card>
      )}

      {(creating || editing) && (
        <ReportDialog
          existing={editing}
          onClose={() => { setCreating(false); setEditing(null) }}
        />
      )}
      {previewName && <PreviewDialog name={previewName} onClose={() => setPreviewName(null)} />}
      {archiveName && <ArchiveDialog name={archiveName} onClose={() => setArchiveName(null)} />}
    </div>
  )
}

// ——— create/edit dialog ———

interface ScheduleParts { freq: '' | 'daily' | 'weekly' | 'monthly'; weekday: string; day: number; time: string }

function parseScheduleStr(s?: string): ScheduleParts {
  const out: ScheduleParts = { freq: '', weekday: 'monday', day: 1, time: '07:00' }
  if (!s) return out
  const [body, at] = s.split('@')
  if (at) out.time = at
  const [head, arg] = body.split(':')
  if (head === 'daily') out.freq = 'daily'
  else if (head === 'weekly') { out.freq = 'weekly'; if (arg) out.weekday = arg }
  else if (head === 'monthly') { out.freq = 'monthly'; if (arg) out.day = Number(arg) || 1 }
  return out
}

function composeSchedule(p: ScheduleParts): string {
  if (p.freq === '') return ''
  const at = p.time ? `@${p.time}` : ''
  if (p.freq === 'daily') return `daily${at}`
  if (p.freq === 'weekly') return `weekly:${p.weekday}${at}`
  return `monthly:${p.day}${at}`
}

function ReportDialog({ existing, onClose }: { existing: Report | null; onClose: () => void }) {
  const editing = !!existing
  const params = (existing?.params ?? {}) as Record<string, unknown>
  const [name, setName] = useState(existing?.name ?? '')
  const [type, setType] = useState<ReportType>(existing?.type ?? 'availability')
  const [selector, setSelector] = useState(String(params.selector ?? ''))
  const [windowDays, setWindowDays] = useState<number>(Number(params.windowDays ?? 30))
  const [target, setTarget] = useState<string>(params.target != null ? String(params.target) : '')
  const [includeDowntimes, setIncludeDowntimes] = useState<boolean>(!!params.includeDowntimes)
  const [sched, setSched] = useState<ScheduleParts>(parseScheduleStr(existing?.schedule))
  const [email, setEmail] = useState<string[]>(existing?.email ?? [])
  const [keep, setKeep] = useState<number>(existing?.keep ?? 10)

  const showTarget = type === 'availability' || type === 'sla'
  const showSelector = type === 'availability' || type === 'sla'

  const save = useSave(
    async (doc: Report) => {
      if (editing) {
        const fresh = await reportApi.get(doc.name)
        return reportApi.update(doc.name, { ...fresh.data, ...doc }, fresh.etag)
      }
      return reportApi.create(doc)
    },
    { invalidate: [reportApi.queryKey as unknown as string[]], onDone: onClose },
  )

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    const p: Record<string, unknown> = {}
    if (showSelector && selector.trim()) p.selector = selector.trim()
    if (windowDays > 0) p.windowDays = windowDays
    if (showTarget && target.trim()) p.target = Number(target)
    if (includeDowntimes) p.includeDowntimes = true
    save.mutate({
      name: name.trim(), type, params: p,
      schedule: composeSchedule(sched) || undefined,
      email: email.length ? email : undefined,
      keep: keep || undefined,
    })
  }

  return (
    <Dialog open onClose={onClose} title={editing ? `${t('edit')}: ${existing!.name}` : t('create')} size="lg">
      <form className="space-y-3" onSubmit={submit}>
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('name')} required>
            <Input value={name} onChange={(e) => setName(e.target.value)} disabled={editing} autoFocus={!editing} />
          </Field>
          <Field label={t('type')}>
            <Select value={type} onChange={(e) => setType(e.target.value as ReportType)}>
              {REPORT_TYPES.map((ty) => <option key={ty} value={ty}>{typeLabel(ty)}</option>)}
            </Select>
          </Field>
        </div>

        {showSelector && (
          <Field label="Selector" hint="Label-Filter der einbezogenen Objekte, z.B. env=prod">
            <Input value={selector} onChange={(e) => setSelector(e.target.value)} placeholder="env=prod" />
          </Field>
        )}
        <div className="grid grid-cols-2 gap-3">
          <Field label="Zeitraum">
            <Select value={windowDays} onChange={(e) => setWindowDays(Number(e.target.value))}>
              {WINDOWS.map((w) => <option key={w.days} value={w.days}>{w.label}</option>)}
            </Select>
          </Field>
          {showTarget && (
            <Field label="SLA-Ziel %" hint="leer = 99.9">
              <Input type="number" step="0.001" value={target}
                onChange={(e) => setTarget(e.target.value)} placeholder="99.9" />
            </Field>
          )}
        </div>
        {showSelector && (
          <label className="flex items-center gap-2 text-sm text-foreground/90 cursor-pointer">
            <input type="checkbox" checked={includeDowntimes}
              onChange={(e) => setIncludeDowntimes(e.target.checked)} />
            Geplante Downtimes mitzählen
          </label>
        )}

        <div className="border-t border-border pt-3">
          <div className="text-xs text-muted-foreground font-medium mb-2">{t('schedule')}</div>
          <div className="grid grid-cols-3 gap-2">
            <Field label="Frequenz">
              <Select value={sched.freq} onChange={(e) => setSched({ ...sched, freq: e.target.value as ScheduleParts['freq'] })}>
                <option value="">— {t('none')} —</option>
                <option value="daily">täglich</option>
                <option value="weekly">wöchentlich</option>
                <option value="monthly">monatlich</option>
              </Select>
            </Field>
            {sched.freq === 'weekly' && (
              <Field label="Wochentag">
                <Select value={sched.weekday} onChange={(e) => setSched({ ...sched, weekday: e.target.value })}>
                  {WEEKDAYS.map((d) => <option key={d} value={d}>{WEEKDAY_DE[d]}</option>)}
                </Select>
              </Field>
            )}
            {sched.freq === 'monthly' && (
              <Field label="Tag (1–31)">
                <Input type="number" min={1} max={31} value={sched.day}
                  onChange={(e) => setSched({ ...sched, day: Number(e.target.value) })} />
              </Field>
            )}
            {sched.freq !== '' && (
              <Field label="Uhrzeit">
                <Input type="time" value={sched.time} onChange={(e) => setSched({ ...sched, time: e.target.value })} />
              </Field>
            )}
          </div>
          {sched.freq !== '' && (
            <div className="text-[11px] text-muted-foreground mt-1 font-mono">→ {composeSchedule(sched)}</div>
          )}
        </div>

        <div className="grid grid-cols-2 gap-3">
          <Field label={t('recipients')} hint="E-Mail-Adressen für den Versand">
            <ListEditor value={email} onChange={setEmail} placeholder="ops@example.com" />
          </Field>
          <Field label="Archiv behalten" hint="Anzahl gespeicherter Läufe">
            <Input type="number" min={0} value={keep} onChange={(e) => setKeep(Number(e.target.value))} />
          </Field>
        </div>

        <FormError error={save.error} />
        <SubmitRow onCancel={onClose} saving={save.isPending} label={editing ? t('save') : t('create')} disabled={!name.trim()} />
      </form>
    </Dialog>
  )
}

// ——— HTML preview via sandboxed iframe ———

function PreviewDialog({ name, onClose }: { name: string; onClose: () => void }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['report-preview', name],
    queryFn: async () => {
      const res = await fetch(`/api/v1/reports/${encodeURIComponent(name)}:render?format=html`,
        { credentials: 'same-origin' })
      if (!res.ok) throw new Error(`render failed (${res.status})`)
      return res.text()
    },
  })
  return (
    <Dialog open onClose={onClose} title={`${t('preview')}: ${name}`} size="xl">
      {isLoading && <Spinner />}
      {error && <FormError error={error} />}
      {data != null && (
        <iframe
          title={`report-${name}`}
          sandbox=""
          srcDoc={data}
          className="w-full h-[70vh] bg-white rounded-lg border border-input"
        />
      )}
      <div className="flex justify-end gap-2 pt-3">
        <Button variant="ghost" onClick={() => downloadRender(name, 'csv')}>CSV</Button>
        <Button variant="ghost" onClick={() => downloadRender(name, 'json')}>JSON</Button>
        <Button onClick={onClose}>{t('close')}</Button>
      </div>
    </Dialog>
  )
}

// ——— archive list + downloads ———

interface ArchiveEntry { id: string; slot: string; format: string; createdAt: string }

function ArchiveDialog({ name, onClose }: { name: string; onClose: () => void }) {
  const { data, isLoading } = useQuery({
    queryKey: ['report-archive', name],
    queryFn: () => get<ListResponse<ArchiveEntry>>(`/reports/${encodeURIComponent(name)}/archive?limit=100`),
  })
  const rows = data?.items ?? []
  return (
    <Dialog open onClose={onClose} title={`${t('archive')}: ${name}`} size="lg">
      {isLoading && <Spinner />}
      {!isLoading && rows.length === 0 && <Empty text={t('empty')} />}
      {rows.length > 0 && (
        <Table head={['Slot', t('format'), 'Erstellt', '']}>
          {rows.map((e) => (
            <tr key={e.id} className="hover:bg-card/40">
              <td className="px-3 py-2 font-mono text-xs text-foreground/90">{e.slot}</td>
              <td className="px-3 py-2">
                <Badge className="bg-muted text-foreground/90 border-input">{e.format}</Badge>
              </td>
              <td className="px-3 py-2 text-muted-foreground text-xs">{fmtTime(e.createdAt)}</td>
              <td className="px-3 py-2">
                <a
                  href={`/api/v1/reports/${encodeURIComponent(name)}/archive/${encodeURIComponent(e.id)}`}
                  className="text-primary hover:text-primary text-xs"
                  download
                >↓ {t('download')}</a>
              </td>
            </tr>
          ))}
        </Table>
      )}
      <div className="flex justify-end pt-3">
        <Button onClick={onClose}>{t('close')}</Button>
      </div>
    </Dialog>
  )
}
