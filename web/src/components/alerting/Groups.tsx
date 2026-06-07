// Alert-groups tab: aggregate/rollup rules (SPEC §9.4). CRUD only.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { resourceApi } from '../../api'
import type { AlertGroup } from '../../types'
import { Badge, Button, Dialog, Empty, Input, Table } from '../ui'
import { Field, FormError, ListEditor, DurationInput, SubmitRow, DeleteButton, useSave } from '../forms'
import { t } from '../../i18n'

const groupsApi = resourceApi<AlertGroup>('alert-groups')
const aggregates = ['count', 'min', 'max', 'avg', 'sum', 'median'] as const

function emptyGroup(): AlertGroup {
  return { name: '', groupBy: [], window: '5m', aggregate: 'count' }
}

export function GroupsTab() {
  const { data } = useQuery({ queryKey: groupsApi.queryKey, queryFn: groupsApi.list })
  const [editing, setEditing] = useState<{ group: AlertGroup; etag: number } | null>(null)

  const open = async (name?: string) => {
    if (!name) { setEditing({ group: emptyGroup(), etag: 0 }); return }
    const { data: g, etag } = await groupsApi.get(name)
    setEditing({ group: g, etag })
  }

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Button variant="primary" onClick={() => open()}>{t('create')}</Button>
      </div>
      {(data?.length ?? 0) === 0 ? <Empty text={t('empty')} /> : (
        <Table head={[t('name'), 'Group-By', 'Fenster', 'Aggregat', 'Min', t('actions')]}>
          {data!.map((g) => (
            <tr key={g.name} className="hover:bg-slate-800/30">
              <td className="px-3 py-2 font-medium text-slate-200">{g.name}</td>
              <td className="px-3 py-2 font-mono text-xs text-slate-400">{g.groupBy.join(', ') || '—'}</td>
              <td className="px-3 py-2 font-mono text-xs text-slate-400">{g.window}</td>
              <td className="px-3 py-2"><Badge className="bg-slate-800 text-slate-300 border-slate-700">{g.aggregate ?? 'count'}</Badge></td>
              <td className="px-3 py-2 text-xs text-slate-400 tabular-nums">{g.minCount ?? '—'}</td>
              <td className="px-3 py-2 text-right"><Button size="sm" onClick={() => open(g.name)}>{t('edit')}</Button></td>
            </tr>
          ))}
        </Table>
      )}
      {editing && <GroupDialog state={editing} onClose={() => setEditing(null)} />}
    </div>
  )
}

function GroupDialog({ state, onClose }: { state: { group: AlertGroup; etag: number }; onClose: () => void }) {
  const isNew = state.etag === 0 && !state.group.name
  const [g, setG] = useState<AlertGroup>(state.group)
  const set = (patch: Partial<AlertGroup>) => setG((prev) => ({ ...prev, ...patch }))

  const save = useSave(
    (doc: AlertGroup) => isNew ? groupsApi.create(doc) : groupsApi.update(doc.name, doc, state.etag),
    { invalidate: [groupsApi.queryKey], onDone: onClose },
  )
  const remove = useSave((name: string) => groupsApi.remove(name),
    { invalidate: [groupsApi.queryKey], onDone: onClose })

  const onSubmit = (e: React.FormEvent) => { e.preventDefault(); save.mutate(g) }

  return (
    <Dialog open onClose={onClose} title={isNew ? t('create') : `${t('edit')}: ${state.group.name}`} size="md">
      <form onSubmit={onSubmit} className="space-y-3">
        <Field label={t('name')} required>
          <Input value={g.name} disabled={!isNew} onChange={(e) => set({ name: e.target.value })} placeholder="per-host-flood" />
        </Field>
        <Field label="Group-By (Label-Keys)">
          <ListEditor value={g.groupBy} onChange={(v) => set({ groupBy: v })} placeholder="host" />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Fenster">
            <DurationInput value={g.window} onChange={(v) => set({ window: v })} placeholder="5m" />
          </Field>
          <Field label="Aggregat">
            <select value={g.aggregate ?? 'count'} onChange={(e) => set({ aggregate: e.target.value as AlertGroup['aggregate'] })}
              className="bg-slate-900 border border-slate-700 rounded-lg px-3 py-1.5 text-sm text-slate-200 w-full focus:outline-none focus:border-blue-500 cursor-pointer">
              {aggregates.map((a) => <option key={a} value={a}>{a}</option>)}
            </select>
          </Field>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Wert-Pfad" hint="optional (für min/max/avg/sum/median)">
            <Input value={g.valuePath ?? ''} onChange={(e) => set({ valuePath: e.target.value || undefined })} placeholder="payload.value" />
          </Field>
          <Field label="Min. Anzahl" hint="optional">
            <Input type="number" value={g.minCount ?? ''} onChange={(e) => set({ minCount: e.target.value ? Number(e.target.value) : undefined })} placeholder="3" />
          </Field>
        </div>
        <FormError error={save.error} />
        <div className="flex items-center justify-between pt-2">
          {!isNew ? <DeleteButton onDelete={() => remove.mutate(state.group.name)} /> : <span />}
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!g.name} />
        </div>
      </form>
    </Dialog>
  )
}
