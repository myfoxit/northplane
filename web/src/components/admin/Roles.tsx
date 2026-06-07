// Roles: ETag-versioned named resources (resourceApi<Role>('roles')).
// System roles (system:true) are immutable — rendered with a readonly
// badge and no edit/delete affordances. Editable roles open an ETag-aware
// dialog: the form loads the current version via resourceApi.get and PUTs
// with that etag; a 409/412 conflict surfaces through FormError.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { resourceApi } from '../../api'
import type { Role } from '../../types'
import { Button, Table, Empty, Dialog, Input } from '../ui'
import { Field, FormError, SubmitRow, useSave, DeleteButton, ListEditor } from '../forms'
import { t } from '../../i18n'
import { StatusBadge, TableActions, RowActions } from './common'

const rolesApi = resourceApi<Role>('roles')

export function RolesTab() {
  const { data, isLoading } = useQuery({ queryKey: rolesApi.queryKey, queryFn: rolesApi.list })
  const [editing, setEditing] = useState<Role | 'new' | null>(null)
  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setEditing('new')} label={t('create')} />
      <Table head={[t('name'), t('permissions'), 'Erbt', 'IdP-Gruppen', '']}>
        {(data ?? []).map((r) => (
          <tr key={r.name}>
            <td className="px-3 py-2 text-slate-200">
              {r.name} {r.system && <span className="ml-1"><StatusBadge kind="system" /></span>}
            </td>
            <td className="px-3 py-2 text-xs text-slate-400 font-mono truncate max-w-72">
              {r.permissions?.join(', ') || '—'}
            </td>
            <td className="px-3 py-2 text-xs text-slate-400">{r.includes?.join(', ') || '—'}</td>
            <td className="px-3 py-2 text-xs text-slate-400">{r.idpGroups?.join(', ') || '—'}</td>
            <td className="px-3 py-2">
              {!r.system && (
                <RowActions>
                  <Button size="sm" variant="ghost" onClick={() => setEditing(r)}>{t('edit')}</Button>
                  <RoleDelete role={r} />
                </RowActions>
              )}
            </td>
          </tr>
        ))}
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}
      {editing && (
        <RoleDialog name={editing === 'new' ? null : editing.name} onClose={() => setEditing(null)} />
      )}
    </div>
  )
}

function RoleDelete({ role }: { role: Role }) {
  const save = useSave(() => rolesApi.remove(role.name), { invalidate: [[...rolesApi.queryKey]] })
  return (
    <>
      <DeleteButton onDelete={() => save.mutate(undefined)} />
      {save.isError && <FormError error={save.error} />}
    </>
  )
}

function RoleDialog({ name, onClose }: { name: string | null; onClose: () => void }) {
  const isNew = !name
  // Load the current document + ETag for the edit flow.
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...rolesApi.queryKey, name],
    queryFn: () => rolesApi.get(name!),
    enabled: !isNew,
  })
  if (!isNew && isLoading) {
    return <Dialog open onClose={onClose} title={t('loading')} size="lg"><div className="text-slate-500 text-sm">…</div></Dialog>
  }
  return (
    <RoleForm
      doc={loaded?.data ?? { name: '', permissions: [] }}
      etag={loaded?.etag ?? 0}
      isNew={isNew}
      onClose={onClose}
    />
  )
}

function RoleForm({ doc, etag, isNew, onClose }: {
  doc: Role; etag: number; isNew: boolean; onClose: () => void
}) {
  const [name, setName] = useState(doc.name)
  const [permissions, setPermissions] = useState<string[]>(doc.permissions ?? [])
  const [includes, setIncludes] = useState<string[]>(doc.includes ?? [])
  const [idpGroups, setIdpGroups] = useState<string[]>(doc.idpGroups ?? [])
  const [scopeTenant, setScopeTenant] = useState(doc.scope?.tenantId ?? '')
  const [scopeFolder, setScopeFolder] = useState(doc.scope?.folder ?? '')
  const [scopeSelector, setScopeSelector] = useState(doc.scope?.selector ?? '')

  const build = (): Role => {
    const scope = (scopeTenant || scopeFolder || scopeSelector)
      ? { tenantId: scopeTenant || undefined, folder: scopeFolder || undefined, selector: scopeSelector || undefined }
      : undefined
    return { ...doc, name, permissions, includes, idpGroups, scope }
  }
  const save = useSave(
    () => isNew ? rolesApi.create(build()) : rolesApi.update(doc.name, build(), etag),
    { invalidate: [[...rolesApi.queryKey]], onDone: onClose },
  )
  return (
    <Dialog open onClose={onClose} title={isNew ? t('create') : `${t('edit')}: ${doc.name}`} size="lg">
      <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
        <Field label={t('name')} required>
          <Input value={name} onChange={(e) => setName(e.target.value)} required disabled={!isNew} />
        </Field>
        <Field label={t('permissions')} hint='z.B. objects:read, alerts:ack, "*" für alle'>
          <ListEditor value={permissions} onChange={setPermissions} placeholder="objects:read" />
        </Field>
        <Field label="Erbt von Rollen (includes)">
          <ListEditor value={includes} onChange={setIncludes} placeholder="viewer" />
        </Field>
        <Field label="IdP-Gruppen (Auto-Zuweisung)">
          <ListEditor value={idpGroups} onChange={setIdpGroups} placeholder="np-admins" />
        </Field>
        <div className="grid grid-cols-3 gap-2">
          <Field label="Scope: Mandant"><Input value={scopeTenant} onChange={(e) => setScopeTenant(e.target.value)} /></Field>
          <Field label="Scope: Ordner"><Input value={scopeFolder} onChange={(e) => setScopeFolder(e.target.value)} /></Field>
          <Field label="Scope: Selektor"><Input value={scopeSelector} onChange={(e) => setScopeSelector(e.target.value)} /></Field>
        </div>
        <FormError error={save.error} />
        <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name} />
      </form>
    </Dialog>
  )
}
