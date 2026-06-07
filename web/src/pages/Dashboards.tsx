// Dashboards + Wallboard (CMP Visualization-Dashboard parity, SPEC §12.3).
// List → create → grid view with an inline edit mode (resize, add/remove
// widgets, titles) saved via ETag PUT. ?wallboard renders chrome-free
// (Layout strips the shell) and auto-refreshes for big-screen displays.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { resourceApi } from '../api'
import type { DashboardDoc, DashboardWidget } from '../types'
import { Button, Card, Dialog, Empty, Spinner, Badge } from '../components/ui'
import { Field, Select, TextArea, Toggle, FormError, SubmitRow, useSave, DeleteButton } from '../components/forms'
import { Input } from '../components/ui'
import { WidgetBody, widgetTypeLabel } from '../components/dash/widgets'
import { ObjectPicker, MetricPicker } from '../components/dash/pickers'
import { t } from '../i18n'

const dashApi = resourceApi<DashboardDoc>('dashboards')

// ——— grid placement: Tailwind can't interpolate, so map to fixed classes ———
const COL: Record<number, string> = {
  1: 'col-span-1', 2: 'col-span-2', 3: 'col-span-3', 4: 'col-span-4',
  5: 'col-span-5', 6: 'col-span-6', 7: 'col-span-7', 8: 'col-span-8',
  9: 'col-span-9', 10: 'col-span-10', 11: 'col-span-11', 12: 'col-span-12',
}
const ROW_PX: Record<number, number> = { 1: 120, 2: 248, 3: 376 }

function defaultWidget(type: DashboardWidget['type']): DashboardWidget {
  const base: DashboardWidget = { type, w: 6, h: 1 }
  switch (type) {
    case 'counters': return { ...base, w: 12, h: 1 }
    case 'metric': return { ...base, w: 6, h: 2, range: '3h' }
    case 'problems': return { ...base, w: 6, h: 2, limit: 10 }
    case 'alerts': return { ...base, w: 6, h: 2, limit: 10 }
    case 'bpi': return { ...base, w: 6, h: 2 }
    case 'markdown': return { ...base, w: 6, h: 1, text: '' }
    default: return base
  }
}

// ————————————————————————— LIST —————————————————————————

export function DashboardsPage() {
  const navigate = useNavigate()
  const [creating, setCreating] = useState(false)
  const { data, isLoading } = useQuery({ queryKey: dashApi.queryKey, queryFn: dashApi.list })

  const create = useSave(
    (doc: DashboardDoc) => dashApi.create(doc),
    { invalidate: [dashApi.queryKey as unknown as string[]], onDone: () => setCreating(false) },
  )
  const remove = useSave(
    (name: string) => dashApi.remove(name),
    { invalidate: [dashApi.queryKey as unknown as string[]] },
  )

  const [name, setName] = useState('')
  const [shared, setShared] = useState(false)

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-bold">{t('dashboards')}</h1>
        <Button variant="primary" onClick={() => { setName(''); setShared(false); setCreating(true) }}>
          {t('newDashboard')}
        </Button>
      </div>

      {isLoading && <Spinner />}
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}

      <div className="grid sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {(data ?? []).map((d) => (
          <Card key={d.name}>
            <div className="flex items-start justify-between gap-2">
              <Link
                to="/dashboards/$name" params={{ name: d.name }}
                className="font-semibold text-slate-200 hover:text-blue-300"
              >
                ◫ {d.name}
              </Link>
              {d.shared && <Badge className="bg-sky-500/15 text-sky-400 border-sky-500/30">geteilt</Badge>}
            </div>
            <div className="text-xs text-slate-500 mt-2">
              {(d.spec?.widgets?.length ?? 0)} {t('widget')}
            </div>
            <div className="flex items-center justify-between mt-3">
              <Link
                to="/dashboards/$name" params={{ name: d.name }}
                className="text-xs text-blue-400 hover:text-blue-300"
              >
                Öffnen →
              </Link>
              <DeleteButton onDelete={() => remove.mutate(d.name)} />
            </div>
          </Card>
        ))}
      </div>

      <Dialog open={creating} onClose={() => setCreating(false)} title={t('newDashboard')}>
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (!name.trim()) return
            create.mutate(
              { name: name.trim(), shared, spec: { widgets: [defaultWidget('counters')] } },
              { onSuccess: () => navigate({ to: '/dashboards/$name', params: { name: name.trim() } }) },
            )
          }}
        >
          <Field label={t('name')} required>
            <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
          <Field label="Geteilt (für alle sichtbar)">
            <Toggle checked={shared} onChange={setShared} label={shared ? 'ja' : 'nein'} />
          </Field>
          <FormError error={create.error} />
          <SubmitRow onCancel={() => setCreating(false)} saving={create.isPending} label={t('create')} disabled={!name.trim()} />
        </form>
      </Dialog>
    </div>
  )
}

// ————————————————————————— VIEW —————————————————————————

