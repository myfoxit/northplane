// UI-message-stream client (Vercel AI SDK v1 wire format): parses the
// SSE response of POST /api/v1/ai/chat and folds chunks into message
// parts. This is a *transient* per-turn stream over fetch — not a
// standing EventSource — so it does not reintroduce the connection
// starvation the old /stream consumer had (see api.ts header comment).
import { APIError, parseError } from '../api'
import { activeTenantId } from '../tenant'

export interface UIChunk {
  type: string
  id?: string
  delta?: string
  messageId?: string
  toolCallId?: string
  toolName?: string
  inputTextDelta?: string
  input?: unknown
  output?: unknown
  errorText?: string
  finishReason?: string
  toolMetadata?: { proposed?: boolean; actionId?: string }
  messageMetadata?: Record<string, unknown>
}

// AgentPart is the persisted/streamed part shape (mirrors backend ai.Part).
export interface AgentPart {
  type: 'text' | 'reasoning' | 'dynamic-tool' | 'step-start'
  text?: string
  toolName?: string
  toolCallId?: string
  state?: 'input-streaming' | 'input-available' | 'output-available' | 'output-error'
  input?: unknown
  inputText?: string // partial JSON while streaming
  output?: unknown
  errorText?: string
  proposed?: boolean
  actionId?: string
  // stream-local routing key for text/reasoning deltas (per step)
  _key?: string
}

export interface StreamingMessage {
  id: string
  parts: AgentPart[]
  status: 'streaming' | 'done' | 'error' | 'aborted'
  error?: string
  finishReason?: string
}

export function emptyStreamingMessage(): StreamingMessage {
  return { id: '', parts: [], status: 'streaming' }
}

// reduceChunk folds one chunk into the message (returns a new object so
// React state updates propagate).
export function reduceChunk(msg: StreamingMessage, c: UIChunk): StreamingMessage {
  const next: StreamingMessage = { ...msg, parts: [...msg.parts] }
  const findByKey = (type: string, key?: string): AgentPart | undefined => {
    for (let i = next.parts.length - 1; i >= 0; i--) {
      const p = next.parts[i]
      if (p && p.type === type && p._key === key) return p
    }
    return undefined
  }
  const findTool = (toolCallId?: string): AgentPart | undefined => {
    for (let i = next.parts.length - 1; i >= 0; i--) {
      const p = next.parts[i]
      if (p && p.type === 'dynamic-tool' && p.toolCallId === toolCallId) return p
    }
    return undefined
  }
  const replace = (old: AgentPart, updated: AgentPart) => {
    next.parts = next.parts.map((p) => (p === old ? updated : p))
  }
  switch (c.type) {
    case 'start':
      if (c.messageId) next.id = c.messageId
      break
    case 'start-step':
      next.parts.push({ type: 'step-start' })
      break
    case 'finish-step':
      // clear stream keys: a later text-start with the same id is a new part
      next.parts = next.parts.map((p) => (p._key ? { ...p, _key: undefined } : p))
      break
    case 'text-start':
      next.parts.push({ type: 'text', text: '', _key: c.id })
      break
    case 'text-delta': {
      const p = findByKey('text', c.id)
      if (p) replace(p, { ...p, text: (p.text ?? '') + (c.delta ?? '') })
      break
    }
    case 'text-end':
      break
    case 'reasoning-start':
      next.parts.push({ type: 'reasoning', text: '', _key: c.id })
      break
    case 'reasoning-delta': {
      const p = findByKey('reasoning', c.id)
      if (p) replace(p, { ...p, text: (p.text ?? '') + (c.delta ?? '') })
      break
    }
    case 'reasoning-end':
      break
    case 'tool-input-start':
      next.parts.push({
        type: 'dynamic-tool', toolName: c.toolName, toolCallId: c.toolCallId,
        state: 'input-streaming', inputText: '',
      })
      break
    case 'tool-input-delta': {
      const p = findTool(c.toolCallId)
      if (p) replace(p, { ...p, inputText: (p.inputText ?? '') + (c.inputTextDelta ?? '') })
      break
    }
    case 'tool-input-available': {
      const p = findTool(c.toolCallId)
      const part: AgentPart = {
        type: 'dynamic-tool', toolName: c.toolName, toolCallId: c.toolCallId,
        state: 'input-available', input: c.input,
      }
      if (p) replace(p, { ...part, inputText: undefined })
      else next.parts.push(part)
      break
    }
    case 'tool-output-available': {
      const p = findTool(c.toolCallId)
      if (p) {
        replace(p, {
          ...p, state: 'output-available', output: c.output,
          proposed: c.toolMetadata?.proposed, actionId: c.toolMetadata?.actionId,
        })
      }
      break
    }
    case 'tool-output-error': {
      const p = findTool(c.toolCallId)
      if (p) replace(p, { ...p, state: 'output-error', errorText: c.errorText })
      break
    }
    case 'error':
      next.status = 'error'
      next.error = c.errorText
      break
    case 'abort':
      next.status = 'aborted'
      break
    case 'finish':
      if (next.status === 'streaming') next.status = 'done'
      next.finishReason = c.finishReason
      break
  }
  return next
}

export interface SendBody {
  chatId: string
  message?: string
  trigger?: 'submit-message' | 'regenerate-message'
  messageId?: string
}

// streamChat POSTs one agent turn and yields parsed chunks. Resolves
// after [DONE]; rejects on transport/HTTP errors. Abort via signal.
export async function streamChat(
  body: SendBody,
  onChunk: (c: UIChunk) => void,
  signal?: AbortSignal,
): Promise<void> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  const tenant = activeTenantId()
  if (tenant) headers['X-Northplane-Tenant'] = tenant
  const res = await fetch('/api/v1/ai/chat', {
    method: 'POST', headers, credentials: 'same-origin',
    body: JSON.stringify(body), signal,
  })
  if (res.status === 401) {
    window.location.href = '/login'
    throw new APIError(401, 'auth', 'login required', '')
  }
  if (!res.ok || !res.body) throw await parseError(res)

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    for (;;) {
      const nl = buf.indexOf('\n\n')
      if (nl < 0) break
      const event = buf.slice(0, nl)
      buf = buf.slice(nl + 2)
      for (const line of event.split('\n')) {
        if (!line.startsWith('data:')) continue
        const data = line.slice(5).trim()
        if (data === '[DONE]') return
        try {
          onChunk(JSON.parse(data) as UIChunk)
        } catch {
          // tolerate malformed keep-alives
        }
      }
    }
  }
}
