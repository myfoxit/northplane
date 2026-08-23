// Escalation-policies tab: ordered step editor + dry-run simulator
// (SPEC §12.3 "Policies mit Simulator"). Each step Card: after, unlessAcked,
// notify target (schedule/contact/contactGroup), channels, repeat, action.
import { useEffect, useState, type RefObject } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ArrowUp, ArrowDown, X, RefreshCw, Settings } from 'lucide-react'
import { post, resourceApi } from '../../api'
import type {
  EscalationPolicy, EscalationStep, Schedule, Contact, ContactGroup, Channel,
} from '../../types'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { Empty, ErrorState, Field, FormError, DurationInput, SubmitRow, DeleteButton, useSave, DuplicateButton } from '@/components/kit'
import { duplicateDoc } from '@/lib/duplicate'
import { t } from '../../i18n'
import { ChannelPicker, ToggleRow } from './common'

const policiesApi = resourceApi<EscalationPolicy>('escalation-policies')
const schedulesApi = resourceApi<Schedule>('schedules')
const contactsApi = resourceApi<Contact>('contacts')
const groupsApi = resourceApi<ContactGroup>('contact-groups')
const channelsApi = resourceApi<Channel>('channels')

// Radix <SelectItem> cannot use "" — sentinel for the "no selection" option.
const NONE = '__none__'

type NotifyKind = 'schedule' | 'contact' | 'contactGroup'

interface Pickers {
  schedules: Schedule[]; contacts: Contact[]; groups: ContactGroup[]; channels: Channel[]
}

function emptyStep(): EscalationStep {
  return { after: '0s', notify: { schedule: '' }, channels: [] }
}
function emptyPolicy(): EscalationPolicy {
  return { name: '', steps: [emptyStep()] }
}

