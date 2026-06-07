// API types mirroring the Go domain model (SPEC §6).

export type Kind = 'host' | 'service'
export type StateType = 'soft' | 'hard'
export type Severity = 'critical' | 'warning' | 'info' | 'ok'
export type AlertStatus = 'open' | 'acked' | 'resolved' | 'expired'

export interface CheckState {
  objectId: string
  state: number
  stateType: StateType
  attempt: number
  output?: string
  longOutput?: string
  perfdata?: string
  lastCheck?: string
  nextCheck?: string
  lastHardChange?: string
  flapping: boolean
  ackedBy?: string
  ackComment?: string
  downtimeDepth: number
}

export interface NPObject {
  id: string
  tenantId: string
  kind: Kind
  name: string
  hostId?: string
  hostName?: string
  folder: string
  labels: Record<string, string>
  spec: Record<string, unknown>
  version: number
  state?: CheckState | null
}

export interface Alert {
  id: string
  status: AlertStatus
  severity: Severity
  title: string
  dedupKey?: string
  objectId?: string
  incidentId?: string
  labels?: Record<string, string>
  openedAt: string
  ackedAt?: string
  ackedBy?: string
  resolvedAt?: string
}

export interface Incident {
  id: string
  status: 'open' | 'resolved'
  severity: Severity
  title: string
  summary?: string
  impact?: string
  ticketUrl?: string
  createdBy: string
  openedAt: string
  version: number
}

export interface NPEvent {
  id: string
  ts: string
  type: string
  objectId?: string
  sourceId?: string
  severity?: Severity
  payload: Record<string, unknown>
}

export interface Overview {
  summary: {
    hostsUp: number; hostsDown: number; hostsUnreachable: number
    servicesOk: number; servicesWarning: number; servicesCritical: number
    servicesUnknown: number; acked: number; inDowntime: number; flapping: number
  }
  openAlerts: Record<string, number>
  openIncidents: Incident[]
}

export interface ProblemRow {
  object: NPObject
  state: CheckState
}

export interface OnCallNow {
  schedule: string
  shifts: { contactId: string; start: string; end: string; override?: boolean }[]
  contacts: { id: string; name: string; email?: string; phone?: string }[]
}

export interface SeriesResult {
  series: { id: number; objectId: string; metric: string; unit?: string; warn?: string; crit?: string }
  points: { t: number; v: number }[]
}

export interface AIAction {
  id: string
  tool: string
  args: Record<string, unknown>
  summary?: string
  status: 'proposed' | 'approved' | 'denied' | 'executed' | 'failed'
  actor: string
  createdAt: string
}

export interface RuleTestResult {
  matched: number
  wouldOpen: Alert[] | null
  sampleViews?: Record<string, unknown>[]
}

export const svcStates = ['OK', 'WARNING', 'CRITICAL', 'UNKNOWN'] as const
export const hostStates = ['UP', 'DOWN', 'UNREACHABLE', 'UNKNOWN'] as const

export function stateLabel(kind: Kind, state: number): string {
  const arr = kind === 'host' ? hostStates : svcStates
  return arr[state] ?? 'UNKNOWN'
}

// Status is never color-only (A-15.29ff): each state carries an icon.
export function stateIcon(kind: Kind, state: number): string {
  if (kind === 'host') return ['●', '✕', '◌', '?'][state] ?? '?'
  return ['●', '▲', '✕', '?'][state] ?? '?'
}

export function stateColor(kind: Kind, state: number): string {
  if (kind === 'host') return ['text-emerald-400', 'text-red-400', 'text-slate-400', 'text-slate-400'][state] ?? ''
  return ['text-emerald-400', 'text-amber-400', 'text-red-400', 'text-slate-400'][state] ?? ''
}

export function sevColor(sev?: Severity): string {
  switch (sev) {
    case 'critical': return 'bg-red-500/15 text-red-400 border-red-500/30'
    case 'warning': return 'bg-amber-500/15 text-amber-400 border-amber-500/30'
    case 'ok': return 'bg-emerald-500/15 text-emerald-400 border-emerald-500/30'
    default: return 'bg-sky-500/15 text-sky-400 border-sky-500/30'
  }
}
