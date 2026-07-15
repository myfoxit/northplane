// Notification dead-letter queue (F-04.04): deliveries that exhausted all
// retries. Surfacing them in the UI closes the API↔UI parity gap — the
// endpoints existed, but troubleshooting needed curl. Replay requeues the
// delivery with a fresh backoff.
import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { get, post, queryClient, fmtTime, type ListResponse } from '../../api'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Empty, Spinner } from '@/components/kit'
import { t } from '../../i18n'

interface DeadLetter {
  id: string
  kind: string
  channelId?: string
  attempts: number
  lastError?: string
  createdAt: string
}

export function DeadLettersTab() {
  const { data, isLoading } = useQuery({
    queryKey: ['dead-letters'],
    queryFn: () => get<ListResponse<DeadLetter>>('/notifications/dead-letters'),
  })
  const [note, setNote] = useState('')
  const replay = useMutation({
    mutationFn: (id: string) => post(`/notifications/dead-letters/${encodeURIComponent(id)}:replay`),
    onSuccess: () => {
      setNote(t('requeued'))
      queryClient.invalidateQueries({ queryKey: ['dead-letters'] })
    },
    onError: (e: unknown) => setNote(`✕ ${e instanceof Error ? e.message : String(e)}`),
  })
  const rows = data?.items ?? []
  return (
    <div className="space-y-3">
      <p className="text-xs text-muted-foreground">
        {t('deadLettersIntro')}
        {note && <span className={`ml-2 ${note.startsWith('✓') ? 'text-emerald-400' : 'text-red-400'}`}>{note}</span>}
      </p>
      {isLoading && <Spinner />}
      {!isLoading && rows.length === 0 && <Empty text={t('noDeadLetters')} />}
      {rows.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              {[t('time'), t('kind'), t('attempts'), t('lastError'), ''].map((h, i) => <TableHead key={i}>{h}</TableHead>)}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((d) => (
              <TableRow key={d.id}>
                <TableCell className="text-xs text-muted-foreground tabular-nums">{fmtTime(d.createdAt)}</TableCell>
                <TableCell><Badge variant="outline" className="bg-muted text-muted-foreground border-input">{d.kind}</Badge></TableCell>
                <TableCell className="text-xs tabular-nums">{d.attempts}</TableCell>
                <TableCell className="text-xs text-red-400/90 font-mono truncate max-w-md">{d.lastError || '—'}</TableCell>
                <TableCell className="text-right">
                  <Button size="sm" variant="outline" onClick={() => replay.mutate(d.id)} disabled={replay.isPending}>
                    Replay
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}
