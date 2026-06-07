// Admin (SPEC §12.3): rules with test runner, channels with test send,
// API tokens, audit browser with chain verification, system health,
// AI approval queue.
import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { get, post, del, queryClient, fmtTime, type ListResponse } from '../api'
import type { AIAction, RuleTestResult } from '../types'
import { Button, Card, Input, Empty, Badge, Table } from '../components/ui'
import { t } from '../i18n'

const tabs = ['rules', 'channels', 'tokens', 'audit', 'aiQueue', 'health'] as const
type Tab = typeof tabs[number]

export function AdminPage() {
  const [tab, setTab] = useState<Tab>('rules')
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-bold">{t('admin')}</h1>
      <div className="flex gap-1 border-b border-slate-800">
        {tabs.map((tb) => (
          <button key={tb} onClick={() => setTab(tb)}
            className={`px-3 py-2 text-sm cursor-pointer ${tab === tb
              ? 'text-blue-400 border-b-2 border-blue-400'
              : 'text-slate-400 hover:text-slate-200'}`}>
            {t(tb)}
          </button>
        ))}
      </div>
      {tab === 'rules' && <RulesTab />}
      {tab === 'channels' && <ChannelsTab />}
      {tab === 'tokens' && <TokensTab />}
      {tab === 'audit' && <AuditTab />}
      {tab === 'aiQueue' && <AIQueueTab />}
      {tab === 'health' && <HealthTab />}
    </div>
  )
}

interface Rule { name: string; match?: string; severity?: string; escalationPolicy?: string; disabled?: boolean }

function RulesTab() {
  const { data } = useQuery({
    queryKey: ['rules'],
    queryFn: () => get<ListResponse<Rule>>('/alert-rules'),
  })
  const [testResult, setTestResult] = useState<{ rule: string; result: RuleTestResult } | null>(null)
  const test = useMutation({
    mutationFn: (name: string) =>
      post<RuleTestResult>(`/alert-rules/${encodeURIComponent(name)}:test`, {}),
    onSuccess: (result, name) => setTestResult({ rule: name, result }),
  })
  return (
    <div className="space-y-3">
      {(data?.items?.length ?? 0) === 0 && <Empty text={t('empty')} />}
      {data?.items?.map((rule) => (
        <div key={rule.name} className="bg-slate-900/50 border border-slate-800 rounded-lg px-3 py-2 flex items-center gap-3">
          <div className="flex-1 min-w-0">
            <div className="text-sm font-medium text-slate-200">{rule.name}
              {rule.disabled && <Badge className="ml-2 bg-slate-800 text-slate-500 border-slate-700">disabled</Badge>}
            </div>
            <div className="text-xs text-slate-500 font-mono truncate">{rule.match ?? '(heartbeat)'}</div>
          </div>
          {rule.severity && <Badge className="bg-slate-800 text-slate-400 border-slate-700">{rule.severity}</Badge>}
          {rule.escalationPolicy && <span className="text-xs text-slate-500">→ {rule.escalationPolicy}</span>}
          <Button size="sm" onClick={() => test.mutate(rule.name)} disabled={test.isPending}>
            {t('testRule')}
          </Button>
        </div>
      ))}
      {testResult && (
        <Card title={`Test: ${testResult.rule} — letzte 24h`}>
          <p className="text-sm text-slate-300">
            {testResult.result.matched} Events hätten gematcht,
            {' '}{testResult.result.wouldOpen?.length ?? 0} Alarme wären entstanden.
          </p>
          {testResult.result.wouldOpen?.map((a, i) => (
            <div key={i} className="text-xs text-slate-400 mt-1 font-mono">→ [{a.severity}] {a.title}</div>
          ))}
        </Card>
      )}
    </div>
  )
}

interface Channel { name: string; type: string; enabled: boolean }

