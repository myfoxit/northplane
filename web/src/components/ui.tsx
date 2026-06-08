// Vendored UI primitives in the shadcn style (SPEC §12.1: code in the
// repo, no component npm package — updates are deliberate diffs).
// Colours come from the semantic tokens in index.css (bg-card, text-
// muted-foreground, border-border, …); icons from lucide-react.
import {
  type ReactNode, type ButtonHTMLAttributes, type InputHTMLAttributes, useEffect, useRef,
} from 'react'
import { Loader2, X, AlertTriangle, RefreshCw } from 'lucide-react'
import { APIError } from '../api'
import { t } from '../i18n'

function cx(...parts: (string | false | undefined)[]): string {
  return parts.filter(Boolean).join(' ')
}

export function Button({ variant = 'default', size = 'md', className, ...props }:
  ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'default' | 'primary' | 'danger' | 'ghost'; size?: 'sm' | 'md' }) {
  const variants = {
    default: 'bg-muted hover:bg-accent border border-input text-foreground shadow-sm',
    primary: 'bg-primary hover:bg-primary/90 text-primary-foreground shadow-sm',
    danger: 'bg-destructive/90 hover:bg-destructive text-white shadow-sm',
    ghost: 'text-muted-foreground hover:bg-accent hover:text-foreground',
  }
  const sizes = { sm: 'px-2 py-1 text-xs rounded-md', md: 'px-3 py-1.5 text-sm rounded-lg' }
  return (
    <button
      className={cx('inline-flex items-center justify-center gap-1.5 font-medium transition-colors',
        'disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer',
        variants[variant], sizes[size], className)}
      {...props}
    />
  )
}

export function Input(props: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      {...props}
      className={cx(
        'bg-card border border-input rounded-lg px-3 py-1.5 text-sm text-foreground',
        'placeholder:text-muted-foreground focus:border-ring w-full',
        props.className,
      )}
    />
  )
}

export function Card({ title, children, className, actions }:
  { title?: ReactNode; children: ReactNode; className?: string; actions?: ReactNode }) {
  return (
    <div className={cx('bg-card border border-border rounded-xl shadow-sm', className)}>
      {(title || actions) && (
        <div className="flex items-center justify-between px-4 py-2.5 border-b border-border">
          <h2 className="text-sm font-semibold text-foreground">{title}</h2>
          {actions}
        </div>
      )}
      <div className="p-4">{children}</div>
    </div>
  )
}

export function Badge({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span className={cx('inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-xs font-medium border whitespace-nowrap', className)}>
      {children}
    </span>
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
    <div className={cx('bg-card border rounded-xl px-4 py-3 shadow-sm', tones[tone])}>
      <div className="text-xs text-muted-foreground uppercase tracking-wider">{label}</div>
      <div className={cx('text-2xl font-bold mt-0.5 tabular-nums', valueTones[tone])}>{value}</div>
    </div>
  )
}

export function Dialog({ open, onClose, title, children, size = 'md' }:
  { open: boolean; onClose: () => void; title: string; children: ReactNode; size?: 'md' | 'lg' | 'xl' }) {
  const panelRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    panelRef.current?.focus() // move focus into the dialog on open
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])
  if (!open) return null
  const widths = { md: 'max-w-md', lg: 'max-w-2xl', xl: 'max-w-4xl' }
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4 np-overlay-in" onClick={onClose}>
      <div
        ref={panelRef} tabIndex={-1}
        role="dialog" aria-modal="true" aria-label={title}
        className={cx('bg-card border border-border rounded-xl w-full shadow-2xl outline-none',
          'max-h-[90vh] overflow-y-auto np-content-in', widths[size])}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between gap-3 px-4 py-3 border-b border-border sticky top-0 bg-card rounded-t-xl z-10">
          <span className="text-sm font-semibold text-foreground">{title}</span>
          <button
            type="button" onClick={onClose} aria-label={t('close')}
            className="text-muted-foreground hover:text-foreground hover:bg-accent rounded-md p-1 -mr-1 cursor-pointer transition-colors"
          >
            <X size={16} />
          </button>
        </div>
        <div className="p-4">{children}</div>
      </div>
    </div>
  )
}

// Tabs: shared underline-style tab bar (Admin, Maintenance, Templates …).
export function TabBar<T extends string>({ tabs, value, onChange, labels }:
  { tabs: readonly T[]; value: T; onChange: (t: T) => void; labels: (t: T) => string }) {
  return (
    <div className="flex gap-1 border-b border-border mb-4 overflow-x-auto">
      {tabs.map((tb) => (
        <button
          key={tb} onClick={() => onChange(tb)}
          className={cx('px-3 py-2 text-sm whitespace-nowrap cursor-pointer transition-colors border-b-2 -mb-px',
            value === tb ? 'text-primary border-primary'
              : 'text-muted-foreground border-transparent hover:text-foreground')}
        >
          {labels(tb)}
        </button>
      ))}
    </div>
  )
}

export function Table({ head, children }: { head: string[]; children: ReactNode }) {
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left text-xs text-muted-foreground uppercase tracking-wider border-b border-border">
          {head.map((h) => <th key={h} className="px-3 py-2 font-medium">{h}</th>)}
        </tr>
      </thead>
      <tbody className="divide-y divide-border">{children}</tbody>
    </table>
  )
}

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
    <div className={cx('flex flex-col items-center justify-center gap-2 p-6 text-center', className)}>
      <AlertTriangle className="text-danger" size={22} />
      <div className="text-sm font-medium text-foreground">{t('loadError')}</div>
      {msg && <div className="text-xs text-muted-foreground max-w-md break-words">{msg}</div>}
      {onRetry && (
        <Button size="sm" onClick={onRetry} className="mt-1">
          <RefreshCw size={13} /> {t('retry')}
        </Button>
      )}
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
