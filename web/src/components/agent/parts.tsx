// Agent message part rendering: markdown text (XSS-safe — react-markdown
// renders to React elements, raw HTML stays disabled), collapsible
// reasoning, and tool cards with the northplane approval gate.
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Brain, Check, ChevronDown, ChevronRight, Loader2, Wrench, X } from 'lucide-react'
import { post, queryClient } from '../../api'
import { Button } from '@/components/ui/button'
import { t } from '../../i18n'
import type { AgentPart } from '../../lib/uistream'

// Compact markdown styling on theme tokens (no typography plugin).
const mdComponents = {
  p: (props: React.ComponentProps<'p'>) => <p className="mb-2 last:mb-0 leading-relaxed" {...props} />,
  ul: (props: React.ComponentProps<'ul'>) => <ul className="mb-2 list-disc pl-5 space-y-0.5" {...props} />,
  ol: (props: React.ComponentProps<'ol'>) => <ol className="mb-2 list-decimal pl-5 space-y-0.5" {...props} />,
  h1: (props: React.ComponentProps<'h1'>) => <h3 className="font-bold mt-3 mb-1.5" {...props} />,
  h2: (props: React.ComponentProps<'h2'>) => <h3 className="font-bold mt-3 mb-1.5" {...props} />,
  h3: (props: React.ComponentProps<'h3'>) => <h4 className="font-semibold mt-2 mb-1" {...props} />,
  a: (props: React.ComponentProps<'a'>) => (
    <a className="text-primary underline underline-offset-2" target="_blank" rel="noreferrer" {...props} />
  ),
  code: ({ className, children, ...rest }: React.ComponentProps<'code'>) => {
    const block = /language-/.test(className ?? '')
    return block ? (
      <code className={`${className ?? ''} font-mono text-xs`} {...rest}>{children}</code>
    ) : (
      <code className="font-mono text-[0.85em] bg-muted/60 border border-border rounded px-1 py-0.5" {...rest}>
        {children}
      </code>
    )
  },
  pre: (props: React.ComponentProps<'pre'>) => (
    <pre className="mb-2 text-xs bg-muted/50 border border-border rounded-lg p-3 overflow-auto font-mono" {...props} />
  ),
  table: (props: React.ComponentProps<'table'>) => (
    <div className="mb-2 overflow-x-auto">
      <table className="text-xs border-collapse w-full" {...props} />
    </div>
  ),
  th: (props: React.ComponentProps<'th'>) => (
    <th className="border border-border bg-muted/40 px-2 py-1 text-left font-semibold" {...props} />
  ),
  td: (props: React.ComponentProps<'td'>) => <td className="border border-border px-2 py-1 align-top" {...props} />,
  blockquote: (props: React.ComponentProps<'blockquote'>) => (
    <blockquote className="border-l-2 border-border pl-3 text-muted-foreground mb-2" {...props} />
  ),
  hr: () => <hr className="border-border my-3" />,
}

export function MarkdownText({ text }: { text: string }) {
  return (
    <div className="text-sm text-foreground/90">
      <Markdown remarkPlugins={[remarkGfm]} components={mdComponents}>{text}</Markdown>
    </div>
  )
}

function ReasoningPart({ text, streaming }: { text: string; streaming: boolean }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="text-xs">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex items-center gap-1 text-muted-foreground hover:text-foreground/80 cursor-pointer"
      >
        {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
        <Brain size={12} />
        <span>{t('agentReasoning')}</span>
        {streaming && <Loader2 size={11} className="animate-spin" />}
      </button>
      {open && (
        <div className="mt-1 ml-4 pl-2 border-l border-border text-muted-foreground italic whitespace-pre-wrap">
          {text || '…'}
        </div>
      )}
    </div>
  )
}

