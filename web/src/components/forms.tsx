// Shared form primitives for the management UIs (CMP-Admin/Wizard-Parität).
// Same vendored-shadcn philosophy as ui.tsx: plain Tailwind on the semantic
// tokens, no form lib — controlled components + useMutation at the call site.
import { useState, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { X, Loader2 } from 'lucide-react'
import { t } from '../i18n'
import { APIError } from '../api'
import { Button } from './ui'

function cx(...parts: (string | false | undefined)[]): string {
  return parts.filter(Boolean).join(' ')
}

// Field: label + control + optional hint/error, grid-friendly.
export function Field({ label, hint, error, required, children, className }: {
  label: ReactNode; hint?: string; error?: string; required?: boolean
  children: ReactNode; className?: string
}) {
  return (
    <label className={cx('block text-sm', className)}>
      <span className="text-xs text-muted-foreground font-medium">
        {label}{required && <span className="text-danger"> *</span>}
      </span>
      <div className="mt-1">{children}</div>
      {hint && !error && <span className="text-[11px] text-muted-foreground">{hint}</span>}
      {error && <span className="text-[11px] text-danger">{error}</span>}
    </label>
  )
}

export function Select({ className, children, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      {...props}
      className={cx(
        'bg-card border border-input rounded-lg px-3 py-1.5 text-sm text-foreground w-full',
        'focus:border-ring cursor-pointer', className,
      )}
    >
      {children}
    </select>
  )
}

export function TextArea({ className, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      {...props}
      className={cx(
        'bg-card border border-input rounded-lg px-3 py-1.5 text-sm text-foreground w-full font-mono',
        'placeholder:text-muted-foreground focus:border-ring min-h-20', className,
      )}
    />
  )
}

export function Toggle({ checked, onChange, label }: {
  checked: boolean; onChange: (v: boolean) => void; label?: string
}) {
  return (
    <button
      type="button" role="switch" aria-checked={checked}
      onClick={() => onChange(!checked)}
      className="inline-flex items-center gap-2 cursor-pointer group"
    >
      <span className={cx('w-8 h-4.5 rounded-full transition-colors relative',
        checked ? 'bg-primary' : 'bg-input')}>
        <span className={cx('absolute top-0.5 w-3.5 h-3.5 rounded-full bg-white transition-transform',
          checked ? 'translate-x-4' : 'translate-x-0.5')} />
      </span>
      {label && <span className="text-sm text-foreground/90 group-hover:text-foreground">{label}</span>}
    </button>
  )
}

// Duration input: free text in Go syntax ("30s", "5m", "1h30m") with
// client-side validation matching internal/model Duration parsing.
const goDuration = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/
export function isDuration(v: string): boolean {
  return v === '' || goDuration.test(v)
}
export function DurationInput({ value, onChange, placeholder }: {
  value: string; onChange: (v: string) => void; placeholder?: string
}) {
  const ok = isDuration(value)
  return (
    <input
      value={value} onChange={(e) => onChange(e.target.value)}
      placeholder={placeholder ?? '60s'}
      className={cx(
        'bg-card border rounded-lg px-3 py-1.5 text-sm text-foreground w-full font-mono',
        'placeholder:text-muted-foreground',
        ok ? 'border-input focus:border-ring' : 'border-destructive/70',
      )}
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
          <input
            value={v}
            onChange={(e) => onChange({ ...value, [k]: e.target.value })}
            className="flex-1 bg-card border border-input rounded-lg px-2 py-1 text-sm font-mono text-foreground focus:border-ring"
          />
          <Button size="sm" variant="ghost" aria-label={t('remove')} onClick={() => {
            const next = { ...value }
            delete next[k]
            onChange(next)
          }}><X size={13} /></Button>
        </div>
      ))}
      <div className="flex gap-1.5">
        <input
          value={nk} onChange={(e) => setNk(e.target.value)} placeholder={keyPlaceholder ?? 'key'}
          className="w-32 bg-card border border-input rounded-lg px-2 py-1 text-sm font-mono text-foreground focus:border-ring"
        />
        <input
          value={nv} onChange={(e) => setNv(e.target.value)} placeholder={valuePlaceholder ?? 'value'}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && nk) {
              onChange({ ...value, [nk]: nv })
              setNk(''); setNv('')
            }
          }}
          className="flex-1 bg-card border border-input rounded-lg px-2 py-1 text-sm font-mono text-foreground focus:border-ring"
        />
        <Button size="sm" disabled={!nk} onClick={() => {
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
              className="text-muted-foreground hover:text-danger cursor-pointer"
              aria-label={t('remove')}
              onClick={() => onChange(value.filter((_, j) => j !== i))}
            ><X size={12} /></button>
          </span>
        ))}
      </div>
      <div className="flex gap-1.5">
        <input
          value={draft} onChange={(e) => setDraft(e.target.value)} placeholder={placeholder}
          list={suggestions ? `sugg-${placeholder}` : undefined}
          onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); add() } }}
          className="flex-1 bg-card border border-input rounded-lg px-2 py-1 text-sm font-mono text-foreground focus:border-ring"
        />
        {suggestions && (
          <datalist id={`sugg-${placeholder}`}>
            {suggestions.map((s) => <option key={s} value={s} />)}
          </datalist>
        )}
        <Button size="sm" disabled={!draft.trim()} onClick={add}>{t('add')}</Button>
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
    <div className="text-sm text-danger bg-destructive/10 border border-destructive/30 rounded-lg px-3 py-2">
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
      <Button variant="primary" type="submit" disabled={saving || disabled}>
        {saving ? <Loader2 className="animate-spin" size={14} /> : (label ?? t('save'))}
      </Button>
    </div>
  )
}

// useSave: uniform mutation wrapper — invalidates query keys, surfaces
// the APIError for FormError, closes the dialog on success.
export function useSave<TArgs>(fn: (args: TArgs) => Promise<unknown>, opts: {
  invalidate: readonly (readonly string[])[]; onDone?: () => void
}) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      for (const key of opts.invalidate) qc.invalidateQueries({ queryKey: key as string[] })
      opts.onDone?.()
    },
  })
}

// Delete button with inline confirm (no native confirm(), ISO 9241 dialog
// principles: reversible steps surfaced, A-15.29ff).
export function DeleteButton({ onDelete, size = 'sm' }: { onDelete: () => void; size?: 'sm' | 'md' }) {
  const [arm, setArm] = useState(false)
  if (!arm) {
    return <Button size={size} variant="ghost" onClick={() => setArm(true)}>{t('delete')}</Button>
  }
  return (
    <span className="inline-flex gap-1">
      <Button size={size} variant="danger" onClick={() => { setArm(false); onDelete() }}>{t('deleteConfirm')}</Button>
      <Button size={size} variant="ghost" onClick={() => setArm(false)}>{t('cancel')}</Button>
    </span>
  )
}
