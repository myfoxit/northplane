// Reusable ObjectSpec editor (CMP "Monitoring Admin" parity): the full
// declarative check configuration including the interval/retry/attempts
// block. Shared by the host/service ObjectForm and the template editor so
// both surfaces stay in lock-step (SPEC §6.2 — same fields everywhere).
//
// The fields are grouped into exported sections (AddressField, CheckSection,
// IntervalSection, NotifySection, AdvancedSection). The flat SpecFields stacks
// them for the wide template editor; ObjectForm distributes them across tabs
// (FORM-1/3/5). Reference pickers switch between the compact chip combobox
// (in a form) and the full two-pane DualListPicker (on wide surfaces) via
// `compact` (FORM-2).
import { useQuery } from '@tanstack/react-query'
import { get } from '../../api'
import type { ObjectSpec } from '../../types'
import { useBuiltins, useResourceNames } from './specUtil'
import { Field, DurationInput, KVEditor, ListEditor } from '@/components/kit'
import { DualListPicker } from '@/components/DualListPicker'
import { MultiSelect } from '@/components/MultiSelect'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import { t } from '../../i18n'

// Radix SelectItem value cannot be "" — sentinel stands in for the
// "inherit/all" empty option and maps back to '' on change.
const NONE = '__none__'

type Kind = 'host' | 'service' | ''

// Props shared by every section: the current spec + a shallow patcher, the
// object kind (toggles host-only parents / service-only staleness), and whether
// to render compact form pickers instead of the wide dual-list.
type SectionProps = {
  spec: ObjectSpec
  patch: (p: Partial<ObjectSpec>) => void
  kind: Kind
  compact?: boolean
  // Name of the object being edited, so it can be excluded from its own parent
  // list (a host must not be its own parent — NP-07). Absent in the template
  // editor, where there is no single self.
  selfName?: string
}

// RefPicker: compact chip combobox in a form, full two-pane transfer list on a
// wide surface (FORM-2). Same {value,onChange,options} contract either way.
function RefPicker({ compact, value, onChange, options, placeholder }: {
  compact?: boolean
  value: string[]
  onChange: (v: string[]) => void
  options: string[]
  placeholder?: string
}) {
  return compact
    ? <MultiSelect value={value} onChange={onChange} options={options} placeholder={placeholder} />
    : <DualListPicker value={value} onChange={onChange} options={options} />
}

// ——— tri-state inheritance toggles ————————————————————————————————
// EnableChecks/Notifications/FlapDetection are *bool in the Go model:
// undefined means "inherit from template/defaults", true/false override.
// A plain checkbox cannot express that, so we surface three explicit
// choices (CMP shows the same "from template / yes / no" tri-state).
type Tri = 'inherit' | 'on' | 'off'
function triOf(v: boolean | undefined): Tri {
  return v === undefined ? 'inherit' : v ? 'on' : 'off'
}
function triVal(tri: Tri): boolean | undefined {
  return tri === 'inherit' ? undefined : tri === 'on'
}
function TriField({ label, value, onChange }: {
  label: string; value: boolean | undefined; onChange: (v: boolean | undefined) => void
}) {
  return (
    <Field label={label}>
      <Select value={triOf(value)} onValueChange={(v) => onChange(triVal(v as Tri))}>
        <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem value="inherit">{t('inherited')}</SelectItem>
          <SelectItem value="on">{t('enabled')}</SelectItem>
          <SelectItem value="off">{t('disabled')}</SelectItem>
        </SelectContent>
      </Select>
    </Field>
  )
}

// ——— check-command picker ———————————————————————————————————————
// Object.checkCommand is a typed reference string ("builtin:icmp",
// "exec:check_pg", "agent:exec:check_disk", "passive"). We split it into a
// kind dropdown + a free-text remainder, recomposing on change.
function splitRef(ref?: string): { kind: string; rest: string } {
  if (!ref || ref === 'passive') return { kind: 'passive', rest: '' }
  if (ref.startsWith('builtin:')) return { kind: 'builtin', rest: ref.slice('builtin:'.length) }
  if (ref.startsWith('agent:exec:')) return { kind: 'agent:exec', rest: ref.slice('agent:exec:'.length) }
  if (ref.startsWith('agent:')) return { kind: 'agent:exec', rest: ref.slice('agent:'.length) }
  if (ref.startsWith('exec:')) return { kind: 'exec', rest: ref.slice('exec:'.length) }
  return { kind: 'command', rest: ref } // bare name → named CheckCommand
}
function joinRef(kind: string, rest: string): string {
  if (kind === 'passive') return 'passive'
  if (kind === 'command') return rest // bare name = named CheckCommand lookup
  return `${kind}:${rest}`
}

