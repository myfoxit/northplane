// Alert-rules tab: CRUD + an in-dialog rule tester (CEL match XOR
// heartbeat source). SPEC §9.2 / F-05.04 — the tester is a first-class
// feature: demo event JSON -> POST :test -> matched count + would-open alerts.
import { useEffect, useState, type RefObject } from 'react'
import { useQuery } from '@tanstack/react-query'
import { FlaskConical, Loader2 } from 'lucide-react'
import { post, resourceApi } from '../../api'
import type { AlertRule, AlertGroup, EscalationPolicy, RuleTestResult, Severity } from '../../types'
import { sevColor } from '../../types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { Empty, ErrorState, Field, FormError, KVEditor, DurationInput, SubmitRow, DeleteButton, useSave } from '@/components/kit'
import { t } from '../../i18n'
import { SeverityField, ToggleRow } from './common'
import { excerpt } from './datetime'

const rulesApi = resourceApi<AlertRule>('alert-rules')
const groupsApi = resourceApi<AlertGroup>('alert-groups')
const policiesApi = resourceApi<EscalationPolicy>('escalation-policies')

// Radix <SelectItem> cannot use "" — sentinel for the "no selection" option.
const NONE = '__none__'

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

export function RulesTab({ createRef }: { createRef?: RefObject<() => void> }) {
  const { data: rules, isError, error, refetch } = useQuery({ queryKey: rulesApi.queryKey, queryFn: rulesApi.list })
  const { data: groups } = useQuery({ queryKey: groupsApi.queryKey, queryFn: groupsApi.list })
  const { data: policies } = useQuery({ queryKey: policiesApi.queryKey, queryFn: policiesApi.list })
  const [editing, setEditing] = useState<{ rule: AlertRule; etag: number } | null>(null)

  const open = async (name?: string) => {
    if (!name) { setEditing({ rule: emptyRule(), etag: 0 }); return }
    const { data, etag } = await rulesApi.get(name)
    setEditing({ rule: data, etag })
  }
  // Expose "create" to the page header (NP-13).
  useEffect(() => { if (createRef) createRef.current = () => void open() })

  if (isError && !rules) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }

  return (
    <div className="space-y-3">
      {(rules?.length ?? 0) === 0 ? <Empty text={t('noRulesFriendly')} /> : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('name')}</TableHead>
              <TableHead>{t('severity')}</TableHead>
              <TableHead>{t('matchExpression')}</TableHead>
              <TableHead>{t('escalations')}</TableHead>
              <TableHead></TableHead>
              <TableHead>{t('actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rules!.map((r) => (
              <TableRow key={r.name}>
                <TableCell className="font-medium text-foreground">{r.name}</TableCell>
                <TableCell><Badge variant="outline" className={sevColor(r.severity)}>{r.severity}</Badge></TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">
                  {r.heartbeat ? <span className="text-muted-foreground">♥ {r.heartbeat.source} / {r.heartbeat.expectEvery}</span> : excerpt(r.match)}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">{r.escalationPolicy ?? '—'}</TableCell>
                <TableCell>{r.disabled && <Badge variant="outline" className="bg-muted text-muted-foreground border-input">{t('disabled')}</Badge>}</TableCell>
                <TableCell>
                  <div className="flex gap-1 justify-end">
                    <RowTestButton rule={r} />
                    <Button size="sm" variant="outline" onClick={() => open(r.name)}>{t('edit')}</Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
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
        <Dialog open onOpenChange={(o) => { if (!o) { setRes(null); setErr(null) } }}>
          <DialogContent className="max-w-md">
            <DialogHeader>
              <DialogTitle>{`${t('testRule')}: ${rule.name}`}</DialogTitle>
            </DialogHeader>
            <FormError error={err} />
            {res && <TestResultView res={res} />}
          </DialogContent>
        </Dialog>
      )}
    </>
  )
}

