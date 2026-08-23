// Objects explorer (SPEC §12.3): selector filter bar + full text,
// virtualised table (≥ 200 rows), object detail with effective config,
// metric charts with threshold bands, event timeline.
// CMP "Monitoring Admin" + "Wizard" parity: create / edit / delete hosts
// and services (incl. check-interval management) and the batch importer.
import { useEffect, useMemo, useRef, useState } from 'react'
import { keepPreviousData, useQuery, useMutation } from '@tanstack/react-query'
import { Link, useParams, useNavigate, useSearch } from '@tanstack/react-router'
import { useVirtualizer } from '@tanstack/react-virtual'
import { X, RefreshCw, Tag, Search } from 'lucide-react'
import { get, post, del, fmtTime, fmtAgo, queryClient, APIError, type ListResponse } from '../api'
import type { NPObject, NPEvent, SeriesResult, ObjectSpec, ObjectsSearch } from '../types'
import { stateLabel, stateIcon, stateColor, eventBadge } from '../types'
import { useRefreshInterval } from '../settings'
import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { Empty, LabelChips, ErrorState, DuplicateButton } from '@/components/kit'
import { redactSecrets } from '@/lib/redact'
import { parsePerfdata } from '@/lib/perfdata'
import { humanizeOutput } from '@/lib/humanize'
import { Chart } from '../components/Chart'
import { groupByUnit, thresholdTone, fmtMetric, rateifyCounters } from '../components/dash/series'
import { DowntimeDialog } from '../components/AckDialog'
import { ObjectFormDialog, BatchAddDialog } from '../components/objects/ObjectForm'
import { t } from '../i18n'

// Radix SelectItem value cannot be "" — sentinel for the empty/all option,
// mapped back to undefined in the URL filter handlers.
const ALL = '__all__'

