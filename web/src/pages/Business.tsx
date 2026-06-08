// Business services (CMP BPI parity, SPEC §9.7): live aggregation tree on
// the left, SLA budget + definition detail on the right. Nodes are
// created/edited with rule (worst|best|quorum|weighted), an optional SLA
// target/window, and a leaf binding (object | selector | inner node).
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get, resourceApi } from '../api'
import type { BusinessService } from '../types'
import { Button, Card, Dialog, Empty, Spinner, Tile, ErrorState } from '../components/ui'
import { Input } from '../components/ui'
import { Field, Select, FormError, SubmitRow, useSave, DeleteButton, Toggle } from '../components/forms'
import { ObjectPicker } from '../components/dash/pickers'
import { bsStateMeta, type BSNode } from '../components/dash/widgets'
import { t } from '../i18n'

const bsApi = resourceApi<BusinessService>('business-services')

const BS_RULES = ['worst', 'best', 'quorum', 'weighted'] as const
const RULE_DE: Record<string, string> = {
  worst: 'Schlechtester (worst)', best: 'Bester (best)',
  quorum: 'Quorum (% gesund)', weighted: 'Gewichtet (weighted)',
}
const SLA_WINDOWS = [
  { v: 'month', label: 'Monat (30 T.)' }, { v: 'quarter', label: 'Quartal (90 T.)' },
  { v: 'year', label: 'Jahr (365 T.)' },
]

// SLA response shape (GET /business-services/{name}/sla).
interface SLAResponse {
  service: string; target: number; windowDays: number; availability: number
  budgetTotal: string; budgetSpent: string; budgetLeft: string
}

export function BusinessPage() {
  const [selected, setSelected] = useState<string | null>(null) // service name
  const [editing, setEditing] = useState<BusinessService | null>(null)
  const [creating, setCreating] = useState(false)

  const { data: tree, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['business-tree'],
    queryFn: () => get<BSNode[]>('/business-services:tree'),
    refetchInterval: 30_000,
  })
  // Flat list backs the parent picker + detail lookup.
  const { data: flat } = useQuery({ queryKey: bsApi.queryKey, queryFn: bsApi.list })

  const remove = useSave((name: string) => bsApi.remove(name), {
    invalidate: [bsApi.queryKey as unknown as string[], ['business-tree']],
    onDone: () => setSelected(null),
  })

  const selectedDef = flat?.find((b) => b.name === selected) ?? null
  const roots = tree ?? []

  if (isError && !tree) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-bold">{t('business')}</h1>
        <Button variant="primary" onClick={() => setCreating(true)}>{t('create')}</Button>
      </div>

      <div className="grid lg:grid-cols-2 gap-4">
        <Card title={t('tree')}>
          {isLoading && <Spinner />}
          {!isLoading && roots.length === 0 && <Empty text={t('empty')} />}
          {roots.length > 0 && (
            <div className="-mx-1">
              <BSTree nodes={roots} selected={selected} onSelect={setSelected} />
            </div>
          )}
        </Card>

        <div className="space-y-4">
          {selected && selectedDef ? (
            <BSDetail
              def={selectedDef}
              onEdit={() => setEditing(selectedDef)}
              onDelete={() => remove.mutate(selectedDef.name)}
            />
          ) : (
            <Card title="Detail"><Empty text="Knoten im Baum wählen." /></Card>
          )}
        </div>
      </div>

      {(creating || editing) && (
        <BSDialog
          existing={editing}
          all={flat ?? []}
          onClose={() => { setCreating(false); setEditing(null) }}
        />
      )}
    </div>
  )
}

// ——— recursive live tree ———

function BSTree({ nodes, selected, onSelect, depth = 0 }: {
  nodes: BSNode[]; selected: string | null; onSelect: (n: string) => void; depth?: number
}) {
  return (
    <>
      {nodes.map((n) => {
        const m = bsStateMeta(n.state)
        const active = selected === n.service.name
        return (
          <div key={n.service.id}>
            <button
              onClick={() => onSelect(n.service.name)}
              className={`w-full text-left flex items-center gap-2 py-1.5 px-2 rounded text-sm cursor-pointer transition-colors ${
                active ? 'bg-primary/10 text-primary' : 'hover:bg-muted/50 text-foreground'}`}
              style={{ paddingLeft: 8 + depth * 16 }}
            >
              <span className={`${m.color} font-bold w-4 text-center shrink-0`}>{m.icon}</span>
              <span className="truncate">{n.service.name}</span>
              {typeof n.service.slaTarget === 'number' && n.service.slaTarget > 0 && (
                <span className="text-muted-foreground/70 text-xs ml-auto shrink-0">SLA {n.service.slaTarget}%</span>
              )}
            </button>
            {n.children && n.children.length > 0 && (
              <BSTree nodes={n.children} selected={selected} onSelect={onSelect} depth={depth + 1} />
            )}
          </div>
        )
      })}
    </>
  )
}

