// Active-customer context for the CMP multi-customer console.
//
// An operator with the admin:tenants permission can switch the "active
// customer" from the sidebar (TenantSwitcher). The selected tenant id is
// persisted to localStorage and injected as the X-Northplane-Tenant header on
// every API call (see api.ts), so the whole UI — every module — operates on
// that customer's isolated instance. Cleared (null) = the operator's own
// tenant, i.e. no header, backend falls back to the token's tenant.
//
// This module is deliberately framework-light and imports nothing from api.ts:
// api.ts reads activeTenantId() with no React/queryClient coupling, so there
// is no import cycle. Resetting the React Query cache on a switch is the
// caller's job (TenantSwitcher already holds the queryClient) — keeping it out
// of here is what avoids the cycle.

import { useSyncExternalStore } from 'react'

const KEY = 'np.activeTenant'

const listeners = new Set<() => void>()

// activeTenantId reads the persisted active customer id, or null for the
// operator's own tenant. Safe to call from non-React code (api.ts).
export function activeTenantId(): string | null {
  try {
    return window.localStorage.getItem(KEY) || null
  } catch {
    return null
  }
}

// setActiveTenantId switches the active customer (null clears back to the
// operator's own tenant) and notifies subscribers so the UI re-renders.
// Callers holding the React Query client should clear it afterwards so one
// customer's cached data never bleeds into another's view.
export function setActiveTenantId(id: string | null): void {
  try {
    if (id) window.localStorage.setItem(KEY, id)
    else window.localStorage.removeItem(KEY)
  } catch {
    // ignore storage failures (private mode etc.) — the header just won't persist
  }
  listeners.forEach((l) => l())
}

function subscribe(cb: () => void): () => void {
  listeners.add(cb)
  return () => {
    listeners.delete(cb)
  }
}

// useActiveTenant is the reactive view of the active customer id (or null).
export function useActiveTenant(): string | null {
  return useSyncExternalStore(subscribe, activeTenantId, () => null)
}
