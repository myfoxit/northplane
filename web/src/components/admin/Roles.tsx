// Roles: ETag-versioned named resources (resourceApi<Role>('roles')).
// System roles (system:true) are immutable — rendered with a readonly
// badge and no edit/delete affordances. Editable roles open an ETag-aware
// dialog: the form loads the current version via resourceApi.get and PUTs
// with that etag; a 409/412 conflict surfaces through FormError.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { resourceApi } from '../../api'
import type { Role } from '../../types'
import { Button } from '@/components/ui/button'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Empty, Spinner, Field, FormError, SubmitRow, useSave, DeleteButton, ListEditor, DuplicateButton } from '@/components/kit'
import { duplicateDoc } from '@/lib/duplicate'
import { t } from '../../i18n'
import { StatusBadge, TableActions, RowActions } from './common'

const rolesApi = resourceApi<Role>('roles')

export function RolesTab() {
  const { data, isLoading } = useQuery({ queryKey: rolesApi.queryKey, queryFn: rolesApi.list })
  const [editing, setEditing] = useState<Role | 'new' | null>(null)
  const [copying, setCopying] = useState<Role | null>(null)
  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setEditing('new')} label={t('create')} writePerm="admin:write" />
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('name')}</TableHead>
            <TableHead>{t('permissions')}</TableHead>
            <TableHead>{t('inherits')}</TableHead>
            <TableHead>{t('idpGroups')}</TableHead>
            <TableHead></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(data ?? []).map((r) => (
            <TableRow key={r.name}>
              <TableCell className="px-3 py-2 text-foreground">
                {r.name} {r.system && <span className="ml-1"><StatusBadge kind="system" /></span>}
              </TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground font-mono truncate max-w-72">
                {r.permissions?.join(', ') || '—'}
              </TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground">{r.includes?.join(', ') || '—'}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground">{r.idpGroups?.join(', ') || '—'}</TableCell>
              <TableCell className="px-3 py-2">
                <RowActions>
                  {!r.system && <Button size="sm" variant="ghost" onClick={() => setEditing(r)}>{t('edit')}</Button>}
                  {/* System roles are read-only, but a copy is the way to derive a custom role from one. */}
                  <DuplicateButton onClick={() => setCopying(r)} />
                  {!r.system && <RoleDelete role={r} />}
                </RowActions>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}
      {editing && (
        <RoleDialog name={editing === 'new' ? null : editing.name} onClose={() => setEditing(null)} />
      )}
      {copying && (
        <RoleDialog name={copying.name} copy existing={(data ?? []).map((r) => r.name)}
          onClose={() => setCopying(null)} />
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

// RoleDialog: `name` null → create; set → edit; set + `copy` → create a
// duplicate seeded from that role (envelope and `system` flag stripped, so a
// copy of a built-in role is an ordinary custom role).
function RoleDialog({ name, copy, existing, onClose }: {
  name: string | null; copy?: boolean; existing?: string[]; onClose: () => void
}) {
  const isNew = !name || !!copy
  // Load the current document + ETag for the edit/copy flow.
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...rolesApi.queryKey, name],
    queryFn: () => rolesApi.get(name!),
    enabled: !!name,
  })
  if (name && isLoading) {
    return (
      <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t('loading')}</DialogTitle>
          </DialogHeader>
          <Spinner />
        </DialogContent>
      </Dialog>
    )
  }
  const src = loaded?.data
  return (
    <RoleForm
      doc={copy && src ? duplicateDoc(src, existing, ['system']) : (src ?? { name: '', permissions: [] })}
      etag={copy ? 0 : (loaded?.etag ?? 0)}
      isNew={isNew}
      copyOf={copy ? name ?? undefined : undefined}
      onClose={onClose}
    />
  )
}

function RoleForm({ doc, etag, isNew, copyOf, onClose }: {
  doc: Role; etag: number; isNew: boolean; copyOf?: string; onClose: () => void
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
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{copyOf ? `${t('duplicate')}: ${copyOf}` : isNew ? t('create') : `${t('edit')}: ${doc.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
          <Field label={t('name')} required>
            <Input value={name} onChange={(e) => setName(e.target.value)} required disabled={!isNew} />
          </Field>
          <Field label={t('permissions')} hint={t('permissionsHint')}>
            <ListEditor value={permissions} onChange={setPermissions} placeholder="objects:read" />
          </Field>
          <Field label={t('inheritsFromRoles')}>
            <ListEditor value={includes} onChange={setIncludes} placeholder="viewer" />
          </Field>
          <Field label={t('idpGroupsAutoAssign')}>
            <ListEditor value={idpGroups} onChange={setIdpGroups} placeholder="np-admins" />
          </Field>
          <div className="grid grid-cols-3 gap-2">
            <Field label={t('scopeTenant')}><Input value={scopeTenant} onChange={(e) => setScopeTenant(e.target.value)} /></Field>
            <Field label={t('scopeFolder')}><Input value={scopeFolder} onChange={(e) => setScopeFolder(e.target.value)} /></Field>
            <Field label={t('scopeSelector')}><Input value={scopeSelector} onChange={(e) => setScopeSelector(e.target.value)} /></Field>
          </div>
          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name} />
        </form>
      </DialogContent>
    </Dialog>
  )
}
