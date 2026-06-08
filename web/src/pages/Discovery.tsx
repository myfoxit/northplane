// Network discovery (CMP Wizard parity, SPEC §10.5/§11.3): start a CIDR
// TCP-probe scan, watch it complete, then turn selected host hits into
// monitored hosts via POST /objects:batch. The backend scan contract is
// {cidr, ports?} → {id, cidr, ports, status, startedAt, doneAt?, found[]}.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Check, X } from 'lucide-react'
import { get, post, fmtTime, fmtAgo, type ListResponse } from '../api'
import { Button, Card, Empty, Spinner, Table, Badge, ErrorState } from '../components/ui'
import { Input } from '../components/ui'
import { Field, ListEditor, FormError, useSave } from '../components/forms'
import { t } from '../i18n'

// Actual scan shape from internal/api/discovery.go.
interface DiscoveryHit {
  address: string
  hostname?: string
  openPorts: number[] | null
  suggest: string[] | null
}
interface Scan {
  id: string
  cidr: string
  ports: number[] | null
  status: 'running' | 'done' | 'failed'
  startedAt: string
  doneAt?: string
  found?: DiscoveryHit[] | null
  error?: string
}

interface BatchResult {
  created: number
  failed: number
  results: { name: string; id?: string; error?: string }[]
}

function statusBadge(s: Scan['status']) {
  if (s === 'running') return <Badge className="bg-sky-500/15 text-sky-400 border-sky-500/30">läuft…</Badge>
  if (s === 'failed') return <Badge className="bg-red-500/15 text-red-400 border-red-500/30">fehlgeschlagen</Badge>
  return <Badge className="bg-emerald-500/15 text-emerald-400 border-emerald-500/30">fertig</Badge>
}

