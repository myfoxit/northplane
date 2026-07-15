// Outgoing webhooks + heartbeats (API/UI parity): both resources were
// API-only — these tabs close the gap. Webhooks are ETag-versioned via
// resourceApi<WebhookSub>('webhooks') (SPEC §11.5); heartbeats use the
// dedicated list/upsert/delete endpoints (F-02.02 dead-man inputs) and
// show the beat URL for copy-paste into cron jobs.
import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { resourceApi, get, post, del, queryClient, fmtAgo, type ListResponse } from '../../api'
import type { WebhookSub, HeartbeatDef, Severity } from '../../types'
import { Button } from '@/components/ui/button'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { Empty, Spinner, Field, FormError, SubmitRow, useSave, DeleteButton, KVEditor, DurationInput } from '@/components/kit'
import { t } from '../../i18n'
import { StatusBadge, TableActions, RowActions } from './common'
import { SeverityField } from '../alerting/common'

// ——————————————————— outgoing webhooks ———————————————————

const webhooksApi = resourceApi<WebhookSub>('webhooks')

export function WebhooksTab() {
  const { data, isLoading } = useQuery({ queryKey: webhooksApi.queryKey, queryFn: webhooksApi.list })
  const [editing, setEditing] = useState<WebhookSub | 'new' | null>(null)
  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setEditing('new')} label={t('create')} />
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('name')}</TableHead>
            <TableHead>URL</TableHead>
            <TableHead>{t('eventTypes')}</TableHead>
            <TableHead>Selector</TableHead>
            <TableHead>{t('status')}</TableHead>
            <TableHead></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(data ?? []).map((w) => (
            <TableRow key={w.name}>
              <TableCell className="px-3 py-2 text-foreground">{w.name}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground font-mono truncate max-w-64">{w.url}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground">{w.types?.length ? w.types.join(', ') : t('all')}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground font-mono">{w.selector || '—'}</TableCell>
              <TableCell className="px-3 py-2">{w.disabled ? <StatusBadge kind="disabled" /> : <StatusBadge kind="enabled" />}</TableCell>
              <TableCell className="px-3 py-2">
                <RowActions>
                  <Button size="sm" variant="ghost" onClick={() => setEditing(w)}>{t('edit')}</Button>
                  <WebhookDelete name={w.name} />
                </RowActions>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}
      {editing && (
        <WebhookDialog name={editing === 'new' ? null : editing.name} onClose={() => setEditing(null)} />
      )}
    </div>
  )
}

function WebhookDelete({ name }: { name: string }) {
  const save = useSave(() => webhooksApi.remove(name), { invalidate: [[...webhooksApi.queryKey]] })
  return <DeleteButton onDelete={() => save.mutate(undefined)} />
}

function WebhookDialog({ name, onClose }: { name: string | null; onClose: () => void }) {
  const isNew = !name
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...webhooksApi.queryKey, name],
    queryFn: () => webhooksApi.get(name!),
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
    <WebhookForm
      doc={loaded?.data ?? { name: '', url: '' }}
      etag={loaded?.etag ?? 0}
      isNew={isNew}
      onClose={onClose}
    />
  )
}