function TestResultView({ res }: { res: RuleTestResult }) {
  return (
    <div className="space-y-2 text-sm">
      <p className="text-foreground/90">
        <span className="font-semibold text-foreground">{res.matched}</span>{' '}{t('eventsWouldMatch')},{' '}
        <span className="font-semibold text-foreground">{res.wouldOpen?.length ?? 0}</span> {t('alertsWouldOpen')}.
      </p>
      {(res.wouldOpen?.length ?? 0) > 0 && (
        <div className="space-y-1">
          {res.wouldOpen!.map((a, i) => (
            <div key={i} className="flex items-center gap-2 bg-card/60 border border-border rounded-md px-2 py-1">
              <Badge variant="outline" className={sevColor(a.severity)}>{a.severity}</Badge>
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
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{isNew ? t('newRule') : `${t('edit')}: ${state.rule.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} className="space-y-3">
          <Field label={t('name')} required>
            <Input value={r.name} disabled={!isNew}
              onChange={(e) => set({ name: e.target.value })} placeholder="host-down-critical" />
          </Field>

          {/* Quelle: CEL match XOR heartbeat */}
          <div>
            <span className="text-xs text-muted-foreground font-medium">{t('source')}</span>
            <div className="flex gap-4 mt-1 mb-2">
              <label className="flex items-center gap-1.5 text-sm text-foreground/90 cursor-pointer">
                <input type="radio" checked={source === 'cel'} onChange={() => setSource('cel')} /> CEL-Match
              </label>
              <label className="flex items-center gap-1.5 text-sm text-foreground/90 cursor-pointer">
                <input type="radio" checked={source === 'heartbeat'} onChange={() => setSource('heartbeat')} /> Heartbeat
              </label>
            </div>
            {source === 'cel' ? (
              <Textarea
                value={r.match ?? ''} onChange={(e) => set({ match: e.target.value })}
                placeholder={'event.type == "state_change" && event.severity == "critical"'}
                rows={3}
                className="font-mono"
              />
            ) : (
              <div className="grid grid-cols-2 gap-3">
                <Field label="Source">
                  <Input value={r.heartbeat?.source ?? ''}
                    onChange={(e) => set({ heartbeat: { source: e.target.value, expectEvery: r.heartbeat?.expectEvery ?? '60s' } })}
                    placeholder="backup-job" />
                </Field>
                <Field label={t('expectEvery')}>
                  <DurationInput value={r.heartbeat?.expectEvery ?? ''}
                    onChange={(v) => set({ heartbeat: { source: r.heartbeat?.source ?? '', expectEvery: v } })}
                    placeholder="1h" />
                </Field>
              </div>
            )}
          </div>

          <div className="grid grid-cols-2 gap-3">
            <SeverityField value={r.severity} onChange={(v: Severity) => set({ severity: v })} label={t('severity')} />
            <Field label={t('dedupKey')} hint={t('optionalGoTemplate')}>
              <Input value={r.dedupKey ?? ''} onChange={(e) => set({ dedupKey: e.target.value || undefined })}
                placeholder="{{.ObjectID}}" />
            </Field>
          </div>

          <Field label={t('titleGoTemplate')} hint="optional">
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
              <Select value={r.escalationPolicy ?? NONE} onValueChange={(v) => set({ escalationPolicy: v === NONE ? undefined : v })}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>— {t('none')} —</SelectItem>
                  {policies.map((p) => <SelectItem key={p.name} value={p.name}>{p.name}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
            <Field label={t('group')}>
              <Select value={r.groupId ?? NONE} onValueChange={(v) => set({ groupId: v === NONE ? undefined : v })}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>— {t('none')} —</SelectItem>
                  {groups.map((g) => <SelectItem key={g.name} value={g.name}>{g.name}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
          </div>

          <Field label={t('setLabels')}>
            <KVEditor value={r.setLabels ?? {}} onChange={(v) => set({ setLabels: v })} keyPlaceholder="team" valuePlaceholder="netops" />
          </Field>

          <div className="flex gap-6">
            <ToggleRow label={t('resolveOnOk')} checked={r.resolveOnOk ?? false} onChange={(v) => set({ resolveOnOk: v })} />
            <ToggleRow label={t('createIncident')} checked={r.incident ?? false} onChange={(v) => set({ incident: v })} />
            <ToggleRow label={t('disabled')} checked={r.disabled ?? false} onChange={(v) => set({ disabled: v })} />
          </div>

          <FormError error={save.error} />

          <TestPanel rule={{ ...r, heartbeat: source === 'heartbeat' ? r.heartbeat : undefined, match: source === 'cel' ? r.match : undefined }} />

          <div className="flex items-center justify-between pt-2">
            {!isNew ? <DeleteButton onDelete={() => remove.mutate(state.rule.name)} /> : <span />}
            <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!r.name} />
          </div>
        </form>
      </DialogContent>
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
    // Guard the empty rule client-side: without it the backend returns
    // 'validation failed — rule "": match expression required', leaking an
    // empty rule name (NP-18). Give a clean, actionable message instead.
    if (!rule.match?.trim() && !rule.heartbeat?.source?.trim()) {
      setErr(new Error(t('matchRequired'))); return
    }
    setBusy(true); setErr(null); setRes(null)
    let event: unknown
    try { event = JSON.parse(json) }
    catch (e) { setErr(new Error(t('invalidJson') + String(e))); setBusy(false); return }
    try {
      setRes(await post<RuleTestResult>('/alert-rules:test', { rule, demoEvents: [event] }))
    } catch (e) { setErr(e) } finally { setBusy(false) }
  }

  return (
    <div className="border border-border rounded-lg p-3 bg-card/40 space-y-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-semibold text-foreground/90 inline-flex items-center gap-1"><FlaskConical size={13} /> {t('testRule')} — Demo-Event</span>
        <Button size="sm" variant="outline" type="button" onClick={run} disabled={busy}>{busy ? <Loader2 className="animate-spin" size={14} /> : t('run')}</Button>
      </div>
      <Textarea value={json} onChange={(e) => setJson(e.target.value)} rows={6} className="font-mono" />
      <FormError error={err} />
      {res && <TestResultView res={res} />}
    </div>
  )
}
