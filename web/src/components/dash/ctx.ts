// Dashboard view context: the dashboard-level time range + auto-refresh that
// every widget obeys (Grafana's top-right time picker / refresh). Kept in a
// pure .ts module (no component exports) so widgets.tsx stays component-only
// and react-refresh is happy. The Provider used is `DashViewCtx.Provider`.
import { createContext, useContext } from 'react'

export interface DashView {
  // Range token ("1h".."30d") overriding a time-series widget's own range.
  // undefined → each widget keeps its configured range.
  range?: string
  // Auto-refresh interval in ms; 0 = off.
  refreshMs: number
}

export const REFRESH_TOKENS: { value: string; label: string; ms: number }[] = [
  { value: 'off', label: 'Aus', ms: 0 },
  { value: '10s', label: '10 s', ms: 10_000 },
  { value: '30s', label: '30 s', ms: 30_000 },
  { value: '1m', label: '1 min', ms: 60_000 },
  { value: '5m', label: '5 min', ms: 300_000 },
]

export const RANGE_TOKENS = ['1h', '3h', '6h', '12h', '24h', '7d', '30d']

export function refreshMsFor(token?: string): number {
  return REFRESH_TOKENS.find((r) => r.value === token)?.ms ?? 30_000
}

// Default: 30s refresh, no range override (legacy behaviour).
export const DashViewCtx = createContext<DashView>({ refreshMs: 30_000 })

export function useDashView(): DashView {
  return useContext(DashViewCtx)
}
