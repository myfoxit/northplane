// Sites (SPEC §7.7 Variante B): Kunden-Standorte, deren Edge-Instanz
// sich von außen verbindet — Status-Heartbeats hoch, Config-Bundle
// runter. Die Tabelle zeigt Verbundenheit + zuletzt gemeldete Zähler;
// der Editor pflegt das Bundle, das die Edge-Instanz zieht und anwendet.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get, resourceApi, fmtAgo, type ListResponse } from '../../api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Checkbox } from '@/components/ui/checkbox'
import { Empty, Field, FormError, SubmitRow, DeleteButton, useSave } from '@/components/kit'
import { t } from '../../i18n'
import { StatusBadge, TableActions, RowActions } from './common'

interface Site {
  name: string
  description?: string
  bundle?: string
  disabled?: boolean
}
interface SiteView extends Site {
  connected: boolean
  status: {
    lastSeenAt?: string
    version?: string
    bundleEtag?: string
    applyError?: string
    stats?: Record<string, number>
    sourceIp?: string
  }
}

const sites = resourceApi<Site>('sites')
const OVERVIEW = ['sites', 'overview'] as const

export function SitesTab() {
  const { data, isLoading } = useQuery({
    queryKey: [...OVERVIEW],
    queryFn: () => get<ListResponse<SiteView>>('/sites:overview').then((r) => r.items ?? []),
    refetchInterval: 30_000,
  })
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<string | null>(null)
  const remove = useSave((name: string) => sites.remove(name), {
    invalidate: [[...OVERVIEW]],
  })
  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setCreating(true)} label={t('create')} />
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('name')}</TableHead>
            <TableHead>{t('status')}</TableHead>
            <TableHead>{t('lastSeen')}</TableHead>
            <TableHead>Version</TableHead>
            <TableHead>Hosts/Services</TableHead>
            <TableHead>{t('openAlerts')}</TableHead>
            <TableHead>{t('configuration')}</TableHead>
            <TableHead className="text-right">{t('actions')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(data ?? []).map((s) => (
            <TableRow key={s.name}>
              <TableCell className="px-3 py-2">
                <div className="text-foreground">{s.name}</div>
                {s.description && <div className="text-xs text-muted-foreground">{s.description}</div>}
              </TableCell>
              <TableCell className="px-3 py-2">
                {s.disabled ? <StatusBadge kind="disabled" />
                  : s.connected
                    ? <Badge variant="outline" className="bg-emerald-500/10 text-emerald-400 border-emerald-800">{t('connected')}</Badge>
                    : <Badge variant="outline" className="bg-red-500/10 text-red-400 border-red-800">{t('disconnected')}</Badge>}
              </TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground">{fmtAgo(s.status.lastSeenAt)}</TableCell>
              <TableCell className="px-3 py-2 text-xs font-mono text-muted-foreground">{s.status.version ?? '—'}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground">
                {s.status.stats ? `${s.status.stats.hosts ?? 0} / ${s.status.stats.services ?? 0}` : '—'}
              </TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground">{s.status.stats?.alertsOpen ?? '—'}</TableCell>
              <TableCell className="px-3 py-2">
                {s.status.applyError
                  ? <Badge variant="outline" className="bg-red-500/10 text-red-400 border-red-800" title={s.status.applyError}>{t('applyError')}</Badge>
                  : s.status.bundleEtag
                    ? <Badge variant="outline" className="bg-emerald-500/10 text-emerald-400 border-emerald-800">{t('applied')}</Badge>
                    : <Badge variant="outline" className="bg-muted text-muted-foreground border-input">—</Badge>}
              </TableCell>
              <TableCell className="px-3 py-2">
                <RowActions>
                  <Button variant="ghost" size="sm" onClick={() => setEditing(s.name)}>{t('edit')}</Button>
                  <DeleteButton onDelete={() => remove.mutate(s.name)} />
                </RowActions>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}
      <p className="text-xs text-muted-foreground">
        {t('edgeConnectionHint')}{' '}
        <code className="font-mono">federation: {'{'} mode: edge, mainUrl: …, token: np_…, site: &lt;Name&gt; {'}'}</code>
      </p>
      {creating && <SiteDialog onClose={() => setCreating(false)} />}
      {editing && <SiteDialog name={editing} onClose={() => setEditing(null)} />}
    </div>
  )
}

function SiteDialog({ name, onClose }: { name?: string; onClose: () => void }) {
  const isEdit = Boolean(name)
  const existing = useQuery({
    queryKey: ['sites', 'doc', name],
    queryFn: () => sites.get(name!),
    enabled: isEdit,
  })
  const [form, setForm] = useState<Site | null>(isEdit ? null : { name: '' })
  const doc = form ?? (existing.data ? { ...existing.data.data } : null)
  const save = useSave(
    (d: Site) => isEdit ? sites.update(name!, d, existing.data?.etag ?? 0) : sites.create(d),
    { invalidate: [[...OVERVIEW]], onDone: onClose },
  )
  if (!doc) return null
  const set = (patch: Partial<Site>) => setForm({ ...doc, ...patch })
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{`${t('sites')} — ${isEdit ? t('edit') : t('create')}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); save.mutate(doc) }} className="space-y-3">
          <Field label={t('name')} required>
            <Input value={doc.name} onChange={(e) => set({ name: e.target.value })} disabled={isEdit} required />
          </Field>
          <Field label={t('description')}>
            <Input value={doc.description ?? ''} onChange={(e) => set({ description: e.target.value })} />
          </Field>
          <Field label={t('configBundleYaml')} hint={t('configBundleHint')}>
            <Textarea
              value={doc.bundle ?? ''}
              onChange={(e) => set({ bundle: e.target.value })}
              placeholder={'kind: Host\nmetadata:\n  name: web-01\nspec:\n  address: 10.0.0.10\n---\n…'}
              className="font-mono text-xs min-h-48"
            />
          </Field>
          <label className="flex items-center gap-2 text-sm">
            <Checkbox checked={doc.disabled ?? false} onCheckedChange={(v) => set({ disabled: v === true })} />
            {t('disabledEdgeReject')}
          </label>
          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!doc.name} />
        </form>
      </DialogContent>
    </Dialog>
  )
}
