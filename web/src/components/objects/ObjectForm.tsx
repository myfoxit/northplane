// Host/Service create + edit form and the Wizard-style batch importer
// (CMP "Monitoring Admin" object editor + "Wizard"/Massenanlage parity).
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { X, Check, Loader2 } from 'lucide-react'
import { get, post, put, APIError } from '../../api'
import type { NPObject, ObjectSpec, Kind } from '../../types'
import { Field, KVEditor, FormError, SubmitRow, useSave } from '@/components/kit'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import { t } from '../../i18n'
import {
  AddressField, CheckSection, IntervalSection, NotifySection, AdvancedSection,
} from './SpecFields'
import { cleanSpec, specOf } from './specUtil'

// Radix SelectItem value cannot be "" — sentinel stands in for the
// empty/"choose host" option and maps back to '' on change.
const NONE = '__none__'

// ObjectBody is the POST/PUT payload (internal/api ObjectBody): note the
// field is `host` (name or id), not hostId.
interface ObjectBody {
  name: string
  host?: string
  folder?: string
  labels?: Record<string, string>
  spec: ObjectSpec
}

// useHostPicker is a hook (always call unconditionally — rules of hooks);
// pass enabled to skip the request when hosts aren't needed.
function useHostPicker(enabled: boolean) {
  return useQuery({
    queryKey: ['objects', 'host-names'],
    queryFn: () => get<{ items: { id: string; name: string }[] | null }>('/hosts?limit=2000&withState=false')
      .then((r) => r.items ?? []),
    staleTime: 30_000,
    enabled,
  })
}

