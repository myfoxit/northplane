// Event log (SPEC §12.3 / F-05.01): searchable, filterable, NDJSON export.
// Server-side filters (types, severity, from, objectId) + cursor paging
// (EVENT-1); rows without a natural summary get a per-type one-liner and
// the raw payload stays behind the expander (EVENT-2).
import { useState } from 'react'
import { keepPreviousData, useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { get, fmtTime, type ListResponse } from '../api'
import type { NPEvent } from '../types'
import { eventBadge } from '../types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Empty, ErrorState } from '@/components/kit'
import { t } from '../i18n'
import { useRefreshInterval } from '../settings'

const eventTypes = ['', 'state_change', 'alert_opened', 'alert_resolved', 'notification',
  'escalation', 'ack', 'ingress', 'config', 'downtime', 'silence', 'heartbeat_missed',
  'flapping_start', 'flapping_end', 'ai_action']

const severities = ['', 'critical', 'warning', 'info', 'ok']

// Time windows for the from= filter; '' = everything.
const RANGES = [
  ['', 'allTime'],
  ['1', 'lastHour'],
  ['24', 'last24h'],
  ['168', 'last7d'],
  ['720', 'last30d'],
] as const

// summaryFor renders the one-line row text. Falls back through the
// payload's natural text keys, then composes one from the type's known
// shape — notification/escalation/ack/config rows carry no summary field.
function summaryFor(e: NPEvent): string {
  const p = (e.payload ?? {}) as Record<string, unknown>
  const natural = p.summary ?? p.output ?? p.object ?? p.title
  if (typeof natural === 'string' && natural) return natural
  const s = (v: unknown) => (typeof v === 'string' ? v : '')
  switch (e.type) {
    case 'notification': {
      const status = s(p.status) || 'sent'
      const err = s(p.error)
      return `${s(p.channel) || '?'} → ${s(p.contact) || '?'}: ${status}${err ? ` — ${err}` : ''}`
    }
    case 'escalation': {
      const contacts = Array.isArray(p.contacts) ? (p.contacts as unknown[]).join(', ') : ''
      return `${t('escalationStep')} ${String(p.step ?? '?')}${contacts ? ` → ${contacts}` : ''}`
    }
    case 'ack':
      return `${s(p.by) || '?'}${p.comment ? `: ${s(p.comment)}` : ''}`
    case 'config': {
      const kinds = Array.isArray(p.kinds) ? (p.kinds as unknown[]).join(', ') : ''
      return kinds ? `${t('configChangedKinds')}: ${kinds}` : t('configChangedKinds')
    }
    case 'downtime':
    case 'silence':
      return s(p.comment) || s(p.selector) || s(p.objectId)
    case 'heartbeat_missed':
      return s(p.heartbeat)
    default:
      return ''
  }
}

export function EventsPage() {
  const [type, setType] = useState('')
  const [severity, setSeverity] = useState('')
  const [hours, setHours] = useState<(typeof RANGES)[number][0]>('')
  const [objectRef, setObjectRef] = useState('')
  const refresh = useRefreshInterval()

  // Object suggestions: names typed into the filter resolve to ids —
  // the API filters by id only.
  const { data: objects } = useQuery({
    queryKey: ['objects', 'names'],
    queryFn: () => get<ListResponse<{ id: string; name: string }>>('/objects?limit=2000&withState=false'),
    staleTime: 60_000,
  })
  const objectId = objects?.items?.find((o) => o.name === objectRef)?.id ?? objectRef

  // from= is computed at fetch/click time, not during render — Date.now()
  // in render trips react-hooks/purity, and a per-fetch anchor also keeps
  // the window sliding across auto-refreshes.
  const buildParams = () => {
    const from = hours ? new Date(Date.now() - Number(hours) * 3_600_000).toISOString() : ''
    return `types=${type}&severity=${severity}&from=${encodeURIComponent(from)}` +
      `&objectId=${encodeURIComponent(objectId)}`
  }
  const { data, isLoading, isError, error, refetch, fetchNextPage, hasNextPage, isFetchingNextPage } =
    useInfiniteQuery({
      queryKey: ['events', type, severity, hours, objectId],
      queryFn: ({ pageParam }) => get<ListResponse<NPEvent>>(
        `/events?${buildParams()}&limit=200${pageParam ? `&cursor=${encodeURIComponent(pageParam)}` : ''}`),
      initialPageParam: '',
      getNextPageParam: (last) => last.nextCursor || undefined,
      placeholderData: keepPreviousData, // filter changes render instantly
      refetchInterval: refresh,
    })
  const rows = data?.pages.flatMap((p) => p.items ?? []) ?? []
  if (isError && !data) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-bold">{t('events')}</h1>
        <a href="/api/v1/events:export"
          onClick={(e) => { e.preventDefault(); window.location.href = `/api/v1/events:export?${buildParams()}` }}
          className="text-xs text-muted-foreground hover:text-foreground/90">⇩ NDJSON Export</a>
      </div>
      <div className="flex gap-2 flex-wrap">
        <Select value={type || '__all__'} onValueChange={(v) => setType(v === '__all__' ? '' : v)}>
          <SelectTrigger><SelectValue /></SelectTrigger>
          <SelectContent>
            {eventTypes.map((et) => <SelectItem key={et} value={et || '__all__'}>{et || t('allTypes')}</SelectItem>)}
          </SelectContent>
        </Select>
        <Select value={severity || '__all__'} onValueChange={(v) => setSeverity(v === '__all__' ? '' : v)}>
          <SelectTrigger className="w-40"><SelectValue /></SelectTrigger>
          <SelectContent>
            {severities.map((sv) => <SelectItem key={sv} value={sv || '__all__'}>{sv || t('allSeverities')}</SelectItem>)}
          </SelectContent>
        </Select>
        <Select value={hours || '__all__'} onValueChange={(v) => setHours((v === '__all__' ? '' : v) as typeof hours)}>
          <SelectTrigger className="w-44"><SelectValue /></SelectTrigger>
          <SelectContent>
            {RANGES.map(([v, label]) => <SelectItem key={v || '__all__'} value={v || '__all__'}>{t(label)}</SelectItem>)}
          </SelectContent>
        </Select>
        <Input placeholder={t('objectFilterPlaceholder')} value={objectRef}
          onChange={(e) => setObjectRef(e.target.value)} className="max-w-xs" list="events-objects" />
        <datalist id="events-objects">
          {objects?.items?.map((o) => <option key={o.id} value={o.name} />)}
        </datalist>
      </div>
      {isLoading && <Empty text={t('loading')} />}
      {!isLoading && rows.length === 0 && <Empty text={t('empty')} />}
      <div className="border border-border rounded-xl bg-card/40 divide-y divide-border/50 text-sm">
        {rows.map((e) => (
          <details key={e.id} className="group">
            <summary className="flex items-center gap-3 px-3 py-1.5 cursor-pointer hover:bg-muted/40 list-none">
              <span className="text-muted-foreground/70 text-xs tabular-nums w-36 shrink-0">{fmtTime(e.ts)}</span>
              <Badge variant="outline" className="bg-muted text-muted-foreground border-input w-32 justify-center shrink-0">{e.type}</Badge>
              {(() => { const b = eventBadge(e); return b && <Badge variant="outline" className={b.className}>{b.label}</Badge> })()}
              <span className="text-muted-foreground text-xs truncate flex-1">{summaryFor(e)}</span>
            </summary>
            <pre className="text-xs text-muted-foreground font-mono px-4 py-2 bg-background/60 overflow-auto max-h-48">
              {JSON.stringify(e.payload, null, 2)}
            </pre>
          </details>
        ))}
      </div>
      {hasNextPage && (
        <div className="flex justify-center">
          <Button variant="outline" size="sm" disabled={isFetchingNextPage} onClick={() => fetchNextPage()}>
            {isFetchingNextPage ? t('loading') : t('loadMore')}
          </Button>
        </div>
      )}
    </div>
  )
}
