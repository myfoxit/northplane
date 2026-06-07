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

// ——— Resource documents (named CRUD, ETag-versioned) — mirror internal/model ———

export interface ObjectSpec {
  address?: string
  templates?: string[]
  parents?: string[]
  checkCommand?: string
  args?: string[]
  interval?: string          // Go duration ("30s", "5m")
  retryInterval?: string
  maxCheckAttempts?: number
  timeout?: string
  checkPeriod?: string
  notificationPeriod?: string
  // Direct notification routing (Nagios contact_groups semantics):
  contacts?: string[]
  contactGroups?: string[]
  notifyOn?: string[] // warning|critical|unknown|down|unreachable|recovery
  enableNotifications?: boolean
  enableChecks?: boolean
  enableFlapDetection?: boolean
  flapThresholdLow?: number
  flapThresholdHigh?: number
  stalenessAfter?: string
  stalenessText?: string
  thresholdMode?: 'static' | 'adaptive'
  zone?: string
  runbook?: string
  vars?: Record<string, string>
}

export interface Template {
  id?: string
  kind?: Kind
  name: string
  labels?: Record<string, string>
  spec: ObjectSpec
  version?: number
}

export interface CheckCommand {
  id?: string
  name: string
  type: 'exec' | 'builtin' | 'agent' | 'passive'
  line: string[]
  env?: boolean
  timeout?: string
  version?: number
}

export interface TimePeriod {
  id?: string
  name: string
  alias?: string
  days?: Record<string, string[]>
  exceptions?: Record<string, string[]>
  exclude?: string[]
  version?: number
}

export interface AlertRule {
  id?: string
  name: string
  disabled?: boolean
  match?: string
  heartbeat?: { source: string; expectEvery: string }
  pendingFor?: string
  dedupKey?: string
  severity: Severity
  title?: string
  autoCloseAfter?: string
  resolveOnOk?: boolean
  escalationPolicy?: string
  groupId?: string
  setLabels?: Record<string, string>
  incident?: boolean
  version?: number
}

export interface AlertGroup {
  id?: string
  name: string
  groupBy: string[]
  window: string
  aggregate?: 'count' | 'min' | 'max' | 'avg' | 'sum' | 'median'
  valuePath?: string
  minCount?: number
  version?: number
}

export type ChannelType = 'email' | 'webhook' | 'slack' | 'teams' | 'ntfy' | 'sms' | 'push' | 'voice'
  | 'servicenow' | 'zendesk' | 'jira' | 'ticket'

export interface Channel {
  id?: string
  name: string
  type: ChannelType
  enabled: boolean
  config: Record<string, string>
  template?: string
  version?: number
}

export interface ChannelPreference {
  profile: string
  period?: string
  channels: ChannelType[]
  severity?: Severity
}

export interface Contact {
  id?: string
  name: string
  email?: string
  phone?: string
  userId?: string
  timeZone?: string
  preferences?: ChannelPreference[]
  version?: number
}

export interface ContactGroup {
  id?: string
  name: string
  members: string[]
  idpGroup?: string
  version?: number
}

export interface EscalationStep {
  after: string
  unlessAcked?: boolean
  notify?: { schedule?: string; escalateTo?: string; contact?: string; contactGroup?: string }
  channels?: ChannelType[]
  repeatEvery?: string
  maxRepeats?: number
  action?: { webhook?: string; servicenow?: { assignmentGroup: string; autoClose: boolean } }
}

export interface EscalationPolicy {
  id?: string
  name: string
  steps: EscalationStep[]
  version?: number
}

export interface Rotation {
  name?: string
  participants: string[]
  unit: 'daily' | 'weekly' | 'custom'
  length?: string
  anchor: string
  restriction?: Record<string, string[]>
}

export interface Schedule {
  id?: string
  name: string
  timeZone: string
  layers: Rotation[]
  version?: number
}

export interface ScheduleOverride {
  id?: string
  scheduleId?: string
  contactId: string
  start: string
  end: string
  reason?: string
}

export interface Silence {
  id?: string
  selector?: string
  textRegex?: string
  comment: string
  createdBy?: string
  startsAt?: string
  expiresAt: string
  version?: number
}

