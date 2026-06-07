// Users (SPEC §11.2 / §13.2): local break-glass accounts — create, edit
// name/email/roles/disabled, admin password reset, delete. The backend
// protects the last enabled local admin (409 np:users/last-admin) and the
// email-uniqueness (409 np:users/email-in-use) — both surface via FormError.
// A self-service "Mein Passwort ändern" card sits in the footer.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get, post, put, del, fmtTime, type ListResponse } from '../../api'
import type { User, Role } from '../../types'
import { Button, Card, Table, Empty, Badge, Input, Dialog } from '../ui'
import { Field, FormError, SubmitRow, useSave, DeleteButton, ListEditor } from '../forms'
import { t } from '../../i18n'
import { StatusBadge, TableActions, RowActions } from './common'

const USERS = ['users'] as const

export function UsersTab() {
  const { data, isLoading } = useQuery({
    queryKey: [...USERS],
    queryFn: () => get<ListResponse<User>>('/users?limit=500').then((r) => r.items ?? []),
  })
  const { data: roles } = useQuery({
    queryKey: ['resources', 'roles'],
    queryFn: () => get<ListResponse<Role>>('/roles?limit=500').then((r) => r.items ?? []),
  })
  const [editing, setEditing] = useState<User | 'new' | null>(null)
  const [pwUser, setPwUser] = useState<User | null>(null)

  const roleNames = (roles ?? []).map((r) => r.name)

  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setEditing('new')} label={t('newUser')} />
      <Table head={[t('name'), t('email'), t('permissions'), t('status'), t('lastSeen'), '']}>
        {(data ?? []).map((u) => (
          <tr key={u.id}>
            <td className="px-3 py-2 text-slate-200">
              {u.name}
              {!u.local && <Badge className="ml-2 bg-sky-500/10 text-sky-400 border-sky-800">OIDC</Badge>}
            </td>
            <td className="px-3 py-2 text-slate-400 text-xs">{u.email}</td>
            <td className="px-3 py-2 text-xs text-slate-400">{u.roles?.join(', ') || '—'}</td>
            <td className="px-3 py-2">{u.disabled ? <StatusBadge kind="disabled" /> : <StatusBadge kind="enabled" />}</td>
            <td className="px-3 py-2 text-xs text-slate-500 tabular-nums">{u.lastSeenAt ? fmtTime(u.lastSeenAt) : '—'}</td>
            <td className="px-3 py-2">
              <RowActions>
                {u.local && <Button size="sm" variant="ghost" onClick={() => setPwUser(u)}>{t('setPassword')}</Button>}
                <Button size="sm" variant="ghost" onClick={() => setEditing(u)}>{t('edit')}</Button>
                <UserDelete user={u} />
              </RowActions>
            </td>
          </tr>
        ))}
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}

      {editing && (
        <UserDialog
          user={editing === 'new' ? null : editing}
          roleNames={roleNames}
          onClose={() => setEditing(null)}
        />
      )}
      {pwUser && <SetPasswordDialog user={pwUser} onClose={() => setPwUser(null)} />}

      <ChangeOwnPassword />
    </div>
  )
}

function UserDelete({ user }: { user: User }) {
  const save = useSave((id: string) => del(`/users/${encodeURIComponent(id)}`), { invalidate: [[...USERS]] })
  return (
    <>
      <DeleteButton onDelete={() => save.mutate(user.id!)} />
      {save.isError && <FormError error={save.error} />}
    </>
  )
}

