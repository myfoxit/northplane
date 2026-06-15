import { describe, it, expect } from 'vitest'
import {
  GRID_COLS, clampItem, collides, compact, moveItem, resizeItem,
  layoutBottom, hasPositions, flowLayout, toLayout, type GridItem,
} from './grid'

const it_ = (i: number, x: number, y: number, w: number, h: number): GridItem => ({ i, x, y, w, h })

// byIndex sorts a layout deterministically for comparison.
const byIndex = (l: GridItem[]) => [...l].sort((a, b) => a.i - b.i)

// noOverlap asserts the layout has no two overlapping rectangles.
function noOverlap(l: GridItem[]): boolean {
  for (let a = 0; a < l.length; a++)
    for (let b = a + 1; b < l.length; b++)
      if (collides(l[a]!, l[b]!)) return false
  return true
}

describe('clampItem', () => {
  it('keeps a valid item unchanged', () => {
    expect(clampItem(it_(0, 3, 2, 4, 2))).toEqual(it_(0, 3, 2, 4, 2))
  })
  it('caps width at the column count and pulls x in so it fits', () => {
    expect(clampItem(it_(0, 10, 0, 20, 1))).toEqual(it_(0, 0, 0, GRID_COLS, 1))
  })
  it('clamps negative/overflowing x and floors y at 0', () => {
    expect(clampItem(it_(0, 11, -5, 6, 1))).toEqual(it_(0, 6, 0, 6, 1))
  })
  it('forces w/h to at least 1', () => {
    expect(clampItem(it_(0, 0, 0, 0, 0))).toEqual(it_(0, 0, 0, 1, 1))
  })
})

describe('collides', () => {
  it('detects overlap', () => {
    expect(collides(it_(0, 0, 0, 4, 2), it_(1, 2, 1, 4, 2))).toBe(true)
  })
  it('treats touching edges as non-colliding', () => {
    expect(collides(it_(0, 0, 0, 4, 2), it_(1, 4, 0, 4, 2))).toBe(false) // side by side
    expect(collides(it_(0, 0, 0, 4, 2), it_(1, 0, 2, 4, 2))).toBe(false) // stacked
  })
  it('an item never collides with itself', () => {
    const a = it_(3, 0, 0, 4, 2)
    expect(collides(a, { ...a })).toBe(false)
  })
})

describe('compact (gravity)', () => {
  it('pulls a floating item up to y=0', () => {
    const out = compact([it_(0, 0, 5, 4, 2)])
    expect(out[0]!.y).toBe(0)
  })
  it('stacks two same-column items with no gap', () => {
    const out = byIndex(compact([it_(0, 0, 0, 4, 2), it_(1, 0, 10, 4, 2)]))
    expect(out[1]!.y).toBe(2)
  })
  it('leaves side-by-side items both at the top', () => {
    const out = byIndex(compact([it_(0, 0, 9, 6, 2), it_(1, 6, 4, 6, 2)]))
    expect(out[0]!.y).toBe(0)
    expect(out[1]!.y).toBe(0)
    expect(noOverlap(out)).toBe(true)
  })
  it('never produces overlaps', () => {
    const out = compact([
      it_(0, 0, 0, 6, 2), it_(1, 0, 1, 6, 2), it_(2, 3, 0, 6, 2),
    ])
    expect(noOverlap(out)).toBe(true)
  })
  it('pins one item and packs the rest around it', () => {
    // item 1 pinned at y=4; item 0 should sit above it, item 2 below.
    const out = byIndex(compact(
      [it_(0, 0, 6, 12, 2), it_(1, 0, 4, 12, 2), it_(2, 0, 0, 12, 2)],
      1,
    ))
    expect(out[1]!.y).toBe(4) // pinned, unchanged
    expect(noOverlap(out)).toBe(true)
    expect(out[0]!.y + out[1]!.y + out[2]!.y).toBeGreaterThan(0)
  })
})

describe('moveItem', () => {
  it('moves an item and pushes a collided neighbour out of the way', () => {
    const base = [it_(0, 0, 0, 6, 2), it_(1, 6, 0, 6, 2)]
    const out = byIndex(moveItem(base, 1, 0, 0)) // drop item 1 onto item 0
    expect(noOverlap(out)).toBe(true)
    expect(out[1]!.x).toBe(0)
    expect(out[1]!.y).toBe(0) // pinned where dropped
    expect(out[0]!.y).toBe(2) // shoved below
  })
  it('clamps an out-of-bounds target', () => {
    const out = moveItem([it_(0, 0, 0, 6, 2)], 0, 99, -5)
    expect(out[0]!.x).toBe(GRID_COLS - 6)
    expect(out[0]!.y).toBe(0)
  })
})

describe('resizeItem', () => {
  it('grows an item and reflows the rest', () => {
    const base = [it_(0, 0, 0, 4, 1), it_(1, 0, 1, 4, 1)]
    const out = byIndex(resizeItem(base, 0, 4, 3))
    expect(out[0]!.h).toBe(3)
    expect(out[1]!.y).toBe(3) // pushed below the taller item 0
    expect(noOverlap(out)).toBe(true)
  })
  it('pulls x left when the new width would overflow the right edge', () => {
    const out = resizeItem([it_(0, 9, 0, 3, 1)], 0, 6, 1)
    expect(out[0]!.x).toBe(GRID_COLS - 6)
  })
})

describe('layoutBottom', () => {
  it('is the max y+h', () => {
    expect(layoutBottom([it_(0, 0, 0, 4, 2), it_(1, 4, 3, 4, 2)])).toBe(5)
  })
  it('is 0 for an empty layout', () => {
    expect(layoutBottom([])).toBe(0)
  })
})

describe('hasPositions', () => {
  it('is false when any widget lacks x/y', () => {
    expect(hasPositions([{ x: 0, y: 0 }, { w: 6 }])).toBe(false)
  })
  it('is false for an empty list', () => {
    expect(hasPositions([])).toBe(false)
  })
  it('is true when every widget has numeric x and y', () => {
    expect(hasPositions([{ x: 0, y: 0 }, { x: 6, y: 0 }])).toBe(true)
  })
})

describe('flowLayout (migration)', () => {
  it('packs widgets left-to-right and wraps at the column count', () => {
    const out = byIndex(flowLayout([{ w: 6 }, { w: 6 }, { w: 6 }]))
    expect(out[0]!).toMatchObject({ x: 0, y: 0 })
    expect(out[1]!).toMatchObject({ x: 6, y: 0 })
    expect(out[2]!).toMatchObject({ x: 0, y: 1 }) // wrapped below row 1 (h defaults to 1)
    expect(noOverlap(out)).toBe(true)
  })
  it('defaults missing width to half the grid', () => {
    expect(flowLayout([{}])[0]!.w).toBe(6)
  })
})

describe('toLayout', () => {
  it('flows widgets that have no positions yet (back-compat)', () => {
    const out = byIndex(toLayout([{ w: 12, h: 1 }, { w: 6, h: 2 }]))
    expect(out[0]!).toMatchObject({ x: 0, y: 0, w: 12 })
    expect(out[1]!).toMatchObject({ x: 0, y: 1 })
  })
  it('honours stored positions when every widget has them', () => {
    const out = byIndex(toLayout([
      { x: 6, y: 0, w: 6, h: 2 }, { x: 0, y: 0, w: 6, h: 2 },
    ]))
    expect(out[0]!.x).toBe(6)
    expect(out[1]!.x).toBe(0)
    expect(noOverlap(out)).toBe(true)
  })
})
