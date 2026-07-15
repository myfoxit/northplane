// Administration (SPEC §12.3): full system administration — users, roles,
// contacts, contact groups, notification channels, event sources, tenants
// and secrets — alongside the existing API tokens, audit browser (with
// chain verification), AI approval queue and system health. Each management
// tab is a Table list with status badges + an "Anlegen" Dialog form and
// per-row Bearbeiten/Löschen; ETag-versioned resources load via
// resourceApi.get and PUT with that etag (409/412 → FormError).
import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { Sparkles } from 'lucide-react'
import { get, post, del, queryClient, fmtTime, type ListResponse } from '../api'
import type { AIAction } from '../types'
import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle, CardAction, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Empty } from '@/components/kit'
import { t } from '../i18n'
import { UsersTab } from '../components/admin/Users'
import { RolesTab } from '../components/admin/Roles'
import { ContactsTab } from '../components/admin/Contacts'
import { GroupsTab } from '../components/admin/Groups'
import { ChannelsTab } from '../components/admin/Channels'
import { SourcesTab } from '../components/admin/Sources'
import { WebhooksTab, HeartbeatsTab } from '../components/admin/Integrations'
import { TenantsTab } from '../components/admin/Tenants'
import { SitesTab } from '../components/admin/Sites'
import { SecretsTab } from '../components/admin/Secrets'
import { MCPTab } from '../components/admin/MCP'
import { AgentsTab } from '../components/admin/Agents'
import { DeadLettersTab } from '../components/admin/DeadLetters'
import { BundlesTab } from '../components/admin/Bundles'

const tabs = [
  'users', 'roles', 'contacts', 'contactGroups', 'channels', 'eventSources',
  'webhooks', 'heartbeats', 'tenants', 'sites', 'secrets', 'tokens', 'mcp', 'agents',
  'deadLetters', 'bundles', 'audit', 'aiQueue', 'health',
] as const
type Tab = typeof tabs[number]

