// Dashboards + Wallboard (CMP Visualization-Dashboard parity, SPEC §12.3).
// List → create → a Grafana-style free-positioning grid: in edit mode every
// panel can be dragged by its header and resized from the corner, with the rest
// reflowing live (grid.ts). A dashboard-level time range + auto-refresh drive
// every widget (DashViewCtx). ?wallboard renders chrome-free + auto-refreshing
// for big-screen displays. Saved via ETag PUT.
import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import {
  ArrowLeft, ArrowRight, Maximize2, X, LayoutGrid, Pencil, Radar, Loader2,
  GripVertical, Settings2, Copy, RefreshCw, Clock,
  Activity, BarChart3, Bell, Donut, Gauge, Network, Table, TriangleAlert, FileText, Sigma,
} from 'lucide-react'
import { APIError, resourceApi } from '../api'
import { dashboardDocSchema } from '../schemas'
import type { DashboardDoc, DashboardWidget } from '../types'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { Empty, Spinner, ErrorState, Field, FormError, SubmitRow, useSave, DeleteButton } from '@/components/kit'
import { WidgetBody } from '../components/dash/widgets'
import { widgetTypeLabel } from '../components/dash/util'
import { ObjectPicker, MetricPicker } from '../components/dash/pickers'
import { cn } from '@/lib/utils'
import { GridLayout, type DragHandleProps } from '../components/dash/GridLayout'
import { toLayout, layoutBottom, flowLayout, type GridItem } from '../components/dash/grid'
import { DashViewCtx, RANGE_TOKENS, REFRESH_TOKENS, refreshMsFor } from '../components/dash/ctx'
import { t } from '../i18n'

// Pass the zod schema so the single-doc read validates the (opaque,
// frontend-owned) widget JSON at the boundary — a malformed spec becomes one
// clear APIError instead of an obscure crash inside a widget.
const dashApi = resourceApi<DashboardDoc>('dashboards', dashboardDocSchema)

// Radix SelectItem value cannot be "" — sentinel for the table widget's
// empty scope ("Hosts + Services"), mapped back to undefined in the config.
const BOTH_SCOPE = '__both__'

const clampNum = (v: string, lo: number, hi: number) =>
  Math.max(lo, Math.min(hi, Math.round(Number(v) || lo)))

// Per-type icon so the type picker and panel headers read at a glance instead
// of being text-only / generic (DASH-6).
const WIDGET_ICONS: Record<DashboardWidget['type'], typeof Activity> = {
  counters: LayoutGrid,
  problems: TriangleAlert,
  alerts: Bell,
  metric: Activity,
  gauge: Gauge,
  stat: Sigma,
  donut: Donut,
  bar: BarChart3,
  table: Table,
  bpi: Network,
  markdown: FileText,
}

function WidgetTypeIcon({ type, size = 14, className }: {
  type: DashboardWidget['type']; size?: number; className?: string
}) {
  const Icon = WIDGET_ICONS[type]
  return <Icon size={size} className={className} />
}

function defaultWidget(type: DashboardWidget['type']): DashboardWidget {
  const base: DashboardWidget = { type, w: 6, h: 1 }
  switch (type) {
    case 'counters': return { ...base, w: 12, h: 1 }
    case 'metric': return { ...base, w: 6, h: 2, range: '3h' }
    case 'gauge': return { ...base, w: 3, h: 2 }
    case 'stat': return { ...base, w: 3, h: 2, range: '6h' }
    case 'donut': return { ...base, w: 4, h: 2, scope: 'services' }
    case 'bar': return { ...base, w: 6, h: 2, limit: 8 }
    case 'table': return { ...base, w: 12, h: 2, limit: 15 }
    case 'problems': return { ...base, w: 6, h: 2, limit: 10 }
    case 'alerts': return { ...base, w: 6, h: 2, limit: 10 }
    case 'bpi': return { ...base, w: 6, h: 2 }
    case 'markdown': return { ...base, w: 6, h: 1, text: '' }
    default: return base
  }
}

// applyLayout merges a grid layout's x/y/w/h back onto the widgets by index.
function applyLayout(widgets: DashboardWidget[], layout: GridItem[]): DashboardWidget[] {
  return widgets.map((wd, i) => {
    const li = layout.find((l) => l.i === i)
    return li ? { ...wd, x: li.x, y: li.y, w: li.w, h: li.h } : wd
  })
}

