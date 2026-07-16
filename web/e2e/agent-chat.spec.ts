// Agent-chat surface against the real server (demo mode, no LLM
// provider): page shell, the provider-connection flow (Ollama needs no
// key, so create works end-to-end), and the admin policy editor.
import { test, expect } from '@playwright/test'
import { authFile } from './lib/roles'

test.describe('agent chat — page & providers', () => {
  test('empty state renders and a provider connection can be created', async ({ page }) => {
    await page.goto('/agent')
    await expect(page.getByRole('heading', { name: /^KI-Agent$/ })).toBeVisible()
    await expect(page.getByText(/Chatte mit deiner Infrastruktur/)).toBeVisible()

    // No connections yet → CTA opens the providers dialog.
    await page.getByTestId('agent-connect-cta').click()
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByText('KI-Provider')).toBeVisible()

    // Create an Ollama connection (keyless — works without a secret box).
    await dialog.getByTestId('agent-add-connection').click()
    await dialog.getByPlaceholder(/Mein Anthropic-Konto/).fill('Lokales Ollama')
    await dialog.getByRole('combobox').first().click()
    await page.getByRole('option', { name: 'Ollama (lokal)' }).click()
    await dialog.getByRole('button', { name: 'Speichern' }).click()

    // Listed with provider id; then visible in the composer picker.
    await expect(dialog.getByText('Lokales Ollama')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByTestId('agent-conn-picker')).toContainText('Lokales Ollama')
    await expect(page.getByTestId('agent-input')).toBeVisible()
  })
})

test.describe('agent chat — admin policy', () => {
  test.use({ storageState: authFile('admin') })

  test('policy tab lists tools and saves', async ({ page }) => {
    await page.goto('/admin')
    await page.getByRole('tab', { name: 'KI-Provider' }).click()
    await expect(page.getByText('Agent-Richtlinie')).toBeVisible()
    // The registry renders (a known read tool + a mutating one).
    await expect(page.getByText('get_overview')).toBeVisible()
    await expect(page.getByText('create_downtime')).toBeVisible()
    // Toggle a tool off and save.
    await page.getByLabel('get_overview Aktiv').click()
    await page.getByTestId('admin-save-policy').click()
    await expect(page.getByText('Laden fehlgeschlagen.')).toHaveCount(0)
    // Persisted: reload shows the switch off.
    await page.reload()
    await page.getByRole('tab', { name: 'KI-Provider' }).click()
    await expect(page.getByLabel('get_overview Aktiv')).not.toBeChecked()
    // Restore for other tests.
    await page.getByLabel('get_overview Aktiv').click()
    await page.getByTestId('admin-save-policy').click()
  })
})
