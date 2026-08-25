// Reports (CMP Reports parity, SPEC §9.8): define/edit report definitions,
// render (HTML preview / CSV / JSON), run-now (render+archive+send), and
// browse the scheduled-render archive. Schedule is composed into the
// backend grammar: daily[@HH:MM] | weekly:<weekday>[@HH:MM] | monthly[:day][@HH:MM].
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Check, TriangleAlert } from 'lucide-react'
import { get, post, resourceApi, fmtTime, type ListResponse } from '../api'
import type { Report, ReportType } from '../types'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from '@/components/ui/select'
import { Empty, Spinner, ErrorState, Field, ListEditor, FormError, SubmitRow, useSave, DeleteButton, DuplicateButton } from '@/components/kit'
import { copyName } from '@/lib/duplicate'
import { t } from '../i18n'

const reportApi = resourceApi<Report>('reports')

// Radix SelectItem value cannot be "" — sentinel for the empty schedule
// frequency ("no schedule"), mapped back to '' before composeSchedule.
const NONE = '__none__'

const REPORT_TYPES: ReportType[] = ['availability', 'sla', 'alert-stats', 'oncall', 'audit']
const WEEKDAYS = ['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday']
const WEEKDAY_DE: Record<string, string> = {
  monday: t('weekdayMonday'), tuesday: t('weekdayTuesday'), wednesday: t('weekdayWednesday'), thursday: t('weekdayThursday'),
  friday: t('weekdayFriday'), saturday: t('weekdaySaturday'), sunday: t('weekdaySunday'),
}
const WINDOWS = [
  { days: 1, label: '24h' }, { days: 7, label: t('days7') },
  { days: 30, label: t('days30') }, { days: 90, label: t('days90') },
]

function typeLabel(ty: ReportType): string {
  const de: Record<ReportType, string> = {
    availability: t('availability'), sla: 'SLA', 'alert-stats': t('alertStats'),
    oncall: t('oncall'), audit: t('permissions'),
  }
  return de[ty] ?? ty
}