export function EscalationsTab({ createRef }: { createRef?: RefObject<() => void> }) {
  const { data: policies, isError, error, refetch } = useQuery({ queryKey: policiesApi.queryKey, queryFn: policiesApi.list })
  const { data: schedules } = useQuery({ queryKey: schedulesApi.queryKey, queryFn: schedulesApi.list })
  const { data: contacts } = useQuery({ queryKey: contactsApi.queryKey, queryFn: contactsApi.list })
  const { data: groups } = useQuery({ queryKey: groupsApi.queryKey, queryFn: groupsApi.list })
  const { data: channels } = useQuery({ queryKey: channelsApi.queryKey, queryFn: channelsApi.list })
  const [editing, setEditing] = useState<{ policy: EscalationPolicy; etag: number; copyOf?: string } | null>(null)

  const pickers: Pickers = {
    schedules: schedules ?? [], contacts: contacts ?? [], groups: groups ?? [], channels: channels ?? [],
  }

  const open = async (name?: string) => {
    if (!name) { setEditing({ policy: emptyPolicy(), etag: 0 }); return }
    const { data, etag } = await policiesApi.get(name)
    setEditing({ policy: { ...data, steps: data.steps ?? [] }, etag })
  }
  // Duplicate: a create (etag 0) seeded from the stored policy under a fresh name.
  const openCopy = async (name: string) => {
    const { data } = await policiesApi.get(name)
    const policy = duplicateDoc({ ...data, steps: data.steps ?? [] }, (policies ?? []).map((p) => p.name))
    setEditing({ policy, etag: 0, copyOf: name })
  }
  useEffect(() => { if (createRef) createRef.current = () => void open() })

  if (isError && !policies) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }

  return (
    <div className="space-y-3">
      {(policies?.length ?? 0) === 0 ? <Empty text={t('empty')} /> : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('name')}</TableHead>
              <TableHead>{t('steps')}</TableHead>
              <TableHead>{t('stepsOverview')}</TableHead>
              <TableHead>{t('actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {policies!.map((p) => (
              <TableRow key={p.name}>
                <TableCell className="font-medium text-foreground">{p.name}</TableCell>
                <TableCell className="text-xs text-muted-foreground tabular-nums">{p.steps?.length ?? 0}</TableCell>
                <TableCell className="text-xs text-muted-foreground font-mono truncate">
                  {(p.steps ?? []).map((s) => `+${s.after}`).join(' → ') || '—'}
                </TableCell>
                <TableCell className="text-right">
                  <div className="flex gap-1 justify-end">
                    <Button size="sm" variant="outline" onClick={() => open(p.name)}>{t('edit')}</Button>
                    <DuplicateButton onClick={() => void openCopy(p.name)} />
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {editing && <PolicyDialog state={editing} pickers={pickers} onClose={() => setEditing(null)} />}
    </div>
  )
}

function PolicyDialog({ state, pickers, onClose }: {
  state: { policy: EscalationPolicy; etag: number; copyOf?: string }; pickers: Pickers; onClose: () => void
}) {
  // etag 0 = not stored yet: a blank form or a duplicate (stored docs are ≥ 1).
  const isNew = state.etag === 0
  const [p, setP] = useState<EscalationPolicy>(state.policy)
  const [sim, setSim] = useState<{ steps: Record<string, unknown>[] } | null>(null)
  const [simErr, setSimErr] = useState<unknown>(null)

  const setStep = (i: number, step: EscalationStep) =>
    setP((prev) => ({ ...prev, steps: prev.steps.map((s, j) => (j === i ? step : s)) }))
  const addStep = () => setP((prev) => ({ ...prev, steps: [...prev.steps, emptyStep()] }))
  const removeStep = (i: number) => setP((prev) => ({ ...prev, steps: prev.steps.filter((_, j) => j !== i) }))
  const move = (i: number, dir: -1 | 1) => setP((prev) => {
    const steps = [...prev.steps]
    const j = i + dir
    const a = steps[i], b = steps[j]
    if (j < 0 || j >= steps.length || !a || !b) return prev
    steps[i] = b
    steps[j] = a
    return { ...prev, steps }
  })

  const save = useSave(
    (doc: EscalationPolicy) => isNew ? policiesApi.create(doc) : policiesApi.update(doc.name, doc, state.etag),
    { invalidate: [policiesApi.queryKey], onDone: onClose },
  )
  const remove = useSave((name: string) => policiesApi.remove(name),
    { invalidate: [policiesApi.queryKey], onDone: onClose })

  const simulate = async () => {
    setSimErr(null); setSim(null)
    try { setSim(await post<{ steps: Record<string, unknown>[] }>(`/escalation-policies/${encodeURIComponent(p.name)}:simulate`, {})) }
    catch (e) { setSimErr(e) }
  }

  const onSubmit = (e: React.FormEvent) => { e.preventDefault(); save.mutate(p) }

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{state.copyOf ? `${t('duplicate')}: ${state.copyOf}` : isNew ? t('create') : `${t('edit')}: ${state.policy.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} className="space-y-3">
          <Field label={t('name')} required>
            <Input value={p.name} disabled={!isNew} onChange={(e) => setP({ ...p, name: e.target.value })} placeholder="standard-oncall" />
          </Field>

          <div className="space-y-2">
            <span className="text-xs text-muted-foreground font-medium">{t('steps')}</span>
            {p.steps.map((step, i) => (
              <StepCard
                key={i} index={i} step={step} pickers={pickers}
                onChange={(s) => setStep(i, s)}
                onRemove={() => removeStep(i)}
                onUp={i > 0 ? () => move(i, -1) : undefined}
                onDown={i < p.steps.length - 1 ? () => move(i, 1) : undefined}
              />
            ))}
            <Button size="sm" variant="outline" type="button" onClick={addStep}>+ {t('step')}</Button>
          </div>

          <FormError error={save.error} />

          {/* Simulator */}
          <div className="border border-border rounded-lg p-3 bg-card/40 space-y-2">
            <div className="flex items-center justify-between">
              <span className="text-xs font-semibold text-foreground/90">{t('simulate')} — {t('whoNotifiedWhen')}</span>
              <Button size="sm" variant="outline" type="button" onClick={simulate} disabled={isNew} title={isNew ? t('saveFirst') : undefined}>
                {t('simulate')}
              </Button>
            </div>
            {isNew && <p className="text-[11px] text-muted-foreground">{t('savePolicyFirst')}</p>}
            <FormError error={simErr} />
            {sim && <SimTimeline steps={sim.steps} />}
          </div>

          <div className="flex items-center justify-between pt-2">
            {!isNew ? <DeleteButton onDelete={() => remove.mutate(state.policy.name)} /> : <span />}
            <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!p.name} />
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function StepCard({ index, step, pickers, onChange, onRemove, onUp, onDown }: {
  index: number; step: EscalationStep; pickers: Pickers
  onChange: (s: EscalationStep) => void
  onRemove: () => void; onUp?: () => void; onDown?: () => void
}) {
  const notifyKind: NotifyKind =
    step.notify?.contact !== undefined ? 'contact'
    : step.notify?.contactGroup !== undefined ? 'contactGroup'
    : 'schedule'
  const set = (patch: Partial<EscalationStep>) => onChange({ ...step, ...patch })
  const setNotify = (kind: NotifyKind) => {
    if (kind === 'schedule') set({ notify: { schedule: '' } })
    else if (kind === 'contact') set({ notify: { contact: '' } })
    else set({ notify: { contactGroup: '' } })
  }
  const showAction = step.action?.webhook !== undefined

  return (
    <Card className="border-border/80 py-3 gap-3">
      <CardContent className="px-3">
        <div className="flex items-center justify-between mb-2">
          <span className="text-xs font-semibold text-muted-foreground">{t('step')} {index + 1}</span>
          <div className="flex gap-1">
            {onUp && <Button size="sm" variant="ghost" type="button" onClick={onUp} title={t('up')} aria-label={t('up')}><ArrowUp size={13} /></Button>}
            {onDown && <Button size="sm" variant="ghost" type="button" onClick={onDown} title={t('down')} aria-label={t('down')}><ArrowDown size={13} /></Button>}
            <Button size="sm" variant="ghost" type="button" onClick={onRemove} title={t('remove')} aria-label={t('remove')}><X size={13} /></Button>
          </div>
        </div>

        <div className="grid grid-cols-2 gap-3">
          <Field label={t('after')} hint={t('afterHint')}>
            <DurationInput value={step.after} onChange={(v) => set({ after: v })} placeholder="0s" />
          </Field>
          <div className="flex items-end pb-1">
            <ToggleRow label={t('unlessAcked')} checked={step.unlessAcked ?? false} onChange={(v) => set({ unlessAcked: v })} />
          </div>
        </div>

        {/* Notify target */}
        <div className="mt-2">
          <span className="text-xs text-muted-foreground font-medium">{t('notify')}</span>
          <div className="flex gap-4 mt-1 mb-1.5">
            {(['schedule', 'contact', 'contactGroup'] as NotifyKind[]).map((k) => (
              <label key={k} className="flex items-center gap-1.5 text-sm text-foreground/90 cursor-pointer">
                <input type="radio" checked={notifyKind === k} onChange={() => setNotify(k)} />
                {k === 'schedule' ? t('rosterSchedule') : k === 'contact' ? t('contact') : t('contactGroup')}
              </label>
            ))}
          </div>
          {notifyKind === 'schedule' && (
            <div className="grid grid-cols-2 gap-3">
              <Select value={step.notify?.schedule ? step.notify.schedule : NONE}
                onValueChange={(v) => set({ notify: { ...step.notify, schedule: v === NONE ? '' : v } })}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>{t('selectSchedule')}</SelectItem>
                  {pickers.schedules.map((s) => <SelectItem key={s.name} value={s.name}>{s.name}</SelectItem>)}
                </SelectContent>
              </Select>
              <Select value={step.notify?.escalateTo ? step.notify.escalateTo : NONE}
                onValueChange={(v) => set({ notify: { ...step.notify, escalateTo: v === NONE ? undefined : v } })}>
                <SelectTrigger className="w-full" title={t('whoFromRotation')}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>{t('primary')}</SelectItem>
                  <SelectItem value="backup">{t('backupSecond')}</SelectItem>
                </SelectContent>
              </Select>
            </div>
          )}
          {notifyKind === 'contact' && (
            <Select value={step.notify?.contact ? step.notify.contact : NONE}
              onValueChange={(v) => set({ notify: { contact: v === NONE ? '' : v } })}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NONE}>{t('selectContact')}</SelectItem>
                {pickers.contacts.map((c) => <SelectItem key={c.name} value={c.name}>{c.name}</SelectItem>)}
              </SelectContent>
            </Select>
          )}
          {notifyKind === 'contactGroup' && (
            <Select value={step.notify?.contactGroup ? step.notify.contactGroup : NONE}
              onValueChange={(v) => set({ notify: { contactGroup: v === NONE ? '' : v } })}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NONE}>{t('selectGroup')}</SelectItem>
                {pickers.groups.map((g) => <SelectItem key={g.name} value={g.name}>{g.name}</SelectItem>)}
              </SelectContent>
            </Select>
          )}
        </div>

        <div className="mt-2">
          <span className="text-xs text-muted-foreground font-medium">{t('channels')}</span>
          <div className="mt-1"><ChannelPicker value={step.channels ?? []} onChange={(v) => set({ channels: v })} /></div>
        </div>

        <div className="grid grid-cols-2 gap-3 mt-2">
          <Field label={t('repeatEvery')} hint="optional">
            <DurationInput value={step.repeatEvery ?? ''} onChange={(v) => set({ repeatEvery: v || undefined })} placeholder="10m" />
          </Field>
          <Field label={t('maxRepeats')} hint="optional">
            <Input type="number" value={step.maxRepeats ?? ''} onChange={(e) => set({ maxRepeats: e.target.value ? Number(e.target.value) : undefined })} placeholder="3" />
          </Field>
        </div>

        {/* Action webhook */}
        <div className="mt-2">
          <ToggleRow
            label={t('actionTriggerWebhook')}
            checked={showAction}
            onChange={(on) => set({ action: on ? { webhook: '' } : undefined })}
          />
          {showAction && (
            <div className="mt-1">
              <Select value={step.action?.webhook ? step.action.webhook : NONE}
                onValueChange={(v) => set({ action: { webhook: v === NONE ? '' : v } })}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>{t('selectWebhookChannel')}</SelectItem>
                  {pickers.channels.filter((c) => c.type === 'webhook').map((c) => <SelectItem key={c.name} value={c.name}>{c.name}</SelectItem>)}
                </SelectContent>
              </Select>
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  )
}

// Render the simulate response: one row per step (who, when, channels).
function SimTimeline({ steps }: { steps: Record<string, unknown>[] }) {
  if (steps.length === 0) return <Empty text={t('noSteps')} />
  return (
    <ol className="space-y-1.5">
      {steps.map((s, i) => {
        const who = Array.isArray(s.notify) ? (s.notify as string[]) : []
        const channels = Array.isArray(s.channels) ? (s.channels as string[]) : []
        return (
          <li key={i} className="flex items-start gap-3 bg-card/60 border border-border rounded-md px-3 py-2">
            <span className="text-xs font-mono text-primary mt-0.5 shrink-0">+{String(s.after ?? '')}</span>
            <div className="min-w-0 flex-1">
              <div className="text-sm text-foreground">
                {who.length > 0 ? who.join(', ') : <span className="text-muted-foreground">{t('nobodyResolved')}</span>}
                {s.schedule ? <span className="text-xs text-muted-foreground"> · {String(s.schedule)}</span> : null}
                {s.unlessAcked ? <span className="text-[11px] text-amber-400"> · {t('onlyIfOpen')}</span> : null}
              </div>
              {channels.length > 0 && <div className="text-[11px] text-muted-foreground font-mono">{channels.join(', ')}</div>}
              {s.repeatEvery ? <div className="text-[11px] text-muted-foreground inline-flex items-center gap-1"><RefreshCw size={11} /> {t('every')} {String(s.repeatEvery)} (max {String(s.maxRepeats ?? '∞')})</div> : null}
              {s.action ? <div className="text-[11px] text-muted-foreground inline-flex items-center gap-1"><Settings size={11} /> {t('action')}</div> : null}
            </div>
          </li>
        )
      })}
    </ol>
  )
}
