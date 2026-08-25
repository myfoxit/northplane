// Shared building blocks for the system-administration tabs
// (CMP-Admin-Parität, SPEC §12.3). Consistent CRUD UX everywhere:
// a Table list with status badges, an "Anlegen" button opening a Dialog
// form, and per-row Bearbeiten/Löschen. ETag-aware edit flows load via
// resourceApi.get (getWithEtag) and PUT with that etag; 409/412 surface
// through FormError.
import { type ReactNode } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useWhoami } from '@/hooks/useWhoami'
import { hasPermission } from '../../permissions'
import { t } from '../../i18n'

// Status badges — never colour-only (A-15.29ff): each carries its word.
export function StatusBadge({ kind }: { kind: 'disabled' | 'enabled' | 'system' }) {
  if (kind === 'disabled') {
    return <Badge variant="outline" className="bg-muted text-muted-foreground border-input">{t('disabled')}</Badge>
  }
  if (kind === 'system') {
    return <Badge variant="outline" className="bg-purple-500/10 text-purple-400 border-purple-800">{t('systemBadge')}</Badge>
  }
  return <Badge variant="outline" className="bg-emerald-500/10 text-emerald-400 border-emerald-800">{t('enabled')}</Badge>
}

export function TypeBadge({ children }: { children: ReactNode }) {
  return (
    <Badge variant="outline" className="bg-muted text-foreground/90 border-input justify-center font-mono">
      {children}
    </Badge>
  )
}

// Header above each management table: title-less, just the create button.
// writePerm hides the create button from operators whose request would
// only 403 (RBAC-1) — the API enforces regardless.
export function TableActions({ onCreate, label, writePerm, children }: {
  onCreate?: () => void; label: string; writePerm?: string; children?: ReactNode
}) {
  const { data: me } = useWhoami()
  // Fail open while whoami is still loading/unavailable — the API enforces
  // regardless, and a momentary button beats hiding it from an admin.
  const allowed = !writePerm || !me || hasPermission(me.permissions, writePerm)
  return (
    <div className="flex items-center gap-2 justify-end">
      {children}
      {onCreate && allowed && <Button variant="default" size="sm" onClick={onCreate}>{label}</Button>}
    </div>
  )
}

// Edit/Delete pair for a table row (Delete is the confirm-armed button).
export function RowActions({ children }: { children: ReactNode }) {
  return <div className="flex items-center justify-end gap-1">{children}</div>
}
