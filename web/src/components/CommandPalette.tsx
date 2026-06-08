// ⌘K command palette (SPEC §12.4): navigation + object jump + actions.
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  LayoutGrid, TriangleAlert, Boxes, CircleDot, Zap, Activity,
  Phone, Settings, Maximize2, Command,
} from 'lucide-react'
import { get, type ListResponse } from '../api'
import type { NPObject } from '../types'
import { stateIcon, stateColor } from '../types'

interface Entry {
  label: string
  hint?: string
  icon?: ReactNode
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
      { label: 'Overview', icon: <LayoutGrid size={14} />, run: () => navigate({ to: '/' }) },
      { label: 'Problems', icon: <TriangleAlert size={14} />, run: () => navigate({ to: '/problems' }) },
      { label: 'Objects', icon: <Boxes size={14} />, run: () => navigate({ to: '/objects' }) },
      { label: 'Alerts', icon: <CircleDot size={14} />, run: () => navigate({ to: '/alerts' }) },
      { label: 'Incidents', icon: <Zap size={14} />, run: () => navigate({ to: '/incidents' }) },
      { label: 'Events', icon: <Activity size={14} />, run: () => navigate({ to: '/events' }) },
      { label: 'On-Call', icon: <Phone size={14} />, run: () => navigate({ to: '/oncall' }) },
      { label: 'Admin', icon: <Settings size={14} />, run: () => navigate({ to: '/admin' }) },
      { label: 'Wallboard', icon: <Maximize2 size={14} />, run: () => { window.location.href = '/?wallboard=1' } },
      { label: 'API Docs', icon: <Command size={14} />, run: () => { window.open('/api/docs', '_blank') } },
    ]
    const q = query.toLowerCase()
    const filtered = q ? pages.filter((p) => p.label.toLowerCase().includes(q)) : pages
    const objEntries: Entry[] = (objects?.items ?? []).map((o) => ({
      label: o.name,
      hint: o.kind === 'service' ? `service @ ${o.hostName}` : 'host',
      icon: o.state ? stateIcon(o.kind, o.state.state) : '○',
      iconClass: o.state ? stateColor(o.kind, o.state.state) : 'text-muted-foreground',
      run: () => navigate({ to: '/objects/$id', params: { id: o.id } }),
    }))
    return [...objEntries, ...filtered]
  }, [query, objects, navigate])

  useEffect(() => setSelected(0), [entries.length])

  if (!open) return null
  return (
    <div className="fixed inset-0 z-50 bg-black/60 flex items-start justify-center pt-[18vh] p-4" onClick={onClose}>
      <div
        className="bg-card border border-input rounded-xl w-full max-w-lg shadow-2xl overflow-hidden"
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
          className="w-full bg-transparent px-4 py-3 text-sm outline-none text-foreground placeholder:text-muted-foreground border-b border-border"
        />
        <div className="max-h-72 overflow-auto py-1">
          {entries.map((entry, i) => (
            <button
              key={`${entry.label}-${i}`}
              className={`w-full text-left px-4 py-2 text-sm flex items-center gap-3 cursor-pointer ${
                i === selected ? 'bg-primary/15 text-primary' : 'text-foreground/90 hover:bg-muted'}`}
              onMouseEnter={() => setSelected(i)}
              onClick={() => { entry.run(); onClose() }}
            >
              <span className={`w-4 flex justify-center ${entry.iconClass ?? 'text-muted-foreground'}`}>{entry.icon}</span>
              <span className="flex-1">{entry.label}</span>
              {entry.hint && <span className="text-xs text-muted-foreground">{entry.hint}</span>}
            </button>
          ))}
          {entries.length === 0 && <div className="px-4 py-3 text-sm text-muted-foreground">nichts gefunden</div>}
        </div>
      </div>
    </div>
  )
}
