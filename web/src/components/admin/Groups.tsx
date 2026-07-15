// Contact groups: ETag-versioned via resourceApi<ContactGroup>. Members are
// contact references (model.ContactGroup.Members); the member editor offers
// the existing contacts as autocomplete suggestions while still accepting
// free entry. An optional idpGroup mirrors an Entra/Keycloak group.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { resourceApi } from '../../api'
import type { ContactGroup, Contact } from '../../types'
import { Button } from '@/components/ui/button'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Empty, Spinner, Field, FormError, SubmitRow, useSave, DeleteButton, ListEditor } from '@/components/kit'
import { t } from '../../i18n'
import { TableActions, RowActions } from './common'

const groupsApi = resourceApi<ContactGroup>('contact-groups')
const contactsApi = resourceApi<Contact>('contacts')

export function GroupsTab() {
  const { data, isLoading } = useQuery({ queryKey: groupsApi.queryKey, queryFn: groupsApi.list })
  const [editing, setEditing] = useState<ContactGroup | 'new' | null>(null)
  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setEditing('new')} label={t('create')} />
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('name')}</TableHead>
            <TableHead>{t('members')}</TableHead>
            <TableHead>{t('idpGroup')}</TableHead>
            <TableHead></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(data ?? []).map((g) => (
            <TableRow key={g.name}>
              <TableCell className="px-3 py-2 text-foreground">{g.name}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground">{g.members?.join(', ') || '—'}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground font-mono">{g.idpGroup || '—'}</TableCell>
              <TableCell className="px-3 py-2">
                <RowActions>
                  <Button size="sm" variant="ghost" onClick={() => setEditing(g)}>{t('edit')}</Button>
                  <GroupDelete group={g} />
                </RowActions>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}
      {editing && (
        <GroupDialog name={editing === 'new' ? null : editing.name} onClose={() => setEditing(null)} />
      )}
    </div>
  )
}

function GroupDelete({ group }: { group: ContactGroup }) {
  const save = useSave(() => groupsApi.remove(group.name), { invalidate: [[...groupsApi.queryKey]] })
  return (
    <>
      <DeleteButton onDelete={() => save.mutate(undefined)} />
      {save.isError && <FormError error={save.error} />}
    </>
  )
}

function GroupDialog({ name, onClose }: { name: string | null; onClose: () => void }) {
  const isNew = !name
  const { data: contacts } = useQuery({ queryKey: contactsApi.queryKey, queryFn: contactsApi.list })
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...groupsApi.queryKey, name],
    queryFn: () => groupsApi.get(name!),
    enabled: !isNew,
  })
  if (!isNew && isLoading) {
    return (
      <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{t('loading')}</DialogTitle>
          </DialogHeader>
          <Spinner />
        </DialogContent>
      </Dialog>
    )
  }
  return (
    <GroupForm
      doc={loaded?.data ?? { name: '', members: [] }}
      etag={loaded?.etag ?? 0}
      isNew={isNew}
      suggestions={(contacts ?? []).map((c) => c.name)}
      onClose={onClose}
    />
  )
}

function GroupForm({ doc, etag, isNew, suggestions, onClose }: {
  doc: ContactGroup; etag: number; isNew: boolean; suggestions: string[]; onClose: () => void
}) {
  const [name, setName] = useState(doc.name)
  const [members, setMembers] = useState<string[]>(doc.members ?? [])
  const [idpGroup, setIdpGroup] = useState(doc.idpGroup ?? '')

  const build = (): ContactGroup => ({ ...doc, name, members, idpGroup: idpGroup || undefined })
  const save = useSave(
    () => isNew ? groupsApi.create(build()) : groupsApi.update(doc.name, build(), etag),
    { invalidate: [[...groupsApi.queryKey]], onDone: onClose },
  )
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{isNew ? t('create') : `${t('edit')}: ${doc.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
          <Field label={t('name')} required>
            <Input value={name} onChange={(e) => setName(e.target.value)} required disabled={!isNew} />
          </Field>
          <Field label={t('members')} hint={t('membersHint')}>
            <ListEditor value={members} onChange={setMembers} placeholder={t('contactPlaceholder')} suggestions={suggestions} />
          </Field>
          <Field label={t('idpGroup')} hint={t('idpGroupHint')}>
            <Input value={idpGroup} onChange={(e) => setIdpGroup(e.target.value)} />
          </Field>
          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name} />
        </form>
      </DialogContent>
    </Dialog>
  )
}
