// Agent chat (SPEC §10.4 evolution): full chat workspace on top of the
// multi-provider agent backend — chat history, per-chat provider/model
// choice, streamed turns with reasoning + tool cards, message delete
// and regenerate. Streaming rides a per-turn fetch (see lib/uistream).
import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import {
  Bot, KeyRound, Plus, RefreshCw, Send, Square, Trash2, Wrench,
} from 'lucide-react'
import { del, get, post, put, queryClient, fmtAgo, type ListResponse } from '../api'
import type { AIChatMeta, AIChatMessage, AIConnection, AIModelInfo, AIChatSettings } from '../types'
import { Button } from '@/components/ui/button'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Empty, ErrorState, Spinner } from '@/components/kit'
import { MessageParts } from '../components/agent/parts'
import { ProvidersDialog } from '../components/agent/ProvidersDialog'
import { connectionsQuery } from '../components/agent/queries'
import {
  emptyStreamingMessage, reduceChunk, streamChat,
  type AgentPart, type StreamingMessage,
} from '../lib/uistream'
import { t } from '../i18n'

interface ChatDetail {
  chat: AIChatMeta
  messages: AIChatMessage[] | null
}

// Composer picker state for a chat that does not exist yet.
interface DraftTarget {
  connectionId: string
  model: string
}

