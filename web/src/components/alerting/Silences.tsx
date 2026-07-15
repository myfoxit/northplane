// Silences tab (SPEC §9.2): TTL-mandatory mute rules. List + create
// dialog with quick-select expiry; delete = expire early.
import { useEffect, useState, type RefObject } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get, post, del, fmtTime, type ListResponse } from '../../api'
import type { Silence } from '../../types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { Empty, Field, FormError, SubmitRow, DeleteButton, useSave } from '@/components/kit'
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

export function SilencesTab({ createRef }: { createRef?: RefObject<() => void> }) {
  const { data } = useQuery({ queryKey: KEY, queryFn: fetchSilences })
  const [creating, setCreating] = useState(false)
  const remove = useSave((id: string) => del(`/silences/${encodeURIComponent(id)}`), { invalidate: [KEY] })
  useEffect(() => { if (createRef) createRef.current = () => setCreating(true) })

  return (
    <div className="space-y-3">
      {(data?.length ?? 0) === 0 ? <Empty text={t('noActiveSilences')} /> : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Selector</TableHead>
              <TableHead>Regex</TableHead>
              <TableHead>{t('comment')}</TableHead>
              <TableHead>{t('expires')}</TableHead>
              <TableHead>{t('from')}</TableHead>
              <TableHead>{t('actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data!.map((s) => (
              <TableRow key={s.id}>
                <TableCell className="font-mono text-xs text-foreground/90">{s.selector || '—'}</TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">{s.textRegex || '—'}</TableCell>
                <TableCell className="text-sm text-foreground/90">{s.comment}</TableCell>
                <TableCell className="text-xs text-muted-foreground tabular-nums">{fmtTime(s.expiresAt)}</TableCell>
                <TableCell className="text-xs text-muted-foreground">{s.createdBy ?? '—'}</TableCell>
                <TableCell className="text-right">
                  {s.id && <DeleteButton onDelete={() => remove.mutate(s.id!)} />}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
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
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{`${t('silences')} ${t('create').toLowerCase()}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} className="space-y-3">
          <Field label={t('selector')} hint={t('silenceSelectorHint')}>
            <Input value={selector} onChange={(e) => setSelector(e.target.value)} placeholder="env=prod" />
          </Field>
          <Field label={t('textRegex')} hint={t('textRegexHint')}>
            <Input value={textRegex} onChange={(e) => setTextRegex(e.target.value)} placeholder="disk.*full" />
          </Field>
          <Field label={t('comment')} required>
            <Input value={comment} onChange={(e) => setComment(e.target.value)} placeholder={t('maintenanceWindowPlaceholder')} />
          </Field>
          <Field label={t('expires')}>
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
      </DialogContent>
    </Dialog>
  )
}
