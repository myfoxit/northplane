// Event sources (SPEC §7.5/§9.2): every ingress adapter is an EventSource,
// ETag-versioned via resourceApi<EventSourceDef>('event-sources'). The form
// has a common header (name, type, enabled, rateLimit/burst, labels) plus a
// type-specific Config block: webhook/alertmanager expose auth + ingest URL,
// snmp-trap exposes the listener/community/severity and SNMPv3 credentials,
// imap/email expose the mailbox connection. webhook sources additionally get
// a CEL Mapping editor. Whatever the typed block omits stays reachable via a
// "Weitere Einstellungen" KVEditor.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { resourceApi } from '../../api'
import type { EventSourceDef, Severity } from '../../types'
import { Button, Table, Empty, Dialog, Input, Badge, Spinner } from '../ui'
import { Field, FormError, SubmitRow, useSave, DeleteButton, Select, Toggle, KVEditor, DurationInput } from '../forms'
import { t } from '../../i18n'
import { TypeBadge, StatusBadge, TableActions, RowActions, secretHint } from './common'

const sourcesApi = resourceApi<EventSourceDef>('event-sources')

const SOURCE_TYPES = ['webhook', 'alertmanager', 'snmp-trap', 'imap', 'email'] as const
const AUTH_MODES = ['none', 'token', 'hmac', 'basic'] as const
const SEVERITIES: Severity[] = ['critical', 'warning', 'info', 'ok']

export function SourcesTab() {
  const { data, isLoading } = useQuery({ queryKey: sourcesApi.queryKey, queryFn: sourcesApi.list })
  const [editing, setEditing] = useState<EventSourceDef | 'new' | null>(null)
  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setEditing('new')} label={t('create')} />
      <Table head={[t('type'), t('name'), t('status'), 'Ingest', '']}>
        {(data ?? []).map((s) => (
          <tr key={s.name}>
            <td className="px-3 py-2 w-28"><TypeBadge>{s.type}</TypeBadge></td>
            <td className="px-3 py-2 text-foreground">{s.name}</td>
            <td className="px-3 py-2">{s.enabled ? <StatusBadge kind="enabled" /> : <StatusBadge kind="disabled" />}</td>
            <td className="px-3 py-2 text-xs text-muted-foreground font-mono truncate max-w-56">
              {(s.type === 'webhook' || s.type === 'alertmanager') ? `/api/v1/ingest/${s.name}` : '—'}
            </td>
            <td className="px-3 py-2">
              <RowActions>
                <Button size="sm" variant="ghost" onClick={() => setEditing(s)}>{t('edit')}</Button>
                <SourceDelete source={s} />
              </RowActions>
            </td>
          </tr>
        ))}
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}
      {editing && (
        <SourceDialog name={editing === 'new' ? null : editing.name} onClose={() => setEditing(null)} />
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

function SourceDialog({ name, onClose }: { name: string | null; onClose: () => void }) {
  const isNew = !name
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...sourcesApi.queryKey, name],
    queryFn: () => sourcesApi.get(name!),
    enabled: !isNew,
  })
  if (!isNew && isLoading) {
    return <Dialog open onClose={onClose} title={t('loading')} size="xl"><Spinner /></Dialog>
  }
  return (
    <SourceForm
      doc={loaded?.data ?? { name: '', type: 'webhook', enabled: true, authMode: 'none', config: {} }}
      etag={loaded?.etag ?? 0}
      isNew={isNew}
      onClose={onClose}
    />
  )
}

// Known config keys per source type; the rest fall into the KVEditor.
const SNMP_KEYS = ['listen', 'community', 'severity', 'v3User', 'v3AuthProto', 'v3AuthSecretRef', 'v3PrivProto', 'v3PrivSecretRef']
const MAIL_KEYS = ['host', 'port', 'tls', 'username', 'passwordSecretRef', 'folder', 'pollInterval', 'markSeen', 'severity']

