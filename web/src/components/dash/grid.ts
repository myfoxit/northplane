// Pure 12-column grid-layout engine for dashboards (Grafana-style free layout).
// Widgets carry {x,y,w,h} in grid units; these helpers place, move, resize and
// gravity-compact them with collision resolution. Pure (no React/DOM) so the
// packing algorithm is unit-tested directly (grid.test.ts).
//
// Coordinate model: x ∈ [0, COLS), y ≥ 0 (row units, unbounded), w ≥ 1, h ≥ 1.
// "Gravity" pulls every item toward y=0, packing the grid like Grafana/RGL.

export const GRID_COLS = 12

// Pixel geometry shared by the renderer (GridLayout.tsx). One row unit ≈ a KPI
// tile; the margin matches the old gap-3 (12px) so existing dashboards keep
// their visual rhythm after migration.
export const ROW_HEIGHT = 120
export const GRID_MARGIN = 12

// A layout item is a widget's rectangle tagged with its index in the widgets
// array, so a reordered/compacted layout can be merged back by index.
export interface GridItem {
  i: number
  x: number
  y: number
  w: number
  h: number
}

const clampInt = (n: number, lo: number, hi: number) =>
  Math.max(lo, Math.min(hi, Math.round(Number.isFinite(n) ? n : lo)))

// clampItem keeps a rectangle inside the grid: 1 ≤ w ≤ COLS, h ≥ 1, x so the
// item never overflows the right edge, y ≥ 0.
export function clampItem(it: GridItem): GridItem {
  const w = clampInt(it.w || 1, 1, GRID_COLS)
  const h = Math.max(1, Math.round(it.h || 1))
  const x = clampInt(it.x || 0, 0, GRID_COLS - w)
  const y = Math.max(0, Math.round(it.y || 0))
  return { ...it, x, y, w, h }
}

// collides: do two rectangles overlap? (touching edges do not count).
export function collides(a: GridItem, b: GridItem): boolean {
  if (a.i === b.i) return false
  return a.x < b.x + b.w && a.x + a.w > b.x && a.y < b.y + b.h && a.y + a.h > b.y
}

function firstCollision(placed: GridItem[], it: GridItem): GridItem | undefined {
  for (const p of placed) if (collides(p, it)) return p
  return undefined
}

// sortByPosition: top-to-bottom, then left-to-right — the order gravity packs in.
function sortByPosition(items: GridItem[]): GridItem[] {
  return [...items].sort((a, b) => a.y - b.y || a.x - b.x || a.i - b.i)
}

// gravity drops a single item as far up as it can go without colliding with any
// already-placed item, then pushes it back down past any it still overlaps.
function gravity(placed: GridItem[], item: GridItem): GridItem {
  const it = { ...item }
  while (it.y > 0 && !firstCollision(placed, { ...it, y: it.y - 1 })) it.y--
  let hit: GridItem | undefined
  while ((hit = firstCollision(placed, it))) it.y = hit.y + hit.h
  return it
}

// compact gravity-packs every item toward the top. `pinned` (an index) keeps
// its position fixed and acts as an obstacle the rest pack around — used during
// a drag/resize so the active item stays under the cursor while others reflow.
export function compact(layout: GridItem[], pinned?: number): GridItem[] {
  const placed: GridItem[] = []
  const pin = pinned != null ? layout.find((it) => it.i === pinned) : undefined
  if (pin) placed.push({ ...pin })
  for (const item of sortByPosition(layout)) {
    if (pin && item.i === pinned) continue
    placed.push(gravity(placed, item))
  }
  return placed.sort((a, b) => a.i - b.i)
}

// moveItem places item `i` at (x,y), pins it there, and reflows the rest.
export function moveItem(layout: GridItem[], i: number, x: number, y: number): GridItem[] {
  const next = layout.map((it) =>
    it.i === i ? clampItem({ ...it, x, y }) : { ...it },
  )
  return compact(next, i)
}

// resizeItem sets item `i`'s span to (w,h) (clamped so it stays on-grid; x is
// pulled left if the new width would overflow), pins it, and reflows the rest.
export function resizeItem(layout: GridItem[], i: number, w: number, h: number): GridItem[] {
  const next = layout.map((it) =>
    it.i === i ? clampItem({ ...it, w, h }) : { ...it },
  )
  return compact(next, i)
}

// layoutBottom: the number of row units the layout occupies (max y+h). 0 when
// empty — the renderer uses this to size the grid container.
export function layoutBottom(layout: GridItem[]): number {
  return layout.reduce((max, it) => Math.max(max, it.y + it.h), 0)
}

// ——— migration: widgets saved before free positioning have only w/h ———

// hasPositions: true once every widget carries numeric x AND y, i.e. the doc
// was authored/saved with free positioning and needs no migration.
export function hasPositions(widgets: { x?: number; y?: number }[]): boolean {
  return widgets.length > 0 && widgets.every(
    (w) => typeof w.x === 'number' && typeof w.y === 'number',
  )
}

// flowLayout assigns x/y to widgets in array order, packing left-to-right and
// wrapping at COLS — reproducing the old document-flow grid so an upgraded
// dashboard looks unchanged until the user drags something.
export function flowLayout(widgets: { w?: number; h?: number }[]): GridItem[] {
  let x = 0, y = 0, rowH = 0
  const out: GridItem[] = []
  widgets.forEach((wd, i) => {
    const w = clampInt(wd.w || 6, 1, GRID_COLS)
    const h = Math.max(1, Math.round(wd.h || 1))
    if (x + w > GRID_COLS) { x = 0; y += rowH; rowH = 0 }
    out.push({ i, x, y, w, h })
    x += w
    rowH = Math.max(rowH, h)
  })
  return compact(out)
}

// toLayout derives the working layout from widgets: their stored x/y when the
// whole doc has them, else a freshly flowed layout (migration). Always
// clamped + compacted so a hand-edited / partial doc can't render overlapping.
export function toLayout(widgets: { x?: number; y?: number; w?: number; h?: number }[]): GridItem[] {
  if (!hasPositions(widgets)) return flowLayout(widgets)
  const items = widgets.map((wd, i) => clampItem({
    i, x: wd.x ?? 0, y: wd.y ?? 0, w: wd.w ?? 6, h: wd.h ?? 1,
  }))
  return compact(items)
}
