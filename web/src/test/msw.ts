// MSW server for component tests: a setupServer() with default handlers for
// the /api/v1/* endpoints the component tests use. Individual tests override
// handlers via server.use(...) (e.g. to assert empty / 500 / error paths).
import { setupServer } from 'msw/node'
import { http, HttpResponse } from 'msw'
import type { ProblemRow } from '../types'

// A couple of canned ProblemRows so the default success path renders rows.
export const sampleProblems: ProblemRow[] = [
  {
    object: {
      id: 'obj-1',
      tenantId: 't1',
      kind: 'service',
      name: 'http',
      hostName: 'web-01',
      folder: '/',
      labels: { env: 'prod' },
      spec: {},
      version: 1,
    },
    state: {
      objectId: 'obj-1',
      state: 2, // CRITICAL
      stateType: 'hard',
      attempt: 3,
      output: 'HTTP 500 from backend',
      flapping: false,
      downtimeDepth: 0,
      lastHardChange: new Date(Date.now() - 5 * 60_000).toISOString(),
    },
  },
  {
    object: {
      id: 'obj-2',
      tenantId: 't1',
      kind: 'host',
      name: 'db-01',
      folder: '/',
      labels: {},
      spec: {},
      version: 1,
    },
    state: {
      objectId: 'obj-2',
      state: 1, // DOWN (host)
      stateType: 'hard',
      attempt: 1,
      output: 'no route to host',
      flapping: false,
      downtimeDepth: 0,
      lastHardChange: new Date(Date.now() - 60 * 60_000).toISOString(),
    },
  },
]

// Default handlers: a non-empty problems list and an empty open-alerts list.
// Tests that need other behavior call server.use(...) to override.
export const handlers = [
  http.get('/api/v1/problems', () =>
    HttpResponse.json({ items: sampleProblems }),
  ),
  http.get('/api/v1/alerts', () => HttpResponse.json({ items: [] })),
  http.get('/api/v1/events', () => HttpResponse.json({ items: [] })),
]

export const server = setupServer(...handlers)
