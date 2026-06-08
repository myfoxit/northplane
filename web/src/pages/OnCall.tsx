// On-call view (SPEC §12.3): who is on duty now, schedule management
// (layered rotations + overrides), per-schedule 14-day timeline and
// hours-per-person stats. ICS links per schedule.
import { useQuery } from '@tanstack/react-query'
import { resourceApi } from '../api'
import type { Schedule } from '../types'
import { ErrorState } from '@/components/kit'
import { t } from '../i18n'
import {
  OnCallNowCards, SchedulesManager, ScheduleDetail,
} from '../components/alerting/Schedules'

const schedulesApi = resourceApi<Schedule>('schedules')

export function OnCallPage() {
  const { data: schedules, isError, error, refetch } = useQuery({ queryKey: schedulesApi.queryKey, queryFn: schedulesApi.list })
  if (isError && !schedules) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-bold">{t('oncall')}</h1>
      <OnCallNowCards />
      <SchedulesManager />
      {(schedules ?? []).map((s) => <ScheduleDetail key={s.name} schedule={s.name} />)}
    </div>
  )
}
