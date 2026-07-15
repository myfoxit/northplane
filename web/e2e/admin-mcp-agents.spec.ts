// E2E for the new admin surfaces: MCP setup (one-liner snippets + token
// mint), agent enrollment (install command + prefilled agent.yaml), the
// dead-letter queue and config bundles (plan against the real backend).
// Runs as admin against the shared demo DB — everything created carries a
// unique name; bundle apply is exercised via dry-run plan only.
import { test, expect, type Page } from '@playwright/test'
import { authFile } from './lib/roles'

test.use({ storageState: authFile('admin') })

const uniq = () => `${Date.now().toString(36)}${Math.floor(Math.random() * 1e4).toString(36)}`

async function openAdminTab(page: Page, tabName: string | RegExp) {
  await page.goto('/admin')
  await expect(page).not.toHaveURL(/\/login/)
  const tab = page.getByRole('tab', { name: tabName })
  await expect(tab).toBeVisible()
  await tab.click()
}

test.describe('admin · MCP', () => {
  test('shows the /mcp endpoint and per-client snippets', async ({ page }) => {
    await openAdminTab(page, 'MCP')

    // endpoint reflects the instance origin
    await expect(page.getByTestId('mcp-url')).toContainText('/mcp')

    // default snippet: Claude Code one-liner with placeholder token
    await expect(page.getByTestId('mcp-snippet')).toContainText('claude mcp add --transport http northplane')
    await expect(page.getByTestId('mcp-snippet')).toContainText('np_<TOKEN>')

    // switching the client switches the snippet shape
    await page.getByRole('tab', { name: 'Cursor' }).click()
    await expect(page.getByTestId('mcp-snippet')).toContainText('mcpServers')
    await page.getByRole('tab', { name: 'Codex CLI' }).click()
    await expect(page.getByTestId('mcp-snippet')).toContainText('[mcp_servers.northplane]')
  })

  test('mints a scoped token and injects it into the snippet', async ({ page }) => {
    await openAdminTab(page, 'MCP')
    const name = `e2e-mcp-${uniq()}`
    await page.getByRole('textbox').first().fill(name)
    await page.getByRole('button', { name: 'Create token' }).click()

    // secret shown once and embedded into the snippet
    await expect(page.getByText('Einmalig sichtbar', { exact: false })).toBeVisible()
    await expect(page.getByTestId('mcp-snippet')).toContainText('Bearer np_')

    // cleanup: revoke via the tokens tab so reruns stay clean
    await page.getByRole('tab', { name: 'API-Tokens' }).click()
    const row = page.getByRole('row', { name: new RegExp(name) })
    await row.getByRole('button', { name: 'Widerrufen' }).click()
    await expect(row).toHaveCount(0)
  })
})

test.describe('admin · Agents', () => {
  test('shows install one-liner and a prefilled agent.yaml', async ({ page }) => {
    await openAdminTab(page, 'Agents')

    await expect(page.getByTestId('agent-install')).toContainText('install.sh | sh')
    const yaml = page.getByTestId('agent-yaml')
    await expect(yaml).toContainText('server: http://127.0.0.1:')
    await expect(yaml).toContainText('token: np_<TOKEN>')

    // platform tabs switch the service snippet
    await expect(page.getByText('systemctl enable --now', { exact: false })).toBeVisible()
    await page.getByRole('tab', { name: /Windows/ }).click()
    await expect(page.getByText('sc.exe create np-agent', { exact: false })).toBeVisible()
  })
})

test.describe('admin · Dead-Letters', () => {
  test('renders the (typically empty) queue', async ({ page }) => {
    await openAdminTab(page, 'Dead-Letters')
    // demo DB has no failed deliveries — the empty state proves the
    // endpoint round-trip works
    await expect(page.getByText(/Keine Dead-Letters|Letzter Fehler/)).toBeVisible()
  })
})

test.describe('admin · Config-Bundles', () => {
  test('plans a bundle dry-run against the real backend', async ({ page }) => {
    await openAdminTab(page, 'Config-Bundles')

    await expect(page.getByRole('link', { name: /bundle\.yaml/ })).toBeVisible()

    const name = `e2e-bundle-host-${uniq()}`
    await page.getByRole('textbox').fill([
      'kind: Host',
      'metadata:',
      `  name: ${name}`,
      'spec:',
      '  address: 192.0.2.1',
    ].join('\n'))
    await page.getByRole('button', { name: /Planen/ }).click()

    // the plan table shows the pending create; nothing is applied
    await expect(page.getByRole('cell', { name: 'create' })).toBeVisible()
    await expect(page.getByRole('cell', { name: new RegExp(name) })).toBeVisible()
    await expect(page.getByRole('button', { name: /Anwenden \(1/ })).toBeVisible()
  })
})
