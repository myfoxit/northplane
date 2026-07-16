// Live sparkline history: a short rolling series per key that accumulates real
// samples across Overview polls (no synthetic points). Kept in a module-level
// map so it survives re-renders; appends happen in an effect (post-commit), so
// nothing mutates during render. The Overview re-renders every poll, so the
// line stays current (a new sample shows on the following render — invisible at
// a 30s cadence).
import { useEffect } from 'react'

const HIST = new Map<string, number[]>()
const CAP = 24

export function useSparkHistory(key: string, value: number | undefined): number[] {
  useEffect(() => {
    if (value == null || !Number.isFinite(value)) return
    const arr = HIST.get(key) ?? []
    if (arr.length && arr[arr.length - 1] === value) return // dedup unchanged samples
    HIST.set(key, [...arr, value].slice(-CAP))
  }, [key, value])
  // Seed with the current value so the first paint already has a baseline line.
  return HIST.get(key) ?? (value != null ? [value] : [])
}