// placeNew gives a new/duplicated widget a slot at the bottom of the grid so it
// never overlaps and every widget keeps explicit positions (back-compat guard).
function placeNew(base: DashboardWidget[], wd: DashboardWidget): DashboardWidget {
  const y = layoutBottom(toLayout(base))
  return { ...wd, x: 0, y, w: wd.w ?? 6, h: wd.h ?? 1 }
}

// ————————————————————————— LIST —————————————————————————

export function DashboardsPage() {
  const navigate = useNavigate()
  const [creating, setCreating] = useState(false)
  const { data, isLoading, isError, error, refetch } = useQuery({ queryKey: dashApi.queryKey, queryFn: dashApi.list })

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

  if (isError && !data) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-bold">{t('dashboards')}</h1>
        <Button variant="default" onClick={() => { setName(''); setShared(false); setCreating(true) }}>
          {t('newDashboard')}
        </Button>
      </div>

      {isLoading && <Spinner />}
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}

      <div className="grid sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {(data ?? []).map((d) => (
          <Card key={d.name}>
            <CardContent>
              <div className="flex items-start justify-between gap-2">
                <Link
                  to="/dashboards/$name" params={{ name: d.name }}
                  className="inline-flex items-center gap-1.5 font-semibold text-foreground hover:text-primary"
                >
                  <LayoutGrid size={15} className="shrink-0" />{d.name}
                </Link>
                {d.shared && <Badge variant="outline" className="bg-sky-500/15 text-sky-400 border-sky-500/30">{t('shared')}</Badge>}
              </div>
              <div className="text-xs text-muted-foreground mt-2">
                {(d.spec?.widgets?.length ?? 0)} {(d.spec?.widgets?.length ?? 0) === 1 ? t('widget') : t('widgets')}
              </div>
              <div className="flex items-center justify-between mt-3">
                <Link
                  to="/dashboards/$name" params={{ name: d.name }}
                  className="text-xs text-primary hover:text-primary inline-flex items-center gap-1"
                >
                  {t('open')} <ArrowRight size={14} />
                </Link>
                <DeleteButton onDelete={() => remove.mutate(d.name)} />
              </div>
            </CardContent>
          </Card>
        ))}
      </div>

      <Dialog open={creating} onOpenChange={(o) => { if (!o) setCreating(false) }}>
        <DialogContent className="max-w-md">
          <DialogHeader><DialogTitle>{t('newDashboard')}</DialogTitle></DialogHeader>
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
            <Field label={t('sharedField')}>
              <Label className="cursor-pointer">
                <Switch checked={shared} onCheckedChange={setShared} />
                <span className="text-sm text-foreground/90">{shared ? t('yes') : t('no')}</span>
              </Label>
            </Field>
            <FormError error={create.error} />
            <SubmitRow onCancel={() => setCreating(false)} saving={create.isPending} label={t('create')} disabled={!name.trim()} />
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// ————————————————————————— VIEW —————————————————————————

// Thin wrapper: reads route params/search and keys the body by name so
// navigating between dashboards remounts it (resets draft/editing).
export function DashboardViewPage() {
  const { name } = useParams({ strict: false }) as { name: string }
  const search = useSearch({ strict: false }) as Record<string, unknown>
  return <DashboardView key={name} name={name} wallboard={'wallboard' in search} />
}

// DashboardView owns the fetch + loading/error; the stateful editor mounts only
// once the doc is loaded so its time/refresh state can initialise from the spec.
function DashboardView({ name, wallboard }: { name: string; wallboard: boolean }) {
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: [...dashApi.queryKey, name],
    queryFn: () => dashApi.get(name),
  })
  if (isError && !data) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }
  if (isLoading) return <Spinner />
  if (!data) return <Empty text={t('empty')} />
  return <DashboardEditor name={name} wallboard={wallboard} doc={data} />
}

