// Overview / Wallboard (SPEC §12.3): KPI tiles, incidents, on-call
// widget; ?wallboard=1 renders the fullscreen read-only mode.
import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useSearch } from '@tanstack/react-router'
import { Radar, Maximize2, Phone, Check } from 'lucide-react'
import { get, fmtAgo, fmtTime } from '../api'
import type { Overview as OverviewData, OnCallNow, ProblemRow, NPEvent } from '../types'
import { stateLabel, stateIcon, stateColor, sevColor, eventBadge } from '../types'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Empty, ErrorState } from '@/components/kit'
import { humanizeOutput } from '@/lib/humanize'
import { StatCard, StatusDonut } from '../components/dash/overview-cards'
import { useDelta } from '../components/dash/delta'
import { t } from '../i18n'
import { useRefreshInterval } from '../settings'

// One-decimal percentage; empty denominator reads as 100% healthy.
const pct = (n: number, d: number) => (d > 0 ? Math.round((n / d) * 1000) / 10 : 100)

export function OverviewPage() {
  const search = useSearch({ strict: false }) as Record<string, unknown>
  const wallboard = !!search.wallboard
  const refresh = useRefreshInterval()
  // Wallboard clock: tick every second so an unattended NOC display never
  // shows a frozen time (which reads as "the whole board is stale").
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    if (!wallboard) return
    const id = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(id)
  }, [wallboard])
  const { data, isError, error, refetch } = useQuery({
    queryKey: ['overview'],
    queryFn: () => get<OverviewData>('/overview'),
    refetchInterval: wallboard ? 10_000 : refresh,
  })
  const { data: oncall } = useQuery({
    queryKey: ['oncall'],
    queryFn: () => get<OnCallNow[]>('/oncall/now'),
    refetchInterval: 60_000,
  })
  const { data: problems } = useQuery({
    queryKey: ['problems'],
    queryFn: () => get<{ items: ProblemRow[] | null }>('/problems?limit=15'),
    refetchInterval: wallboard ? 10_000 : refresh,
  })
  const { data: events } = useQuery({
    queryKey: ['events', 'overview'],
    queryFn: () => get<{ items: NPEvent[] | null }>('/events?limit=20'),
    refetchInterval: wallboard ? 10_000 : refresh,
  })

  const s = data?.summary
  const critAlerts = data?.openAlerts?.critical ?? 0
  const warnAlerts = data?.openAlerts?.warning ?? 0

  const hostsTotal = (s?.hostsUp ?? 0) + (s?.hostsDown ?? 0) + (s?.hostsUnreachable ?? 0)
  const svcTotal = (s?.servicesOk ?? 0) + (s?.servicesWarning ?? 0) + (s?.servicesCritical ?? 0) + (s?.servicesUnknown ?? 0)
  const problemCount = (s?.hostsDown ?? 0) + (s?.hostsUnreachable ?? 0) + (s?.servicesCritical ?? 0) + (s?.servicesWarning ?? 0) + (s?.servicesUnknown ?? 0)
  const alertCount = critAlerts + warnAlerts

  // Movement badges — the last change of each counter across polls. Hooks stay
  // above every early return.
  const dProblems = useDelta('ov.problems', s ? problemCount : undefined)
  const dAlerts = useDelta('ov.alerts', s ? alertCount : undefined)
  // Rising problems/alerts read as bad (crit), falling as good (ok).
  const deltaBadge = (d: number): { badge?: string; badgeTone?: 'ok' | 'crit' } =>
    d === 0 ? {} : { badge: `${d > 0 ? '+' : ''}${d}`, badgeTone: d > 0 ? 'crit' : 'ok' }

  const tiles = (
    <div className="grid gap-3 grid-cols-2 lg:grid-cols-4">
      <StatCard label={t('hostsUp')} value={s?.hostsUp ?? '—'} total={hostsTotal || undefined} tone="ok"
        badge={hostsTotal ? `${pct(s?.hostsUp ?? 0, hostsTotal)}%` : undefined}
        badgeTone={(s?.hostsDown || s?.hostsUnreachable) ? 'crit' : 'ok'}
        sublabel={`${s?.hostsDown ?? 0} ${t('subDown')} · ${s?.hostsUnreachable ?? 0} ${t('subUnreachable')}`}
        to="/objects" search={{ kind: 'host', state: 'up' }} />
      <StatCard label={t('servicesOk')} value={s?.servicesOk ?? '—'} total={svcTotal || undefined} tone="ok"
        badge={svcTotal ? `${pct(s?.servicesOk ?? 0, svcTotal)}%` : undefined}
        badgeTone={s?.servicesCritical ? 'crit' : s?.servicesWarning ? 'warn' : 'ok'}
        sublabel={`${s?.servicesCritical ?? 0} ${t('stCritical')} · ${s?.servicesWarning ?? 0} ${t('stWarning')}`}
        to="/objects" search={{ kind: 'service', state: 'ok' }} />
      <StatCard label={t('activeProblems')} value={s ? problemCount : '—'} tone={problemCount ? 'crit' : 'default'}
        {...deltaBadge(dProblems)}
        sublabel={`${s?.acked ?? 0} ${t('subAcked')} · ${s?.inDowntime ?? 0} ${t('inDowntime')}`}
        to="/problems" />
      <StatCard label={t('openAlerts')} value={alertCount} tone={critAlerts ? 'crit' : warnAlerts ? 'warn' : 'default'}
        {...deltaBadge(dAlerts)}
        sublabel={`${critAlerts} ${t('stCritical')} · ${warnAlerts} ${t('stWarning')}`}
        to="/alerts" />
    </div>
  )

  if (isError && !data) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }

  if (wallboard) {
    return (
      <div className="p-6 space-y-5">
        <div className="flex items-center justify-between">
          <h1 className="text-xl font-bold flex items-center gap-2"><Radar className="text-primary" size={22} /> Northplane {t('wallboard')}</h1>
          <span className="text-muted-foreground text-sm tabular-nums">{now.toLocaleTimeString()}</span>
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
        <a href="/?wallboard=1" className="inline-flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground">
          <Maximize2 size={13} /> {t('wallboard')}
        </a>
      </div>
      {tiles}
      <div className="grid lg:grid-cols-3 gap-4">
        <Card className="lg:col-span-2">
          <CardHeader><CardTitle>{t('problems')}</CardTitle></CardHeader>
          <CardContent>
            <ProblemList problems={problems?.items ?? []} />
          </CardContent>
        </Card>
        <div className="space-y-4">
          <Card>
            <CardHeader><CardTitle>{t('serviceStatus')}</CardTitle></CardHeader>
            <CardContent>
              <StatusDonut total={svcTotal} healthyPct={pct(s?.servicesOk ?? 0, svcTotal)}
                segments={[
                  { label: t('stOk'), value: s?.servicesOk ?? 0, color: 'var(--success)' },
                  { label: t('stWarning'), value: s?.servicesWarning ?? 0, color: 'var(--warning)' },
                  { label: t('stCritical'), value: s?.servicesCritical ?? 0, color: 'var(--danger)' },
                  { label: t('stUnknown'), value: s?.servicesUnknown ?? 0, color: 'var(--muted-foreground)' },
                ]} />
            </CardContent>
          </Card>
          <Card>
            <CardHeader><CardTitle>{t('openIncidents')}</CardTitle></CardHeader>
            <CardContent>
              {(data?.openIncidents?.length ?? 0) === 0
                ? <Empty text={t('empty')} />
                : data!.openIncidents.map((inc) => (
                  <Link key={inc.id} to="/incidents" className="block py-1.5 border-b border-border/60 last:border-0">
                    <Badge variant="outline" className={sevColor(inc.severity)}>{inc.severity}</Badge>
                    <span className="text-sm ml-2 text-foreground/90">{inc.title}</span>
                  </Link>
                ))}
            </CardContent>
          </Card>
          <Card>
            <CardHeader><CardTitle>{t('onCallNow')}</CardTitle></CardHeader>
            <CardContent>
              {(oncall?.length ?? 0) === 0
                ? <Empty text={t('empty')} />
                : oncall!.map((entry) => (
                  <div key={entry.schedule} className="py-1.5 border-b border-border/60 last:border-0">
                    <div className="text-xs text-muted-foreground">{entry.schedule}</div>
                    {entry.contacts?.map((c) => (
                      <div key={c.id ?? c.name} className="text-sm text-foreground/90 flex items-center gap-1.5">
                        <Phone size={12} className="text-muted-foreground" /> {c.name}
                        {c.phone && <span className="text-muted-foreground text-xs ml-1">{c.phone}</span>}
                      </div>
                    ))}
                  </div>
                ))}
            </CardContent>
          </Card>
        </div>
      </div>

      {/* Recent events feed — fills the lower viewport with useful context even
          when everything is green (OVW-1). */}
      <Card>
        <CardHeader><CardTitle>{t('recentEvents')}</CardTitle></CardHeader>
        <CardContent>
          {(events?.items?.length ?? 0) === 0 ? (
            <Empty text={t('empty')} />
          ) : (
            <div className="divide-y divide-border/60">
              {events!.items!.map((e) => (
                <div key={e.id} className="flex items-center gap-3 py-1.5 text-sm">
                  <span className="text-muted-foreground/70 text-xs tabular-nums w-32 shrink-0">{fmtTime(e.ts)}</span>
                  <Badge variant="outline" className="bg-muted text-muted-foreground border-input shrink-0">{e.type}</Badge>
                  {(() => { const b = eventBadge(e); return b && <Badge variant="outline" className={`${b.className} shrink-0`}>{b.label}</Badge> })()}
                  <span className="text-muted-foreground truncate">
                    {humanizeOutput(String((e.payload as Record<string, unknown>).output ??
                      (e.payload as Record<string, unknown>).summary ?? ''))}
                  </span>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function ProblemList({ problems, big }: { problems: ProblemRow[]; big?: boolean }) {
  if (problems.length === 0) {
    return <div className="text-success/90 text-sm p-4 flex items-center gap-1.5"><Check size={15} /> {t('noProblems')}</div>
  }
  return (
    <div className="divide-y divide-border/60">
      {problems.map((p) => (
        <Link
          key={p.object.id} to="/objects/$id" params={{ id: p.object.id }}
          className={`flex items-center gap-3 py-2 hover:bg-card/60 px-1 rounded ${big ? 'text-base' : 'text-sm'}`}
        >
          <span className={`${stateColor(p.object.kind, p.state.state)} font-bold w-20`}>
            {stateIcon(p.object.kind, p.state.state)} {stateLabel(p.object.kind, p.state.state)}
          </span>
          <span className="text-foreground font-medium truncate">
            {p.object.kind === 'service' && p.object.hostName ? `${p.object.hostName} / ` : ''}{p.object.name}
          </span>
          <span className="text-muted-foreground truncate flex-1">{humanizeOutput(p.state.output)}</span>
          <span className="text-muted-foreground/70 text-xs tabular-nums shrink-0">{fmtAgo(p.state.lastHardChange)}</span>
        </Link>
      ))}
    </div>
  )
}
