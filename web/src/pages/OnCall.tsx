// On-call view (SPEC §12.3): who is on duty now, schedule management
// (layered rotations + overrides), per-schedule 14-day timeline and
// hours-per-person stats. ICS links per schedule.
import { useQuery } from '@tanstack/react-query'
import { resourceApi } from '../api'
import type { Schedule } from '../types'
import { t } from '../i18n'
import {
  OnCallNowCards, SchedulesManager, ScheduleDetail,
} from '../components/alerting/Schedules'

const schedulesApi = resourceApi<Schedule>('schedules')

export function OnCallPage() {
  const { data: schedules } = useQuery({ queryKey: schedulesApi.queryKey, queryFn: schedulesApi.list })
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-bold">{t('oncall')}</h1>
      <OnCallNowCards />
      <SchedulesManager />
      {(schedules ?? []).map((s) => <ScheduleDetail key={s.name} schedule={s.name} />)}
    </div>
  )
}
