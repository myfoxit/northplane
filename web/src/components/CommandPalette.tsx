// ⌘K command palette (SPEC §12.4): navigation + object jump + actions.
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { get, type ListResponse } from '../api'
import type { NPObject } from '../types'
import { stateIcon, stateColor } from '../types'

interface Entry {
  label: string
  hint?: string
  icon?: string
  iconClass?: string
  run: () => void
}

export function CommandPalette({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState(0)
  const navigate = useNavigate()
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (open) {
      setQuery('')
      setSelected(0)
      setTimeout(() => inputRef.current?.focus(), 10)
    }
  }, [open])

  const { data: objects } = useQuery({
    queryKey: ['palette-objects', query],
    queryFn: () => get<ListResponse<NPObject>>(`/objects?q=${encodeURIComponent(query)}&limit=8&withState=true`),
    enabled: open && query.length >= 2,
  })

  const entries = useMemo<Entry[]>(() => {
    const pages: Entry[] = [
      { label: 'Overview', icon: '▦', run: () => navigate({ to: '/' }) },
      { label: 'Problems', icon: '▲', run: () => navigate({ to: '/problems' }) },
      { label: 'Objects', icon: '☰', run: () => navigate({ to: '/objects' }) },
      { label: 'Alerts', icon: '◉', run: () => navigate({ to: '/alerts' }) },
      { label: 'Incidents', icon: '☄', run: () => navigate({ to: '/incidents' }) },
      { label: 'Events', icon: '≋', run: () => navigate({ to: '/events' }) },
      { label: 'On-Call', icon: '☎', run: () => navigate({ to: '/oncall' }) },
      { label: 'Admin', icon: '⚙', run: () => navigate({ to: '/admin' }) },
      { label: 'Wallboard', icon: '▣', run: () => { window.location.href = '/?wallboard=1' } },
      { label: 'API Docs', icon: '⌘', run: () => { window.open('/api/docs', '_blank') } },
    ]
    const q = query.toLowerCase()
    const filtered = q ? pages.filter((p) => p.label.toLowerCase().includes(q)) : pages
    const objEntries: Entry[] = (objects?.items ?? []).map((o) => ({
      label: o.name,
      hint: o.kind === 'service' ? `service @ ${o.hostName}` : 'host',
      icon: o.state ? stateIcon(o.kind, o.state.state) : '○',
      iconClass: o.state ? stateColor(o.kind, o.state.state) : 'text-slate-500',
      run: () => navigate({ to: '/objects/$id', params: { id: o.id } }),
    }))
    return [...objEntries, ...filtered]
  }, [query, objects, navigate])

  useEffect(() => setSelected(0), [entries.length])

  if (!open) return null
  return (
    <div className="fixed inset-0 z-50 bg-black/60 flex items-start justify-center pt-[18vh] p-4" onClick={onClose}>
      <div
        className="bg-slate-900 border border-slate-700 rounded-xl w-full max-w-lg shadow-2xl overflow-hidden"
        onClick={(e) => e.stopPropagation()}
      >
        <input
          ref={inputRef}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') { e.preventDefault(); setSelected((s) => Math.min(s + 1, entries.length - 1)) }
            if (e.key === 'ArrowUp') { e.preventDefault(); setSelected((s) => Math.max(s - 1, 0)) }
            if (e.key === 'Enter' && entries[selected]) { entries[selected].run(); onClose() }
            if (e.key === 'Escape') onClose()
          }}
          placeholder="Navigation, Objekte, Aktionen…"
          className="w-full bg-transparent px-4 py-3 text-sm outline-none text-slate-200 placeholder:text-slate-500 border-b border-slate-800"
        />
        <div className="max-h-72 overflow-auto py-1">
          {entries.map((entry, i) => (
            <button
              key={`${entry.label}-${i}`}
              className={`w-full text-left px-4 py-2 text-sm flex items-center gap-3 cursor-pointer ${
                i === selected ? 'bg-blue-500/15 text-blue-200' : 'text-slate-300 hover:bg-slate-800'}`}
              onMouseEnter={() => setSelected(i)}
              onClick={() => { entry.run(); onClose() }}
            >
              <span className={`w-4 text-center ${entry.iconClass ?? 'text-slate-500'}`}>{entry.icon}</span>
              <span className="flex-1">{entry.label}</span>
              {entry.hint && <span className="text-xs text-slate-500">{entry.hint}</span>}
            </button>
          ))}
          {entries.length === 0 && <div className="px-4 py-3 text-sm text-slate-500">nichts gefunden</div>}
        </div>
      </div>
    </div>
  )
}
