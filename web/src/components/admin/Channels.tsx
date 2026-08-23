// Notification channels: full ETag-versioned management via
// resourceApi<Channel>('channels'). Each channel type renders a tailored
// config form (the key→Field mapping below); secret-bearing fields carry
// the $SECRET:name$ hint. Anything the typed form does not cover is always
// reachable through a "Weitere Einstellungen" KVEditor fallback so no
// config key is ever unreachable. Each row can fire a test notification
// (POST /channels/{name}:test-notification {target?}).
import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { resourceApi, post } from '../../api'
import type { Channel, ChannelType } from '../../types'
import { Button } from '@/components/ui/button'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { Empty, Spinner, Field, FormError, SubmitRow, useSave, DeleteButton, KVEditor, DuplicateButton } from '@/components/kit'
import { duplicateDoc } from '@/lib/duplicate'
import { t } from '../../i18n'
import { TypeBadge, StatusBadge, TableActions, RowActions } from './common'

const channelsApi = resourceApi<Channel>('channels')

const CHANNEL_TYPES: ChannelType[] = [
  'email', 'webhook', 'slack', 'teams', 'ntfy', 'sms', 'push', 'voice',
  'mqtt', 'servicenow', 'zendesk', 'jira', 'ticket',
]

// Field spec per config key. secret → render the $SECRET hint.
type FieldSpec = { key: string; label: string; secret?: boolean; hint?: string; type?: 'number' }
// Per-channel-type key sets (SPEC §12.3). Web push needs no config (VAPID is
// server-side); the push keys below only feed the mobile app (FCM/APNs).
const CONFIG_FIELDS: Record<string, FieldSpec[]> = {
  // email is provider-driven (smtp | sendmail | resend | ses) — the
  // concrete keys come from emailFields() based on config.provider.
  email: [],
  webhook: [
    { key: 'url', label: 'URL' },
    { key: 'secret', label: 'HMAC-Secret', secret: true },
    { key: 'method', label: t('httpMethod'), hint: t('postDefault') },
  ],
  slack: [{ key: 'url', label: t('webhookUrl'), secret: true }],
  teams: [{ key: 'url', label: t('webhookUrl'), secret: true }],
  ntfy: [
    { key: 'url', label: t('serverUrl'), hint: t('ntfyUrlHint') },
    { key: 'topic', label: 'Topic' },
    { key: 'token', label: t('accessToken'), secret: true },
  ],
  // sms is provider-driven; we surface the common twilio + generic-http keys.
  sms: [
    { key: 'provider', label: 'Provider', hint: 'twilio | generic-http' },
    { key: 'accountSid', label: 'Account SID (twilio)', secret: true },
    { key: 'authToken', label: t('authTokenTwilio'), secret: true },
    { key: 'apiKeySid', label: 'API-Key-SID (twilio)', hint: t('apiKeyAltHint') },
    { key: 'apiKeySecret', label: 'API-Key-Secret (twilio)', secret: true },
    { key: 'from', label: t('senderNumber') },
    { key: 'url', label: 'URL (generic-http)', secret: true },
  ],
  // push: Web-Push braucht keine Config (VAPID serverseitig); FCM/APNs
  // versorgen die Northplane-Alarm-App (Mobile).
  push: [
    { key: 'fcmServiceAccount', label: 'FCM-Service-Account (JSON)', secret: true },
    { key: 'apnsKey', label: 'APNs-Key (.p8)', secret: true },
    { key: 'apnsKeyId', label: 'APNs-Key-ID' },
    { key: 'apnsTeamId', label: 'APNs-Team-ID' },
    { key: 'apnsTopic', label: 'APNs-Topic', hint: t('apnsTopicHint') },
    { key: 'apnsSandbox', label: 'APNs-Sandbox (true/false)' },
  ],
  // voice: Twilio Voice (TTS + DTMF über /api/v1/voice/gather: 4 = ack,
  // 6 = resolve) oder generic-http Gateways (SPEC §9.6).
  voice: [
    { key: 'provider', label: 'Provider', hint: 'twilio | generic-http' },
    { key: 'accountSid', label: 'Account SID (twilio)', secret: true },
    { key: 'authToken', label: t('authTokenTwilio'), secret: true },
    { key: 'apiKeySid', label: 'API-Key-SID (twilio)', hint: t('apiKeyAltHint') },
    { key: 'apiKeySecret', label: 'API-Key-Secret (twilio)', secret: true },
    { key: 'from', label: t('callerNumber') },
    { key: 'language', label: t('ttsLanguage'), hint: t('ttsLanguageHint') },
    { key: 'url', label: 'URL (generic-http)', secret: true },
  ],
  // mqtt publisher: alarm notifications onto a broker topic.
  mqtt: [
    { key: 'url', label: t('brokerUrl'), hint: t('mqttUrlHint') },
    { key: 'topic', label: 'Topic' },
    { key: 'username', label: t('username') },
    { key: 'password', label: t('password'), secret: true },
    { key: 'qos', label: 'QoS', hint: '0 | 1 | 2' },
    { key: 'retain', label: 'Retain (true/false)' },
    { key: 'clientId', label: 'Client-ID' },
    { key: 'tlsInsecure', label: t('tlsInsecure') },
  ],
  // Ticket-Systeme (F-04.05): Ticket bei Eskalation, Auto-Close bei Resolve.
  servicenow: [
    { key: 'url', label: t('instanceUrl'), hint: t('servicenowUrlHint') },
    { key: 'username', label: t('username') },
    { key: 'password', label: t('password'), secret: true },
    { key: 'table', label: t('table'), hint: t('incidentDefault') },
    { key: 'closeState', label: 'Close-State', hint: t('closeStateHint') },
    { key: 'autoClose', label: 'Auto-Close (true/false)' },
  ],
  zendesk: [
    { key: 'url', label: 'Subdomain-URL', hint: 'https://<subdomain>.zendesk.com' },
    { key: 'email', label: t('agentEmail') },
    { key: 'apiToken', label: t('apiToken'), secret: true },
    { key: 'closeStatus', label: 'Close-Status', hint: t('solvedDefault') },
    { key: 'autoClose', label: 'Auto-Close (true/false)' },
  ],
  jira: [
    { key: 'url', label: 'Jira-URL', hint: 'https://<org>.atlassian.net' },
    { key: 'project', label: t('projectKey'), hint: t('egOps') },
    { key: 'issueType', label: t('issueType'), hint: t('taskDefault') },
    { key: 'username', label: t('userEmail') },
    { key: 'password', label: t('apiToken'), secret: true },
    { key: 'closeTransitionId', label: 'Close-Transition-ID', hint: t('closeTransitionHint') },
    { key: 'autoClose', label: 'Auto-Close (true/false)' },
  ],
  ticket: [
    { key: 'url', label: 'Create-URL (POST, JSON)' },
    { key: 'token', label: t('bearerToken'), secret: true },
    { key: 'username', label: t('basicAuthUser') },
    { key: 'password', label: t('basicAuthPassword'), secret: true },
    { key: 'refField', label: t('ticketIdField'), hint: t('ticketIdFieldHint') },
    { key: 'ticketUrlTemplate', label: t('ticketUrlTemplate'), hint: 'https://…/{ref}' },
    { key: 'closeUrl', label: 'Close-URL', hint: t('refPlaceholderHint') },
    { key: 'autoClose', label: 'Auto-Close (true/false)' },
  ],
}

