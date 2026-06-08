// Admin "communications" surface E2E (SPEC §12.3): drives the real
// `northplaned --demo` binary + embedded shadcn SPA as the admin role and
// exercises the /admin tabs that own the notification fan-out config —
// Kontakte, Kontaktgruppen, Kanäle, Event-Quellen, Webhooks, Heartbeats.
// Each tab gets a full CRUD round-trip (or a meaningful smoke + create/delete)
// with unique names so the suite is independent and re-runnable on the one
// shared demo DB; everything created is also deleted.
import { test, expect, type Page } from '@playwright/test'
import { authFile } from './lib/roles'

test.use({ storageState: authFile('admin') })

// Unique suffix per spawned worker+run so parallel PW_PORT suites and reruns
// against the same DB never collide on resource names.
const uniq = () => `${Date.now().toString(36)}${Math.floor(Math.random() * 1e4).toString(36)}`

// The admin forms use a custom Field component (kit.tsx) whose <Label> is NOT
// associated with its <Input> (no htmlFor/id) — so getByLabel cannot find the
// control. Instead we scope to the Field wrapper (a div carrying both the label
// text and, in a sibling div, the control) and pull its textbox. Matching the
// label exactly avoids "Name" also hitting "Benutzername" etc.
function field(scope: ReturnType<Page['locator']>, label: string) {
  // The Field wrapper (div.block.text-sm) holds the label text + the control;
  // required fields render the label as "<label> *", so anchor on the start of
  // the wrapper's text rather than an exact match.
  return scope
    .locator('div.block.text-sm')
    .filter({ hasText: new RegExp(`^${label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`) })
    .getByRole('textbox')
}

// Open /admin and switch to the named tab. Tabs are shadcn TabsTrigger →
// role="tab"; their accessible names are the exact German i18n strings.
async function openAdminTab(page: Page, tabName: string | RegExp) {
  await page.goto('/admin')
  await expect(page).not.toHaveURL(/\/login/)
  const tab = page.getByRole('tab', { name: tabName })
  await expect(tab).toBeVisible()
  await tab.click()
}

// The confirm-armed DeleteButton: first "Löschen" arms, then "Wirklich löschen?"
// commits. We scope to the row to avoid hitting another row's button.
async function deleteRow(row: ReturnType<Page['locator']>) {
  await row.getByRole('button', { name: 'Löschen', exact: true }).click()
  await row.getByRole('button', { name: 'Wirklich löschen?' }).click()
}

