// Event sources (SPEC §7.5/§9.2): every ingress adapter is an EventSource,
// ETag-versioned via resourceApi<EventSourceDef>('event-sources'). The form
// has a common header (name, type, enabled, rateLimit/burst, labels) plus a
// type-specific Config block: webhook/alertmanager expose auth + ingest URL,
// snmp-trap exposes the listener/community/severity and SNMPv3 credentials,
// imap/email expose the mailbox connection, voice-inbound/sms-inbound the
// Twilio telephony ingress (IVR menu, allowFrom, ACK keyword), mqtt the
// broker subscription, espa/espa-x the serial-protocol listeners. webhook
// sources additionally get a CEL Mapping editor. Whatever the typed block
// omits stays reachable via a "Weitere Einstellungen" KVEditor.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { resourceApi } from '../../api'
import type { EventSourceDef, Severity } from '../../types'
import { Button } from '@/components/ui/button'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { Empty, Spinner, Field, FormError, SubmitRow, useSave, DeleteButton, KVEditor, DurationInput, DuplicateButton } from '@/components/kit'
import { duplicateDoc } from '@/lib/duplicate'
import { t } from '../../i18n'
import { TypeBadge, StatusBadge, TableActions, RowActions } from './common'

// Radix SelectItem value cannot be "" — sentinel for the "(Standard)"/"—"
// empty config options. setCfg('', …) deletes the key, matching the old
// native <option value=""> behaviour.
const CFG_DEFAULT = '__default__'

const sourcesApi = resourceApi<EventSourceDef>('event-sources')

const SOURCE_TYPES = [
  'webhook', 'alertmanager', 'snmp-trap', 'imap', 'email',
  'voice-inbound', 'sms-inbound', 'asterisk-inbound', 'mqtt', 'espa', 'espa-x',
] as const
const AUTH_MODES = ['none', 'token', 'hmac', 'basic'] as const
const SEVERITIES: Severity[] = ['critical', 'warning', 'info', 'ok']

export function SourcesTab() {
  const { data, isLoading } = useQuery({ queryKey: sourcesApi.queryKey, queryFn: sourcesApi.list })
  const [editing, setEditing] = useState<EventSourceDef | 'new' | null>(null)
  const [copying, setCopying] = useState<EventSourceDef | null>(null)
  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setEditing('new')} label={t('create')} />
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('type')}</TableHead>
            <TableHead>{t('name')}</TableHead>
            <TableHead>{t('status')}</TableHead>
            <TableHead>Ingest</TableHead>
            <TableHead></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(data ?? []).map((s) => (
            <TableRow key={s.name}>
              <TableCell className="px-3 py-2 w-28"><TypeBadge>{s.type}</TypeBadge></TableCell>
              <TableCell className="px-3 py-2 text-foreground">{s.name}</TableCell>
              <TableCell className="px-3 py-2">{s.enabled ? <StatusBadge kind="enabled" /> : <StatusBadge kind="disabled" />}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground font-mono truncate max-w-56">
                {(s.type === 'webhook' || s.type === 'alertmanager') ? `/api/v1/ingest/${s.name}`
                  : s.type === 'voice-inbound' ? `/api/v1/voice/inbound/${s.id ?? ''}`
                  : s.type === 'sms-inbound' ? `/api/v1/sms/inbound/${s.id ?? ''}`
                  : s.type === 'asterisk-inbound' ? `agi://<host>:4573/${s.id ?? ''}`
                  : '—'}
              </TableCell>
              <TableCell className="px-3 py-2">
                <RowActions>
                  <Button size="sm" variant="ghost" onClick={() => setEditing(s)}>{t('edit')}</Button>
                  <DuplicateButton onClick={() => setCopying(s)} />
                  <SourceDelete source={s} />
                </RowActions>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}
      {editing && (
        <SourceDialog name={editing === 'new' ? null : editing.name} onClose={() => setEditing(null)} />
      )}
      {copying && (
        <SourceDialog name={copying.name} copy existing={(data ?? []).map((x) => x.name)}
          onClose={() => setCopying(null)} />
      )}
    </div>
  )
}

