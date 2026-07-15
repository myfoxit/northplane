// DualListPicker: the classic Windows-style two-pane "transfer list" control.
// The left pane lists selectable options not yet chosen; the right pane holds
// the current selection. Items move with the ›/»/«/‹ buttons or a double-click,
// and each pane has its own filter box so long catalogs stay searchable.
//
// Used for the multi-value reference fields on hosts/services — contacts,
// contact-groups, templates, parents — where an operator picks several existing
// entries at once. Selection order on the right pane is preserved (append on
// add), matching the "later template wins" semantics of the spec chain.
//
// allowCustom keeps parity with the old free-text ListEditor: a value typed
// into the left filter that isn't in the fetched option set can still be added
// (Enter, or the "+ add" affordance), so referencing a not-yet-listed name
// never becomes impossible.
import { useMemo, useState } from 'react'
import { ChevronRight, ChevronsRight, ChevronLeft, ChevronsLeft, Plus } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { t } from '../i18n'

export function DualListPicker({
  value, onChange, options, availableLabel, selectedLabel, allowCustom = true,
}: {
  value: string[]
  onChange: (v: string[]) => void
  options: string[]
  availableLabel?: string
  selectedLabel?: string
  allowCustom?: boolean
}) {
  const [availFilter, setAvailFilter] = useState('')
  const [selFilter, setSelFilter] = useState('')
  // "marked" = clicked/highlighted items awaiting a › / ‹ move.
  const [availMarked, setAvailMarked] = useState<Set<string>>(() => new Set())
  const [selMarked, setSelMarked] = useState<Set<string>>(() => new Set())

  const selectedSet = useMemo(() => new Set(value), [value])
  // Coerce to strings up front: options is typed string[], but a caller can
  // still hand us the wrong shape (e.g. a shared React-Query cache slot that
  // resolved to objects). Normalising here keeps every downstream string op
  // (localeCompare, toLowerCase) from throwing and taking the page down.
  const opts = useMemo(() => (options ?? []).map((o) => String(o)), [options])
  // Available universe = options minus what's already selected, de-duplicated
  // and alphabetised (the selected pane keeps insertion order instead).
  const available = useMemo(() => {
    const uniq = Array.from(new Set(opts)).filter((o) => !selectedSet.has(o))
    uniq.sort((a, b) => a.localeCompare(b))
    return uniq
  }, [opts, selectedSet])

  const availShown = useMemo(
    () => available.filter((o) => o.toLowerCase().includes(availFilter.trim().toLowerCase())),
    [available, availFilter],
  )
  const selShown = useMemo(
    () => value.filter((o) => o.toLowerCase().includes(selFilter.trim().toLowerCase())),
    [value, selFilter],
  )

  const add = (items: string[]) => {
    const fresh = items.filter((i) => !selectedSet.has(i))
    if (fresh.length) onChange([...value, ...fresh])
    setAvailMarked(new Set())
  }
  const remove = (items: string[]) => {
    if (!items.length) return
    const drop = new Set(items)
    onChange(value.filter((v) => !drop.has(v)))
    setSelMarked(new Set())
  }
  // Enter / "+ add" in the left filter: adopt a typed value that isn't a known
  // option (allowCustom), else move the exact match if the filter names one.
  const addTyped = () => {
    const v = availFilter.trim()
    if (!v) return
    if (available.includes(v)) { add([v]); setAvailFilter('') }
    else if (allowCustom && !selectedSet.has(v)) { onChange([...value, v]); setAvailFilter('') }
  }
  const canAddTyped = allowCustom && availFilter.trim() !== ''
    && !selectedSet.has(availFilter.trim()) && !available.includes(availFilter.trim())

  const toggle = (marked: Set<string>, setMarked: (s: Set<string>) => void, item: string) => {
    const next = new Set(marked)
    if (next.has(item)) next.delete(item)
    else next.add(item)
    setMarked(next)
  }

  return (
    <div className="flex items-stretch gap-2">
      <Pane
        title={availableLabel ?? t('available')}
        count={available.length}
        filter={availFilter}
        onFilter={setAvailFilter}
        onFilterEnter={addTyped}
        items={availShown}
        marked={availMarked}
        onToggle={(it) => toggle(availMarked, setAvailMarked, it)}
        onActivate={(it) => add([it])}
        footer={canAddTyped ? (
          <button
            type="button" onClick={addTyped}
            className="flex w-full items-center gap-1 px-2 py-1.5 text-xs text-primary hover:bg-primary/10 border-t border-border"
          >
            <Plus size={12} /> {t('addValue')}: <span className="font-mono">{availFilter.trim()}</span>
          </button>
        ) : null}
      />

      {/* move controls — the › » « ‹ the ask calls out */}
      <div className="flex flex-col justify-center gap-1.5 shrink-0">
        <MoveBtn label="›"  onClick={() => add([...availMarked])}          disabled={availMarked.size === 0} icon={ChevronRight} />
        <MoveBtn label="»"  onClick={() => add(availShown)}               disabled={availShown.length === 0} icon={ChevronsRight} />
        <MoveBtn label="«"  onClick={() => remove(selShown)}              disabled={selShown.length === 0} icon={ChevronsLeft} />
        <MoveBtn label="‹"  onClick={() => remove([...selMarked])}         disabled={selMarked.size === 0} icon={ChevronLeft} />
      </div>

      <Pane
        title={selectedLabel ?? t('selected')}
        count={value.length}
        filter={selFilter}
        onFilter={setSelFilter}
        items={selShown}
        marked={selMarked}
        onToggle={(it) => toggle(selMarked, setSelMarked, it)}
        onActivate={(it) => remove([it])}
        mono
      />
    </div>
  )
}

