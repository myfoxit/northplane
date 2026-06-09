// MCP setup tab: instance URL, per-client snippets, token mint flow.
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../../test/msw'
import { renderWithProviders } from '../../test/render'
import { MCPTab } from './MCP'
import { snippetFor } from './mcp-snippets'

describe('<MCPTab />', () => {
  it('shows the instance /mcp endpoint and the Claude Code one-liner by default', async () => {
    renderWithProviders(<MCPTab />)
    expect((await screen.findByTestId('mcp-url')).textContent).toMatch(/\/mcp$/)
    const snippet = screen.getByTestId('mcp-snippet').textContent ?? ''
    expect(snippet).toContain('claude mcp add --transport http northplane')
    expect(snippet).toContain('Authorization: Bearer np_<TOKEN>')
  })

  it('switches snippets per client', async () => {
    const user = userEvent.setup()
    renderWithProviders(<MCPTab />)
    await user.click(await screen.findByRole('tab', { name: 'Cursor' }))
    expect(screen.getByTestId('mcp-snippet').textContent).toContain('"mcpServers"')
    await user.click(screen.getByRole('tab', { name: 'Gemini CLI' }))
    expect(screen.getByTestId('mcp-snippet').textContent).toContain('"httpUrl"')
    await user.click(screen.getByRole('tab', { name: 'Codex CLI' }))
    expect(screen.getByTestId('mcp-snippet').textContent).toContain('[mcp_servers.northplane]')
  })

  it('mints a token and injects it into the snippet', async () => {
    let sentBody: Record<string, unknown> | undefined
    server.use(http.post('/api/v1/api-tokens', async ({ request }) => {
      sentBody = await request.json() as Record<string, unknown>
      return HttpResponse.json({ token: 'np_minted_secret_1' }, { status: 201 })
    }))
    const user = userEvent.setup()
    renderWithProviders(<MCPTab />)
    await user.click(await screen.findByRole('button', { name: /Token erstellen|Create token/ }))
    await waitFor(() => expect(screen.getByTestId('mcp-snippet').textContent).toContain('np_minted_secret_1'))
    expect(sentBody?.aiAgent).toBe(true)
    expect(Array.isArray(sentBody?.scopes)).toBe(true)
  })
})

describe('snippetFor', () => {
  const url = 'https://np.example.com/mcp'
  it.each(['claude-code', 'claude-desktop', 'cursor', 'vscode', 'windsurf', 'codex', 'gemini'] as const)(
    '%s snippet carries url and token', (client) => {
      const { code } = snippetFor(client, url, 'np_abc')
      expect(code).toContain(url)
      expect(code).toContain('np_abc')
    })
})