function SourceForm({ doc, etag, isNew, onClose }: {
  doc: EventSourceDef; etag: number; isNew: boolean; onClose: () => void
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

  const knownKeys = type === 'snmp-trap' ? SNMP_KEYS : (type === 'imap' || type === 'email') ? MAIL_KEYS : []
  const knownSet = new Set(knownKeys)
  const extra = Object.fromEntries(Object.entries(config).filter(([k]) => !knownSet.has(k)))
  const setExtra = (next: Record<string, string>) =>
    setConfig({ ...Object.fromEntries(Object.entries(config).filter(([k]) => knownSet.has(k))), ...next })

  const isHTTP = type === 'webhook' || type === 'alertmanager'
  const isMail = type === 'imap' || type === 'email'

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
    <Dialog open onClose={onClose} title={isNew ? t('create') : `${t('edit')}: ${doc.name}`} size="xl">
      <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
        <div className="grid grid-cols-2 gap-2">
          <Field label={t('name')} required>
            <Input value={name} onChange={(e) => setName(e.target.value)} required disabled={!isNew} />
          </Field>
          <Field label={t('type')}>
            <Select value={type} onChange={(e) => setType(e.target.value)}>
              {SOURCE_TYPES.map((ty) => <option key={ty} value={ty}>{ty}</option>)}
            </Select>
          </Field>
        </div>
        <Toggle checked={enabled} onChange={setEnabled} label={t('enabled')} />

        <div className="grid grid-cols-2 gap-2">
          <Field label="Rate-Limit (Events/s)" hint="0/leer = Standard">
            <Input type="number" step="any" value={rateLimit} onChange={(e) => setRateLimit(e.target.value)} />
          </Field>
          <Field label="Burst">
            <Input type="number" value={burst} onChange={(e) => setBurst(e.target.value)} />
          </Field>
        </div>

        {/* HTTP ingress (webhook / alertmanager) */}
        {isHTTP && (
          <div className="border border-border rounded-lg p-3 space-y-2">
            <div className="text-xs text-muted-foreground font-medium">Ingress ({type})</div>
            <div className="grid grid-cols-2 gap-2">
              <Field label="Auth-Modus">
                <Select value={authMode} onChange={(e) => setAuthMode(e.target.value as EventSourceDef['authMode'])}>
                  {AUTH_MODES.map((m) => <option key={m} value={m}>{m}</option>)}
                </Select>
              </Field>
              <Field label="Secret-Referenz" hint={secretHint}>
                <Input value={secretRef} onChange={(e) => setSecretRef(e.target.value)} />
              </Field>
            </div>
            <div className="text-[11px] text-muted-foreground">
              Ingest-URL: <code className="text-muted-foreground">/api/v1/ingest/{name || '<name>'}</code>
            </div>
          </div>
        )}

        {/* SNMP trap receiver */}
        {type === 'snmp-trap' && (
          <div className="border border-border rounded-lg p-3 space-y-2">
            <div className="text-xs text-muted-foreground font-medium">SNMP-Trap</div>
            <div className="grid grid-cols-2 gap-2">
              <Field label="Listen" hint="udp://:9162"><Input value={cfg('listen')} onChange={(e) => setCfg('listen', e.target.value)} placeholder="udp://:9162" /></Field>
              <Field label="Community (v1/v2c)"><Input value={cfg('community')} onChange={(e) => setCfg('community', e.target.value)} /></Field>
              <Field label="Severity">
                <Select value={cfg('severity')} onChange={(e) => setCfg('severity', e.target.value)}>
                  <option value="">(Standard)</option>
                  {SEVERITIES.map((s) => <option key={s} value={s}>{s}</option>)}
                </Select>
              </Field>
            </div>
            <div className="text-xs text-muted-foreground pt-1">SNMPv3</div>
            <div className="grid grid-cols-2 gap-2">
              <Field label="v3 User"><Input value={cfg('v3User')} onChange={(e) => setCfg('v3User', e.target.value)} /></Field>
              <Field label="Auth-Protokoll">
                <Select value={cfg('v3AuthProto')} onChange={(e) => setCfg('v3AuthProto', e.target.value)}>
                  <option value="">—</option>
                  {['MD5', 'SHA', 'SHA256'].map((x) => <option key={x} value={x}>{x}</option>)}
                </Select>
              </Field>
              <Field label="Auth-Secret-Ref" hint={secretHint}><Input value={cfg('v3AuthSecretRef')} onChange={(e) => setCfg('v3AuthSecretRef', e.target.value)} /></Field>
              <Field label="Priv-Protokoll">
                <Select value={cfg('v3PrivProto')} onChange={(e) => setCfg('v3PrivProto', e.target.value)}>
                  <option value="">—</option>
                  {['DES', 'AES', 'AES256'].map((x) => <option key={x} value={x}>{x}</option>)}
                </Select>
              </Field>
              <Field label="Priv-Secret-Ref" hint={secretHint}><Input value={cfg('v3PrivSecretRef')} onChange={(e) => setCfg('v3PrivSecretRef', e.target.value)} /></Field>
            </div>
          </div>
        )}

        {/* IMAP / e-mail mailbox poller */}
        {isMail && (
          <div className="border border-border rounded-lg p-3 space-y-2">
            <div className="text-xs text-muted-foreground font-medium">Postfach ({type})</div>
            <div className="grid grid-cols-2 gap-2">
              <Field label="Host"><Input value={cfg('host')} onChange={(e) => setCfg('host', e.target.value)} /></Field>
              <Field label="Port"><Input type="number" value={cfg('port')} onChange={(e) => setCfg('port', e.target.value)} /></Field>
              <Field label="TLS">
                <Select value={cfg('tls')} onChange={(e) => setCfg('tls', e.target.value)}>
                  <option value="">(Standard)</option>
                  <option value="on">on</option>
                  <option value="off">off</option>
                </Select>
              </Field>
              <Field label="Benutzername"><Input value={cfg('username')} onChange={(e) => setCfg('username', e.target.value)} /></Field>
              <Field label="Passwort-Secret-Ref" hint={secretHint}><Input value={cfg('passwordSecretRef')} onChange={(e) => setCfg('passwordSecretRef', e.target.value)} /></Field>
              <Field label="Ordner"><Input value={cfg('folder')} onChange={(e) => setCfg('folder', e.target.value)} placeholder="INBOX" /></Field>
              <Field label="Poll-Intervall">
                <DurationInput value={cfg('pollInterval')} onChange={(v) => setCfg('pollInterval', v)} placeholder="60s" />
              </Field>
              <Field label="Severity">
                <Select value={cfg('severity')} onChange={(e) => setCfg('severity', e.target.value)}>
                  <option value="">(Standard)</option>
                  {SEVERITIES.map((s) => <option key={s} value={s}>{s}</option>)}
                </Select>
              </Field>
            </div>
            <Toggle checked={cfg('markSeen') === 'true'} onChange={(v) => setCfg('markSeen', v ? 'true' : '')} label="Gelesen markieren (markSeen)" />
          </div>
        )}

        {/* CEL mapping — webhook only */}
        {type === 'webhook' && (
          <Field label="Mapping (CEL)" hint="NormEvent-Felder aus dem Roh-Payload">
            <KVEditor value={mapping} onChange={setMapping} keyPlaceholder="severity" valuePlaceholder='body.level' />
          </Field>
        )}

        <Field label={t('labels')} hint="werden in jedes Event gemerged">
          <KVEditor value={labels} onChange={setLabels} />
        </Field>

        <Field label="Weitere Einstellungen" hint="zusätzliche Config-Schlüssel">
          <KVEditor value={extra} onChange={setExtra} />
        </Field>

        {isHTTP && type === 'alertmanager' && (
          <Badge className="bg-muted text-muted-foreground border-input">Alertmanager-Webhook empfängt Prometheus-Alerts</Badge>
        )}

        <FormError error={save.error} />
        <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name} />
      </form>
    </Dialog>
  )
}
