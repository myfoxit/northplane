// MCP setup (SPEC §10.3): the instance speaks Model Context Protocol at
// /mcp (Streamable HTTP, Bearer = ordinary API token). This tab mints a
// scoped token and renders ready-to-paste setup snippets for the popular
// MCP clients — the "one-liner" onboarding path. Token secrets appear
// exactly once (same rule as the API-Tokens tab).
import { useMemo, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { post, queryClient } from '../../api'
import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle, CardDescription, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Field } from '@/components/kit'
import { t } from '../../i18n'
import { TOKEN_PLACEHOLDER, SCOPE_PRESETS, CLIENTS, snippetFor, type ClientKey } from './mcp-snippets'

function CopyButton({ text }: { text: string }) {
  const [done, setDone] = useState(false)
  return (
    <Button size="sm" variant="outline" onClick={() => {
      void navigator.clipboard?.writeText(text).then(() => {
        setDone(true)
        setTimeout(() => setDone(false), 2000)
      })
    }}>
      {done ? t('copied') : t('copy')}
    </Button>
  )
}

export function MCPTab() {
  const mcpUrl = `${window.location.origin}/mcp`
  const [client, setClient] = useState<ClientKey>('claude-code')
  const [name, setName] = useState('mcp-agent')
  const [preset, setPreset] = useState<(typeof SCOPE_PRESETS)[number]['key']>('read')
  const [minted, setMinted] = useState('')

  const scopes = useMemo(
    () => SCOPE_PRESETS.find((p) => p.key === preset)!.scopes,
    [preset],
  )
  const create = useMutation({
    mutationFn: () => post<{ token: string }>('/api-tokens', {
      name,
      scopes: scopes.split(',').map((s) => s.trim()).filter(Boolean),
      aiAgent: true,
    }),
    onSuccess: (r) => {
      setMinted(r.token)
      queryClient.invalidateQueries({ queryKey: ['tokens'] })
    },
  })

  const token = minted || TOKEN_PLACEHOLDER
  const snippet = snippetFor(client, mcpUrl, token)

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>MCP-Server</CardTitle>
          <CardDescription>
            Diese Instanz spricht Model Context Protocol (Streamable HTTP). Jeder MCP-Client —
            Claude Code, Claude Desktop, Cursor &amp; Co. — kann damit Monitoring lesen, bedienen
            und (mit Freigabe-Queue) konfigurieren. Auth: gewöhnliches API-Token als Bearer.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex items-center gap-2">
            <code data-testid="mcp-url" className="text-sm bg-muted/50 border border-border rounded px-2 py-1 font-mono">{mcpUrl}</code>
            <CopyButton text={mcpUrl} />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>1. Token erstellen</CardTitle>
          <CardDescription>Das Token bestimmt, was der Agent darf (Least Privilege; Audit als ai_agent).</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex gap-2 items-end flex-wrap">
            <Field label={t('name')}>
              <Input value={name} onChange={(e) => setName(e.target.value)} className="max-w-48" />
            </Field>
            <Field label="Rechte">
              <Tabs value={preset} onValueChange={(v) => setPreset(v as typeof preset)}>
                <TabsList>
                  {SCOPE_PRESETS.map((p) => <TabsTrigger key={p.key} value={p.key}>{p.label}</TabsTrigger>)}
                </TabsList>
              </Tabs>
            </Field>
            <Button onClick={() => create.mutate()} disabled={!name || create.isPending}>{t('newToken')}</Button>
          </div>
          <div className="text-xs text-muted-foreground font-mono">{scopes}</div>
          {minted && (
            <div className="bg-amber-950/40 border border-amber-800/50 rounded-lg p-3">
              <div className="text-xs text-amber-400 mb-1">Einmalig sichtbar — die Snippets unten enthalten es bereits:</div>
              <code className="text-sm text-amber-200 break-all select-all">{minted}</code>
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>2. Client verbinden</CardTitle>
          <CardDescription>
            {minted ? 'Snippet kopieren — fertig.' : `Snippet kopieren und ${TOKEN_PLACEHOLDER} durch ein Token ersetzen.`}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <Tabs value={client} onValueChange={(v) => setClient(v as ClientKey)}>
            <TabsList className="flex-wrap h-auto">
              {CLIENTS.map((c) => <TabsTrigger key={c.key} value={c.key}>{c.label}</TabsTrigger>)}
            </TabsList>
          </Tabs>
          <div className="space-y-2">
            <div className="flex items-center justify-between gap-2">
              <span className="text-xs text-muted-foreground">{snippet.label}</span>
              <CopyButton text={snippet.code} />
            </div>
            <pre data-testid="mcp-snippet" className="text-xs bg-muted/50 border border-border rounded-lg p-3 overflow-auto font-mono whitespace-pre-wrap break-all">
              {snippet.code}
            </pre>
          </div>
          <p className="text-xs text-muted-foreground">
            Lokal auf demselben Host geht auch stdio: <code className="font-mono">northplaned mcp</code> mit{' '}
            <code className="font-mono">NORTHPLANE_TOKEN</code> in der Umgebung.
          </p>
        </CardContent>
      </Card>
    </div>
  )
}
