// Secrets ($SECRET:name$, SPEC §8.2/§13.2): values are write-only — the API
// only ever returns the names (GET /secrets → string[]). Create/overwrite is
// PUT /secrets/{name} {value}; delete is DELETE /secrets/{name}. The create
// dialog warns that the value is never shown again. Channels and event
// sources reference these by their $SECRET:name$ form.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get, put, del } from '../../api'
import { Table, Empty, Dialog, Input } from '../ui'
import { Field, FormError, SubmitRow, useSave, DeleteButton } from '../forms'
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
        Werte werden verschlüsselt gespeichert und nie wieder angezeigt. Referenz in Kanälen/Quellen: <code className="text-muted-foreground">$SECRET:name$</code>
      </p>
      <Table head={[t('name'), 'Referenz', '']}>
        {(data ?? []).map((name) => (
          <tr key={name}>
            <td className="px-3 py-2 text-foreground font-mono">{name}</td>
            <td className="px-3 py-2 text-xs text-muted-foreground font-mono">$SECRET:{name}$</td>
            <td className="px-3 py-2">
              <RowActions>
                <SecretDelete name={name} />
              </RowActions>
            </td>
          </tr>
        ))}
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
    <Dialog open onClose={onClose} title={`${t('secrets')} — ${t('create')}`} size="md">
      <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
        <Field label={t('name')} required hint="z.B. smtp-password">
          <Input value={name} onChange={(e) => setName(e.target.value)} required autoComplete="off" />
        </Field>
        <Field label="Wert" required hint="Wird nie wieder angezeigt.">
          <Input type="password" value={value} onChange={(e) => setValue(e.target.value)} required autoComplete="new-password" />
        </Field>
        <FormError error={save.error} />
        <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name || !value} />
      </form>
    </Dialog>
  )
}