function CheckCommandField({ spec, patch }: { spec: ObjectSpec; patch: (p: Partial<ObjectSpec>) => void }) {
  const { data: builtins } = useBuiltins()
  const commands = useResourceNames('check-commands', ['resources', 'check-commands', 'names'])
  const { kind, rest } = splitRef(spec.checkCommand)
  const listId = kind === 'builtin' ? 'sugg-builtins' : kind === 'command' ? 'sugg-commands' : undefined
  const kindSelect = (
    <Select value={kind} onValueChange={(v) => patch({ checkCommand: joinRef(v, rest) })}>
      <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
      <SelectContent>
        <SelectItem value="builtin">builtin</SelectItem>
        <SelectItem value="command">{t('commandDefined')}</SelectItem>
        <SelectItem value="exec">exec</SelectItem>
        <SelectItem value="agent:exec">agent:exec</SelectItem>
        <SelectItem value="passive">passive</SelectItem>
      </SelectContent>
    </Select>
  )
  // passive has no command to type — show only the kind selector (full width)
  // with a one-line explanation instead of a redundant disabled field (FORM-6).
  if (kind === 'passive') {
    return (
      <Field label={t('checkCommand')} hint={t('passiveCheckHint')}>
        {kindSelect}
      </Field>
    )
  }
  return (
    <div className="grid grid-cols-[10rem_1fr] gap-2">
      <Field label={t('checkCommand')}>{kindSelect}</Field>
      <Field label={kind === 'builtin' ? t('builtinCheck') : kind === 'command' ? t('checkCommand') : t('commandPlugin')}>
        <Input
          value={rest}
          list={listId}
          placeholder={kind === 'builtin' ? 'icmp' : kind === 'command' ? 'check_disk_all' : 'check_postgres'}
          onChange={(e) => patch({ checkCommand: joinRef(kind, e.target.value) })}
        />
        {kind === 'builtin' && (
          <datalist id="sugg-builtins">
            {(builtins ?? []).map((b) => <option key={b} value={b} />)}
          </datalist>
        )}
        {kind === 'command' && (
          <datalist id="sugg-commands">
            {(commands.data ?? []).map((c) => <option key={c} value={c} />)}
          </datalist>
        )}
      </Field>
    </div>
  )
}

// ——— notifyOn state chips ———————————————————————————————————————
// Which hard transitions notify (empty = all problems + recovery).
const NOTIFY_TOKENS: Record<'host' | 'service' | '', string[]> = {
  host: ['down', 'unreachable', 'recovery'],
  service: ['warning', 'critical', 'unknown', 'recovery'],
  '': ['warning', 'critical', 'unknown', 'down', 'unreachable', 'recovery'],
}

function NotifyOnChips({ kind, value, onChange }: {
  kind: 'host' | 'service' | ''
  value: string[] | undefined
  onChange: (v: string[] | undefined) => void
}) {
  const active = (tok: string) => (value ?? []).includes(tok)
  const toggle = (tok: string) => {
    const cur = value ?? []
    const next = active(tok) ? cur.filter((x) => x !== tok) : [...cur, tok]
    onChange(next.length ? next : undefined)
  }
  return (
    <div className="flex flex-wrap gap-1.5">
      {NOTIFY_TOKENS[kind].map((tok) => (
        <button
          key={tok} type="button" onClick={() => toggle(tok)}
          className={`px-2 py-0.5 rounded-full text-xs border transition-colors ${
            active(tok)
              ? 'bg-primary/20 border-primary/60 text-primary'
              : 'bg-card border-input text-muted-foreground hover:border-input'
          }`}
        >
          {tok}
        </button>
      ))}
    </div>
  )
}

// ——— sections ————————————————————————————————————————————————————

// AddressField — the object's monitored address (Basis tab).
export function AddressField({ spec, patch }: SectionProps) {
  return (
    <Field label={t('address')}>
      <Input value={spec.address ?? ''} placeholder="10.0.0.1 / host.example.com"
        onChange={(e) => patch({ address: e.target.value })} />
    </Field>
  )
}

