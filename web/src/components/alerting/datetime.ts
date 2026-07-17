// Pure helpers + constants for the alerting-config UIs. Kept JSX-free in a
// .ts module so the component widgets (controls.tsx) stay HMR-clean
// (react-refresh/only-export-components).
import type { ChannelType, Severity } from '../../types'

// All severities a rule/alert can carry (ok is allowed for resolve rules).
export const severities: Severity[] = ['critical', 'warning', 'info', 'ok']

// Channel types the notifier supports (mirror ChannelType in types.ts).
export const channelTypes: ChannelType[] = [
  'email', 'webhook', 'slack', 'teams', 'ntfy', 'sms', 'push', 'voice', 'mqtt',
]

// <input type="datetime-local"> needs "YYYY-MM-DDTHH:mm" in LOCAL time.
// Convert an ISO/RFC3339 instant to that, and back to a full ISO string.
export function isoToLocalInput(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function localInputToIso(v: string): string {
  if (!v) return ''
  const d = new Date(v) // interpreted as local time by the engine
  if (Number.isNaN(d.getTime())) return ''
  return d.toISOString()
}

// "now + n milliseconds" as a datetime-local value, for quick-select buttons.
export function nowPlus(ms: number): string {
  return isoToLocalInput(new Date(Date.now() + ms).toISOString())
}

// Shorten a long CEL expression for table cells.
export function excerpt(s: string | undefined, max = 48): string {
  if (!s) return ''
  return s.length > max ? s.slice(0, max - 1) + '…' : s
}