// Thin wrapper: reads the route params/search and keys the stateful body by
// name so navigating between dashboards remounts it (resets draft/editing
// without an effect).
export function DashboardViewPage() {
  const { name } = useParams({ strict: false }) as { name: string }
  const search = useSearch({ strict: false }) as Record<string, unknown>
  return <DashboardView key={name} name={name} wallboard={'wallboard' in search} />
}

function DashboardView({ name, wallboard }: { name: string; wallboard: boolean }) {
  const { data, isLoading } = useQuery({
    queryKey: [...dashApi.queryKey, name],
    queryFn: () => dashApi.get(name),
  })

  const [editing, setEditing] = useState(false)
  // Working copy of widgets while editing; null = use the server copy.
  const [draft, setDraft] = useState<DashboardWidget[] | null>(null)
  const [addOpen, setAddOpen] = useState(false)

  const widgets = draft ?? data?.data.spec?.widgets ?? []

  const save = useSave(
    async (next: DashboardWidget[]) => {
      // Re-fetch for a fresh ETag (other tabs may have written).
      const fresh = await dashApi.get(name)
      const doc: DashboardDoc = { ...fresh.data, name, spec: { widgets: next } }
      return dashApi.update(name, doc, fresh.etag)
    },
    {
      invalidate: [[...dashApi.queryKey, name] as unknown as string[], dashApi.queryKey as unknown as string[]],
      onDone: () => { setDraft(null); setEditing(false) },
    },
  )

  if (isLoading) return <Spinner />
  if (!data) return <Empty text={t('empty')} />

  const startEdit = () => { setDraft([...widgets]); setEditing(true) }
  const cancelEdit = () => { setDraft(null); setEditing(false) }
  const mutateWidget = (i: number, patch: Partial<DashboardWidget>) =>
    setDraft((cur) => (cur ?? widgets).map((wd, j) => (j === i ? { ...wd, ...patch } : wd)))
  const removeWidget = (i: number) =>
    setDraft((cur) => (cur ?? widgets).filter((_, j) => j !== i))
  const addWidget = (wd: DashboardWidget) =>
    setDraft((cur) => [...(cur ?? widgets), wd])

  return (
    <div className={wallboard ? 'p-6 space-y-4' : 'space-y-4'}>
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-2 min-w-0">
          {!wallboard && (
            <Link to="/dashboards" className="text-slate-500 hover:text-slate-300 text-sm shrink-0">←</Link>
          )}
          <h1 className={`font-bold truncate ${wallboard ? 'text-2xl' : 'text-lg'}`}>
            {wallboard && <span className="text-blue-400">▲ </span>}{name}
          </h1>
          {wallboard && (
            <span className="text-slate-500 text-sm tabular-nums ml-auto">{new Date().toLocaleTimeString()}</span>
          )}
        </div>
        {!wallboard && (
          <div className="flex items-center gap-2 shrink-0">
            {editing ? (
              <>
                <Button onClick={() => setAddOpen(true)}>+ {t('addWidget')}</Button>
                <Button variant="ghost" onClick={cancelEdit}>{t('cancel')}</Button>
                <Button variant="primary" onClick={() => save.mutate(widgets)} disabled={save.isPending}>
                  {save.isPending ? '…' : t('save')}
                </Button>
              </>
            ) : (
              <>
                <a
                  href={`/dashboards/${encodeURIComponent(name)}?wallboard`}
                  className="text-xs text-slate-500 hover:text-slate-300 px-2 py-1.5"
                >▣ {t('wallboard')}</a>
                <Button onClick={startEdit}>✎ {t('edit')}</Button>
              </>
            )}
          </div>
        )}
      </div>

      {save.error && <FormError error={save.error} />}

      {widgets.length === 0 ? (
        <Empty text={editing ? 'Noch keine Widgets — „Widget hinzufügen".' : t('empty')} />
      ) : (
        <div className="grid grid-cols-12 gap-3">
          {widgets.map((wd, i) => (
            <div key={i} className={COL[wd.w ?? 6] ?? 'col-span-6'}>
              <div className="bg-slate-900/60 border border-slate-800 rounded-xl h-full flex flex-col">
                <div className="flex items-center justify-between px-3 py-2 border-b border-slate-800">
                  <span className="text-xs font-semibold text-slate-400 truncate">
                    {wd.title || widgetTypeLabel(wd.type)}
                  </span>
                  {editing && (
                    <button
                      className="text-slate-500 hover:text-red-400 cursor-pointer text-sm shrink-0"
                      onClick={() => removeWidget(i)}
                      title={t('remove')}
                    >✕</button>
                  )}
                </div>
                {editing && (
                  <div className="px-3 py-2 border-b border-slate-800 space-y-2 bg-slate-950/40">
                    <WidgetEditFields widget={wd} onChange={(patch) => mutateWidget(i, patch)} />
                  </div>
                )}
                <div
                  className="p-3 overflow-auto flex-1"
                  style={{ minHeight: ROW_PX[wd.h ?? 1] ?? 120 }}
                >
                  <WidgetBody widget={wd} />
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      {addOpen && (
        <AddWidgetDialog onClose={() => setAddOpen(false)} onAdd={(wd) => { addWidget(wd); setAddOpen(false) }} />
      )}
    </div>
  )
}

// ——— per-widget edit controls (size, title + type-specific config) ———

function WidgetEditFields({ widget, onChange }: {
  widget: DashboardWidget; onChange: (patch: Partial<DashboardWidget>) => void
}) {
  return (
    <>
      <div className="grid grid-cols-3 gap-2">
        <Field label="Titel" className="col-span-3">
          <Input value={widget.title ?? ''} onChange={(e) => onChange({ title: e.target.value })} placeholder={widgetTypeLabel(widget.type)} />
        </Field>
        <Field label="Breite">
          <Select value={widget.w ?? 6} onChange={(e) => onChange({ w: Number(e.target.value) })}>
            {[3, 4, 6, 8, 12].map((n) => <option key={n} value={n}>{n}/12</option>)}
          </Select>
        </Field>
        <Field label="Höhe">
          <Select value={widget.h ?? 1} onChange={(e) => onChange({ h: Number(e.target.value) })}>
            {[1, 2, 3].map((n) => <option key={n} value={n}>{n}</option>)}
          </Select>
        </Field>
      </div>
      <WidgetConfigFields widget={widget} onChange={onChange} />
    </>
  )
}

// Type-specific configuration, reused by the add-widget dialog.
function WidgetConfigFields({ widget, onChange }: {
  widget: DashboardWidget; onChange: (patch: Partial<DashboardWidget>) => void
}) {
  switch (widget.type) {
    case 'metric':
      return (
        <div className="space-y-2">
          <Field label="Objekt">
            <ObjectPicker value={widget.object} onChange={(object) => onChange({ object, metric: '' })} />
          </Field>
          <Field label="Metrik">
            <MetricPicker objectId={widget.object} value={widget.metric} onChange={(metric) => onChange({ metric })} />
          </Field>
          <Field label="Zeitraum">
            <Select value={widget.range ?? '3h'} onChange={(e) => onChange({ range: e.target.value })}>
              {['1h', '3h', '24h', '7d'].map((r) => <option key={r} value={r}>{r}</option>)}
            </Select>
          </Field>
        </div>
      )
    case 'problems':
    case 'alerts':
      return (
        <div className="space-y-2">
          <Field label="Limit">
            <Input type="number" min={1} max={50} value={widget.limit ?? 10}
              onChange={(e) => onChange({ limit: Number(e.target.value) })} />
          </Field>
          {widget.type === 'problems' && (
            <Field label="Selector (optional)">
              <Input value={widget.selector ?? ''} placeholder="env=prod"
                onChange={(e) => onChange({ selector: e.target.value })} />
            </Field>
          )}
        </div>
      )
    case 'bpi':
      return (
        <Field label="Business Service (Name, leer = alle)">
          <Input value={widget.service ?? ''} placeholder="z.B. Webshop"
            onChange={(e) => onChange({ service: e.target.value })} />
        </Field>
      )
    case 'markdown':
      return (
        <Field label="Text">
          <TextArea value={widget.text ?? ''} onChange={(e) => onChange({ text: e.target.value })} rows={4} />
        </Field>
      )
    default:
      return null
  }
}

// ——— add-widget dialog: pick a type, configure, append ———

const WIDGET_TYPES: DashboardWidget['type'][] = ['counters', 'problems', 'alerts', 'metric', 'bpi', 'markdown']

// Rendered only while open (mounted fresh each time) so its working state
// resets without an effect.
function AddWidgetDialog({ onClose, onAdd }: {
  onClose: () => void; onAdd: (w: DashboardWidget) => void
}) {
  const [draft, setDraft] = useState<DashboardWidget>(() => defaultWidget('counters'))
  const changeType = (next: DashboardWidget['type']) => setDraft(defaultWidget(next))

  return (
    <Dialog open onClose={onClose} title={t('addWidget')} size="lg">
      <div className="space-y-3">
        <Field label={t('type')}>
          <Select value={draft.type} onChange={(e) => changeType(e.target.value as DashboardWidget['type'])}>
            {WIDGET_TYPES.map((tp) => <option key={tp} value={tp}>{widgetTypeLabel(tp)}</option>)}
          </Select>
        </Field>
        <Field label="Titel (optional)">
          <Input value={draft.title ?? ''} onChange={(e) => setDraft({ ...draft, title: e.target.value })}
            placeholder={widgetTypeLabel(draft.type)} />
        </Field>
        <WidgetConfigFields widget={draft} onChange={(patch) => setDraft((d) => ({ ...d, ...patch }))} />
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button variant="primary" onClick={() => onAdd(draft)}>{t('add')}</Button>
        </div>
      </div>
    </Dialog>
  )
}