function Pane({
  title, count, filter, onFilter, onFilterEnter, items, marked, onToggle, onActivate, footer, mono,
}: {
  title: string; count: number
  filter: string; onFilter: (v: string) => void; onFilterEnter?: () => void
  items: string[]; marked: Set<string>
  onToggle: (item: string) => void; onActivate: (item: string) => void
  footer?: React.ReactNode; mono?: boolean
}) {
  return (
    <div className="flex-1 min-w-0 flex flex-col border border-border rounded-lg bg-card/40 overflow-hidden">
      <div className="flex items-center justify-between px-2 py-1.5 border-b border-border bg-muted/40">
        <span className="text-xs font-semibold text-muted-foreground uppercase tracking-wider truncate">{title}</span>
        <span className="text-[11px] text-muted-foreground tabular-nums shrink-0">{count}</span>
      </div>
      <div className="p-1.5 border-b border-border">
        <Input
          value={filter}
          onChange={(e) => onFilter(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter' && onFilterEnter) { e.preventDefault(); onFilterEnter() } }}
          placeholder={t('filterEllipsis')}
          className="h-7 text-xs"
        />
      </div>
      <ul className="flex-1 overflow-auto min-h-[8rem] max-h-52">
        {items.length === 0 && (
          <li className="px-2 py-3 text-center text-[11px] text-muted-foreground">{t('nothingFound')}</li>
        )}
        {items.map((it) => (
          <li key={it}>
            <button
              type="button"
              onClick={() => onToggle(it)}
              onDoubleClick={() => onActivate(it)}
              title={it}
              className={cn(
                'w-full text-left px-2 py-1 text-xs truncate cursor-pointer transition-colors',
                mono && 'font-mono',
                marked.has(it)
                  ? 'bg-primary/20 text-primary'
                  : 'text-foreground/90 hover:bg-muted/60',
              )}
            >
              {it}
            </button>
          </li>
        ))}
      </ul>
      {footer}
    </div>
  )
}

function MoveBtn({ onClick, disabled, icon: Icon, label }: {
  onClick: () => void; disabled: boolean
  icon: React.ComponentType<{ size?: number }>; label: string
}) {
  return (
    <Button
      type="button" size="sm" variant="outline"
      className="h-7 w-8 p-0" onClick={onClick} disabled={disabled} aria-label={label}
    >
      <Icon size={14} />
    </Button>
  )
}
