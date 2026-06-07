// Vendored UI primitives in the shadcn style (SPEC §12.1: code in the
// repo, no component npm package — updates are deliberate diffs).
import { type ReactNode, type ButtonHTMLAttributes, type InputHTMLAttributes, useEffect } from 'react'

function cx(...parts: (string | false | undefined)[]): string {
  return parts.filter(Boolean).join(' ')
}

export function Button({ variant = 'default', size = 'md', className, ...props }:
  ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'default' | 'primary' | 'danger' | 'ghost'; size?: 'sm' | 'md' }) {
  const variants = {
    default: 'bg-slate-800 hover:bg-slate-700 border border-slate-700 text-slate-200',
    primary: 'bg-blue-600 hover:bg-blue-500 text-white',
    danger: 'bg-red-600/80 hover:bg-red-500 text-white',
    ghost: 'hover:bg-slate-800 text-slate-300',
  }
  const sizes = { sm: 'px-2 py-1 text-xs rounded-md', md: 'px-3 py-1.5 text-sm rounded-lg' }
  return (
    <button
      className={cx('font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer',
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
        'bg-slate-900 border border-slate-700 rounded-lg px-3 py-1.5 text-sm text-slate-200',
        'placeholder:text-slate-500 focus:outline-none focus:border-blue-500 w-full',
        props.className,
      )}
    />
  )
}

export function Card({ title, children, className, actions }:
  { title?: ReactNode; children: ReactNode; className?: string; actions?: ReactNode }) {
  return (
    <div className={cx('bg-slate-900/60 border border-slate-800 rounded-xl', className)}>
      {(title || actions) && (
        <div className="flex items-center justify-between px-4 py-2.5 border-b border-slate-800">
          <h2 className="text-sm font-semibold text-slate-300">{title}</h2>
          {actions}
        </div>
      )}
      <div className="p-4">{children}</div>
    </div>
  )
}

export function Badge({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span className={cx('inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-xs font-medium border', className)}>
      {children}
    </span>
  )
}

export function Tile({ label, value, tone = 'default' }:
  { label: string; value: ReactNode; tone?: 'default' | 'ok' | 'warn' | 'crit' }) {
  const tones = {
    default: 'border-slate-800',
    ok: 'border-emerald-900/60',
    warn: 'border-amber-900/60',
    crit: 'border-red-900/60',
  }
  const valueTones = {
    default: 'text-slate-100',
    ok: 'text-emerald-400',
    warn: 'text-amber-400',
    crit: 'text-red-400',
  }
  return (
    <div className={cx('bg-slate-900/60 border rounded-xl px-4 py-3', tones[tone])}>
      <div className="text-xs text-slate-500 uppercase tracking-wider">{label}</div>
      <div className={cx('text-2xl font-bold mt-0.5 tabular-nums', valueTones[tone])}>{value}</div>
    </div>
  )
}

export function Dialog({ open, onClose, title, children }:
  { open: boolean; onClose: () => void; title: string; children: ReactNode }) {
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])
  if (!open) return null
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" onClick={onClose}>
      <div
        role="dialog" aria-modal="true" aria-label={title}
        className="bg-slate-900 border border-slate-700 rounded-xl w-full max-w-md shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="px-4 py-3 border-b border-slate-800 text-sm font-semibold text-slate-200">{title}</div>
        <div className="p-4">{children}</div>
      </div>
    </div>
  )
}

export function Table({ head, children }: { head: string[]; children: ReactNode }) {
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left text-xs text-slate-500 uppercase tracking-wider border-b border-slate-800">
          {head.map((h) => <th key={h} className="px-3 py-2 font-medium">{h}</th>)}
        </tr>
      </thead>
      <tbody className="divide-y divide-slate-800/70">{children}</tbody>
    </table>
  )
}

export function Spinner() {
  return <div className="animate-pulse text-slate-500 text-sm p-4">…</div>
}

export function Empty({ text }: { text: string }) {
  return <div className="text-slate-500 text-sm p-6 text-center">{text}</div>
}

export function LabelChips({ labels }: { labels?: Record<string, string> }) {
  if (!labels || Object.keys(labels).length === 0) return null
  return (
    <span className="inline-flex flex-wrap gap-1">
      {Object.entries(labels).sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => (
        <span key={k} className="text-[11px] bg-slate-800/80 text-slate-400 rounded px-1.5 py-0.5 font-mono">
          {k}=<span className="text-slate-300">{v}</span>
        </span>
      ))}
    </span>
  )
}