// ObjectForm: create when `edit` is undefined, otherwise edit (PUT with the
// object's version as If-Match; rename is rejected server-side so name is
// locked on edit).
export function ObjectForm({ kind, edit, onDone, onCancel }: {
  kind: Kind
  edit?: NPObject
  onDone: () => void
  onCancel: () => void
}) {
  const isEdit = !!edit
  const [name, setName] = useState(edit?.name ?? '')
  const [folder, setFolder] = useState(edit?.folder ?? '')
  const [labels, setLabels] = useState<Record<string, string>>(edit?.labels ?? {})
  const [host, setHost] = useState(edit?.hostId ?? '')
  const [spec, setSpec] = useState<ObjectSpec>(specOf(edit?.spec))
  // Tabs (FORM-1/3/5): default to Basis so creating a host is a name+address
  // job; check/notify/advanced config lives behind the other tabs.
  const [tab, setTab] = useState<'basis' | 'check' | 'notify' | 'advanced'>('basis')

  const patch = (p: Partial<ObjectSpec>) => setSpec((s) => ({ ...s, ...p }))
  const hosts = useHostPicker(kind === 'service')

  const save = useSave<void>(async () => {
    const body: ObjectBody = { name, folder, labels, spec: cleanSpec(spec) }
    if (kind === 'service') body.host = host
    if (isEdit) {
      return put<NPObject>(`/objects/${edit!.id}`, body, edit!.version)
    }
    return post<NPObject>(kind === 'host' ? '/hosts' : '/services', body)
  }, { invalidate: [['objects']], onDone })

  const err = save.error
  const conflict = err instanceof APIError && (err.status === 409 || err.status === 412)

  return (
    <form
      onSubmit={(e) => { e.preventDefault(); save.mutate() }}
      className="flex flex-col min-h-0 flex-1"
    >
      <Tabs value={tab} onValueChange={(v) => setTab(v as typeof tab)} className="flex flex-col min-h-0 flex-1 gap-0">
        <div className="px-6 pt-3 shrink-0">
          <TabsList className="w-full justify-start">
            <TabsTrigger value="basis">Basis</TabsTrigger>
            <TabsTrigger value="check">Prüfung</TabsTrigger>
            <TabsTrigger value="notify">Benachrichtigungen</TabsTrigger>
            <TabsTrigger value="advanced">Erweitert</TabsTrigger>
          </TabsList>
        </div>

        {/* Only this middle region scrolls; header/tabs above and the action
            bar below stay pinned (FORM-5). */}
        <div className="overflow-y-auto min-h-0 flex-1 px-6 py-4">
          <TabsContent value="basis" className="mt-0 space-y-4">
            <div className="grid grid-cols-2 gap-2">
              <Field label={t('name')} required>
                <Input value={name} onChange={(e) => setName(e.target.value)}
                  disabled={isEdit} placeholder={kind === 'host' ? 'web01' : 'http'} autoFocus={!isEdit} />
              </Field>
              <Field label={t('folder')} hint="z.B. /prod/web">
                <Input value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="/" />
              </Field>
            </div>

            {kind === 'service' && (
              <Field label={t('host')} required hint={isEdit ? 'Host kann nicht geändert werden' : undefined}>
                <Select value={host || NONE} onValueChange={(v) => setHost(v === NONE ? '' : v)} disabled={isEdit} required>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NONE}>— {t('host')} wählen —</SelectItem>
                    {(hosts.data ?? []).map((h) => (
                      <SelectItem key={h.id} value={h.id}>{h.name}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            )}

            <AddressField spec={spec} patch={patch} kind={kind} />

            <Field label={t('labels')}>
              <KVEditor value={labels} onChange={setLabels} keyPlaceholder="env" valuePlaceholder="prod" />
            </Field>
          </TabsContent>

          <TabsContent value="check" className="mt-0 space-y-4">
            <CheckSection spec={spec} patch={patch} kind={kind} compact />
            <IntervalSection spec={spec} patch={patch} kind={kind} />
          </TabsContent>

          <TabsContent value="notify" className="mt-0">
            <NotifySection spec={spec} patch={patch} kind={kind} compact />
          </TabsContent>

          <TabsContent value="advanced" className="mt-0">
            <AdvancedSection spec={spec} patch={patch} kind={kind} compact />
          </TabsContent>
        </div>
      </Tabs>

      <div className="border-t border-border px-6 py-3 shrink-0 space-y-2">
        {conflict
          ? <FormError error="Konflikt — bitte neu laden." />
          : <FormError error={err} />}
        <SubmitRow onCancel={onCancel} saving={save.isPending}
          disabled={!name || (kind === 'service' && !host)}
          label={isEdit ? t('save') : t('create')} />
      </div>
    </form>
  )
}

// ObjectFormDialog wraps ObjectForm in a Dialog. The content is a flex column
// with a pinned header + footer action bar; only the tab body scrolls, so on a
// tall form the title and Anlegen/Speichern stay reachable (FORM-5).
export function ObjectFormDialog({ open, kind, edit, onClose }: {
  open: boolean; kind: Kind; edit?: NPObject; onClose: () => void
}) {
  if (!open) return null
  const title = edit
    ? `${t('edit')}: ${edit.name}`
    : kind === 'host' ? t('newHost') : t('newService')
  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl sm:max-w-2xl p-0 gap-0 flex flex-col overflow-hidden">
        <DialogHeader className="px-6 pt-6 pb-3 border-b border-border shrink-0">
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <ObjectForm kind={kind} edit={edit} onDone={onClose} onCancel={onClose} />
      </DialogContent>
    </Dialog>
  )
}

// ——— Batch importer (Wizard / Massenanlage) ——————————————————————
// One host per line: "name address [tmpl,tmpl] [k=v,k=v]". Bracketed groups
// are optional; the first [...] is templates, the second is labels. Default
// folder + check command apply to every row. Posts to /objects:batch.
interface ParsedRow {
  name: string
  address?: string
  templates: string[]
  labels: Record<string, string>
  error?: string
}

function parseLine(line: string): ParsedRow | null {
  const raw = line.trim()
  if (!raw || raw.startsWith('#')) return null
  const brackets: string[] = []
  const stripped = raw.replace(/\[([^\]]*)\]/g, (_, inner) => { brackets.push(inner); return '' })
  const tokens = stripped.split(/\s+/).filter(Boolean)
  const name = tokens[0] ?? ''
  const address = tokens[1]
  const templates = (brackets[0] ?? '').split(',').map((s) => s.trim()).filter(Boolean)
  const labels: Record<string, string> = {}
  for (const pair of (brackets[1] ?? '').split(',')) {
    const [k, ...rest] = pair.split('=')
    if (k?.trim()) labels[k.trim()] = rest.join('=').trim()
  }
  const row: ParsedRow = { name, address, templates, labels }
  if (!name) row.error = 'kein Name'
  return row
}

interface BatchResult {
  created: number
  failed: number
  results: { name: string; id?: string; error?: string }[]
}

