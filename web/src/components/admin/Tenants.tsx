// Tenants (SPEC §11): list + create only (GET /tenants, POST {name,slug}).
// There is no per-tenant edit/delete surface in the API yet, so the row
// actions are intentionally read-only.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get, post, type ListResponse } from '../../api'
import { Table, Empty, Dialog, Input, Badge } from '../ui'
import { Field, FormError, SubmitRow, useSave } from '../forms'
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
      <TableActions onCreate={() => setCreating(true)} label={t('create')} />
      <Table head={[t('name'), 'Slug', t('status'), 'ID']}>
        {(data ?? []).map((tn) => (
          <tr key={tn.id}>
            <td className="px-3 py-2 text-foreground">{tn.name}</td>
            <td className="px-3 py-2 text-xs text-muted-foreground font-mono">{tn.slug}</td>
            <td className="px-3 py-2">{tn.disabled ? <StatusBadge kind="disabled" /> : <StatusBadge kind="enabled" />}</td>
            <td className="px-3 py-2 text-xs text-muted-foreground/70 font-mono">{tn.id}</td>
          </tr>
        ))}
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}
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
    <Dialog open onClose={onClose} title={`${t('tenants')} — ${t('create')}`} size="md">
      <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
        <Field label={t('name')} required>
          <Input value={name} onChange={(e) => setName(e.target.value)} required />
        </Field>
        <Field label="Slug" required hint="URL-tauglicher Kurzname">
          <Input value={slug} onChange={(e) => setSlug(e.target.value)} required />
        </Field>
        <Badge className="bg-muted text-muted-foreground border-input">Mandanten können derzeit nicht gelöscht werden</Badge>
        <FormError error={save.error} />
        <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name || !slug} />
      </form>
    </Dialog>
  )
}
