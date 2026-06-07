// AI assistant sidebar (SPEC §10.4): chat with action cards — proposed
// mutations render as approval buttons, never invisible execution.
import { useRef, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { post, queryClient, APIError } from '../api'
import { Button } from './ui'
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
    <aside className="w-96 shrink-0 border-l border-slate-800 flex flex-col bg-slate-950">
      <div className="px-4 py-3 border-b border-slate-800 flex items-center justify-between">
        <span className="text-sm font-semibold text-slate-300">✦ {t('assistant')}</span>
        <button onClick={onClose} className="text-slate-500 hover:text-slate-300 cursor-pointer">✕</button>
      </div>
      <div ref={scrollRef} className="flex-1 overflow-auto p-3 space-y-3">
        {turns.length === 0 && (
          <p className="text-xs text-slate-500 p-2">
            Triage, Korrelation, Konfiguration per Sprache. Mutationen laufen
            über Action-Cards mit Bestätigung — nichts passiert unsichtbar.
          </p>
        )}
        {turns.map((turn, i) => (
          <div key={i} className={turn.role === 'user' ? 'text-right' : ''}>
            <div className={`inline-block max-w-[90%] text-left rounded-xl px-3 py-2 text-sm whitespace-pre-wrap ${
              turn.role === 'user' ? 'bg-blue-600/30 text-blue-100' : 'bg-slate-900 text-slate-300 border border-slate-800'}`}>
              {turn.text || '…'}
            </div>
            {turn.actions?.map((action, j) => (
              <div key={j} className="mt-2 bg-slate-900 border border-slate-700 rounded-lg p-2.5 text-xs">
                <div className="font-mono text-slate-400">{action.tool}</div>
                <div className="font-mono text-slate-500 truncate">{JSON.stringify(action.input)}</div>
                {action.error && <div className="text-red-400 mt-1">{action.error}</div>}
                {action.proposed && action.actionId && (
                  <div className="flex gap-2 mt-2">
                    <Button size="sm" variant="primary"
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
                  <div className="text-emerald-500/80 mt-1">✓ ausgeführt (auditiert)</div>
                )}
              </div>
            ))}
          </div>
        ))}
        {send.isPending && <div className="text-slate-500 text-sm animate-pulse px-2">✦ …</div>}
      </div>
      <div className="p-3 border-t border-slate-800">
        <textarea
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); submit() } }}
          placeholder={t('askPlaceholder')}
          rows={2}
          className="w-full bg-slate-900 border border-slate-700 rounded-lg px-3 py-2 text-sm text-slate-200 placeholder:text-slate-500 resize-none focus:outline-none focus:border-blue-500"
        />
      </div>
    </aside>
  )
}
