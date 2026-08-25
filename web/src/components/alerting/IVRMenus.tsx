// IVR-menus tab (SPEC §9.6 evolution): DTMF phone menus for alarm lines.
// CRUD editor following the Escalations pattern: list table + dialog with
// an ordered option editor. Each option binds one digit to an action;
// trigger-alarm options carry severity/title/escalation policy/record plus
// an np.sound label for the alarm app.
import { useEffect, useState, type RefObject } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ArrowUp, ArrowDown, X } from 'lucide-react'
import { resourceApi } from '../../api'
import type { EscalationPolicy, IVRAction, IVRMenu, IVROption, Severity } from '../../types'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
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
import { Empty, ErrorState, Field, FormError, SubmitRow, DeleteButton, useSave, DuplicateButton } from '@/components/kit'
import { duplicateDoc } from '@/lib/duplicate'
import { t } from '../../i18n'
import { ToggleRow } from './common'

const menusApi = resourceApi<IVRMenu>('ivr-menus')
const policiesApi = resourceApi<EscalationPolicy>('escalation-policies')

// Radix <SelectItem> cannot use "" — sentinel for the "no selection" option.
const NONE = '__none__'

const DIGITS = ['0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '*', '#'] as const
const ACTIONS: IVRAction[] = ['trigger-alarm', 'list-alerts', 'ack-alert', 'resolve-alert', 'say']
// Manual alarms are always problems — no 'ok' severity here.
const OPTION_SEVERITIES: Severity[] = ['critical', 'warning', 'info']
// Alarm-app sounds (np.sound label contract, see northplane-alarm README).
const NP_SOUNDS = ['none', 'np_klaxon', 'np_sirene', 'np_puls'] as const

function emptyOption(): IVROption {
  return { digit: '1', action: 'trigger-alarm' }
}
function emptyMenu(): IVRMenu {
  return { name: '', options: [emptyOption()] }
}

export function IVRMenusTab({ createRef }: { createRef?: RefObject<() => void> }) {
  const { data: menus, isError, error, refetch } = useQuery({ queryKey: menusApi.queryKey, queryFn: menusApi.list })
  const { data: policies } = useQuery({ queryKey: policiesApi.queryKey, queryFn: policiesApi.list })
  const [editing, setEditing] = useState<{ menu: IVRMenu; etag: number; copyOf?: string } | null>(null)

  const open = async (name?: string) => {
    if (!name) { setEditing({ menu: emptyMenu(), etag: 0 }); return }
    const { data, etag } = await menusApi.get(name)
    setEditing({ menu: { ...data, options: data.options ?? [] }, etag })
  }
  // Duplicate: a create (etag 0) seeded from the stored menu under a fresh name.
  const openCopy = async (name: string) => {
    const { data } = await menusApi.get(name)
    const menu = duplicateDoc({ ...data, options: data.options ?? [] }, (menus ?? []).map((x) => x.name))
    setEditing({ menu, etag: 0, copyOf: name })
  }
  useEffect(() => { if (createRef) createRef.current = () => void open() })

  if (isError && !menus) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }

  return (
    <div className="space-y-3">
      {(menus?.length ?? 0) === 0 ? <Empty text={t('noIvrMenusFriendly')} /> : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('name')}</TableHead>
              <TableHead>{t('languageField')}</TableHead>
              <TableHead>{t('options')}</TableHead>
              <TableHead>{t('optionsOverview')}</TableHead>
              <TableHead>{t('actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {menus!.map((m) => (
              <TableRow key={m.name}>
                <TableCell className="font-medium text-foreground">{m.name}</TableCell>
                <TableCell className="text-xs text-muted-foreground">{m.language ?? '—'}</TableCell>
                <TableCell className="text-xs text-muted-foreground tabular-nums">{m.options?.length ?? 0}</TableCell>
                <TableCell className="text-xs text-muted-foreground font-mono truncate max-w-72">
                  {(m.options ?? []).map((o) => `${o.digit}=${o.action}`).join(' · ') || '—'}
                </TableCell>
                <TableCell className="text-right">
                  <div className="flex gap-1 justify-end">
                    <Button size="sm" variant="outline" onClick={() => open(m.name)}>{t('edit')}</Button>
                    <DuplicateButton onClick={() => void openCopy(m.name)} />
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {editing && (
        <MenuDialog state={editing} policies={policies ?? []} onClose={() => setEditing(null)} />
      )}
    </div>
  )
}

function MenuDialog({ state, policies, onClose }: {
  state: { menu: IVRMenu; etag: number; copyOf?: string }; policies: EscalationPolicy[]; onClose: () => void
}) {
  // etag 0 = not stored yet: a blank form or a duplicate (stored docs are ≥ 1).
  const isNew = state.etag === 0
  const [m, setM] = useState<IVRMenu>(state.menu)
  const set = (patch: Partial<IVRMenu>) => setM((prev) => ({ ...prev, ...patch }))

  const setOption = (i: number, o: IVROption) =>
    setM((prev) => ({ ...prev, options: prev.options.map((x, j) => (j === i ? o : x)) }))
  const addOption = () => setM((prev) => ({ ...prev, options: [...prev.options, emptyOption()] }))
  const removeOption = (i: number) => setM((prev) => ({ ...prev, options: prev.options.filter((_, j) => j !== i) }))
  const move = (i: number, dir: -1 | 1) => setM((prev) => {
    const options = [...prev.options]
    const j = i + dir
    const a = options[i], b = options[j]
    if (j < 0 || j >= options.length || !a || !b) return prev
    options[i] = b
    options[j] = a
    return { ...prev, options }
  })

  const save = useSave(
    (doc: IVRMenu) => isNew ? menusApi.create(doc) : menusApi.update(doc.name, doc, state.etag),
    { invalidate: [menusApi.queryKey], onDone: onClose },
  )
  const remove = useSave((name: string) => menusApi.remove(name),
    { invalidate: [menusApi.queryKey], onDone: onClose })

  const onSubmit = (e: React.FormEvent) => { e.preventDefault(); save.mutate(m) }

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      {/* Viewport-bounded with an internal scroll region: menus with many
          options grew taller than the screen and pushed Save below the
          fold — the action row now stays pinned (FORM-5). */}
      <DialogContent className="max-w-2xl max-h-[85vh] flex flex-col">
        <DialogHeader>
          <DialogTitle>{state.copyOf ? `${t('duplicate')}: ${state.copyOf}` : isNew ? t('create') : `${t('edit')}: ${state.menu.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col min-h-0 flex-1">
          <div className="space-y-3 overflow-y-auto min-h-0 flex-1 pr-1">
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('name')} required>
              <Input value={m.name} disabled={!isNew}
                onChange={(e) => set({ name: e.target.value })} placeholder="alarmzentrale" />
            </Field>
            <Field label={t('languageField')} hint={t('ttsLanguageHint')}>
              <Input value={m.language ?? ''} onChange={(e) => set({ language: e.target.value || undefined })}
                placeholder="de-DE" />
            </Field>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <Field label={t('voiceField')} hint={t('voiceHint')}>
              <Input value={m.voice ?? ''} onChange={(e) => set({ voice: e.target.value || undefined })} />
            </Field>
            <Field label="PIN" hint={t('pinHint')}>
              <Input value={m.pin ?? ''} onChange={(e) => set({ pin: e.target.value || undefined })}
                inputMode="numeric" placeholder="4711" />
            </Field>
          </div>

          <Field label={t('greeting')} hint={t('greetingHint')}>
            <Textarea value={m.greeting ?? ''} rows={2}
              onChange={(e) => set({ greeting: e.target.value || undefined })} />
          </Field>

          <ToggleRow label={t('trustCallerId')} hint={t('trustCallerIdHint')}
            checked={m.trustCallerId ?? false} onChange={(v) => set({ trustCallerId: v || undefined })} />

          <div className="space-y-2">
            <span className="text-xs text-muted-foreground font-medium">{t('options')}</span>
            {m.options.map((o, i) => (
              <OptionCard
                key={i} index={i} option={o} policies={policies}
                onChange={(next) => setOption(i, next)}
                onRemove={() => removeOption(i)}
                onUp={i > 0 ? () => move(i, -1) : undefined}
                onDown={i < m.options.length - 1 ? () => move(i, 1) : undefined}
              />
            ))}
            <Button size="sm" variant="outline" type="button" onClick={addOption}>+ {t('option')}</Button>
          </div>

          <FormError error={save.error} />
          </div>

          <div className="flex items-center justify-between pt-3 mt-3 border-t border-border shrink-0">
            {!isNew ? <DeleteButton onDelete={() => remove.mutate(state.menu.name)} /> : <span />}
            <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!m.name} />
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function OptionCard({ index, option, policies, onChange, onRemove, onUp, onDown }: {
  index: number; option: IVROption; policies: EscalationPolicy[]
  onChange: (o: IVROption) => void
  onRemove: () => void; onUp?: () => void; onDown?: () => void
}) {
  const set = (patch: Partial<IVROption>) => onChange({ ...option, ...patch })
  // np.sound travels inside option.labels — keep other labels untouched.
  const sound = option.labels?.['np.sound'] ?? 'none'
  const setSound = (v: string) => {
    const labels = { ...(option.labels ?? {}) }
    if (v === 'none') delete labels['np.sound']
    else labels['np.sound'] = v
    set({ labels: Object.keys(labels).length ? labels : undefined })
  }

  return (
    <Card className="border-border/80 py-3 gap-3">
      <CardContent className="px-3">
        <div className="flex items-center justify-between mb-2">
          <span className="text-xs font-semibold text-muted-foreground">{t('option')} {index + 1}</span>
          <div className="flex gap-1">
            {onUp && <Button size="sm" variant="ghost" type="button" onClick={onUp} title={t('up')} aria-label={t('up')}><ArrowUp size={13} /></Button>}
            {onDown && <Button size="sm" variant="ghost" type="button" onClick={onDown} title={t('down')} aria-label={t('down')}><ArrowDown size={13} /></Button>}
            <Button size="sm" variant="ghost" type="button" onClick={onRemove} title={t('remove')} aria-label={t('remove')}><X size={13} /></Button>
          </div>
        </div>

        <div className="grid grid-cols-3 gap-3">
          <Field label={t('digit')}>
            <Select value={option.digit} onValueChange={(v) => set({ digit: v })}>
              <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
              <SelectContent>
                {DIGITS.map((d) => <SelectItem key={d} value={d}>{d}</SelectItem>)}
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('action')}>
            <Select value={option.action} onValueChange={(v) => set({ action: v as IVRAction })}>
              <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
              <SelectContent>
                {ACTIONS.map((a) => <SelectItem key={a} value={a}>{a}</SelectItem>)}
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('labelField')} hint={t('labelFieldHint')}>
            <Input value={option.label ?? ''} onChange={(e) => set({ label: e.target.value || undefined })} />
          </Field>
        </div>

        {option.action === 'trigger-alarm' && (
          <div className="mt-2 space-y-2">
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('severityLevel')}>
                <Select value={option.severity ?? 'critical'} onValueChange={(v) => set({ severity: v as Severity })}>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {OPTION_SEVERITIES.map((s) => <SelectItem key={s} value={s}>{s}</SelectItem>)}
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('title')} hint="optional — {caller} / {called}">
                <Input value={option.title ?? ''} onChange={(e) => set({ title: e.target.value || undefined })}
                  placeholder="Telefonalarm von {caller}" />
              </Field>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <Field label={t('escalationPolicyField')}>
                <Select value={option.escalationPolicy ?? NONE}
                  onValueChange={(v) => set({ escalationPolicy: v === NONE ? undefined : v })}>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NONE}>— {t('none')} —</SelectItem>
                    {policies.map((p) => <SelectItem key={p.name} value={p.name}>{p.name}</SelectItem>)}
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('npSound')}>
                <Select value={sound} onValueChange={setSound}>
                  <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {NP_SOUNDS.map((s) => (
                      <SelectItem key={s} value={s}>{s === 'none' ? t('none') : s}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </div>
            <ToggleRow label={t('recordCall')} checked={option.record ?? false}
              onChange={(v) => set({ record: v || undefined })} />
          </div>
        )}

        {option.action === 'say' && (
          <div className="mt-2">
            <Field label={t('text')}>
              <Textarea value={option.text ?? ''} rows={2}
                onChange={(e) => set({ text: e.target.value || undefined })} />
            </Field>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
