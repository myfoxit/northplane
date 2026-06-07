// Configuration: templates, check commands, time periods (CMP Wizard /
// "Monitoring Admin" parity). Each tab is a Table + create/edit Dialog,
// optimistic-locking aware (ETag/If-Match via resourceApi).
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { resourceApi, APIError } from '../api'
import type { Template, CheckCommand, TimePeriod, ObjectSpec, Kind } from '../types'
import { Card, Table, Button, Empty, Dialog, Badge, TabBar, Spinner } from '../components/ui'
import { Field, Select, KVEditor, ListEditor, Toggle, DurationInput, FormError, SubmitRow, useSave, DeleteButton } from '../components/forms'
import { Input } from '../components/ui'
import { SpecFields, cleanSpec } from '../components/objects/SpecFields'
import { t } from '../i18n'

const tabs = ['templates', 'check-commands', 'time-periods'] as const
type Tab = typeof tabs[number]
const tabLabels: Record<Tab, string> = {
  templates: t('templates'),
  'check-commands': 'Check-Kommandos',
  'time-periods': 'Zeiträume',
}

export function TemplatesPage() {
  const [tab, setTab] = useState<Tab>('templates')
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-bold">{t('templates')} &amp; Konfiguration</h1>
      <TabBar tabs={tabs} value={tab} onChange={setTab} labels={(tb) => tabLabels[tb]} />
      {tab === 'templates' && <TemplatesTab />}
      {tab === 'check-commands' && <CheckCommandsTab />}
      {tab === 'time-periods' && <TimePeriodsTab />}
    </div>
  )
}

