// Downtimes tab (SPEC §6.3/§11.4): scheduled maintenance windows on an
// object or a selector, fixed or flexible, optionally recurring (RRULE).
import { useEffect, useState, type RefObject } from 'react'
import { useQuery } from '@tanstack/react-query'
import { X } from 'lucide-react'
import { get, post, del, fmtTime, type ListResponse } from '../../api'
import type { Downtime, NPObject } from '../../types'
import { Badge } from '@/components/ui/badge'
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
import { Empty, Field, FormError, DurationInput, SubmitRow, DeleteButton, DuplicateButton, useSave } from '@/components/kit'
import { t } from '../../i18n'
import { DateTimeInput } from './common'
import { isAhead, isoToLocalInput, localInputToIso, nowPlus } from './datetime'

const KEY = ['downtimes']
const fetchDowntimes = () => get<ListResponse<Downtime>>('/downtimes').then((r) => r.items ?? [])

export function DowntimesTab({ createRef }: { createRef?: RefObject<() => void> }) {
  const { data } = useQuery({ queryKey: KEY, queryFn: fetchDowntimes })
  const [creating, setCreating] = useState(false)
  // Duplicate: the create dialog seeded from an existing downtime (target,
  // type, RRULE, comment — and its window while that still lies ahead).
  const [copying, setCopying] = useState<Downtime | null>(null)
  const remove = useSave((id: string) => del(`/downtimes/${encodeURIComponent(id)}`), { invalidate: [KEY] })
  useEffect(() => { if (createRef) createRef.current = () => setCreating(true) })

  return (
    <div className="space-y-3">
      {(data?.length ?? 0) === 0 ? <Empty text={t('noScheduledDowntimes')} /> : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('target')}</TableHead>
              <TableHead>{t('type')}</TableHead>
              <TableHead>{t('window')}</TableHead>
              <TableHead>RRULE</TableHead>
              <TableHead>{t('comment')}</TableHead>
              <TableHead>{t('actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data!.map((d) => (
              <TableRow key={d.id}>
                <TableCell className="font-mono text-xs text-foreground/90">{d.objectId || d.selector || '—'}</TableCell>
                <TableCell><Badge variant="outline" className="bg-muted text-foreground/90 border-input">{d.type}</Badge></TableCell>
                <TableCell className="text-xs text-muted-foreground tabular-nums">{fmtTime(d.start)} – {fmtTime(d.end)}</TableCell>
                <TableCell className="font-mono text-[11px] text-muted-foreground">{d.rrule || '—'}</TableCell>
                <TableCell className="text-sm text-foreground/90">{d.comment}</TableCell>
                <TableCell className="text-right">
                  <div className="flex gap-1 justify-end">
                    <DuplicateButton onClick={() => setCopying(d)} />
                    {d.id && <DeleteButton onDelete={() => remove.mutate(d.id!)} />}
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {creating && <DowntimeDialog onClose={() => setCreating(false)} />}
      {copying && <DowntimeDialog copyFrom={copying} onClose={() => setCopying(null)} />}
    </div>
  )
}

// DowntimeDialog: blank create, or — with `copyFrom` — a create seeded from an
// existing downtime. The source window is kept only while it still lies
// ahead; a window that already ended would create a downtime that is over
// before it starts, so that case falls back to the default "now + 1h".
function DowntimeDialog({ copyFrom, onClose }: { copyFrom?: Downtime; onClose: () => void }) {
  const src = copyFrom
  const windowAhead = isAhead(src?.end)
  const [targetKind, setTargetKind] = useState<'object' | 'selector'>(src?.selector && !src.objectId ? 'selector' : 'object')
  const [objectId, setObjectId] = useState(src?.objectId ?? '')
  const [selector, setSelector] = useState(src?.selector ?? '')
  const [type, setType] = useState<'fixed' | 'flexible'>(src?.type === 'flexible' ? 'flexible' : 'fixed')
  const [start, setStart] = useState(windowAhead ? isoToLocalInput(src!.start) : nowPlus(0))
  const [end, setEnd] = useState(windowAhead ? isoToLocalInput(src!.end) : nowPlus(3600_000))
  const [duration, setDuration] = useState(src?.duration ?? '1h')
  const [rrule, setRrule] = useState(src?.rrule ?? '')
  const [comment, setComment] = useState(src?.comment ?? '')

  const save = useSave(
    () => post<Downtime>('/downtimes', {
      objectId: targetKind === 'object' ? objectId || undefined : undefined,
      selector: targetKind === 'selector' ? selector || undefined : undefined,
      type,
      start: localInputToIso(start),
      end: localInputToIso(end),
      duration: type === 'flexible' ? duration || undefined : undefined,
      rrule: rrule || undefined,
      comment,
    }),
    { invalidate: [KEY], onDone: onClose },
  )

  const onSubmit = (e: React.FormEvent) => { e.preventDefault(); save.mutate(undefined as unknown as void) }
  const targetOk = targetKind === 'object' ? !!objectId : !!selector

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{src ? `${t('duplicate')}: ${src.comment || src.objectId || src.selector || t('downtime')}` : `${t('downtimes')} ${t('create').toLowerCase()}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} className="space-y-3">
          <div>
            <span className="text-xs text-muted-foreground font-medium">{t('target')}</span>
            <div className="flex gap-4 mt-1 mb-2">
              <label className="flex items-center gap-1.5 text-sm text-foreground/90 cursor-pointer">
                <input type="radio" checked={targetKind === 'object'} onChange={() => setTargetKind('object')} /> {t('object')}
              </label>
              <label className="flex items-center gap-1.5 text-sm text-foreground/90 cursor-pointer">
                <input type="radio" checked={targetKind === 'selector'} onChange={() => setTargetKind('selector')} /> {t('selector')}
              </label>
            </div>
            {targetKind === 'object'
              ? <ObjectPicker value={objectId} onChange={setObjectId} />
              : <Input value={selector} onChange={(e) => setSelector(e.target.value)} placeholder="env=prod" />}
          </div>

          <div className="grid grid-cols-2 gap-3">
            <Field label={t('type')}>
              <Select value={type} onValueChange={(v) => setType(v as 'fixed' | 'flexible')}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="fixed">fixed</SelectItem>
                  <SelectItem value="flexible">flexible</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            {type === 'flexible' && (
              <Field label={t('duration')} hint={t('durationHint')}>
                <DurationInput value={duration} onChange={setDuration} placeholder="1h" />
              </Field>
            )}
          </div>

          <div className="grid grid-cols-2 gap-3">
            <Field label={t('from')}><DateTimeInput value={start} onChange={setStart} /></Field>
            <Field label={t('to')}><DateTimeInput value={end} onChange={setEnd} /></Field>
          </div>

          <Field label="RRULE" hint={t('optionalRecurring')}>
            <Input value={rrule} onChange={(e) => setRrule(e.target.value)} placeholder="FREQ=WEEKLY;BYDAY=SA" />
          </Field>
          <Field label={t('comment')} required>
            <Input value={comment} onChange={(e) => setComment(e.target.value)} placeholder="Patch-Day" />
          </Field>

          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!comment || !targetOk} label={t('create')} />
        </form>
      </DialogContent>
    </Dialog>
  )
}

// Searchable object picker fed by GET /objects?q=.
function ObjectPicker({ value, onChange }: { value: string; onChange: (id: string) => void }) {
  const [q, setQ] = useState('')
  const { data } = useQuery({
    queryKey: ['objects', 'search', q],
    queryFn: () => get<ListResponse<NPObject>>(`/objects?q=${encodeURIComponent(q)}&limit=20`).then((r) => r.items ?? []),
    enabled: q.length > 0,
  })
  const chosen = value ? (data ?? []).find((o) => o.id === value) : undefined

  return (
    <div className="space-y-1.5">
      <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('searchObject')} />
      {value && (
        <div className="flex items-center gap-2 text-xs">
          <Badge variant="outline" className="bg-primary/15 text-primary border-primary/30">{chosen?.name ?? value}</Badge>
          <button type="button" aria-label={t('remove')} className="text-muted-foreground hover:text-red-400" onClick={() => onChange('')}><X size={13} /></button>
        </div>
      )}
      {q.length > 0 && (data?.length ?? 0) > 0 && (
        <div className="max-h-40 overflow-y-auto border border-border rounded-lg divide-y divide-border/70">
          {data!.map((o) => (
            <button key={o.id} type="button"
              onClick={() => { onChange(o.id); setQ('') }}
              className="w-full text-left px-3 py-1.5 text-sm text-foreground/90 hover:bg-muted/50 flex items-center gap-2">
              <span className="text-[10px] text-muted-foreground uppercase">{o.kind}</span>
              <span className="truncate">{o.name}</span>
              {o.hostName && <span className="text-xs text-muted-foreground/70">@ {o.hostName}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
