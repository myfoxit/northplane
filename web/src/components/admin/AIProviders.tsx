// Admin → KI-Provider: tenant-wide shared connections plus the agent
// tool policy ("what may the agent do"). Policy can only narrow the
// built-in gates; auto-approve is the one deliberate widening and
// stays an explicit admin decision (SPEC §10.1).
import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { del, get, put, queryClient, type ListResponse } from '../../api'
import type { AIConnection, AIPolicy, AIToolInfo } from '../../types'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import { Empty, ErrorState, FormError, Spinner, DeleteButton } from '@/components/kit'
import { ConnectionForm } from '../agent/ProvidersDialog'
import { t } from '../../i18n'

export function AIProvidersTab() {
  return (
    <div className="space-y-4">
      <SharedConnections />
      <PolicyCard />
    </div>
  )
}

function SharedConnections() {
  const conns = useQuery({
    queryKey: ['ai-connections'],
    queryFn: () => get<ListResponse<AIConnection>>('/ai/connections'),
  })
  const [editing, setEditing] = useState<AIConnection | 'new' | null>(null)
  const remove = useMutation({
    mutationFn: (id: string) => del(`/ai/connections/${id}?shared=true`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['ai-connections'] }),
  })
  const shared = (conns.data?.items ?? []).filter((c) => c.shared)
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('agentProviders')}</CardTitle>
        <CardDescription>{t('agentSharedConnections')}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {conns.isLoading && <Spinner />}
        {conns.isError && <ErrorState error={conns.error} onRetry={conns.refetch} />}
        {editing ? (
          <ConnectionForm existing={editing === 'new' ? undefined : editing} shared
            onDone={() => setEditing(null)} />
        ) : (
          <>
            {shared.length === 0 && <Empty text={t('agentNoConnections')} />}
            {shared.map((c) => (
              <div key={c.id} className="flex items-center gap-2 bg-card/50 border border-border rounded-lg px-3 py-2 text-sm">
                <div className="min-w-0 flex-1">
                  <div className="font-medium text-foreground/90">{c.name}</div>
                  <div className="text-xs text-muted-foreground">
                    {c.provider}{c.hasKey ? ` · ${c.keyHint}` : ''}{c.defaultModel ? ` · ${c.defaultModel}` : ''}
                  </div>
                </div>
                <Button size="xs" variant="ghost" onClick={() => setEditing(c)}>{t('edit')}</Button>
                <DeleteButton onDelete={() => remove.mutate(c.id)} />
              </div>
            ))}
            <Button size="sm" onClick={() => setEditing('new')} data-testid="admin-add-shared-connection">
              {t('agentAddConnection')}
            </Button>
          </>
        )}
      </CardContent>
    </Card>
  )
}

function PolicyCard() {
  const tools = useQuery({
    queryKey: ['ai-tools'],
    queryFn: () => get<ListResponse<AIToolInfo>>('/ai/tools'),
  })
  const policy = useQuery({
    queryKey: ['ai-policy'],
    queryFn: () => get<AIPolicy>('/ai/policy'),
  })
  // draft holds unsaved edits; reads fall through to the stored policy.
  const [draft, setDraft] = useState<AIPolicy | null>(null)
  const [error, setError] = useState<unknown>(null)

  const save = useMutation({
    mutationFn: (p: AIPolicy) => put<AIPolicy>('/ai/policy', p, p.version ?? 0),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['ai-policy'] })
      queryClient.invalidateQueries({ queryKey: ['ai-tools'] })
      setDraft(null)
    },
    onError: setError,
  })

  const p = draft ?? policy.data ?? {}
  const disabled = new Set(p.disabled ?? [])
  const auto = new Set(p.autoApprove ?? [])
  const toggle = (set: Set<string>, name: string, on: boolean) => {
    const next = new Set(set)
    if (on) next.add(name)
    else next.delete(name)
    return [...next].sort()
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('agentPolicy')}</CardTitle>
        <CardDescription>{t('agentPolicyHint')}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {(tools.isLoading || policy.isLoading) && <Spinner />}
        {tools.isError && <ErrorState error={tools.error} onRetry={tools.refetch} />}
        {tools.data?.items && (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('name')}</TableHead>
                  <TableHead className="w-24 text-center">{t('agentToolActive')}</TableHead>
                  <TableHead className="w-28 text-center">{t('agentAutoApprove')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {tools.data.items.map((tool) => (
                  <TableRow key={tool.name}>
                    <TableCell>
                      <div className="font-mono text-xs">{tool.name}
                        {tool.mutating && (
                          <span className="ml-2 rounded border border-warning/50 text-warning px-1 py-px text-[10px]">
                            {t('agentMutating')}
                          </span>
                        )}
                      </div>
                      <div className="text-xs text-muted-foreground max-w-xl truncate">{tool.description}</div>
                    </TableCell>
                    <TableCell className="text-center">
                      <Switch checked={!disabled.has(tool.name)} aria-label={`${tool.name} ${t('agentToolActive')}`}
                        onCheckedChange={(on) => setDraft({ ...p, disabled: toggle(disabled, tool.name, !on) })} />
                    </TableCell>
                    <TableCell className="text-center">
                      {tool.mutating && !tool.autoOk && (
                        <Switch checked={auto.has(tool.name)} aria-label={`${tool.name} ${t('agentAutoApprove')}`}
                          onCheckedChange={(on) => setDraft({ ...p, autoApprove: toggle(auto, tool.name, on) })} />
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
        <div className="flex items-center gap-3">
          <label className="text-sm text-muted-foreground">{t('agentMaxRounds')}</label>
          <Input type="number" min={0} max={24} className="w-24"
            value={p.maxRounds ?? 0}
            onChange={(e) => setDraft({ ...p, maxRounds: parseInt(e.target.value, 10) || 0 })} />
        </div>
        <FormError error={error} />
        <div>
          <Button size="sm" disabled={draft === null || save.isPending}
            onClick={() => { setError(null); if (draft) save.mutate(draft) }}
            data-testid="admin-save-policy">
            {t('save')}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