// 409/412 → standard German conflict copy.
function conflictMessage(err: unknown): unknown {
  if (err instanceof APIError && (err.status === 409 || err.status === 412)) {
    return 'Konflikt — bitte neu laden.'
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
  const [editing, setEditing] = useState<{ name: string } | 'new' | null>(null)
  const remove = useSave<string>((name) => templatesApi.remove(name), { invalidate: templatesInval })

  return (
    <Card title={t('templates')} actions={<Button variant="primary" size="sm" onClick={() => setEditing('new')}>+ {t('create')}</Button>}>
      {isLoading ? <Spinner /> : (data?.length ?? 0) === 0 ? <Empty text={t('empty')} /> : (
        <Table head={[t('name'), t('type'), t('labels'), t('templates'), '']}>
          {data!.map((tpl) => (
            <tr key={tpl.name}>
              <td className="px-3 py-2 text-slate-200 font-medium">{tpl.name}</td>
              <td className="px-3 py-2">
                <Badge className="bg-slate-800 text-slate-400 border-slate-700">{tpl.kind || 'beide'}</Badge>
              </td>
              <td className="px-3 py-2 text-xs text-slate-500 font-mono">
                {Object.entries(tpl.labels ?? {}).map(([k, v]) => `${k}=${v}`).join(', ') || '—'}
              </td>
              <td className="px-3 py-2 text-xs text-slate-500 font-mono">{(tpl.spec?.templates ?? []).join(', ') || '—'}</td>
              <td className="px-3 py-2 text-right whitespace-nowrap">
                <Button size="sm" variant="ghost" onClick={() => setEditing({ name: tpl.name })}>{t('edit')}</Button>
                <DeleteButton onDelete={() => remove.mutate(tpl.name)} />
              </td>
            </tr>
          ))}
        </Table>
      )}
      {editing && (
        <TemplateDialog
          name={editing === 'new' ? null : editing.name}
          onClose={() => setEditing(null)}
        />
      )}
    </Card>
  )
}

function TemplateDialog({ name, onClose }: { name: string | null; onClose: () => void }) {
  const isEdit = name !== null
  // Load existing (with ETag) for edit.
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...templatesApi.queryKey, name],
    queryFn: () => templatesApi.get(name!),
    enabled: isEdit,
  })

  const [draftName, setDraftName] = useState('')
  const [kind, setKind] = useState<Kind | ''>('')
  const [labels, setLabels] = useState<Record<string, string>>({})
  const [spec, setSpec] = useState<ObjectSpec>({})
  const [ready, setReady] = useState(!isEdit)

  // Hydrate form once the document arrives (edit mode).
  if (isEdit && loaded && !ready) {
    setDraftName(loaded.data.name)
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
    <Dialog open onClose={onClose} title={isEdit ? `${t('edit')}: ${name}` : `${t('templates')} — ${t('create')}`} size="lg">
      {isEdit && isLoading ? <Spinner /> : (
        <form onSubmit={(e) => { e.preventDefault(); save.mutate() }} className="space-y-4">
          <div className="grid grid-cols-2 gap-2">
            <Field label={t('name')} required>
              <Input value={isEdit ? name! : draftName} disabled={isEdit}
                onChange={(e) => setDraftName(e.target.value)} placeholder="generic-host" autoFocus={!isEdit} />
            </Field>
            <Field label={t('type')} hint="leer = Host & Service">
              <Select value={kind} onChange={(e) => setKind(e.target.value as Kind | '')}>
                <option value="">beide</option>
                <option value="host">{t('host')}</option>
                <option value="service">{t('service')}</option>
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
    </Dialog>
  )
}

// ——— Check commands ——————————————————————————————————————————————
const commandsApi = resourceApi<CheckCommand>('check-commands')
const commandsInval: string[][] = [[...commandsApi.queryKey]]

function CheckCommandsTab() {
  const { data, isLoading } = useQuery({ queryKey: commandsApi.queryKey, queryFn: commandsApi.list })
  const [editing, setEditing] = useState<{ name: string } | 'new' | null>(null)
  const remove = useSave<string>((name) => commandsApi.remove(name), { invalidate: commandsInval })

  return (
    <Card title="Check-Kommandos" actions={<Button variant="primary" size="sm" onClick={() => setEditing('new')}>+ {t('create')}</Button>}>
      {isLoading ? <Spinner /> : (data?.length ?? 0) === 0 ? <Empty text={t('empty')} /> : (
        <Table head={[t('name'), t('type'), 'Kommandozeile', 'Env', t('timeout'), '']}>
          {data!.map((cmd) => (
            <tr key={cmd.name}>
              <td className="px-3 py-2 text-slate-200 font-medium">{cmd.name}</td>
              <td className="px-3 py-2"><Badge className="bg-slate-800 text-slate-400 border-slate-700">{cmd.type}</Badge></td>
              <td className="px-3 py-2 text-xs text-slate-500 font-mono truncate max-w-md">{(cmd.line ?? []).join(' ') || '—'}</td>
              <td className="px-3 py-2 text-xs text-slate-500">{cmd.env ? 'ja' : '—'}</td>
              <td className="px-3 py-2 text-xs text-slate-500 font-mono">{cmd.timeout || '—'}</td>
              <td className="px-3 py-2 text-right whitespace-nowrap">
                <Button size="sm" variant="ghost" onClick={() => setEditing({ name: cmd.name })}>{t('edit')}</Button>
                <DeleteButton onDelete={() => remove.mutate(cmd.name)} />
              </td>
            </tr>
          ))}
        </Table>
      )}
      {editing && (
        <CheckCommandDialog name={editing === 'new' ? null : editing.name} onClose={() => setEditing(null)} />
      )}
    </Card>
  )
}

function CheckCommandDialog({ name, onClose }: { name: string | null; onClose: () => void }) {
  const isEdit = name !== null
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...commandsApi.queryKey, name],
    queryFn: () => commandsApi.get(name!),
    enabled: isEdit,
  })

  const [draftName, setDraftName] = useState('')
  const [type, setType] = useState<CheckCommand['type']>('exec')
  const [line, setLine] = useState<string[]>([])
  const [env, setEnv] = useState(false)
  const [timeout, setTimeoutV] = useState('')
  const [ready, setReady] = useState(!isEdit)

  if (isEdit && loaded && !ready) {
    setDraftName(loaded.data.name)
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
    <Dialog open onClose={onClose} title={isEdit ? `${t('edit')}: ${name}` : `Check-Kommando — ${t('create')}`} size="lg">
      {isEdit && isLoading ? <Spinner /> : (
        <form onSubmit={(e) => { e.preventDefault(); save.mutate() }} className="space-y-4">
          <div className="grid grid-cols-2 gap-2">
            <Field label={t('name')} required>
              <Input value={isEdit ? name! : draftName} disabled={isEdit}
                onChange={(e) => setDraftName(e.target.value)} placeholder="check_postgres" autoFocus={!isEdit} />
            </Field>
            <Field label={t('type')}>
              <Select value={type} onChange={(e) => setType(e.target.value as CheckCommand['type'])}>
                <option value="exec">exec</option>
                <option value="builtin">builtin</option>
                <option value="agent">agent</option>
                <option value="passive">passive</option>
              </Select>
            </Field>
          </div>
          <Field label="Kommandozeile" hint="Ein Token pro Eintrag; argv[0] = Plugin/Builtin-Name, dann $ARG1$ …">
            <ListEditor value={line} onChange={setLine} placeholder="check_postgres" />
          </Field>
          <div className="grid grid-cols-2 gap-2 items-end">
            <Field label={t('timeout')}>
              <DurationInput value={timeout} onChange={setTimeoutV} placeholder="30s" />
            </Field>
            <div className="pb-1.5">
              <Toggle checked={env} onChange={setEnv} label="NAGIOS_*/NORTHPLANE_* Umgebungsmakros exportieren" />
            </div>
          </div>
          <FormError error={conflictMessage(save.error)} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!(isEdit ? name : draftName)}
            label={isEdit ? t('save') : t('create')} />
        </form>
      )}
    </Dialog>
  )
}

