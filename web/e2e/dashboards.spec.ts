import { test, expect, type Page } from '@playwright/test'
import { authFile } from './lib/roles'

// Dashboards E2E. Drives the real `northplaned --demo` binary + embedded
// shadcn SPA through the German UI.
//
// Demo seed (internal/demo/demo.go) ships a SHARED dashboard `demo-overview`
// with five widgets: counters ("State summary"), problems ("Open problems"),
// metric ("Web latency"), bpi ("Webshop health", service demo-webshop) and a
// table ("Demo-Inventar", selector demo=true). The BPI tree root is
// `demo-webshop` with leaves demo-webshop-web / -dns / -gateway; the table
// lists the demo-* objects.
//
// ROLES: reads run as the default `operator`. WRITES (create/update/delete)
// run as `admin`: the dashboards CRUD is registered under the generic "config"
// permission group (api/reports.go: resourceCRUD("dashboards", …, "config")),
// so a write needs `config:write` — which the operator role does NOT carry
// (model/admin.go BuiltinRoles grants operator only dashboards:read/write, but
// the endpoint gates on config:write). The mutating describe block therefore
// opts into the admin storage state; the operator POST 403s ("missing
// permission — config:write").
//
// The dashboard editor saves widgets with an ETag/If-Match PUT and a 409
// re-fetch+retry (Dashboards.tsx `save`), so the rename/persistence test
// reloads the page and re-asserts from the server copy — no fixed sleeps.

// ——— helpers ————————————————————————————————————————————————

