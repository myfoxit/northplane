// Configuration: templates, check commands, time periods (CMP Wizard /
// "Monitoring Admin" parity). Each tab is a Table + create/edit Dialog,
// optimistic-locking aware (ETag/If-Match via resourceApi).
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { resourceApi, APIError } from '../api'
import type { Template, CheckCommand, TimePeriod, ObjectSpec, Kind } from '../types'
import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle, CardAction, CardContent } from '@/components/ui/card'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Empty, Spinner, Field, KVEditor, ListEditor, DurationInput, FormError, SubmitRow, useSave, DeleteButton, DuplicateButton } from '@/components/kit'
import { copyName } from '@/lib/duplicate'
import { SpecFields } from '../components/objects/SpecFields'
import { cleanSpec } from '../components/objects/specUtil'
import { t } from '../i18n'

const tabs = ['templates', 'check-commands', 'time-periods'] as const
type Tab = typeof tabs[number]
const tabLabels: Record<Tab, string> = {
  templates: t('templates'),
  'check-commands': t('checkCommands'),
  'time-periods': t('timePeriods'),
}

export function TemplatesPage() {
  const [tab, setTab] = useState<Tab>('templates')
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-bold">{t('templates')} &amp; {t('configuration')}</h1>
      <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
        <TabsList>
          {tabs.map((tb) => <TabsTrigger key={tb} value={tb}>{tabLabels[tb]}</TabsTrigger>)}
        </TabsList>
      </Tabs>
      {tab === 'templates' && <TemplatesTab />}
      {tab === 'check-commands' && <CheckCommandsTab />}
      {tab === 'time-periods' && <TimePeriodsTab />}
    </div>
  )
}

// Radix SelectItem value cannot be "" — sentinel for the empty template
// kind ("Host & Service"), mapped back to '' for the document.
const BOTH = '__both__'

// 409/412 → standard German conflict copy.
function conflictMessage(err: unknown): unknown {
  if (err instanceof APIError && (err.status === 409 || err.status === 412)) {
    return t('conflictReload')
  }
  return err
}

// ——— Templates ——————————————————————————————————————————————————
const templatesApi = resourceApi<Template>('templates')
// Mutable invalidation set (useSave wants string[][]; queryKey is readonly).
// Includes the name-suggestion query used by SpecFields datalists.
const templatesInval: string[][] = [[...templatesApi.queryKey], ['resources', 'templates', 'names']]