// ——— detail: SLA card + definition card ———

function BSDetail({ def, onEdit, onDelete }: {
  def: BusinessService; onEdit: () => void; onDelete: () => void
}) {
  const { data: sla, isLoading } = useQuery({
    queryKey: ['business-sla', def.name],
    queryFn: () => get<SLAResponse>(`/business-services/${encodeURIComponent(def.name)}/sla`),
    refetchInterval: 60_000,
  })
  const binding = def.objectId ? `Objekt: ${def.objectId}`
    : def.selector ? `Selector: ${def.selector}` : 'innerer Knoten'
  return (
    <>
      <Card
        title={`${t('sla')} — ${def.name}`}
        actions={
          <div className="flex gap-1">
            <Button size="sm" onClick={onEdit}>{t('edit')}</Button>
            <DeleteButton onDelete={onDelete} />
          </div>
        }
      >
        {isLoading && <Spinner />}
        {sla && (
          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-3">
              <Tile label="Verfügbarkeit" value={`${sla.availability.toFixed(3)}%`}
                tone={sla.availability >= sla.target ? 'ok' : 'crit'} />
              <Tile label="Ziel" value={`${sla.target}%`} />
            </div>
            <div className="grid grid-cols-3 gap-3">
              <Tile label="Budget" value={sla.budgetTotal} />
              <Tile label="Verbraucht" value={sla.budgetSpent} tone={sla.budgetSpent !== '0s' ? 'warn' : 'default'} />
              <Tile label="Verbleibend" value={sla.budgetLeft} tone={sla.budgetLeft === '0s' ? 'crit' : 'ok'} />
            </div>
            <div className="text-xs text-muted-foreground">Fenster: {sla.windowDays} Tage</div>
          </div>
        )}
        {!isLoading && !sla && <Empty text="keine SLA-Daten" />}
      </Card>

      <Card title="Definition">
        <dl className="text-sm space-y-1.5">
          <Row k={t('name')} v={def.name} />
          <Row k="Regel" v={RULE_DE[def.rule ?? 'worst'] ?? def.rule ?? 'worst'} />
          {def.rule === 'quorum' && <Row k="Quorum" v={`${def.quorumPct ?? 50}%`} />}
          <Row k="Bindung" v={binding} mono={!!def.selector || !!def.objectId} />
          {def.parentId && <Row k="Parent" v={def.parentId} mono />}
          {typeof def.weight === 'number' && def.weight > 0 && <Row k="Gewicht" v={String(def.weight)} />}
          {typeof def.slaTarget === 'number' && def.slaTarget > 0 && (
            <Row k="SLA-Ziel" v={`${def.slaTarget}% / ${def.slaWindow ?? 'month'}`} />
          )}
          {def.excludeDowntimes && <Row k="Downtimes" v="ausgeschlossen" />}
        </dl>
      </Card>
    </>
  )
}

function Row({ k, v, mono }: { k: string; v?: string; mono?: boolean }) {
  return (
    <div className="flex gap-2">
      <dt className="text-muted-foreground w-28 shrink-0">{k}</dt>
      <dd className={`text-foreground/90 min-w-0 break-words ${mono ? 'font-mono text-xs pt-0.5' : ''}`}>{v || '—'}</dd>
    </div>
  )
}

// ——— create/edit dialog ———

type Binding = 'object' | 'selector' | 'none'