// download triggers a browser download of a :render result blob. The render
// endpoint is POST-only (POST /api/v1/reports/{name}:render) — a GET 404s.
async function downloadRender(name: string, format: 'csv' | 'json') {
  const res = await fetch(
    `/api/v1/reports/${encodeURIComponent(name)}:render?format=${format}`,
    { method: 'POST', credentials: 'same-origin' },
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
  const [copying, setCopying] = useState<Report | null>(null)
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
  // Starter content instead of a bare "No entries." (DASH-1): a 30-day
  // availability report over all objects, refined afterwards in the dialog.
  const createStarter = useSave<void>(
    () => reportApi.create({
      name: 'availability-30d', type: 'availability',
      params: { windowDays: 30, target: 99.9 },
    } as unknown as Report),
    { invalidate: [reportApi.queryKey as unknown as string[]] },
  )

  const rows = data ?? []
  if (isError && !data) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-bold">{t('reports')}</h1>
        <Button variant="default" onClick={() => setCreating(true)}>{t('create')}</Button>
      </div>
      {toast && (
        <div className="text-sm text-emerald-400 bg-emerald-500/10 border border-emerald-500/30 rounded-lg px-3 py-2 flex items-center gap-1.5">
          <Check size={14} /> {toast}
        </div>
      )}

      {isLoading && <Spinner />}
      {!isLoading && rows.length === 0 && (
        <div className="space-y-3">
          <Empty text={t('reportsEmptyHint')} />
          <div className="flex justify-center">
            <Button variant="outline" disabled={createStarter.isPending} onClick={() => createStarter.mutate()}>
              {t('createStarterReport')}
            </Button>
          </div>
        </div>
      )}
      {rows.length > 0 && (
        <Card>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  {[t('name'), t('type'), t('schedule'), t('recipients'), 'Keep', t('actions')].map((h, i) => <TableHead key={i}>{h}</TableHead>)}
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((r) => (
                  <TableRow key={r.name} className="hover:bg-card/40">
                    <TableCell className="font-medium text-foreground">{r.name}</TableCell>
                    <TableCell>
                      <Badge variant="outline" className="bg-muted text-foreground/90 border-input">{typeLabel(r.type)}</Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground font-mono text-xs">{r.schedule || '—'}</TableCell>
                    <TableCell className="tabular-nums">
                      {r.schedule && (r.email?.length ?? 0) === 0 ? (
                        <span className="inline-flex items-center gap-1 text-amber-400" title={t('noRecipientsWarn')}>
                          <TriangleAlert size={12} /> 0
                        </span>
                      ) : (
                        <span className="text-muted-foreground">{r.email?.length ?? 0}</span>
                      )}
                    </TableCell>
                    <TableCell className="text-muted-foreground tabular-nums">{r.keep ?? '—'}</TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-1">
                        <Button size="sm" variant="outline" onClick={() => setPreviewName(r.name)}>{t('preview')}</Button>
                        <Button size="sm" variant="ghost" onClick={() => downloadRender(r.name, 'csv')}>CSV</Button>
                        <Button size="sm" variant="ghost" onClick={() => downloadRender(r.name, 'json')}>JSON</Button>
                        <Button size="sm" variant="outline" onClick={() => runNow.mutate(r.name)} disabled={runNow.isPending}>
                          {t('run')}
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => setArchiveName(r.name)}>{t('archive')}</Button>
                        <Button size="sm" variant="ghost" onClick={() => setEditing(r)}>{t('edit')}</Button>
                        <DuplicateButton onClick={() => setCopying(r)} />
                        <DeleteButton onDelete={() => remove.mutate(r.name)} />
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      {(creating || editing || copying) && (
        <ReportDialog
          existing={editing ?? copying}
          copy={!!copying}
          existingNames={(data ?? []).map((r) => r.name)}
          onClose={() => { setCreating(false); setEditing(null); setCopying(null) }}
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
  const [head, arg] = (body ?? '').split(':')
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

// ReportDialog: `existing` null → create; set → edit; set + `copy` → create a
// duplicate seeded from that report (type, params, schedule, recipients)
// under a fresh name — the save is a POST.
function ReportDialog({ existing, copy, existingNames, onClose }: {
  existing: Report | null; copy?: boolean; existingNames?: string[]; onClose: () => void
}) {
  const editing = !!existing && !copy
  const params = (existing?.params ?? {}) as Record<string, unknown>
  const [name, setName] = useState(
    copy && existing ? copyName(existing.name, existingNames) : (existing?.name ?? ''))
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
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl max-h-[90vh] overflow-y-auto">
        <DialogHeader><DialogTitle>{copy && existing ? `${t('duplicate')}: ${existing.name}` : editing ? `${t('edit')}: ${existing!.name}` : t('create')}</DialogTitle></DialogHeader>
        <form className="space-y-3" onSubmit={submit}>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('name')} required>
              <Input value={name} onChange={(e) => setName(e.target.value)} disabled={editing} autoFocus={!editing} />
            </Field>
            <Field label={t('type')}>
              <Select value={type} onValueChange={(v) => setType(v as ReportType)}>
                <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {REPORT_TYPES.map((ty) => <SelectItem key={ty} value={ty}>{typeLabel(ty)}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
          </div>

        {showSelector && (
          <Field label={t('selector')} hint={t('selectorFilterHint')}>
            <Input value={selector} onChange={(e) => setSelector(e.target.value)} placeholder="env=prod" />
          </Field>
        )}
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('timeRange')}>
            <Select value={String(windowDays)} onValueChange={(v) => setWindowDays(Number(v))}>
              <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
              <SelectContent>
                {WINDOWS.map((w) => <SelectItem key={w.days} value={String(w.days)}>{w.label}</SelectItem>)}
              </SelectContent>
            </Select>
          </Field>
          {showTarget && (
            <Field label={t('slaTargetPct')} hint={t('emptyDefault999')}>
              <Input type="number" step="0.001" value={target}
                onChange={(e) => setTarget(e.target.value)} placeholder="99.9" />
            </Field>
          )}
        </div>
        {showSelector && (
          <label className="flex items-center gap-2 text-sm text-foreground/90 cursor-pointer">
            <input type="checkbox" checked={includeDowntimes}
              onChange={(e) => setIncludeDowntimes(e.target.checked)} />
            {t('countScheduledDowntimes')}
          </label>
        )}

        <div className="border-t border-border pt-3">
          <div className="text-xs text-muted-foreground font-medium mb-2">{t('schedule')}</div>
          <div className="grid grid-cols-3 gap-2">
            <Field label={t('frequency')}>
              <Select value={sched.freq || NONE} onValueChange={(v) => setSched({ ...sched, freq: (v === NONE ? '' : v) as ScheduleParts['freq'] })}>
                <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>— {t('none')} —</SelectItem>
                  <SelectItem value="daily">{t('daily')}</SelectItem>
                  <SelectItem value="weekly">{t('weekly')}</SelectItem>
                  <SelectItem value="monthly">{t('monthly')}</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            {sched.freq === 'weekly' && (
              <Field label={t('weekday')}>
                <Select value={sched.weekday} onValueChange={(v) => setSched({ ...sched, weekday: v })}>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {WEEKDAYS.map((d) => <SelectItem key={d} value={d}>{WEEKDAY_DE[d]}</SelectItem>)}
                  </SelectContent>
                </Select>
              </Field>
            )}
            {sched.freq === 'monthly' && (
              <Field label={t('dayOfMonth')}>
                <Input type="number" min={1} max={31} value={sched.day}
                  onChange={(e) => setSched({ ...sched, day: Number(e.target.value) })} />
              </Field>
            )}
            {sched.freq !== '' && (
              <Field label={t('timeOfDay')}>
                <Input type="time" value={sched.time} onChange={(e) => setSched({ ...sched, time: e.target.value })} />
              </Field>
            )}
          </div>
          {sched.freq !== '' && (
            <div className="text-[11px] text-muted-foreground mt-1 font-mono">→ {composeSchedule(sched)}</div>
          )}
        </div>

        <div className="grid grid-cols-2 gap-3">
          <Field label={t('recipients')} hint={t('recipientsHint')}>
            <ListEditor value={email} onChange={setEmail} placeholder="ops@example.com" />
          </Field>
          <Field label={t('keepArchive')} hint={t('keepArchiveHint')}>
            <Input type="number" min={0} value={keep} onChange={(e) => setKeep(Number(e.target.value))} />
          </Field>
        </div>
        {composeSchedule(sched) && email.length === 0 && (
          <div className="text-xs text-amber-400 flex items-center gap-1.5">
            <TriangleAlert size={13} className="shrink-0" /> {t('noRecipientsWarn')}
          </div>
        )}

          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} label={editing ? t('save') : t('create')} disabled={!name.trim()} />
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ——— HTML preview via sandboxed iframe ———

function PreviewDialog({ name, onClose }: { name: string; onClose: () => void }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['report-preview', name],
    queryFn: async () => {
      // POST — the :render action is POST-only; a GET returns 404 (NP-02).
      const res = await fetch(`/api/v1/reports/${encodeURIComponent(name)}:render?format=html`,
        { method: 'POST', credentials: 'same-origin' })
      if (!res.ok) throw new Error(`render failed (${res.status})`)
      return res.text()
    },
  })
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-4xl max-h-[90vh] overflow-y-auto">
        <DialogHeader><DialogTitle>{`${t('preview')}: ${name}`}</DialogTitle></DialogHeader>
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
          <Button variant="outline" onClick={onClose}>{t('close')}</Button>
        </div>
      </DialogContent>
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
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl max-h-[90vh] overflow-y-auto">
        <DialogHeader><DialogTitle>{`${t('archive')}: ${name}`}</DialogTitle></DialogHeader>
        {isLoading && <Spinner />}
        {!isLoading && rows.length === 0 && <Empty text={t('empty')} />}
        {rows.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                {['Slot', t('format'), t('createdAt'), ''].map((h, i) => <TableHead key={i}>{h}</TableHead>)}
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((e) => (
                <TableRow key={e.id} className="hover:bg-card/40">
                  <TableCell className="font-mono text-xs text-foreground/90">{e.slot}</TableCell>
                  <TableCell>
                    <Badge variant="outline" className="bg-muted text-foreground/90 border-input">{e.format}</Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs">{fmtTime(e.createdAt)}</TableCell>
                  <TableCell>
                    <a
                      href={`/api/v1/reports/${encodeURIComponent(name)}/archive/${encodeURIComponent(e.id)}`}
                      className="text-primary hover:text-primary text-xs"
                      download
                    >↓ {t('download')}</a>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        <div className="flex justify-end pt-3">
          <Button variant="outline" onClick={onClose}>{t('close')}</Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
