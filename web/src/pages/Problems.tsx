// Problems view (SPEC §12.3): priority-sorted, inline ack/downtime in
// ≤ 3 clicks, handled toggle, live via SSE invalidation.
import { useState } from 'react'
import { keepPreviousData, useQuery, useMutation } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { RefreshCw, Check } from 'lucide-react'
import { get, post, queryClient, fmtAgo } from '../api'
import type { ProblemRow, Alert } from '../types'
import { stateLabel, stateIcon, stateColor } from '../types'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Empty, ErrorState } from '@/components/kit'
import { AckDialog, DowntimeDialog } from '../components/AckDialog'
import { t } from '../i18n'

export function ProblemsPage() {
  const [includeHandled, setIncludeHandled] = useState(false)
  const [ackTarget, setAckTarget] = useState<{ alertId: string; name: string } | null>(null)
  const [dtTarget, setDtTarget] = useState<{ objectId: string; name: string } | null>(null)

  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['problems', includeHandled],
    queryFn: () => get<{ items: ProblemRow[] | null }>(`/problems?includeHandled=${includeHandled}`),
    placeholderData: keepPreviousData, // toggle shows the last list instantly
  })
  const { data: alerts } = useQuery({
    queryKey: ['alerts', 'open-map'],
    queryFn: () => get<{ items: Alert[] | null }>('/alerts?status=open&limit=500'),
  })
  const alertByObject: Record<string, Alert> = {}
  for (const a of alerts?.items ?? []) {
    if (a.objectId) alertByObject[a.objectId] = a
  }

  const recheck = useMutation({
    mutationFn: (objectId: string) => post(`/objects/${objectId}/check-now`),
    onSuccess: () => setTimeout(() => queryClient.invalidateQueries({ queryKey: ['problems'] }), 2000),
  })

  const rows = data?.items ?? []
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-bold">{t('problems')} <span className="text-muted-foreground text-sm">({rows.length})</span></h1>
        <label className="text-xs text-muted-foreground flex items-center gap-2 cursor-pointer">
          <input type="checkbox" checked={includeHandled} onChange={(e) => setIncludeHandled(e.target.checked)} />
          inkl. quittiert/Downtime
        </label>
      </div>
      {isLoading && <Empty text={t('loading')} />}
      {isError && !isLoading && <ErrorState error={error} onRetry={() => refetch()} />}
      {!isLoading && !isError && rows.length === 0 && (
        <div className="text-success/90 text-sm p-8 text-center border border-success/30 rounded-xl bg-success/5 flex items-center justify-center gap-2">
          <Check size={16} /> {t('noProblems')}
        </div>
      )}
      <div className="space-y-1.5">
        {rows.map((p) => {
          const alert = alertByObject[p.object.id]
          return (
            <div key={p.object.id}
              className="flex items-center gap-3 bg-card/50 border border-border rounded-lg px-3 py-2 group">
              <span className={`${stateColor(p.object.kind, p.state.state)} font-bold text-sm w-24 shrink-0`}>
                {stateIcon(p.object.kind, p.state.state)} {stateLabel(p.object.kind, p.state.state)}
              </span>
              <div className="min-w-0 flex-1">
                <Link to="/objects/$id" params={{ id: p.object.id }}
                  className="text-sm font-medium text-foreground hover:text-primary">
                  {p.object.kind === 'service' && p.object.hostName ? `${p.object.hostName} / ` : ''}{p.object.name}
                </Link>
                <div className="text-xs text-muted-foreground truncate">{p.state.output}</div>
              </div>
              <div className="flex items-center gap-1.5 shrink-0">
                {p.state.ackedBy && <Badge variant="outline" className="bg-sky-500/10 text-sky-400 border-sky-800">{t('acked')}: {p.state.ackedBy}</Badge>}
                {p.state.downtimeDepth > 0 && <Badge variant="outline" className="bg-muted text-muted-foreground border-input">{t('inDowntime')}</Badge>}
                {p.state.flapping && <Badge variant="outline" className="bg-purple-500/10 text-purple-400 border-purple-800">{t('flapping')}</Badge>}
                <span className="text-xs text-muted-foreground/70 tabular-nums w-16 text-right">{fmtAgo(p.state.lastHardChange)}</span>
              </div>
              <div className="flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity shrink-0">
                {alert && !p.state.ackedBy && (
                  <Button size="sm" onClick={() => setAckTarget({ alertId: alert.id, name: p.object.name })}>
                    {t('ack')}
                  </Button>
                )}
                <Button size="sm" onClick={() => setDtTarget({ objectId: p.object.id, name: p.object.name })}>
                  {t('downtime')}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => recheck.mutate(p.object.id)} title={t('checkNow')} aria-label={t('checkNow')}>
                  <RefreshCw size={14} />
                </Button>
              </div>
            </div>
          )
        })}
      </div>
      <AckDialog open={!!ackTarget} alertId={ackTarget?.alertId ?? ''} objectName={ackTarget?.name}
        onClose={() => setAckTarget(null)} />
      <DowntimeDialog open={!!dtTarget} objectId={dtTarget?.objectId ?? ''} objectName={dtTarget?.name}
        onClose={() => setDtTarget(null)} />
    </div>
  )
}