// State filter options (URL token → label). Tokens match
// stateLabel(kind, n).toLowerCase() so one filter serves both kinds.
const STATE_FILTERS: [string, string][] = [
  ['', t('allStatuses')],
  ['problem', t('problems')],
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
  const [copy, setCopy] = useState<NPObject | null>(null)
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

  const refresh = useRefreshInterval()
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['objects', selector, query],
    queryFn: () => get<ListResponse<NPObject>>(
      `/objects?selector=${encodeURIComponent(selector)}&q=${encodeURIComponent(query)}&limit=2000`),
    placeholderData: keepPreviousData, // navigation shows the last list instantly
    refetchInterval: refresh,
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
  // Names a duplicate must not collide with: hosts are unique per tenant,
  // services per host — so the suggested "-copy" name is free from the start.
  const siblingNames = (o: NPObject) => all
    .filter((x) => x.kind === o.kind && (o.kind === 'host' || x.hostId === o.hostId))
    .map((x) => x.name)
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
        {/* Two distinct search scopes, disambiguated by icon + tooltip (LIST-2):
            a label selector vs. free-text over name/output. */}
        <div className="relative max-w-xs w-full">
          <Tag size={13} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none" />
          <Input placeholder={t('filter')} title={t('labelSelectorTitle')}
            value={selector} onChange={(e) => setSelector(e.target.value)} className="pl-8" />
        </div>
        <div className="relative max-w-xs w-full">
          <Search size={13} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none" />
          <Input placeholder={t('fulltextPlaceholder')} title={t('fulltextSearchTitle')}
            value={query} onChange={(e) => setQuery(e.target.value)} className="pl-8" />
        </div>
        <Select value={kindFilter || ALL} onValueChange={(v) => patchSearch({ kind: (v === ALL ? undefined : v) as ObjectsSearch['kind'] })}>
          <SelectTrigger className="w-36"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('hostsAndServices')}</SelectItem>
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
            <X size={14} /> {t('resetFilter')}
          </Button>
        )}
      </div>
      {isLoading && <Empty text={t('loading')} />}
      {/* Column headers so the flat list reads as a table (LIST-1). */}
      <div className="flex items-center gap-3 px-3 py-1.5 text-[11px] font-medium text-muted-foreground uppercase tracking-wider border border-b-0 border-border rounded-t-xl bg-muted/30">
        <span className="w-24 shrink-0">{t('state')}</span>
        <span className="w-16 shrink-0">{t('type')}</span>
        <span className="w-28 shrink-0">{t('folder')}</span>
        <span className="w-56 shrink-0">{t('name')}</span>
        <span className="flex-1">{t('output')}</span>
      </div>
      <div ref={parentRef} className="flex-1 overflow-auto border border-border rounded-b-xl bg-card/40 min-h-[420px]">
        <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
          {virtualizer.getVirtualItems().map((vi) => {
            const o = rows[vi.index]
            // The virtualizer's count is bound to rows.length, so the index is
            // always in range — but noUncheckedIndexedAccess still widens the
            // lookup to NPObject | undefined; guard rather than assert.
            if (!o) return null
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
                  {/* aria-hidden: keep the row link's accessible name the
                      object identity (kind + name), not the folder path. */}
                  <span aria-hidden className="w-28 shrink-0 text-xs text-muted-foreground truncate font-mono" title={o.folder}>{o.folder}</span>
                  <span className="text-foreground font-medium truncate w-56 shrink-0">
                    {o.kind === 'service' && o.hostName ? `${o.hostName} / ` : ''}{o.name}
                  </span>
                  <span className="text-muted-foreground text-xs truncate flex-1">{humanizeOutput(o.state?.output)}</span>
                  <LabelChips labels={o.labels} />
                </Link>
                <div className="shrink-0 flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                  <Button size="sm" variant="ghost" onClick={() => recheck.mutate(o.id)} title={t('checkNow')} aria-label={t('checkNow')}><RefreshCw size={13} /></Button>
                  <Button size="sm" variant="ghost" onClick={() => setEdit(o)}>{t('edit')}</Button>
                  <DuplicateButton onClick={() => setCopy(o)} />
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
      {copy && (
        <ObjectFormDialog open kind={copy.kind} copyFrom={copy} existingNames={siblingNames(copy)}
          onClose={() => setCopy(null)} />
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
  const [copyOpen, setCopyOpen] = useState(false)

  const { data: obj, isLoading: objLoading, isError: objIsError, error: objError, refetch: refetchObj } = useQuery({
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
  // A host's own services (kind=service, hostId=this host) — shown as a card
  // below so the detail view of a host lists what runs on it. Enabled only for
  // hosts; services have no sub-services.
  const isHost = obj?.kind === 'host'
  const refresh = useRefreshInterval()
  const { data: svcData } = useQuery({
    queryKey: ['objects', id, 'services'],
    queryFn: () => get<ListResponse<NPObject>>(`/services?hostId=${id}&limit=1000`),
    enabled: isHost,
    refetchInterval: refresh,
  })
  const services = useMemo(() => svcData?.items ?? [], [svcData])
  // A service's sibling services on the same host (DETAIL-4) — lets you jump
  // across a host's services without going via the host page.
  const { data: sibData } = useQuery({
    queryKey: ['objects', obj?.hostId, 'siblings'],
    queryFn: () => get<ListResponse<NPObject>>(`/services?hostId=${obj!.hostId}&limit=1000`),
    enabled: obj?.kind === 'service' && !!obj?.hostId,
    refetchInterval: refresh,
  })
  const siblings = useMemo(() => (sibData?.items ?? []).filter((sv) => sv.id !== id), [sibData, id])
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
  // Overlay same-unit metrics in one chart (grouped so unlike units don't share
  // a y-axis); memoised so the Chart effect stays stable across refetches.
  const metricGroups = useMemo(
    () => groupByUnit(rateifyCounters((series ?? []).filter((s) => !s.series.metric.startsWith('np_')))),
    [series],
  )
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

  // A missing/deleted object (bad or stale id) must not spin forever (NP-03):
  // a 404 gets a real not-found state with a way back; other failures reuse the
  // shared error UI with retry.
  if (objIsError) {
    const notFound = objError instanceof APIError && objError.status === 404
    if (!notFound) {
      return <div className="p-8"><ErrorState error={objError} onRetry={() => refetchObj()} /></div>
    }
    return (
      <div className="flex flex-col items-center justify-center gap-3 p-16 text-center">
        <div className="text-4xl font-bold text-muted-foreground">404</div>
        <div className="text-sm text-muted-foreground max-w-sm">{t('objectNotFound')}</div>
        <Button variant="outline" onClick={() => navigate({ to: '/objects' })}>{t('backToObjects')}</Button>
      </div>
    )
  }
  if (objLoading || !obj) return <Empty text={t('loading')} />
  const cs = obj.state
  // Prefer the resolved effective config for the interval card (so template
  // inheritance is visible); fall back to the object's own spec.
  const eff = (effective?.spec ?? obj.spec) as ObjectSpec
  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="text-xs text-muted-foreground flex flex-wrap items-center gap-x-1">
            <Link to="/objects" className="hover:text-foreground/90">{t('objects')}</Link>
            {obj.folder !== '/' && (
              <>{' / '}<Link to="/objects" search={{ q: obj.folder }} className="hover:text-foreground/90">{obj.folder}</Link></>
            )}
            {obj.kind === 'service' && obj.hostName && (
              <>{' / '}{obj.hostId
                ? <Link to="/objects/$id" params={{ id: obj.hostId }} className="hover:text-foreground/90 font-medium">{obj.hostName}</Link>
                : <span>{obj.hostName}</span>}</>
            )}
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
          <div className="mt-1 flex items-center gap-2 flex-wrap">
            <LabelChips labels={obj.labels} />
            {obj.kind === 'service' && obj.hostName && obj.hostId && (
              <Link to="/objects/$id" params={{ id: obj.hostId }}
                className="text-[11px] bg-muted text-muted-foreground rounded px-1.5 py-0.5 hover:text-foreground">
                Host: <span className="text-foreground/90 font-medium">{obj.hostName}</span>
              </Link>
            )}
          </div>
        </div>
        <div className="flex gap-2 shrink-0 items-center">
          <Button variant="default" onClick={() => setEditOpen(true)}>{t('edit')}</Button>
          <DuplicateButton size="md" variant="outline" label onClick={() => setCopyOpen(true)} />
          <Button variant="outline" onClick={() => recheck.mutate()} disabled={recheck.isPending}><RefreshCw size={14} /> {t('checkNow')}</Button>
          <Button variant="outline" onClick={() => setDtOpen(true)}>{t('downtime')}</Button>
          <ObjectDeleteButton kind={obj.kind} size="md" onDelete={() => remove.mutate()} />
        </div>
      </div>

      {/* Lead with State + Metrics; raw config moves into its own tab (DETAIL-2). */}
      <Tabs defaultValue="overview">
        <TabsList variant="line">
          <TabsTrigger value="overview">{t('overview')}</TabsTrigger>
          <TabsTrigger value="history">{t('history')}</TabsTrigger>
          <TabsTrigger value="config">{t('configuration')}</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="mt-4 space-y-4">
          <div className="grid lg:grid-cols-2 gap-4 items-start">
            <Card>
              <CardHeader><CardTitle>{t('state')}</CardTitle></CardHeader>
              <CardContent>
                <dl className="text-sm space-y-1.5">
                  <Row k={t('output')} v={humanizeOutput(cs?.output)} mono />
                  {cs?.longOutput && <Row k="Long" v={cs.longOutput} mono />}
                  <Row k="Last check" v={cs?.lastCheck ? `${fmtTime(cs.lastCheck)} (${fmtAgo(cs.lastCheck)} ago)` : '—'} />
                  <Row k="Next check" v={fmtTime(cs?.nextCheck)} />
                  <Row k="Last hard change" v={fmtTime(cs?.lastHardChange)} />
                  {cs?.ackedBy && <Row k={t('acked')} v={`${cs.ackedBy} — ${cs.ackComment ?? ''}`} />}
                  {(cs?.downtimeDepth ?? 0) > 0 && <Row k={t('downtime')} v={t('active')} />}
                  {cs?.flapping && <Row k="Flapping" v={t('yes')} />}
                </dl>
                {cs?.perfdata && (
                  <div className="mt-3 pt-3 border-t border-border/50">
                    <div className="text-[11px] text-muted-foreground uppercase tracking-wider mb-2">Perfdata</div>
                    <PerfMeters perfdata={cs.perfdata} />
                  </div>
                )}
              </CardContent>
            </Card>
            <Card>
              <CardHeader><CardTitle>{`${t('interval')} & Scheduling`}</CardTitle></CardHeader>
              <CardContent>
                <div className="grid grid-cols-2 sm:grid-cols-3 gap-x-4 gap-y-2 text-sm">
                  <Cfg k={t('interval')} v={eff.interval} />
                  <Cfg k={t('retryInterval')} v={eff.retryInterval} />
                  <Cfg k={t('maxAttempts')} v={eff.maxCheckAttempts != null ? String(eff.maxCheckAttempts) : undefined} />
                  <Cfg k={t('timeout')} v={eff.timeout} />
                  <Cfg k={t('checkPeriod')} v={eff.checkPeriod} />
                  <Cfg k={t('checkCommand')} v={eff.checkCommand} mono />
                </div>
              </CardContent>
            </Card>
          </div>

          {metricGroups.length > 0 && (
            <Card>
              <CardHeader><CardTitle>{t('metrics')}</CardTitle></CardHeader>
              <CardContent>
                <div className="grid lg:grid-cols-2 gap-6">
                  {metricGroups.map((g, i) => (
                    <Chart key={i} results={g} />
                  ))}
                </div>
              </CardContent>
            </Card>
          )}

          {isHost && <RelatedServicesCard title={t('services')} services={services} />}
          {obj.kind === 'service' && siblings.length > 0 && (
            <RelatedServicesCard title={t('otherHostServices')} services={siblings} />
          )}
        </TabsContent>

        <TabsContent value="history" className="mt-4">
          <Card>
            <CardHeader><CardTitle>{t('history')}</CardTitle></CardHeader>
            <CardContent><HistoryList events={events?.items ?? []} /></CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="config" className="mt-4">
          <Card>
            <CardHeader><CardTitle>{t('effectiveConfig')}</CardTitle></CardHeader>
            <CardContent>
              <EffectiveConfig spec={(effective?.spec ?? obj.spec) as Record<string, unknown>} templateChain={effective?.templateChain} />
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      <DowntimeDialog open={dtOpen} objectId={obj.id} objectName={obj.name} onClose={() => setDtOpen(false)} />
      {editOpen && <ObjectFormDialog open kind={obj.kind} edit={obj} onClose={() => setEditOpen(false)} />}
      {copyOpen && (
        <ObjectFormDialog open kind={obj.kind} copyFrom={obj}
          existingNames={obj.kind === 'service' ? [obj.name, ...siblings.map((s) => s.name)] : undefined}
          onClose={() => setCopyOpen(false)} />
      )}
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

// PerfMeters: render a check's perfdata string as small labelled bars with
// warn/crit markers instead of a raw "load1=1.09;3;6;;" blob (DETAIL-5).
function PerfMeters({ perfdata }: { perfdata: string }) {
  const perfs = useMemo(() => parsePerfdata(perfdata), [perfdata])
  if (perfs.length === 0) {
    return <div className="text-xs text-muted-foreground font-mono break-all">{perfdata}</div>
  }
  return (
    <div className="space-y-2">
      {perfs.map((p) => {
        const min = p.min ?? 0
        // A bar only means something against a real scale: an explicit max or a
        // warn/crit threshold. An unbounded counter (e.g. SNMP sysUpTime) has
        // neither — drawing it would fill the bar to 100% of nothing, so we show
        // just the value instead (NP-09).
        const hasScale = p.max !== null || p.warn !== null || p.crit !== null
        const max = p.max ?? (Math.max(p.value, p.crit ?? 0, p.warn ?? 0) || 1)
        const span = (max - min) || 1
        const at = (n: number) => ((n - min) / span) * 100
        const tone = thresholdTone(p.value, p.warn, p.crit)
        return (
          <div key={p.label} className="text-xs">
            <div className="flex items-baseline justify-between mb-0.5 gap-2">
              <span className="text-muted-foreground font-mono truncate">{p.label}</span>
              <span className="text-foreground tabular-nums font-semibold shrink-0">{fmtMetric(p.value)}{p.unit}</span>
            </div>
            {hasScale && (
              <div className="relative h-2 bg-slate-800/80 rounded overflow-hidden">
                <div className="absolute inset-y-0 left-0 rounded" style={{ width: `${Math.max(1.5, Math.min(100, at(p.value)))}%`, background: tone }} />
                {p.warn !== null && p.warn >= min && p.warn <= max && (
                  <div className="absolute inset-y-0 w-px bg-amber-400/70" style={{ left: `${at(p.warn)}%` }} />
                )}
                {p.crit !== null && p.crit >= min && p.crit <= max && (
                  <div className="absolute inset-y-0 w-px bg-red-400/80" style={{ left: `${at(p.crit)}%` }} />
                )}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

// RelatedServicesCard: a host's own services, or a service's siblings on the
// same host (DETAIL-4). Rows drill into the object detail.
function RelatedServicesCard({ title, services }: { title: string; services: NPObject[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          {title} <span className="text-muted-foreground text-sm font-normal">({services.length})</span>
        </CardTitle>
      </CardHeader>
      <CardContent>
        {services.length === 0 ? (
          <Empty text={t('noServices')} />
        ) : (
          <div className="divide-y divide-border/50">
            {services.map((s) => (
              <Link
                key={s.id} to="/objects/$id" params={{ id: s.id }}
                className="flex items-center gap-3 py-1.5 px-2 -mx-2 rounded-md hover:bg-muted/40 text-sm group"
              >
                <span className={`w-28 shrink-0 font-semibold ${s.state ? stateColor(s.kind, s.state.state) : 'text-muted-foreground'}`}>
                  {s.state?.lastCheck
                    ? `${stateIcon(s.kind, s.state.state)} ${stateLabel(s.kind, s.state.state)}`
                    : `○ ${t('pending')}`}
                </span>
                <span className="font-medium truncate w-56 shrink-0 text-foreground">{s.name}</span>
                <span className="text-muted-foreground text-xs truncate flex-1">{humanizeOutput(s.state?.output)}</span>
                <LabelChips labels={s.labels} />
              </Link>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  )
}

// HistoryList: event timeline with severity-coloured badges so CRITICAL vs OK
// transitions read at a glance (DETAIL-5).
function HistoryList({ events }: { events: NPEvent[] }) {
  if (events.length === 0) return <Empty text={t('empty')} />
  return (
    <div className="space-y-0.5 text-sm">
      {events.map((e) => (
        <div key={e.id} className="flex items-center gap-3 py-1.5 px-2 -mx-2 rounded hover:bg-muted/30 border-b border-border/40 last:border-0">
          <span className="text-muted-foreground/70 text-xs tabular-nums w-36 shrink-0">{fmtTime(e.ts)}</span>
          <Badge variant="outline" className="bg-muted text-muted-foreground border-input shrink-0">{e.type}</Badge>
          {(() => { const b = eventBadge(e); return b && <Badge variant="outline" className={`${b.className} shrink-0`}>{b.label}</Badge> })()}
          <span className="text-muted-foreground text-xs truncate">
            {humanizeOutput(String((e.payload as Record<string, unknown>).output ??
              (e.payload as Record<string, unknown>).summary ?? JSON.stringify(e.payload)))}
          </span>
        </div>
      ))}
    </div>
  )
}

// EffectiveConfig: the resolved spec as a scannable key/value table (secrets
// redacted, DETAIL-1) with the template chain and a Raw JSON toggle (DETAIL-3).
// Per-field origin (own vs which template) isn't exposed by the API, so the
// chain is surfaced as a whole rather than annotated per row.
function EffectiveConfig({ spec, templateChain }: {
  spec: Record<string, unknown>; templateChain?: string[] | null
}) {
  const [raw, setRaw] = useState(false)
  const red = useMemo(() => redactSecrets(spec) as Record<string, unknown>, [spec])
  const entries = Object.entries(red).filter(([, v]) =>
    v !== undefined && v !== null && v !== ''
    && !(Array.isArray(v) && v.length === 0)
    && !(typeof v === 'object' && !Array.isArray(v) && Object.keys(v as object).length === 0),
  )
  const fmtVal = (v: unknown): string => {
    if (Array.isArray(v)) return v.map((x) => (typeof x === 'object' ? JSON.stringify(x) : String(x))).join(', ')
    if (v !== null && typeof v === 'object') return JSON.stringify(v)
    return String(v)
  }
  return (
    <div className="space-y-3">
      <div className="flex items-start justify-between gap-3">
        {templateChain?.length ? (
          <div className="text-xs text-muted-foreground min-w-0">
            {t('templateChain')}: <span className="text-foreground/80 font-mono break-words">{templateChain.join(' → ')}</span>
          </div>
        ) : <span />}
        <button type="button" onClick={() => setRaw((r) => !r)}
          className="text-xs text-muted-foreground hover:text-foreground shrink-0">
          {raw ? t('table') : 'Raw JSON'}
        </button>
      </div>
      {raw ? (
        <pre className="text-xs text-muted-foreground overflow-auto max-h-72 font-mono">
          {JSON.stringify(red, null, 2)}
        </pre>
      ) : entries.length === 0 ? (
        <Empty text={t('empty')} />
      ) : (
        <dl className="text-sm">
          {entries.map(([k, v]) => (
            <div key={k} className="flex gap-3 py-1 border-b border-border/40 last:border-0">
              <dt className="text-muted-foreground w-44 shrink-0 font-mono text-xs pt-0.5">{k}</dt>
              <dd className="text-foreground/90 min-w-0 break-words font-mono text-xs">{fmtVal(v)}</dd>
            </div>
          ))}
        </dl>
      )}
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
        <span className="text-[11px] text-amber-400">{t('deleteHostCascade')}</span>
      )}
      <Button size={btnSize} variant="destructive" onClick={() => { setArm(false); onDelete() }}>{t('deleteConfirm')}</Button>
      <Button size={btnSize} variant="ghost" onClick={() => setArm(false)}>{t('cancel')}</Button>
    </span>
  )
}
