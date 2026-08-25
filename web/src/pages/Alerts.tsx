// Alerts & Incidents (SPEC §12.3).
import { useState } from 'react'
import { keepPreviousData, useQuery, useMutation } from '@tanstack/react-query'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { Check, Siren, Sparkles } from 'lucide-react'
import { get, post, queryClient, fmtAgo, type ListResponse } from '../api'
import type { Alert, AlertsSearch, Incident, Severity } from '../types'
import { sevColor } from '../types'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardHeader, CardTitle, CardAction, CardContent } from '@/components/ui/card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Empty, ErrorState, Field, FormError, SubmitRow, useSave } from '@/components/kit'
import { AckDialog } from '../components/AckDialog'
import { TriggerAlertDialog } from '../components/TriggerAlertDialog'
import { t } from '../i18n'
import { useRefreshInterval } from '../settings'

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
  const [triggerOpen, setTriggerOpen] = useState(false)
  const [toast, setToast] = useState<string | null>(null)
  const refresh = useRefreshInterval()
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['alerts', status],
    queryFn: () => get<ListResponse<Alert>>(`/alerts?status=${status}&limit=200`),
    placeholderData: keepPreviousData,
    refetchInterval: refresh,
  })
  const resolve = useMutation({
    mutationFn: (id: string) => post(`/alerts/${id}:resolve`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['alerts'] }),
  })
  // Snooze = ack now, auto-unack at `until` (UX-1: was API-only).
  const snooze = useMutation({
    mutationFn: ({ id, hours }: { id: string; hours: number }) =>
      post(`/alerts/${id}:snooze`, { until: new Date(Date.now() + hours * 3_600_000).toISOString() }),
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
          <Button variant="destructive" onClick={() => setTriggerOpen(true)}>
            <Siren size={14} /> {t('triggerAlert')}
          </Button>
          <Select value={severity || '__all__'}
            onValueChange={(v) => patchSearch({ severity: v === '__all__' ? undefined : v })}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="__all__">{t('allSeverities')}</SelectItem>
              <SelectItem value="critical">critical</SelectItem>
              <SelectItem value="warning">warning</SelectItem>
              <SelectItem value="info">info</SelectItem>
            </SelectContent>
          </Select>
          <Select value={status} onValueChange={(v) => patchSearch({ status: v })}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="open,acked">{t('openAcked')}</SelectItem>
              <SelectItem value="open">{t('onlyOpen')}</SelectItem>
              <SelectItem value="resolved,expired">{t('closed')}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
      {toast && (
        <div className="text-sm text-emerald-400 bg-emerald-500/10 border border-emerald-500/30 rounded-lg px-3 py-2 flex items-center gap-1.5">
          <Check size={14} /> {toast}
        </div>
      )}
      {isLoading && <Empty text={t('loading')} />}
      {!isLoading && rows.length === 0 && <Empty text={t('noAlertsFriendly')} />}
      <div className="space-y-1.5">
        {rows.map((a) => (
          <div key={a.id} className="flex items-center gap-3 bg-card/50 border border-border rounded-lg px-3 py-2 group">
            <Badge variant="outline" className={sevColor(a.severity)}>{a.severity}</Badge>
            <div className="min-w-0 flex-1">
              <div className="text-sm text-foreground font-medium truncate">{a.title}</div>
              <div className="text-xs text-muted-foreground">
                {a.status}{a.ackedBy ? ` ${t('by')} ${a.ackedBy}` : ''} · {t('since')} {fmtAgo(a.openedAt)}
                {a.dedupKey ? ` · ${a.dedupKey}` : ''}
              </div>
            </div>
            <div className="flex gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
              {a.status === 'open' && (
                <Button size="sm" onClick={() => setAckTarget(a)}>{t('ack')}</Button>
              )}
              {a.status === 'open' && (
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button size="sm" variant="ghost">{t('snoozeAction')}</Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    {([1, 4, 24] as const).map((h) => (
                      <DropdownMenuItem key={h} onClick={() => snooze.mutate({ id: a.id, hours: h })}>
                        {h} {t('hours')}
                      </DropdownMenuItem>
                    ))}
                  </DropdownMenuContent>
                </DropdownMenu>
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
      {triggerOpen && (
        <TriggerAlertDialog
          onClose={() => setTriggerOpen(false)}
          onRaised={() => {
            setToast(t('alertRaised'))
            setTimeout(() => setToast(null), 2500)
          }}
        />
      )}
    </div>
  )
}

// Manual incident creation (INC-1): title + severity + impact + ticket URL
// onto POST /incidents — the same shape the API accepts.
function NewIncidentDialog({ onClose }: { onClose: () => void }) {
  const [title, setTitle] = useState('')
  const [severity, setSeverity] = useState<Severity>('warning')
  const [impact, setImpact] = useState('')
  const [ticketUrl, setTicketUrl] = useState('')
  const save = useSave<void>(
    () => post<Incident>('/incidents', {
      title,
      severity,
      ...(impact ? { impact } : {}),
      ...(ticketUrl ? { ticketUrl } : {}),
    }),
    { invalidate: [['incidents']], onDone: onClose },
  )
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-md">
        <DialogHeader><DialogTitle>{t('newIncident')}</DialogTitle></DialogHeader>
        <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); save.mutate() }}>
          <Field label={t('title')} required>
            <Input value={title} onChange={(e) => setTitle(e.target.value)} autoFocus />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('severity')}>
              <Select value={severity} onValueChange={(v) => setSeverity(v as Severity)}>
                <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {(['critical', 'warning', 'info'] as const).map((s) => (
                    <SelectItem key={s} value={s}>{s}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label={t('impact')}>
              <Input value={impact} onChange={(e) => setImpact(e.target.value)} />
            </Field>
          </div>
          <Field label="Ticket-URL" hint="https://…">
            <Input value={ticketUrl} onChange={(e) => setTicketUrl(e.target.value)} type="url" />
          </Field>
          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!title} label={t('create')} />
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function IncidentsPage() {
  const refresh = useRefreshInterval()
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['incidents'],
    queryFn: () => get<ListResponse<Incident>>('/incidents?limit=100'),
    refetchInterval: refresh,
  })
  const resolve = useMutation({
    mutationFn: (id: string) => post(`/incidents/${id}:resolve`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['incidents'] }),
  })
  const summarize = useMutation({
    mutationFn: (id: string) => post(`/incidents/${id}:summarize`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['incidents'] }),
  })
  const [createOpen, setCreateOpen] = useState(false)
  const rows = data?.items ?? []
  if (isError && !data) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <h1 className="text-lg font-bold">{t('incidents')}</h1>
        {/* Manual incidents were API-only (POST /incidents) while the empty
            state said "open one manually" — the button closes that gap (INC-1). */}
        <Button variant="default" onClick={() => setCreateOpen(true)}>{t('newIncident')}</Button>
      </div>
      {isLoading && <Empty text={t('loading')} />}
      {!isLoading && rows.length === 0 && <Empty text={t('noIncidentsFriendly')} />}
      {createOpen && <NewIncidentDialog onClose={() => setCreateOpen(false)} />}
      <div className="grid lg:grid-cols-2 gap-3">
        {rows.map((inc) => (
          <Card key={inc.id}>
            <CardHeader>
              <CardTitle>
                <span className="flex items-center gap-2">
                  <Badge variant="outline" className={sevColor(inc.severity)}>{inc.severity}</Badge>
                  <span className={inc.status === 'resolved' ? 'line-through text-muted-foreground' : ''}>{inc.title}</span>
                </span>
              </CardTitle>
              {inc.status === 'open' && (
                <CardAction>
                  <div className="flex gap-1">
                    <Button size="sm" variant="ghost" onClick={() => summarize.mutate(inc.id)}
                      disabled={summarize.isPending} title={t('aiSummary')} aria-label={t('aiSummary')}><Sparkles size={14} /></Button>
                    <Button size="sm" onClick={() => resolve.mutate(inc.id)}>{t('resolve')}</Button>
                  </div>
                </CardAction>
              )}
            </CardHeader>
            <CardContent>
              <div className="text-xs text-muted-foreground mb-1">
                {inc.createdBy} · {fmtAgo(inc.openedAt)} · {inc.impact}
              </div>
              {inc.summary && <p className="text-sm text-foreground/90 whitespace-pre-wrap">{inc.summary}</p>}
              {inc.ticketUrl && (
                <a href={inc.ticketUrl} target="_blank" rel="noreferrer"
                  className="text-xs text-primary hover:underline">Ticket ↗</a>
              )}
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  )
}
