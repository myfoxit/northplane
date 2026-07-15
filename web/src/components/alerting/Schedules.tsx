// On-call schedule management (SPEC §9.5 / §12.3): schedule CRUD with a
// layered-rotation editor, per-schedule override management, a 14-day
// timeline and an hours-per-person stats card. Consumed by OnCall.tsx.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { X, Download, Phone } from 'lucide-react'
import { get, post, fmtTime, resourceApi } from '../../api'
import type { Schedule, Rotation, Contact, ScheduleOverride, OnCallNow } from '../../types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card, CardAction, CardContent, CardHeader, CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { Empty, Field, FormError, KVEditor, ListEditor, DurationInput, SubmitRow, DeleteButton, useSave } from '@/components/kit'
import { t } from '../../i18n'
import { DateTimeInput } from './common'
import { isoToLocalInput, localInputToIso, nowPlus } from './datetime'

const schedulesApi = resourceApi<Schedule>('schedules')
const contactsApi = resourceApi<Contact>('contacts')

// Radix <SelectItem> cannot use "" — sentinel for the "no selection" option.
const NONE = '__none__'

interface Shift { contactId: string; layer?: string; start: string; end: string; override?: boolean }
interface StatRow { contactId: string; contact: string; hours: number; weekendHours: number; overrides: number }

function emptyLayer(): Rotation {
  return { name: '', participants: [], unit: 'weekly', anchor: new Date().toISOString(), restriction: {} }
}
function emptySchedule(): Schedule {
  return { name: '', timeZone: 'Europe/Berlin', layers: [emptyLayer()] }
}

// Build a from/to RFC3339 pair relative to "now". Called lazily inside
// queryFns so render stays pure (react-hooks/purity).
function dayRange(daysBack: number, daysFwd: number): { from: string; to: string } {
  const now = Date.now()
  return {
    from: new Date(now - daysBack * 86400_000).toISOString(),
    to: new Date(now + daysFwd * 86400_000).toISOString(),
  }
}


// Resolve a contact id OR name to a display name.
function useContactName() {
  const { data: contacts } = useQuery({ queryKey: contactsApi.queryKey, queryFn: contactsApi.list })
  const nameOf = (ref: string) => contacts?.find((c) => c.id === ref || c.name === ref)?.name ?? ref
  return { contacts: contacts ?? [], nameOf }
}