function TemplatesTab() {
  const { data, isLoading } = useQuery({ queryKey: templatesApi.queryKey, queryFn: templatesApi.list })
  const [editing, setEditing] = useState<{ name: string; copy?: boolean } | 'new' | null>(null)
  const remove = useSave<string>((name) => templatesApi.remove(name), { invalidate: templatesInval })

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('templates')}</CardTitle>
        <CardAction><Button variant="default" size="sm" onClick={() => setEditing('new')}>+ {t('create')}</Button></CardAction>
      </CardHeader>
      <CardContent>
        {isLoading ? <Spinner /> : (data?.length ?? 0) === 0 ? <Empty text={t('empty')} /> : (
          <Table>
            <TableHeader>
              <TableRow>
                {[t('name'), t('type'), t('labels'), t('templates'), ''].map((h, i) => <TableHead key={i}>{h}</TableHead>)}
              </TableRow>
            </TableHeader>
            <TableBody>
              {data!.map((tpl) => (
                <TableRow key={tpl.name}>
                  <TableCell className="text-foreground font-medium">{tpl.name}</TableCell>
                  <TableCell>
                    <Badge variant="outline" className="bg-muted text-muted-foreground border-input">{tpl.kind || t('both')}</Badge>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground font-mono">
                    {Object.entries(tpl.labels ?? {}).map(([k, v]) => `${k}=${v}`).join(', ') || '—'}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground font-mono">{(tpl.spec?.templates ?? []).join(', ') || '—'}</TableCell>
                  <TableCell className="text-right whitespace-nowrap">
                    <Button size="sm" variant="ghost" onClick={() => setEditing({ name: tpl.name })}>{t('edit')}</Button>
                    <DuplicateButton onClick={() => setEditing({ name: tpl.name, copy: true })} />
                    <DeleteButton onDelete={() => remove.mutate(tpl.name)} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        {editing && (
          <TemplateDialog
            name={editing === 'new' ? null : editing.name}
            copy={editing !== 'new' && editing.copy}
            existing={(data ?? []).map((x) => x.name)}
            onClose={() => setEditing(null)}
          />
        )}
      </CardContent>
    </Card>
  )
}

// TemplateDialog: `name` null → create; set → edit; set + `copy` → create a
// duplicate seeded from that template (same hydration path, but the draft
// name is a fresh "-copy" and the save is a POST).
function TemplateDialog({ name, copy, existing, onClose }: {
  name: string | null; copy?: boolean; existing?: string[]; onClose: () => void
}) {
  const hasSource = name !== null
  const isEdit = hasSource && !copy
  // Load existing (with ETag) for edit — and as the seed for a duplicate.
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...templatesApi.queryKey, name],
    queryFn: () => templatesApi.get(name!),
    enabled: hasSource,
  })

  const [draftName, setDraftName] = useState('')
  const [kind, setKind] = useState<Kind | ''>('')
  const [labels, setLabels] = useState<Record<string, string>>({})
  const [spec, setSpec] = useState<ObjectSpec>({})
  const [ready, setReady] = useState(!hasSource)

  // Hydrate form once the document arrives (edit / copy mode).
  if (hasSource && loaded && !ready) {
    setDraftName(copy ? copyName(loaded.data.name, existing) : loaded.data.name)
    setKind(loaded.data.kind ?? '')
    setLabels(loaded.data.labels ?? {})
    setSpec(loaded.data.spec ?? {})
    setReady(true)
  }

  const save = useSave<void>(async () => {
    const doc: Template = {
      name: isEdit ? name! : draftName,
      ...(kind ? { kind } : {}),
      labels,
      spec: cleanSpec(spec),
    }
    if (isEdit) return templatesApi.update(name!, doc, loaded!.etag)
    return templatesApi.create(doc)
  }, { invalidate: templatesInval, onDone: onClose })

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl max-h-[90vh] overflow-y-auto">
        <DialogHeader><DialogTitle>{copy ? `${t('duplicate')}: ${name}` : isEdit ? `${t('edit')}: ${name}` : `${t('templates')} — ${t('create')}`}</DialogTitle></DialogHeader>
        {hasSource && isLoading ? <Spinner /> : (
          <form onSubmit={(e) => { e.preventDefault(); save.mutate() }} className="space-y-4">
            <div className="grid grid-cols-2 gap-2">
              <Field label={t('name')} required>
                <Input value={isEdit ? name! : draftName} disabled={isEdit}
                  onChange={(e) => setDraftName(e.target.value)} placeholder="generic-host" autoFocus={!isEdit} />
              </Field>
              <Field label={t('type')} hint={t('emptyHostService')}>
                <Select value={kind || BOTH} onValueChange={(v) => setKind((v === BOTH ? '' : v) as Kind | '')}>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value={BOTH}>{t('both')}</SelectItem>
                    <SelectItem value="host">{t('host')}</SelectItem>
                    <SelectItem value="service">{t('service')}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </div>
            <Field label={t('labels')}>
              <KVEditor value={labels} onChange={setLabels} keyPlaceholder="env" valuePlaceholder="prod" />
            </Field>
            <SpecFields spec={spec} onChange={setSpec} kind={kind} />
            <FormError error={conflictMessage(save.error)} />
            <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!(isEdit ? name : draftName)}
              label={isEdit ? t('save') : t('create')} />
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

// ——— Check commands ——————————————————————————————————————————————
const commandsApi = resourceApi<CheckCommand>('check-commands')
const commandsInval: string[][] = [[...commandsApi.queryKey]]

function CheckCommandsTab() {
  const { data, isLoading } = useQuery({ queryKey: commandsApi.queryKey, queryFn: commandsApi.list })
  const [editing, setEditing] = useState<{ name: string; copy?: boolean } | 'new' | null>(null)
  const remove = useSave<string>((name) => commandsApi.remove(name), { invalidate: commandsInval })

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('checkCommands')}</CardTitle>
        <CardAction><Button variant="default" size="sm" onClick={() => setEditing('new')}>+ {t('create')}</Button></CardAction>
      </CardHeader>
      <CardContent>
        {isLoading ? <Spinner /> : (data?.length ?? 0) === 0 ? <Empty text={t('empty')} /> : (
          <Table>
            <TableHeader>
              <TableRow>
                {[t('name'), t('type'), t('commandLine'), 'Env', t('timeout'), ''].map((h, i) => <TableHead key={i}>{h}</TableHead>)}
              </TableRow>
            </TableHeader>
            <TableBody>
              {data!.map((cmd) => (
                <TableRow key={cmd.name}>
                  <TableCell className="text-foreground font-medium">{cmd.name}</TableCell>
                  <TableCell><Badge variant="outline" className="bg-muted text-muted-foreground border-input">{cmd.type}</Badge></TableCell>
                  <TableCell className="text-xs text-muted-foreground font-mono truncate max-w-md">{(cmd.line ?? []).join(' ') || '—'}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{cmd.env ? t('yes') : '—'}</TableCell>
                  <TableCell className="text-xs text-muted-foreground font-mono">{cmd.timeout || '—'}</TableCell>
                  <TableCell className="text-right whitespace-nowrap">
                    <Button size="sm" variant="ghost" onClick={() => setEditing({ name: cmd.name })}>{t('edit')}</Button>
                    <DuplicateButton onClick={() => setEditing({ name: cmd.name, copy: true })} />
                    <DeleteButton onDelete={() => remove.mutate(cmd.name)} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        {editing && (
          <CheckCommandDialog name={editing === 'new' ? null : editing.name}
            copy={editing !== 'new' && editing.copy} existing={(data ?? []).map((x) => x.name)}
            onClose={() => setEditing(null)} />
        )}
      </CardContent>
    </Card>
  )
}

// CheckCommandDialog: `name` null → create; set → edit; set + `copy` → a
// duplicate seeded from that command under a fresh name (POST).
function CheckCommandDialog({ name, copy, existing, onClose }: {
  name: string | null; copy?: boolean; existing?: string[]; onClose: () => void
}) {
  const hasSource = name !== null
  const isEdit = hasSource && !copy
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...commandsApi.queryKey, name],
    queryFn: () => commandsApi.get(name!),
    enabled: hasSource,
  })

  const [draftName, setDraftName] = useState('')
  const [type, setType] = useState<CheckCommand['type']>('exec')
  const [line, setLine] = useState<string[]>([])
  const [env, setEnv] = useState(false)
  const [timeout, setTimeoutV] = useState('')
  const [ready, setReady] = useState(!hasSource)

  if (hasSource && loaded && !ready) {
    setDraftName(copy ? copyName(loaded.data.name, existing) : loaded.data.name)
    setType(loaded.data.type)
    setLine(loaded.data.line ?? [])
    setEnv(!!loaded.data.env)
    setTimeoutV(loaded.data.timeout ?? '')
    setReady(true)
  }

  const save = useSave<void>(async () => {
    const doc: CheckCommand = {
      name: isEdit ? name! : draftName,
      type,
      line,
      env,
      ...(timeout ? { timeout } : {}),
    }
    if (isEdit) return commandsApi.update(name!, doc, loaded!.etag)
    return commandsApi.create(doc)
  }, { invalidate: commandsInval, onDone: onClose })

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl max-h-[90vh] overflow-y-auto">
        <DialogHeader><DialogTitle>{copy ? `${t('duplicate')}: ${name}` : isEdit ? `${t('edit')}: ${name}` : `${t('checkCommand')} — ${t('create')}`}</DialogTitle></DialogHeader>
        {hasSource && isLoading ? <Spinner /> : (
          <form onSubmit={(e) => { e.preventDefault(); save.mutate() }} className="space-y-4">
            <div className="grid grid-cols-2 gap-2">
              <Field label={t('name')} required>
                <Input value={isEdit ? name! : draftName} disabled={isEdit}
                  onChange={(e) => setDraftName(e.target.value)} placeholder="check_postgres" autoFocus={!isEdit} />
              </Field>
              <Field label={t('type')}>
                <Select value={type} onValueChange={(v) => setType(v as CheckCommand['type'])}>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="exec">exec</SelectItem>
                    <SelectItem value="builtin">builtin</SelectItem>
                    <SelectItem value="agent">agent</SelectItem>
                    <SelectItem value="passive">passive</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </div>
            <Field label={t('commandLine')} hint={t('commandLineHint')}>
              <ListEditor value={line} onChange={setLine} placeholder="check_postgres" />
            </Field>
            <div className="grid grid-cols-2 gap-2 items-end">
              <Field label={t('timeout')}>
                <DurationInput value={timeout} onChange={setTimeoutV} placeholder="30s" />
              </Field>
              <div className="pb-1.5">
                <Label className="cursor-pointer">
                  <Switch checked={env} onCheckedChange={setEnv} />
                  <span className="text-sm text-foreground/90">{t('exportEnvMacros')}</span>
                </Label>
              </div>
            </div>
            <FormError error={conflictMessage(save.error)} />
            <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!(isEdit ? name : draftName)}
              label={isEdit ? t('save') : t('create')} />
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

// ——— Time periods ————————————————————————————————————————————————
const periodsApi = resourceApi<TimePeriod>('time-periods')
const periodsInval: string[][] = [[...periodsApi.queryKey], ['resources', 'time-periods', 'names']]
const WEEKDAYS = ['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday'] as const
const WEEKDAY_DE: Record<typeof WEEKDAYS[number], string> = {
  monday: t('weekdayMonday'), tuesday: t('weekdayTuesday'), wednesday: t('weekdayWednesday'), thursday: t('weekdayThursday'),
  friday: t('weekdayFriday'), saturday: t('weekdaySaturday'), sunday: t('weekdaySunday'),
}

function TimePeriodsTab() {
  const { data, isLoading } = useQuery({ queryKey: periodsApi.queryKey, queryFn: periodsApi.list })
  const [editing, setEditing] = useState<{ name: string; copy?: boolean } | 'new' | null>(null)
  const remove = useSave<string>((name) => periodsApi.remove(name), { invalidate: periodsInval })

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('timePeriods')}</CardTitle>
        <CardAction><Button variant="default" size="sm" onClick={() => setEditing('new')}>+ {t('create')}</Button></CardAction>
      </CardHeader>
      <CardContent>
        {isLoading ? <Spinner /> : (data?.length ?? 0) === 0 ? <Empty text={t('empty')} /> : (
          <Table>
            <TableHeader>
              <TableRow>
                {[t('name'), 'Alias', t('days'), t('exceptions'), ''].map((h, i) => <TableHead key={i}>{h}</TableHead>)}
              </TableRow>
            </TableHeader>
            <TableBody>
              {data!.map((tp) => (
                <TableRow key={tp.name}>
                  <TableCell className="text-foreground font-medium">{tp.name}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{tp.alias || '—'}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{Object.keys(tp.days ?? {}).length || '—'}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{Object.keys(tp.exceptions ?? {}).length || '—'}</TableCell>
                  <TableCell className="text-right whitespace-nowrap">
                    <Button size="sm" variant="ghost" onClick={() => setEditing({ name: tp.name })}>{t('edit')}</Button>
                    <DuplicateButton onClick={() => setEditing({ name: tp.name, copy: true })} />
                    <DeleteButton onDelete={() => remove.mutate(tp.name)} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        {editing && (
          <TimePeriodDialog name={editing === 'new' ? null : editing.name}
            copy={editing !== 'new' && editing.copy} existing={(data ?? []).map((x) => x.name)}
            onClose={() => setEditing(null)} />
        )}
      </CardContent>
    </Card>
  )
}

// TimePeriodDialog: `name` null → create; set → edit; set + `copy` → a
// duplicate seeded from that period under a fresh name (POST).
function TimePeriodDialog({ name, copy, existing, onClose }: {
  name: string | null; copy?: boolean; existing?: string[]; onClose: () => void
}) {
  const hasSource = name !== null
  const isEdit = hasSource && !copy
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...periodsApi.queryKey, name],
    queryFn: () => periodsApi.get(name!),
    enabled: hasSource,
  })

  const [draftName, setDraftName] = useState('')
  const [alias, setAlias] = useState('')
  const [days, setDays] = useState<Record<string, string[]>>({})
  const [exceptions, setExceptions] = useState<Record<string, string>>({}) // date → "r,r" comma string
  const [exclude, setExclude] = useState<string[]>([])
  const [ready, setReady] = useState(!hasSource)

  if (hasSource && loaded && !ready) {
    setDraftName(copy ? copyName(loaded.data.name, existing) : loaded.data.name)
    setAlias(loaded.data.alias ?? '')
    setDays(loaded.data.days ?? {})
    // exceptions stored as date → string[]; flatten to comma string for the KV editor.
    const ex: Record<string, string> = {}
    for (const [k, v] of Object.entries(loaded.data.exceptions ?? {})) ex[k] = (v ?? []).join(',')
    setExceptions(ex)
    setExclude(loaded.data.exclude ?? [])
    setReady(true)
  }

  const save = useSave<void>(async () => {
    // Drop empty weekday entries; re-inflate exceptions to string arrays.
    const cleanDays: Record<string, string[]> = {}
    for (const [k, v] of Object.entries(days)) if (v.length) cleanDays[k] = v
    const cleanEx: Record<string, string[]> = {}
    for (const [k, v] of Object.entries(exceptions)) {
      const ranges = v.split(',').map((s) => s.trim()).filter(Boolean)
      if (ranges.length) cleanEx[k] = ranges
    }
    const doc: TimePeriod = {
      name: isEdit ? name! : draftName,
      ...(alias ? { alias } : {}),
      ...(Object.keys(cleanDays).length ? { days: cleanDays } : {}),
      ...(Object.keys(cleanEx).length ? { exceptions: cleanEx } : {}),
      ...(exclude.length ? { exclude } : {}),
    }
    if (isEdit) return periodsApi.update(name!, doc, loaded!.etag)
    return periodsApi.create(doc)
  }, { invalidate: periodsInval, onDone: onClose })

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl max-h-[90vh] overflow-y-auto">
        <DialogHeader><DialogTitle>{copy ? `${t('duplicate')}: ${name}` : isEdit ? `${t('edit')}: ${name}` : `${t('timePeriod')} — ${t('create')}`}</DialogTitle></DialogHeader>
        {hasSource && isLoading ? <Spinner /> : (
          <form onSubmit={(e) => { e.preventDefault(); save.mutate() }} className="space-y-4">
            <div className="grid grid-cols-2 gap-2">
              <Field label={t('name')} required>
                <Input value={isEdit ? name! : draftName} disabled={isEdit}
                  onChange={(e) => setDraftName(e.target.value)} placeholder="business-hours" autoFocus={!isEdit} />
              </Field>
              <Field label="Alias">
                <Input value={alias} onChange={(e) => setAlias(e.target.value)} placeholder={t('businessHoursPlaceholder')} />
              </Field>
            </div>

            <div className="border border-border rounded-lg p-3 space-y-2">
              <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">{t('weekdays')}</div>
              {WEEKDAYS.map((wd) => (
                <Field key={wd} label={WEEKDAY_DE[wd]} hint={t('rangesHint')}>
                  <ListEditor
                    value={days[wd] ?? []}
                    onChange={(v) => setDays((d) => ({ ...d, [wd]: v }))}
                    placeholder="09:00-17:00"
                  />
                </Field>
              ))}
            </div>

            <Field label={t('exceptions')} hint={t('exceptionsHint')}>
              <KVEditor value={exceptions} onChange={setExceptions} keyPlaceholder="2026-12-24" valuePlaceholder="00:00-00:00" />
            </Field>

            <Field label={t('exclusion')} hint={t('exclusionHint')}>
              <ListEditor value={exclude} onChange={setExclude} placeholder="holidays" />
            </Field>

            <FormError error={conflictMessage(save.error)} />
            <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!(isEdit ? name : draftName)}
              label={isEdit ? t('save') : t('create')} />
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