function UserDialog({ user, roleNames, onClose }: {
  user: User | null; roleNames: string[]; onClose: () => void
}) {
  const isNew = !user
  const [name, setName] = useState(user?.name ?? '')
  const [email, setEmail] = useState(user?.email ?? '')
  const [password, setPassword] = useState('')
  const [roles, setRoles] = useState<string[]>(user?.roles ?? [])
  const [disabled, setDisabled] = useState(user?.disabled ?? false)

  const save = useSave(
    () => {
      if (isNew) {
        return post<User>('/users', {
          name, email, password: password || undefined, roles, disabled,
        })
      }
      // PUT body is a partial; we always send the editable quartet.
      return put<User>(`/users/${encodeURIComponent(user!.id!)}`, { name, email, roles, disabled }, 0)
    },
    { invalidate: [[...USERS]], onDone: onClose },
  )

  return (
    <Dialog open onClose={onClose} title={isNew ? t('newUser') : `${t('edit')}: ${user!.name}`} size="md">
      <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
        <Field label={t('name')} required>
          <Input value={name} onChange={(e) => setName(e.target.value)} required />
        </Field>
        <Field label={t('email')} required>
          <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
        </Field>
        {isNew && (
          <Field label={t('password')} hint="Optional — mind. 12 Zeichen. Leer = nur OIDC-Login.">
            <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password" minLength={12} />
          </Field>
        )}
        <Field label={t('permissions')} hint="Rollen-Namen">
          <ListEditor value={roles} onChange={setRoles} placeholder="admin" suggestions={roleNames} />
        </Field>
        <label className="flex items-center gap-2 text-sm text-slate-300 cursor-pointer">
          <input type="checkbox" checked={disabled} onChange={(e) => setDisabled(e.target.checked)} />
          {t('disabled')}
        </label>
        <FormError error={save.error} />
        <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name || !email} />
      </form>
    </Dialog>
  )
}

function SetPasswordDialog({ user, onClose }: { user: User; onClose: () => void }) {
  const [pw, setPw] = useState('')
  const save = useSave(
    () => post(`/users/${encodeURIComponent(user.id!)}:set-password`, { password: pw }),
    { invalidate: [[...USERS]], onDone: onClose },
  )
  return (
    <Dialog open onClose={onClose} title={`${t('setPassword')}: ${user.name}`} size="md">
      <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
        <Field label={t('password')} required hint="Mind. 12 Zeichen. Leer lassen entfernt das Passwort (nur OIDC).">
          <Input type="password" value={pw} onChange={(e) => setPw(e.target.value)}
            autoComplete="new-password" />
        </Field>
        <FormError error={save.error} />
        <SubmitRow onCancel={onClose} saving={save.isPending} label={t('setPassword')} />
      </form>
    </Dialog>
  )
}

// Self-service: the logged-in user changes their own password without
// admin rights (POST /users/me:change-password, verifies the old one).
function ChangeOwnPassword() {
  const [oldPw, setOldPw] = useState('')
  const [newPw, setNewPw] = useState('')
  const [ok, setOk] = useState(false)
  const save = useSave(
    () => post('/users/me:change-password', { oldPassword: oldPw, newPassword: newPw }),
    { invalidate: [], onDone: () => { setOk(true); setOldPw(''); setNewPw('') } },
  )
  return (
    <Card title="Mein Passwort ändern">
      <form
        onSubmit={(e) => { e.preventDefault(); setOk(false); save.mutate(undefined) }}
        className="flex flex-wrap items-end gap-3"
      >
        <Field label="Aktuelles Passwort" className="flex-1 min-w-40">
          <Input type="password" value={oldPw} onChange={(e) => setOldPw(e.target.value)} autoComplete="current-password" />
        </Field>
        <Field label="Neues Passwort" hint="Mind. 12 Zeichen" className="flex-1 min-w-40">
          <Input type="password" value={newPw} onChange={(e) => setNewPw(e.target.value)} autoComplete="new-password" />
        </Field>
        <Button variant="primary" type="submit" disabled={!oldPw || !newPw || save.isPending}>
          {t('changePassword')}
        </Button>
        {ok && <span className="text-sm text-emerald-400">{t('saved')}</span>}
      </form>
      <div className="mt-2"><FormError error={save.error} /></div>
    </Card>
  )
}
