// Reusable ObjectSpec editor (CMP "Monitoring Admin" parity): the full
// declarative check configuration including the interval/retry/attempts
// block. Shared by the host/service ObjectForm and the template editor so
// both surfaces stay in lock-step (SPEC §6.2 — same fields everywhere).
import { useQuery } from '@tanstack/react-query'
import { get } from '../../api'
import type { ObjectSpec } from '../../types'
import { Field, Select, DurationInput, KVEditor, ListEditor } from '../forms'
import { Input } from '../ui'
import { t } from '../../i18n'

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
      <Select value={triOf(value)} onChange={(e) => onChange(triVal(e.target.value as Tri))}>
        <option value="inherit">Vererbt</option>
        <option value="on">{t('enabled')}</option>
        <option value="off">{t('disabled')}</option>
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
  return { kind: 'exec', rest: ref } // bare name → named command (importer)
}
function joinRef(kind: string, rest: string): string {
  if (kind === 'passive') return 'passive'
  return `${kind}:${rest}`
}

export function useBuiltins() {
  return useQuery({
    queryKey: ['check-commands', 'builtins'],
    queryFn: () => get<string[]>('/check-commands:builtins'),
    staleTime: 5 * 60_000,
  })
}

// Names of a resource collection (templates / time-periods / hosts) for
// datalist suggestions.
export function useResourceNames(base: string, queryKey: string[]) {
  return useQuery({
    queryKey,
    queryFn: () => get<{ items: { name: string }[] | null }>(`/${base}?limit=500`)
      .then((r) => (r.items ?? []).map((x) => x.name)),
    staleTime: 60_000,
  })
}

function CheckCommandField({ spec, patch }: { spec: ObjectSpec; patch: (p: Partial<ObjectSpec>) => void }) {
  const { data: builtins } = useBuiltins()
  const { kind, rest } = splitRef(spec.checkCommand)
  return (
    <div className="grid grid-cols-[10rem_1fr] gap-2">
      <Field label={t('checkCommand')}>
        <Select value={kind} onChange={(e) => patch({ checkCommand: joinRef(e.target.value, rest) })}>
          <option value="builtin">builtin</option>
          <option value="exec">exec</option>
          <option value="agent:exec">agent:exec</option>
          <option value="passive">passive</option>
        </Select>
      </Field>
      <Field label={kind === 'builtin' ? 'Builtin-Check' : kind === 'passive' ? '—' : 'Kommando / Plugin'}>
        {kind === 'passive' ? (
          <Input value="passive" disabled />
        ) : (
          <>
            <Input
              value={rest}
              list={kind === 'builtin' ? 'sugg-builtins' : undefined}
              placeholder={kind === 'builtin' ? 'icmp' : 'check_postgres'}
              onChange={(e) => patch({ checkCommand: joinRef(kind, e.target.value) })}
            />
            {kind === 'builtin' && (
              <datalist id="sugg-builtins">
                {(builtins ?? []).map((b) => <option key={b} value={b} />)}
              </datalist>
            )}
          </>
        )}
      </Field>
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
  const hosts = useQuery({
    queryKey: ['objects', 'host-names'],
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
        <ListEditor value={spec.templates ?? []} onChange={(v) => patch({ templates: v })}
          placeholder="generic-service" suggestions={templates.data ?? []} />
      </Field>

      {kind !== 'service' && (
        <Field label={t('parents')} hint="Hosts für die Erreichbarkeitslogik">
          <ListEditor value={spec.parents ?? []} onChange={(v) => patch({ parents: v })}
            placeholder="core-switch01" suggestions={hosts.data ?? []} />
        </Field>
      )}

      {/* INTERVALS — the heart of CMP check-interval management. */}
      <div className="border border-slate-800 rounded-lg p-3 space-y-3">
        <div className="text-xs font-semibold text-slate-400 uppercase tracking-wider">{t('interval')} &amp; Scheduling</div>
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
          <Field label="Benachrichtigungszeitraum">
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
          <Select value={spec.thresholdMode ?? ''} onChange={(e) => patch({ thresholdMode: (e.target.value || undefined) as ObjectSpec['thresholdMode'] })}>
            <option value="">Vererbt</option>
            <option value="static">static</option>
            <option value="adaptive">adaptive (KI)</option>
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
        <textarea
          value={spec.runbook ?? ''}
          onChange={(e) => patch({ runbook: e.target.value })}
          placeholder="## Runbook&#10;1. Prüfe …"
          className="bg-slate-900 border border-slate-700 rounded-lg px-3 py-1.5 text-sm text-slate-200 w-full font-mono placeholder:text-slate-500 focus:outline-none focus:border-blue-500 min-h-20"
        />
      </Field>
    </div>
  )
}

// ——— serialisation helpers ———————————————————————————————————————
// Strip empty strings / empty arrays / empty maps so the wire payload only
// carries deliberate overrides (omitempty parity → keeps effective-config
// inheritance clean). undefined fields are dropped by JSON.stringify.
export function cleanSpec(spec: ObjectSpec): ObjectSpec {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(spec)) {
    if (v === undefined || v === null || v === '') continue
    if (Array.isArray(v) && v.length === 0) continue
    if (typeof v === 'object' && !Array.isArray(v) && Object.keys(v as object).length === 0) continue
    out[k] = v
  }
  return out as ObjectSpec
}

// A NPObject.spec is typed as Record<string, unknown>; narrow it for the
// editor. Returns a shallow copy so edits never mutate the cache.
export function specOf(raw: Record<string, unknown> | undefined): ObjectSpec {
  return { ...(raw as ObjectSpec | undefined) }
}