export interface Downtime {
  id?: string
  objectId?: string
  selector?: string
  type: 'fixed' | 'flexible'
  start: string
  end: string
  duration?: string
  rrule?: string
  comment: string
  createdBy?: string
  version?: number
}

export interface User {
  id?: string
  name: string
  email: string
  subject?: string
  local?: boolean
  roles?: string[]
  disabled?: boolean
  lastSeenAt?: string
  version?: number
}

export interface Role {
  id?: string
  name: string
  permissions: string[]
  scope?: { tenantId?: string; folder?: string; selector?: string }
  includes?: string[]
  idpGroups?: string[]
  system?: boolean
  version?: number
}

export interface EventSourceDef {
  id?: string
  name: string
  type: string // webhook | alertmanager | snmp-trap | imap | email | …
  enabled: boolean
  authMode?: 'token' | 'hmac' | 'basic' | 'none'
  secretRef?: string
  mapping?: Record<string, string>
  config?: Record<string, string>
  rateLimit?: number
  burst?: number
  labels?: Record<string, string>
  version?: number
}

export interface BusinessService {
  id?: string
  name: string
  parentId?: string
  rule?: string
  quorumPct?: number
  objectId?: string
  selector?: string
  weight?: number
  slaTarget?: number
  slaWindow?: 'month' | 'quarter' | 'year'
  excludeDowntimes?: boolean
  version?: number
}

export type ReportType = 'availability' | 'sla' | 'alert-stats' | 'oncall' | 'audit'

export interface Report {
  id?: string
  name: string
  type: ReportType
  params?: Record<string, unknown>
  schedule?: string // "daily[@HH:MM]" | "weekly:monday" | "monthly[:day]"
  email?: string[]
  keep?: number
  version?: number
}

// Dashboard spec is frontend-owned JSON (SPEC §12.3 / model.Dashboard.Spec).
export interface DashboardWidget {
  type: 'counters' | 'problems' | 'metric' | 'bpi' | 'markdown' | 'alerts'
    | 'gauge' | 'donut' | 'bar' | 'table'
  title?: string
  // metric/gauge/bar widget:
  object?: string
  metric?: string
  range?: string // "3h", "24h", "7d"
  // gauge widget:
  max?: number   // scale end, default 100 (or auto from data)
  // donut/table widget:
  scope?: 'services' | 'hosts'
  // problems/alerts/bar/table widget:
  limit?: number
  selector?: string
  // table widget: free-text filter (matches name/output)
  query?: string
  // bpi widget:
  service?: string
  // markdown widget:
  text?: string
  // grid placement (12-col grid):
  w?: number
  h?: number
}

export interface DashboardDoc {
  id?: string
  name: string
  shared?: boolean
  spec: { widgets: DashboardWidget[] }
  shareToken?: string
  version?: number
}

// Wire shape of internal/api/discovery.go WebhookSubscription
// (outgoing event webhooks, SPEC §11.5).
export interface WebhookSub {
  name: string
  url: string
  types?: string[]
  selector?: string
  secret?: string
  disabled?: boolean
  version?: number
}

// Wire shape of internal/model.Heartbeat (dead-man inputs, F-02.02).
export interface HeartbeatDef {
  id?: string
  name: string
  expectEvery: string
  grace?: string
  severity?: Severity
  labels?: Record<string, string>
  lastBeat?: string
  missing?: boolean
}

// Wire shape of internal/api/discovery.go.
export interface DiscoveryScan {
  id: string
  status: 'running' | 'done' | 'failed'
  cidr: string
  ports?: number[] | null
  startedAt: string
  doneAt?: string
  error?: string
  found?: {
    address: string
    hostname?: string
    openPorts: number[] | null
    suggest: string[] | null
  }[] | null
}

// URL search-param shapes of the filterable list routes (drill-down
// targets — keys must stay optional so plain links don't need them).
export interface ObjectsSearch {
  selector?: string
  q?: string
  state?: string
  kind?: 'host' | 'service'
}

export interface AlertsSearch {
  status?: string
  severity?: string
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