function BSDialog({ existing, all, onClose }: {
  existing: BusinessService | null; all: BusinessService[]; onClose: () => void
}) {
  const editing = !!existing
  const initialBinding: Binding = existing?.objectId ? 'object'
    : existing?.selector ? 'selector' : 'none'

  const [name, setName] = useState(existing?.name ?? '')
  const [parentId, setParentId] = useState(existing?.parentId ?? '')
  const [rule, setRule] = useState<string>(existing?.rule ?? 'worst')
  const [quorumPct, setQuorumPct] = useState<number>(existing?.quorumPct ?? 50)
  const [binding, setBinding] = useState<Binding>(initialBinding)
  const [objectId, setObjectId] = useState(existing?.objectId ?? '')
  const [selector, setSelector] = useState(existing?.selector ?? '')
  const [weight, setWeight] = useState<number>(existing?.weight ?? 0)
  const [slaTarget, setSlaTarget] = useState<string>(existing?.slaTarget != null ? String(existing.slaTarget) : '')
  const [slaWindow, setSlaWindow] = useState<string>(existing?.slaWindow ?? 'month')
  const [excludeDowntimes, setExcludeDowntimes] = useState<boolean>(!!existing?.excludeDowntimes)

  const save = useSave(
    async (doc: BusinessService) => {
      if (editing) {
        const fresh = await bsApi.get(doc.name)
        return bsApi.update(doc.name, { ...fresh.data, ...doc }, fresh.etag)
      }
      return bsApi.create(doc)
    },
    { invalidate: [bsApi.queryKey as unknown as string[], ['business-tree']], onDone: onClose },
  )

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    const doc: BusinessService = {
      name: name.trim(),
      parentId: parentId || undefined,
      rule: (rule as BusinessService['rule']) || undefined,
      quorumPct: rule === 'quorum' ? quorumPct : undefined,
      objectId: binding === 'object' ? (objectId || undefined) : undefined,
      selector: binding === 'selector' ? (selector || undefined) : undefined,
      weight: weight > 0 ? weight : undefined,
      slaTarget: slaTarget.trim() ? Number(slaTarget) : undefined,
      slaWindow: slaTarget.trim() ? (slaWindow as BusinessService['slaWindow']) : undefined,
      excludeDowntimes: excludeDowntimes || undefined,
    }
    save.mutate(doc)
  }

  // parent options exclude self to avoid trivial cycles.
  const parentOptions = all.filter((b) => b.name !== existing?.name)

  return (
    <Dialog open onClose={onClose} title={editing ? `${t('edit')}: ${existing!.name}` : t('create')} size="lg">
      <form className="space-y-3" onSubmit={submit}>
        <div className="grid grid-cols-2 gap-3">
          <Field label={t('name')} required>
            <Input value={name} onChange={(e) => setName(e.target.value)} disabled={editing} autoFocus={!editing} />
          </Field>
          <Field label="Parent (übergeordnet)">
            <Select value={parentId} onChange={(e) => setParentId(e.target.value)}>
              <option value="">— Wurzel —</option>
              {parentOptions.map((b) => <option key={b.id ?? b.name} value={b.id}>{b.name}</option>)}
            </Select>
          </Field>
        </div>

        <div className="grid grid-cols-2 gap-3">
          <Field label="Aggregationsregel">
            <Select value={rule} onChange={(e) => setRule(e.target.value)}>
              {BS_RULES.map((r) => <option key={r} value={r}>{RULE_DE[r]}</option>)}
            </Select>
          </Field>
          {rule === 'quorum' && (
            <Field label="Quorum % gesund">
              <Input type="number" min={1} max={100} value={quorumPct}
                onChange={(e) => setQuorumPct(Number(e.target.value))} />
            </Field>
          )}
        </div>

        <div className="border-t border-border pt-3">
          <div className="text-xs text-muted-foreground font-medium mb-2">Leaf-Bindung</div>
          <div className="flex gap-4 text-sm text-foreground/90 mb-2">
            {(['object', 'selector', 'none'] as Binding[]).map((b) => (
              <label key={b} className="flex items-center gap-1.5 cursor-pointer">
                <input type="radio" name="binding" checked={binding === b} onChange={() => setBinding(b)} />
                {b === 'object' ? 'Objekt' : b === 'selector' ? 'Selector' : 'keine (innerer Knoten)'}
              </label>
            ))}
          </div>
          {binding === 'object' && (
            <ObjectPicker value={objectId} onChange={(id) => setObjectId(id)} />
          )}
          {binding === 'selector' && (
            <Input value={selector} onChange={(e) => setSelector(e.target.value)} placeholder="app=web,env=prod" />
          )}
        </div>

        <div className="grid grid-cols-3 gap-3 border-t border-border pt-3">
          <Field label="Gewicht" hint="0 = unbenutzt">
            <Input type="number" min={0} step="0.1" value={weight}
              onChange={(e) => setWeight(Number(e.target.value))} />
          </Field>
          <Field label="SLA-Ziel %" hint="leer = kein SLA">
            <Input type="number" step="0.001" value={slaTarget}
              onChange={(e) => setSlaTarget(e.target.value)} placeholder="99.9" />
          </Field>
          <Field label="SLA-Fenster">
            <Select value={slaWindow} onChange={(e) => setSlaWindow(e.target.value)} disabled={!slaTarget.trim()}>
              {SLA_WINDOWS.map((w) => <option key={w.v} value={w.v}>{w.label}</option>)}
            </Select>
          </Field>
        </div>
        <Field label="Geplante Downtimes ausschließen">
          <Toggle checked={excludeDowntimes} onChange={setExcludeDowntimes} label={excludeDowntimes ? 'ja' : 'nein'} />
        </Field>

        <FormError error={save.error} />
        <SubmitRow onCancel={onClose} saving={save.isPending} label={editing ? t('save') : t('create')} disabled={!name.trim()} />
      </form>
    </Dialog>
  )
}
