// Alerts & Incidents (SPEC §12.3).
import { useState } from 'react'
import { keepPreviousData, useQuery, useMutation } from '@tanstack/react-query'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { Sparkles } from 'lucide-react'
import { get, post, queryClient, fmtAgo, type ListResponse } from '../api'
import type { Alert, AlertsSearch, Incident, Severity } from '../types'
import { sevColor } from '../types'
import { Button, Badge, Empty, Card, ErrorState } from '../components/ui'
import { AckDialog } from '../components/AckDialog'
import { t } from '../i18n'

export function AlertsPage() {
  // Status + severity filters live in the URL (linkable, back-button).
  const search = useSearch({ strict: false }) as AlertsSearch
  const navigate = useNavigate()
  const status = search.status ?? 'open,acked'
  const severity = search.severity ?? ''
  const patchSearch = (patch: Partial<AlertsSearch>) =>
    navigate({
      to: '/alerts',
      search: (prev) => ({ ...prev, ...patch }),
      replace: true,
    })
  const [ackTarget, setAckTarget] = useState<Alert | null>(null)
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['alerts', status],
    queryFn: () => get<ListResponse<Alert>>(`/alerts?status=${status}&limit=200`),
    placeholderData: keepPreviousData,
  })
  const resolve = useMutation({
    mutationFn: (id: string) => post(`/alerts/${id}:resolve`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['alerts'] }),
  })
  const rows = (data?.items ?? []).filter((a) => !severity || a.severity === (severity as Severity))
  if (isError && !data) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <h1 className="text-lg font-bold">{t('alerts')} <span className="text-muted-foreground text-sm">({rows.length})</span></h1>
        <div className="flex gap-2">
          <select value={severity} onChange={(e) => patchSearch({ severity: e.target.value || undefined })}
            className="bg-card border border-input rounded-lg px-2 py-1 text-sm text-foreground/90">
            <option value="">alle Severities</option>
            <option value="critical">critical</option>
            <option value="warning">warning</option>
            <option value="info">info</option>
          </select>
          <select value={status} onChange={(e) => patchSearch({ status: e.target.value })}
            className="bg-card border border-input rounded-lg px-2 py-1 text-sm text-foreground/90">
            <option value="open,acked">offen + quittiert</option>
            <option value="open">nur offen</option>
            <option value="resolved,expired">geschlossen</option>
          </select>
        </div>
      </div>
      {isLoading && <Empty text={t('loading')} />}
      {!isLoading && rows.length === 0 && <Empty text={t('empty')} />}
      <div className="space-y-1.5">
        {rows.map((a) => (
          <div key={a.id} className="flex items-center gap-3 bg-card/50 border border-border rounded-lg px-3 py-2 group">
            <Badge className={sevColor(a.severity)}>{a.severity}</Badge>
            <div className="min-w-0 flex-1">
              <div className="text-sm text-foreground font-medium truncate">{a.title}</div>
              <div className="text-xs text-muted-foreground">
                {a.status}{a.ackedBy ? ` von ${a.ackedBy}` : ''} · seit {fmtAgo(a.openedAt)}
                {a.dedupKey ? ` · ${a.dedupKey}` : ''}
              </div>
            </div>
            <div className="flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
              {a.status === 'open' && (
                <Button size="sm" onClick={() => setAckTarget(a)}>{t('ack')}</Button>
              )}
              {(a.status === 'open' || a.status === 'acked') && (
                <Button size="sm" variant="ghost" onClick={() => resolve.mutate(a.id)}>{t('resolve')}</Button>
              )}
            </div>
          </div>
        ))}
      </div>
      <AckDialog open={!!ackTarget} alertId={ackTarget?.id ?? ''} objectName={ackTarget?.title}
        onClose={() => setAckTarget(null)} />
    </div>
  )
}

export function IncidentsPage() {
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['incidents'],
    queryFn: () => get<ListResponse<Incident>>('/incidents?limit=100'),
  })
  const resolve = useMutation({
    mutationFn: (id: string) => post(`/incidents/${id}:resolve`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['incidents'] }),
  })
  const summarize = useMutation({
    mutationFn: (id: string) => post(`/incidents/${id}:summarize`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['incidents'] }),
  })
  const rows = data?.items ?? []
  if (isError && !data) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-bold">{t('incidents')}</h1>
      {isLoading && <Empty text={t('loading')} />}
      {!isLoading && rows.length === 0 && <Empty text={t('empty')} />}
      <div className="grid lg:grid-cols-2 gap-3">
        {rows.map((inc) => (
          <Card key={inc.id} title={
            <span className="flex items-center gap-2">
              <Badge className={sevColor(inc.severity)}>{inc.severity}</Badge>
              <span className={inc.status === 'resolved' ? 'line-through text-muted-foreground' : ''}>{inc.title}</span>
            </span>
          } actions={
            inc.status === 'open' ? (
              <div className="flex gap-1">
                <Button size="sm" variant="ghost" onClick={() => summarize.mutate(inc.id)}
                  disabled={summarize.isPending} title="AI-Zusammenfassung" aria-label="AI-Zusammenfassung"><Sparkles size={14} /></Button>
                <Button size="sm" onClick={() => resolve.mutate(inc.id)}>{t('resolve')}</Button>
              </div>
            ) : undefined
          }>
            <div className="text-xs text-muted-foreground mb-1">
              {inc.createdBy} · {fmtAgo(inc.openedAt)} · {inc.impact}
            </div>
            {inc.summary && <p className="text-sm text-foreground/90 whitespace-pre-wrap">{inc.summary}</p>}
            {inc.ticketUrl && (
              <a href={inc.ticketUrl} target="_blank" rel="noreferrer"
                className="text-xs text-primary hover:underline">Ticket ↗</a>
            )}
          </Card>
        ))}
      </div>
    </div>
  )
}
