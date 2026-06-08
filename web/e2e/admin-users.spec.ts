// Admin → /admin (ADMIN role). Exercises the full system-administration
// surface end-to-end against the real `northplaned --demo` binary: the Users,
// Roles, Tenants and Secrets tabs. Each test is independent, uses unique
// names/emails, and cleans up what it creates. The last-admin guard is only
// *asserted* — admin access is never actually removed.
import { test, expect, type Page } from '@playwright/test'
import { authFile } from './lib/roles'

test.use({ storageState: authFile('admin') })

// — helpers ————————————————————————————————————————————————————————————

// Open /admin and switch to the named tab (German tab labels from i18n.ts).
async function openTab(page: Page, tab: string) {
  await page.goto('/admin')
  await expect(page.getByRole('heading', { name: 'Administration' })).toBeVisible()
  await page.getByRole('tab', { name: tab, exact: true }).click()
}

// The row <tr> whose any cell contains the given text (email / name / key).
function rowWith(page: Page, text: string) {
  return page.getByRole('row').filter({ hasText: text })
}

// The form <input> belonging to a Field with the given label. The kit Field
// renders <label data-slot="label">{label}</label> followed by the control,
// but without an htmlFor/id association — so getByLabel can't bind them. We
// anchor on the exact label text and take the next input in document order.
function fieldInput(dialog: ReturnType<Page['getByRole']>, label: string) {
  return dialog
    .locator('label[data-slot="label"]', { hasText: new RegExp(`^${label}\\s*\\*?$`) })
    .locator('xpath=following::input[1]')
}

// Add a value to a ListEditor (roles / permissions): the editor input is the
// last input inside the Field, typed + Enter commits a chip.
async function addToList(dialog: ReturnType<Page['getByRole']>, label: string, value: string) {
  const input = dialog
    .locator('label[data-slot="label"]', { hasText: new RegExp(`^${label}\\s*`) })
    .locator('xpath=following::input[1]')
  await input.fill(value)
  await input.press('Enter')
}

// — USERS tab ——————————————————————————————————————————————————————————

test.describe('Admin · Users', () => {
  test('lists the seeded admin / operator / viewer users', async ({ page }) => {
    await openTab(page, 'Benutzer')
    await expect(page.getByText('admin@e2e.local')).toBeVisible()
    await expect(page.getByText('operator@demo.local')).toBeVisible()
    await expect(page.getByText('viewer@demo.local')).toBeVisible()
  })

  test('creates a local user that appears in the list', async ({ page }) => {
    const email = `e2e-user-${Date.now()}@test.local`
    await openTab(page, 'Benutzer')

    await page.getByRole('button', { name: 'Benutzer anlegen' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Benutzer anlegen' })).toBeVisible()

    await fieldInput(dialog, 'Name').fill('E2E Created User')
    await fieldInput(dialog, 'E-Mail').fill(email)
    await fieldInput(dialog, 'Passwort').fill('super-secret-pw-123')
    // Assign the viewer role via the ListEditor.
    await addToList(dialog, 'Berechtigungen', 'viewer')

    await dialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(dialog).toBeHidden()

    await expect(page.getByText(email)).toBeVisible()
    await expect(rowWith(page, email)).toContainText('viewer')

    // cleanup
    await deleteUser(page, email)
  })

  test('disables then re-enables a user (toggle reflects state)', async ({ page }) => {
    const email = `e2e-user-${Date.now()}@test.local`
    await openTab(page, 'Benutzer')
    await createUser(page, email, 'Toggle User', 'viewer')

    // Starts enabled.
    await expect(rowWith(page, email)).toContainText('Aktiv')

    // Disable via the edit dialog's Switch.
    await rowWith(page, email).getByRole('button', { name: 'Bearbeiten' }).click()
    let dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('switch')).not.toBeChecked()
    await dialog.getByRole('switch').click()
    await expect(dialog.getByRole('switch')).toBeChecked()
    await dialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(dialog).toBeHidden()
    await expect(rowWith(page, email)).toContainText('Deaktiviert')

    // Re-enable.
    await rowWith(page, email).getByRole('button', { name: 'Bearbeiten' }).click()
    dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('switch')).toBeChecked()
    await dialog.getByRole('switch').click()
    await expect(dialog.getByRole('switch')).not.toBeChecked()
    await dialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(dialog).toBeHidden()
    await expect(rowWith(page, email)).toContainText('Aktiv')

    // cleanup
    await deleteUser(page, email)
  })

  test('admin can reset a user password (set-password)', async ({ page }) => {
    const email = `e2e-user-${Date.now()}@test.local`
    await openTab(page, 'Benutzer')
    await createUser(page, email, 'Reset User', 'viewer')

    await rowWith(page, email).getByRole('button', { name: 'Passwort setzen' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: /Passwort setzen/ })).toBeVisible()
    await fieldInput(dialog, 'Passwort').fill('a-brand-new-pw-456')
    await dialog.getByRole('button', { name: 'Passwort setzen' }).click()

    // Success = dialog closes with no FormError; the user remains listed.
    await expect(dialog).toBeHidden()
    await expect(page.getByText(email)).toBeVisible()

    // cleanup
    await deleteUser(page, email)
  })

  test('deletes a user (two-click confirm) so it disappears', async ({ page }) => {
    const email = `e2e-user-${Date.now()}@test.local`
    await openTab(page, 'Benutzer')
    await createUser(page, email, 'Delete User', 'viewer')
    await expect(page.getByText(email)).toBeVisible()

    await deleteUser(page, email)
    await expect(page.getByText(email)).toHaveCount(0)
  })

  test('last-admin guard: disabling the only admin is rejected', async ({ page }) => {
    await openTab(page, 'Benutzer')
    const adminRow = rowWith(page, 'admin@e2e.local')
    await expect(adminRow).toContainText('Aktiv')

    // Attempt to disable the sole admin via the edit dialog.
    await adminRow.getByRole('button', { name: 'Bearbeiten' }).click()
    const dialog = page.getByRole('dialog')
    await dialog.getByRole('switch').click()
    await expect(dialog.getByRole('switch')).toBeChecked()
    await dialog.getByRole('button', { name: 'Speichern' }).click()

    // Backend returns 409 np:users/last-admin → the FormError banner surfaces
    // ("cannot remove the last enabled local administrator"), the dialog stays
    // open, and the admin is still Aktiv (access is never removed).
    await expect(
      dialog.getByText(/last enabled local administrator/i),
    ).toBeVisible()
    await expect(dialog).toBeVisible()

    // Abort without persisting the (rejected) change.
    await dialog.getByRole('button', { name: 'Abbrechen' }).click()
    await expect(dialog).toBeHidden()
    await expect(rowWith(page, 'admin@e2e.local')).toContainText('Aktiv')
  })
})