function ChannelsTab() {
  const { data } = useQuery({
    queryKey: ['resources', 'channels'],
    queryFn: () => get<ListResponse<Channel>>('/channels'),
  })
  const [result, setResult] = useState('')
  const testSend = useMutation({
    mutationFn: (name: string) =>
      post<{ result: string; detail?: string }>(`/channels/${encodeURIComponent(name)}:test-notification`, {}),
    onSuccess: (r) => setResult(`✓ ${r.result} ${r.detail ?? ''}`),
    onError: (e) => setResult(`✕ ${String(e)}`),
  })
  return (
    <div className="space-y-2">
      {(data?.items?.length ?? 0) === 0 && <Empty text="Keine Kanäle — per API/Bundle anlegen (kind: Channel)." />}
      {data?.items?.map((ch) => (
        <div key={ch.name} className="bg-slate-900/50 border border-slate-800 rounded-lg px-3 py-2 flex items-center gap-3">
          <Badge className="bg-slate-800 text-slate-300 border-slate-700 w-20 justify-center">{ch.type}</Badge>
          <span className="text-sm text-slate-200 flex-1">{ch.name}</span>
          {!ch.enabled && <Badge className="bg-slate-800 text-slate-500 border-slate-700">aus</Badge>}
          <Button size="sm" onClick={() => testSend.mutate(ch.name)} disabled={testSend.isPending}>
            {t('testSend')}
          </Button>
        </div>
      ))}
      {result && <p className="text-xs text-slate-400">{result}</p>}
    </div>
  )
}

interface Token { id: string; name: string; prefix: string; scopes: string[]; aiAgent?: boolean; lastUsedAt?: string }

function TokensTab() {
  const { data } = useQuery({
    queryKey: ['tokens'],
    queryFn: () => get<ListResponse<Token>>('/api-tokens'),
  })
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState('objects:read,alerts:read')
  const [minted, setMinted] = useState('')
  const create = useMutation({
    mutationFn: () => post<{ token: string }>('/api-tokens', {
      name, scopes: scopes.split(',').map((s) => s.trim()).filter(Boolean),
    }),
    onSuccess: (r) => {
      setMinted(r.token)
      setName('')
      queryClient.invalidateQueries({ queryKey: ['tokens'] })
    },
  })
  const revoke = useMutation({
    mutationFn: (id: string) => del(`/api-tokens/${id}`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['tokens'] }),
  })
  return (
    <div className="space-y-4">
      <Card title={t('newToken')}>
        <div className="flex gap-2">
          <Input placeholder="Name" value={name} onChange={(e) => setName(e.target.value)} className="max-w-48" />
          <Input placeholder="scopes (kommagetrennt)" value={scopes} onChange={(e) => setScopes(e.target.value)} />
          <Button variant="primary" onClick={() => create.mutate()} disabled={!name || create.isPending}>
            {t('newToken')}
          </Button>
        </div>
        {minted && (
          <div className="mt-3 bg-amber-950/40 border border-amber-800/50 rounded-lg p-3">
            <div className="text-xs text-amber-400 mb-1">Einmalig sichtbar — jetzt sichern:</div>
            <code className="text-sm text-amber-200 break-all select-all">{minted}</code>
          </div>
        )}
      </Card>
      <Table head={['Name', 'Prefix', 'Scopes', 'Zuletzt', '']}>
        {data?.items?.map((tok) => (
          <tr key={tok.id}>
            <td className="px-3 py-2 text-slate-200">{tok.name}{tok.aiAgent ? ' ✦' : ''}</td>
            <td className="px-3 py-2 font-mono text-slate-500">np_{tok.prefix}…</td>
            <td className="px-3 py-2 text-xs text-slate-400">{tok.scopes?.join(', ')}</td>
            <td className="px-3 py-2 text-xs text-slate-500">{tok.lastUsedAt ? fmtTime(tok.lastUsedAt) : '—'}</td>
            <td className="px-3 py-2 text-right">
              <Button size="sm" variant="danger" onClick={() => revoke.mutate(tok.id)}>{t('revoke')}</Button>
            </td>
          </tr>
        ))}
      </Table>
    </div>
  )
}

interface AuditEntry {
  seq: number; ts: string; actorType: string; actorId: string
  action: string; resource?: string
}

