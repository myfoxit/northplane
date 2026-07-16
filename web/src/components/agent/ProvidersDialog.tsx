// Provider connections manager: the user's own LLM accounts (keys are
// sealed server-side; only a hint ever comes back). Admin-shared
// connections appear read-only here — they are managed in Admin → KI.
import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { CheckCircle2, KeyRound, Plug, XCircle } from 'lucide-react'
import { del, post, put, queryClient, APIError } from '../../api'
import type { AIConnection } from '../../types'
import { connectionsQuery, providersQuery } from './queries'
import { Button } from '@/components/ui/button'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import { Empty, Field, FormError, SubmitRow, DeleteButton } from '@/components/kit'
import { t } from '../../i18n'

interface FormState {
  id?: string
  name: string
  provider: string
  endpoint: string
  apiKey: string
  defaultModel: string
}

const emptyForm: FormState = { name: '', provider: 'anthropic', endpoint: '', apiKey: '', defaultModel: '' }

// ConnectionForm covers create + edit for personal connections; the
// admin tab reuses it with shared=true.
export function ConnectionForm({ existing, shared, onDone }: {
  existing?: AIConnection
  shared?: boolean
  onDone: () => void
}) {
  const providers = useQuery(providersQuery)
  const [form, setForm] = useState<FormState>(existing ? {
    id: existing.id, name: existing.name, provider: existing.provider,
    endpoint: existing.endpoint ?? '', apiKey: '', defaultModel: existing.defaultModel ?? '',
  } : emptyForm)
  const [error, setError] = useState<unknown>(null)

  const ptype = providers.data?.items?.find((p) => p.id === form.provider)

  const save = useMutation({
    mutationFn: () => {
      const body: Record<string, unknown> = {
        name: form.name, provider: form.provider, endpoint: form.endpoint,
        defaultModel: form.defaultModel, shared: !!shared,
      }
      // apiKey semantics: omitted = keep stored key (edit), set = replace.
      if (form.apiKey !== '' || !existing) body.apiKey = form.apiKey
      return existing
        ? put<AIConnection>(`/ai/connections/${existing.id}`, body, existing.version)
        : post<AIConnection>('/ai/connections', body)
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ai-connections'] })
      onDone()
    },
    onError: setError,
  })

  return (
    <form
      className="space-y-3"
      onSubmit={(e) => { e.preventDefault(); setError(null); save.mutate() }}
    >
      <Field label={t('name')} required>
        <Input value={form.name} required
          onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
          placeholder={t('agentConnNamePlaceholder')} />
      </Field>
      {!existing && (
        <Field label={t('agentProvider')}>
          <Select value={form.provider}
            onValueChange={(v) => setForm((f) => ({ ...f, provider: v, endpoint: '' }))}>
            <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
            <SelectContent>
              {providers.data?.items?.map((p) => (
                <SelectItem key={p.id} value={p.id}>{p.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
      <Field label="API-Key" hint={ptype?.keyUrl}>
        <Input type="password" value={form.apiKey} autoComplete="off"
          onChange={(e) => setForm((f) => ({ ...f, apiKey: e.target.value }))}
          placeholder={existing?.hasKey ? `${t('agentKeyStored')} ${existing.keyHint ?? ''}` : 'sk-…'} />
      </Field>
      <Field label="Endpoint" hint={ptype?.endpoint || undefined}>
        <Input value={form.endpoint}
          onChange={(e) => setForm((f) => ({ ...f, endpoint: e.target.value }))}
          placeholder={ptype?.endpoint || 'https://…'} />
      </Field>
      <Field label={t('agentDefaultModel')}>
        <Input value={form.defaultModel}
          onChange={(e) => setForm((f) => ({ ...f, defaultModel: e.target.value }))}
          placeholder={ptype?.models?.[0]?.id ?? ''} />
      </Field>
      <FormError error={error} />
      <SubmitRow onCancel={onDone} saving={save.isPending} />
    </form>
  )
}

function TestButton({ conn }: { conn: AIConnection }) {
  const test = useMutation({
    mutationFn: () => post<{ status: string; models: number }>(`/ai/connections/${conn.id}:test`),
  })
  return (
    <span className="flex items-center gap-1.5">
      <Button size="xs" variant="outline" onClick={() => test.mutate()} disabled={test.isPending}>
        <Plug size={12} /> {t('test')}
      </Button>
      {test.isSuccess && (
        <span className="text-success text-xs flex items-center gap-1">
          <CheckCircle2 size={12} /> {test.data.models} {t('agentModels')}
        </span>
      )}
      {test.isError && (
        <span className="text-danger text-xs flex items-center gap-1" title={test.error instanceof APIError ? test.error.detail : String(test.error)}>
          <XCircle size={12} /> {t('agentTestFailed')}
        </span>
      )}
    </span>
  )
}

export function ProvidersDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const conns = useQuery(connectionsQuery)
  const [editing, setEditing] = useState<AIConnection | 'new' | null>(null)

  const remove = useMutation({
    mutationFn: (id: string) => del(`/ai/connections/${id}`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['ai-connections'] }),
  })

  const items = conns.data?.items ?? []
  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) { setEditing(null); onClose() } }}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <KeyRound size={15} /> {t('agentProviders')}
          </DialogTitle>
        </DialogHeader>
        {editing ? (
          <ConnectionForm existing={editing === 'new' ? undefined : editing} onDone={() => setEditing(null)} />
        ) : (
          <div className="space-y-2">
            {items.length === 0 && <Empty text={t('agentNoConnections')} />}
            {items.map((c) => (
              <div key={c.id} className="flex items-center gap-2 bg-card/50 border border-border rounded-lg px-3 py-2 text-sm">
                <div className="min-w-0 flex-1">
                  <div className="font-medium text-foreground/90 truncate">
                    {c.name}
                    {c.shared && (
                      <span className="ml-2 text-[10px] rounded border border-border px-1 py-px text-muted-foreground">
                        {t('agentShared')}
                      </span>
                    )}
                  </div>
                  <div className="text-xs text-muted-foreground truncate">
                    {c.provider}{c.hasKey ? ` · ${c.keyHint}` : ''}{c.defaultModel ? ` · ${c.defaultModel}` : ''}
                  </div>
                </div>
                <TestButton conn={c} />
                {!c.shared && (
                  <>
                    <Button size="xs" variant="ghost" onClick={() => setEditing(c)}>{t('edit')}</Button>
                    <DeleteButton onDelete={() => remove.mutate(c.id)} />
                  </>
                )}
              </div>
            ))}
            <div className="pt-1">
              <Button size="sm" onClick={() => setEditing('new')} data-testid="agent-add-connection">
                {t('agentAddConnection')}
              </Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
