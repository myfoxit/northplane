// ⌘K command palette (SPEC §12.4): navigation + object jump + actions.
// Built on shadcn/cmdk (CommandDialog) for proper listbox/aria/focus-trap.
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  LayoutGrid, TriangleAlert, Boxes, CircleDot, Zap, Activity,
  Phone, Settings, Maximize2, Command as CommandIcon,
} from 'lucide-react'
import {
  CommandDialog, CommandInput, CommandList, CommandEmpty, CommandGroup, CommandItem, CommandSeparator,
} from '@/components/ui/command'
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
  const navigate = useNavigate()

  useEffect(() => {
    if (open) setQuery('')
  }, [open])

  const { data: objects } = useQuery({
    queryKey: ['palette-objects', query],
    queryFn: () => get<ListResponse<NPObject>>(`/objects?q=${encodeURIComponent(query)}&limit=8&withState=true`),
    enabled: open && query.length >= 2,
  })

  const pages = useMemo<Entry[]>(() => [
    { label: 'Overview', icon: <LayoutGrid size={14} />, run: () => navigate({ to: '/' }) },
    { label: 'Problems', icon: <TriangleAlert size={14} />, run: () => navigate({ to: '/problems' }) },
    { label: 'Objects', icon: <Boxes size={14} />, run: () => navigate({ to: '/objects' }) },
    { label: 'Alerts', icon: <CircleDot size={14} />, run: () => navigate({ to: '/alerts' }) },
    { label: 'Incidents', icon: <Zap size={14} />, run: () => navigate({ to: '/incidents' }) },
    { label: 'Events', icon: <Activity size={14} />, run: () => navigate({ to: '/events' }) },
    { label: 'On-Call', icon: <Phone size={14} />, run: () => navigate({ to: '/oncall' }) },
    { label: 'Admin', icon: <Settings size={14} />, run: () => navigate({ to: '/admin' }) },
    { label: 'Wallboard', icon: <Maximize2 size={14} />, run: () => { window.location.href = '/?wallboard=1' } },
    { label: 'API Docs', icon: <CommandIcon size={14} />, run: () => { window.open('/api/docs', '_blank') } },
  ], [navigate])

  // Object entries are already server-filtered (fuzzy on the API). cmdk's
  // built-in filter then matches both groups against the typed query — its
  // value embeds the object name so server hits survive the local pass.
  const objEntries: Entry[] = useMemo(() => (objects?.items ?? []).map((o) => ({
    label: o.name,
    hint: o.kind === 'service' ? `service @ ${o.hostName}` : 'host',
    icon: o.state ? stateIcon(o.kind, o.state.state) : '○',
    iconClass: o.state ? stateColor(o.kind, o.state.state) : 'text-muted-foreground',
    run: () => navigate({ to: '/objects/$id', params: { id: o.id } }),
  })), [objects, navigate])

  const select = (entry: Entry) => { entry.run(); onClose() }

  return (
    <CommandDialog
      open={open}
      onOpenChange={(o) => { if (!o) onClose() }}
      className="max-w-lg"
    >
      <CommandInput
        value={query}
        onValueChange={setQuery}
        placeholder="Navigation, Objekte, Aktionen…"
      />
      <CommandList>
        <CommandEmpty>nichts gefunden</CommandEmpty>
        {objEntries.length > 0 && (
          <CommandGroup>
            {objEntries.map((entry, i) => (
              <CommandItem
                key={`obj-${entry.label}-${i}`}
                value={`${entry.label} ${query}`}
                onSelect={() => select(entry)}
              >
                <span className={`w-4 flex justify-center ${entry.iconClass ?? 'text-muted-foreground'}`}>{entry.icon}</span>
                <span className="flex-1">{entry.label}</span>
                {entry.hint && <span className="text-xs text-muted-foreground">{entry.hint}</span>}
              </CommandItem>
            ))}
          </CommandGroup>
        )}
        {objEntries.length > 0 && <CommandSeparator />}
        <CommandGroup>
          {pages.map((entry, i) => (
            <CommandItem
              key={`page-${entry.label}-${i}`}
              value={entry.label}
              onSelect={() => select(entry)}
            >
              <span className={`w-4 flex justify-center ${entry.iconClass ?? 'text-muted-foreground'}`}>{entry.icon}</span>
              <span className="flex-1">{entry.label}</span>
            </CommandItem>
          ))}
        </CommandGroup>
      </CommandList>
    </CommandDialog>
  )
}
