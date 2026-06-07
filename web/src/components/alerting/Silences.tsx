// Silences tab (SPEC §9.2): TTL-mandatory mute rules. List + create
// dialog with quick-select expiry; delete = expire early.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get, post, del, fmtTime, type ListResponse } from '../../api'
import type { Silence } from '../../types'
import { Button, Dialog, Empty, Input, Table } from '../ui'
import { Field, FormError, SubmitRow, DeleteButton, useSave } from '../forms'
import { t } from '../../i18n'
import { DateTimeInput } from './common'
import { localInputToIso, nowPlus } from './datetime'

const KEY = ['silences']
const fetchSilences = () => get<ListResponse<Silence>>('/silences').then((r) => r.items ?? [])

const quick: { label: string; ms: number }[] = [
  { label: '1h', ms: 3600_000 },
  { label: '4h', ms: 4 * 3600_000 },
  { label: '24h', ms: 24 * 3600_000 },
]

export function SilencesTab() {
  const { data } = useQuery({ queryKey: KEY, queryFn: fetchSilences })
  const [creating, setCreating] = useState(false)
  const remove = useSave((id: string) => del(`/silences/${encodeURIComponent(id)}`), { invalidate: [KEY] })

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Button variant="primary" onClick={() => setCreating(true)}>{t('create')}</Button>
      </div>
      {(data?.length ?? 0) === 0 ? <Empty text="Keine aktiven Silences." /> : (
        <Table head={['Selector', 'Regex', t('comment'), 'Läuft ab', 'Von', t('actions')]}>
          {data!.map((s) => (
            <tr key={s.id} className="hover:bg-slate-800/30">
              <td className="px-3 py-2 font-mono text-xs text-slate-300">{s.selector || '—'}</td>
              <td className="px-3 py-2 font-mono text-xs text-slate-400">{s.textRegex || '—'}</td>
              <td className="px-3 py-2 text-sm text-slate-300">{s.comment}</td>
              <td className="px-3 py-2 text-xs text-slate-400 tabular-nums">{fmtTime(s.expiresAt)}</td>
              <td className="px-3 py-2 text-xs text-slate-500">{s.createdBy ?? '—'}</td>
              <td className="px-3 py-2 text-right">
                {s.id && <DeleteButton onDelete={() => remove.mutate(s.id!)} />}
              </td>
            </tr>
          ))}
        </Table>
      )}
      {creating && <SilenceDialog onClose={() => setCreating(false)} />}
    </div>
  )
}

function SilenceDialog({ onClose }: { onClose: () => void }) {
  const [selector, setSelector] = useState('')
  const [textRegex, setTextRegex] = useState('')
  const [comment, setComment] = useState('')
  const [expires, setExpires] = useState(nowPlus(3600_000))

  const save = useSave(
    () => post<Silence>('/silences', {
      selector: selector || undefined,
      textRegex: textRegex || undefined,
      comment,
      expiresAt: localInputToIso(expires),
    }),
    { invalidate: [KEY], onDone: onClose },
  )

  const onSubmit = (e: React.FormEvent) => { e.preventDefault(); save.mutate(undefined as unknown as void) }

  return (
    <Dialog open onClose={onClose} title={`${t('silences')} ${t('create').toLowerCase()}`} size="md">
      <form onSubmit={onSubmit} className="space-y-3">
        <Field label="Selector" hint="Label-Selector — Selector oder Regex erforderlich">
          <Input value={selector} onChange={(e) => setSelector(e.target.value)} placeholder="env=prod" />
        </Field>
        <Field label="Text-Regex" hint="passt auf Alarm-Titel">
          <Input value={textRegex} onChange={(e) => setTextRegex(e.target.value)} placeholder="disk.*full" />
        </Field>
        <Field label={t('comment')} required>
          <Input value={comment} onChange={(e) => setComment(e.target.value)} placeholder="Wartungsfenster DB" />
        </Field>
        <Field label="Läuft ab">
          <div className="flex gap-1.5 mb-1.5">
            {quick.map((q) => (
              <Button key={q.label} size="sm" type="button" variant="ghost"
                onClick={() => setExpires(nowPlus(q.ms))}>{q.label}</Button>
            ))}
          </div>
          <DateTimeInput value={expires} onChange={setExpires} />
        </Field>
        <FormError error={save.error} />
        <SubmitRow onCancel={onClose} saving={save.isPending}
          disabled={!comment || (!selector && !textRegex) || !expires} label={t('create')} />
      </form>
    </Dialog>
  )
}