function DashboardEditor({ name, wallboard, doc }: {
  name: string; wallboard: boolean; doc: { data: DashboardDoc; etag: number }
}) {
  const qc = useQueryClient()
  const [editing, setEditing] = useState(false)
  // Working copy of widgets while editing; null = use the server copy.
  const [draft, setDraft] = useState<DashboardWidget[] | null>(null)
  const [addOpen, setAddOpen] = useState(false)
  const [editIdx, setEditIdx] = useState<number | null>(null)
  // Dashboard-level time range + auto-refresh (Grafana's top-right controls).
  const [range, setRange] = useState(doc.data.spec?.time ?? '3h')
  const [refresh, setRefresh] = useState(doc.data.spec?.refresh ?? '30s')

  const widgets = useMemo(() => draft ?? doc.data.spec?.widgets ?? [], [draft, doc.data.spec?.widgets])
  const layout = useMemo(() => toLayout(widgets), [widgets])
  const ctxValue = useMemo(() => ({ range, refreshMs: refreshMsFor(refresh) }), [range, refresh])

  const save = useSave(
    async (spec: DashboardDoc['spec']) => {
      // Fast path: PUT with the ETag we already hold. A 409 (another tab wrote)
      // re-fetches once and retries on the fresh version.
      const put = (base: DashboardDoc, etag: number) =>
        dashApi.update(name, { ...base, name, spec }, etag)
      try {
        return await put(doc.data, doc.etag)
      } catch (err) {
        if (err instanceof APIError && err.status === 409) {
          const fresh = await dashApi.get(name)
          return put(fresh.data, fresh.etag)
        }
        throw err
      }
    },
    {
      invalidate: [[...dashApi.queryKey, name] as unknown as string[], dashApi.queryKey as unknown as string[]],
      onDone: () => { setDraft(null); setEditing(false); setEditIdx(null) },
    },
  )

  const startEdit = () => { setDraft(applyLayout(widgets, layout)); setEditing(true) }
  const cancelEdit = () => { setDraft(null); setEditing(false); setEditIdx(null) }
  const mutateWidget = (i: number, patch: Partial<DashboardWidget>) =>
    setDraft((cur) => (cur ?? widgets).map((wd, j) => (j === i ? { ...wd, ...patch } : wd)))
  const removeWidget = (i: number) => {
    setDraft((cur) => (cur ?? widgets).filter((_, j) => j !== i))
    setEditIdx(null)
  }
  const duplicateWidget = (i: number) =>
    setDraft((cur) => {
      const baseWidgets = cur ?? widgets
      const src = baseWidgets[i]
      if (!src) return baseWidgets
      return [...baseWidgets, placeNew(baseWidgets, { ...src, title: src.title ? `${src.title} ${t('copySuffix')}` : undefined })]
    })
  const addWidget = (wd: DashboardWidget) =>
    setDraft((cur) => { const base = cur ?? widgets; return [...base, placeNew(base, wd)] })
  // Tidy: re-flow every panel left-to-right and gravity-pack it, closing the
  // dead space that free dragging leaves behind (DASH-4).
  const tidyLayout = () =>
    setDraft((cur) => { const base = cur ?? widgets; return applyLayout(base, flowLayout(base)) })

  return (
    <DashViewCtx.Provider value={ctxValue}>
      <div className={wallboard ? 'p-6 space-y-4' : 'space-y-4'}>
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <div className="flex items-center gap-2 min-w-0">
            {!wallboard && (
              <Link to="/dashboards" className="text-muted-foreground hover:text-foreground/90 text-sm shrink-0" aria-label={t('back')}>
                <ArrowLeft size={16} />
              </Link>
            )}
            <h1 className={`font-bold truncate ${wallboard ? 'text-2xl' : 'text-lg'}`}>
              {wallboard && <Radar className="inline align-middle text-primary mr-2 -mt-1" size={22} />}{name}
            </h1>
            {wallboard && (
              <span className="text-muted-foreground text-sm tabular-nums ml-auto">{new Date().toLocaleTimeString()}</span>
            )}
          </div>
          {!wallboard && (
            <div className="flex items-center gap-2 shrink-0">
              <DashControls
                range={range} setRange={setRange}
                refresh={refresh} setRefresh={setRefresh}
                onRefreshNow={() => qc.invalidateQueries()}
              />
              <span className="w-px h-6 bg-border mx-1" />
              {editing ? (
                <>
                  <Button variant="outline" onClick={() => setAddOpen(true)}>+ {t('addWidget')}</Button>
                  <Button variant="ghost" onClick={tidyLayout} disabled={widgets.length === 0} title={t('tidyLayoutTitle')}>
                    <LayoutGrid size={14} /> {t('tidy')}
                  </Button>
                  <Button variant="ghost" onClick={cancelEdit}>{t('cancel')}</Button>
                  <Button variant="default" onClick={() => save.mutate({ widgets, time: range, refresh })} disabled={save.isPending}>
                    {save.isPending ? <Loader2 className="animate-spin" size={14} /> : t('save')}
                  </Button>
                </>
              ) : (
                <>
                  <a
                    href={`/dashboards/${encodeURIComponent(name)}?wallboard`}
                    className="text-xs text-muted-foreground hover:text-foreground/90 px-2 py-1.5 inline-flex items-center gap-1"
                  ><Maximize2 size={14} /> {t('wallboard')}</a>
                  <Button variant="outline" onClick={startEdit}><Pencil size={14} /> {t('edit')}</Button>
                </>
              )}
            </div>
          )}
        </div>

        {save.error && <FormError error={save.error} />}

        {widgets.length === 0 ? (
          <Empty text={editing ? t('noPanelsYet') : t('empty')} />
        ) : (
          <GridLayout
            layout={layout}
            editing={editing}
            onChange={(next) => setDraft((cur) => applyLayout(cur ?? widgets, next))}
            renderItem={(item, handle) => {
              const wd = widgets[item.i]
              if (!wd) return null
              return (
                <Panel
                  widget={wd} editing={editing} handle={handle}
                  onConfigure={() => setEditIdx(item.i)}
                  onDuplicate={() => duplicateWidget(item.i)}
                  onRemove={() => removeWidget(item.i)}
                />
              )
            }}
          />
        )}

        {addOpen && (
          <AddWidgetDialog onClose={() => setAddOpen(false)} onAdd={(wd) => { addWidget(wd); setAddOpen(false) }} />
        )}
        {editIdx !== null && widgets[editIdx] && (
          <WidgetEditDialog
            widget={widgets[editIdx]!}
            onChange={(patch) => mutateWidget(editIdx, patch)}
            onClose={() => setEditIdx(null)}
          />
        )}
      </div>
    </DashViewCtx.Provider>
  )
}

