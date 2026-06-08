// Alert-groups tab: aggregate/rollup rules (SPEC §9.4). CRUD only.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { resourceApi } from '../../api'
import type { AlertGroup } from '../../types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { Empty, ErrorState, Field, FormError, ListEditor, DurationInput, SubmitRow, DeleteButton, useSave } from '@/components/kit'
import { t } from '../../i18n'

const groupsApi = resourceApi<AlertGroup>('alert-groups')
const aggregates = ['count', 'min', 'max', 'avg', 'sum', 'median'] as const

function emptyGroup(): AlertGroup {
  return { name: '', groupBy: [], window: '5m', aggregate: 'count' }
}

export function GroupsTab() {
  const { data, isError, error, refetch } = useQuery({ queryKey: groupsApi.queryKey, queryFn: groupsApi.list })
  const [editing, setEditing] = useState<{ group: AlertGroup; etag: number } | null>(null)

  const open = async (name?: string) => {
    if (!name) { setEditing({ group: emptyGroup(), etag: 0 }); return }
    const { data: g, etag } = await groupsApi.get(name)
    setEditing({ group: g, etag })
  }

  if (isError && !data) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Button variant="default" onClick={() => open()}>{t('create')}</Button>
      </div>
      {(data?.length ?? 0) === 0 ? <Empty text={t('empty')} /> : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('name')}</TableHead>
              <TableHead>Group-By</TableHead>
              <TableHead>Fenster</TableHead>
              <TableHead>Aggregat</TableHead>
              <TableHead>Min</TableHead>
              <TableHead>{t('actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data!.map((g) => (
              <TableRow key={g.name}>
                <TableCell className="font-medium text-foreground">{g.name}</TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">{g.groupBy.join(', ') || '—'}</TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">{g.window}</TableCell>
                <TableCell><Badge variant="outline" className="bg-muted text-foreground/90 border-input">{g.aggregate ?? 'count'}</Badge></TableCell>
                <TableCell className="text-xs text-muted-foreground tabular-nums">{g.minCount ?? '—'}</TableCell>
                <TableCell className="text-right"><Button size="sm" variant="outline" onClick={() => open(g.name)}>{t('edit')}</Button></TableCell>
              </TableRow>
            ))}
          </TableBody>
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
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{isNew ? t('create') : `${t('edit')}: ${state.group.name}`}</DialogTitle>
        </DialogHeader>
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
              <Select value={g.aggregate ?? 'count'} onValueChange={(v) => set({ aggregate: v as AlertGroup['aggregate'] })}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {aggregates.map((a) => <SelectItem key={a} value={a}>{a}</SelectItem>)}
                </SelectContent>
              </Select>
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
      </DialogContent>
    </Dialog>
  )
}
