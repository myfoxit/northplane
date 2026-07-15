// Reusable ObjectSpec editor (CMP "Monitoring Admin" parity): the full
// declarative check configuration including the interval/retry/attempts
// block. Shared by the host/service ObjectForm and the template editor so
// both surfaces stay in lock-step (SPEC §6.2 — same fields everywhere).
import { useQuery } from '@tanstack/react-query'
import { get } from '../../api'
import type { ObjectSpec } from '../../types'
import { useBuiltins, useResourceNames } from './specUtil'
import { Field, DurationInput, KVEditor, ListEditor } from '@/components/kit'
import { DualListPicker } from '@/components/DualListPicker'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import { t } from '../../i18n'

// Radix SelectItem value cannot be "" — sentinel stands in for the
// "inherit/all" empty option and maps back to '' on change.
const NONE = '__none__'

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
          <SelectItem value="inherit">Vererbt</SelectItem>
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
        <SelectItem value="command">Kommando (definiert)</SelectItem>
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
      <Field label={t('checkCommand')} hint="Passiv: Ergebnisse werden extern eingespeist (Agent / API) — kein aktiver Check.">
        {kindSelect}
      </Field>
    )
  }
  return (
    <div className="grid grid-cols-[10rem_1fr] gap-2">
      <Field label={t('checkCommand')}>{kindSelect}</Field>
      <Field label={kind === 'builtin' ? 'Builtin-Check' : kind === 'command' ? 'Check-Kommando' : 'Kommando / Plugin'}>
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

// ——— the spec editor ————————————————————————————————————————————
// `kind` toggles the host-only (parents) / service-only (staleness) blocks.
// Pass kind='' from the template editor to show everything.
export function SpecFields({ spec, onChange, kind, hideCommand }: {
  spec: ObjectSpec
  onChange: (s: ObjectSpec) => void
  kind: 'host' | 'service' | ''
  hideCommand?: boolean
}) {
  const patch = (p: Partial<ObjectSpec>) => onChange({ ...spec, ...p })
  const templates = useResourceNames('templates', ['resources', 'templates', 'names'])
  const periods = useResourceNames('time-periods', ['resources', 'time-periods', 'names'])
  const contactGroups = useResourceNames('contact-groups', ['resources', 'contact-groups', 'names'])
  const contacts = useResourceNames('contacts', ['resources', 'contacts', 'names'])
  // Distinct query key from ObjectForm's useHostPicker (['objects','host-names']):
  // that one caches {id,name} objects for the host <Select>, this one caches
  // plain name strings for the parents picker. Sharing the key let whichever
  // ran first decide the shape — and object-shaped data crashed the string-only
  // DualListPicker (localeCompare/toLowerCase on an object).
  const hosts = useQuery({
    queryKey: ['objects', 'host-names', 'namesOnly'],
    queryFn: () => get<{ items: { name: string }[] | null }>('/hosts?limit=2000&withState=false')
      .then((r) => (r.items ?? []).map((x) => x.name)),
    staleTime: 30_000,
    enabled: kind !== 'service', // parents apply to hosts (and the catch-all template view)
  })

  const passive = splitRef(spec.checkCommand).kind === 'passive'

  return (
    <div className="space-y-4">
      <Field label={t('address')}>
        <Input value={spec.address ?? ''} placeholder="10.0.0.1 / host.example.com"
          onChange={(e) => patch({ address: e.target.value })} />
      </Field>

      {!hideCommand && <CheckCommandField spec={spec} patch={patch} />}

      <Field label={t('args')} hint="Ein Argument pro Eintrag ($ARG1$, $ARG2$ …)">
        <ListEditor value={spec.args ?? []} onChange={(v) => patch({ args: v })} placeholder="--port=5432" />
      </Field>

      <Field label={t('templates')} hint="Vererbung in deklarierter Reihenfolge (später gewinnt)">
        <DualListPicker value={spec.templates ?? []} onChange={(v) => patch({ templates: v })}
          options={templates.data ?? []} />
      </Field>

      {kind !== 'service' && (
        <Field label={t('parents')} hint="Hosts für die Erreichbarkeitslogik">
          <DualListPicker value={spec.parents ?? []} onChange={(v) => patch({ parents: v })}
            options={hosts.data ?? []} />
        </Field>
      )}

      {/* INTERVALS — the heart of CMP check-interval management. */}
      <div className="border border-border rounded-lg p-3 space-y-3">
        <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">{t('interval')} &amp; Scheduling</div>
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-2">
          <Field label={t('interval')} hint="z.B. 60s, 5m">
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
            <Input value={spec.checkPeriod ?? ''} list="sugg-periods" placeholder="24x7"
              onChange={(e) => patch({ checkPeriod: e.target.value })} />
            <datalist id="sugg-periods">
              {(periods.data ?? []).map((p) => <option key={p} value={p} />)}
            </datalist>
          </Field>
        </div>
      </div>

      {/* NOTIFICATION ROUTING — Nagios contact_groups parity: the
          referenced groups/contacts are notified directly on hard state
          changes; notifyOn filters the transitions, the period gates the
          time window. */}
      <div className="border border-border rounded-lg p-3 space-y-3">
        <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">{t('notifications')}</div>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
          <Field label="Kontaktgruppen" hint="Direkt benachrichtigt bei harten Statuswechseln">
            <DualListPicker value={spec.contactGroups ?? []} onChange={(v) => patch({ contactGroups: v })}
              options={contactGroups.data ?? []} />
          </Field>
          <Field label="Kontakte" hint="Zusätzliche Einzelkontakte">
            <DualListPicker value={spec.contacts ?? []} onChange={(v) => patch({ contacts: v })}
              options={contacts.data ?? []} />
          </Field>
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
          <Field label="Benachrichtigen bei" hint="Leer = alle Problemzustände + Recovery">
            <NotifyOnChips kind={kind} value={spec.notifyOn} onChange={(v) => patch({ notifyOn: v })} />
          </Field>
          <Field label="Benachrichtigungszeitraum" hint="Zeitfenster (Time-Period), leer = immer">
            <Input value={spec.notificationPeriod ?? ''} list="sugg-periods" placeholder="24x7"
              onChange={(e) => patch({ notificationPeriod: e.target.value })} />
          </Field>
        </div>
      </div>

      {/* Behaviour toggles + threshold mode. */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-2">
        <TriField label="Checks" value={spec.enableChecks} onChange={(v) => patch({ enableChecks: v })} />
        <TriField label={t('notifications')} value={spec.enableNotifications} onChange={(v) => patch({ enableNotifications: v })} />
        <TriField label={t('flapDetection')} value={spec.enableFlapDetection} onChange={(v) => patch({ enableFlapDetection: v })} />
        <Field label="Threshold-Modus">
          <Select
            value={spec.thresholdMode || NONE}
            onValueChange={(v) => patch({ thresholdMode: (v === NONE ? undefined : v) as ObjectSpec['thresholdMode'] })}
          >
            <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value={NONE}>Vererbt</SelectItem>
              <SelectItem value="static">static</SelectItem>
              <SelectItem value="adaptive">adaptive (KI)</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      </div>

      {(kind === 'service' || kind === '') && (
        <div className="grid grid-cols-2 gap-2">
          <Field label="Staleness-Frist (passiv)" hint="Frische passiver Ergebnisse">
            <DurationInput value={spec.stalenessAfter ?? ''} onChange={(v) => patch({ stalenessAfter: v })}
              placeholder={passive ? '10m' : '—'} />
          </Field>
          <Field label="Staleness-Text">
            <Input value={spec.stalenessText ?? ''} placeholder="Kein Ergebnis erhalten"
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
          placeholder="## Runbook&#10;1. Prüfe …"
          className="font-mono min-h-20"
        />
      </Field>
    </div>
  )
}

