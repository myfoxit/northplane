// App-specific components with no shadcn/ui equivalent, ported from the
// legacy ui.tsx / forms.tsx onto shadcn tokens + @/components/ui/* primitives.
// Same export names + prop signatures as the originals, so consumers migrate
// by only swapping the import path. Generic primitives (Button/Input/Card/
// Dialog/Tabs/Table/Badge/Toggle/TextArea) are intentionally NOT here —
// consumers use shadcn @/components/ui/* directly.
import { useState, type ReactNode } from 'react'
import { Loader2, X, AlertTriangle, RefreshCw } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { APIError } from '../api'
import { t } from '../i18n'
import { isDuration } from '@/lib/duration'

// Re-exports so both stay importable from '@/components/kit'. They live in
// their own modules to keep this file component-only (react-refresh-clean).
export { useSave } from '@/hooks/useSave'
export { isDuration } from '@/lib/duration'

// —— from ui.tsx ————————————————————————————————————————————————————

export function Spinner() {
  return (
    <div className="flex items-center justify-center p-4 text-muted-foreground">
      <Loader2 className="animate-spin" size={18} />
    </div>
  )
}

export function Empty({ text }: { text: string }) {
  return <div className="text-muted-foreground text-sm p-6 text-center">{text}</div>
}

// ErrorState: inline failure UI for a query that errored — mirrors Empty,
// surfaces the RFC 9457 detail from APIError, optional retry.
export function ErrorState({ error, onRetry, className }:
  { error?: unknown; onRetry?: () => void; className?: string }) {
  const msg = error instanceof APIError
    ? `${error.message}${error.detail ? ` — ${error.detail}` : ''}`
    : error instanceof Error ? error.message : (error ? String(error) : '')
  return (
    <div className={cn('flex flex-col items-center justify-center gap-2 p-6 text-center', className)}>
      <AlertTriangle className="text-danger" size={22} />
      <div className="text-sm font-medium text-foreground">{t('loadError')}</div>
      {msg && <div className="text-xs text-muted-foreground max-w-md break-words">{msg}</div>}
      {onRetry && (
        <Button size="sm" variant="outline" onClick={onRetry} className="mt-1">
          <RefreshCw size={13} /> {t('retry')}
        </Button>
      )}
    </div>
  )
}

export function Tile({ label, value, tone = 'default' }:
  { label: string; value: ReactNode; tone?: 'default' | 'ok' | 'warn' | 'crit' }) {
  const tones = {
    default: 'border-border',
    ok: 'border-success/30',
    warn: 'border-warning/30',
    crit: 'border-danger/30',
  }
  const valueTones = {
    default: 'text-foreground',
    ok: 'text-success',
    warn: 'text-warning',
    crit: 'text-danger',
  }
  return (
    <div className={cn('bg-card border rounded-xl px-4 py-3 shadow-sm', tones[tone])}>
      <div className="text-xs text-muted-foreground uppercase tracking-wider">{label}</div>
      <div className={cn('text-2xl font-bold mt-0.5 tabular-nums', valueTones[tone])}>{value}</div>
    </div>
  )
}

export function LabelChips({ labels }: { labels?: Record<string, string> }) {
  if (!labels || Object.keys(labels).length === 0) return null
  return (
    <span className="inline-flex flex-wrap gap-1">
      {Object.entries(labels).sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => (
        <span key={k} className="text-[11px] bg-muted text-muted-foreground rounded px-1.5 py-0.5 font-mono">
          {k}=<span className="text-foreground/90">{v}</span>
        </span>
      ))}
    </span>
  )
}

// —— from forms.tsx ————————————————————————————————————————————————————

// Field: label + control + optional hint/error, grid-friendly.
export function Field({ label, hint, error, required, children, className }: {
  label: ReactNode; hint?: string; error?: string; required?: boolean
  children: ReactNode; className?: string
}) {
  return (
    <div className={cn('block text-sm', className)}>
      <Label className="block text-xs leading-tight break-words text-muted-foreground font-medium">
        {label}{required && <span className="text-destructive"> *</span>}
      </Label>
      <div className="mt-1">{children}</div>
      {hint && !error && <span className="text-[11px] text-muted-foreground">{hint}</span>}
      {error && <span className="text-[11px] text-destructive">{error}</span>}
    </div>
  )
}

// Duration input: free text in Go syntax ("30s", "5m", "1h30m") with
// client-side validation (isDuration) matching internal/model Duration parsing.
export function DurationInput({ value, onChange, placeholder }: {
  value: string; onChange: (v: string) => void; placeholder?: string
}) {
  const ok = isDuration(value)
  return (
    <Input
      value={value} onChange={(e) => onChange(e.target.value)}
      placeholder={placeholder ?? '60s'}
      aria-invalid={!ok}
      className="font-mono"
    />
  )
}

