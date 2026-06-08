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
import { Empty, Spinner, Field, FormError, SubmitRow, useSave, DeleteButton, KVEditor } from '@/components/kit'
import { t } from '../../i18n'
import { TypeBadge, StatusBadge, TableActions, RowActions, secretHint } from './common'

const channelsApi = resourceApi<Channel>('channels')

const CHANNEL_TYPES: ChannelType[] = [
  'email', 'webhook', 'slack', 'teams', 'ntfy', 'sms', 'push', 'voice',
  'servicenow', 'zendesk', 'jira', 'ticket',
]

// Field spec per config key. secret → render the $SECRET hint.
type FieldSpec = { key: string; label: string; secret?: boolean; hint?: string; type?: 'number' }
// Per-channel-type key sets (SPEC §12.3). "push"/"voice" need no config
// here (VAPID / provider are server-side) — only the KVEditor fallback.
const CONFIG_FIELDS: Record<string, FieldSpec[]> = {
  email: [
    { key: 'host', label: 'SMTP-Host' },
    { key: 'port', label: 'Port', type: 'number' },
    { key: 'from', label: 'Absender (From)' },
    { key: 'username', label: 'Benutzername' },
    { key: 'password', label: 'Passwort', secret: true },
    { key: 'starttls', label: 'STARTTLS (true/false)' },
  ],
  webhook: [
    { key: 'url', label: 'URL' },
    { key: 'secret', label: 'HMAC-Secret', secret: true },
    { key: 'method', label: 'HTTP-Methode', hint: 'POST (Standard)' },
  ],
  slack: [{ key: 'url', label: 'Webhook-URL', secret: true }],
  teams: [{ key: 'url', label: 'Webhook-URL', secret: true }],
  ntfy: [
    { key: 'url', label: 'Server-URL', hint: 'z.B. https://ntfy.sh' },
    { key: 'topic', label: 'Topic' },
    { key: 'token', label: 'Access-Token', secret: true },
  ],
  // sms is provider-driven; we surface the common twilio + generic-http keys.
  sms: [
    { key: 'provider', label: 'Provider', hint: 'twilio | generic-http' },
    { key: 'accountSid', label: 'Account SID (twilio)', secret: true },
    { key: 'authToken', label: 'Auth-Token (twilio)', secret: true },
    { key: 'from', label: 'Absender-Nummer (twilio)' },
    { key: 'url', label: 'URL (generic-http)', secret: true },
  ],
  push: [],
  // voice: Twilio Voice (TTS + DTMF-Ack über /api/v1/voice/gather) oder
  // generic-http Gateways (SPEC §9.6).
  voice: [
    { key: 'provider', label: 'Provider', hint: 'twilio | generic-http' },
    { key: 'accountSid', label: 'Account SID (twilio)', secret: true },
    { key: 'authToken', label: 'Auth-Token (twilio)', secret: true },
    { key: 'from', label: 'Anrufer-Nummer (twilio)' },
    { key: 'language', label: 'TTS-Sprache', hint: 'z.B. de-DE (Standard en-US)' },
    { key: 'url', label: 'URL (generic-http)', secret: true },
  ],
  // Ticket-Systeme (F-04.05): Ticket bei Eskalation, Auto-Close bei Resolve.
  servicenow: [
    { key: 'url', label: 'Instanz-URL', hint: 'https://<instanz>.service-now.com' },
    { key: 'username', label: 'Benutzername' },
    { key: 'password', label: 'Passwort', secret: true },
    { key: 'table', label: 'Tabelle', hint: 'incident (Standard)' },
    { key: 'closeState', label: 'Close-State', hint: '6 = Resolved (Standard)' },
    { key: 'autoClose', label: 'Auto-Close (true/false)' },
  ],
  zendesk: [
    { key: 'url', label: 'Subdomain-URL', hint: 'https://<subdomain>.zendesk.com' },
    { key: 'email', label: 'Agent-E-Mail' },
    { key: 'apiToken', label: 'API-Token', secret: true },
    { key: 'closeStatus', label: 'Close-Status', hint: 'solved (Standard)' },
    { key: 'autoClose', label: 'Auto-Close (true/false)' },
  ],
  jira: [
    { key: 'url', label: 'Jira-URL', hint: 'https://<org>.atlassian.net' },
    { key: 'project', label: 'Projekt-Key', hint: 'z.B. OPS' },
    { key: 'issueType', label: 'Issue-Typ', hint: 'Task (Standard)' },
    { key: 'username', label: 'Benutzer / E-Mail' },
    { key: 'password', label: 'API-Token', secret: true },
    { key: 'closeTransitionId', label: 'Close-Transition-ID', hint: 'Workflow-Transition zum Schließen' },
    { key: 'autoClose', label: 'Auto-Close (true/false)' },
  ],
  ticket: [
    { key: 'url', label: 'Create-URL (POST, JSON)' },
    { key: 'token', label: 'Bearer-Token', secret: true },
    { key: 'username', label: 'Basic-Auth Benutzer' },
    { key: 'password', label: 'Basic-Auth Passwort', secret: true },
    { key: 'refField', label: 'Ticket-ID-Feld', hint: 'JSON-Pfad, z.B. data.ticketId (Standard: id)' },
    { key: 'ticketUrlTemplate', label: 'Ticket-URL-Vorlage', hint: 'https://…/{ref}' },
    { key: 'closeUrl', label: 'Close-URL', hint: '{ref}-Platzhalter' },
    { key: 'autoClose', label: 'Auto-Close (true/false)' },
  ],
}