export function SchedulesManager() {
  const { data } = useQuery({ queryKey: schedulesApi.queryKey, queryFn: schedulesApi.list })
  const [editing, setEditing] = useState<{ schedule: Schedule; etag: number } | null>(null)

  const open = async (name?: string) => {
    if (!name) { setEditing({ schedule: emptySchedule(), etag: 0 }); return }
    const { data: s, etag } = await schedulesApi.get(name)
    setEditing({ schedule: { ...s, layers: s.layers ?? [] }, etag })
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('schedules')}</CardTitle>
        <CardAction><Button size="sm" variant="default" onClick={() => open()}>{t('create')}</Button></CardAction>
      </CardHeader>
      <CardContent>
        {(data?.length ?? 0) === 0 ? <Empty text={t('noSchedules')} /> : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('name')}</TableHead>
                <TableHead>{t('timezone')}</TableHead>
                <TableHead>Layer</TableHead>
                <TableHead>{t('actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data!.map((s) => (
                <TableRow key={s.name}>
                  <TableCell className="font-medium text-foreground">{s.name}</TableCell>
                  <TableCell className="text-xs text-muted-foreground font-mono">{s.timeZone}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{s.layers?.length ?? 0}</TableCell>
                  <TableCell className="text-right"><Button size="sm" variant="outline" onClick={() => open(s.name)}>{t('edit')}</Button></TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        {editing && <ScheduleDialog state={editing} onClose={() => setEditing(null)} />}
      </CardContent>
    </Card>
  )
}

function ScheduleDialog({ state, onClose }: { state: { schedule: Schedule; etag: number }; onClose: () => void }) {
  const isNew = state.etag === 0 && !state.schedule.name
  const [s, setS] = useState<Schedule>(state.schedule)
  const { contacts } = useContactName()
  const suggestions = contacts.map((c) => c.name)

  const setLayer = (i: number, layer: Rotation) =>
    setS((prev) => ({ ...prev, layers: prev.layers.map((l, j) => (j === i ? layer : l)) }))
  const addLayer = () => setS((prev) => ({ ...prev, layers: [...prev.layers, emptyLayer()] }))
  const removeLayer = (i: number) => setS((prev) => ({ ...prev, layers: prev.layers.filter((_, j) => j !== i) }))

  const save = useSave(
    (doc: Schedule) => isNew ? schedulesApi.create(doc) : schedulesApi.update(doc.name, doc, state.etag),
    { invalidate: [schedulesApi.queryKey, ['schedules'], ['oncall']], onDone: onClose },
  )
  const remove = useSave((name: string) => schedulesApi.remove(name),
    { invalidate: [schedulesApi.queryKey, ['schedules'], ['oncall']], onDone: onClose })

  const onSubmit = (e: React.FormEvent) => { e.preventDefault(); save.mutate(s) }

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{isNew ? t('create') : `${t('edit')}: ${state.schedule.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} className="space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('name')} required>
              <Input value={s.name} disabled={!isNew} onChange={(e) => setS({ ...s, name: e.target.value })} placeholder="netops-primary" />
            </Field>
            <Field label={t('timezone')} hint="IANA-Zone">
              <Input value={s.timeZone} onChange={(e) => setS({ ...s, timeZone: e.target.value })} placeholder="Europe/Berlin" />
            </Field>
          </div>

          <div className="space-y-2">
            <span className="text-xs text-muted-foreground font-medium">Layer ({t('rotation')})</span>
            {s.layers.map((layer, i) => (
              <LayerCard key={i} index={i} layer={layer} suggestions={suggestions}
                onChange={(l) => setLayer(i, l)}
                onRemove={s.layers.length > 1 ? () => removeLayer(i) : undefined} />
            ))}
            <Button size="sm" variant="outline" type="button" onClick={addLayer}>+ Layer</Button>
          </div>

          <FormError error={save.error} />
          <div className="flex items-center justify-between pt-2">
            {!isNew ? <DeleteButton onDelete={() => remove.mutate(state.schedule.name)} /> : <span />}
            <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!s.name} />
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function LayerCard({ index, layer, suggestions, onChange, onRemove }: {
  index: number; layer: Rotation; suggestions: string[]
  onChange: (l: Rotation) => void; onRemove?: () => void
}) {
  const set = (patch: Partial<Rotation>) => onChange({ ...layer, ...patch })
  return (
    <Card className="border-border/80 py-3 gap-3">
      <CardContent className="px-3">
        <div className="flex items-center justify-between mb-2">
          <span className="text-xs font-semibold text-muted-foreground">Layer {index + 1}</span>
          {onRemove && <Button size="sm" variant="ghost" type="button" onClick={onRemove} title={t('remove')} aria-label={t('remove')}><X size={13} /></Button>}
        </div>
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('name')} hint="optional">
            <Input value={layer.name ?? ''} onChange={(e) => set({ name: e.target.value || undefined })} placeholder={t('weekPlaceholder')} />
          </Field>
          <Field label={t('rhythm')}>
            <Select value={layer.unit} onValueChange={(v) => set({ unit: v as Rotation['unit'] })}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="daily">{t('daily')}</SelectItem>
                <SelectItem value="weekly">{t('weekly')}</SelectItem>
                <SelectItem value="custom">{t('custom')}</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        </div>
        <Field label={t('participants')} className="mt-2">
          <ListEditor value={layer.participants} onChange={(v) => set({ participants: v })}
            placeholder={t('contactNamePlaceholder')} suggestions={suggestions} />
        </Field>
        <div className="grid grid-cols-2 gap-3 mt-2">
          {layer.unit === 'custom' && (
            <Field label={t('shiftLength')}>
              <DurationInput value={layer.length ?? ''} onChange={(v) => set({ length: v || undefined })} placeholder="12h" />
            </Field>
          )}
          <Field label={t('anchor')}>
            <DateTimeInput value={isoToLocalInput(layer.anchor)} onChange={(v) => set({ anchor: localInputToIso(v) || layer.anchor })} />
          </Field>
        </div>
        <Field label={t('restriction')} className="mt-2"
          hint={t('restrictionHint')}>
          <KVEditor
            value={Object.fromEntries(Object.entries(layer.restriction ?? {}).map(([k, v]) => [k, v.join(',')]))}
            onChange={(v) => set({ restriction: Object.fromEntries(Object.entries(v).map(([k, csv]) => [k, csv.split(',').map((x) => x.trim()).filter(Boolean)])) })}
            keyPlaceholder="mon" valuePlaceholder="08:00-17:00" />
        </Field>
      </CardContent>
    </Card>
  )
}

// ——— On-call "now" cards (kept from the original OnCall view) ———
export function OnCallNowCards() {
  const { data: now } = useQuery({ queryKey: ['oncall'], queryFn: () => get<OnCallNow[]>('/oncall/now') })
  return (
    <div className="grid lg:grid-cols-3 gap-3">
      {(now ?? []).map((entry) => (
        <Card key={entry.schedule}>
          <CardHeader>
            <CardTitle>{entry.schedule}</CardTitle>
            <CardAction>
              <a className="text-xs text-muted-foreground hover:text-foreground/90 inline-flex items-center gap-1"
                href={`/api/v1/schedules/${encodeURIComponent(entry.schedule)}/ics`}><Download size={13} /> ICS</a>
            </CardAction>
          </CardHeader>
          <CardContent>
            {(entry.contacts?.length ?? 0) === 0
              ? <Empty text={t('nobodyOnDuty')} />
              : entry.contacts.map((c) => (
                <div key={c.id ?? c.name} className="py-1">
                  <div className="text-base font-semibold text-foreground inline-flex items-center gap-1.5"><Phone size={14} /> {c.name}</div>
                  <div className="text-xs text-muted-foreground">{c.email} {c.phone && `· ${c.phone}`}</div>
                </div>
              ))}
          </CardContent>
        </Card>
      ))}
      {(now?.length ?? 0) === 0 && <Empty text={t('noOnCallSchedules')} />}
    </div>
  )
}

// ——— Per-schedule detail: timeline + overrides + stats ———
export function ScheduleDetail({ schedule }: { schedule: string }) {
  const { nameOf } = useContactName()
  const { data: shifts } = useQuery({
    queryKey: ['schedules', schedule, 'timeline'],
    queryFn: () => {
      const { from, to } = dayRange(0, 14)
      return get<Shift[] | null>(`/schedules/${encodeURIComponent(schedule)}/timeline?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`)
    },
  })
  const { data: stats } = useQuery({
    queryKey: ['schedules', schedule, 'stats'],
    queryFn: () => get<StatRow[] | null>(`/schedules/${encodeURIComponent(schedule)}/stats`),
  })

  return (
    <Card>
      <CardHeader>
        <CardTitle>{`${schedule} — ${t('days14')}`}</CardTitle>
        <CardAction><OverrideManager schedule={schedule} /></CardAction>
      </CardHeader>
      <CardContent>
        <TimelineBlocks shifts={shifts ?? []} nameOf={nameOf} />
        {(stats?.length ?? 0) > 0 && (
          <div className="mt-3">
            <div className="text-xs text-muted-foreground font-medium mb-1">{t('hours')} {t('perPerson30Days')}</div>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('contact')}</TableHead>
                  <TableHead>{t('hours')}</TableHead>
                  <TableHead>{t('weekend')}</TableHead>
                  <TableHead>{t('overrides')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {stats!.map((row) => (
                  <TableRow key={row.contactId}>
                    <TableCell className="text-foreground">{row.contact || nameOf(row.contactId)}</TableCell>
                    <TableCell className="tabular-nums text-foreground/90">{row.hours.toFixed(1)}</TableCell>
                    <TableCell className="tabular-nums text-muted-foreground">{row.weekendHours.toFixed(1)}</TableCell>
                    <TableCell className="tabular-nums text-muted-foreground">{row.overrides}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

// Horizontal day blocks (no chart lib): group shifts by calendar day.
function TimelineBlocks({ shifts, nameOf }: { shifts: Shift[]; nameOf: (id: string) => string }) {
  if (shifts.length === 0) return <Empty text={t('noShifts')} />
  const palette = ['bg-blue-600/40', 'bg-emerald-600/40', 'bg-purple-600/40', 'bg-amber-600/40', 'bg-pink-600/40', 'bg-cyan-600/40']
  const colorByContact: Record<string, string> = {}
  let next = 0
  const colorOf = (id: string) => (colorByContact[id] ??= palette[next++ % palette.length] ?? 'bg-slate-600/40')

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-1">
        {shifts.map((sh, i) => (
          <div key={i}
            title={`${nameOf(sh.contactId)}: ${fmtTime(sh.start)} – ${fmtTime(sh.end)}${sh.override ? ' (Override)' : ''}`}
            className={`flex-1 min-w-24 rounded-md px-2 py-1.5 text-xs ${colorOf(sh.contactId)} ${sh.override ? 'ring-2 ring-amber-400/70 ring-inset' : ''}`}>
            <div className="text-[10px] text-white/60 tabular-nums">{new Date(sh.start).toLocaleDateString(undefined, { weekday: 'short', day: '2-digit', month: '2-digit' })}</div>
            <div className="text-white/90 truncate font-medium">{nameOf(sh.contactId)}{sh.override && ' ⤳'}</div>
          </div>
        ))}
      </div>
      <div className="flex justify-between text-[11px] text-muted-foreground/70 tabular-nums">
        <span>{fmtTime(shifts[0]?.start)}</span>
        <span>{fmtTime(shifts[shifts.length - 1]?.end)}</span>
      </div>
    </div>
  )
}

// Override list + create dialog, per schedule.
function OverrideManager({ schedule }: { schedule: string }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button size="sm" variant="outline" onClick={() => setOpen(true)}>{t('overrides')}</Button>
      {open && <OverrideDialog schedule={schedule} onClose={() => setOpen(false)} />}
    </>
  )
}

function OverrideDialog({ schedule, onClose }: { schedule: string; onClose: () => void }) {
  const { contacts, nameOf } = useContactName()
  // Overrides are not listed by a dedicated endpoint; surface them from the
  // timeline (override shifts carry override:true) so existing ones are visible.
  const { data: shifts, refetch } = useQuery({
    queryKey: ['schedules', schedule, 'timeline', 'overrides'],
    queryFn: () => {
      const { from, to } = dayRange(1, 30)
      return get<Shift[] | null>(`/schedules/${encodeURIComponent(schedule)}/timeline?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`)
    },
  })
  const overrideShifts = (shifts ?? []).filter((s) => s.override)

  const [contactId, setContactId] = useState('')
  const [start, setStart] = useState(nowPlus(0))
  const [end, setEnd] = useState(nowPlus(86400_000))
  const [reason, setReason] = useState('')

  const save = useSave(
    () => post<ScheduleOverride>(`/schedules/${encodeURIComponent(schedule)}/overrides`, {
      contactId, start: localInputToIso(start), end: localInputToIso(end), reason: reason || undefined,
    }),
    { invalidate: [['schedules', schedule]], onDone: () => { refetch(); setReason(''); setContactId('') } },
  )

  const onSubmit = (e: React.FormEvent) => { e.preventDefault(); save.mutate(undefined as unknown as void) }

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{`${t('overrides')}: ${schedule}`}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <form onSubmit={onSubmit} className="space-y-3 border border-border rounded-lg p-3 bg-card/40">
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('contact')} required>
                <Select value={contactId ? contactId : NONE} onValueChange={(v) => setContactId(v === NONE ? '' : v)}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NONE}>{t('selectDash')}</SelectItem>
                    {contacts.map((c) => <SelectItem key={c.name} value={c.id ?? c.name}>{c.name}</SelectItem>)}
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('reason')} hint="optional">
                <Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder={t('vacationPlaceholder')} />
              </Field>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('from')}><DateTimeInput value={start} onChange={setStart} /></Field>
              <Field label={t('to')}><DateTimeInput value={end} onChange={setEnd} /></Field>
            </div>
            <FormError error={save.error} />
            <SubmitRow onCancel={onClose} saving={save.isPending} label={t('add')} disabled={!contactId} />
          </form>

          <div>
            <div className="text-xs text-muted-foreground font-medium mb-1">{t('activeOverrides')}</div>
            {overrideShifts.length === 0 ? <Empty text={t('noOverrides')} /> : (
              <div className="space-y-1">
                {overrideShifts.map((o, i) => (
                  <div key={i} className="flex items-center gap-2 bg-card/60 border border-border rounded-md px-3 py-1.5">
                    <Badge variant="outline" className="bg-amber-500/15 text-amber-400 border-amber-500/30">Override</Badge>
                    <span className="text-sm text-foreground">{nameOf(o.contactId)}</span>
                    <span className="text-xs text-muted-foreground ml-auto tabular-nums">{fmtTime(o.start)} – {fmtTime(o.end)}</span>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