// ——— a single panel: chrome (drag handle + hover toolbar) around WidgetBody ———

function Panel({ widget, editing, handle, onConfigure, onDuplicate, onRemove }: {
  widget: DashboardWidget
  editing: boolean
  handle: DragHandleProps | null
  onConfigure: () => void
  onDuplicate: () => void
  onRemove: () => void
}) {
  return (
    <div className="group/panel h-full flex flex-col bg-card/60 border border-border rounded-xl overflow-hidden hover:border-primary/30 transition-colors">
      <div
        {...(handle ?? {})}
        className={`flex items-center justify-between px-3 py-2 border-b border-border ${editing ? 'bg-background/40' : ''}`}
      >
        <span className="text-xs font-semibold text-muted-foreground truncate flex items-center gap-1.5 min-w-0">
          {editing && <GripVertical size={13} className="opacity-40 shrink-0" />}
          <WidgetTypeIcon type={widget.type} size={13} className="opacity-50 shrink-0" />
          <span className="truncate">{widget.title || widgetTypeLabel(widget.type)}</span>
        </span>
        {editing && (
          <div
            className="flex items-center gap-0.5 shrink-0 opacity-0 group-hover/panel:opacity-100 focus-within:opacity-100 transition-opacity"
            onPointerDown={(e) => e.stopPropagation()}
          >
            <PanelBtn label={t('configure')} onClick={onConfigure}><Settings2 size={13} /></PanelBtn>
            <PanelBtn label={t('duplicate')} onClick={onDuplicate}><Copy size={13} /></PanelBtn>
            <PanelBtn label={t('remove')} onClick={onRemove} danger><X size={13} /></PanelBtn>
          </div>
        )}
      </div>
      <div className="p-3 overflow-auto flex-1">
        <WidgetBody widget={widget} />
      </div>
    </div>
  )
}

