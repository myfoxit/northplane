// Runtime-validation boundary (zod). The hand-written interfaces in types.ts
// are the source of truth for the *shape*; these schemas validate that the
// data crossing the network boundary actually matches that shape at runtime.
//
// The highest-value target is the dashboard document: its `spec` is
// frontend-owned JSON, round-tripped through storage as an opaque blob
// (model.Dashboard.Spec), so it's the most likely payload to come back
// malformed (hand-edited, partially migrated, written by an older client).
// Validating it at the read boundary turns "silent wrong render / crash deep
// in a widget" into one clear error.
//
// Each schema is cross-checked against the corresponding hand-written type via
// an `Expect<Equal<…>>` assertion at the bottom, so the schema and the TS type
// can't silently drift apart.

import * as z from 'zod'
import type {
  DashboardWidget,
  DashboardDoc,
  Alert,
  NPObject,
  CheckState,
  Overview,
  Incident,
} from './types'

// —— shared leaf schemas ———————————————————————————————————————————————

const severitySchema = z.enum(['critical', 'warning', 'info', 'ok'])
const alertStatusSchema = z.enum(['open', 'acked', 'resolved', 'expired'])
const kindSchema = z.enum(['host', 'service'])
const stateTypeSchema = z.enum(['soft', 'hard'])

const stringRecord = z.record(z.string(), z.string())

// —— DashboardWidget / DashboardDoc (primary target) ——————————————————————

export const dashboardWidgetSchema = z.object({
  type: z.enum([
    'counters', 'problems', 'metric', 'bpi', 'markdown', 'alerts',
    'gauge', 'donut', 'bar', 'table',
  ]),
  title: z.string().optional(),
  object: z.string().optional(),
  metric: z.string().optional(),
  range: z.string().optional(),
  max: z.number().optional(),
  scope: z.enum(['services', 'hosts']).optional(),
  limit: z.number().optional(),
  selector: z.string().optional(),
  query: z.string().optional(),
  service: z.string().optional(),
  text: z.string().optional(),
  w: z.number().optional(),
  h: z.number().optional(),
})

export const dashboardDocSchema = z.object({
  id: z.string().optional(),
  name: z.string(),
  shared: z.boolean().optional(),
  spec: z.object({
    widgets: z.array(dashboardWidgetSchema),
  }),
  shareToken: z.string().optional(),
  version: z.number().optional(),
})

// —— Example read models (a few, not every endpoint) ——————————————————————

export const alertSchema = z.object({
  id: z.string(),
  status: alertStatusSchema,
  severity: severitySchema,
  title: z.string(),
  dedupKey: z.string().optional(),
  objectId: z.string().optional(),
  incidentId: z.string().optional(),
  labels: stringRecord.optional(),
  openedAt: z.string(),
  ackedAt: z.string().optional(),
  ackedBy: z.string().optional(),
  resolvedAt: z.string().optional(),
})

const checkStateSchema = z.object({
  objectId: z.string(),
  state: z.number(),
  stateType: stateTypeSchema,
  attempt: z.number(),
  output: z.string().optional(),
  longOutput: z.string().optional(),
  perfdata: z.string().optional(),
  lastCheck: z.string().optional(),
  nextCheck: z.string().optional(),
  lastHardChange: z.string().optional(),
  flapping: z.boolean(),
  ackedBy: z.string().optional(),
  ackComment: z.string().optional(),
  downtimeDepth: z.number(),
})

export const npObjectSchema = z.object({
  id: z.string(),
  tenantId: z.string(),
  kind: kindSchema,
  name: z.string(),
  hostId: z.string().optional(),
  hostName: z.string().optional(),
  folder: z.string(),
  labels: stringRecord,
  spec: z.record(z.string(), z.unknown()),
  version: z.number(),
  state: checkStateSchema.nullable().optional(),
})

const incidentSchema = z.object({
  id: z.string(),
  status: z.enum(['open', 'resolved']),
  severity: severitySchema,
  title: z.string(),
  summary: z.string().optional(),
  impact: z.string().optional(),
  ticketUrl: z.string().optional(),
  createdBy: z.string(),
  openedAt: z.string(),
  version: z.number(),
})

export const overviewSchema = z.object({
  summary: z.object({
    hostsUp: z.number(),
    hostsDown: z.number(),
    hostsUnreachable: z.number(),
    servicesOk: z.number(),
    servicesWarning: z.number(),
    servicesCritical: z.number(),
    servicesUnknown: z.number(),
    acked: z.number(),
    inDowntime: z.number(),
    flapping: z.number(),
  }),
  openAlerts: z.record(z.string(), z.number()),
  openIncidents: z.array(incidentSchema),
})

// —— Type-level alignment guards ————————————————————————————————————————
// These never run; they fail the typecheck if a schema and its hand-written
// type drift apart. `z.infer` may differ from the interface only in optional
// vs `| undefined` nuances — the Equal helper treats `?:` and `| undefined`
// as equivalent for our purposes via the structural comparison below.

type Equal<A, B> =
  (<T>() => T extends A ? 1 : 2) extends (<T>() => T extends B ? 1 : 2) ? true : false
type Expect<T extends true> = T

// Each tuple member is `true` only if the schema's inferred type equals the
// hand-written interface; any drift becomes a compile error here. Exported so
// it counts as "used" under noUnusedLocals and is reachable from tests.
export type SchemaTypeChecks = [
  Expect<Equal<z.infer<typeof dashboardWidgetSchema>, DashboardWidget>>,
  Expect<Equal<z.infer<typeof dashboardDocSchema>, DashboardDoc>>,
  Expect<Equal<z.infer<typeof alertSchema>, Alert>>,
  Expect<Equal<z.infer<typeof checkStateSchema>, CheckState>>,
  Expect<Equal<z.infer<typeof npObjectSchema>, NPObject>>,
  Expect<Equal<z.infer<typeof incidentSchema>, Incident>>,
  Expect<Equal<z.infer<typeof overviewSchema>, Overview>>,
]