export function ChannelsTab() {
  const { data, isLoading } = useQuery({ queryKey: channelsApi.queryKey, queryFn: channelsApi.list })
  const [editing, setEditing] = useState<Channel | 'new' | null>(null)
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

function ChannelDialog({ name, onClose }: { name: string | null; onClose: () => void }) {
  const isNew = !name
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...channelsApi.queryKey, name],
    queryFn: () => channelsApi.get(name!),
    enabled: !isNew,
  })
  if (!isNew && isLoading) {
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
  return (
    <ChannelForm
      doc={loaded?.data ?? { name: '', type: 'email', enabled: true, config: {} }}
      etag={loaded?.etag ?? 0}
      isNew={isNew}
      onClose={onClose}
    />
  )
}

function ChannelForm({ doc, etag, isNew, onClose }: {
  doc: Channel; etag: number; isNew: boolean; onClose: () => void
}) {
  const [name, setName] = useState(doc.name)
  const [type, setType] = useState<ChannelType>(doc.type)
  const [enabled, setEnabled] = useState(doc.enabled)
  const [config, setConfig] = useState<Record<string, string>>(doc.config ?? {})
  const [template, setTemplate] = useState(doc.template ?? '')

  const known = CONFIG_FIELDS[type] ?? []
  const knownKeys = new Set(known.map((f) => f.key))
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
          <DialogTitle>{isNew ? t('create') : `${t('edit')}: ${doc.name}`}</DialogTitle>
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
              <div className="text-xs text-muted-foreground font-medium">Konfiguration ({type})</div>
              {known.map((f) => (
                <Field key={f.key} label={f.label} hint={f.secret ? secretHint : f.hint}>
                  <Input
                    type={f.type === 'number' ? 'number' : (f.secret ? 'text' : 'text')}
                    value={config[f.key] ?? ''}
                    onChange={(e) => setField(f.key, e.target.value)}
                  />
                </Field>
              ))}
            </div>
          )}

          <Field label="Weitere Einstellungen" hint="Beliebige zusätzliche Config-Schlüssel">
            <KVEditor value={extra} onChange={setExtra} />
          </Field>

          <Field label="Template" hint="optional — überschreibt das Standard-Template">
            <Input value={template} onChange={(e) => setTemplate(e.target.value)} placeholder="(Standard)" />
          </Field>

          {type === 'push' && (
            <Badge variant="outline" className="bg-muted text-muted-foreground border-input">VAPID serverseitig — keine Config nötig</Badge>
          )}

          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name} />
        </form>
      </DialogContent>
    </Dialog>
  )
}