export function AdminPage() {
  const [tab, setTab] = useState<Tab>('users')
  return (
    <div className="space-y-4">
      <h1 className="text-lg font-bold">{t('admin')}</h1>
      <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
        {/* 19 tabs overflow one row — scroll horizontally instead of clipping
            the last ones (NAV-1). w-max lets the list size to its content so
            the wrapper is what scrolls; the thin scrollbar signals more tabs. */}
        <div className="overflow-x-auto pb-1 [scrollbar-width:thin]">
          <TabsList className="w-max">
            {tabs.map((tb) => <TabsTrigger key={tb} value={tb}>{t(tb)}</TabsTrigger>)}
          </TabsList>
        </div>
      </Tabs>
      {tab === 'users' && <UsersTab />}
      {tab === 'roles' && <RolesTab />}
      {tab === 'contacts' && <ContactsTab />}
      {tab === 'contactGroups' && <GroupsTab />}
      {tab === 'channels' && <ChannelsTab />}
      {tab === 'eventSources' && <SourcesTab />}
      {tab === 'webhooks' && <WebhooksTab />}
      {tab === 'heartbeats' && <HeartbeatsTab />}
      {tab === 'tenants' && <TenantsTab />}
      {tab === 'sites' && <SitesTab />}
      {tab === 'secrets' && <SecretsTab />}
      {tab === 'tokens' && <TokensTab />}
      {tab === 'mcp' && <MCPTab />}
      {tab === 'agents' && <AgentsTab />}
      {tab === 'deadLetters' && <DeadLettersTab />}
      {tab === 'bundles' && <BundlesTab />}
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
      <Card>
        <CardHeader><CardTitle>{t('newToken')}</CardTitle></CardHeader>
        <CardContent>
          <div className="flex gap-2">
            <Input placeholder="Name" value={name} onChange={(e) => setName(e.target.value)} className="max-w-48" />
            <Input placeholder={t('scopesCommaSep')} value={scopes} onChange={(e) => setScopes(e.target.value)} />
            <Button variant="default" onClick={() => create.mutate()} disabled={!name || create.isPending}>
              {t('newToken')}
            </Button>
          </div>
          {minted && (
            <div className="mt-3 bg-amber-950/40 border border-amber-800/50 rounded-lg p-3">
              <div className="text-xs text-amber-400 mb-1">{t('tokenOnceVisibleSave')}</div>
              <code className="text-sm text-amber-200 break-all select-all">{minted}</code>
            </div>
          )}
        </CardContent>
      </Card>
      <Table>
        <TableHeader>
          <TableRow>
            {[t('name'), 'Prefix', 'Scopes', t('lastUsed'), ''].map((h, i) => <TableHead key={i}>{h}</TableHead>)}
          </TableRow>
        </TableHeader>
        <TableBody>
          {data?.items?.map((tok) => (
            <TableRow key={tok.id}>
              <TableCell className="text-foreground">
                <span className="inline-flex items-center gap-1">{tok.name}{tok.aiAgent && <Sparkles size={14} />}</span>
              </TableCell>
              <TableCell className="font-mono text-muted-foreground">np_{tok.prefix}…</TableCell>
              <TableCell className="text-xs text-muted-foreground">{tok.scopes?.join(', ')}</TableCell>
              <TableCell className="text-xs text-muted-foreground">{tok.lastUsedAt ? fmtTime(tok.lastUsedAt) : '—'}</TableCell>
              <TableCell className="text-right">
                <Button size="sm" variant="destructive" onClick={() => revoke.mutate(tok.id)}>{t('revoke')}</Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
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
        <a href="/api/v1/audit:export" className="text-xs text-muted-foreground hover:text-foreground/90 ml-auto">⇩ NDJSON (SIEM)</a>
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            {['Seq', t('time'), t('actor'), t('action'), t('resource')].map((h) => <TableHead key={h}>{h}</TableHead>)}
          </TableRow>
        </TableHeader>
        <TableBody>
          {data?.items?.map((e) => (
            <TableRow key={e.seq}>
              <TableCell className="text-muted-foreground/70 tabular-nums">{e.seq}</TableCell>
              <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(e.ts)}</TableCell>
              <TableCell className="text-xs">
                <Badge variant="outline" className={e.actorType === 'ai_agent'
                  ? 'bg-purple-500/10 text-purple-400 border-purple-800'
                  : 'bg-muted text-muted-foreground border-input'}>
                  {e.actorType}
                </Badge>
              </TableCell>
              <TableCell className="text-foreground/90 font-mono text-xs">{e.action}</TableCell>
              <TableCell className="text-muted-foreground text-xs font-mono truncate max-w-48">{e.resource}</TableCell>
            </TableRow>
          ))}
        </TableBody>
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
      {rows.length === 0 && <Empty text={t('noAiActions')} />}
      {rows.map((a) => (
        <div key={a.id} className="bg-card/50 border border-border rounded-lg px-3 py-2 flex items-center gap-3">
          <Badge variant="outline" className={
            a.status === 'proposed' ? 'bg-amber-500/10 text-amber-400 border-amber-800'
              : a.status === 'executed' ? 'bg-emerald-500/10 text-emerald-400 border-emerald-800'
                : 'bg-muted text-muted-foreground border-input'}>
            {a.status}
          </Badge>
          <div className="flex-1 min-w-0">
            <div className="text-sm text-foreground font-mono">{a.tool}</div>
            <div className="text-xs text-muted-foreground font-mono truncate">{JSON.stringify(a.args)}</div>
          </div>
          <span className="text-xs text-muted-foreground/70">{a.actor} · {fmtTime(a.createdAt)}</span>
          {a.status === 'proposed' && (
            <div className="flex gap-1">
              <Button size="sm" variant="default" onClick={() => decide.mutate({ id: a.id, verb: 'approve' })}>
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
      <Card>
        <CardHeader><CardTitle>system/info</CardTitle></CardHeader>
        <CardContent>
          <pre className="text-xs text-muted-foreground font-mono">{JSON.stringify(info, null, 2)}</pre>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>system/health</CardTitle>
          <CardAction>
            <a href="/metrics" className="text-xs text-muted-foreground hover:text-foreground/90">OpenMetrics ↗</a>
          </CardAction>
        </CardHeader>
        <CardContent>
          <pre className="text-xs text-muted-foreground font-mono overflow-auto max-h-96">{JSON.stringify(health, null, 2)}</pre>
        </CardContent>
      </Card>
    </div>
  )
}
