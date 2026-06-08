// Alert-rules tab: CRUD + an in-dialog rule tester (CEL match XOR
// heartbeat source). SPEC §9.2 / F-05.04 — the tester is a first-class
// feature: demo event JSON -> POST :test -> matched count + would-open alerts.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { FlaskConical, Loader2 } from 'lucide-react'
import { post, resourceApi } from '../../api'
import type { AlertRule, AlertGroup, EscalationPolicy, RuleTestResult, Severity } from '../../types'
import { sevColor } from '../../types'
import { Badge, Button, Dialog, Empty, ErrorState, Table } from '../ui'
import { Field, FormError, KVEditor, DurationInput, TextArea, SubmitRow, DeleteButton, useSave } from '../forms'
import { Input } from '../ui'
import { t } from '../../i18n'
import { SeverityField, ToggleRow } from './common'
import { excerpt } from './datetime'

const rulesApi = resourceApi<AlertRule>('alert-rules')
const groupsApi = resourceApi<AlertGroup>('alert-groups')
const policiesApi = resourceApi<EscalationPolicy>('escalation-policies')

const SAMPLE_EVENT = JSON.stringify({
  type: 'state_change',
  severity: 'critical',
  objectId: 'host/web01',
  payload: {
    object: 'web01', kind: 'host', toState: 1, to: 'DOWN',
    stateType: 'hard', attempt: 3, output: 'CRITICAL - 100% packet loss',
  },
}, null, 2)

function emptyRule(): AlertRule {
  return { name: '', severity: 'critical', match: '', resolveOnOk: true }
}

export function RulesTab() {
  const { data: rules, isError, error, refetch } = useQuery({ queryKey: rulesApi.queryKey, queryFn: rulesApi.list })
  const { data: groups } = useQuery({ queryKey: groupsApi.queryKey, queryFn: groupsApi.list })
  const { data: policies } = useQuery({ queryKey: policiesApi.queryKey, queryFn: policiesApi.list })
  const [editing, setEditing] = useState<{ rule: AlertRule; etag: number } | null>(null)

  const open = async (name?: string) => {
    if (!name) { setEditing({ rule: emptyRule(), etag: 0 }); return }
    const { data, etag } = await rulesApi.get(name)
    setEditing({ rule: data, etag })
  }

  if (isError && !rules) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Button variant="primary" onClick={() => open()}>{t('newRule')}</Button>
      </div>
      {(rules?.length ?? 0) === 0 ? <Empty text={t('empty')} /> : (
        <Table head={[t('name'), t('severity'), t('matchExpression'), t('escalations'), '', t('actions')]}>
          {rules!.map((r) => (
            <tr key={r.name} className="hover:bg-muted/30">
              <td className="px-3 py-2 font-medium text-foreground">{r.name}</td>
              <td className="px-3 py-2"><Badge className={sevColor(r.severity)}>{r.severity}</Badge></td>
              <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
                {r.heartbeat ? <span className="text-muted-foreground">♥ {r.heartbeat.source} / {r.heartbeat.expectEvery}</span> : excerpt(r.match)}
              </td>
              <td className="px-3 py-2 text-xs text-muted-foreground">{r.escalationPolicy ?? '—'}</td>
              <td className="px-3 py-2">{r.disabled && <Badge className="bg-muted text-muted-foreground border-input">{t('disabled')}</Badge>}</td>
              <td className="px-3 py-2">
                <div className="flex gap-1 justify-end">
                  <RowTestButton rule={r} />
                  <Button size="sm" onClick={() => open(r.name)}>{t('edit')}</Button>
                </div>
              </td>
            </tr>
          ))}
        </Table>
      )}
      {editing && (
        <RuleDialog
          state={editing}
          groups={groups ?? []}
          policies={policies ?? []}
          onClose={() => setEditing(null)}
        />
      )}
    </div>
  )
}

// Per-row quick test: runs the stored rule against the last 24h of history.
function RowTestButton({ rule }: { rule: AlertRule }) {
  const [res, setRes] = useState<RuleTestResult | null>(null)
  const [err, setErr] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const run = async () => {
    setBusy(true); setErr(null)
    try { setRes(await post<RuleTestResult>(`/alert-rules/${encodeURIComponent(rule.name)}:test`, {})) }
    catch (e) { setErr(e) }
    finally { setBusy(false) }
  }
  return (
    <>
      <Button size="sm" variant="ghost" title={t('testRule')} aria-label={t('testRule')} onClick={run} disabled={busy}><FlaskConical size={13} /></Button>
      {(res || err) && (
        <Dialog open onClose={() => { setRes(null); setErr(null) }} title={`${t('testRule')}: ${rule.name}`} size="md">
          <FormError error={err} />
          {res && <TestResultView res={res} />}
        </Dialog>
      )}
    </>
  )
}