// Key-value editor for labels / config / mapping maps.
export function KVEditor({ value, onChange, keyPlaceholder, valuePlaceholder }: {
  value: Record<string, string>; onChange: (v: Record<string, string>) => void
  keyPlaceholder?: string; valuePlaceholder?: string
}) {
  const [nk, setNk] = useState('')
  const [nv, setNv] = useState('')
  const entries = Object.entries(value)
  return (
    <div className="space-y-1.5">
      {entries.map(([k, v]) => (
        <div key={k} className="flex gap-1.5 items-center">
          <span className="font-mono text-xs text-muted-foreground bg-muted rounded px-2 py-1.5 min-w-24">{k}</span>
          <Input
            value={v}
            onChange={(e) => onChange({ ...value, [k]: e.target.value })}
            className="flex-1 h-8 font-mono"
          />
          <Button size="sm" variant="ghost" type="button" aria-label={t('remove')} onClick={() => {
            const next = { ...value }
            delete next[k]
            onChange(next)
          }}><X size={13} /></Button>
        </div>
      ))}
      <div className="flex gap-1.5">
        <Input
          value={nk} onChange={(e) => setNk(e.target.value)} placeholder={keyPlaceholder ?? 'key'}
          className="w-32 h-8 font-mono"
        />
        <Input
          value={nv} onChange={(e) => setNv(e.target.value)} placeholder={valuePlaceholder ?? 'value'}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && nk) {
              onChange({ ...value, [nk]: nv })
              setNk(''); setNv('')
            }
          }}
          className="flex-1 h-8 font-mono"
        />
        <Button size="sm" type="button" disabled={!nk} onClick={() => {
          onChange({ ...value, [nk]: nv })
          setNk(''); setNv('')
        }}>{t('add')}</Button>
      </div>
    </div>
  )
}

// String-list editor (templates, args, e-mail recipients, parents …).
export function ListEditor({ value, onChange, placeholder, suggestions }: {
  value: string[]; onChange: (v: string[]) => void
  placeholder?: string; suggestions?: string[]
}) {
  const [draft, setDraft] = useState('')
  const add = () => {
    const v = draft.trim()
    if (v && !value.includes(v)) onChange([...value, v])
    setDraft('')
  }
  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap gap-1">
        {value.map((v, i) => (
          <span key={v} className="inline-flex items-center gap-1 text-xs bg-muted text-foreground rounded-md px-2 py-1 font-mono">
            {v}
            <button
              type="button"
              className="text-muted-foreground hover:text-danger cursor-pointer"
              aria-label={t('remove')}
              onClick={() => onChange(value.filter((_, j) => j !== i))}
            ><X size={12} /></button>
          </span>
        ))}
      </div>
      <div className="flex gap-1.5">
        <Input
          value={draft} onChange={(e) => setDraft(e.target.value)} placeholder={placeholder}
          list={suggestions ? `sugg-${placeholder}` : undefined}
          onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); add() } }}
          className="flex-1 h-8 font-mono"
        />
        {suggestions && (
          <datalist id={`sugg-${placeholder}`}>
            {suggestions.map((s) => <option key={s} value={s} />)}
          </datalist>
        )}
        <Button size="sm" type="button" disabled={!draft.trim()} onClick={add}>{t('add')}</Button>
      </div>
    </div>
  )
}

// Form-level error banner with RFC 9457 detail.
export function FormError({ error }: { error: unknown }) {
  if (!error) return null
  const msg = error instanceof APIError
    ? `${error.message}${error.detail ? ` — ${error.detail}` : ''}`
    : String(error)
  return (
    <div className="text-sm text-destructive bg-destructive/10 border border-destructive/30 rounded-lg px-3 py-2">
      {msg}
    </div>
  )
}

// Standard save/cancel row.
export function SubmitRow({ onCancel, saving, label, disabled }: {
  onCancel?: () => void; saving?: boolean; label?: string; disabled?: boolean
}) {
  return (
    <div className="flex justify-end gap-2 pt-2">
      {onCancel && <Button variant="ghost" type="button" onClick={onCancel}>{t('cancel')}</Button>}
      <Button variant="default" type="submit" disabled={saving || disabled}>
        {saving ? <Loader2 className="animate-spin" size={14} /> : (label ?? t('save'))}
      </Button>
    </div>
  )
}

// Delete button with inline confirm (no native confirm(), ISO 9241 dialog
// principles: reversible steps surfaced, A-15.29ff).
export function DeleteButton({ onDelete, size = 'sm' }: { onDelete: () => void; size?: 'sm' | 'md' }) {
  const [arm, setArm] = useState(false)
  const btnSize = size === 'md' ? 'default' : 'sm'
  if (!arm) {
    return <Button size={btnSize} variant="ghost" type="button" onClick={() => setArm(true)}>{t('delete')}</Button>
  }
  return (
    <span className="inline-flex gap-1">
      <Button size={btnSize} variant="destructive" type="button" onClick={() => { setArm(false); onDelete() }}>{t('deleteConfirm')}</Button>
      <Button size={btnSize} variant="ghost" type="button" onClick={() => setArm(false)}>{t('cancel')}</Button>
    </span>
  )
}
