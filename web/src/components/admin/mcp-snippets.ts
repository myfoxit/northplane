// Per-client MCP setup snippets (pure data/functions, separated from the
// MCP tab component for react-refresh and unit tests).
import { t } from '../../i18n'

export const TOKEN_PLACEHOLDER = 'np_<TOKEN>'

// Scope presets for the minted MCP token: read-only for pure analysis
// agents; operate adds ack/recheck/downtime; configure adds the generic
// config tools' write scopes.
export const SCOPE_PRESETS = [
  { key: 'read', label: t('scopeRead'), scopes: 'objects:read,alerts:read,incidents:read,events:read,oncall:read,metrics:read,reports:render' },
  { key: 'operate', label: t('scopeOperate'), scopes: 'objects:read,alerts:read,incidents:read,events:read,oncall:read,metrics:read,reports:render,alerts:ack,objects:write,maintenance:write' },
  { key: 'configure', label: t('scopeConfigure'), scopes: 'objects:read,alerts:read,incidents:read,events:read,oncall:read,metrics:read,reports:render,config:write,oncall:write' },
] as const

export type ClientKey = 'claude-code' | 'claude-desktop' | 'cursor' | 'vscode' | 'windsurf' | 'codex' | 'gemini'

export const CLIENTS: { key: ClientKey; label: string }[] = [
  { key: 'claude-code', label: 'Claude Code' },
  { key: 'claude-desktop', label: 'Claude Desktop' },
  { key: 'cursor', label: 'Cursor' },
  { key: 'vscode', label: 'VS Code' },
  { key: 'windsurf', label: 'Windsurf' },
  { key: 'codex', label: 'Codex CLI' },
  { key: 'gemini', label: 'Gemini CLI' },
]

// snippetFor renders the per-client setup text. `label` is shown above
// the code block (one-liner vs config-file location).
export function snippetFor(client: ClientKey, mcpUrl: string, token: string): { label: string; code: string } {
  const auth = `Authorization: Bearer ${token}`
  switch (client) {
    case 'claude-code':
      return {
        label: t('oneCommandTerminal'),
        code: `claude mcp add --transport http northplane ${mcpUrl} --header "${auth}"`,
      }
    case 'claude-desktop':
      return {
        label: 'claude_desktop_config.json → "mcpServers" (Settings → Developer → Edit Config):',
        code: JSON.stringify({
          mcpServers: {
            northplane: {
              command: 'npx',
              args: ['-y', 'mcp-remote', mcpUrl, '--header', auth],
            },
          },
        }, null, 2),
      }
    case 'cursor':
      return {
        label: t('cursorMcpLabel'),
        code: JSON.stringify({
          mcpServers: {
            northplane: { url: mcpUrl, headers: { Authorization: `Bearer ${token}` } },
          },
        }, null, 2),
      }
    case 'vscode':
      return {
        label: t('vscodeMcpLabel'),
        code: `code --add-mcp '${JSON.stringify({ name: 'northplane', type: 'http', url: mcpUrl, headers: { Authorization: `Bearer ${token}` } })}'`,
      }
    case 'windsurf':
      return {
        label: '~/.codeium/windsurf/mcp_config.json → "mcpServers":',
        code: JSON.stringify({
          mcpServers: {
            northplane: { serverUrl: mcpUrl, headers: { Authorization: `Bearer ${token}` } },
          },
        }, null, 2),
      }
    case 'codex':
      return {
        label: '~/.codex/config.toml:',
        code: `[mcp_servers.northplane]\ncommand = "npx"\nargs = ["-y", "mcp-remote", "${mcpUrl}", "--header", "${auth}"]`,
      }
    case 'gemini':
      return {
        label: '~/.gemini/settings.json → "mcpServers":',
        code: JSON.stringify({
          mcpServers: {
            northplane: { httpUrl: mcpUrl, headers: { Authorization: `Bearer ${token}` } },
          },
        }, null, 2),
      }
  }
}
