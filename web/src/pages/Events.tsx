// Event log (SPEC §12.3 / F-05.01): searchable, filterable, NDJSON export.
import { useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { get, fmtTime, type ListResponse } from '../api'
import type { NPEvent } from '../types'
import { sevColor } from '../types'
import { Badge, Empty, Input } from '../components/ui'
import { t } from '../i18n'

const eventTypes = ['', 'state_change', 'alert_opened', 'alert_resolved', 'notification',
  'escalation', 'ack', 'ingress', 'config', 'downtime', 'silence', 'heartbeat_missed', 'ai_action']

export function EventsPage() {
  const [type, setType] = useState('')
  const [objectId, setObjectId] = useState('')
  const { data, isLoading } = useQuery({
    queryKey: ['events', type, objectId],
    queryFn: () => get<ListResponse<NPEvent>>(
      `/events?types=${type}&objectId=${encodeURIComponent(objectId)}&limit=200`),
    placeholderData: keepPreviousData, // filter changes render instantly
  })
  const rows = data?.items ?? []
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-bold">{t('events')}</h1>
        <a href={`/api/v1/events:export?types=${type}`}
          className="text-xs text-slate-500 hover:text-slate-300">⇩ NDJSON Export</a>
      </div>
      <div className="flex gap-2">
        <select value={type} onChange={(e) => setType(e.target.value)}
          className="bg-slate-900 border border-slate-700 rounded-lg px-2 py-1 text-sm text-slate-300">
          {eventTypes.map((et) => <option key={et} value={et}>{et || 'alle Typen'}</option>)}
        </select>
        <Input placeholder="Object-ID…" value={objectId} onChange={(e) => setObjectId(e.target.value)}
          className="max-w-xs" />
      </div>
      {isLoading && <Empty text={t('loading')} />}
      {!isLoading && rows.length === 0 && <Empty text={t('empty')} />}
      <div className="border border-slate-800 rounded-xl bg-slate-900/40 divide-y divide-slate-800/50 text-sm">
        {rows.map((e) => (
          <details key={e.id} className="group">
            <summary className="flex items-center gap-3 px-3 py-1.5 cursor-pointer hover:bg-slate-800/40 list-none">
              <span className="text-slate-600 text-xs tabular-nums w-36 shrink-0">{fmtTime(e.ts)}</span>
              <Badge className="bg-slate-800 text-slate-400 border-slate-700 w-32 justify-center shrink-0">{e.type}</Badge>
              {e.severity && <Badge className={sevColor(e.severity)}>{e.severity}</Badge>}
              <span className="text-slate-400 text-xs truncate flex-1">
                {String((e.payload as Record<string, unknown>)?.summary ??
                  (e.payload as Record<string, unknown>)?.output ??
                  (e.payload as Record<string, unknown>)?.object ?? '')}
              </span>
            </summary>
            <pre className="text-xs text-slate-500 font-mono px-4 py-2 bg-slate-950/60 overflow-auto max-h-48">
              {JSON.stringify(e.payload, null, 2)}
            </pre>
          </details>
        ))}
      </div>
    </div>
  )
}
