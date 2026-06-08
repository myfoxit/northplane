// Objects explorer (SPEC §12.3): selector filter bar + full text,
// virtualised table (≥ 200 rows), object detail with effective config,
// metric charts with threshold bands, event timeline.
// CMP "Monitoring Admin" + "Wizard" parity: create / edit / delete hosts
// and services (incl. check-interval management) and the batch importer.
import { useEffect, useMemo, useRef, useState } from 'react'
import { keepPreviousData, useQuery, useMutation } from '@tanstack/react-query'
import { Link, useParams, useNavigate, useSearch } from '@tanstack/react-router'
import { useVirtualizer } from '@tanstack/react-virtual'
import { X, RefreshCw } from 'lucide-react'
import { get, post, del, fmtTime, fmtAgo, queryClient, type ListResponse } from '../api'
import type { NPObject, NPEvent, SeriesResult, ObjectSpec, ObjectsSearch } from '../types'
import { stateLabel, stateIcon, stateColor } from '../types'
import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from '@/components/ui/select'
import { Empty, LabelChips, ErrorState } from '@/components/kit'
import { Chart } from '../components/Chart'
import { DowntimeDialog } from '../components/AckDialog'
import { ObjectFormDialog, BatchAddDialog } from '../components/objects/ObjectForm'
import { t } from '../i18n'

// Radix SelectItem value cannot be "" — sentinel for the empty/all option,
// mapped back to undefined in the URL filter handlers.
const ALL = '__all__'

// State filter options (URL token → label). Tokens match
// stateLabel(kind, n).toLowerCase() so one filter serves both kinds.
const STATE_FILTERS: [string, string][] = [
  ['', 'Alle Status'],
  ['problem', 'Probleme'],
  ['ok', 'OK'],
  ['up', 'Up'],
  ['warning', 'Warning'],
  ['critical', 'Critical'],
  ['unknown', 'Unknown'],
  ['down', 'Down'],
  ['unreachable', 'Unreachable'],
  ['pending', 'Pending'],
]

