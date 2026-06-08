// Downtimes tab (SPEC §6.3/§11.4): scheduled maintenance windows on an
// object or a selector, fixed or flexible, optionally recurring (RRULE).
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { X } from 'lucide-react'
import { get, post, del, fmtTime, type ListResponse } from '../../api'
import type { Downtime, NPObject } from '../../types'
import { Badge, Button, Dialog, Empty, Input, Table } from '../ui'
import { Field, FormError, DurationInput, SubmitRow, DeleteButton, useSave } from '../forms'
import { t } from '../../i18n'
import { DateTimeInput } from './common'
import { localInputToIso, nowPlus } from './datetime'

const KEY = ['downtimes']
const fetchDowntimes = () => get<ListResponse<Downtime>>('/downtimes').then((r) => r.items ?? [])

export function DowntimesTab() {
  const { data } = useQuery({ queryKey: KEY, queryFn: fetchDowntimes })
  const [creating, setCreating] = useState(false)
  const remove = useSave((id: string) => del(`/downtimes/${encodeURIComponent(id)}`), { invalidate: [KEY] })

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Button variant="primary" onClick={() => setCreating(true)}>{t('create')}</Button>
      </div>
      {(data?.length ?? 0) === 0 ? <Empty text="Keine geplanten Downtimes." /> : (
        <Table head={[t('target'), t('type'), 'Fenster', 'RRULE', t('comment'), t('actions')]}>
          {data!.map((d) => (
            <tr key={d.id} className="hover:bg-muted/30">
              <td className="px-3 py-2 font-mono text-xs text-foreground/90">{d.objectId || d.selector || '—'}</td>
              <td className="px-3 py-2"><Badge className="bg-muted text-foreground/90 border-input">{d.type}</Badge></td>
              <td className="px-3 py-2 text-xs text-muted-foreground tabular-nums">{fmtTime(d.start)} – {fmtTime(d.end)}</td>
              <td className="px-3 py-2 font-mono text-[11px] text-muted-foreground">{d.rrule || '—'}</td>
              <td className="px-3 py-2 text-sm text-foreground/90">{d.comment}</td>
              <td className="px-3 py-2 text-right">
                {d.id && <DeleteButton onDelete={() => remove.mutate(d.id!)} />}
              </td>
            </tr>
          ))}
        </Table>
      )}
      {creating && <DowntimeDialog onClose={() => setCreating(false)} />}
    </div>
  )
}

function DowntimeDialog({ onClose }: { onClose: () => void }) {
  const [targetKind, setTargetKind] = useState<'object' | 'selector'>('object')
  const [objectId, setObjectId] = useState('')
  const [selector, setSelector] = useState('')
  const [type, setType] = useState<'fixed' | 'flexible'>('fixed')
  const [start, setStart] = useState(nowPlus(0))
  const [end, setEnd] = useState(nowPlus(3600_000))
  const [duration, setDuration] = useState('1h')
  const [rrule, setRrule] = useState('')
  const [comment, setComment] = useState('')

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
    <Dialog open onClose={onClose} title={`${t('downtimes')} ${t('create').toLowerCase()}`} size="md">
      <form onSubmit={onSubmit} className="space-y-3">
        <div>
          <span className="text-xs text-muted-foreground font-medium">{t('target')}</span>
          <div className="flex gap-4 mt-1 mb-2">
            <label className="flex items-center gap-1.5 text-sm text-foreground/90 cursor-pointer">
              <input type="radio" checked={targetKind === 'object'} onChange={() => setTargetKind('object')} /> Objekt
            </label>
            <label className="flex items-center gap-1.5 text-sm text-foreground/90 cursor-pointer">
              <input type="radio" checked={targetKind === 'selector'} onChange={() => setTargetKind('selector')} /> Selector
            </label>
          </div>
          {targetKind === 'object'
            ? <ObjectPicker value={objectId} onChange={setObjectId} />
            : <Input value={selector} onChange={(e) => setSelector(e.target.value)} placeholder="env=prod" />}
        </div>

        <div className="grid grid-cols-2 gap-3">
          <Field label={t('type')}>
            <select value={type} onChange={(e) => setType(e.target.value as 'fixed' | 'flexible')}
              className="bg-card border border-input rounded-lg px-3 py-1.5 text-sm text-foreground w-full focus:border-ring cursor-pointer">
              <option value="fixed">fixed</option>
              <option value="flexible">flexible</option>
            </select>
          </Field>
          {type === 'flexible' && (
            <Field label="Dauer" hint="aktive Zeit im Fenster">
              <DurationInput value={duration} onChange={setDuration} placeholder="1h" />
            </Field>
          )}
        </div>

        <div className="grid grid-cols-2 gap-3">
          <Field label={t('from')}><DateTimeInput value={start} onChange={setStart} /></Field>
          <Field label={t('to')}><DateTimeInput value={end} onChange={setEnd} /></Field>
        </div>

        <Field label="RRULE" hint="optional, wiederkehrend">
          <Input value={rrule} onChange={(e) => setRrule(e.target.value)} placeholder="FREQ=WEEKLY;BYDAY=SA" />
        </Field>
        <Field label={t('comment')} required>
          <Input value={comment} onChange={(e) => setComment(e.target.value)} placeholder="Patch-Day" />
        </Field>

        <FormError error={save.error} />
        <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!comment || !targetOk} label={t('create')} />
      </form>
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
      <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Objekt suchen…" />
      {value && (
        <div className="flex items-center gap-2 text-xs">
          <Badge className="bg-primary/15 text-primary border-primary/30">{chosen?.name ?? value}</Badge>
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