// Open the create dialog, type a name, submit, and wait for the SPA to
// navigate to the new dashboard's grid view (which always starts with one
// counters widget).
async function createDashboard(page: Page, name: string): Promise<void> {
  await page.goto('/dashboards')
  await page.getByRole('button', { name: 'Dashboard anlegen' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('heading', { name: 'Dashboard anlegen' })).toBeVisible()
  // The name is the autoFocused text input in the dialog (Field's Label isn't
  // htmlFor-associated, so target the textbox directly).
  await dialog.getByRole('textbox').first().fill(name)
  await dialog.getByRole('button', { name: 'Anlegen' }).click()
  // create.onSuccess navigates to /dashboards/$name.
  await page.waitForURL(`**/dashboards/${encodeURIComponent(name)}`)
  await expect(page.getByRole('heading', { name })).toBeVisible()
}

// Delete a dashboard from the list via the inline-confirm DeleteButton on its
// card. We scope to the card so we don't arm the wrong row.
async function deleteDashboard(page: Page, name: string): Promise<void> {
  await page.goto('/dashboards')
  const card = page
    .locator('.grid > div', { has: page.getByRole('link', { name }) })
    .first()
  await card.getByRole('button', { name: 'Löschen' }).click()
  await card.getByRole('button', { name: 'Wirklich löschen?' }).click()
  await expect(page.getByRole('link', { name })).toHaveCount(0)
}

// ——— shared demo dashboard (read) ——————————————————————————————

test.describe('demo-overview dashboard', () => {
  test('list shows the shared demo-overview dashboard', async ({ page }) => {
    await page.goto('/dashboards')
    await expect(page.getByRole('heading', { name: 'Dashboards' })).toBeVisible()
    // The seeded dashboard appears as a card link, badged "geteilt" (shared).
    const link = page.getByRole('link', { name: 'demo-overview', exact: true })
    await expect(link).toBeVisible()
    const card = page.locator('.grid > div', { has: link }).first()
    await expect(card.getByText('geteilt')).toBeVisible()
  })

  test('opening demo-overview renders all seeded widgets with demo data', async ({ page }) => {
    await page.goto('/dashboards')
    await page.getByRole('link', { name: 'demo-overview', exact: true }).click()
    await page.waitForURL('**/dashboards/demo-overview')
    await expect(page.getByRole('heading', { name: 'demo-overview' })).toBeVisible()

    // Widget headers carry the seeded titles (Dashboards.tsx renders
    // `wd.title || widgetTypeLabel(wd.type)` in each widget chrome).
    await expect(page.getByText('State summary')).toBeVisible() // counters
    await expect(page.getByText('Open problems')).toBeVisible() // problems
    await expect(page.getByText('Web latency')).toBeVisible() // metric chart
    await expect(page.getByText('Webshop health')).toBeVisible() // bpi
    await expect(page.getByText('Demo-Inventar')).toBeVisible() // table

    // Counters widget: KPI tiles drill into the objects/alerts lists. The
    // "Hosts UP" tile is a link into /objects.
    await expect(page.getByRole('link', { name: /Hosts UP/ }).first()).toBeVisible()
    await expect(page.getByRole('link', { name: /Services OK/ }).first()).toBeVisible()

    // BPI widget: the business-service tree renders the seeded root and its
    // leaves by name.
    await expect(page.getByText('demo-webshop', { exact: true })).toBeVisible()
    await expect(page.getByText('demo-webshop-web')).toBeVisible()
    await expect(page.getByText('SLA 99.9%')).toBeVisible()

    // Table widget ("Demo-Inventar", selector demo=true): demo objects render
    // as drill-down rows. The state header column proves it's the live table.
    await expect(page.getByRole('columnheader', { name: 'Zustand' })).toBeVisible()
    await expect(page.getByRole('columnheader', { name: 'Ausgabe' })).toBeVisible()
    await expect(page.getByRole('link', { name: /demo-gateway/ }).first()).toBeVisible()
  })

  test('demo-overview opens as a chrome-free wallboard', async ({ page }) => {
    await page.goto('/dashboards/demo-overview?wallboard')
    // Wallboard strips the editor controls (no "Bearbeiten" button) and shows
    // the title large with the live clock.
    await expect(page.getByRole('heading', { name: 'demo-overview' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Bearbeiten' })).toHaveCount(0)
    // Widget content still renders (the seeded titles are present).
    await expect(page.getByText('Webshop health')).toBeVisible()
    await expect(page.getByText('Demo-Inventar')).toBeVisible()
  })
})

// ——— full CRUD lifecycle on an operator-created dashboard ——————————

test.describe('dashboard lifecycle (admin)', () => {
  // Dashboard writes need config:write, which only the admin role carries.
  test.use({ storageState: authFile('admin') })

  test('create → add widget → edit/rename → persist → delete', async ({ page }) => {
    const name = `e2e-dash-${Date.now()}`

    // CREATE — opens straight into the grid view with one counters widget.
    await createDashboard(page, name)
    await expect(page.getByText('Zähler (KPIs)')).toBeVisible()

    // It also shows up back on the list with the "1 Widget" count.
    await page.goto('/dashboards')
    await expect(page.getByRole('link', { name, exact: true })).toBeVisible()

    // ADD A WIDGET — enter edit mode, open the add-widget dialog, pick the
    // "Probleme" (problems) type via the shadcn Select, give it a unique
    // title, and append it.
    await page.getByRole('link', { name, exact: true }).click()
    await page.waitForURL(`**/dashboards/${encodeURIComponent(name)}`)
    await page.getByRole('button', { name: 'Bearbeiten' }).click()
    await page.getByRole('button', { name: 'Widget hinzufügen' }).click()

    const addDialog = page.getByRole('dialog')
    await expect(addDialog.getByRole('heading', { name: 'Widget hinzufügen' })).toBeVisible()
    // The type is picked from the icon-card gallery (each type is a button).
    await addDialog.getByRole('button', { name: 'Probleme', exact: true }).click()
    const widgetTitle = `E2E-Probleme-${Date.now()}`
    // The "Titel (optional)" input carries the type label as its placeholder
    // ("Probleme" once the problems type is selected).
    await addDialog.getByPlaceholder('Probleme').fill(widgetTitle)
    await addDialog.getByRole('button', { name: 'Hinzufügen' }).click()

    // The new widget appears in the (still-editing) draft grid.
    await expect(page.getByText(widgetTitle)).toBeVisible()

    // EDIT — rename the COUNTERS widget via its panel config dialog (opened
    // from the panel's hover toolbar), so we exercise both an added widget and
    // a renamed existing one in one save. The counters panel is the first one
    // (layout is index-sorted), so its "Konfigurieren" button is .first().
    const renamed = `Übersicht-${Date.now()}`
    await page.getByRole('button', { name: 'Konfigurieren' }).first().click()
    const editDialog = page.getByRole('dialog')
    await expect(editDialog.getByRole('heading', { name: /Panel bearbeiten/ })).toBeVisible()
    await editDialog.getByPlaceholder('Zähler (KPIs)').fill(renamed)
    await editDialog.getByRole('button', { name: 'Fertig' }).click()

    // SAVE — goes through the ETag/If-Match PUT (409 → re-fetch + retry).
    await page.getByRole('button', { name: 'Speichern' }).click()
    // Save exits edit mode (onDone clears draft + editing) → the "Bearbeiten"
    // button comes back.
    await expect(page.getByRole('button', { name: 'Bearbeiten' })).toBeVisible()

    // PERSIST — reload and re-assert from the server copy.
    await page.reload()
    await expect(page.getByText(renamed)).toBeVisible()
    await expect(page.getByText(widgetTitle)).toBeVisible()
    // The list now reports two widgets for this dashboard.
    await page.goto('/dashboards')
    const card = page
      .locator('.grid > div', { has: page.getByRole('link', { name, exact: true }) })
      .first()
    await expect(card.getByText('2 Widgets')).toBeVisible()

    // DELETE — remove it and confirm it's gone from the list.
    await deleteDashboard(page, name)
  })

  test('free layout: resize a panel + persist positions and time/refresh', async ({ page }) => {
    const name = `e2e-grid-${Date.now()}`
    await createDashboard(page, name) // one counters widget (w:12)

    // Add a gauge (w:3) so we have a non-full-width panel to resize.
    await page.getByRole('button', { name: 'Bearbeiten' }).click()
    await page.getByRole('button', { name: 'Widget hinzufügen' }).click()
    const addDialog = page.getByRole('dialog')
    await addDialog.getByRole('button', { name: 'Gauge (Tacho)', exact: true }).click()
    await addDialog.getByRole('button', { name: 'Hinzufügen' }).click()

    // Set a non-default dashboard time range + refresh (DashControls).
    await page.getByRole('combobox', { name: 'Zeitraum' }).click()
    await page.getByRole('option', { name: '24h' }).click()
    await page.getByRole('combobox', { name: 'Aktualisierungsintervall' }).click()
    await page.getByRole('option', { name: '10 s' }).click()

    // RESIZE the gauge (2nd panel → 2nd resize grip) wider via a pointer drag.
    // Let the layout settle (add-dialog close + panel reflow transitions) so
    // the grip's measured position is stable before we grab it.
    await page.waitForTimeout(400)
    const gaugePanel = page.locator('[class*="group/panel"]').filter({ hasText: 'Gauge (Tacho)' })
    const wBefore = (await gaugePanel.boundingBox())!.width
    const grip = page.getByTitle('Größe ändern').nth(1)
    const box = await grip.boundingBox()
    if (!box) throw new Error('resize grip not found')
    const cx = box.x + box.width / 2, cy = box.y + box.height / 2
    await page.mouse.move(cx, cy)
    await page.mouse.down()
    await page.mouse.move(cx + 60, cy + 30, { steps: 4 })
    await page.mouse.move(cx + 260, cy + 140, { steps: 12 })
    await page.mouse.up()
    const wAfter = (await gaugePanel.boundingBox())!.width
    expect(wAfter, `gauge resized wider ${wBefore}→${wAfter}`).toBeGreaterThan(wBefore + 20)

    // SAVE → reload → assert from the persisted server doc via the API.
    await page.getByRole('button', { name: 'Speichern' }).click()
    await expect(page.getByRole('button', { name: 'Bearbeiten' })).toBeVisible()

    const res = await page.request.get(`/api/v1/dashboards/${encodeURIComponent(name)}`)
    expect(res.ok()).toBeTruthy()
    const doc = await res.json()
    expect(doc.spec.time).toBe('24h')
    expect(doc.spec.refresh).toBe('10s')
    expect(doc.spec.widgets).toHaveLength(2)
    // Every widget carries explicit grid coordinates now.
    for (const w of doc.spec.widgets) {
      expect(typeof w.x).toBe('number')
      expect(typeof w.y).toBe('number')
      expect(typeof w.w).toBe('number')
      expect(typeof w.h).toBe('number')
    }
    // The gauge was dragged wider (started at w:3).
    const gauge = doc.spec.widgets.find((w: { type: string }) => w.type === 'gauge')
    expect(gauge.w).toBeGreaterThan(3)
    // The gauge sits below the full-width counters row (gravity-packed).
    expect(gauge.y).toBeGreaterThan(0)

    await deleteDashboard(page, name)
  })

  test('a created dashboard can be deleted independently', async ({ page }) => {
    const name = `e2e-dash-del-${Date.now()}`
    await createDashboard(page, name)

    await page.goto('/dashboards')
    await expect(page.getByRole('link', { name, exact: true })).toBeVisible()

    await deleteDashboard(page, name)
    // Hard-reload to prove the deletion is server-side, not just cache.
    await page.reload()
    await expect(page.getByRole('link', { name, exact: true })).toHaveCount(0)
  })
})