function ToolPart({ part }: { part: AgentPart }) {
  const [open, setOpen] = useState(false)
  const [decided, setDecided] = useState('')
  const approve = useMutation({
    mutationFn: (actionId: string) => post(`/ai/actions/${actionId}:approve`),
    onSuccess: () => {
      setDecided('approved')
      queryClient.invalidateQueries({ queryKey: ['ai-actions'] })
    },
  })
  const deny = useMutation({
    mutationFn: (actionId: string) => post(`/ai/actions/${actionId}:deny`),
    onSuccess: () => {
      setDecided('denied')
      queryClient.invalidateQueries({ queryKey: ['ai-actions'] })
    },
  })
  const running = part.state === 'input-streaming' || part.state === 'input-available'
  const inputJSON = part.input !== undefined ? JSON.stringify(part.input, null, 2) : (part.inputText ?? '')
  const outputJSON = part.output !== undefined ? JSON.stringify(part.output, null, 2) : ''
  return (
    <div className="bg-card border border-input rounded-lg text-xs" data-testid="tool-card">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="w-full flex items-center gap-1.5 px-2.5 py-2 cursor-pointer text-left"
      >
        {running ? (
          <Loader2 size={12} className="animate-spin text-muted-foreground shrink-0" />
        ) : part.state === 'output-error' ? (
          <X size={12} className="text-danger shrink-0" />
        ) : (
          <Wrench size={12} className="text-muted-foreground shrink-0" />
        )}
        <span className="font-mono text-foreground/90">{part.toolName}</span>
        {part.proposed && (
          <span className="rounded border border-warning/50 text-warning px-1 py-px text-[10px]">
            {t('agentProposalBadge')}
          </span>
        )}
        {part.state === 'output-available' && !part.proposed && (
          <Check size={12} className="text-success shrink-0" />
        )}
        <span className="ml-auto text-muted-foreground">{open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}</span>
      </button>
      {open && (
        <div className="px-2.5 pb-2 space-y-1.5">
          {inputJSON && (
            <pre className="bg-muted/40 border border-border rounded p-2 overflow-auto max-h-40 font-mono">{inputJSON}</pre>
          )}
          {outputJSON && (
            <pre className="bg-muted/40 border border-border rounded p-2 overflow-auto max-h-56 font-mono">{outputJSON}</pre>
          )}
        </div>
      )}
      {part.state === 'output-error' && (
        <div className="px-2.5 pb-2 text-danger">{part.errorText}</div>
      )}
      {part.proposed && part.actionId && !decided && (
        <div className="flex gap-2 px-2.5 pb-2.5">
          <Button size="xs" variant="default" disabled={approve.isPending}
            onClick={() => approve.mutate(part.actionId!)}>
            {t('approve')}
          </Button>
          <Button size="xs" variant="ghost" disabled={deny.isPending}
            onClick={() => deny.mutate(part.actionId!)}>
            {t('deny')}
          </Button>
        </div>
      )}
      {decided === 'approved' && (
        <div className="px-2.5 pb-2 text-success flex items-center gap-1"><Check size={12} /> {t('agentApproved')}</div>
      )}
      {decided === 'denied' && (
        <div className="px-2.5 pb-2 text-muted-foreground flex items-center gap-1"><X size={12} /> {t('agentDenied')}</div>
      )}
    </div>
  )
}

// MessageParts renders a persisted or streaming parts array.
export function MessageParts({ parts, streaming }: { parts: AgentPart[]; streaming?: boolean }) {
  const last = parts[parts.length - 1]
  return (
    <div className="space-y-2">
      {parts.map((part, i) => {
        switch (part.type) {
          case 'text':
            return <MarkdownText key={i} text={part.text ?? ''} />
          case 'reasoning':
            return <ReasoningPart key={i} text={part.text ?? ''} streaming={!!streaming && part === last} />
          case 'dynamic-tool':
            return <ToolPart key={part.toolCallId ?? i} part={part} />
          default:
            return null
        }
      })}
    </div>
  )
}
