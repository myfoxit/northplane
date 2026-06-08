// AI assistant sidebar (SPEC §10.4): chat with action cards — proposed
// mutations render as approval buttons, never invisible execution.
import { useRef, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Sparkles, X, Check } from 'lucide-react'
import { post, queryClient, APIError } from '../api'
import { Button } from '@/components/ui/button'
import { t } from '../i18n'

interface ActionCard {
  tool: string
  input: Record<string, unknown>
  proposed: boolean
  actionId?: string
  result?: unknown
  error?: string
}

interface Turn {
  role: 'user' | 'assistant'
  text: string
  actions?: ActionCard[]
}

export function AISidebar({ onClose }: { onClose: () => void }) {
  const [turns, setTurns] = useState<Turn[]>([])
  const [input, setInput] = useState('')
  const [conversationId, setConversationId] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)

  const send = useMutation({
    mutationFn: (message: string) =>
      post<{ conversationId: string; reply: string; actions?: ActionCard[] }>(
        '/ai/conversations', { conversationId, message }),
    onSuccess: (resp) => {
      setConversationId(resp.conversationId)
      setTurns((ts) => [...ts, { role: 'assistant', text: resp.reply, actions: resp.actions }])
      queryClient.invalidateQueries({ queryKey: ['ai-actions'] })
      setTimeout(() => scrollRef.current?.scrollTo({ top: 1e9 }), 50)
    },
    onError: (err) => {
      const detail = err instanceof APIError ? `${err.message}: ${err.detail}` : String(err)
      setTurns((ts) => [...ts, { role: 'assistant', text: `⚠ ${detail}` }])
    },
  })

  const approve = useMutation({
    mutationFn: (actionId: string) => post(`/ai/actions/${actionId}:approve`),
    onSuccess: () => queryClient.invalidateQueries(),
  })

  const submit = () => {
    const message = input.trim()
    if (!message || send.isPending) return
    setTurns((ts) => [...ts, { role: 'user', text: message }])
    setInput('')
    send.mutate(message)
    setTimeout(() => scrollRef.current?.scrollTo({ top: 1e9 }), 50)
  }

  return (
    <aside className="w-96 shrink-0 border-l border-border flex flex-col bg-background">
      <div className="px-4 py-3 border-b border-border flex items-center justify-between">
        <span className="text-sm font-semibold text-foreground/90 flex items-center gap-1.5">
          <Sparkles size={14} /> {t('assistant')}
        </span>
        <button onClick={onClose} aria-label={t('close')} className="text-muted-foreground hover:text-foreground/90 cursor-pointer">
          <X size={14} />
        </button>
      </div>
      <div ref={scrollRef} className="flex-1 overflow-auto p-3 space-y-3">
        {turns.length === 0 && (
          <p className="text-xs text-muted-foreground p-2">
            Triage, Korrelation, Konfiguration per Sprache. Mutationen laufen
            über Action-Cards mit Bestätigung — nichts passiert unsichtbar.
          </p>
        )}
        {turns.map((turn, i) => (
          <div key={i} className={turn.role === 'user' ? 'text-right' : ''}>
            <div className={`inline-block max-w-[90%] text-left rounded-xl px-3 py-2 text-sm whitespace-pre-wrap ${
              turn.role === 'user' ? 'bg-primary/30 text-foreground' : 'bg-card text-foreground/90 border border-border'}`}>
              {turn.text || '…'}
            </div>
            {turn.actions?.map((action, j) => (
              <div key={j} className="mt-2 bg-card border border-input rounded-lg p-2.5 text-xs">
                <div className="font-mono text-muted-foreground">{action.tool}</div>
                <div className="font-mono text-muted-foreground truncate">{JSON.stringify(action.input)}</div>
                {action.error && <div className="text-red-400 mt-1">{action.error}</div>}
                {action.proposed && action.actionId && (
                  <div className="flex gap-2 mt-2">
                    <Button size="sm" variant="default"
                      onClick={() => approve.mutate(action.actionId!)}
                      disabled={approve.isPending}>
                      {t('approve')}
                    </Button>
                    <Button size="sm" variant="ghost"
                      onClick={() => post(`/ai/actions/${action.actionId}:deny`)}>
                      {t('deny')}
                    </Button>
                  </div>
                )}
                {!action.proposed && !action.error && (
                  <div className="text-emerald-500/80 mt-1 flex items-center gap-1">
                    <Check size={13} /> ausgeführt (auditiert)
                  </div>
                )}
              </div>
            ))}
          </div>
        ))}
        {send.isPending && (
          <div className="text-muted-foreground text-sm animate-pulse px-2 flex items-center gap-1.5">
            <Sparkles size={14} /> …
          </div>
        )}
      </div>
      <div className="p-3 border-t border-border">
        <textarea
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); submit() } }}
          placeholder={t('askPlaceholder')}
          rows={2}
          className="w-full bg-card border border-input rounded-lg px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground resize-none focus:border-ring"
        />
      </div>
    </aside>
  )
}