function PanelBtn({ label, onClick, danger, children }: {
  label: string; onClick: () => void; danger?: boolean; children: React.ReactNode
}) {
  return (
    <button
      type="button" onClick={onClick} title={label} aria-label={label}
      className={`p-1 rounded cursor-pointer text-muted-foreground hover:bg-muted/70 ${danger ? 'hover:text-red-400' : 'hover:text-foreground'}`}
    >{children}</button>
  )
}

// ——— dashboard-level time range + refresh (Grafana top-right controls) ———

function DashControls({ range, setRange, refresh, setRefresh, onRefreshNow }: {
  range: string; setRange: (v: string) => void
  refresh: string; setRefresh: (v: string) => void
  onRefreshNow: () => void
}) {
  return (
    <div className="flex items-center gap-1.5">
      <div className="flex items-center gap-1 text-muted-foreground">
        <Clock size={14} className="shrink-0" />
        <Select value={range} onValueChange={setRange}>
          <SelectTrigger className="h-8 w-[88px]" aria-label={t('timeRange')}><SelectValue /></SelectTrigger>
          <SelectContent>
            {RANGE_TOKENS.map((r) => <SelectItem key={r} value={r}>{r}</SelectItem>)}
          </SelectContent>
        </Select>
      </div>
      <Select value={refresh} onValueChange={setRefresh}>
        <SelectTrigger className="h-8 w-[84px]" aria-label={t('refreshInterval')}><SelectValue /></SelectTrigger>
        <SelectContent>
          {REFRESH_TOKENS.map((r) => <SelectItem key={r.value} value={r.value}>{r.label}</SelectItem>)}
        </SelectContent>
      </Select>
      <Button variant="ghost" size="icon" className="h-8 w-8" title={t('refreshNow')} aria-label={t('refreshNow')} onClick={onRefreshNow}>
        <RefreshCw size={14} />
      </Button>
    </div>
  )
}

// WidgetPreview: live render of a widget, shared by the add + edit dialogs so a
// data-source / config change is visible immediately (DASH-1). Rendered inside
// the dashboard's DashViewCtx, so its queries use the dashboard time range.
function WidgetPreview({ widget }: { widget: DashboardWidget }) {
  return (
    <div>
      <div className="text-xs text-muted-foreground uppercase tracking-wider mb-1.5">{t('preview')}</div>
      <div className="border border-border rounded-lg bg-card/40 p-3 h-56 overflow-auto">
        <WidgetBody widget={widget} />
      </div>
    </div>
  )
}

// ——— per-widget configuration dialog (replaces the old inline editor) ———