function TestResultView({ res }: { res: RuleTestResult }) {
  return (
    <div className="space-y-2 text-sm">
      <p className="text-foreground/90">
        <span className="font-semibold text-foreground">{res.matched}</span> Events würden matchen,{' '}
        <span className="font-semibold text-foreground">{res.wouldOpen?.length ?? 0}</span> Alarme entstehen.
      </p>
      {(res.wouldOpen?.length ?? 0) > 0 && (
        <div className="space-y-1">
          {res.wouldOpen!.map((a, i) => (
            <div key={i} className="flex items-center gap-2 bg-card/60 border border-border rounded-md px-2 py-1">
              <Badge className={sevColor(a.severity)}>{a.severity}</Badge>
              <span className="text-xs text-foreground/90 font-mono truncate">{a.title}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function RuleDialog({ state, groups, policies, onClose }: {
  state: { rule: AlertRule; etag: number }
  groups: AlertGroup[]; policies: EscalationPolicy[]; onClose: () => void
}) {
  const isNew = state.etag === 0 && !state.rule.name
  const [r, setR] = useState<AlertRule>(state.rule)
  const [source, setSource] = useState<'cel' | 'heartbeat'>(state.rule.heartbeat ? 'heartbeat' : 'cel')
  const set = (patch: Partial<AlertRule>) => setR((prev) => ({ ...prev, ...patch }))

  const save = useSave(
    (doc: AlertRule) => isNew ? rulesApi.create(doc) : rulesApi.update(doc.name, doc, state.etag),
    { invalidate: [rulesApi.queryKey], onDone: onClose },
  )
  const remove = useSave((name: string) => rulesApi.remove(name),
    { invalidate: [rulesApi.queryKey], onDone: onClose })

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    const doc: AlertRule = { ...r }
    if (source === 'heartbeat') {
      doc.match = undefined
      doc.heartbeat = { source: r.heartbeat?.source ?? '', expectEvery: r.heartbeat?.expectEvery ?? '60s' }
    } else {
      doc.heartbeat = undefined
    }
    save.mutate(doc)
  }

  return (
    <Dialog open onClose={onClose} title={isNew ? t('newRule') : `${t('edit')}: ${state.rule.name}`} size="lg">
      <form onSubmit={onSubmit} className="space-y-3">
        <Field label={t('name')} required>
          <Input value={r.name} disabled={!isNew}
            onChange={(e) => set({ name: e.target.value })} placeholder="host-down-critical" />
        </Field>

        {/* Quelle: CEL match XOR heartbeat */}
        <div>
          <span className="text-xs text-muted-foreground font-medium">Quelle</span>
          <div className="flex gap-4 mt-1 mb-2">
            <label className="flex items-center gap-1.5 text-sm text-foreground/90 cursor-pointer">
              <input type="radio" checked={source === 'cel'} onChange={() => setSource('cel')} /> CEL-Match
            </label>
            <label className="flex items-center gap-1.5 text-sm text-foreground/90 cursor-pointer">
              <input type="radio" checked={source === 'heartbeat'} onChange={() => setSource('heartbeat')} /> Heartbeat
            </label>
          </div>
          {source === 'cel' ? (
            <TextArea
              value={r.match ?? ''} onChange={(e) => set({ match: e.target.value })}
              placeholder={'event.type == "state_change" && event.severity == "critical"'}
              rows={3}
            />
          ) : (
            <div className="grid grid-cols-2 gap-3">
              <Field label="Source">
                <Input value={r.heartbeat?.source ?? ''}
                  onChange={(e) => set({ heartbeat: { source: e.target.value, expectEvery: r.heartbeat?.expectEvery ?? '60s' } })}
                  placeholder="backup-job" />
              </Field>
              <Field label="Erwartet alle">
                <DurationInput value={r.heartbeat?.expectEvery ?? ''}
                  onChange={(v) => set({ heartbeat: { source: r.heartbeat?.source ?? '', expectEvery: v } })}
                  placeholder="1h" />
              </Field>
            </div>
          )}
        </div>

        <div className="grid grid-cols-2 gap-3">
          <SeverityField value={r.severity} onChange={(v: Severity) => set({ severity: v })} label={t('severity')} />
          <Field label={t('dedupKey')} hint="optional, Go-Template">
            <Input value={r.dedupKey ?? ''} onChange={(e) => set({ dedupKey: e.target.value || undefined })}
              placeholder="{{.ObjectID}}" />
          </Field>
        </div>

        <Field label="Titel (Go-Template)" hint="optional">
          <Input value={r.title ?? ''} onChange={(e) => set({ title: e.target.value || undefined })}
            placeholder="{{.ObjectName}} ist {{.ToLabel}}" />
        </Field>

        <div className="grid grid-cols-2 gap-3">
          <Field label={t('pendingFor')} hint="optional">
            <DurationInput value={r.pendingFor ?? ''} onChange={(v) => set({ pendingFor: v || undefined })} placeholder="5m" />
          </Field>
          <Field label={t('autoClose')} hint="optional">
            <DurationInput value={r.autoCloseAfter ?? ''} onChange={(v) => set({ autoCloseAfter: v || undefined })} placeholder="24h" />
          </Field>
        </div>

        <div className="grid grid-cols-2 gap-3">
          <Field label={t('escalations')}>
            <select value={r.escalationPolicy ?? ''} onChange={(e) => set({ escalationPolicy: e.target.value || undefined })}
              className="bg-card border border-input rounded-lg px-3 py-1.5 text-sm text-foreground w-full focus:border-ring cursor-pointer">
              <option value="">— {t('none')} —</option>
              {policies.map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}
            </select>
          </Field>
          <Field label="Gruppe">
            <select value={r.groupId ?? ''} onChange={(e) => set({ groupId: e.target.value || undefined })}
              className="bg-card border border-input rounded-lg px-3 py-1.5 text-sm text-foreground w-full focus:border-ring cursor-pointer">
              <option value="">— {t('none')} —</option>
              {groups.map((g) => <option key={g.name} value={g.name}>{g.name}</option>)}
            </select>
          </Field>
        </div>

        <Field label="Labels setzen">
          <KVEditor value={r.setLabels ?? {}} onChange={(v) => set({ setLabels: v })} keyPlaceholder="team" valuePlaceholder="netops" />
        </Field>

        <div className="flex gap-6">
          <ToggleRow label="Bei OK schließen" checked={r.resolveOnOk ?? false} onChange={(v) => set({ resolveOnOk: v })} />
          <ToggleRow label="Incident anlegen" checked={r.incident ?? false} onChange={(v) => set({ incident: v })} />
          <ToggleRow label={t('disabled')} checked={r.disabled ?? false} onChange={(v) => set({ disabled: v })} />
        </div>

        <FormError error={save.error} />

        <TestPanel rule={{ ...r, heartbeat: source === 'heartbeat' ? r.heartbeat : undefined, match: source === 'cel' ? r.match : undefined }} />

        <div className="flex items-center justify-between pt-2">
          {!isNew ? <DeleteButton onDelete={() => remove.mutate(state.rule.name)} /> : <span />}
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!r.name} />
        </div>
      </form>
    </Dialog>
  )
}

// In-dialog tester against a hand-written demo event (does not need the rule
// to be saved — posts {rule, demoEvents} to /alert-rules:test).
function TestPanel({ rule }: { rule: AlertRule }) {
  const [json, setJson] = useState(SAMPLE_EVENT)
  const [res, setRes] = useState<RuleTestResult | null>(null)
  const [err, setErr] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)

  const run = async () => {
    setBusy(true); setErr(null); setRes(null)
    let event: unknown
    try { event = JSON.parse(json) }
    catch (e) { setErr(new Error('Ungültiges JSON: ' + String(e))); setBusy(false); return }
    try {
      setRes(await post<RuleTestResult>('/alert-rules:test', { rule, demoEvents: [event] }))
    } catch (e) { setErr(e) } finally { setBusy(false) }
  }

  return (
    <div className="border border-border rounded-lg p-3 bg-card/40 space-y-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-semibold text-foreground/90 inline-flex items-center gap-1"><FlaskConical size={13} /> {t('testRule')} — Demo-Event</span>
        <Button size="sm" type="button" onClick={run} disabled={busy}>{busy ? <Loader2 className="animate-spin" size={14} /> : t('run')}</Button>
      </div>
      <TextArea value={json} onChange={(e) => setJson(e.target.value)} rows={6} />
      <FormError error={err} />
      {res && <TestResultView res={res} />}
    </div>
  )
}
