// Overview / Wallboard (SPEC §12.3): KPI tiles, incidents, on-call
// widget; ?wallboard=1 renders the fullscreen read-only mode.
import { useQuery } from '@tanstack/react-query'
import { Link, useSearch } from '@tanstack/react-router'
import { get, fmtAgo } from '../api'
import type { Overview as OverviewData, OnCallNow, ProblemRow } from '../types'
import { stateLabel, stateIcon, stateColor, sevColor } from '../types'
import { Card, Badge, Empty } from '../components/ui'
import { TileLink } from '../components/dash/widgets'
import { t } from '../i18n'

export function OverviewPage() {
  const search = useSearch({ strict: false }) as Record<string, unknown>
  const wallboard = !!search.wallboard
  const { data } = useQuery({
    queryKey: ['overview'],
    queryFn: () => get<OverviewData>('/overview'),
    refetchInterval: wallboard ? 10_000 : 30_000,
  })
  const { data: oncall } = useQuery({
    queryKey: ['oncall'],
    queryFn: () => get<OnCallNow[]>('/oncall/now'),
    refetchInterval: 60_000,
  })
  const { data: problems } = useQuery({
    queryKey: ['problems'],
    queryFn: () => get<{ items: ProblemRow[] | null }>('/problems?limit=15'),
  })

  const s = data?.summary
  const critAlerts = data?.openAlerts?.critical ?? 0
  const warnAlerts = data?.openAlerts?.warning ?? 0

  const tiles = (
    <div className={`grid gap-3 ${wallboard ? 'grid-cols-3 lg:grid-cols-6' : 'grid-cols-2 lg:grid-cols-6'}`}>
      <TileLink to="/objects" search={{ kind: 'host', state: 'up' }} label={t('hostsUp')} value={s?.hostsUp ?? '—'} tone="ok" />
      <TileLink to="/objects" search={{ kind: 'host', state: 'down' }} label={t('hostsDown')} value={s?.hostsDown ?? '—'} tone={s?.hostsDown ? 'crit' : 'default'} />
      <TileLink to="/objects" search={{ kind: 'service', state: 'ok' }} label={t('servicesOk')} value={s?.servicesOk ?? '—'} tone="ok" />
      <TileLink to="/objects" search={{ kind: 'service', state: 'warning' }} label={t('servicesWarning')} value={s?.servicesWarning ?? '—'} tone={s?.servicesWarning ? 'warn' : 'default'} />
      <TileLink to="/objects" search={{ kind: 'service', state: 'critical' }} label={t('servicesCritical')} value={s?.servicesCritical ?? '—'} tone={s?.servicesCritical ? 'crit' : 'default'} />
      <TileLink to="/alerts" label={t('openAlerts')} value={`${critAlerts + warnAlerts}`} tone={critAlerts ? 'crit' : warnAlerts ? 'warn' : 'default'} />
    </div>
  )

  if (wallboard) {
    return (
      <div className="p-6 space-y-5">
        <div className="flex items-center justify-between">
          <h1 className="text-xl font-bold"><span className="text-blue-400">▲</span> Northplane {t('wallboard')}</h1>
          <span className="text-slate-500 text-sm tabular-nums">{new Date().toLocaleTimeString()}</span>
        </div>
        {tiles}
        <ProblemList problems={problems?.items ?? []} big />
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-bold">{t('overview')}</h1>
        <a href="/?wallboard=1" className="text-xs text-slate-500 hover:text-slate-300">▣ {t('wallboard')}</a>
      </div>
      {tiles}
      <div className="grid lg:grid-cols-3 gap-4">
        <Card title={t('problems')} className="lg:col-span-2">
          <ProblemList problems={problems?.items ?? []} />
        </Card>
        <div className="space-y-4">
          <Card title={t('openIncidents')}>
            {(data?.openIncidents?.length ?? 0) === 0
              ? <Empty text={t('empty')} />
              : data!.openIncidents.map((inc) => (
                <Link key={inc.id} to="/incidents" className="block py-1.5 border-b border-slate-800/60 last:border-0">
                  <Badge className={sevColor(inc.severity)}>{inc.severity}</Badge>
                  <span className="text-sm ml-2 text-slate-300">{inc.title}</span>
                </Link>
              ))}
          </Card>
          <Card title={t('onCallNow')}>
            {(oncall?.length ?? 0) === 0
              ? <Empty text={t('empty')} />
              : oncall!.map((entry) => (
                <div key={entry.schedule} className="py-1.5 border-b border-slate-800/60 last:border-0">
                  <div className="text-xs text-slate-500">{entry.schedule}</div>
                  {entry.contacts?.map((c) => (
                    <div key={c.id ?? c.name} className="text-sm text-slate-300">☎ {c.name}
                      {c.phone && <span className="text-slate-500 text-xs ml-2">{c.phone}</span>}
                    </div>
                  ))}
                </div>
              ))}
          </Card>
        </div>
      </div>
    </div>
  )
}

function ProblemList({ problems, big }: { problems: ProblemRow[]; big?: boolean }) {
  if (problems.length === 0) {
    return <div className="text-emerald-500/80 text-sm p-4">✓ {t('noProblems')}</div>
  }
  return (
    <div className="divide-y divide-slate-800/60">
      {problems.map((p) => (
        <Link
          key={p.object.id} to="/objects/$id" params={{ id: p.object.id }}
          className={`flex items-center gap-3 py-2 hover:bg-slate-900/60 px-1 rounded ${big ? 'text-base' : 'text-sm'}`}
        >
          <span className={`${stateColor(p.object.kind, p.state.state)} font-bold w-20`}>
            {stateIcon(p.object.kind, p.state.state)} {stateLabel(p.object.kind, p.state.state)}
          </span>
          <span className="text-slate-200 font-medium truncate">
            {p.object.kind === 'service' && p.object.hostName ? `${p.object.hostName} / ` : ''}{p.object.name}
          </span>
          <span className="text-slate-500 truncate flex-1">{p.state.output}</span>
          <span className="text-slate-600 text-xs tabular-nums shrink-0">{fmtAgo(p.state.lastHardChange)}</span>
        </Link>
      ))}
    </div>
  )
}