function AuditTab() {
  const [action, setAction] = useState('')
  const { data } = useQuery({
    queryKey: ['audit', action],
    queryFn: () => get<ListResponse<AuditEntry>>(`/audit?action=${encodeURIComponent(action)}&limit=100`),
  })
  const [verify, setVerify] = useState('')
  const doVerify = useMutation({
    mutationFn: () => post<{ intact: boolean; verified: number; error?: string }>('/audit:verify'),
    onSuccess: (r) => setVerify(r.intact
      ? `✓ ${t('chainIntact')} (${r.verified})`
      : `✕ ${t('chainBroken')}: ${r.error}`),
  })
  return (
    <div className="space-y-3">
      <div className="flex gap-2 items-center">
        <Input placeholder="Action-Prefix (host. / alert. / ai.)" value={action}
          onChange={(e) => setAction(e.target.value)} className="max-w-xs" />
        <Button onClick={() => doVerify.mutate()}>{t('verifyChain')}</Button>
        {verify && <span className={`text-sm ${verify.startsWith('✓') ? 'text-emerald-400' : 'text-red-400'}`}>{verify}</span>}
        <a href="/api/v1/audit:export" className="text-xs text-slate-500 hover:text-slate-300 ml-auto">⇩ NDJSON (SIEM)</a>
      </div>
      <Table head={['Seq', 'Zeit', 'Akteur', 'Aktion', 'Ressource']}>
        {data?.items?.map((e) => (
          <tr key={e.seq}>
            <td className="px-3 py-1.5 text-slate-600 tabular-nums">{e.seq}</td>
            <td className="px-3 py-1.5 text-slate-500 text-xs tabular-nums">{fmtTime(e.ts)}</td>
            <td className="px-3 py-1.5 text-xs">
              <Badge className={e.actorType === 'ai_agent'
                ? 'bg-purple-500/10 text-purple-400 border-purple-800'
                : 'bg-slate-800 text-slate-400 border-slate-700'}>
                {e.actorType}
              </Badge>
            </td>
            <td className="px-3 py-1.5 text-slate-300 font-mono text-xs">{e.action}</td>
            <td className="px-3 py-1.5 text-slate-500 text-xs font-mono truncate max-w-48">{e.resource}</td>
          </tr>
        ))}
      </Table>
    </div>
  )
}

function AIQueueTab() {
  const { data } = useQuery({
    queryKey: ['ai-actions'],
    queryFn: () => get<ListResponse<AIAction>>('/ai/actions'),
    refetchInterval: 15_000,
  })
  const decide = useMutation({
    mutationFn: ({ id, verb }: { id: string; verb: 'approve' | 'deny' }) =>
      post(`/ai/actions/${id}:${verb}`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['ai-actions'] }),
  })
  const rows = data?.items ?? []
  return (
    <div className="space-y-2">
      {rows.length === 0 && <Empty text="Keine AI-Aktionen." />}
      {rows.map((a) => (
        <div key={a.id} className="bg-slate-900/50 border border-slate-800 rounded-lg px-3 py-2 flex items-center gap-3">
          <Badge className={
            a.status === 'proposed' ? 'bg-amber-500/10 text-amber-400 border-amber-800'
              : a.status === 'executed' ? 'bg-emerald-500/10 text-emerald-400 border-emerald-800'
                : 'bg-slate-800 text-slate-400 border-slate-700'}>
            {a.status}
          </Badge>
          <div className="flex-1 min-w-0">
            <div className="text-sm text-slate-200 font-mono">{a.tool}</div>
            <div className="text-xs text-slate-500 font-mono truncate">{JSON.stringify(a.args)}</div>
          </div>
          <span className="text-xs text-slate-600">{a.actor} · {fmtTime(a.createdAt)}</span>
          {a.status === 'proposed' && (
            <div className="flex gap-1">
              <Button size="sm" variant="primary" onClick={() => decide.mutate({ id: a.id, verb: 'approve' })}>
                {t('approve')}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => decide.mutate({ id: a.id, verb: 'deny' })}>
                {t('deny')}
              </Button>
            </div>
          )}
        </div>
      ))}
    </div>
  )
}

function HealthTab() {
  const { data: health } = useQuery({
    queryKey: ['health'],
    queryFn: () => get<Record<string, unknown>>('/system/health'),
    refetchInterval: 10_000,
  })
  const { data: info } = useQuery({
    queryKey: ['info'],
    queryFn: () => get<Record<string, unknown>>('/system/info'),
  })
  return (
    <div className="grid lg:grid-cols-2 gap-4">
      <Card title="system/info">
        <pre className="text-xs text-slate-400 font-mono">{JSON.stringify(info, null, 2)}</pre>
      </Card>
      <Card title="system/health" actions={
        <a href="/metrics" className="text-xs text-slate-500 hover:text-slate-300">OpenMetrics ↗</a>}>
        <pre className="text-xs text-slate-400 font-mono overflow-auto max-h-96">{JSON.stringify(health, null, 2)}</pre>
      </Card>
    </div>
  )
}
