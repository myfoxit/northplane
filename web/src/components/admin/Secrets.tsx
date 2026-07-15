// Secrets ($SECRET:name$, SPEC §8.2/§13.2): values are write-only — the API
// only ever returns the names (GET /secrets → string[]). Create/overwrite is
// PUT /secrets/{name} {value}; delete is DELETE /secrets/{name}. The create
// dialog warns that the value is never shown again. Channels and event
// sources reference these by their $SECRET:name$ form.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get, put, del } from '../../api'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Empty, Field, FormError, SubmitRow, useSave, DeleteButton } from '@/components/kit'
import { t } from '../../i18n'
import { TableActions, RowActions } from './common'

const SECRETS = ['secrets'] as const

export function SecretsTab() {
  const { data, isLoading } = useQuery({
    queryKey: [...SECRETS],
    queryFn: () => get<string[]>('/secrets'),
  })
  const [creating, setCreating] = useState(false)
  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setCreating(true)} label={t('create')} />
      <p className="text-xs text-muted-foreground">
        {t('secretsIntro')} <code className="text-muted-foreground">$SECRET:name$</code>
      </p>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('name')}</TableHead>
            <TableHead>{t('reference')}</TableHead>
            <TableHead></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(data ?? []).map((name) => (
            <TableRow key={name}>
              <TableCell className="px-3 py-2 text-foreground font-mono">{name}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground font-mono">$SECRET:{name}$</TableCell>
              <TableCell className="px-3 py-2">
                <RowActions>
                  <SecretDelete name={name} />
                </RowActions>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}
      {creating && <SecretDialog onClose={() => setCreating(false)} />}
    </div>
  )
}

function SecretDelete({ name }: { name: string }) {
  const save = useSave(() => del(`/secrets/${encodeURIComponent(name)}`), { invalidate: [[...SECRETS]] })
  return (
    <>
      <DeleteButton onDelete={() => save.mutate(undefined)} />
      {save.isError && <FormError error={save.error} />}
    </>
  )
}

function SecretDialog({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const save = useSave(
    () => put(`/secrets/${encodeURIComponent(name)}`, { value }, 0),
    { invalidate: [[...SECRETS]], onDone: onClose },
  )
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{`${t('secrets')} — ${t('create')}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
          <Field label={t('name')} required hint={t('egSmtpPassword')}>
            <Input value={name} onChange={(e) => setName(e.target.value)} required autoComplete="off" />
          </Field>
          <Field label={t('value')} required hint={t('neverShownAgain')}>
            <Input type="password" value={value} onChange={(e) => setValue(e.target.value)} required autoComplete="new-password" />
          </Field>
          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name || !value} />
        </form>
      </DialogContent>
    </Dialog>
  )
}