function WidgetEditDialog({ widget, onChange, onClose }: {
  widget: DashboardWidget
  onChange: (patch: Partial<DashboardWidget>) => void
  onClose: () => void
}) {
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-lg max-h-[90vh] overflow-y-auto">
        <DialogHeader><DialogTitle>{t('editPanel')} — {widgetTypeLabel(widget.type)}</DialogTitle></DialogHeader>
        <div className="space-y-3">
          <Field label={t('title')}>
            <Input value={widget.title ?? ''} onChange={(e) => onChange({ title: e.target.value })} placeholder={widgetTypeLabel(widget.type)} />
          </Field>
          <div className="grid grid-cols-2 gap-2">
            <Field label={t('widthCols')}>
              <Input type="number" min={1} max={12} value={widget.w ?? 6}
                onChange={(e) => onChange({ w: clampNum(e.target.value, 1, 12) })} />
            </Field>
            <Field label={t('heightRows')}>
              <Input type="number" min={1} max={8} value={widget.h ?? 1}
                onChange={(e) => onChange({ h: clampNum(e.target.value, 1, 8) })} />
            </Field>
          </div>
          <WidgetConfigFields widget={widget} onChange={onChange} />
          <WidgetPreview widget={widget} />
          <div className="flex justify-end pt-2">
            <Button variant="default" onClick={onClose}>{t('done')}</Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

// ThresholdFields: optional warn/crit overrides for chart-like widgets. Empty
// inputs clear the override so the metric's own perfdata thresholds apply.
function ThresholdFields({ widget, onChange }: {
  widget: DashboardWidget; onChange: (patch: Partial<DashboardWidget>) => void
}) {
  return (
    <div className="grid grid-cols-2 gap-2">
      <Field label={t('warnOptional')}>
        <Input type="number" value={widget.warn ?? ''} placeholder={t('egEighty')}
          onChange={(e) => onChange({ warn: e.target.value ? Number(e.target.value) : undefined })} />
      </Field>
      <Field label={t('critOptional')}>
        <Input type="number" value={widget.crit ?? ''} placeholder={t('egNinety')}
          onChange={(e) => onChange({ crit: e.target.value ? Number(e.target.value) : undefined })} />
      </Field>
    </div>
  )
}

// Type-specific configuration, reused by the add-widget + edit dialogs.
function WidgetConfigFields({ widget, onChange }: {
  widget: DashboardWidget; onChange: (patch: Partial<DashboardWidget>) => void
}) {
  switch (widget.type) {
    case 'metric':
      return (
        <div className="space-y-2">
          <Field label={t('object')}>
            <ObjectPicker value={widget.object} onChange={(object) => onChange({ object, metric: '' })} />
          </Field>
          <Field label={t('selectorOverlay')}>
            <Input value={widget.selector ?? ''} placeholder="env=prod"
              onChange={(e) => onChange({ selector: e.target.value })} />
          </Field>
          <Field label={t('metric')}>
            <MetricPicker objectId={widget.object} value={widget.metric} onChange={(metric) => onChange({ metric })} />
          </Field>
          <ThresholdFields widget={widget} onChange={onChange} />
        </div>
      )
    case 'gauge':
      return (
        <div className="space-y-2">
          <Field label={t('object')}>
            <ObjectPicker value={widget.object} onChange={(object) => onChange({ object, metric: '' })} />
          </Field>
          <Field label={t('metric')}>
            <MetricPicker objectId={widget.object} value={widget.metric} onChange={(metric) => onChange({ metric })} />
          </Field>
          <Field label={t('scaleMax')}>
            <Input type="number" min={1} value={widget.max ?? ''}
              onChange={(e) => onChange({ max: e.target.value ? Number(e.target.value) : undefined })} />
          </Field>
          <ThresholdFields widget={widget} onChange={onChange} />
        </div>
      )
    case 'stat':
      return (
        <div className="space-y-2">
          <Field label={t('object')}>
            <ObjectPicker value={widget.object} onChange={(object) => onChange({ object, metric: '' })} />
          </Field>
          <Field label={t('metric')}>
            <MetricPicker objectId={widget.object} value={widget.metric} onChange={(metric) => onChange({ metric })} />
          </Field>
          <ThresholdFields widget={widget} onChange={onChange} />
        </div>
      )
    case 'donut':
      return (
        <Field label={t('scope')}>
          <Select value={widget.scope ?? 'services'}
            onValueChange={(v) => onChange({ scope: v as 'services' | 'hosts' })}>
            <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="services">Services</SelectItem>
              <SelectItem value="hosts">Hosts</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      )
    case 'table':
      return (
        <div className="space-y-2">
          <Field label={t('scope')}>
            <Select value={widget.scope ?? BOTH_SCOPE}
              onValueChange={(v) => onChange({ scope: (v === BOTH_SCOPE ? undefined : v) as 'services' | 'hosts' | undefined })}>
              <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value={BOTH_SCOPE}>{t('hostsAndServices')}</SelectItem>
                <SelectItem value="hosts">Hosts</SelectItem>
                <SelectItem value="services">Services</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('selectorOptional')}>
            <Input value={widget.selector ?? ''} placeholder="env=prod"
              onChange={(e) => onChange({ selector: e.target.value })} />
          </Field>
          <Field label={t('fulltextFilter')}>
            <Input value={widget.query ?? ''} placeholder="db"
              onChange={(e) => onChange({ query: e.target.value })} />
          </Field>
          <Field label={t('limit')}>
            <Input type="number" min={1} max={100} value={widget.limit ?? 15}
              onChange={(e) => onChange({ limit: Number(e.target.value) })} />
          </Field>
        </div>
      )
    case 'bar':
      return (
        <div className="space-y-2">
          <Field label={t('object')}>
            <ObjectPicker value={widget.object} onChange={(object) => onChange({ object, metric: '' })} />
          </Field>
          <Field label={t('metricFilter')}>
            <MetricPicker objectId={widget.object} value={widget.metric} onChange={(metric) => onChange({ metric })} />
          </Field>
          <Field label={t('limit')}>
            <Input type="number" min={1} max={20} value={widget.limit ?? 8}
              onChange={(e) => onChange({ limit: Number(e.target.value) })} />
          </Field>
          <ThresholdFields widget={widget} onChange={onChange} />
        </div>
      )
    case 'problems':
    case 'alerts':
      return (
        <div className="space-y-2">
          <Field label={t('limit')}>
            <Input type="number" min={1} max={50} value={widget.limit ?? 10}
              onChange={(e) => onChange({ limit: Number(e.target.value) })} />
          </Field>
          {widget.type === 'problems' && (
            <Field label={t('selectorOptional')}>
              <Input value={widget.selector ?? ''} placeholder="env=prod"
                onChange={(e) => onChange({ selector: e.target.value })} />
            </Field>
          )}
        </div>
      )
    case 'bpi':
      return (
        <Field label={t('bpiServiceField')}>
          <Input value={widget.service ?? ''} placeholder={t('egWebshop')}
            onChange={(e) => onChange({ service: e.target.value })} />
        </Field>
      )
    case 'markdown':
      return (
        <Field label={t('text')}>
          <Textarea value={widget.text ?? ''} onChange={(e) => onChange({ text: e.target.value })} rows={4} className="font-mono" />
        </Field>
      )
    default:
      return null
  }
}

