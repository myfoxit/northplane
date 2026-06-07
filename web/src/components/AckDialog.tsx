// Ack/Downtime dialogs with consequence text (SPEC §12.4: destructive/
// mutating actions confirm with what happens; ack ≤ 3 clicks total).
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { post, queryClient } from '../api'
import { Button, Dialog, Input } from './ui'
import { t } from '../i18n'

export function AckDialog({ alertId, objectName, open, onClose }:
  { alertId: string; objectName?: string; open: boolean; onClose: () => void }) {
  const [comment, setComment] = useState('')
  const ack = useMutation({
    mutationFn: () => post(`/alerts/${alertId}:ack`, { comment }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['alerts'] })
      queryClient.invalidateQueries({ queryKey: ['problems'] })
      onClose()
    },
  })
  return (
    <Dialog open={open} onClose={onClose} title={`${t('ack')}${objectName ? ` — ${objectName}` : ''}`}>
      <p className="text-xs text-amber-400/90 mb-3">{t('ackConfirm')}</p>
      <Input
        placeholder={t('comment')} value={comment} autoFocus
        onChange={(e) => setComment(e.target.value)}
        onKeyDown={(e) => { if (e.key === 'Enter') ack.mutate() }}
      />
      {ack.isError && <p className="text-xs text-red-400 mt-2">{String(ack.error)}</p>}
      <div className="flex justify-end gap-2 mt-4">
        <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
        <Button variant="primary" onClick={() => ack.mutate()} disabled={ack.isPending}>
          {t('ack')}
        </Button>
      </div>
    </Dialog>
  )
}

export function DowntimeDialog({ objectId, objectName, open, onClose }:
  { objectId: string; objectName?: string; open: boolean; onClose: () => void }) {
  const [comment, setComment] = useState('')
  const [hours, setHours] = useState('2')
  const dt = useMutation({
    mutationFn: () => {
      const start = new Date()
      const end = new Date(start.getTime() + parseFloat(hours || '2') * 3600_000)
      return post('/downtimes', {
        objectId, type: 'fixed',
        start: start.toISOString(), end: end.toISOString(), comment,
      })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['problems'] })
      queryClient.invalidateQueries({ queryKey: ['downtimes'] })
      onClose()
    },
  })
  return (
    <Dialog open={open} onClose={onClose} title={`${t('downtime')}${objectName ? ` — ${objectName}` : ''}`}>
      <div className="space-y-3">
        <div>
          <label className="text-xs text-slate-400 block mb-1">{t('hours')}</label>
          <Input type="number" min="0.5" step="0.5" value={hours} onChange={(e) => setHours(e.target.value)} />
        </div>
        <div>
          <label className="text-xs text-slate-400 block mb-1">{t('comment')} *</label>
          <Input value={comment} autoFocus onChange={(e) => setComment(e.target.value)} />
        </div>
      </div>
      {dt.isError && <p className="text-xs text-red-400 mt-2">{String(dt.error)}</p>}
      <div className="flex justify-end gap-2 mt-4">
        <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
        <Button variant="primary" onClick={() => dt.mutate()} disabled={dt.isPending || !comment}>
          {t('confirm')}
        </Button>
      </div>
    </Dialog>
  )
}
