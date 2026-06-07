// Shared async pickers for dashboards/business config: an object search
// Select (GET /objects?q=) and a per-object metric Select
// (GET /objects/{id}/metrics). Kept here so the metric widget config and
// the BPI leaf-binding form reuse the exact same lookup behaviour.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get, type ListResponse } from '../../api'
import type { NPObject } from '../../types'
import { Select } from '../forms'
import { Input } from '../ui'

interface SeriesMeta { id: number; objectId: string; metric: string; unit?: string }

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
      <Input placeholder="Objekt suchen…" value={q} onChange={(e) => setQ(e.target.value)} />
      <Select
        value={value ?? ''}
        onChange={(e) => {
          const id = e.target.value
          const obj = items.find((o) => o.id === id)
          onChange(id, obj ? (obj.kind === 'service' && obj.hostName ? `${obj.hostName} / ${obj.name}` : obj.name) : undefined)
        }}
      >
        <option value="">— Objekt wählen —</option>
        {!hasValue && value && <option value={value}>{value}</option>}
        {items.map((o) => (
          <option key={o.id} value={o.id}>
            {o.kind === 'service' && o.hostName ? `${o.hostName} / ${o.name}` : o.name}
          </option>
        ))}
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
    <Select value={value ?? ''} onChange={(e) => onChange(e.target.value)} disabled={!objectId}>
      <option value="">— alle Metriken —</option>
      {value && !series.some((s) => s.metric === value) && <option value={value}>{value}</option>}
      {series.map((s) => (
        <option key={s.id} value={s.metric}>{s.metric}{s.unit ? ` (${s.unit})` : ''}</option>
      ))}
    </Select>
  )
}