// emailFields builds the e-mail key set for the selected provider
// (backend: internal/notify/email.go). smtp covers jeden Relay (Postfix,
// SES-SMTP, Mailgun …); sendmail nutzt den lokalen MTA; resend/ses sind
// HTTP-APIs.
const EMAIL_PROVIDERS = ['smtp', 'sendmail', 'resend', 'ses'] as const
function emailFields(provider: string): FieldSpec[] {
  const common: FieldSpec[] = [
    { key: 'provider', label: 'Provider', hint: t('emailProviderHint') },
    { key: 'from', label: t('senderFrom') },
  ]
  switch (provider || 'smtp') {
    case 'sendmail':
      return [...common, { key: 'sendmailPath', label: t('sendmailPath'), hint: t('sendmailPathHint') }]
    case 'resend':
      return [...common, { key: 'apiKey', label: t('apiKey'), secret: true }]
    case 'ses':
      return [
        ...common,
        { key: 'region', label: 'AWS-Region', hint: t('egRegion') },
        { key: 'accessKeyId', label: 'Access-Key-ID' },
        { key: 'secretAccessKey', label: 'Secret-Access-Key', secret: true },
        { key: 'sessionToken', label: 'Session-Token (optional)', secret: true },
      ]
    default: // smtp
      return [
        ...common,
        { key: 'host', label: 'SMTP-Host' },
        { key: 'port', label: 'Port', type: 'number', hint: t('smtpPortHint') },
        { key: 'username', label: t('username') },
        { key: 'password', label: t('password'), secret: true },
        { key: 'tls', label: t('tlsMode'), hint: t('tlsModeHint') },
        { key: 'allowPlaintext', label: t('allowPlaintext'), hint: t('allowPlaintextHint') },
      ]
  }
}