// CheckSection — check command + arguments + template inheritance (Prüfung).
export function CheckSection({ spec, patch, compact, hideCommand }: SectionProps & { hideCommand?: boolean }) {
  const templates = useResourceNames('templates', ['resources', 'templates', 'names'])
  return (
    <div className="space-y-4">
      {!hideCommand && <CheckCommandField spec={spec} patch={patch} />}

      <Field label={t('args')} hint={t('argsHint')}>
        <ListEditor value={spec.args ?? []} onChange={(v) => patch({ args: v })} placeholder="--port=5432" />
      </Field>

      <Field label={t('templates')} hint={t('templatesHint')}>
        <RefPicker compact={compact} value={spec.templates ?? []} onChange={(v) => patch({ templates: v })}
          options={templates.data ?? []} placeholder={t('addTemplate')} />
      </Field>
    </div>
  )
}

// IntervalSection — the heart of CMP check-interval management (Prüfung).
export function IntervalSection({ spec, patch }: SectionProps) {
  const periods = useResourceNames('time-periods', ['resources', 'time-periods', 'names'])
  return (
    <div className="border border-border rounded-lg p-3 space-y-3">
      <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">{t('interval')} &amp; Scheduling</div>
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-2">
        <Field label={t('interval')} hint={t('intervalHint')}>
          <DurationInput value={spec.interval ?? ''} onChange={(v) => patch({ interval: v })} placeholder="60s" />
        </Field>
        <Field label={t('retryInterval')}>
          <DurationInput value={spec.retryInterval ?? ''} onChange={(v) => patch({ retryInterval: v })} placeholder="15s" />
        </Field>
        <Field label={t('maxAttempts')}>
          <Input type="number" min={1} value={spec.maxCheckAttempts ?? ''}
            onChange={(e) => patch({ maxCheckAttempts: e.target.value === '' ? undefined : Number(e.target.value) })} />
        </Field>
        <Field label={t('timeout')}>
          <DurationInput value={spec.timeout ?? ''} onChange={(v) => patch({ timeout: v })} placeholder="30s" />
        </Field>
      </div>
      <div className="grid grid-cols-2 gap-2">
        <Field label={t('checkPeriod')}>
          <Input value={spec.checkPeriod ?? ''} list="sugg-check-periods" placeholder="24x7"
            onChange={(e) => patch({ checkPeriod: e.target.value })} />
          <datalist id="sugg-check-periods">
            {(periods.data ?? []).map((p) => <option key={p} value={p} />)}
          </datalist>
        </Field>
      </div>
    </div>
  )
}

// NotifySection — Nagios contact_groups parity: referenced groups/contacts are
// notified on hard state changes; notifyOn filters transitions, the period
// gates the window (Benachrichtigungen).
export function NotifySection({ spec, patch, kind, compact }: SectionProps) {
  const contactGroups = useResourceNames('contact-groups', ['resources', 'contact-groups', 'names'])
  const contacts = useResourceNames('contacts', ['resources', 'contacts', 'names'])
  const periods = useResourceNames('time-periods', ['resources', 'time-periods', 'names'])
  return (
    <div className="border border-border rounded-lg p-3 space-y-3">
      <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">{t('notifications')}</div>
      <div className={compact ? 'grid grid-cols-1 gap-3' : 'grid grid-cols-1 sm:grid-cols-2 gap-2'}>
        <Field label={t('contactGroups')} hint={t('contactGroupsHint')}>
          <RefPicker compact={compact} value={spec.contactGroups ?? []} onChange={(v) => patch({ contactGroups: v })}
            options={contactGroups.data ?? []} placeholder={t('addGroup')} />
        </Field>
        <Field label={t('contacts')} hint={t('contactsHint')}>
          <RefPicker compact={compact} value={spec.contacts ?? []} onChange={(v) => patch({ contacts: v })}
            options={contacts.data ?? []} placeholder={t('addContact')} />
        </Field>
      </div>
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
        <Field label={t('notifyOn')} hint={t('notifyOnHint')}>
          <NotifyOnChips kind={kind} value={spec.notifyOn} onChange={(v) => patch({ notifyOn: v })} />
        </Field>
        <Field label={t('notificationPeriod')} hint={t('notificationPeriodHint')}>
          <Input value={spec.notificationPeriod ?? ''} list="sugg-notify-periods" placeholder="24x7"
            onChange={(e) => patch({ notificationPeriod: e.target.value })} />
          <datalist id="sugg-notify-periods">
            {(periods.data ?? []).map((p) => <option key={p} value={p} />)}
          </datalist>
        </Field>
      </div>
    </div>
  )
}

