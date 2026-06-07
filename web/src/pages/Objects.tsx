// Objects explorer (SPEC §12.3): selector filter bar + full text,
// virtualised table (≥ 200 rows), object detail with effective config,
// metric charts with threshold bands, event timeline.
import { useRef, useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { useVirtualizer } from '@tanstack/react-virtual'
import { get, post, fmtTime, fmtAgo, queryClient, type ListResponse } from '../api'
import type { NPObject, NPEvent, SeriesResult } from '../types'
import { stateLabel, stateIcon, stateColor } from '../types'
import { Button, Card, Input, Empty, LabelChips, Badge } from '../components/ui'
import { Chart } from '../components/Chart'
import { DowntimeDialog } from '../components/AckDialog'
import { t } from '../i18n'

export function ObjectsPage() {
  const [selector, setSelector] = useState('')
  const [query, setQuery] = useState('')
  const parentRef = useRef<HTMLDivElement>(null)

  const { data, isLoading } = useQuery({
    queryKey: ['objects', selector, query],
    queryFn: () => get<ListResponse<NPObject>>(
      `/objects?selector=${encodeURIComponent(selector)}&q=${encodeURIComponent(query)}&limit=2000`),
  })
  const rows = data?.items ?? []
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 44,
    overscan: 20,
  })

  return (
    <div className="space-y-3 h-full flex flex-col">
      <h1 className="text-lg font-bold">{t('objects')} <span className="text-slate-500 text-sm">({rows.length})</span></h1>
      <div className="flex gap-2">
        <Input placeholder={t('filter')} value={selector} onChange={(e) => setSelector(e.target.value)} className="max-w-xs" />
        <Input placeholder="Volltext…" value={query} onChange={(e) => setQuery(e.target.value)} className="max-w-xs" />
      </div>
      {isLoading && <Empty text={t('loading')} />}
      <div ref={parentRef} className="flex-1 overflow-auto border border-slate-800 rounded-xl bg-slate-900/40 min-h-[420px]">
        <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
          {virtualizer.getVirtualItems().map((vi) => {
            const o = rows[vi.index]
            return (
              <Link
                key={o.id} to="/objects/$id" params={{ id: o.id }}
                className="absolute left-0 right-0 flex items-center gap-3 px-3 border-b border-slate-800/50 hover:bg-slate-800/40 text-sm"
                style={{ top: vi.start, height: vi.size }}
              >
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
            )
          })}
        </div>
        {!isLoading && rows.length === 0 && <Empty text={t('empty')} />}
      </div>
    </div>
  )
}

export function ObjectDetailPage() {
  const { id } = useParams({ strict: false }) as { id: string }
  const [dtOpen, setDtOpen] = useState(false)

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

  if (!obj) return <Empty text={t('loading')} />
  const cs = obj.state
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
        <div className="flex gap-2 shrink-0">
          <Button onClick={() => recheck.mutate()} disabled={recheck.isPending}>↻ {t('checkNow')}</Button>
          <Button onClick={() => setDtOpen(true)}>{t('downtime')}</Button>
        </div>
      </div>

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