// ——— add-widget dialog: pick a type, configure, append ———

const WIDGET_TYPES: DashboardWidget['type'][] = [
  'counters', 'problems', 'alerts', 'table', 'metric', 'stat', 'gauge', 'donut', 'bar', 'bpi', 'markdown',
]

// Rendered only while open (mounted fresh each time) so its working state
// resets without an effect.
function AddWidgetDialog({ onClose, onAdd }: {
  onClose: () => void; onAdd: (w: DashboardWidget) => void
}) {
  const [draft, setDraft] = useState<DashboardWidget>(() => defaultWidget('counters'))
  // Switching type resets type-specific config but keeps any title already typed.
  const changeType = (next: DashboardWidget['type']) =>
    setDraft((d) => ({ ...defaultWidget(next), title: d.title }))

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl max-h-[90vh] overflow-y-auto">
        <DialogHeader><DialogTitle>{t('addWidget')}</DialogTitle></DialogHeader>
        <div className="space-y-3">
          {/* Type gallery: an icon card per widget type so the choice is visual,
              not a text-only dropdown, and add+configure stay one flow (DASH-3). */}
          <div>
            <div className="text-xs text-muted-foreground uppercase tracking-wider mb-1.5">{t('type')}</div>
            <div className="grid grid-cols-3 sm:grid-cols-5 gap-2">
              {WIDGET_TYPES.map((tp) => {
                const active = draft.type === tp
                return (
                  <button
                    key={tp} type="button" onClick={() => changeType(tp)}
                    aria-pressed={active}
                    className={cn(
                      'flex flex-col items-center gap-1.5 rounded-lg border px-2 py-3 text-xs cursor-pointer transition-colors',
                      active
                        ? 'border-primary/60 bg-primary/10 text-foreground'
                        : 'border-border bg-card/40 text-muted-foreground hover:border-primary/30 hover:text-foreground',
                    )}
                  >
                    <WidgetTypeIcon type={tp} size={18} className={active ? 'text-primary' : ''} />
                    <span className="text-center leading-tight">{widgetTypeLabel(tp)}</span>
                  </button>
                )
              })}
            </div>
          </div>
          <Field label={t('titleOptional')}>
            <Input value={draft.title ?? ''} onChange={(e) => setDraft({ ...draft, title: e.target.value })}
              placeholder={widgetTypeLabel(draft.type)} />
          </Field>
          <WidgetConfigFields widget={draft} onChange={(patch) => setDraft((d) => ({ ...d, ...patch }))} />
          <WidgetPreview widget={draft} />
          <div className="flex justify-end gap-2 pt-2">
            <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
            <Button variant="default" onClick={() => onAdd(draft)}>{t('add')}</Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
