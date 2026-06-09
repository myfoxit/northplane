// Sidebar control for the live-data refresh cadence (replaces the old SSE
// "always live" behaviour with an explicit, user-chosen polling interval).
import { RefreshCw } from 'lucide-react'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { REFRESH_PRESETS, useRefreshInterval, setRefreshInterval, type RefreshValue } from '../settings'

const toKey = (v: RefreshValue) => (v === false ? 'off' : String(v))
const fromKey = (k: string): RefreshValue => (k === 'off' ? false : Number(k))

export function RefreshControl() {
  const value = useRefreshInterval()
  return (
    <div className="flex items-center justify-between gap-2 px-1">
      <span className="flex items-center gap-1.5 text-xs text-muted-foreground" title="Wie oft Live-Daten neu geladen werden">
        <RefreshCw size={12} className="shrink-0" /> Aktualisierung
      </span>
      <Select value={toKey(value)} onValueChange={(k) => setRefreshInterval(fromKey(k))}>
        <SelectTrigger className="h-6 w-[64px] text-xs px-2"><SelectValue /></SelectTrigger>
        <SelectContent>
          {REFRESH_PRESETS.map((p) => (
            <SelectItem key={toKey(p.value)} value={toKey(p.value)} className="text-xs">{p.label}</SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}
