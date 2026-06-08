import { describe, it, expect } from 'vitest'
import {
  dashboardWidgetSchema,
  dashboardDocSchema,
  alertSchema,
  npObjectSchema,
  overviewSchema,
} from './schemas'

const validDoc = {
  name: 'Prod overview',
  shared: true,
  version: 3,
  spec: {
    widgets: [
      { type: 'counters' },
      { type: 'gauge', title: 'CPU', object: 'obj-1', metric: 'cpu', range: '3h', max: 100 },
      { type: 'table', scope: 'services', limit: 25, query: 'web', w: 6, h: 4 },
      { type: 'markdown', text: '# Notes' },
    ],
  },
}

describe('dashboardDocSchema', () => {
  it('accepts a fully populated valid document', () => {
    const r = dashboardDocSchema.safeParse(validDoc)
    expect(r.success).toBe(true)
  })

  it('accepts a minimal document (name + empty widgets)', () => {
    expect(dashboardDocSchema.safeParse({ name: 'd', spec: { widgets: [] } }).success).toBe(true)
  })

  it('rejects a document missing the required name', () => {
    const r = dashboardDocSchema.safeParse({ spec: { widgets: [] } })
    expect(r.success).toBe(false)
  })

  it('rejects a document with a malformed widget (unknown type)', () => {
    const bad = { name: 'd', spec: { widgets: [{ type: 'pie-chart-3000' }] } }
    const r = dashboardDocSchema.safeParse(bad)
    expect(r.success).toBe(false)
    if (!r.success) {
      expect(r.error.issues.some((i) => i.path.includes('widgets'))).toBe(true)
    }
  })

  it('rejects a widget whose numeric field is the wrong type', () => {
    const bad = { name: 'd', spec: { widgets: [{ type: 'gauge', max: 'lots' }] } }
    expect(dashboardDocSchema.safeParse(bad).success).toBe(false)
  })

  it('rejects when spec.widgets is not an array', () => {
    expect(dashboardDocSchema.safeParse({ name: 'd', spec: { widgets: {} } }).success).toBe(false)
  })

  it('rejects a non-object payload', () => {
    expect(dashboardDocSchema.safeParse(null).success).toBe(false)
    expect(dashboardDocSchema.safeParse('not json').success).toBe(false)
  })
})

describe('dashboardWidgetSchema', () => {
  it('accepts every widget type with no extra fields', () => {
    for (const type of [
      'counters', 'problems', 'metric', 'bpi', 'markdown',
      'alerts', 'gauge', 'donut', 'bar', 'table',
    ]) {
      expect(dashboardWidgetSchema.safeParse({ type }).success).toBe(true)
    }
  })

  it('rejects a widget with no type', () => {
    expect(dashboardWidgetSchema.safeParse({ title: 'x' }).success).toBe(false)
  })

  it('rejects an invalid scope value', () => {
    expect(dashboardWidgetSchema.safeParse({ type: 'donut', scope: 'pods' }).success).toBe(false)
  })
})

describe('example read-model schemas', () => {
  it('alertSchema accepts a valid alert and rejects a bad status', () => {
    const ok = {
      id: 'a1', status: 'open', severity: 'critical', title: 'down', openedAt: '2026-06-08T00:00:00Z',
    }
    expect(alertSchema.safeParse(ok).success).toBe(true)
    expect(alertSchema.safeParse({ ...ok, status: 'snoozed' }).success).toBe(false)
    expect(alertSchema.safeParse({ ...ok, severity: 'fatal' }).success).toBe(false)
  })

  it('npObjectSchema accepts an object with and without nested state', () => {
    const base = {
      id: 'o1', tenantId: 't1', kind: 'service', name: 'http',
      folder: '/', labels: {}, spec: {}, version: 1,
    }
    expect(npObjectSchema.safeParse(base).success).toBe(true)
    expect(npObjectSchema.safeParse({ ...base, state: null }).success).toBe(true)
    expect(npObjectSchema.safeParse({
      ...base,
      state: {
        objectId: 'o1', state: 2, stateType: 'hard', attempt: 1,
        flapping: false, downtimeDepth: 0,
      },
    }).success).toBe(true)
    expect(npObjectSchema.safeParse({ ...base, kind: 'router' }).success).toBe(false)
  })

  it('overviewSchema requires the full summary block', () => {
    const ok = {
      summary: {
        hostsUp: 1, hostsDown: 0, hostsUnreachable: 0, servicesOk: 5, servicesWarning: 1,
        servicesCritical: 0, servicesUnknown: 0, acked: 0, inDowntime: 0, flapping: 0,
      },
      openAlerts: { critical: 2 },
      openIncidents: [],
    }
    expect(overviewSchema.safeParse(ok).success).toBe(true)
    // drop a required summary field -> must fail
    const partialSummary: Record<string, number> = { ...ok.summary }
    delete partialSummary.hostsUp
    expect(overviewSchema.safeParse({ ...ok, summary: partialSummary }).success).toBe(false)
  })
})
