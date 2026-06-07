// On-call view (SPEC §12.3): who is on duty, 14-day timeline per
// schedule, ICS links.
import { useQuery } from '@tanstack/react-query'
import { get, fmtTime, type ListResponse } from '../api'
import type { OnCallNow } from '../types'
import { Card, Empty } from '../components/ui'
import { t } from '../i18n'

interface Shift {
  contactId: string
  start: string
  end: string
  override?: boolean
}

interface Schedule {
  name: string
}

export function OnCallPage() {
  const { data: now } = useQuery({
    queryKey: ['oncall'],
    queryFn: () => get<OnCallNow[]>('/oncall/now'),
  })
  const { data: schedules } = useQuery({
    queryKey: ['schedules'],
    queryFn: () => get<ListResponse<Schedule>>('/schedules'),
  })
  const list = schedules?.items ?? []
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-bold">{t('oncall')}</h1>
      <div className="grid lg:grid-cols-3 gap-3">
        {(now ?? []).map((entry) => (
          <Card key={entry.schedule} title={entry.schedule}
            actions={<a className="text-xs text-slate-500 hover:text-slate-300"
              href={`/api/v1/schedules/${encodeURIComponent(entry.schedule)}/ics`}>⇩ ICS</a>}>
            {(entry.contacts?.length ?? 0) === 0
              ? <Empty text="niemand im Dienst" />
              : entry.contacts.map((c) => (
                <div key={c.id ?? c.name} className="py-1">
                  <div className="text-base font-semibold text-slate-200">☎ {c.name}</div>
                  <div className="text-xs text-slate-500">{c.email} {c.phone && `· ${c.phone}`}</div>
                </div>
              ))}
          </Card>
        ))}
        {(now?.length ?? 0) === 0 && <Empty text="Keine Bereitschaftspläne definiert." />}
      </div>
      {list.map((s) => <Timeline key={s.name} schedule={s.name} />)}
    </div>
  )
}

function Timeline({ schedule }: { schedule: string }) {
  const { data } = useQuery({
    queryKey: ['schedules', schedule, 'timeline'],
    queryFn: () => get<Shift[] | null>(`/schedules/${encodeURIComponent(schedule)}/timeline?days=14`),
  })
  const { data: contacts } = useQuery({
    queryKey: ['resources', 'contacts'],
    queryFn: () => get<ListResponse<{ id: string; name: string }>>('/contacts'),
  })
  const nameOf = (id: string) =>
    contacts?.items?.find((c) => c.id === id || c.name === id)?.name ?? id

  if (!data?.length) return null
  const palette = ['bg-blue-600/50', 'bg-emerald-600/50', 'bg-purple-600/50',
    'bg-amber-600/50', 'bg-pink-600/50', 'bg-cyan-600/50']
  const colorByContact: Record<string, string> = {}
  let nextColor = 0
  const t0 = new Date(data[0].start).getTime()
  const t1 = new Date(data[data.length - 1].end).getTime()
  const span = Math.max(1, t1 - t0)

  return (
    <Card title={`${schedule} — 14 Tage`}>
      <div className="relative h-12 bg-slate-950 rounded-lg overflow-hidden border border-slate-800">
        {data.map((shift, i) => {
          const left = ((new Date(shift.start).getTime() - t0) / span) * 100
          const width = ((new Date(shift.end).getTime() - new Date(shift.start).getTime()) / span) * 100
          if (!colorByContact[shift.contactId]) {
            colorByContact[shift.contactId] = palette[nextColor++ % palette.length]
          }
          return (
            <div key={i}
              title={`${nameOf(shift.contactId)}: ${fmtTime(shift.start)} – ${fmtTime(shift.end)}${shift.override ? ' (Override)' : ''}`}
              className={`absolute top-0 bottom-0 flex items-center justify-center text-[11px] text-white/90 truncate px-1 border-r border-slate-950 ${colorByContact[shift.contactId]} ${shift.override ? 'ring-2 ring-amber-400/70 ring-inset' : ''}`}
              style={{ left: `${left}%`, width: `${width}%` }}
            >
              {width > 5 ? nameOf(shift.contactId) : ''}
            </div>
          )
        })}
      </div>
      <div className="flex justify-between text-[11px] text-slate-600 mt-1 tabular-nums">
        <span>{fmtTime(data[0].start)}</span>
        <span>{fmtTime(data[data.length - 1].end)}</span>
      </div>
    </Card>
  )
}
