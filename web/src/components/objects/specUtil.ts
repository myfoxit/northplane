// Non-component helpers for the ObjectSpec editor — the resource-name query
// hooks and the spec (de)serialisation helpers. Split out of SpecFields.tsx
// so that file can stay component-only (react-refresh/only-export-components
// clean — Fast Refresh needs a module to export components OR helpers, not a
// mix).
import { useQuery } from '@tanstack/react-query'
import { get } from '../../api'
import type { ObjectSpec } from '../../types'

export function useBuiltins() {
  return useQuery({
    queryKey: ['check-commands', 'builtins'],
    queryFn: () => get<string[]>('/check-commands:builtins'),
    staleTime: 5 * 60_000,
  })
}

// Names of a resource collection (templates / time-periods / hosts) for
// datalist suggestions.
export function useResourceNames(base: string, queryKey: string[]) {
  return useQuery({
    queryKey,
    queryFn: () => get<{ items: { name: string }[] | null }>(`/${base}?limit=500`)
      .then((r) => (r.items ?? []).map((x) => x.name)),
    staleTime: 60_000,
  })
}

// ——— serialisation helpers ———————————————————————————————————————
// Strip empty strings / empty arrays / empty maps so the wire payload only
// carries deliberate overrides (omitempty parity → keeps effective-config
// inheritance clean). undefined fields are dropped by JSON.stringify.
export function cleanSpec(spec: ObjectSpec): ObjectSpec {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(spec)) {
    if (v === undefined || v === null || v === '') continue
    if (Array.isArray(v) && v.length === 0) continue
    if (typeof v === 'object' && !Array.isArray(v) && Object.keys(v as object).length === 0) continue
    out[k] = v
  }
  return out as ObjectSpec
}

// A NPObject.spec is typed as Record<string, unknown>; narrow it for the
// editor. Returns a shallow copy so edits never mutate the cache.
export function specOf(raw: Record<string, unknown> | undefined): ObjectSpec {
  return { ...(raw as ObjectSpec | undefined) }
}