export function DiscoveryPage() {
  const [cidr, setCidr] = useState('')
  const [portsStr, setPortsStr] = useState('')
  const [openScan, setOpenScan] = useState<string | null>(null)

  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ['discovery-scans'],
    queryFn: () => get<ListResponse<Scan>>('/discovery/scans'),
    // Poll fast while any scan is still running.
    refetchInterval: (q) => {
      const items = (q.state.data as ListResponse<Scan> | undefined)?.items ?? []
      return items.some((s) => s.status === 'running') ? 5000 : false
    },
  })

  const start = useSave(
    (body: { cidr: string; ports?: number[] }) => post<Scan>('/discovery/scans', body),
    {
      invalidate: [['discovery-scans']],
      onDone: () => { setCidr(''); setPortsStr('') },
    },
  )

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!cidr.trim()) return
    const ports = portsStr.split(',').map((p) => parseInt(p.trim(), 10)).filter((n) => Number.isFinite(n))
    start.mutate({ cidr: cidr.trim(), ports: ports.length ? ports : undefined })
  }

  const scans = data?.items ?? []
  if (isError && !data) {
    return <div className="p-8"><ErrorState error={error} onRetry={() => refetch()} /></div>
  }
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-bold">{t('discovery')}</h1>

      <Card title={t('startScan')}>
        <form className="space-y-3" onSubmit={submit}>
          <div className="grid sm:grid-cols-2 gap-3">
            <Field label={t('scanRange')} required hint="max. /20">
              <Input value={cidr} onChange={(e) => setCidr(e.target.value)} placeholder="192.168.1.0/24" />
            </Field>
            <Field label="Ports (optional)" hint="Standard: 22,80,443,3389,5432,3306,8080">
              <Input value={portsStr} onChange={(e) => setPortsStr(e.target.value)} placeholder="22,80,443" />
            </Field>
          </div>
          <FormError error={start.error} />
          <div className="flex justify-end">
            <Button variant="primary" type="submit" disabled={start.isPending || !cidr.trim()}>
              {start.isPending ? '…' : t('startScan')}
            </Button>
          </div>
        </form>
      </Card>

      <Card title="Scans">
        {isLoading && <Spinner />}
        {!isLoading && scans.length === 0 && <Empty text={t('empty')} />}
        {scans.length > 0 && (
          <Table head={[t('status'), 'CIDR', 'Gestartet', t('suggestions'), '']}>
            {scans.map((s) => (
              <tr key={s.id} className="hover:bg-card/40">
                <td className="px-3 py-2">{statusBadge(s.status)}</td>
                <td className="px-3 py-2 font-mono text-xs text-foreground/90">{s.cidr}</td>
                <td className="px-3 py-2 text-muted-foreground text-xs" title={fmtTime(s.startedAt)}>
                  {fmtAgo(s.startedAt)}
                </td>
                <td className="px-3 py-2 text-muted-foreground tabular-nums">{s.found?.length ?? 0}</td>
                <td className="px-3 py-2">
                  <Button size="sm" disabled={(s.found?.length ?? 0) === 0}
                    onClick={() => setOpenScan(openScan === s.id ? null : s.id)}>
                    {openScan === s.id ? 'schließen' : 'Vorschläge'}
                  </Button>
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Card>

      {openScan && (() => {
        const scan = scans.find((s) => s.id === openScan)
        return scan ? <SuggestionPanel scan={scan} /> : null
      })()}
    </div>
  )
}

// ——— suggestions → batch-create hosts ———

function SuggestionPanel({ scan }: { scan: Scan }) {
  const hits = scan.found ?? []
  const [picked, setPicked] = useState<Set<string>>(() => new Set(hits.map((h) => h.address)))
  const [folder, setFolder] = useState('/discovered')
  const [templates, setTemplates] = useState<string[]>([])
  const [result, setResult] = useState<BatchResult | null>(null)

  const accept = useSave(
    (body: unknown) => post<BatchResult>('/objects:batch', body),
    {
      invalidate: [['objects'], ['overview']],
      onDone: () => { /* result is set in the mutate onSuccess below */ },
    },
  )

  const toggle = (addr: string) =>
    setPicked((cur) => {
      const next = new Set(cur)
      if (next.has(addr)) next.delete(addr); else next.add(addr)
      return next
    })

  const submit = () => {
    const chosen = hits.filter((h) => picked.has(h.address))
    if (chosen.length === 0) return
    const hosts = chosen.map((h) => ({
      name: (h.hostname || h.address).replace(/\.$/, ''),
      folder: folder || '/',
      labels: { discovered: 'true' },
      spec: {
        address: h.address,
        templates: templates.length ? templates : undefined,
        checkCommand: 'builtin:icmp',
      },
    }))
    setResult(null)
    accept.mutate(
      { mode: 'partial', hosts },
      { onSuccess: (r) => setResult(r as BatchResult) },
    )
  }

  // Map address → created/failed result for the per-row column.
  const resultByName: Record<string, { id?: string; error?: string }> = {}
  for (const r of result?.results ?? []) resultByName[r.name] = r

  return (
    <Card title={`${t('suggestions')} — ${scan.cidr}`}>
      <div className="grid sm:grid-cols-2 gap-3 mb-3">
        <Field label={t('folder')} hint="Zielordner der neuen Hosts">
          <Input value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="/discovered" />
        </Field>
        <Field label="Templates (optional)" hint="auf alle Hosts anwenden">
          <ListEditor value={templates} onChange={setTemplates} placeholder="linux-server" />
        </Field>
      </div>

      <Table head={['', 'Adresse', 'Hostname', 'Offene Ports', 'Vorgeschlagene Checks', 'Ergebnis']}>
        {hits.map((h) => {
          const name = (h.hostname || h.address).replace(/\.$/, '')
          const res = resultByName[name]
          return (
            <tr key={h.address} className="hover:bg-card/40">
              <td className="px-3 py-2">
                <input type="checkbox" checked={picked.has(h.address)} onChange={() => toggle(h.address)} />
              </td>
              <td className="px-3 py-2 font-mono text-xs text-foreground/90">{h.address}</td>
              <td className="px-3 py-2 text-muted-foreground text-xs">{h.hostname?.replace(/\.$/, '') || '—'}</td>
              <td className="px-3 py-2">
                <div className="flex flex-wrap gap-1">
                  {(h.openPorts ?? []).map((p) => (
                    <span key={p} className="text-[11px] bg-muted text-foreground/90 rounded px-1.5 py-0.5 font-mono">{p}</span>
                  ))}
                </div>
              </td>
              <td className="px-3 py-2 text-muted-foreground text-xs">
                {(h.suggest ?? []).join(', ') || '—'}
              </td>
              <td className="px-3 py-2 text-xs">
                {res?.id && <span className="text-emerald-400 inline-flex items-center gap-1"><Check size={13} /> angelegt</span>}
                {res?.error && <span className="text-red-400 inline-flex items-center gap-1" title={res.error}><X size={13} /> {res.error}</span>}
                {!res && <span className="text-muted-foreground/70">—</span>}
              </td>
            </tr>
          )
        })}
      </Table>

      <FormError error={accept.error} />
      {result && (
        <div className="text-sm text-muted-foreground mt-2">
          {result.created} angelegt · {result.failed} fehlgeschlagen
        </div>
      )}
      <div className="flex items-center justify-between pt-3">
        <span className="text-xs text-muted-foreground">{picked.size} von {hits.length} ausgewählt</span>
        <Button variant="primary" onClick={submit} disabled={accept.isPending || picked.size === 0}>
          {accept.isPending ? '…' : t('acceptSelected')}
        </Button>
      </div>
    </Card>
  )
}