test.describe('admin · contacts', () => {
  test('lists demo contacts, then creates / edits / deletes a contact', async ({ page }) => {
    await openAdminTab(page, 'Kontakte')

    // Demo seed contacts are listed.
    await expect(page.getByRole('cell', { name: 'demo-alice', exact: true })).toBeVisible()
    await expect(page.getByRole('cell', { name: 'demo-bob', exact: true })).toBeVisible()

    const name = `e2e-contact-${uniq()}`
    const email = `${name}@example.test`

    // CREATE — "Anlegen" opens the dialog; fill name + email + add a
    // notification preference whose first channel is "email".
    await page.getByRole('button', { name: 'Anlegen' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Anlegen' })).toBeVisible()
    await field(dialog, 'Name').fill(name)
    await field(dialog, 'E-Mail').fill(email)
    // PreferencesEditor: "Hinzufügen" appends a default-profile row, then the
    // ChannelTypePicker offers "+ email" as a clickable chip.
    await dialog.getByRole('button', { name: 'Hinzufügen' }).click()
    await dialog.getByRole('button', { name: '+ email' }).click()
    await dialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(dialog).toBeHidden()

    // Listed with the email + a non-zero profile count.
    const row = page.getByRole('row', { name: new RegExp(name) })
    await expect(row).toBeVisible()
    await expect(row.getByRole('cell', { name: email })).toBeVisible()

    // EDIT — change the e-mail. Name field is locked on edit (disabled).
    const newEmail = `edited-${name}@example.test`
    await row.getByRole('button', { name: 'Bearbeiten' }).click()
    const editDialog = page.getByRole('dialog')
    await expect(editDialog.getByRole('heading', { name: `Bearbeiten: ${name}` })).toBeVisible()
    const emailField = field(editDialog, 'E-Mail')
    await emailField.fill(newEmail)
    await editDialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(editDialog).toBeHidden()
    await expect(page.getByRole('cell', { name: newEmail })).toBeVisible()

    // DELETE.
    await deleteRow(page.getByRole('row', { name: new RegExp(name) }))
    await expect(page.getByRole('cell', { name, exact: true })).toBeHidden()
  })
})

test.describe('admin · contact groups', () => {
  test('lists demo-ops, then creates a group with a member and deletes it', async ({ page }) => {
    await openAdminTab(page, 'Kontaktgruppen')

    await expect(page.getByRole('cell', { name: 'demo-ops', exact: true })).toBeVisible()

    const name = `e2e-group-${uniq()}`

    // CREATE — name + one member via the ListEditor (type a contact, Enter/Add).
    await page.getByRole('button', { name: 'Anlegen' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Anlegen' })).toBeVisible()
    await field(dialog, 'Name').fill(name)
    const memberInput = dialog.getByPlaceholder('Kontakt…')
    await memberInput.fill('demo-alice')
    await dialog.getByRole('button', { name: 'Hinzufügen' }).click()
    // The added member appears as a chip inside the dialog.
    await expect(dialog.getByText('demo-alice')).toBeVisible()
    await dialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(dialog).toBeHidden()

    // Listed, member shown in the "Mitglieder" cell.
    const row = page.getByRole('row', { name: new RegExp(name) })
    await expect(row).toBeVisible()
    await expect(row.getByRole('cell', { name: /demo-alice/ })).toBeVisible()

    // DELETE.
    await deleteRow(page.getByRole('row', { name: new RegExp(name) }))
    await expect(page.getByRole('cell', { name, exact: true })).toBeHidden()
  })
})

test.describe('admin · channels', () => {
  test('lists demo channels, creates one via type Select, test-sends, deletes', async ({ page }) => {
    await openAdminTab(page, 'Kanäle')

    await expect(page.getByRole('cell', { name: 'demo-email', exact: true })).toBeVisible()
    await expect(page.getByRole('cell', { name: 'demo-hook', exact: true })).toBeVisible()

    const name = `e2e-channel-${uniq()}`

    // CREATE — pick "webhook" via the shadcn type Select, fill its minimal
    // config (URL), keep enabled, save.
    await page.getByRole('button', { name: 'Anlegen' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Anlegen' })).toBeVisible()
    await field(dialog, 'Name').fill(name)
    // Type Select (defaults to "email") → switch to "webhook".
    await dialog.getByRole('combobox').click()
    await page.getByRole('option', { name: 'webhook', exact: true }).click()
    // The webhook config block now renders a "URL" field.
    await field(dialog, 'URL').fill('http://127.0.0.1:65535/never')
    await dialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(dialog).toBeHidden()

    // Listed with its type badge.
    const row = page.getByRole('row', { name: new RegExp(name) })
    await expect(row).toBeVisible()
    await expect(row.getByText('webhook')).toBeVisible()

    // TEST SENDEN on the seeded demo-email channel: clicking fires the
    // POST /channels/{name}:test-notification round-trip and the TestButton
    // renders the outcome inline next to the button. We assert the feedback
    // surfaces — a success "✓ sent …" or, if the demo mock SMTP isn't
    // listening in this run, the backend's "✕ … failed" — either proves the
    // test-send wired button → API → rendered result.
    const emailRow = page.getByRole('row', { name: /demo-email/ })
    await emailRow.getByRole('button', { name: 'Test senden' }).click()
    await expect(emailRow.getByText(/[✓✕]/)).toBeVisible({ timeout: 15_000 })

    // DELETE the created channel.
    await deleteRow(page.getByRole('row', { name: new RegExp(name) }))
    await expect(page.getByRole('cell', { name, exact: true })).toBeHidden()
  })
})

test.describe('admin · event sources', () => {
  test('lists demo sources, creates a webhook source, toggles enable, deletes', async ({ page }) => {
    await openAdminTab(page, 'Event-Quellen')

    await expect(page.getByRole('cell', { name: 'demo-hook-in', exact: true })).toBeVisible()
    await expect(page.getByRole('cell', { name: 'demo-traps', exact: true })).toBeVisible()

    const name = `e2e-source-${uniq()}`

    // CREATE — default type is "webhook"; flip the enabled Switch off so we can
    // assert the disabled status badge, then save.
    await page.getByRole('button', { name: 'Anlegen' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Anlegen' })).toBeVisible()
    await field(dialog, 'Name').fill(name)
    // The "Aktiv" Switch starts on (checked); toggle it off.
    const enableSwitch = dialog.getByRole('switch')
    await expect(enableSwitch).toBeChecked()
    await enableSwitch.click()
    await expect(enableSwitch).not.toBeChecked()
    await dialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(dialog).toBeHidden()

    // Listed and shows the "Deaktiviert" status (we created it disabled).
    const row = page.getByRole('row', { name: new RegExp(name) })
    await expect(row).toBeVisible()
    await expect(row.getByText('Deaktiviert')).toBeVisible()

    // TOGGLE — re-open, enable, save; status flips to "Aktiv".
    await row.getByRole('button', { name: 'Bearbeiten' }).click()
    const editDialog = page.getByRole('dialog')
    const editSwitch = editDialog.getByRole('switch')
    await expect(editSwitch).not.toBeChecked()
    await editSwitch.click()
    await expect(editSwitch).toBeChecked()
    await editDialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(editDialog).toBeHidden()
    await expect(page.getByRole('row', { name: new RegExp(name) }).getByText('Aktiv')).toBeVisible()

    // DELETE.
    await deleteRow(page.getByRole('row', { name: new RegExp(name) }))
    await expect(page.getByRole('cell', { name, exact: true })).toBeHidden()
  })
})

test.describe('admin · integrations (webhooks)', () => {
  test('webhooks tab renders, creates an outgoing webhook and deletes it', async ({ page }) => {
    await openAdminTab(page, 'Webhooks')

    const name = `e2e-webhook-${uniq()}`
    const url = 'https://example.net/e2e-hook'

    // CREATE — name + URL are both required.
    await page.getByRole('button', { name: 'Anlegen' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Anlegen' })).toBeVisible()
    await field(dialog, 'Name').fill(name)
    await field(dialog, 'URL').fill(url)
    await dialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(dialog).toBeHidden()

    // Listed with its URL.
    const row = page.getByRole('row', { name: new RegExp(name) })
    await expect(row).toBeVisible()
    await expect(row.getByRole('cell', { name: url })).toBeVisible()

    // DELETE.
    await deleteRow(page.getByRole('row', { name: new RegExp(name) }))
    await expect(page.getByRole('cell', { name, exact: true })).toBeHidden()
  })
})

test.describe('admin · integrations (heartbeats)', () => {
  test('heartbeats tab renders, creates a dead-man heartbeat and deletes it', async ({ page }) => {
    await openAdminTab(page, 'Heartbeats')

    const name = `e2e-hb-${uniq()}`

    // CREATE — name + "Erwartet alle" (duration) are required; the form
    // pre-fills "1h" so just set a name and save.
    await page.getByRole('button', { name: 'Anlegen' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Anlegen' })).toBeVisible()
    await field(dialog, 'Name').fill(name)
    await field(dialog, 'Erwartet alle').fill('30m')
    await dialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(dialog).toBeHidden()

    // Listed; a brand-new heartbeat has never beaten ("nie").
    const row = page.getByRole('row', { name: new RegExp(name) })
    await expect(row).toBeVisible()
    await expect(row.getByText('nie')).toBeVisible()

    // DELETE.
    await deleteRow(page.getByRole('row', { name: new RegExp(name) }))
    await expect(page.getByRole('cell', { name, exact: true })).toBeHidden()
  })
})