// AdvancedSection — parents, behaviour toggles, staleness, zone, vars, runbook
// (Erweitert). Rarely touched: template inheritance + defaults cover most of it.
export function AdvancedSection({ spec, patch, kind, compact, selfName }: SectionProps) {
  const hosts = useQuery({
    queryKey: ['objects', 'host-names', 'namesOnly'],
    queryFn: () => get<{ items: { name: string }[] | null }>('/hosts?limit=2000&withState=false')
      .then((r) => (r.items ?? []).map((x) => x.name)),
    staleTime: 30_000,
    enabled: kind !== 'service', // parents apply to hosts (and the catch-all template view)
  })
  const passive = splitRef(spec.checkCommand).kind === 'passive'
  // A host cannot be its own parent (would create a self-referential
  // reachability cycle), so drop it from the options (NP-07).
  const parentOptions = (hosts.data ?? []).filter((h) => h !== selfName)
  return (
    <div className="space-y-4">
      {kind !== 'service' && (
        <Field label={t('parents')} hint={t('parentsHint')}>
          <RefPicker compact={compact} value={spec.parents ?? []} onChange={(v) => patch({ parents: v })}
            options={parentOptions} placeholder={t('addParentHost')} />
        </Field>
      )}

      {/* Behaviour toggles + threshold mode. */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-2">
        <TriField label="Checks" value={spec.enableChecks} onChange={(v) => patch({ enableChecks: v })} />
        <TriField label={t('notifications')} value={spec.enableNotifications} onChange={(v) => patch({ enableNotifications: v })} />
        <TriField label={t('flapDetection')} value={spec.enableFlapDetection} onChange={(v) => patch({ enableFlapDetection: v })} />
        <Field label={t('thresholdMode')}>
          <Select
            value={spec.thresholdMode || NONE}
            onValueChange={(v) => patch({ thresholdMode: (v === NONE ? undefined : v) as ObjectSpec['thresholdMode'] })}
          >
            <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value={NONE}>{t('inherited')}</SelectItem>
              <SelectItem value="static">static</SelectItem>
              <SelectItem value="adaptive">{t('adaptiveAi')}</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      </div>

      {(kind === 'service' || kind === '') && (
        <div className="grid grid-cols-2 gap-2">
          <Field label={t('stalenessDeadline')} hint={t('stalenessDeadlineHint')}>
            <DurationInput value={spec.stalenessAfter ?? ''} onChange={(v) => patch({ stalenessAfter: v })}
              placeholder={passive ? '10m' : '—'} />
          </Field>
          <Field label={t('stalenessText')}>
            <Input value={spec.stalenessText ?? ''} placeholder={t('noResultReceived')}
              onChange={(e) => patch({ stalenessText: e.target.value })} />
          </Field>
        </div>
      )}

      <div className="grid grid-cols-2 gap-2">
        <Field label={t('zone')}>
          <Input value={spec.zone ?? ''} placeholder="satellite-rz1"
            onChange={(e) => patch({ zone: e.target.value })} />
        </Field>
      </div>

      <Field label={t('vars')} hint="Macros $_HOSTKEY$ / $_SERVICEKEY$">
        <KVEditor value={spec.vars ?? {}} onChange={(v) => patch({ vars: v })} />
      </Field>

      <Field label={t('runbook')} hint="Markdown">
        <Textarea
          value={spec.runbook ?? ''}
          onChange={(e) => patch({ runbook: e.target.value })}
          placeholder={t('runbookPlaceholder')}
          className="font-mono min-h-20"
        />
      </Field>
    </div>
  )
}

// ——— the flat spec editor (wide template editor) —————————————————————
// `kind` toggles the host-only (parents) / service-only (staleness) blocks.
// Pass kind='' from the template editor to show everything. The full DualList
// pickers are kept here (compact=false) because there is room on this surface.
export function SpecFields({ spec, onChange, kind, hideCommand }: {
  spec: ObjectSpec
  onChange: (s: ObjectSpec) => void
  kind: Kind
  hideCommand?: boolean
}) {
  const patch = (p: Partial<ObjectSpec>) => onChange({ ...spec, ...p })
  return (
    <div className="space-y-4">
      <AddressField spec={spec} patch={patch} kind={kind} />
      <CheckSection spec={spec} patch={patch} kind={kind} hideCommand={hideCommand} />
      <IntervalSection spec={spec} patch={patch} kind={kind} />
      <NotifySection spec={spec} patch={patch} kind={kind} />
      <AdvancedSection spec={spec} patch={patch} kind={kind} />
    </div>
  )
}
