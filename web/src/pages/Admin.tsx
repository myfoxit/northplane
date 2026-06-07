// Administration (SPEC §12.3): full system administration — users, roles,
// contacts, contact groups, notification channels, event sources, tenants
// and secrets — alongside the existing API tokens, audit browser (with
// chain verification), AI approval queue and system health. Each management
// tab is a Table list with status badges + an "Anlegen" Dialog form and
// per-row Bearbeiten/Löschen; ETag-versioned resources load via
// resourceApi.get and PUT with that etag (409/412 → FormError).
import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { get, post, del, queryClient, fmtTime, type ListResponse } from '../api'
import type { AIAction } from '../types'
import { Button, Card, Input, Empty, Badge, Table, TabBar } from '../components/ui'
import { t } from '../i18n'
import { UsersTab } from '../components/admin/Users'
import { RolesTab } from '../components/admin/Roles'
import { ContactsTab } from '../components/admin/Contacts'
import { GroupsTab } from '../components/admin/Groups'
import { ChannelsTab } from '../components/admin/Channels'
import { SourcesTab } from '../components/admin/Sources'
import { WebhooksTab, HeartbeatsTab } from '../components/admin/Integrations'
import { TenantsTab } from '../components/admin/Tenants'
import { SecretsTab } from '../components/admin/Secrets'

const tabs = [
  'users', 'roles', 'contacts', 'contactGroups', 'channels', 'eventSources',
  'webhooks', 'heartbeats', 'tenants', 'secrets', 'tokens', 'audit', 'aiQueue', 'health',
] as const
type Tab = typeof tabs[number]

export function AdminPage() {
  const [tab, setTab] = useState<Tab>('users')
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-bold">{t('admin')}</h1>
      <TabBar tabs={tabs} value={tab} onChange={setTab} labels={(tb) => t(tb)} />
      {tab === 'users' && <UsersTab />}
      {tab === 'roles' && <RolesTab />}
      {tab === 'contacts' && <ContactsTab />}
      {tab === 'contactGroups' && <GroupsTab />}
      {tab === 'channels' && <ChannelsTab />}
      {tab === 'eventSources' && <SourcesTab />}
      {tab === 'webhooks' && <WebhooksTab />}
      {tab === 'heartbeats' && <HeartbeatsTab />}
      {tab === 'tenants' && <TenantsTab />}
      {tab === 'secrets' && <SecretsTab />}
      {tab === 'tokens' && <TokensTab />}
      {tab === 'audit' && <AuditTab />}
      {tab === 'aiQueue' && <AIQueueTab />}
      {tab === 'health' && <HealthTab />}
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