export function AgentChatPage() {
  const [chatId, setChatId] = useState('')
  const [providersOpen, setProvidersOpen] = useState(false)
  const [input, setInput] = useState('')
  const [draft, setDraft] = useState<DraftTarget>({ connectionId: '', model: '' })
  // pending = the in-flight turn (optimistic user msg + streaming assistant)
  const [pending, setPending] = useState<{ user?: string; msg: StreamingMessage } | null>(null)
  const abortRef = useRef<AbortController | null>(null)
  const scrollRef = useRef<HTMLDivElement>(null)

  const chats = useQuery({
    queryKey: ['ai-chats'],
    queryFn: () => get<ListResponse<AIChatMeta>>('/ai/chats'),
  })
  const conns = useQuery(connectionsQuery)
  const detail = useQuery({
    queryKey: ['ai-chat', chatId],
    queryFn: () => get<ChatDetail>(`/ai/chats/${chatId}`),
    enabled: chatId !== '',
  })

  const connections = conns.data?.items ?? []
  const chat = detail.data?.chat
  const activeConnID = chat?.connectionId || draft.connectionId || connections[0]?.id || ''
  const activeConn = connections.find((c) => c.id === activeConnID)

  const models = useQuery({
    queryKey: ['ai-connection-models', activeConnID],
    queryFn: () => get<{ items: AIModelInfo[] | null; note?: string }>(`/ai/connections/${activeConnID}/models`),
    enabled: activeConnID !== '',
    staleTime: 300_000,
    retry: 0,
  })
  const activeModel = chat?.model || draft.model || activeConn?.defaultModel
    || models.data?.items?.[0]?.id || ''

  const scrollDown = () => { setTimeout(() => scrollRef.current?.scrollTo({ top: 1e9 }), 30) }
  useEffect(scrollDown, [chatId, detail.data?.messages?.length, pending?.msg.parts.length])

  const deleteChat = useMutation({
    mutationFn: (id: string) => del(`/ai/chats/${id}`),
    onSuccess: (_d, id) => {
      if (id === chatId) setChatId('')
      queryClient.invalidateQueries({ queryKey: ['ai-chats'] })
    },
  })
  const deleteMessage = useMutation({
    mutationFn: (msgId: string) => del(`/ai/chats/${chatId}/messages/${msgId}`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['ai-chat', chatId] }),
  })

  // retarget persists a connection/model switch on the open chat.
  const retarget = useMutation({
    mutationFn: (target: Partial<DraftTarget> & { settings?: AIChatSettings }) =>
      put<AIChatMeta>(`/ai/chats/${chatId}`, {
        connectionId: target.connectionId, model: target.model, settings: target.settings,
      }, chat?.version ?? 0),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['ai-chat', chatId] }),
  })

  const streaming = pending !== null && pending.msg.status === 'streaming'

  const runTurn = async (id: string, body: Parameters<typeof streamChat>[0], userText?: string) => {
    const ac = new AbortController()
    abortRef.current = ac
    setPending({ user: userText, msg: emptyStreamingMessage() })
    try {
      await streamChat(body, (chunk) => {
        setPending((p) => (p ? { ...p, msg: reduceChunk(p.msg, chunk) } : p))
      }, ac.signal)
    } catch (err) {
      if (!ac.signal.aborted) {
        setPending((p) => p ? {
          ...p,
          msg: { ...p.msg, status: 'error', error: err instanceof Error ? err.message : String(err) },
        } : p)
      }
    } finally {
      abortRef.current = null
      await queryClient.invalidateQueries({ queryKey: ['ai-chat', id] })
      await queryClient.invalidateQueries({ queryKey: ['ai-chats'] })
      // Keep error banners visible; successful turns land in the query data.
      setPending((p) => (p && p.msg.status === 'error' ? p : null))
    }
  }

  const send = async () => {
    const message = input.trim()
    if (!message || streaming) return
    let id = chatId
    if (!id) {
      if (!activeConnID) {
        setProvidersOpen(true)
        return
      }
      const created = await post<AIChatMeta>('/ai/chats', {
        connectionId: activeConnID, model: activeModel,
      })
      id = created.id
      setChatId(id)
      queryClient.invalidateQueries({ queryKey: ['ai-chats'] })
    }
    setInput('')
    await runTurn(id, { chatId: id, message }, message)
  }

  const regenerate = async (messageId: string) => {
    if (streaming || !chatId) return
    await runTurn(chatId, { chatId, trigger: 'regenerate-message', messageId })
  }

  const stop = () => abortRef.current?.abort()

  const messages = detail.data?.messages ?? []
  const lastAssistant = [...messages].reverse().find((m) => m.role === 'assistant')

  return (
    <div className="flex h-[calc(100vh-5rem)] gap-4 -m-2 p-2">
      {/* chat list */}
      <aside className="w-60 shrink-0 flex flex-col gap-2">
        <Button size="sm" variant="outline" className="justify-start" data-testid="agent-new-chat"
          onClick={() => { setChatId(''); setPending(null); setInput('') }}>
          <Plus size={14} /> {t('agentNewChat')}
        </Button>
        <div className="flex-1 overflow-auto space-y-1">
          {chats.isLoading && <Spinner />}
          {chats.data?.items?.length === 0 && <Empty text={t('agentNoChats')} />}
          {chats.data?.items?.map((c) => (
            <div key={c.id}
              className={`group flex items-center gap-1 rounded-lg border px-2.5 py-2 cursor-pointer text-sm ${
                c.id === chatId ? 'bg-primary/10 border-primary/40' : 'bg-card/50 border-border hover:border-input'}`}
              onClick={() => { setChatId(c.id); setPending(null) }}>
              <div className="min-w-0 flex-1">
                <div className="truncate text-foreground/90">{c.title || t('agentNewChat')}</div>
                <div className="text-[11px] text-muted-foreground">{fmtAgo(c.updatedAt)}</div>
              </div>
              <button
                aria-label={t('delete')}
                className="opacity-0 group-hover:opacity-100 text-muted-foreground hover:text-danger cursor-pointer"
                onClick={(e) => { e.stopPropagation(); deleteChat.mutate(c.id) }}>
                <Trash2 size={13} />
              </button>
            </div>
          ))}
        </div>
        <Button size="sm" variant="ghost" className="justify-start" onClick={() => setProvidersOpen(true)}>
          <KeyRound size={14} /> {t('agentProviders')}
        </Button>
      </aside>

      {/* thread */}
      <section className="flex-1 min-w-0 flex flex-col bg-card/30 border border-border rounded-xl">
        <div ref={scrollRef} className="flex-1 overflow-auto p-4 space-y-4">
          {chatId === '' && pending === null && (
            <div className="h-full flex flex-col items-center justify-center gap-3 text-center">
              <Bot size={32} className="text-muted-foreground" />
              <h1 className="text-lg font-bold">{t('agentTitle')}</h1>
              <p className="text-sm text-muted-foreground max-w-md">{t('agentIntro')}</p>
              {connections.length === 0 && (
                <Button size="sm" onClick={() => setProvidersOpen(true)} data-testid="agent-connect-cta">
                  <KeyRound size={14} /> {t('agentConnectFirst')}
                </Button>
              )}
            </div>
          )}
          {chatId !== '' && detail.isLoading && <Spinner />}
          {chatId !== '' && detail.isError && <ErrorState error={detail.error} onRetry={detail.refetch} />}
          {messages.map((m) => (
            <MessageRow key={m.id} msg={m}
              onDelete={() => deleteMessage.mutate(m.id)}
              onRegenerate={m.id === lastAssistant?.id && !streaming ? () => regenerate(m.id) : undefined}
            />
          ))}
          {pending?.user && (
            <div className="text-right">
              <div className="inline-block max-w-[85%] text-left rounded-xl px-3 py-2 text-sm whitespace-pre-wrap bg-primary/25 text-foreground">
                {pending.user}
              </div>
            </div>
          )}
          {pending && (
            <div className="max-w-[95%]">
              {pending.msg.parts.length === 0 && pending.msg.status === 'streaming' && (
                <div className="text-muted-foreground text-sm animate-pulse">…</div>
              )}
              <MessageParts parts={pending.msg.parts.filter((p) => p.type !== 'step-start')} streaming={streaming} />
              {pending.msg.error && (
                <div className="mt-2 text-sm text-destructive bg-destructive/10 border border-destructive/30 rounded-lg px-3 py-2">
                  {pending.msg.error}
                </div>
              )}
            </div>
          )}
        </div>

        {/* composer */}
        <div className="border-t border-border p-3 space-y-2">
          <div className="flex items-center gap-2 flex-wrap">
            <Select value={activeConnID || undefined}
              onValueChange={(v) => {
                if (chatId) retarget.mutate({ connectionId: v, model: '' })
                else setDraft({ connectionId: v, model: '' })
              }}>
              <SelectTrigger size="sm" className="max-w-52" data-testid="agent-conn-picker">
                <SelectValue placeholder={t('agentSelectConnection')} />
              </SelectTrigger>
              <SelectContent>
                {connections.map((c: AIConnection) => (
                  <SelectItem key={c.id} value={c.id}>{c.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={activeModel || undefined}
              onValueChange={(v) => {
                if (chatId) retarget.mutate({ model: v })
                else setDraft((d) => ({ ...d, model: v }))
              }}>
              <SelectTrigger size="sm" className="max-w-64" data-testid="agent-model-picker">
                <SelectValue placeholder={t('agentSelectModel')} />
              </SelectTrigger>
              <SelectContent>
                {(models.data?.items ?? (activeModel ? [{ id: activeModel }] : [])).map((m) => (
                  <SelectItem key={m.id} value={m.id}>{m.label || m.id}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <ChatSettingsPopover chat={chat} onSave={(settings) => retarget.mutate({ settings })} />
            <span className="ml-auto" />
            {streaming && (
              <Button size="xs" variant="outline" onClick={stop} data-testid="agent-stop">
                <Square size={12} /> {t('agentStop')}
              </Button>
            )}
          </div>
          <div className="flex gap-2 items-end">
            <textarea
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); void send() } }}
              placeholder={t('agentSendPlaceholder')}
              rows={2}
              data-testid="agent-input"
              className="flex-1 bg-card border border-input rounded-lg px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground resize-none focus:border-ring outline-none"
            />
            <Button onClick={() => void send()} disabled={streaming || input.trim() === ''}
              aria-label={t('agentSend')} data-testid="agent-send">
              <Send size={15} />
            </Button>
          </div>
        </div>
      </section>

      <ProvidersDialog open={providersOpen} onClose={() => setProvidersOpen(false)} />
    </div>
  )
}

function MessageRow({ msg, onDelete, onRegenerate }: {
  msg: AIChatMessage
  onDelete: () => void
  onRegenerate?: () => void
}) {
  const parts = (msg.parts as AgentPart[] | null ?? []).filter((p) => p.type !== 'step-start')
  if (msg.role === 'user') {
    const text = parts.map((p) => p.text ?? '').join('')
    return (
      <div className="text-right group">
        <div className="inline-block max-w-[85%] text-left rounded-xl px-3 py-2 text-sm whitespace-pre-wrap bg-primary/25 text-foreground">
          {text}
        </div>
        <RowActions onDelete={onDelete} />
      </div>
    )
  }
  return (
    <div className="max-w-[95%] group">
      <MessageParts parts={parts} />
      <div className="flex items-center gap-2">
        <RowActions onDelete={onDelete} onRegenerate={onRegenerate} />
        {msg.model && <span className="text-[10px] text-muted-foreground opacity-0 group-hover:opacity-100">{msg.model}</span>}
      </div>
    </div>
  )
}

function RowActions({ onDelete, onRegenerate }: { onDelete: () => void; onRegenerate?: () => void }) {
  return (
    <div className="mt-1 flex gap-2 opacity-0 group-hover:opacity-100 transition-opacity justify-end">
      {onRegenerate && (
        <button className="text-muted-foreground hover:text-foreground/90 cursor-pointer"
          title={t('agentRegenerate')} onClick={onRegenerate}>
          <RefreshCw size={12} />
        </button>
      )}
      <button className="text-muted-foreground hover:text-danger cursor-pointer"
        title={t('delete')} onClick={onDelete} aria-label={t('agentDeleteMessage')}>
        <Trash2 size={12} />
      </button>
    </div>
  )
}

// ChatSettingsPopover: per-chat agent capabilities (tools on/off, effort).
function ChatSettingsPopover({ chat, onSave }: {
  chat?: AIChatMeta
  onSave: (s: AIChatSettings) => void
}) {
  const settings = chat?.settings ?? {}
  const toolsEnabled = settings.toolsEnabled !== false
  if (!chat) return null
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button size="xs" variant="ghost" data-testid="agent-chat-settings">
          <Wrench size={12} /> {t('agentTools')}
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-64 space-y-3" align="start">
        <div className="flex items-center justify-between">
          <Label htmlFor="agent-tools" className="text-sm">{t('agentToolsEnabled')}</Label>
          <Switch id="agent-tools" checked={toolsEnabled}
            onCheckedChange={(on) => onSave({ ...settings, toolsEnabled: on })} />
        </div>
        <div className="space-y-1">
          <Label className="text-xs text-muted-foreground">{t('agentEffort')}</Label>
          <Select value={settings.effort || 'default'}
            onValueChange={(v) => onSave({ ...settings, effort: v === 'default' ? '' : v })}>
            <SelectTrigger size="sm" className="w-full"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="default">Standard</SelectItem>
              <SelectItem value="low">low</SelectItem>
              <SelectItem value="medium">medium</SelectItem>
              <SelectItem value="high">high</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </PopoverContent>
    </Popover>
  )
}
