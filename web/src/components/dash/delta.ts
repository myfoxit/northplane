// Change-since-the-last-change for the Overview KPI badges.
//
// This replaces the old sparkline history (spark.ts). That series only ever
// accumulated samples for as long as the tab stayed open — nothing is stored
// server-side and unchanged polls were deduped — so on a stable board the line
// at the bottom of every stat card was a flat, meaningless stroke. The one
// genuinely useful thing it fed was the delta badge, which needs a single
// previous value, so that is all this keeps.
//
// State lives in a module-level map so it survives re-renders; the write
// happens in an effect (post-commit), never during render. The Overview
// re-renders on every poll, so a new delta shows on the following render —
// invisible at a 30s cadence.
import { useEffect } from 'react'

const CUR = new Map<string, number>()
const PREV = new Map<string, number>()

// useDelta returns the most recent change of `value` (0 until it has moved
// once). Unchanged samples are ignored, so the badge keeps showing the last
// real movement instead of blanking on the next poll.
export function useDelta(key: string, value: number | undefined): number {
  useEffect(() => {
    if (value == null || !Number.isFinite(value)) return
    const cur = CUR.get(key)
    if (cur === value) return
    if (cur != null) PREV.set(key, cur)
    CUR.set(key, value)
  }, [key, value])

  const cur = CUR.get(key)
  const prev = PREV.get(key)
  return cur != null && prev != null ? cur - prev : 0
}