export function BatchAddDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [text, setText] = useState('')
  const [kind, setKind] = useState<Kind>('host')
  const [folder, setFolder] = useState('')
  const [checkCommand, setCheckCommand] = useState('builtin:icmp')
  const [defHost, setDefHost] = useState('')
  const [mode, setMode] = useState<'all-or-nothing' | 'partial'>('partial')
  const [result, setResult] = useState<BatchResult | null>(null)

  const hosts = useHostPicker(kind === 'service')
  const rows = text.split('\n').map(parseLine).filter((r): r is ParsedRow => r !== null)

  const save = useSave<void>(async () => {
    const bodies = rows.filter((r) => !r.error).map((r) => {
      const spec: ObjectSpec = cleanSpec({
        ...(r.address ? { address: r.address } : {}),
        ...(checkCommand ? { checkCommand } : {}),
        templates: r.templates,
      })
      const body: { name: string; host?: string; folder?: string; labels?: Record<string, string>; spec: ObjectSpec } = {
        name: r.name, folder, labels: r.labels, spec,
      }
      if (kind === 'service') body.host = defHost
      return body
    })
    const payload = kind === 'host'
      ? { mode, hosts: bodies }
      : { mode, services: bodies }
    const res = await post<BatchResult>('/objects:batch', payload)
    setResult(res)
  }, { invalidate: [['objects']] })

  const validCount = rows.filter((r) => !r.error).length

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-4xl">
        <DialogHeader><DialogTitle>{t('batchAdd')}</DialogTitle></DialogHeader>
        <div className="space-y-4">
        <div className="grid grid-cols-2 lg:grid-cols-4 gap-2">
          <Field label={t('type')}>
            <Select value={kind} onValueChange={(v) => { setKind(v as Kind); setResult(null) }}>
              <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="host">{t('host')}</SelectItem>
                <SelectItem value="service">{t('service')}</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('folder')}>
            <Input value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="/" />
          </Field>
          <Field label={t('checkCommand')}>
            <Input value={checkCommand} onChange={(e) => setCheckCommand(e.target.value)} placeholder="builtin:icmp" />
          </Field>
          <Field label="Modus">
            <Select value={mode} onValueChange={(v) => setMode(v as typeof mode)}>
              <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="partial">partiell (Teilerfolg)</SelectItem>
                <SelectItem value="all-or-nothing">alles-oder-nichts</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        </div>

        {kind === 'service' && (
          <Field label={t('host')} required hint="Gilt für alle Zeilen">
            <Select value={defHost || NONE} onValueChange={(v) => setDefHost(v === NONE ? '' : v)} required>
              <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value={NONE}>— {t('host')} wählen —</SelectItem>
                {(hosts.data ?? []).map((h) => <SelectItem key={h.id} value={h.id}>{h.name}</SelectItem>)}
              </SelectContent>
            </Select>
          </Field>
        )}

        <Field label="Zeilen" hint="name adresse [template,template] [key=value,…] — eine pro Zeile">
          <Textarea
            value={text}
            onChange={(e) => { setText(e.target.value); setResult(null) }}
            placeholder={'web01 10.0.0.1 [generic-host] [env=prod,team=web]\ndb01 10.0.0.2 [generic-host,linux]'}
            rows={6}
            className="font-mono"
          />
        </Field>

        {rows.length > 0 && !result && (
          <div className="border border-border rounded-lg overflow-hidden">
            <div className="text-xs text-muted-foreground px-3 py-1.5 border-b border-border">
              {t('preview')} — {validCount} gültig{rows.length - validCount > 0 ? `, ${rows.length - validCount} fehlerhaft` : ''}
            </div>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('name')}</TableHead>
                  <TableHead>{t('address')}</TableHead>
                  <TableHead>{t('templates')}</TableHead>
                  <TableHead>{t('labels')}</TableHead>
                  <TableHead></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((r, i) => (
                  <TableRow key={i} className={r.error ? 'bg-red-500/5' : ''}>
                    <TableCell className="px-3 py-1.5 text-foreground font-medium">{r.name || '—'}</TableCell>
                    <TableCell className="px-3 py-1.5 text-muted-foreground font-mono text-xs">{r.address ?? '—'}</TableCell>
                    <TableCell className="px-3 py-1.5 text-muted-foreground font-mono text-xs">{r.templates.join(', ') || '—'}</TableCell>
                    <TableCell className="px-3 py-1.5 text-muted-foreground font-mono text-xs">
                      {Object.entries(r.labels).map(([k, v]) => `${k}=${v}`).join(', ') || '—'}
                    </TableCell>
                    <TableCell className="px-3 py-1.5 text-xs text-red-400">{r.error ?? ''}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}

        {result && (
          <div className="border border-border rounded-lg overflow-hidden">
            <div className="text-xs px-3 py-1.5 border-b border-border">
              <span className="text-emerald-400">{result.created} erstellt</span>
              {result.failed > 0 && <span className="text-red-400 ml-3">{result.failed} fehlgeschlagen</span>}
            </div>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('name')}</TableHead>
                  <TableHead>{t('status')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {result.results.map((r, i) => (
                  <TableRow key={i}>
                    <TableCell className="px-3 py-1.5 text-foreground">{r.name}</TableCell>
                    <TableCell className="px-3 py-1.5 text-xs">
                      {r.error
                        ? <span className="text-red-400 inline-flex items-center gap-1"><X size={13} /> {r.error}</span>
                        : <span className="text-emerald-400 inline-flex items-center gap-1"><Check size={13} /> erstellt</span>}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}

        <FormError error={save.error} />
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" type="button" onClick={onClose}>
            {result ? t('close') : t('cancel')}
          </Button>
          {!result && (
            <Button
              variant="default" type="button"
              onClick={() => save.mutate()}
              disabled={save.isPending || validCount === 0 || (kind === 'service' && !defHost)}
            >
              {save.isPending ? <Loader2 className="animate-spin" size={14} /> : `${validCount} ${t('create')}`}
            </Button>
          )}
        </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
