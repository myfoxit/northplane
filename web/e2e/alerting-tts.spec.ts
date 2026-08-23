import { test, expect, type Page } from '@playwright/test'
import { authFile } from './lib/roles'

// Text-to-speech profiles E2E: the fifth /alerting tab, "Sprachausgabe (TTS)".
// Drives the real `northplaned --demo` binary + embedded SPA against the
// de-DE strings from src/i18n.ts. Creating/editing profiles needs config:write
// → admin. The normalise-only preview (POST /tts:normalize) needs no TTS
// engine, so the whole flow — create, dry-run preview, delete — runs offline.

test.use({ viewport: { width: 1440, height: 1800 } })

const uniq = (prefix: string) =>
  `${prefix}-${Date.now().toString(36)}-${Math.floor(Math.random() * 1e4).toString(36)}`

async function gotoTTS(page: Page) {
  await page.goto('/alerting')
  const tab = page.getByRole('tab', { name: 'Sprachausgabe (TTS)' })
  await expect(tab).toBeVisible()
  await tab.click()
  await expect(tab).toHaveAttribute('data-state', 'active')
}

async function clickInDialog(page: Page, name: string | RegExp) {
  const btn = page.getByRole('dialog').getByRole('button', { name })
  await expect(btn).toBeVisible()
  await expect(btn).toBeEnabled()
  await btn.scrollIntoViewIfNeeded()
  await btn.click({ force: true })
}

test.describe('Alerting · Sprachausgabe (TTS profiles)', () => {
  test.use({ storageState: authFile('admin') })

  test('creates a profile, previews the normalised text and deletes it', async ({ page }) => {
    const name = uniq('e2e-tts')
    await gotoTTS(page)

    // — CREATE —
    await page.getByRole('button', { name: 'Anlegen' }).click()
    let dialog = page.getByRole('dialog')
    await dialog.getByPlaceholder('default').first().fill(name)
    // default language German, a lexicon entry np-01 → Server eins
    await dialog.getByPlaceholder('de-DE').first().fill('de-DE')
    await clickInDialog(page, '+ Wort')
    await dialog.getByLabel('Wort 1').fill('np-01')
    await dialog.getByLabel('gesprochen als 1').fill('Server eins')

    // — PREVIEW (dry run, no engine) — the unsaved profile travels inline.
    // the preview text is the dialog's only textarea
    await dialog.locator('textarea').fill('Festplatte auf np-01 zu 95% voll')
    await clickInDialog(page, 'Nur normalisieren')
    await expect(dialog.getByText('Festplatte auf Server eins zu 95 Prozent voll.')).toBeVisible()
    await expect(dialog.getByText('de-DE', { exact: true }).first()).toBeVisible()

    // Save.
    await clickInDialog(page, 'Speichern')
    await expect(dialog).toBeHidden()
    await expect(page.getByRole('cell', { name, exact: true })).toBeVisible()

    // — EDIT — reopen: name locked, lexicon row persisted.
    await page.getByRole('row', { name: new RegExp(name) }).getByRole('button', { name: 'Bearbeiten' }).click()
    dialog = page.getByRole('dialog')
    await expect(dialog.getByText(`Bearbeiten: ${name}`)).toBeVisible()
    await expect(dialog.getByLabel('Wort 1')).toHaveValue('np-01')

    // — DELETE —
    await clickInDialog(page, 'Löschen')
    await clickInDialog(page, 'Wirklich löschen?')
    await expect(dialog).toBeHidden()
    await expect(page.getByRole('cell', { name, exact: true })).toHaveCount(0)
  })
})