// ——— Time periods ————————————————————————————————————————————————
const periodsApi = resourceApi<TimePeriod>('time-periods')
const periodsInval: string[][] = [[...periodsApi.queryKey], ['resources', 'time-periods', 'names']]
const WEEKDAYS = ['monday', 'tuesday', 'wednesday', 'thursday', 'friday', 'saturday', 'sunday'] as const
const WEEKDAY_DE: Record<typeof WEEKDAYS[number], string> = {
  monday: 'Montag', tuesday: 'Dienstag', wednesday: 'Mittwoch', thursday: 'Donnerstag',
  friday: 'Freitag', saturday: 'Samstag', sunday: 'Sonntag',
}

function TimePeriodsTab() {
  const { data, isLoading } = useQuery({ queryKey: periodsApi.queryKey, queryFn: periodsApi.list })
  const [editing, setEditing] = useState<{ name: string } | 'new' | null>(null)
  const remove = useSave<string>((name) => periodsApi.remove(name), { invalidate: periodsInval })

  return (
    <Card title="Zeiträume" actions={<Button variant="primary" size="sm" onClick={() => setEditing('new')}>+ {t('create')}</Button>}>
      {isLoading ? <Spinner /> : (data?.length ?? 0) === 0 ? <Empty text={t('empty')} /> : (
        <Table head={[t('name'), 'Alias', 'Tage', 'Ausnahmen', '']}>
          {data!.map((tp) => (
            <tr key={tp.name}>
              <td className="px-3 py-2 text-slate-200 font-medium">{tp.name}</td>
              <td className="px-3 py-2 text-xs text-slate-400">{tp.alias || '—'}</td>
              <td className="px-3 py-2 text-xs text-slate-500">{Object.keys(tp.days ?? {}).length || '—'}</td>
              <td className="px-3 py-2 text-xs text-slate-500">{Object.keys(tp.exceptions ?? {}).length || '—'}</td>
              <td className="px-3 py-2 text-right whitespace-nowrap">
                <Button size="sm" variant="ghost" onClick={() => setEditing({ name: tp.name })}>{t('edit')}</Button>
                <DeleteButton onDelete={() => remove.mutate(tp.name)} />
              </td>
            </tr>
          ))}
        </Table>
      )}
      {editing && (
        <TimePeriodDialog name={editing === 'new' ? null : editing.name} onClose={() => setEditing(null)} />
      )}
    </Card>
  )
}

function TimePeriodDialog({ name, onClose }: { name: string | null; onClose: () => void }) {
  const isEdit = name !== null
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...periodsApi.queryKey, name],
    queryFn: () => periodsApi.get(name!),
    enabled: isEdit,
  })

  const [draftName, setDraftName] = useState('')
  const [alias, setAlias] = useState('')
  const [days, setDays] = useState<Record<string, string[]>>({})
  const [exceptions, setExceptions] = useState<Record<string, string>>({}) // date → "r,r" comma string
  const [exclude, setExclude] = useState<string[]>([])
  const [ready, setReady] = useState(!isEdit)

  if (isEdit && loaded && !ready) {
    setDraftName(loaded.data.name)
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
    <Dialog open onClose={onClose} title={isEdit ? `${t('edit')}: ${name}` : `Zeitraum — ${t('create')}`} size="lg">
      {isEdit && isLoading ? <Spinner /> : (
        <form onSubmit={(e) => { e.preventDefault(); save.mutate() }} className="space-y-4">
          <div className="grid grid-cols-2 gap-2">
            <Field label={t('name')} required>
              <Input value={isEdit ? name! : draftName} disabled={isEdit}
                onChange={(e) => setDraftName(e.target.value)} placeholder="business-hours" autoFocus={!isEdit} />
            </Field>
            <Field label="Alias">
              <Input value={alias} onChange={(e) => setAlias(e.target.value)} placeholder="Geschäftszeiten" />
            </Field>
          </div>

          <div className="border border-slate-800 rounded-lg p-3 space-y-2">
            <div className="text-xs font-semibold text-slate-400 uppercase tracking-wider">Wochentage</div>
            {WEEKDAYS.map((wd) => (
              <Field key={wd} label={WEEKDAY_DE[wd]} hint='Bereiche "HH:MM-HH:MM"'>
                <ListEditor
                  value={days[wd] ?? []}
                  onChange={(v) => setDays((d) => ({ ...d, [wd]: v }))}
                  placeholder="09:00-17:00"
                />
              </Field>
            ))}
          </div>

          <Field label="Ausnahmen" hint='Datum → Bereiche, z.B. "2026-12-24" → "00:00-00:00" (geschlossen)'>
            <KVEditor value={exceptions} onChange={setExceptions} keyPlaceholder="2026-12-24" valuePlaceholder="00:00-00:00" />
          </Field>

          <Field label="Ausschluss (exclude)" hint="Namen anderer Zeiträume, die abgezogen werden">
            <ListEditor value={exclude} onChange={setExclude} placeholder="holidays" />
          </Field>

          <FormError error={conflictMessage(save.error)} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!(isEdit ? name : draftName)}
            label={isEdit ? t('save') : t('create')} />
        </form>
      )}
    </Dialog>
  )
}
