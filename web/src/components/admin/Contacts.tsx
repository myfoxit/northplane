// Contacts: the system's main PII class (SPEC §13.4). ETag-versioned via
// resourceApi<Contact>('contacts'). Beyond name/email/phone/timezone the
// form carries the notification-preferences editor: an ordered list of
// {profile, period?, channels[] (ordered), severity?} rows mirroring
// model.ChannelPreference (F-04.08).
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { X } from 'lucide-react'
import { resourceApi } from '../../api'
import type { Contact, ChannelPreference, ChannelType, Severity } from '../../types'
import { Button } from '@/components/ui/button'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@/components/ui/table'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from '@/components/ui/select'
import { Empty, Spinner, Field, FormError, SubmitRow, useSave, DeleteButton, DuplicateButton } from '@/components/kit'
import { duplicateDoc } from '@/lib/duplicate'
import { t } from '../../i18n'
import { TableActions, RowActions } from './common'

// Radix SelectItem value cannot be "" — sentinel for the "(alle)" min-severity.
const ALL_SEVERITIES = '__all__'

const contactsApi = resourceApi<Contact>('contacts')

const CHANNEL_TYPES: ChannelType[] = ['email', 'webhook', 'slack', 'teams', 'ntfy', 'sms', 'push', 'voice']
const SEVERITIES: Severity[] = ['critical', 'warning', 'info', 'ok']

export function ContactsTab() {
  const { data, isLoading } = useQuery({ queryKey: contactsApi.queryKey, queryFn: contactsApi.list })
  const [editing, setEditing] = useState<Contact | 'new' | null>(null)
  const [copying, setCopying] = useState<Contact | null>(null)
  return (
    <div className="space-y-4">
      <TableActions onCreate={() => setEditing('new')} label={t('create')} />
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('name')}</TableHead>
            <TableHead>{t('email')}</TableHead>
            <TableHead>{t('phone')}</TableHead>
            <TableHead>{t('timezone')}</TableHead>
            <TableHead>{t('profiles')}</TableHead>
            <TableHead></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(data ?? []).map((c) => (
            <TableRow key={c.name}>
              <TableCell className="px-3 py-2 text-foreground">{c.name}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground">{c.email || '—'}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground font-mono">{c.phone || '—'}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground">{c.timeZone || '—'}</TableCell>
              <TableCell className="px-3 py-2 text-xs text-muted-foreground">{c.preferences?.length ?? 0}</TableCell>
              <TableCell className="px-3 py-2">
                <RowActions>
                  <Button size="sm" variant="ghost" onClick={() => setEditing(c)}>{t('edit')}</Button>
                  <DuplicateButton onClick={() => setCopying(c)} />
                  <ContactDelete contact={c} />
                </RowActions>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {!isLoading && (data?.length ?? 0) === 0 && <Empty text={t('empty')} />}
      {editing && (
        <ContactDialog name={editing === 'new' ? null : editing.name} onClose={() => setEditing(null)} />
      )}
      {copying && (
        <ContactDialog name={copying.name} copy existing={(data ?? []).map((c) => c.name)}
          onClose={() => setCopying(null)} />
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

// ContactDialog: `name` null → create; set → edit; set + `copy` → create a
// duplicate seeded from that contact (envelope stripped, fresh name).
function ContactDialog({ name, copy, existing, onClose }: {
  name: string | null; copy?: boolean; existing?: string[]; onClose: () => void
}) {
  const isNew = !name || !!copy
  const { data: loaded, isLoading } = useQuery({
    queryKey: [...contactsApi.queryKey, name],
    queryFn: () => contactsApi.get(name!),
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
    <ContactForm
      doc={copy && src ? duplicateDoc(src, existing) : (src ?? { name: '', preferences: [] })}
      etag={copy ? 0 : (loaded?.etag ?? 0)}
      isNew={isNew}
      copyOf={copy ? name ?? undefined : undefined}
      onClose={onClose}
    />
  )
}

function ContactForm({ doc, etag, isNew, copyOf, onClose }: {
  doc: Contact; etag: number; isNew: boolean; copyOf?: string; onClose: () => void
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
    <Dialog open onOpenChange={(o) => { if (!o) onClose() }}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{copyOf ? `${t('duplicate')}: ${copyOf}` : isNew ? t('create') : `${t('edit')}: ${doc.name}`}</DialogTitle>
        </DialogHeader>
        <form onSubmit={(e) => { e.preventDefault(); save.mutate(undefined) }} className="space-y-3">
          <div className="grid grid-cols-2 gap-2">
            <Field label={t('name')} required><Input value={name} onChange={(e) => setName(e.target.value)} required disabled={!isNew} /></Field>
            <Field label={t('email')}><Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} /></Field>
            <Field label={t('phone')} hint={t('phoneHint')}><Input value={phone} onChange={(e) => setPhone(e.target.value)} /></Field>
            <Field label={t('timezone')} hint={t('timezoneHint')}><Input value={timeZone} onChange={(e) => setTimeZone(e.target.value)} /></Field>
          </div>
          <div>
            <div className="text-xs text-muted-foreground font-medium mb-1">{t('notificationPreferences')}</div>
            <PreferencesEditor value={prefs} onChange={setPrefs} />
          </div>
          <FormError error={save.error} />
          <SubmitRow onCancel={onClose} saving={save.isPending} disabled={!name} />
        </form>
      </DialogContent>
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
        <div key={i} className="border border-border rounded-lg p-2 space-y-2 bg-card/40">
          <div className="grid grid-cols-3 gap-2">
            <Field label={t('profile')}><Input value={p.profile} onChange={(e) => update(i, { profile: e.target.value })} placeholder="default" /></Field>
            <Field label={t('timePeriodField')}><Input value={p.period ?? ''} onChange={(e) => update(i, { period: e.target.value || undefined })} placeholder={t('alwaysParen')} /></Field>
            <Field label={t('minSeverity')}>
              <Select
                value={p.severity ?? ALL_SEVERITIES}
                onValueChange={(v) => update(i, { severity: v === ALL_SEVERITIES ? undefined : (v as Severity) })}
              >
                <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL_SEVERITIES}>{t('allParen')}</SelectItem>
                  {SEVERITIES.map((s) => <SelectItem key={s} value={s}>{s}</SelectItem>)}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <Field label={t('channelsPriority')}>
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
            aria-label={t('remove')}
            className="inline-flex items-center gap-1 text-xs bg-primary/20 text-primary border border-primary/50 rounded-md px-2 py-1 cursor-pointer">
            <span className="text-muted-foreground">{i + 1}.</span> {c} <X size={13} />
          </button>
        ))}
        {value.length === 0 && <span className="text-xs text-muted-foreground py-1">{t('none')}</span>}
      </div>
      {available.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {available.map((c) => (
            <button key={c} type="button" onClick={() => onChange([...value, c])}>
              <Badge variant="outline" className="bg-muted text-muted-foreground border-input hover:text-foreground cursor-pointer">+ {c}</Badge>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