function WebhookForm({ doc, etag, isNew, onClose }: {
  doc: WebhookSub; etag: number; isNew: boolean; onClose: () => void
}) {
  const [w, setW] = useState<WebhookSub>(doc)
  const set = (patch: Partial<WebhookSub>) => setW((prev) => ({ ...prev, ...patch }))
  const save = useSave(
    () => isNew ? webhooksApi.create(w) : webhooksApi.update(doc.name, w, etag),
    { invalidate: [[...webhooksApi.queryKey]], onDone: onClose },
  )
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{isNew ? t('create') : `${t('edit')}: ${doc.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
          <div className="grid grid-cols-2 gap-2">
            <Field label={t('name')} required>
              <Input value={w.name} onChange={(e) => set({ name: e.target.value })} required disabled={!isNew} />
            </Field>
            <Field label="URL" required>
              <Input value={w.url} onChange={(e) => set({ url: e.target.value })}
                placeholder="https://example.net/hook" required />
            </Field>
          </div>
          <Field label={t('eventTypesField')}
            hint="state_change, alert_opened, alert_resolved, notification, …">
            <Input value={w.types?.join(', ') ?? ''}
              onChange={(e) => set({ types: e.target.value.split(',').map((s) => s.trim()).filter(Boolean) })} />
          </Field>
          <div className="grid grid-cols-2 gap-2">
            <Field label={t('selectorOptional')}>
              <Input value={w.selector ?? ''} placeholder="env=prod"
                onChange={(e) => set({ selector: e.target.value || undefined })} />
            </Field>
            <Field label={t('hmacSecret')} hint={t('secretHint')}>
              <Input value={w.secret ?? ''} onChange={(e) => set({ secret: e.target.value || undefined })} />
            </Field>
          </div>
          <div className="flex items-center gap-2">
            <Switch id="webhook-enabled" checked={!w.disabled} onCheckedChange={(v) => set({ disabled: !v })} />
            <Label htmlFor="webhook-enabled">{t('enabled')}</Label>
          </div>
          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!w.name || !w.url} />
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ——————————————————— heartbeats (dead-man) ———————————————————

export function HeartbeatsTab() {
  const { data, isLoading } = useQuery({
    queryKey: ['heartbeats'],
    queryFn: () => get<ListResponse<HeartbeatDef>>('/heartbeats'),
    refetchInterval: 30_000,
  })
  const [editing, setEditing] = useState<HeartbeatDef | 'new' | null>(null)
  const remove = useMutation({
    mutationFn: (name: string) => del(`/heartbeats/${encodeURIComponent(name)}`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['heartbeats'] }),
  })
  const rows = data?.items ?? []
  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setEditing('new')} label={t('create')} />
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('name')}</TableHead>
            <TableHead>{t('expectEvery')}</TableHead>
            <TableHead>{t('grace')}</TableHead>
            <TableHead>{t('severity')}</TableHead>
            <TableHead>{t('lastBeat')}</TableHead>
            <TableHead>{t('status')}</TableHead>
            <TableHead></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((h) => (
            <TableRow key={h.name}>
              <TableCell className="px-3 py-2 text-foreground">{h.name}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground tabular-nums">{h.expectEvery}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground tabular-nums">{h.grace || '—'}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground">{h.severity || 'critical'}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground tabular-nums">
                {h.lastBeat ? fmtAgo(h.lastBeat) : t('never')}
              </TableCell>
              <TableCell className="px-3 py-2">
                {h.missing
                  ? <Badge variant="outline" className="bg-red-500/10 text-red-400 border-red-800">{t('missing')}</Badge>
                  : <Badge variant="outline" className="bg-emerald-500/10 text-emerald-400 border-emerald-800">ok</Badge>}
              </TableCell>
              <TableCell className="px-3 py-2">
                <RowActions>
                  <Button size="sm" variant="ghost" onClick={() => setEditing(h)}>{t('edit')}</Button>
                  <DeleteButton onDelete={() => remove.mutate(h.name)} />
                </RowActions>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {!isLoading && rows.length === 0 && <Empty text={t('empty')} />}
      {editing && (
        <HeartbeatForm doc={editing === 'new' ? { name: '', expectEvery: '1h' } : editing}
          isNew={editing === 'new'} onClose={() => setEditing(null)} />
      )}
    </div>
  )
}

function HeartbeatForm({ doc, isNew, onClose }: {
  doc: HeartbeatDef; isNew: boolean; onClose: () => void
}) {
  const [h, setH] = useState<HeartbeatDef>(doc)
  const set = (patch: Partial<HeartbeatDef>) => setH((prev) => ({ ...prev, ...patch }))
  const save = useSave(
    () => post('/heartbeats', {
      name: h.name, expectEvery: h.expectEvery, grace: h.grace || undefined,
      severity: h.severity || undefined, labels: h.labels,
    }),
    { invalidate: [['heartbeats']], onDone: onClose },
  )
  const beatURL = `/api/v1/heartbeats/${encodeURIComponent(h.name || '<name>')}/beat`
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{isNew ? t('create') : `${t('edit')}: ${doc.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
          <Field label={t('name')} required>
            <Input value={h.name} onChange={(e) => set({ name: e.target.value })}
              required disabled={!isNew} placeholder="backup-job" />
          </Field>
          <div className="grid grid-cols-2 gap-2">
            <Field label={t('expectEvery')} required>
              <DurationInput value={h.expectEvery} onChange={(v) => set({ expectEvery: v })} placeholder="1h" />
            </Field>
            <Field label={t('graceOptional')}>
              <DurationInput value={h.grace ?? ''} onChange={(v) => set({ grace: v || undefined })} placeholder="10m" />
            </Field>
          </div>
          <SeverityField value={(h.severity ?? 'critical') as Severity}
            onChange={(v: Severity) => set({ severity: v })} label={t('severity')} />
          <Field label={t('labels')}>
            <KVEditor value={h.labels ?? {}} onChange={(v) => set({ labels: v })}
              keyPlaceholder="team" valuePlaceholder="netops" />
          </Field>
          <div className="bg-card/60 border border-border rounded-lg p-3">
            <div className="text-xs text-muted-foreground mb-1">{t('beatUrlHint')}</div>
            <code className="text-xs text-foreground/90 break-all select-all">
              curl -H "Authorization: Bearer np_…" {beatURL}
            </code>
          </div>
          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!h.name || !h.expectEvery} />
        </form>
      </DialogContent>
    </Dialog>
  )
}
