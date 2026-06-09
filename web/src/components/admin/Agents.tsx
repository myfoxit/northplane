// Agent enrollment (SPEC §8.4): np-agent is the NCPA-style host agent —
// passive push by default (no inbound ports), optional HTTPS listener the
// server can query. This tab is the "easily connected" path: mint a
// write-scoped token, copy the install one-liner, copy a prefilled
// agent.yaml and a service unit. Everything here is plain config text —
// the same files an operator would write by hand.
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { post, queryClient } from '../../api'
import { Button } from '@/components/ui/button'
import { Card, CardHeader, CardTitle, CardDescription, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Field } from '@/components/kit'
import { t } from '../../i18n'
import {
  TOKEN_PLACEHOLDER, INSTALL_CMD, agentYaml,
  SYSTEMD_UNIT, LAUNCHD_PLIST, WINDOWS_SERVICE,
} from './agent-snippets'

function CopyBtn({ text }: { text: string }) {
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

function Snippet({ label, code, testid }: { label: string; code: string; testid?: string }) {
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs text-muted-foreground">{label}</span>
        <CopyBtn text={code} />
      </div>
      <pre data-testid={testid} className="text-xs bg-muted/50 border border-border rounded-lg p-3 overflow-auto font-mono whitespace-pre-wrap break-all">{code}</pre>
    </div>
  )
}

export function AgentsTab() {
  const server = window.location.origin
  const [name, setName] = useState('agent-' + (window.crypto?.randomUUID?.().slice(0, 8) ?? 'host'))
  const [hostname, setHostname] = useState('')
  const [minted, setMinted] = useState('')
  const [platform, setPlatform] = useState<'systemd' | 'launchd' | 'windows'>('systemd')

  const create = useMutation({
    mutationFn: () => post<{ token: string }>('/api-tokens', {
      name, scopes: ['objects:write'],
    }),
    onSuccess: (r) => {
      setMinted(r.token)
      queryClient.invalidateQueries({ queryKey: ['tokens'] })
    },
  })

  const token = minted || TOKEN_PLACEHOLDER
  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>np-agent</CardTitle>
          <CardDescription>
            Host-Agent für Linux, macOS und Windows: Load/CPU, Speicher, Disks, Netzwerk,
            Prozesse plus eigene Nagios-Plugins. Standard ist Passiv-Push (keine offenen Ports,
            Store-and-Forward bei Ausfällen); optional lauscht er NCPA-artig auf HTTPS und der
            Server fragt ab (Check-Typ <code className="font-mono">agent</code>).
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Snippet label="1. Binary installieren (Linux/macOS; Windows: Release-Zip von GitHub):" code={INSTALL_CMD} testid="agent-install" />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>2. Token erstellen</CardTitle>
          <CardDescription>Der Agent braucht genau einen Scope: objects:write (Ergebnisse einliefern).</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex gap-2 items-end flex-wrap">
            <Field label={t('name')}>
              <Input value={name} onChange={(e) => setName(e.target.value)} className="max-w-56" />
            </Field>
            <Field label="Hostname (optional)">
              <Input value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="web-01" className="max-w-48" />
            </Field>
            <Button onClick={() => create.mutate()} disabled={!name || create.isPending}>{t('newToken')}</Button>
          </div>
          {minted && (
            <div className="bg-amber-950/40 border border-amber-800/50 rounded-lg p-3">
              <div className="text-xs text-amber-400 mb-1">Einmalig sichtbar — die agent.yaml unten enthält es bereits:</div>
              <code className="text-sm text-amber-200 break-all select-all">{minted}</code>
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>3. Konfigurieren &amp; starten</CardTitle>
          <CardDescription>/etc/northplane/agent.yaml (bzw. C:\ProgramData\northplane\agent.yaml) anlegen, Dienst starten — der Host erscheint automatisch unter Objekte.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <Snippet label="agent.yaml:" code={agentYaml(server, token, hostname)} testid="agent-yaml" />
          <Tabs value={platform} onValueChange={(v) => setPlatform(v as typeof platform)}>
            <TabsList>
              <TabsTrigger value="systemd">Linux (systemd)</TabsTrigger>
              <TabsTrigger value="launchd">macOS (launchd)</TabsTrigger>
              <TabsTrigger value="windows">Windows (Dienst)</TabsTrigger>
            </TabsList>
          </Tabs>
          {platform === 'systemd' && (
            <Snippet label="/etc/systemd/system/np-agent.service — dann: systemctl enable --now np-agent" code={SYSTEMD_UNIT} />
          )}
          {platform === 'launchd' && (
            <Snippet label="/Library/LaunchDaemons/com.northplane.agent.plist — dann: sudo launchctl load -w …" code={LAUNCHD_PLIST} />
          )}
          {platform === 'windows' && (
            <Snippet label="PowerShell (als Administrator):" code={WINDOWS_SERVICE} />
          )}
        </CardContent>
      </Card>
    </div>
  )
}
