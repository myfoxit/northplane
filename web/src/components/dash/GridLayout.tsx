// GridLayout — the draggable/resizable 12-column dashboard grid (Grafana-style).
// Absolute-positions each panel from its {x,y,w,h} grid cell; in edit mode a
// panel can be dragged by its header and resized from the bottom-right grip,
// with the rest of the grid reflowing live (gravity) and a placeholder marking
// the snapped target. The packing maths live in grid.ts (pure, unit-tested);
// this component only turns pointer gestures into move/resize calls and paints.
//
// Gestures use the Pointer Capture API: the element the gesture starts on
// (header or grip) captures the pointer, so every subsequent move/up fires on
// that same element regardless of where the cursor goes — no window listeners
// or render-time refs (the new react-hooks compiler lints forbid the latter).
import { useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import {
  GRID_COLS, ROW_HEIGHT, GRID_MARGIN, layoutBottom, moveItem, resizeItem, type GridItem,
} from './grid'
import { t } from '../../i18n'

// Props the consumer spreads onto its panel header to make it a drag handle.
export interface DragHandleProps {
  onPointerDown: (e: React.PointerEvent) => void
  onPointerMove: (e: React.PointerEvent) => void
  onPointerUp: (e: React.PointerEvent) => void
  onPointerCancel: (e: React.PointerEvent) => void
  style: React.CSSProperties
}

interface Gesture {
  mode: 'move' | 'resize'
  i: number
  startX: number
  startY: number
  base: GridItem[]        // layout snapshot at gesture start (stable reflow base)
  baseItem: GridItem      // the dragged item at gesture start
  colW: number
  offX: number            // live pixel offset (move) / size delta (resize)
  offY: number
  preview: GridItem[]     // reflowed layout for the current pointer position
}

const cellRect = (it: GridItem, colW: number) => ({
  left: it.x * (colW + GRID_MARGIN),
  top: it.y * (ROW_HEIGHT + GRID_MARGIN),
  width: it.w * colW + (it.w - 1) * GRID_MARGIN,
  height: it.h * ROW_HEIGHT + (it.h - 1) * GRID_MARGIN,
})

export function GridLayout({ layout, editing, onChange, renderItem }: {
  layout: GridItem[]
  editing: boolean
  onChange: (next: GridItem[]) => void
  renderItem: (item: GridItem, handle: DragHandleProps | null) => ReactNode
}) {
  const ref = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(0)
  const [gesture, setGesture] = useState<Gesture | null>(null)

  // Track the container's content width → column width.
  useLayoutEffect(() => {
    if (!ref.current) return
    const el = ref.current
    const measure = () => setWidth(el.clientWidth)
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const colW = width > 0 ? (width - (GRID_COLS - 1) * GRID_MARGIN) / GRID_COLS : 0
  const effective = gesture ? gesture.preview : layout
  const rows = layoutBottom(effective)
  const containerH = rows > 0 ? rows * ROW_HEIGHT + (rows - 1) * GRID_MARGIN : 0

  const onDown = (mode: 'move' | 'resize', item: GridItem) => (e: React.PointerEvent) => {
    if (!editing || colW === 0) return
    e.preventDefault()
    e.stopPropagation()
    e.currentTarget.setPointerCapture?.(e.pointerId)
    setGesture({
      mode, i: item.i, startX: e.clientX, startY: e.clientY,
      base: layout, baseItem: item, colW, offX: 0, offY: 0, preview: layout,
    })
  }
  // Move/up read the latest gesture: move via a functional update, up via the
  // (event-handler) closure — both legal outside render.
  const onMove = (e: React.PointerEvent) => {
    const clientX = e.clientX, clientY = e.clientY
    setGesture((g) => {
      if (!g) return g
      const dx = clientX - g.startX
      const dy = clientY - g.startY
      const dCols = Math.round(dx / (g.colW + GRID_MARGIN))
      const dRows = Math.round(dy / (ROW_HEIGHT + GRID_MARGIN))
      return g.mode === 'move'
        ? { ...g, offX: dx, offY: dy, preview: moveItem(g.base, g.i, g.baseItem.x + dCols, g.baseItem.y + dRows) }
        : { ...g, offX: dx, offY: dy, preview: resizeItem(g.base, g.i, g.baseItem.w + dCols, g.baseItem.h + dRows) }
    })
  }
  const onUp = () => {
    if (gesture) onChange(gesture.preview)
    setGesture(null)
  }
  const handlerProps = {
    onPointerMove: onMove, onPointerUp: onUp, onPointerCancel: onUp,
  }

  return (
    <div ref={ref} className="relative w-full" style={{ height: containerH }}>
      {/* placeholder: where the active panel will land */}
      {gesture && colW > 0 && (() => {
        const target = gesture.preview.find((it) => it.i === gesture.i)
        if (!target) return null
        const r = cellRect(target, colW)
        return (
          <div
            className="absolute rounded-xl border-2 border-dashed border-primary/50 bg-primary/10 transition-all duration-75 pointer-events-none"
            style={{ left: r.left, top: r.top, width: r.width, height: r.height }}
          />
        )
      })()}

      {colW > 0 && effective.map((item) => {
        const active = gesture?.i === item.i
        const ag = active ? gesture! : null
        const r = cellRect(ag ? ag.baseItem : item, colW)
        // The active panel follows the cursor (pixel offset / live size); the
        // others animate to their reflowed cells.
        const style: React.CSSProperties = ag
          ? ag.mode === 'move'
            ? { left: r.left, top: r.top, width: r.width, height: r.height, transform: `translate(${ag.offX}px, ${ag.offY}px)`, zIndex: 30 }
            : { left: r.left, top: r.top, width: Math.max(colW, r.width + ag.offX), height: Math.max(ROW_HEIGHT, r.height + ag.offY), zIndex: 30 }
          : { left: r.left, top: r.top, width: r.width, height: r.height }
        const handle: DragHandleProps | null = editing
          ? {
            onPointerDown: onDown('move', item),
            ...handlerProps,
            style: { cursor: ag?.mode === 'move' ? 'grabbing' : 'grab', touchAction: 'none' },
          }
          : null
        return (
          <div
            key={item.i}
            className={`absolute ${ag ? 'select-none' : 'transition-all duration-150'}`}
            style={style}
          >
            <div className={`relative h-full ${ag ? 'opacity-90 shadow-2xl ring-2 ring-primary/40 rounded-xl' : ''}`}>
              {renderItem(item, handle)}
              {editing && (
                <div
                  onPointerDown={onDown('resize', item)}
                  {...handlerProps}
                  className="absolute -bottom-0.5 -right-0.5 w-5 h-5 cursor-nwse-resize z-20 text-muted-foreground/60 hover:text-primary"
                  style={{ touchAction: 'none' }}
                  title={t('resize')}
                  aria-label={t('resize')}
                >
                  <svg viewBox="0 0 16 16" className="w-full h-full pointer-events-none">
                    <path d="M15 5 L5 15 M15 10 L10 15 M15 15 L14 15" stroke="currentColor" strokeWidth="1.5" fill="none" strokeLinecap="round" />
                  </svg>
                </div>
              )}
            </div>
          </div>
        )
      })}
    </div>
  )
}
