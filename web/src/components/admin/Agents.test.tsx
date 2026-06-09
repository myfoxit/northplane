// Agent enrollment tab: install one-liner, prefilled agent.yaml, token mint.
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '../../test/msw'
import { renderWithProviders } from '../../test/render'
import { AgentsTab } from './Agents'
import { agentYaml } from './agent-snippets'

describe('<AgentsTab />', () => {
  it('renders install command and a prefilled agent.yaml', async () => {
    renderWithProviders(<AgentsTab />)
    expect((await screen.findByTestId('agent-install')).textContent).toContain('install.sh | sh')
    const yaml = screen.getByTestId('agent-yaml').textContent ?? ''
    expect(yaml).toContain(`server: ${window.location.origin}`)
    expect(yaml).toContain('token: np_<TOKEN>')
    expect(yaml).toContain('interval: 60s')
  })

  it('mints an objects:write token into the yaml and honors the hostname field', async () => {
    let sentBody: Record<string, unknown> | undefined
    server.use(http.post('/api/v1/api-tokens', async ({ request }) => {
      sentBody = await request.json() as Record<string, unknown>
      return HttpResponse.json({ token: 'np_agent_tok_9' }, { status: 201 })
    }))
    const user = userEvent.setup()
    renderWithProviders(<AgentsTab />)
    await user.type(await screen.findByPlaceholderText('web-01'), 'db-42')
    await user.click(screen.getByRole('button', { name: /Token erstellen|Create token/ }))
    await waitFor(() => expect(screen.getByTestId('agent-yaml').textContent).toContain('token: np_agent_tok_9'))
    expect(screen.getByTestId('agent-yaml').textContent).toContain('hostname: db-42')
    expect(sentBody?.scopes).toEqual(['objects:write'])
  })

  it('switches service-unit snippets per platform', async () => {
    const user = userEvent.setup()
    renderWithProviders(<AgentsTab />)
    expect(await screen.findByText(/systemctl enable --now/)).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: /Windows/ }))
    expect(screen.getByText(/sc\.exe create np-agent/)).toBeInTheDocument()
  })
})

describe('agentYaml', () => {
  it('comments the hostname when omitted', () => {
    expect(agentYaml('https://x', 'np_t', '')).toContain('# hostname:')
    expect(agentYaml('https://x', 'np_t', 'web-9')).toContain('hostname: web-9')
  })
})
