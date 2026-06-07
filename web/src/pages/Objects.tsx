// Objects explorer (SPEC §12.3): selector filter bar + full text,
// virtualised table (≥ 200 rows), object detail with effective config,
// metric charts with threshold bands, event timeline.
// CMP "Monitoring Admin" + "Wizard" parity: create / edit / delete hosts
// and services (incl. check-interval management) and the batch importer.
import { useEffect, useMemo, useRef, useState } from 'react'
import { keepPreviousData, useQuery, useMutation } from '@tanstack/react-query'
import { Link, useParams, useNavigate, useSearch } from '@tanstack/react-router'
import { useVirtualizer } from '@tanstack/react-virtual'
import { get, post, del, fmtTime, fmtAgo, queryClient, type ListResponse } from '../api'
import type { NPObject, NPEvent, SeriesResult, ObjectSpec, ObjectsSearch } from '../types'
import { stateLabel, stateIcon, stateColor } from '../types'
import { Button, Card, Input, Empty, LabelChips, Badge } from '../components/ui'
import { Select } from '../components/forms'
import { Chart } from '../components/Chart'
import { DowntimeDialog } from '../components/AckDialog'
import { ObjectFormDialog, BatchAddDialog } from '../components/objects/ObjectForm'
import { t } from '../i18n'

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

  const { data, isLoading } = useQuery({
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

  return (
    <div className="space-y-3 h-full flex flex-col">
      <div className="flex items-center justify-between gap-2">
        <h1 className="text-lg font-bold">{t('objects')} <span className="text-slate-500 text-sm">({rows.length})</span></h1>
        <div className="flex gap-2">
          <Button onClick={() => setCreate('host')}>+ {t('newHost')}</Button>
          <Button onClick={() => setCreate('service')}>+ {t('newService')}</Button>
          <Button variant="ghost" onClick={() => setBatch(true)}>{t('batchAdd')}</Button>
        </div>
      </div>
      <div className="flex gap-2 flex-wrap">
        <Input placeholder={t('filter')} value={selector} onChange={(e) => setSelector(e.target.value)} className="max-w-xs" />
        <Input placeholder="Volltext…" value={query} onChange={(e) => setQuery(e.target.value)} className="max-w-xs" />
        <Select value={kindFilter} className="w-36"
          onChange={(e) => patchSearch({ kind: (e.target.value || undefined) as ObjectsSearch['kind'] })}>
          <option value="">Hosts + Services</option>
          <option value="host">Hosts</option>
          <option value="service">Services</option>
        </Select>
        <Select value={stateFilter} className="w-36"
          onChange={(e) => patchSearch({ state: e.target.value || undefined })}>
          {STATE_FILTERS.map(([v, label]) => <option key={v} value={v}>{label}</option>)}
        </Select>
        {(stateFilter || kindFilter) && (
          <Button variant="ghost" onClick={() => patchSearch({ state: undefined, kind: undefined })}>
            ✕ Filter zurücksetzen
          </Button>
        )}
      </div>
      {isLoading && <Empty text={t('loading')} />}
      <div ref={parentRef} className="flex-1 overflow-auto border border-slate-800 rounded-xl bg-slate-900/40 min-h-[420px]">
        <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
          {virtualizer.getVirtualItems().map((vi) => {
            const o = rows[vi.index]
            return (
              <div
                key={o.id}
                className="absolute left-0 right-0 flex items-center gap-3 px-3 border-b border-slate-800/50 hover:bg-slate-800/40 text-sm group"
                style={{ top: vi.start, height: vi.size }}
              >
                <Link to="/objects/$id" params={{ id: o.id }} className="flex items-center gap-3 flex-1 min-w-0 h-full">
                  <span className={`w-24 shrink-0 font-semibold ${o.state ? stateColor(o.kind, o.state.state) : 'text-slate-500'}`}>
                    {o.state?.lastCheck
                      ? `${stateIcon(o.kind, o.state.state)} ${stateLabel(o.kind, o.state.state)}`
                      : `○ ${t('pending')}`}
                  </span>
                  <span className="w-16 shrink-0 text-xs text-slate-500 uppercase">{o.kind}</span>
                  <span className="text-slate-200 font-medium truncate w-64 shrink-0">
                    {o.kind === 'service' && o.hostName ? `${o.hostName} / ` : ''}{o.name}
                  </span>
                  <span className="text-slate-500 text-xs truncate flex-1">{o.state?.output}</span>
                  <LabelChips labels={o.labels} />
                </Link>
                <div className="shrink-0 flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                  <Button size="sm" variant="ghost" onClick={() => recheck.mutate(o.id)} title={t('checkNow')}>↻</Button>
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
          <div className="text-xs text-slate-500">
            <Link to="/objects" className="hover:text-slate-300">{t('objects')}</Link>
            {' / '}{obj.folder !== '/' ? `${obj.folder} / ` : ''}
            {obj.kind === 'service' && obj.hostName ? `${obj.hostName} / ` : ''}
          </div>
          <h1 className="text-xl font-bold flex items-center gap-3">
            {obj.name}
            {cs && (
              <span className={`text-base font-bold ${stateColor(obj.kind, cs.state)}`}>
                {stateIcon(obj.kind, cs.state)} {stateLabel(obj.kind, cs.state)}
                <span className="text-slate-500 font-normal text-xs ml-1">
                  ({cs.stateType} {cs.attempt}x)
                </span>
              </span>
            )}
          </h1>
          <div className="mt-1"><LabelChips labels={obj.labels} /></div>
        </div>
        <div className="flex gap-2 shrink-0 items-center">
          <Button variant="primary" onClick={() => setEditOpen(true)}>{t('edit')}</Button>
          <Button onClick={() => recheck.mutate()} disabled={recheck.isPending}>↻ {t('checkNow')}</Button>
          <Button onClick={() => setDtOpen(true)}>{t('downtime')}</Button>
          <ObjectDeleteButton kind={obj.kind} size="md" onDelete={() => remove.mutate()} />
        </div>
      </div>

      {/* Interval / scheduling card (CMP Admin parity — check-interval
          management must be visible at a glance). */}
      <Card title={`${t('interval')} & Scheduling`}>
        <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-x-4 gap-y-2 text-sm">
          <Cfg k={t('interval')} v={eff.interval} />
          <Cfg k={t('retryInterval')} v={eff.retryInterval} />
          <Cfg k={t('maxAttempts')} v={eff.maxCheckAttempts != null ? String(eff.maxCheckAttempts) : undefined} />
          <Cfg k={t('timeout')} v={eff.timeout} />
          <Cfg k={t('checkPeriod')} v={eff.checkPeriod} />
          <Cfg k={t('checkCommand')} v={eff.checkCommand} mono />
        </div>
      </Card>

      <div className="grid lg:grid-cols-2 gap-4">
        <Card title={t('state')}>
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
        </Card>
        <Card title={`${t('effectiveConfig')}${effective?.templateChain?.length ? ` — ${t('templateChain')}: ${effective.templateChain.join(' → ')}` : ''}`}>
          <pre className="text-xs text-slate-400 overflow-auto max-h-64 font-mono">
            {JSON.stringify(effective?.spec ?? obj.spec, null, 2)}
          </pre>
        </Card>
      </div>

      {(series?.length ?? 0) > 0 && (
        <Card title={t('metrics')}>
          <div className="grid lg:grid-cols-2 gap-6">
            {series!.filter((s) => !s.series.metric.startsWith('np_')).map((s) => (
              <Chart key={s.series.id} result={s} />
            ))}
          </div>
        </Card>
      )}

      <Card title={t('history')}>
        {(events?.items?.length ?? 0) === 0
          ? <Empty text={t('empty')} />
          : (
            <div className="space-y-1 text-sm">
              {events!.items!.map((e) => (
                <div key={e.id} className="flex gap-3 py-1 border-b border-slate-800/50 last:border-0">
                  <span className="text-slate-600 text-xs tabular-nums w-36 shrink-0">{fmtTime(e.ts)}</span>
                  <Badge className="bg-slate-800 text-slate-400 border-slate-700">{e.type}</Badge>
                  <span className="text-slate-400 text-xs truncate">
                    {String((e.payload as Record<string, unknown>).output ??
                      (e.payload as Record<string, unknown>).summary ?? JSON.stringify(e.payload))}
                  </span>
                </div>
              ))}
            </div>
          )}
      </Card>
      <DowntimeDialog open={dtOpen} objectId={obj.id} objectName={obj.name} onClose={() => setDtOpen(false)} />
      {editOpen && <ObjectFormDialog open kind={obj.kind} edit={obj} onClose={() => setEditOpen(false)} />}
    </div>
  )
}

function Row({ k, v, mono }: { k: string; v?: string; mono?: boolean }) {
  return (
    <div className="flex gap-2">
      <dt className="text-slate-500 w-36 shrink-0">{k}</dt>
      <dd className={`text-slate-300 min-w-0 break-words ${mono ? 'font-mono text-xs pt-0.5' : ''}`}>{v || '—'}</dd>
    </div>
  )
}

// Cfg: compact label/value cell for the interval card.
function Cfg({ k, v, mono }: { k: string; v?: string; mono?: boolean }) {
  return (
    <div>
      <div className="text-[11px] text-slate-500 uppercase tracking-wider">{k}</div>
      <div className={`text-slate-200 ${mono ? 'font-mono text-xs' : 'tabular-nums'}`}>{v || '—'}</div>
    </div>
  )
}

// ObjectDeleteButton: inline-confirm delete that warns (German) when a host
// is removed, since deleting a host cascades to its services.
export function ObjectDeleteButton({ kind, onDelete, size = 'sm' }: {
  kind: 'host' | 'service'; onDelete: () => void; size?: 'sm' | 'md'
}) {
  const [arm, setArm] = useState(false)
  if (!arm) {
    return <Button size={size} variant="ghost" onClick={() => setArm(true)}>{t('delete')}</Button>
  }
  return (
    <span className="inline-flex items-center gap-1">
      {kind === 'host' && (
        <span className="text-[11px] text-amber-400">Löscht auch alle Services des Hosts?</span>
      )}
      <Button size={size} variant="danger" onClick={() => { setArm(false); onDelete() }}>{t('deleteConfirm')}</Button>
      <Button size={size} variant="ghost" onClick={() => setArm(false)}>{t('cancel')}</Button>
    </span>
  )
}
