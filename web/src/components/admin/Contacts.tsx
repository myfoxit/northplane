// Contacts: the system's main PII class (SPEC §13.4). ETag-versioned via
// resourceApi<Contact>('contacts'). Beyond name/email/phone/timezone the
// form carries the notification-preferences editor: an ordered list of
// {profile, period?, channels[] (ordered), severity?} rows mirroring
// model.ChannelPreference (F-04.08).
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { resourceApi } from '../../api'
import type { Contact, ChannelPreference, ChannelType, Severity } from '../../types'
import { Button, Table, Empty, Dialog, Input, Badge } from '../ui'
import { Field, FormError, SubmitRow, useSave, DeleteButton, Select } from '../forms'
import { t } from '../../i18n'
import { TableActions, RowActions } from './common'

const contactsApi = resourceApi<Contact>('contacts')

const CHANNEL_TYPES: ChannelType[] = ['email', 'webhook', 'slack', 'teams', 'ntfy', 'sms', 'push', 'voice']
const SEVERITIES: Severity[] = ['critical', 'warning', 'info', 'ok']

export function ContactsTab() {
  const { data, isLoading } = useQuery({ queryKey: contactsApi.queryKey, queryFn: contactsApi.list })
  const [editing, setEditing] = useState<Contact | 'new' | null>(null)
  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setEditing('new')} label={t('create')} />
      <Table head={[t('name'), t('email'), t('phone'), t('timezone'), 'Profile', '']}>
        {(data ?? []).map((c) => (
          <tr key={c.name}>
            <td className="px-3 py-2 text-slate-200">{c.name}</td>
            <td className="px-3 py-2 text-xs text-slate-400">{c.email || '—'}</td>
            <td className="px-3 py-2 text-xs text-slate-400 font-mono">{c.phone || '—'}</td>
            <td className="px-3 py-2 text-xs text-slate-400">{c.timeZone || '—'}</td>
            <td className="px-3 py-2 text-xs text-slate-500">{c.preferences?.length ?? 0}</td>
            <td className="px-3 py-2">
              <RowActions>
                <Button size="sm" variant="ghost" onClick={() => setEditing(c)}>{t('edit')}</Button>
                <ContactDelete contact={c} />
              </RowActions>
            </td>
          </tr>
        ))}
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}
      {editing && (
        <ContactDialog name={editing === 'new' ? null : editing.name} onClose={() => setEditing(null)} />
      )}
    </div>
  )
}

function ContactDelete({ contact }: { contact: Contact }) {
  const save = useSave(() => contactsApi.remove(contact.name), { invalidate: [[...contactsApi.queryKey]] })
  return (
    <>
      <DeleteButton onDelete={() => save.mutate(undefined)} />
      {save.isError && <FormError error={save.error} />}
    </>
  )
}

function ContactDialog({ name, onClose }: { name: string | null; onClose: () => void }) {
  const isNew = !name
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...contactsApi.queryKey, name],
    queryFn: () => contactsApi.get(name!),
    enabled: !isNew,
  })
  if (!isNew && isLoading) {
    return <Dialog open onClose={onClose} title={t('loading')} size="lg"><div className="text-slate-500 text-sm">…</div></Dialog>
  }
  return (
    <ContactForm
      doc={loaded?.data ?? { name: '', preferences: [] }}
      etag={loaded?.etag ?? 0}
      isNew={isNew}
      onClose={onClose}
    />
  )
}