export function ObjectsPage() {
  // Filters live in the URL: dashboard drill-downs link here and the
  // back button restores the previous view.
  const search = useSearch({ strict: false }) as ObjectsSearch
  const navigate = useNavigate()
  const [selector, setSelector] = useState(search.selector ?? '')
  const [query, setQuery] = useState(search.q ?? '')
  const stateFilter = search.state ?? ''
  const kindFilter = search.kind ?? ''
  const parentRef = useRef<HTMLDivElement>(null)
  const [create, setCreate] = useState<'host' | 'service' | null>(null)
  const [edit, setEdit] = useState<NPObject | null>(null)
  const [batch, setBatch] = useState(false)

  const patchSearch = (patch: Partial<ObjectsSearch>) =>
    navigate({
      to: '/objects',
      search: (prev) => ({ ...prev, ...patch }),
      replace: true,
    })

  // Debounced URL sync for the text inputs (typing stays local-state fast).
  useEffect(() => {
    const id = window.setTimeout(() => {
      if ((search.selector ?? '') !== selector || (search.q ?? '') !== query) {
        patchSearch({ selector: selector || undefined, q: query || undefined })
      }
    }, 250)
    return () => window.clearTimeout(id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selector, query])

  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['objects', selector, query],
    queryFn: () => get<ListResponse<NPObject>>(
      `/objects?selector=${encodeURIComponent(selector)}&q=${encodeURIComponent(query)}&limit=2000`),
    placeholderData: keepPreviousData, // navigation shows the last list instantly
  })
  const all = useMemo(() => data?.items ?? [], [data])
  // State/kind filtering happens client-side over the loaded window —
  // instant, no extra round-trip.
  const rows = useMemo(() => all.filter((o) => {
    if (kindFilter && o.kind !== kindFilter) return false
    if (!stateFilter) return true
    if (stateFilter === 'pending') return !o.state?.lastCheck
    if (!o.state?.lastCheck) return false
    if (stateFilter === 'problem') return o.state.state !== 0
    return stateLabel(o.kind, o.state.state).toLowerCase() === stateFilter
  }), [all, kindFilter, stateFilter])
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 44,
    overscan: 20,
  })

  const remove = useMutation({
    mutationFn: (id: string) => del(`/objects/${id}`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['objects'] }),
  })
  const recheck = useMutation({
    mutationFn: (id: string) => post(`/objects/${id}/check-now`),
  })

  if (isError && !data) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }

  return (
    <div className="space-y-3 h-full flex flex-col">
      <div className="flex items-center justify-between gap-2">
        <h1 className="text-lg font-bold">{t('objects')} <span className="text-muted-foreground text-sm">({rows.length})</span></h1>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => setCreate('host')}>+ {t('newHost')}</Button>
          <Button variant="outline" onClick={() => setCreate('service')}>+ {t('newService')}</Button>
          <Button variant="ghost" onClick={() => setBatch(true)}>{t('batchAdd')}</Button>
        </div>
      </div>
      <div className="flex gap-2 flex-wrap">
        <Input placeholder={t('filter')} value={selector} onChange={(e) => setSelector(e.target.value)} className="max-w-xs" />
        <Input placeholder="Volltext…" value={query} onChange={(e) => setQuery(e.target.value)} className="max-w-xs" />
        <Select value={kindFilter || ALL} onValueChange={(v) => patchSearch({ kind: (v === ALL ? undefined : v) as ObjectsSearch['kind'] })}>
          <SelectTrigger className="w-36"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>Hosts + Services</SelectItem>
            <SelectItem value="host">Hosts</SelectItem>
            <SelectItem value="service">Services</SelectItem>
          </SelectContent>
        </Select>
        <Select value={stateFilter || ALL} onValueChange={(v) => patchSearch({ state: v === ALL ? undefined : v })}>
          <SelectTrigger className="w-36"><SelectValue /></SelectTrigger>
          <SelectContent>
            {STATE_FILTERS.map(([v, label]) => <SelectItem key={v || ALL} value={v || ALL}>{label}</SelectItem>)}
          </SelectContent>
        </Select>
        {(stateFilter || kindFilter) && (
          <Button variant="ghost" onClick={() => patchSearch({ state: undefined, kind: undefined })}>
            <X size={14} /> Filter zurücksetzen
          </Button>
        )}
      </div>
      {isLoading && <Empty text={t('loading')} />}
      <div ref={parentRef} className="flex-1 overflow-auto border border-border rounded-xl bg-card/40 min-h-[420px]">
        <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
          {virtualizer.getVirtualItems().map((vi) => {
            const o = rows[vi.index]
            return (
              <div
                key={o.id}
                className="absolute left-0 right-0 flex items-center gap-3 px-3 border-b border-border/50 hover:bg-muted/40 text-sm group"
                style={{ top: vi.start, height: vi.size }}
              >
                <Link to="/objects/$id" params={{ id: o.id }} className="flex items-center gap-3 flex-1 min-w-0 h-full">
                  <span className={`w-24 shrink-0 font-semibold ${o.state ? stateColor(o.kind, o.state.state) : 'text-muted-foreground'}`}>
                    {o.state?.lastCheck
                      ? `${stateIcon(o.kind, o.state.state)} ${stateLabel(o.kind, o.state.state)}`
                      : `○ ${t('pending')}`}
                  </span>
                  <span className="w-16 shrink-0 text-xs text-muted-foreground uppercase">{o.kind}</span>
                  <span className="text-foreground font-medium truncate w-64 shrink-0">
                    {o.kind === 'service' && o.hostName ? `${o.hostName} / ` : ''}{o.name}
                  </span>
                  <span className="text-muted-foreground text-xs truncate flex-1">{o.state?.output}</span>
                  <LabelChips labels={o.labels} />
                </Link>
                <div className="shrink-0 flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                  <Button size="sm" variant="ghost" onClick={() => recheck.mutate(o.id)} title={t('checkNow')} aria-label={t('checkNow')}><RefreshCw size={13} /></Button>
                  <Button size="sm" variant="ghost" onClick={() => setEdit(o)}>{t('edit')}</Button>
                  <ObjectDeleteButton kind={o.kind} onDelete={() => remove.mutate(o.id)} />
                </div>
              </div>
            )
          })}
        </div>
        {!isLoading && rows.length === 0 && <Empty text={t('empty')} />}
      </div>

      {create && (
        <ObjectFormDialog open kind={create} onClose={() => setCreate(null)} />
      )}
      {edit && (
        <ObjectFormDialog open kind={edit.kind} edit={edit} onClose={() => setEdit(null)} />
      )}
      <BatchAddDialog open={batch} onClose={() => setBatch(false)} />
    </div>
  )
}