// — ROLES tab ——————————————————————————————————————————————————————————

test.describe('Admin · Roles', () => {
  test('lists the seeded roles', async ({ page }) => {
    await openTab(page, 'Rollen')
    for (const role of ['admin', 'operator', 'viewer', 'ai-agent']) {
      await expect(rowWith(page, role).first()).toBeVisible()
    }
  })

  test('creates a role with a permission then deletes it', async ({ page }) => {
    const name = `e2e-role-${Date.now()}`
    await openTab(page, 'Rollen')

    await page.getByRole('button', { name: 'Anlegen' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Anlegen' })).toBeVisible()

    await fieldInput(dialog, 'Name').fill(name)
    await addToList(dialog, 'Berechtigungen', 'objects:read')
    await dialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(dialog).toBeHidden()

    const row = rowWith(page, name)
    await expect(row).toBeVisible()
    await expect(row).toContainText('objects:read')

    // delete (two-click confirm)
    await row.getByRole('button', { name: 'Löschen' }).click()
    await row.getByRole('button', { name: 'Wirklich löschen?' }).click()
    await expect(page.getByText(name)).toHaveCount(0)
  })
})

// — TENANTS tab ————————————————————————————————————————————————————————

test.describe('Admin · Tenants', () => {
  test('renders and creates a tenant', async ({ page }) => {
    const slug = `e2e-tenant-${Date.now()}`
    await openTab(page, 'Mandanten')

    // The table header proves the tab rendered.
    await expect(page.getByRole('columnheader', { name: 'Slug' })).toBeVisible()

    await page.getByRole('button', { name: 'Anlegen' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: /Mandanten/ })).toBeVisible()
    await fieldInput(dialog, 'Name').fill('E2E Tenant')
    await fieldInput(dialog, 'Slug').fill(slug)
    await dialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(dialog).toBeHidden()

    // Tenants have no delete surface (API limitation) — just assert it landed.
    await expect(page.getByText(slug)).toBeVisible()
  })
})

// — SECRETS tab ————————————————————————————————————————————————————————

test.describe('Admin · Secrets', () => {
  test('renders, creates a write-only secret, then deletes it', async ({ page }) => {
    const key = `e2e-secret-${Date.now()}`
    await openTab(page, 'Secrets')

    await expect(page.getByText(/\$SECRET:name\$/)).toBeVisible()

    await page.getByRole('button', { name: 'Anlegen' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: /Secrets/ })).toBeVisible()
    await fieldInput(dialog, 'Name').fill(key)
    await fieldInput(dialog, 'Wert').fill('top-secret-value')
    await dialog.getByRole('button', { name: 'Speichern' }).click()
    await expect(dialog).toBeHidden()

    const row = rowWith(page, key)
    await expect(row).toBeVisible()
    // Value is write-only: only the name + $SECRET:name$ reference are shown.
    await expect(row).toContainText(`$SECRET:${key}$`)
    await expect(row).not.toContainText('top-secret-value')

    // delete (two-click confirm)
    await row.getByRole('button', { name: 'Löschen' }).click()
    await row.getByRole('button', { name: 'Wirklich löschen?' }).click()
    await expect(page.getByText(key, { exact: true })).toHaveCount(0)
  })
})

// — shared user create/delete flows ———————————————————————————————————————

async function createUser(page: Page, email: string, name: string, role: string) {
  await page.getByRole('button', { name: 'Benutzer anlegen' }).click()
  const dialog = page.getByRole('dialog')
  await fieldInput(dialog, 'Name').fill(name)
  await fieldInput(dialog, 'E-Mail').fill(email)
  await fieldInput(dialog, 'Passwort').fill('super-secret-pw-123')
  await addToList(dialog, 'Berechtigungen', role)
  await dialog.getByRole('button', { name: 'Speichern' }).click()
  await expect(dialog).toBeHidden()
  await expect(page.getByText(email)).toBeVisible()
}

async function deleteUser(page: Page, email: string) {
  const row = rowWith(page, email)
  await row.getByRole('button', { name: 'Löschen' }).click()
  await row.getByRole('button', { name: 'Wirklich löschen?' }).click()
  await expect(page.getByText(email)).toHaveCount(0)
}