function SourceDelete({ source }: { source: EventSourceDef }) {
  const save = useSave(() => sourcesApi.remove(source.name), { invalidate: [[...sourcesApi.queryKey]] })
  return (
    <>
      <DeleteButton onDelete={() => save.mutate(undefined)} />
      {save.isError && <FormError error={save.error} />}
    </>
  )
}

// SourceDialog: `name` null → create; set → edit; set + `copy` → create a
// duplicate seeded from that source (envelope stripped — the copy gets its
// own id, hence its own inbound URL — fresh name).
function SourceDialog({ name, copy, existing, onClose }: {
  name: string | null; copy?: boolean; existing?: string[]; onClose: () => void
}) {
  const isNew = !name || !!copy
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...sourcesApi.queryKey, name],
    queryFn: () => sourcesApi.get(name!),
    enabled: !!name,
  })
  if (name && isLoading) {
    return (
      <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
        <DialogContent className="max-w-4xl">
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
    <SourceForm
      doc={copy && src ? duplicateDoc(src, existing) : (src ?? { name: '', type: 'webhook', enabled: true, authMode: 'none', config: {} })}
      etag={copy ? 0 : (loaded?.etag ?? 0)}
      isNew={isNew}
      copyOf={copy ? name ?? undefined : undefined}
      onClose={onClose}
    />
  )
}

// Known config keys per source type; the rest fall into the KVEditor.
const SNMP_KEYS = ['listen', 'community', 'severity', 'v3User', 'v3AuthProto', 'v3AuthSecretRef', 'v3PrivProto', 'v3PrivSecretRef']
const MAIL_KEYS = ['host', 'port', 'tls', 'username', 'passwordSecretRef', 'folder', 'pollInterval', 'markSeen', 'severity']
const VOICE_KEYS = ['menu', 'language', 'voice', 'allowFrom', 'escalationPolicy', 'severity', 'twilioAuthToken']
const SMS_KEYS = ['action', 'escalationPolicy', 'severity', 'allowFrom', 'ackKeyword', 'language', 'twilioAuthToken']
const MQTT_KEYS = ['url', 'topics', 'qos', 'clientId', 'username', 'password', 'passwordSecretRef', 'tlsInsecure', 'severity']
const ESPA_KEYS = ['listen', 'severity']
const AGI_KEYS = ['listen', 'menu', 'language', 'ttsApp', 'escalationPolicy', 'severity', 'allowFrom', 'recordDir']

function knownKeysFor(type: string): string[] {
  switch (type) {
    case 'snmp-trap': return SNMP_KEYS
    case 'imap': case 'email': return MAIL_KEYS
    case 'voice-inbound': return VOICE_KEYS
    case 'sms-inbound': return SMS_KEYS
    case 'mqtt': return MQTT_KEYS
    case 'espa': case 'espa-x': return ESPA_KEYS
    case 'asterisk-inbound': return AGI_KEYS
    default: return []
  }
}

// Severity select for a config key ('' = backend default) — shared by the
// typed config blocks below.
function CfgSeverity({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <Select value={value || CFG_DEFAULT} onValueChange={(v) => onChange(v === CFG_DEFAULT ? '' : v)}>
      <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
      <SelectContent>
        <SelectItem value={CFG_DEFAULT}>{t('defaultParen')}</SelectItem>
        {SEVERITIES.map((s) => <SelectItem key={s} value={s}>{s}</SelectItem>)}
      </SelectContent>
    </Select>
  )
}