// fieldsFor resolves the visible key set; e-mail hängt vom Provider ab.
function fieldsFor(type: ChannelType, config: Record<string, string>): FieldSpec[] {
  if (type === 'email') return emailFields(config['provider'] ?? '')
  return CONFIG_FIELDS[type] ?? []
}

// Delivery/retry keys every channel type honours (notify queue backoff) —
// rendered as their own "Zustellung / Wiederholungen" group.
const RETRY_FIELDS: FieldSpec[] = [
  { key: 'retryMaxAttempts', label: t('retryMaxAttempts'), type: 'number', hint: t('retryDefaultHint') },
  { key: 'retryBackoffSeconds', label: t('retryBackoffSeconds'), type: 'number', hint: t('retryDefaultHint') },
  { key: 'retryBackoffCapSeconds', label: t('retryBackoffCapSeconds'), type: 'number', hint: t('retryDefaultHint') },
]

export function ChannelsTab() {
  const { data, isLoading } = useQuery({ queryKey: channelsApi.queryKey, queryFn: channelsApi.list })
  const [editing, setEditing] = useState<Channel | 'new' | null>(null)
  const [copying, setCopying] = useState<Channel | null>(null)
  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setEditing('new')} label={t('create')} />
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('type')}</TableHead>
            <TableHead>{t('name')}</TableHead>
            <TableHead>{t('status')}</TableHead>
            <TableHead>Template</TableHead>
            <TableHead></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(data ?? []).map((ch) => (
            <TableRow key={ch.name}>
              <TableCell className="px-3 py-2 w-24"><TypeBadge>{ch.type}</TypeBadge></TableCell>
              <TableCell className="px-3 py-2 text-foreground">{ch.name}</TableCell>
              <TableCell className="px-3 py-2">{ch.enabled ? <StatusBadge kind="enabled" /> : <StatusBadge kind="disabled" />}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground font-mono">{ch.template || '—'}</TableCell>
              <TableCell className="px-3 py-2">
                <RowActions>
                  <TestButton name={ch.name} />
                  <Button size="sm" variant="ghost" onClick={() => setEditing(ch)}>{t('edit')}</Button>
                  <DuplicateButton onClick={() => setCopying(ch)} />
                  <ChannelDelete channel={ch} />
                </RowActions>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}
      {editing && (
        <ChannelDialog name={editing === 'new' ? null : editing.name} onClose={() => setEditing(null)} />
      )}
      {copying && (
        <ChannelDialog name={copying.name} copy existing={(data ?? []).map((c) => c.name)}
          onClose={() => setCopying(null)} />
      )}
    </div>
  )
}

function TestButton({ name }: { name: string }) {
  const [result, setResult] = useState('')
  const test = useMutation({
    mutationFn: (target?: string) =>
      post<{ result: string; detail?: string }>(`/channels/${encodeURIComponent(name)}:test-notification`,
        target ? { target } : {}),
    onSuccess: (r) => setResult(`✓ ${r.result}${r.detail ? ` — ${r.detail}` : ''}`),
    onError: (e: unknown) => setResult(`✕ ${e instanceof Error ? e.message : String(e)}`),
  })
  return (
    <span className="inline-flex items-center gap-2">
      <Button size="sm" variant="ghost" onClick={() => test.mutate(undefined)} disabled={test.isPending}>
        {t('testSend')}
      </Button>
      {result && <span className={`text-xs ${result.startsWith('✓') ? 'text-emerald-400' : 'text-red-400'}`}>{result}</span>}
    </span>
  )
}

function ChannelDelete({ channel }: { channel: Channel }) {
  const save = useSave(() => channelsApi.remove(channel.name), { invalidate: [[...channelsApi.queryKey]] })
  return (
    <>
      <DeleteButton onDelete={() => save.mutate(undefined)} />
      {save.isError && <FormError error={save.error} />}
    </>
  )
}

// ChannelDialog: `name` null → create; set → edit; set + `copy` → create a
// duplicate seeded from that channel (envelope stripped, fresh name).
function ChannelDialog({ name, copy, existing, onClose }: {
  name: string | null; copy?: boolean; existing?: string[]; onClose: () => void
}) {
  const isNew = !name || !!copy
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...channelsApi.queryKey, name],
    queryFn: () => channelsApi.get(name!),
    enabled: !!name,
  })
  if (name && isLoading) {
    return (
      <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t('loading')}</DialogTitle>
          </DialogHeader>
          <Spinner />
        </DialogContent>
      </Dialog>
    )
  }
  const src = loaded?.data
  return (
    <ChannelForm
      doc={copy && src ? duplicateDoc(src, existing) : (src ?? { name: '', type: 'email', enabled: true, config: {} })}
      etag={copy ? 0 : (loaded?.etag ?? 0)}
      isNew={isNew}
      copyOf={copy ? name ?? undefined : undefined}
      onClose={onClose}
    />
  )
}

