// Tenants (SPEC §11): list + create only (GET /tenants, POST {name,slug}).
// There is no per-tenant edit/delete surface in the API yet, so the row
// actions are intentionally read-only.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get, post, type ListResponse } from '../../api'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Empty, Field, FormError, SubmitRow, useSave } from '@/components/kit'
import { t } from '../../i18n'
import { StatusBadge, TableActions } from './common'

interface Tenant { id: string; name: string; slug: string; disabled?: boolean }
const TENANTS = ['tenants'] as const

export function TenantsTab() {
  const { data, isLoading } = useQuery({
    queryKey: [...TENANTS],
    queryFn: () => get<ListResponse<Tenant>>('/tenants').then((r) => r.items ?? []),
  })
  const [creating, setCreating] = useState(false)
  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setCreating(true)} label={t('create')} writePerm="admin:tenants" />
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('name')}</TableHead>
            <TableHead>Slug</TableHead>
            <TableHead>{t('status')}</TableHead>
            <TableHead>ID</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(data ?? []).map((tn) => (
            <TableRow key={tn.id}>
              <TableCell className="px-3 py-2 text-foreground">{tn.name}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground font-mono">{tn.slug}</TableCell>
              <TableCell className="px-3 py-2">{tn.disabled ? <StatusBadge kind="disabled" /> : <StatusBadge kind="enabled" />}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground/70 font-mono">{tn.id}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}
      {/* No edit/delete API exists for tenants (create-only) — state the
          constraint on the list rather than implying it (NP-08). */}
      <p className="text-xs text-muted-foreground">{t('tenantsReadOnly')}</p>
      {creating && <TenantDialog onClose={() => setCreating(false)} />}
    </div>
  )
}

function TenantDialog({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const save = useSave(
    () => post<{ id: string }>('/tenants', { name, slug }),
    { invalidate: [[...TENANTS]], onDone: onClose },
  )
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{`${t('tenants')} — ${t('create')}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
          <Field label={t('name')} required>
            <Input value={name} onChange={(e) => setName(e.target.value)} required />
          </Field>
          <Field label="Slug" required hint={t('slugHint')}>
            <Input value={slug} onChange={(e) => setSlug(e.target.value)} required />
          </Field>
          <Badge variant="outline" className="bg-muted text-muted-foreground border-input">{t('tenantsNoDelete')}</Badge>
          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name || !slug} />
        </form>
      </DialogContent>
    </Dialog>
  )
}