function SourceForm({ doc, etag, isNew, copyOf, onClose }: {
  doc: EventSourceDef; etag: number; isNew: boolean; copyOf?: string; onClose: () => void
}) {
  const [name, setName] = useState(doc.name)
  const [type, setType] = useState<string>(doc.type)
  const [enabled, setEnabled] = useState(doc.enabled)
  const [authMode, setAuthMode] = useState<EventSourceDef['authMode']>(doc.authMode ?? 'none')
  const [secretRef, setSecretRef] = useState(doc.secretRef ?? '')
  const [rateLimit, setRateLimit] = useState(doc.rateLimit != null ? String(doc.rateLimit) : '')
  const [burst, setBurst] = useState(doc.burst != null ? String(doc.burst) : '')
  const [labels, setLabels] = useState<Record<string, string>>(doc.labels ?? {})
  const [mapping, setMapping] = useState<Record<string, string>>(doc.mapping ?? {})
  const [config, setConfig] = useState<Record<string, string>>(doc.config ?? {})

  const cfg = (k: string) => config[k] ?? ''
  const setCfg = (k: string, v: string) =>
    setConfig((c) => { const next = { ...c }; if (v === '') delete next[k]; else next[k] = v; return next })

  const knownKeys = knownKeysFor(type)
  const knownSet = new Set(knownKeys)
  const extra = Object.fromEntries(Object.entries(config).filter(([k]) => !knownSet.has(k)))
  const setExtra = (next: Record<string, string>) =>
    setConfig({ ...Object.fromEntries(Object.entries(config).filter(([k]) => knownSet.has(k))), ...next })

  const isHTTP = type === 'webhook' || type === 'alertmanager'
  const isMail = type === 'imap' || type === 'email'
  // Telephony ingress (Twilio webhooks): auth like HTTP ingress, but the
  // inbound URL is addressed by the source ID, not the name.
  const isTel = type === 'voice-inbound' || type === 'sms-inbound'

  const build = (): EventSourceDef => ({
    ...doc, name, type, enabled, authMode, secretRef: secretRef || undefined,
    rateLimit: rateLimit ? Number(rateLimit) : undefined,
    burst: burst ? Number(burst) : undefined,
    labels: Object.keys(labels).length ? labels : undefined,
    mapping: Object.keys(mapping).length ? mapping : undefined,
    config: Object.keys(config).length ? config : undefined,
  })
  const save = useSave(
    () => isNew ? sourcesApi.create(build()) : sourcesApi.update(doc.name, build(), etag),
    { invalidate: [[...sourcesApi.queryKey]], onDone: onClose },
  )

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-4xl">
        <DialogHeader>
          <DialogTitle>{copyOf ? `${t('duplicate')}: ${copyOf}` : isNew ? t('create') : `${t('edit')}: ${doc.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
          <div className="grid grid-cols-2 gap-2">
            <Field label={t('name')} required>
              <Input value={name} onChange={(e) => setName(e.target.value)} required disabled={!isNew} />
            </Field>
            <Field label={t('type')}>
              <Select value={type} onValueChange={(v) => setType(v)}>
                <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {SOURCE_TYPES.map((ty) => <SelectItem key={ty} value={ty}>{ty}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <div className="flex items-center gap-2">
            <Switch id="source-enabled" checked={enabled} onCheckedChange={setEnabled} />
            <Label htmlFor="source-enabled">{t('enabled')}</Label>
          </div>

          <div className="grid grid-cols-2 gap-2">
            <Field label={t('rateLimitField')} hint={t('rateLimitHint')}>
              <Input type="number" step="any" value={rateLimit} onChange={(e) => setRateLimit(e.target.value)} />
            </Field>
            <Field label="Burst">
              <Input type="number" value={burst} onChange={(e) => setBurst(e.target.value)} />
            </Field>
          </div>

          {/* HTTP ingress (webhook / alertmanager / voice-inbound / sms-inbound) */}
          {(isHTTP || isTel) && (
            <div className="border border-border rounded-lg p-3 space-y-2">
              <div className="text-xs text-muted-foreground font-medium">Ingress ({type})</div>
              <div className="grid grid-cols-2 gap-2">
                <Field label={t('authMode')}>
                  <Select value={authMode} onValueChange={(v) => setAuthMode(v as EventSourceDef['authMode'])}>
                    <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      {AUTH_MODES.map((m) => <SelectItem key={m} value={m}>{m}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label={t('secretRef')} hint={t('secretHint')}>
                  <Input value={secretRef} onChange={(e) => setSecretRef(e.target.value)} />
                </Field>
              </div>
              {isHTTP && (
                <div className="text-[11px] text-muted-foreground">
                  Ingest-URL: <code className="text-muted-foreground">/api/v1/ingest/{name || '<name>'}</code>
                </div>
              )}
              {type === 'voice-inbound' && (
                <div className="text-[11px] text-muted-foreground">
                  {t('inboundUrlLabel')}{' '}
                  <code className="text-muted-foreground">{`/api/v1/voice/inbound/${doc.id || '<id>'}?token=<token>`}</code>
                </div>
              )}
              {type === 'sms-inbound' && (
                <div className="text-[11px] text-muted-foreground">
                  {t('inboundUrlLabel')}{' '}
                  <code className="text-muted-foreground">{`/api/v1/sms/inbound/${doc.id || '<id>'}?token=<token>`}</code>
                </div>
              )}
            </div>
          )}

          {/* SNMP trap receiver */}
          {type === 'snmp-trap' && (
            <div className="border border-border rounded-lg p-3 space-y-2">
              <div className="text-xs text-muted-foreground font-medium">SNMP-Trap</div>
              <div className="grid grid-cols-2 gap-2">
                <Field label="Listen" hint="udp://:9162"><Input value={cfg('listen')} onChange={(e) => setCfg('listen', e.target.value)} placeholder="udp://:9162" /></Field>
                <Field label="Community (v1/v2c)"><Input value={cfg('community')} onChange={(e) => setCfg('community', e.target.value)} /></Field>
                <Field label={t('severity')}>
                  <Select value={cfg('severity') || CFG_DEFAULT} onValueChange={(v) => setCfg('severity', v === CFG_DEFAULT ? '' : v)}>
                    <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value={CFG_DEFAULT}>{t('defaultParen')}</SelectItem>
                      {SEVERITIES.map((s) => <SelectItem key={s} value={s}>{s}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </Field>
              </div>
              <div className="text-xs text-muted-foreground pt-1">SNMPv3</div>
              <div className="grid grid-cols-2 gap-2">
                <Field label="v3 User"><Input value={cfg('v3User')} onChange={(e) => setCfg('v3User', e.target.value)} /></Field>
                <Field label={t('authProtocol')}>
                  <Select value={cfg('v3AuthProto') || CFG_DEFAULT} onValueChange={(v) => setCfg('v3AuthProto', v === CFG_DEFAULT ? '' : v)}>
                    <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value={CFG_DEFAULT}>—</SelectItem>
                      {['MD5', 'SHA', 'SHA256'].map((x) => <SelectItem key={x} value={x}>{x}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label={t('authSecretRef')} hint={t('secretHint')}><Input value={cfg('v3AuthSecretRef')} onChange={(e) => setCfg('v3AuthSecretRef', e.target.value)} /></Field>
                <Field label={t('privProtocol')}>
                  <Select value={cfg('v3PrivProto') || CFG_DEFAULT} onValueChange={(v) => setCfg('v3PrivProto', v === CFG_DEFAULT ? '' : v)}>
                    <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value={CFG_DEFAULT}>—</SelectItem>
                      {['DES', 'AES', 'AES256'].map((x) => <SelectItem key={x} value={x}>{x}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label={t('privSecretRef')} hint={t('secretHint')}><Input value={cfg('v3PrivSecretRef')} onChange={(e) => setCfg('v3PrivSecretRef', e.target.value)} /></Field>
              </div>
            </div>
          )}

          {/* IMAP / e-mail mailbox poller */}
          {isMail && (
            <div className="border border-border rounded-lg p-3 space-y-2">
              <div className="text-xs text-muted-foreground font-medium">{t('mailbox')} ({type})</div>
              <div className="grid grid-cols-2 gap-2">
                <Field label={t('host')}><Input value={cfg('host')} onChange={(e) => setCfg('host', e.target.value)} /></Field>
                <Field label="Port"><Input type="number" value={cfg('port')} onChange={(e) => setCfg('port', e.target.value)} /></Field>
                <Field label="TLS">
                  <Select value={cfg('tls') || CFG_DEFAULT} onValueChange={(v) => setCfg('tls', v === CFG_DEFAULT ? '' : v)}>
                    <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value={CFG_DEFAULT}>{t('defaultParen')}</SelectItem>
                      <SelectItem value="on">on</SelectItem>
                      <SelectItem value="off">off</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
                <Field label={t('username')}><Input value={cfg('username')} onChange={(e) => setCfg('username', e.target.value)} /></Field>
                <Field label={t('passwordSecretRef')} hint={t('secretHint')}><Input value={cfg('passwordSecretRef')} onChange={(e) => setCfg('passwordSecretRef', e.target.value)} /></Field>
                <Field label={t('folder')}><Input value={cfg('folder')} onChange={(e) => setCfg('folder', e.target.value)} placeholder="INBOX" /></Field>
                <Field label={t('pollInterval')}>
                  <DurationInput value={cfg('pollInterval')} onChange={(v) => setCfg('pollInterval', v)} placeholder="60s" />
                </Field>
                <Field label={t('severity')}>
                  <Select value={cfg('severity') || CFG_DEFAULT} onValueChange={(v) => setCfg('severity', v === CFG_DEFAULT ? '' : v)}>
                    <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value={CFG_DEFAULT}>{t('defaultParen')}</SelectItem>
                      {SEVERITIES.map((s) => <SelectItem key={s} value={s}>{s}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </Field>
              </div>
              <div className="flex items-center gap-2">
                <Switch id="source-markseen" checked={cfg('markSeen') === 'true'} onCheckedChange={(v) => setCfg('markSeen', v ? 'true' : '')} />
                <Label htmlFor="source-markseen">{t('markSeen')}</Label>
              </div>
            </div>
          )}

          {/* Voice inbound: Twilio call → IVR menu (SPEC §9.6 evolution) */}
          {type === 'voice-inbound' && (
            <div className="border border-border rounded-lg p-3 space-y-2">
              <div className="text-xs text-muted-foreground font-medium">Voice ({type})</div>
              <div className="grid grid-cols-2 gap-2">
                <Field label={t('ivrMenuField')} hint={t('ivrMenuHint')}>
                  <Input value={cfg('menu')} onChange={(e) => setCfg('menu', e.target.value)} />
                </Field>
                <Field label={t('languageField')} hint={t('ttsLanguageHint')}>
                  <Input value={cfg('language')} onChange={(e) => setCfg('language', e.target.value)} placeholder="de-DE" />
                </Field>
                <Field label={t('voiceField')} hint={t('voiceHint')}>
                  <Input value={cfg('voice')} onChange={(e) => setCfg('voice', e.target.value)} />
                </Field>
                <Field label={t('allowFrom')} hint={t('allowFromHint')}>
                  <Input value={cfg('allowFrom')} onChange={(e) => setCfg('allowFrom', e.target.value)} placeholder="+49,+43" />
                </Field>
                <Field label={t('escalationPolicyField')}>
                  <Input value={cfg('escalationPolicy')} onChange={(e) => setCfg('escalationPolicy', e.target.value)} />
                </Field>
                <Field label={t('severity')}>
                  <CfgSeverity value={cfg('severity')} onChange={(v) => setCfg('severity', v)} />
                </Field>
                <Field label={t('twilioAuthToken')} hint={t('secretHint')}>
                  <Input value={cfg('twilioAuthToken')} onChange={(e) => setCfg('twilioAuthToken', e.target.value)} placeholder="$SECRET:twilio-auth$" />
                </Field>
              </div>
            </div>
          )}

          {/* SMS inbound: Twilio SMS → event/alert + ACK keyword */}
          {type === 'sms-inbound' && (
            <div className="border border-border rounded-lg p-3 space-y-2">
              <div className="text-xs text-muted-foreground font-medium">SMS ({type})</div>
              <div className="grid grid-cols-2 gap-2">
                <Field label={t('action')} hint={t('smsActionHint')}>
                  <Select value={cfg('action') || CFG_DEFAULT} onValueChange={(v) => setCfg('action', v === CFG_DEFAULT ? '' : v)}>
                    <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value={CFG_DEFAULT}>event {t('defaultParen')}</SelectItem>
                      <SelectItem value="alert">alert</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
                <Field label={t('escalationPolicyField')}>
                  <Input value={cfg('escalationPolicy')} onChange={(e) => setCfg('escalationPolicy', e.target.value)} />
                </Field>
                <Field label={t('severity')}>
                  <CfgSeverity value={cfg('severity')} onChange={(v) => setCfg('severity', v)} />
                </Field>
                <Field label={t('allowFrom')} hint={t('allowFromHint')}>
                  <Input value={cfg('allowFrom')} onChange={(e) => setCfg('allowFrom', e.target.value)} placeholder="+49,+43" />
                </Field>
                <Field label={t('ackKeyword')} hint={t('ackKeywordHint')}>
                  <Input value={cfg('ackKeyword')} onChange={(e) => setCfg('ackKeyword', e.target.value)} placeholder="ACK" />
                </Field>
                <Field label={t('languageField')} hint={t('ttsLanguageHint')}>
                  <Input value={cfg('language')} onChange={(e) => setCfg('language', e.target.value)} />
                </Field>
                <Field label={t('twilioAuthToken')} hint={t('secretHint')}>
                  <Input value={cfg('twilioAuthToken')} onChange={(e) => setCfg('twilioAuthToken', e.target.value)} placeholder="$SECRET:twilio-auth$" />
                </Field>
              </div>
            </div>
          )}

          {/* MQTT subscriber */}
          {type === 'mqtt' && (
            <div className="border border-border rounded-lg p-3 space-y-2">
              <div className="text-xs text-muted-foreground font-medium">MQTT</div>
              <div className="grid grid-cols-2 gap-2">
                <Field label={t('brokerUrl')} hint={t('mqttUrlHint')} required>
                  <Input value={cfg('url')} onChange={(e) => setCfg('url', e.target.value)} placeholder="tcp://broker:1883" />
                </Field>
                <Field label="Topics" hint={t('topicsHint')} required>
                  <Input value={cfg('topics')} onChange={(e) => setCfg('topics', e.target.value)} placeholder="alarme/#" />
                </Field>
                <Field label="QoS">
                  <Select value={cfg('qos') || CFG_DEFAULT} onValueChange={(v) => setCfg('qos', v === CFG_DEFAULT ? '' : v)}>
                    <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value={CFG_DEFAULT}>{t('defaultParen')}</SelectItem>
                      {['0', '1', '2'].map((q) => <SelectItem key={q} value={q}>{q}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label="Client-ID">
                  <Input value={cfg('clientId')} onChange={(e) => setCfg('clientId', e.target.value)} />
                </Field>
                <Field label={t('username')}>
                  <Input value={cfg('username')} onChange={(e) => setCfg('username', e.target.value)} />
                </Field>
                <Field label={t('password')} hint={t('secretHint')}>
                  <Input value={cfg('password')} onChange={(e) => setCfg('password', e.target.value)} />
                </Field>
                <Field label={t('passwordSecretRef')} hint={t('secretHint')}>
                  <Input value={cfg('passwordSecretRef')} onChange={(e) => setCfg('passwordSecretRef', e.target.value)} />
                </Field>
                <Field label={t('severity')}>
                  <CfgSeverity value={cfg('severity')} onChange={(v) => setCfg('severity', v)} />
                </Field>
              </div>
              <div className="flex items-center gap-2">
                <Switch id="source-tlsinsecure" checked={cfg('tlsInsecure') === 'true'} onCheckedChange={(v) => setCfg('tlsInsecure', v ? 'true' : '')} />
                <Label htmlFor="source-tlsinsecure">{t('tlsInsecure')}</Label>
              </div>
            </div>
          )}

          {/* ESPA 4.4.4 / ESPA-X listeners (paging / nurse-call systems) */}
          {(type === 'espa' || type === 'espa-x') && (
            <div className="border border-border rounded-lg p-3 space-y-2">
              <div className="text-xs text-muted-foreground font-medium">{type === 'espa' ? 'ESPA 4.4.4' : 'ESPA-X'}</div>
              <div className="grid grid-cols-2 gap-2">
                <Field label="Listen" hint={type === 'espa' ? 'tcp://:2023' : 'tcp://:8123'}>
                  <Input value={cfg('listen')} onChange={(e) => setCfg('listen', e.target.value)}
                    placeholder={type === 'espa' ? 'tcp://:2023' : 'tcp://:8123'} />
                </Field>
                <Field label={t('severity')}>
                  <CfgSeverity value={cfg('severity')} onChange={(v) => setCfg('severity', v)} />
                </Field>
              </div>
            </div>
          )}

          {/* Asterisk inbound: FastAGI from the on-prem PBX (no cloud) */}
          {type === 'asterisk-inbound' && (
            <div className="border border-border rounded-lg p-3 space-y-2">
              <div className="text-xs text-muted-foreground font-medium">Asterisk FastAGI</div>
              <div className="grid grid-cols-2 gap-2">
                <Field label="Listen" hint="tcp://:4573">
                  <Input value={cfg('listen')} onChange={(e) => setCfg('listen', e.target.value)} placeholder="tcp://:4573" />
                </Field>
                <Field label={t('ivrMenuField')} hint={t('ivrMenuHint')}>
                  <Input value={cfg('menu')} onChange={(e) => setCfg('menu', e.target.value)} />
                </Field>
                <Field label={t('languageField')} hint={t('ttsLanguageHint')}>
                  <Input value={cfg('language')} onChange={(e) => setCfg('language', e.target.value)} placeholder="de-DE" />
                </Field>
                <Field label={t('agiTtsApp')} hint={t('agiTtsAppHint')}>
                  <Input value={cfg('ttsApp')} onChange={(e) => setCfg('ttsApp', e.target.value)} placeholder="Flite" />
                </Field>
                <Field label={t('escalationPolicyField')}>
                  <Input value={cfg('escalationPolicy')} onChange={(e) => setCfg('escalationPolicy', e.target.value)} />
                </Field>
                <Field label={t('severity')}>
                  <CfgSeverity value={cfg('severity')} onChange={(v) => setCfg('severity', v)} />
                </Field>
                <Field label={t('allowFrom')} hint={t('allowFromHint')}>
                  <Input value={cfg('allowFrom')} onChange={(e) => setCfg('allowFrom', e.target.value)} placeholder="+49,+43" />
                </Field>
                <Field label={t('agiRecordDir')} hint="/var/spool/asterisk/recording">
                  <Input value={cfg('recordDir')} onChange={(e) => setCfg('recordDir', e.target.value)} />
                </Field>
              </div>
              <Badge variant="outline" className="bg-muted text-muted-foreground border-input">{t('agiDialplanHint')}</Badge>
            </div>
          )}

          {/* CEL mapping — webhook only */}
          {type === 'webhook' && (
            <Field label={t('mappingCel')} hint={t('mappingCelHint')}>
              <KVEditor value={mapping} onChange={setMapping} keyPlaceholder="severity" valuePlaceholder='body.level' />
            </Field>
          )}

          <Field label={t('labels')} hint={t('labelsMergedHint')}>
            <KVEditor value={labels} onChange={setLabels} />
          </Field>

          <Field label={t('moreSettings')} hint={t('moreSettingsHint')}>
            <KVEditor value={extra} onChange={setExtra} />
          </Field>

          {isHTTP && type === 'alertmanager' && (
            <Badge variant="outline" className="bg-muted text-muted-foreground border-input">{t('alertmanagerWebhookInfo')}</Badge>
          )}

          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name} />
        </form>
      </DialogContent>
    </Dialog>
  )
}
