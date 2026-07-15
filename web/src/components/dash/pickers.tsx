// Shared async pickers for dashboards/business config: an object search
// Select (GET /objects?q=) and a per-object metric Select
// (GET /objects/{id}/metrics). Kept here so the metric widget config and
// the BPI leaf-binding form reuse the exact same lookup behaviour.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get, type ListResponse } from '../../api'
import type { NPObject } from '../../types'
import { Input } from '@/components/ui/input'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import { t } from '../../i18n'

interface SeriesMeta { id: number; objectId: string; metric: string; unit?: string }

// Radix SelectItem value cannot be "" — sentinel stands in for the
// empty/"choose" option and maps back to '' on change.
const NONE = '__none__'

// ObjectPicker: type-to-search box + a Select of matches. value is the
// objectId; onChange also reports the matched object name for display.
export function ObjectPicker({ value, onChange }: {
  value?: string
  onChange: (objectId: string, name?: string) => void
}) {
  const [q, setQ] = useState('')
  const { data } = useQuery({
    queryKey: ['objects', 'picker', q],
    queryFn: () => get<ListResponse<NPObject>>(`/objects?q=${encodeURIComponent(q)}&limit=50`),
  })
  const items = data?.items ?? []
  // Ensure the currently-selected id stays selectable even if not in the
  // latest search results.
  const hasValue = !!value && items.some((o) => o.id === value)
  return (
    <div className="space-y-1.5">
      <Input placeholder={t('searchObject')} value={q} onChange={(e) => setQ(e.target.value)} />
      <Select
        value={value || NONE}
        onValueChange={(v) => {
          const id = v === NONE ? '' : v
          const obj = items.find((o) => o.id === id)
          onChange(id, obj ? (obj.kind === 'service' && obj.hostName ? `${obj.hostName} / ${obj.name}` : obj.name) : undefined)
        }}
      >
        <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem value={NONE}>{t('selectObject')}</SelectItem>
          {!hasValue && value && <SelectItem value={value}>{value}</SelectItem>}
          {items.map((o) => (
            <SelectItem key={o.id} value={o.id}>
              {o.kind === 'service' && o.hostName ? `${o.hostName} / ${o.name}` : o.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

// MetricPicker: lists the metric series of an object so a chart widget can
// pin a single metric (empty = all series).
export function MetricPicker({ objectId, value, onChange }: {
  objectId?: string; value?: string; onChange: (metric: string) => void
}) {
  const { data } = useQuery({
    queryKey: ['object-metrics', objectId],
    enabled: !!objectId,
    queryFn: () => get<SeriesMeta[] | null>(`/objects/${objectId}/metrics`),
  })
  const series = (data ?? []).filter((s) => !s.metric.startsWith('np_'))
  return (
    <Select
      value={value || NONE}
      onValueChange={(v) => onChange(v === NONE ? '' : v)}
      disabled={!objectId}
    >
      <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
      <SelectContent>
        <SelectItem value={NONE}>{t('allMetrics')}</SelectItem>
        {value && !series.some((s) => s.metric === value) && <SelectItem value={value}>{value}</SelectItem>}
        {series.map((s) => (
          <SelectItem key={s.id} value={s.metric}>{s.metric}{s.unit ? ` (${s.unit})` : ''}</SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