function ContactForm({ doc, etag, isNew, onClose }: {
  doc: Contact; etag: number; isNew: boolean; onClose: () => void
}) {
  const [name, setName] = useState(doc.name)
  const [email, setEmail] = useState(doc.email ?? '')
  const [phone, setPhone] = useState(doc.phone ?? '')
  const [timeZone, setTimeZone] = useState(doc.timeZone ?? '')
  const [prefs, setPrefs] = useState<ChannelPreference[]>(doc.preferences ?? [])

  const build = (): Contact => ({
    ...doc, name,
    email: email || undefined, phone: phone || undefined, timeZone: timeZone || undefined,
    preferences: prefs.length ? prefs : undefined,
  })
  const save = useSave(
    () => isNew ? contactsApi.create(build()) : contactsApi.update(doc.name, build(), etag),
    { invalidate: [[...contactsApi.queryKey]], onDone: onClose },
  )
  return (
    <Dialog open onClose={onClose} title={isNew ? t('create') : `${t('edit')}: ${doc.name}`} size="lg">
      <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
        <div className="grid grid-cols-2 gap-2">
          <Field label={t('name')} required><Input value={name} onChange={(e) => setName(e.target.value)} required disabled={!isNew} /></Field>
          <Field label={t('email')}><Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} /></Field>
          <Field label={t('phone')} hint="E.164, z.B. +491701234567"><Input value={phone} onChange={(e) => setPhone(e.target.value)} /></Field>
          <Field label={t('timezone')} hint="z.B. Europe/Berlin"><Input value={timeZone} onChange={(e) => setTimeZone(e.target.value)} /></Field>
        </div>
        <div>
          <div className="text-xs text-slate-400 font-medium mb-1">Benachrichtigungs-Präferenzen</div>
          <PreferencesEditor value={prefs} onChange={setPrefs} />
        </div>
        <FormError error={save.error} />
        <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name} />
      </form>
    </Dialog>
  )
}

// Editor for the ordered ChannelPreference rows.
function PreferencesEditor({ value, onChange }: {
  value: ChannelPreference[]; onChange: (v: ChannelPreference[]) => void
}) {
  const update = (i: number, patch: Partial<ChannelPreference>) =>
    onChange(value.map((p, j) => (j === i ? { ...p, ...patch } : p)))
  return (
    <div className="space-y-2">
      {value.map((p, i) => (
        <div key={i} className="border border-slate-800 rounded-lg p-2 space-y-2 bg-slate-900/40">
          <div className="grid grid-cols-3 gap-2">
            <Field label="Profil"><Input value={p.profile} onChange={(e) => update(i, { profile: e.target.value })} placeholder="default" /></Field>
            <Field label="Zeitperiode"><Input value={p.period ?? ''} onChange={(e) => update(i, { period: e.target.value || undefined })} placeholder="(immer)" /></Field>
            <Field label="Min. Severity">
              <Select value={p.severity ?? ''} onChange={(e) => update(i, { severity: (e.target.value || undefined) as Severity | undefined })}>
                <option value="">(alle)</option>
                {SEVERITIES.map((s) => <option key={s} value={s}>{s}</option>)}
              </Select>
            </Field>
          </div>
          <Field label="Kanäle (Reihenfolge = Priorität)">
            <ChannelTypePicker value={p.channels} onChange={(ch) => update(i, { channels: ch })} />
          </Field>
          <div className="flex justify-end">
            <Button size="sm" variant="ghost" type="button" onClick={() => onChange(value.filter((_, j) => j !== i))}>
              {t('remove')}
            </Button>
          </div>
        </div>
      ))}
      <Button size="sm" type="button" onClick={() => onChange([...value, { profile: 'default', channels: [] }])}>
        {t('add')}
      </Button>
    </div>
  )
}

// Ordered multi-select for channel types: click to add (appended in order),
// click a chip to remove. Order is meaningful (priority).
export function ChannelTypePicker({ value, onChange }: {
  value: ChannelType[]; onChange: (v: ChannelType[]) => void
}) {
  const available = CHANNEL_TYPES.filter((c) => !value.includes(c))
  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap gap-1 min-h-6">
        {value.map((c, i) => (
          <button key={c} type="button" onClick={() => onChange(value.filter((x) => x !== c))}
            className="inline-flex items-center gap-1 text-xs bg-blue-600/20 text-blue-300 border border-blue-700/50 rounded-md px-2 py-1 cursor-pointer">
            <span className="text-slate-500">{i + 1}.</span> {c} <span className="text-slate-500">✕</span>
          </button>
        ))}
        {value.length === 0 && <span className="text-xs text-slate-500 py-1">keine</span>}
      </div>
      {available.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {available.map((c) => (
            <button key={c} type="button" onClick={() => onChange([...value, c])}>
              <Badge className="bg-slate-800 text-slate-400 border-slate-700 hover:text-slate-200 cursor-pointer">+ {c}</Badge>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