function ChannelForm({ doc, etag, isNew, copyOf, onClose }: {
  doc: Channel; etag: number; isNew: boolean; copyOf?: string; onClose: () => void
}) {
  const [name, setName] = useState(doc.name)
  const [type, setType] = useState<ChannelType>(doc.type)
  const [enabled, setEnabled] = useState(doc.enabled)
  const [config, setConfig] = useState<Record<string, string>>(doc.config ?? {})
  const [template, setTemplate] = useState(doc.template ?? '')

  const known = fieldsFor(type, config)
  const knownKeys = new Set([...known, ...RETRY_FIELDS].map((f) => f.key))
  const extra = Object.fromEntries(Object.entries(config).filter(([k]) => !knownKeys.has(k)))

  const setField = (k: string, v: string) =>
    setConfig((c) => { const next = { ...c }; if (v === '') delete next[k]; else next[k] = v; return next })
  const setExtra = (next: Record<string, string>) =>
    setConfig({ ...Object.fromEntries(Object.entries(config).filter(([k]) => knownKeys.has(k))), ...next })

  const build = (): Channel => ({ ...doc, name, type, enabled, config, template: template || undefined })
  const save = useSave(
    () => isNew ? channelsApi.create(build()) : channelsApi.update(doc.name, build(), etag),
    { invalidate: [[...channelsApi.queryKey]], onDone: onClose },
  )
  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{copyOf ? `${t('duplicate')}: ${copyOf}` : isNew ? t('create') : `${t('edit')}: ${doc.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
          <div className="grid grid-cols-2 gap-2">
            <Field label={t('name')} required>
              <Input value={name} onChange={(e) => setName(e.target.value)} required disabled={!isNew} />
            </Field>
            <Field label={t('type')}>
              <Select value={type} onValueChange={(v) => setType(v as ChannelType)}>
                <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {CHANNEL_TYPES.map((ty) => <SelectItem key={ty} value={ty}>{ty}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <div className="flex items-center gap-2">
            <Switch id="channel-enabled" checked={enabled} onCheckedChange={setEnabled} />
            <Label htmlFor="channel-enabled">{t('enabled')}</Label>
          </div>

          {known.length > 0 && (
            <div className="border border-border rounded-lg p-3 space-y-2">
              <div className="text-xs text-muted-foreground font-medium">{t('configuration')} ({type})</div>
              {known.map((f) => (
                <Field key={f.key} label={f.label} hint={f.secret ? t('secretHint') : f.hint}>
                  {type === 'email' && f.key === 'provider' ? (
                    <Select value={config['provider'] || 'smtp'} onValueChange={(v) => setField('provider', v === 'smtp' ? '' : v)}>
                      <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                      <SelectContent>
                        {EMAIL_PROVIDERS.map((p) => <SelectItem key={p} value={p}>{p}</SelectItem>)}
                      </SelectContent>
                    </Select>
                  ) : (
                    <Input
                      type={f.type === 'number' ? 'number' : (f.secret ? 'text' : 'text')}
                      value={config[f.key] ?? ''}
                      onChange={(e) => setField(f.key, e.target.value)}
                    />
                  )}
                </Field>
              ))}
            </div>
          )}

          {/* Delivery/retry settings — honoured by every channel type. */}
          <div className="border border-border rounded-lg p-3 space-y-2">
            <div className="text-xs text-muted-foreground font-medium">{t('retryGroup')}</div>
            <div className="grid grid-cols-3 gap-2">
              {RETRY_FIELDS.map((f) => (
                <Field key={f.key} label={f.label} hint={f.hint}>
                  <Input type="number" value={config[f.key] ?? ''}
                    onChange={(e) => setField(f.key, e.target.value)} />
                </Field>
              ))}
            </div>
          </div>

          <Field label={t('moreSettings')} hint={t('moreSettingsHintAny')}>
            <KVEditor value={extra} onChange={setExtra} />
          </Field>

          <Field label="Template" hint={t('templateHint')}>
            <Input value={template} onChange={(e) => setTemplate(e.target.value)} placeholder={t('defaultParen')} />
          </Field>

          {type === 'push' && (
            <Badge variant="outline" className="bg-muted text-muted-foreground border-input">{t('pushMobileHint')}</Badge>
          )}
          {type === 'voice' && (
            <Badge variant="outline" className="bg-muted text-muted-foreground border-input">{t('voiceDtmfHint')}</Badge>
          )}

          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name} />
        </form>
      </DialogContent>
    </Dialog>
  )
}
