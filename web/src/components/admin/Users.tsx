// Users (SPEC §11.2 / §13.2): local break-glass accounts — create, edit
// name/email/roles/disabled, admin password reset, delete. The backend
// protects the last enabled local admin (409 np:users/last-admin) and the
// email-uniqueness (409 np:users/email-in-use) — both surface via FormError.
// A self-service "Mein Passwort ändern" card sits in the footer.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get, post, put, del, fmtTime, type ListResponse } from '../../api'
import type { User, Role } from '../../types'
import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { Empty, Field, FormError, SubmitRow, useSave, DeleteButton, ListEditor, DuplicateButton } from '@/components/kit'
import { copyName } from '@/lib/duplicate'
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
  const [copying, setCopying] = useState<User | null>(null)
  const [pwUser, setPwUser] = useState<User | null>(null)

  const roleNames = (roles ?? []).map((r) => r.name)

  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setEditing('new')} label={t('newUser')} writePerm="admin:users" />
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('name')}</TableHead>
            <TableHead>{t('email')}</TableHead>
            <TableHead>{t('permissions')}</TableHead>
            <TableHead>{t('status')}</TableHead>
            <TableHead>{t('lastSeen')}</TableHead>
            <TableHead></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(data ?? []).map((u) => (
            <TableRow key={u.id}>
              <TableCell className="px-3 py-2 text-foreground">
                {u.name}
                {!u.local && (u.subject?.startsWith('ldap|')
                  ? <Badge variant="outline" className="ml-2 bg-amber-500/10 text-amber-400 border-amber-800">LDAP</Badge>
                  : <Badge variant="outline" className="ml-2 bg-sky-500/10 text-sky-400 border-sky-800">OIDC</Badge>)}
              </TableCell>
              <TableCell className="px-3 py-2 text-muted-foreground text-xs">{u.email}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground">{u.roles?.join(', ') || '—'}</TableCell>
              <TableCell className="px-3 py-2">{u.disabled ? <StatusBadge kind="disabled" /> : <StatusBadge kind="enabled" />}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground tabular-nums">{u.lastSeenAt ? fmtTime(u.lastSeenAt) : '—'}</TableCell>
              <TableCell className="px-3 py-2">
                <RowActions>
                  {u.local && <Button size="sm" variant="ghost" onClick={() => setPwUser(u)}>{t('setPassword')}</Button>}
                  <Button size="sm" variant="ghost" onClick={() => setEditing(u)}>{t('edit')}</Button>
                  <DuplicateButton onClick={() => setCopying(u)} />
                  <UserDelete user={u} />
                </RowActions>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}

      {editing && (
        <UserDialog
          user={editing === 'new' ? null : editing}
          roleNames={roleNames}
          onClose={() => setEditing(null)}
        />
      )}
      {copying && (
        <UserDialog user={copying} copy roleNames={roleNames} onClose={() => setCopying(null)} />
      )}
      {pwUser && <SetPasswordDialog user={pwUser} onClose={() => setPwUser(null)} />}

      <DirectorySyncCard />
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

// UserDialog: `user` null → create; set → edit; set + `copy` → create a new
// local account seeded from that user's roles and disabled flag. Identity is
// NOT copied: the e-mail must be unique (left blank), the display name gets
// the copy suffix, and a copy always needs its own password.
function UserDialog({ user, roleNames, copy, onClose }: {
  user: User | null; roleNames: string[]; copy?: boolean; onClose: () => void
}) {
  const isNew = !user || !!copy
  const [name, setName] = useState(copy && user ? copyName(user.name) : (user?.name ?? ''))
  const [email, setEmail] = useState(copy ? '' : (user?.email ?? ''))
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
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{copy && user ? `${t('duplicate')}: ${user.name}` : isNew ? t('newUser') : `${t('edit')}: ${user!.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
          <Field label={t('name')} required>
            <Input value={name} onChange={(e) => setName(e.target.value)} required />
          </Field>
          <Field label={t('email')} required>
            <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
          </Field>
          {isNew && (
            <Field label={t('password')} hint={t('passwordHintNew')}>
              <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)}
                autoComplete="new-password" minLength={12} />
            </Field>
          )}
          <Field label={t('permissions')} hint={t('roleNamesHint')}>
            <ListEditor value={roles} onChange={setRoles} placeholder="admin" suggestions={roleNames} />
          </Field>
          <div className="flex items-center gap-2">
            <Switch id="user-disabled" checked={disabled} onCheckedChange={setDisabled} />
            <Label htmlFor="user-disabled">{t('disabled')}</Label>
          </div>
          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name || !email} />
        </form>
      </DialogContent>
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
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{`${t('setPassword')}: ${user.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
          <Field label={t('password')} required hint={t('passwordHintSet')}>
            <Input type="password" value={pw} onChange={(e) => setPw(e.target.value)}
              autoComplete="new-password" />
          </Field>
          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} label={t('setPassword')} />
        </form>
      </DialogContent>
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
    <Card>
      <CardHeader>
        <CardTitle>{t('changeOwnPassword')}</CardTitle>
      </CardHeader>
      <CardContent>
        <form
          onSubmit={(e) => { e.preventDefault(); setOk(false); save.mutate(undefined) }}
          className="flex flex-wrap items-end gap-3"
        >
          <Field label={t('currentPassword')} className="flex-1 min-w-40">
            <Input type="password" value={oldPw} onChange={(e) => setOldPw(e.target.value)} autoComplete="current-password" />
          </Field>
          <Field label={t('newPassword')} hint={t('min12Chars')} className="flex-1 min-w-40">
            <Input type="password" value={newPw} onChange={(e) => setNewPw(e.target.value)} autoComplete="new-password" />
          </Field>
          <Button variant="default" type="submit" disabled={!oldPw || !newPw || save.isPending}>
            {t('changePassword')}
          </Button>
          {ok && <span className="text-sm text-emerald-400">{t('saved')}</span>}
        </form>
        <div className="mt-2"><FormError error={save.error} /></div>
      </CardContent>
    </Card>
  )
}

// Directory sync (LDAP): visible only when configured — shows the last
// pass and offers a manual sync. Users arriving from the directory carry
// the LDAP badge above; their roles come from group→role mappings.
interface DirectoryStatus {
  configured: boolean
  url?: string
  syncInterval?: string
  lastSyncAt?: string
  lastError?: string
  lastResult?: { created: number; updated: number; unchanged: number; disabled: number; skipped: number; warnings?: string[] }
}

function DirectorySyncCard() {
  const { data } = useQuery({
    queryKey: ['directory', 'status'],
    queryFn: () => get<DirectoryStatus>('/directory/status'),
  })
  const sync = useSave(() => post('/directory:sync', {}), {
    invalidate: [['directory', 'status'], [...USERS]],
  })
  if (!data?.configured) return null
  const r = data.lastResult
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('directorySync')}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-2 text-sm text-muted-foreground">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
          <span className="font-mono text-xs">{data.url}</span>
          <span>{t('interval')} {data.syncInterval}</span>
          <span>{t('lastRun')}: {data.lastSyncAt ? fmtTime(data.lastSyncAt) : '—'}</span>
          {r && (
            <span className="tabular-nums">
              {r.created} {t('newLower')} · {r.updated} {t('updated')} · {r.disabled} {t('deactivated')} · {r.skipped} {t('skipped')}
            </span>
          )}
        </div>
        {data.lastError && <div className="text-red-400 text-xs">⚠ {data.lastError}</div>}
        {(r?.warnings?.length ?? 0) > 0 && (
          <div className="text-xs text-amber-400 space-y-0.5">
            {r!.warnings!.map((w, i) => <div key={i}>⚠ {w}</div>)}
          </div>
        )}
        <div>
          <Button size="sm" variant="outline" onClick={() => sync.mutate(undefined)} disabled={sync.isPending}>
            {sync.isPending ? t('syncing') : t('syncNow')}
          </Button>
        </div>
        <FormError error={sync.error} />
      </CardContent>
    </Card>
  )
}