export function ObjectDetailPage() {
  const { id } = useParams({ strict: false }) as { id: string }
  const navigate = useNavigate()
  const [dtOpen, setDtOpen] = useState(false)
  const [editOpen, setEditOpen] = useState(false)

  const { data: obj } = useQuery({
    queryKey: ['objects', id],
    queryFn: () => get<NPObject>(`/objects/${id}`),
  })
  const { data: effective } = useQuery({
    queryKey: ['objects', id, 'effective'],
    queryFn: () => get<{ spec: Record<string, unknown>; templateChain: string[] | null }>(
      `/objects/${id}/effective-config`),
  })
  const { data: events } = useQuery({
    queryKey: ['events', id],
    queryFn: () => get<{ items: NPEvent[] | null }>(`/events?objectId=${id}&limit=30`),
  })
  const { data: series } = useQuery({
    queryKey: ['metrics', id],
    queryFn: () => post<SeriesResult[]>('/metrics/query', {
      objectId: id,
      from: new Date(Date.now() - 24 * 3600_000).toISOString(),
      to: new Date().toISOString(),
      maxPoints: 300,
    }),
    refetchInterval: 60_000,
  })
  const recheck = useMutation({
    mutationFn: () => post(`/objects/${id}/check-now`),
    onSuccess: () => setTimeout(() => queryClient.invalidateQueries({ queryKey: ['objects', id] }), 2500),
  })
  const remove = useMutation({
    mutationFn: () => del(`/objects/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['objects'] })
      navigate({ to: '/objects' })
    },
  })

  if (!obj) return <Empty text={t('loading')} />
  const cs = obj.state
  // Prefer the resolved effective config for the interval card (so template
  // inheritance is visible); fall back to the object's own spec.
  const eff = (effective?.spec ?? obj.spec) as ObjectSpec
  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4">
        <div>
          <div className="text-xs text-muted-foreground">
            <Link to="/objects" className="hover:text-foreground/90">{t('objects')}</Link>
            {' / '}{obj.folder !== '/' ? `${obj.folder} / ` : ''}
            {obj.kind === 'service' && obj.hostName ? `${obj.hostName} / ` : ''}
          </div>
          <h1 className="text-xl font-bold flex items-center gap-3">
            {obj.name}
            {cs && (
              <span className={`text-base font-bold ${stateColor(obj.kind, cs.state)}`}>
                {stateIcon(obj.kind, cs.state)} {stateLabel(obj.kind, cs.state)}
                <span className="text-muted-foreground font-normal text-xs ml-1">
                  ({cs.stateType} {cs.attempt}x)
                </span>
              </span>
            )}
          </h1>
          <div className="mt-1"><LabelChips labels={obj.labels} /></div>
        </div>
        <div className="flex gap-2 shrink-0 items-center">
          <Button variant="default" onClick={() => setEditOpen(true)}>{t('edit')}</Button>
          <Button variant="outline" onClick={() => recheck.mutate()} disabled={recheck.isPending}><RefreshCw size={14} /> {t('checkNow')}</Button>
          <Button variant="outline" onClick={() => setDtOpen(true)}>{t('downtime')}</Button>
          <ObjectDeleteButton kind={obj.kind} size="md" onDelete={() => remove.mutate()} />
        </div>
      </div>

      {/* Interval / scheduling card (CMP Admin parity — check-interval
          management must be visible at a glance). */}
      <Card>
        <CardHeader><CardTitle>{`${t('interval')} & Scheduling`}</CardTitle></CardHeader>
        <CardContent>
          <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-x-4 gap-y-2 text-sm">
            <Cfg k={t('interval')} v={eff.interval} />
            <Cfg k={t('retryInterval')} v={eff.retryInterval} />
            <Cfg k={t('maxAttempts')} v={eff.maxCheckAttempts != null ? String(eff.maxCheckAttempts) : undefined} />
            <Cfg k={t('timeout')} v={eff.timeout} />
            <Cfg k={t('checkPeriod')} v={eff.checkPeriod} />
            <Cfg k={t('checkCommand')} v={eff.checkCommand} mono />
          </div>
        </CardContent>
      </Card>

      <div className="grid lg:grid-cols-2 gap-4">
        <Card>
          <CardHeader><CardTitle>{t('state')}</CardTitle></CardHeader>
          <CardContent>
            <dl className="text-sm space-y-1.5">
              <Row k={t('output')} v={cs?.output} mono />
              {cs?.longOutput && <Row k="Long" v={cs.longOutput} mono />}
              {cs?.perfdata && <Row k="Perfdata" v={cs.perfdata} mono />}
              <Row k="Last check" v={cs?.lastCheck ? `${fmtTime(cs.lastCheck)} (${fmtAgo(cs.lastCheck)} ago)` : '—'} />
              <Row k="Next check" v={fmtTime(cs?.nextCheck)} />
              <Row k="Last hard change" v={fmtTime(cs?.lastHardChange)} />
              {cs?.ackedBy && <Row k={t('acked')} v={`${cs.ackedBy} — ${cs.ackComment ?? ''}`} />}
              {(cs?.downtimeDepth ?? 0) > 0 && <Row k={t('downtime')} v="aktiv" />}
              {cs?.flapping && <Row k="Flapping" v="ja" />}
            </dl>
          </CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle>{`${t('effectiveConfig')}${effective?.templateChain?.length ? ` — ${t('templateChain')}: ${effective.templateChain.join(' → ')}` : ''}`}</CardTitle></CardHeader>
          <CardContent>
            <pre className="text-xs text-muted-foreground overflow-auto max-h-64 font-mono">
              {JSON.stringify(effective?.spec ?? obj.spec, null, 2)}
            </pre>
          </CardContent>
        </Card>
      </div>

      {(series?.length ?? 0) > 0 && (
        <Card>
          <CardHeader><CardTitle>{t('metrics')}</CardTitle></CardHeader>
          <CardContent>
            <div className="grid lg:grid-cols-2 gap-6">
              {series!.filter((s) => !s.series.metric.startsWith('np_')).map((s) => (
                <Chart key={s.series.id} result={s} />
              ))}
            </div>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader><CardTitle>{t('history')}</CardTitle></CardHeader>
        <CardContent>
          {(events?.items?.length ?? 0) === 0
            ? <Empty text={t('empty')} />
            : (
              <div className="space-y-1 text-sm">
                {events!.items!.map((e) => (
                  <div key={e.id} className="flex gap-3 py-1 border-b border-border/50 last:border-0">
                    <span className="text-muted-foreground/70 text-xs tabular-nums w-36 shrink-0">{fmtTime(e.ts)}</span>
                    <Badge variant="outline" className="bg-muted text-muted-foreground border-input">{e.type}</Badge>
                    <span className="text-muted-foreground text-xs truncate">
                      {String((e.payload as Record<string, unknown>).output ??
                        (e.payload as Record<string, unknown>).summary ?? JSON.stringify(e.payload))}
                    </span>
                  </div>
                ))}
              </div>
            )}
        </CardContent>
      </Card>
      <DowntimeDialog open={dtOpen} objectId={obj.id} objectName={obj.name} onClose={() => setDtOpen(false)} />
      {editOpen && <ObjectFormDialog open kind={obj.kind} edit={obj} onClose={() => setEditOpen(false)} />}
    </div>
  )
}

function Row({ k, v, mono }: { k: string; v?: string; mono?: boolean }) {
  return (
    <div className="flex gap-2">
      <dt className="text-muted-foreground w-36 shrink-0">{k}</dt>
      <dd className={`text-foreground/90 min-w-0 break-words ${mono ? 'font-mono text-xs pt-0.5' : ''}`}>{v || '—'}</dd>
    </div>
  )
}

// Cfg: compact label/value cell for the interval card.
function Cfg({ k, v, mono }: { k: string; v?: string; mono?: boolean }) {
  return (
    <div>
      <div className="text-[11px] text-muted-foreground uppercase tracking-wider">{k}</div>
      <div className={`text-foreground ${mono ? 'font-mono text-xs' : 'tabular-nums'}`}>{v || '—'}</div>
    </div>
  )
}

// ObjectDeleteButton: inline-confirm delete that warns (German) when a host
// is removed, since deleting a host cascades to its services.
export function ObjectDeleteButton({ kind, onDelete, size = 'sm' }: {
  kind: 'host' | 'service'; onDelete: () => void; size?: 'sm' | 'md'
}) {
  const [arm, setArm] = useState(false)
  const btnSize = size === 'md' ? 'default' : 'sm'
  if (!arm) {
    return <Button size={btnSize} variant="ghost" onClick={() => setArm(true)}>{t('delete')}</Button>
  }
  return (
    <span className="inline-flex items-center gap-1">
      {kind === 'host' && (
        <span className="text-[11px] text-amber-400">Löscht auch alle Services des Hosts?</span>
      )}
      <Button size={btnSize} variant="destructive" onClick={() => { setArm(false); onDelete() }}>{t('deleteConfirm')}</Button>
      <Button size={btnSize} variant="ghost" onClick={() => setArm(false)}>{t('cancel')}</Button>
    </span>
  )
}
