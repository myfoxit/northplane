// E2E coverage for the Objects explorer (src/pages/Objects.tsx) driven through
// the real `northplaned --demo` binary. Operator role (read/write) is the
// Playwright default storageState. The UI renders in German (locale de-DE), so
// every selector below uses the exact strings from src/i18n.ts and the
// component source. Each test navigates fresh and any object it creates is
// deleted again so the shared demo DB stays clean for the next test.
import { test, expect, type Page } from '@playwright/test'

// Demo seed (internal/demo/demo.go) we rely on. Object rows render as
// accessible LINKS whose name includes the lowercase kind + ident, e.g.
// "○ AUSSTEHEND host demo-gateway …". Services display "<host> / <service>".
const HOSTS = ['demo-gateway', 'demo-web', 'demo-dns', 'demo-snmp-device']
const SERVICES = ['demo-snmp-ifwalk', 'demo-tls', 'demo-web-latency', 'demo-batchjob']

// row(): the object's clickable list row. The accessible name concatenates the
// state label, the kind, the (host /) name, the check output and any labels, so
// we match on a regex that pins kind + name and tolerates the surrounding text.
function row(page: Page, kind: 'host' | 'service', name: string) {
  return page.getByRole('link', { name: new RegExp(`${kind} (?:[\\w.-]+ / )?${name}\\b`) })
}

// Open the create dialog ("+ New host" / "+ New service" buttons).
async function openCreate(page: Page, kind: 'host' | 'service') {
  const label = kind === 'host' ? '+ Host anlegen' : '+ Service anlegen'
  await page.getByRole('button', { name: label }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
}

// The shadcn DialogContent is `fixed top-1/2 -translate-y-1/2` with no
// max-height/overflow, so the tall object form overflows a standard 720px-high
// viewport and its lower controls (host Select, submit row) render fully but
// sit outside the viewport — and a fixed/centered dialog can't be scrolled, so
// Playwright cannot click them. The honest fix is to give the page enough room
// for the whole form: we use a tall viewport while a create/edit dialog is
// open, restoring the default afterwards. This keeps every interaction a real,
// un-forced click that the dialog actually receives.
const TALL_VIEWPORT = { width: 1280, height: 1600 }
const DEFAULT_VIEWPORT = { width: 1280, height: 720 }

test.describe('Objects explorer (operator)', () => {
  test('lists the seeded demo hosts and services', async ({ page }) => {
    await page.goto('/objects')

    // All four seed hosts render as rows.
    for (const h of HOSTS) {
      await expect(row(page, 'host', h)).toBeVisible()
    }
    // And the seeded services (shown as "<host> / <service>").
    for (const s of SERVICES) {
      await expect(row(page, 'service', s)).toBeVisible()
    }
  })

  test('filters by kind and reflects the choice in the URL', async ({ page }) => {
    await page.goto('/objects')
    await expect(row(page, 'host', 'demo-gateway')).toBeVisible()
    await expect(row(page, 'service', 'demo-tls')).toBeVisible()

    // The first combobox is the kind filter (Hosts + Services / Hosts /
    // Services). Pick "Hosts" — services must drop out of the list and the
    // URL must persist the filter (?kind=host) via validateSearch.
    const [kindFilter, stateFilter] = await page.getByRole('main').getByRole('combobox').all()
    await kindFilter.click()
    await page.getByRole('option', { name: 'Hosts', exact: true }).click()

    await expect(page).toHaveURL(/kind=host/)
    await expect(row(page, 'host', 'demo-gateway')).toBeVisible()
    await expect(row(page, 'service', 'demo-tls')).toHaveCount(0)

    // Switch to "Services" — hosts drop out, URL flips to ?kind=service.
    await kindFilter.click()
    await page.getByRole('option', { name: 'Services', exact: true }).click()
    await expect(page).toHaveURL(/kind=service/)
    await expect(row(page, 'service', 'demo-tls')).toBeVisible()
    await expect(row(page, 'host', 'demo-gateway')).toHaveCount(0)

    // "Reset filter" clears kind (and state) — both kinds return and
    // the kind param leaves the URL.
    await expect(stateFilter).toBeVisible()
    await page.getByRole('button', { name: 'Reset filter' }).click()
    await expect(page).not.toHaveURL(/kind=/)
    await expect(row(page, 'host', 'demo-gateway')).toBeVisible()
    await expect(row(page, 'service', 'demo-tls')).toBeVisible()
  })

  test('filters by state and reflects it in the URL', async ({ page }) => {
    // A freshly created host has no check result yet, so it is deterministically
    // in the PENDING (AUSSTEHEND) state — a stable, isolated anchor that no other
    // spec can mutate (unlike the shared demo objects, whose state changes once
    // checks run or another suite pushes a result).
    const name = `e2e-state-${Date.now()}`
    await page.setViewportSize(TALL_VIEWPORT)
    await page.goto('/objects')
    await openCreate(page, 'host')
    const dialog = page.getByRole('dialog')
    await dialog.getByPlaceholder('web01').fill(name)
    await dialog.getByRole('button', { name: 'Create', exact: true }).click()
    await expect(dialog).toBeHidden()
    await page.setViewportSize(DEFAULT_VIEWPORT)
    await expect(row(page, 'host', name)).toBeVisible()

    // The second combobox is the state filter. "Pending" / AUSSTEHEND maps to
    // ?state=pending and narrows to objects that have never been checked.
    const stateFilter = page.getByRole('main').getByRole('combobox').nth(1)
    await stateFilter.click()
    await page.getByRole('option', { name: 'Pending', exact: true }).click()

    await expect(page).toHaveURL(/state=pending/)
    await expect(row(page, 'host', name)).toBeVisible()

    // Clearing the filter restores the full list and drops the param.
    await page.getByRole('button', { name: 'Reset filter' }).click()
    await expect(page).not.toHaveURL(/state=/)

    // Clean up the host we created (two-click inline confirm on its detail page).
    await row(page, 'host', name).click()
    await expect(page).toHaveURL(/\/objects\/[\w-]+$/)
    await page.getByRole('button', { name: 'Delete', exact: true }).click()
    await page.getByRole('button', { name: 'Really delete?', exact: true }).click()
    await expect(page).toHaveURL(/\/objects$/)
  })

  test('full-text search narrows the list to a single object', async ({ page }) => {
    await page.goto('/objects')
    await expect(row(page, 'host', 'demo-dns')).toBeVisible()

    // "Volltext…" is the second text input (the first is the env=prod
    // selector). Typing debounces into ?q=… and the server filters.
    await page.getByPlaceholder('Volltext…').fill('demo-dns')
    await expect(page).toHaveURL(/q=demo-dns/)

    await expect(row(page, 'host', 'demo-dns')).toBeVisible()
    // A clearly unrelated seed object must be filtered out.
    await expect(row(page, 'service', 'demo-tls')).toHaveCount(0)

    // Clearing the search brings the rest back.
    await page.getByPlaceholder('Volltext…').fill('')
    await expect(row(page, 'service', 'demo-tls')).toBeVisible()
  })

  test('creates, edits and deletes a host (full lifecycle)', async ({ page }) => {
    const name = `e2e-host-${Date.now()}`
    // Tall viewport so the whole create/edit form (incl. its submit row) fits.
    await page.setViewportSize(TALL_VIEWPORT)
    await page.goto('/objects')

    // — CREATE — open the "New host" dialog, fill the required Name and an
    // address in the spec, then submit ("Create").
    await openCreate(page, 'host')
    const dialog = page.getByRole('dialog')
    // Name is the only required field; it autofocuses, but target it by its
    // create-mode placeholder ("web01") to be explicit.
    await dialog.getByPlaceholder('web01').fill(name)
    // Address lives in SpecFields with this exact placeholder.
    await dialog.getByPlaceholder('10.0.0.1 / host.example.com').fill('10.10.10.10')
    await dialog.getByRole('button', { name: 'Create', exact: true }).click()

    // Dialog closes and the new host appears in the list.
    await expect(dialog).toBeHidden()
    await page.setViewportSize(DEFAULT_VIEWPORT)
    await expect(row(page, 'host', name)).toBeVisible()

    // — EDIT — open its detail page and change a field. Name is locked on edit
    // (rename rejected server-side), so we edit the folder instead.
    await row(page, 'host', name).click()
    await expect(page).toHaveURL(/\/objects\/[\w-]+$/)
    // The detail h1 is "<name> [<state> …]" — its accessible name carries the
    // status suffix once a check runs, so match the heading that contains the
    // name rather than requiring an exact string.
    await expect(page.getByRole('heading', { name: new RegExp(name) })).toBeVisible()

    await page.setViewportSize(TALL_VIEWPORT)
    await page.getByRole('button', { name: 'Edit', exact: true }).click()
    const editDialog = page.getByRole('dialog')
    await expect(editDialog).toBeVisible()
    // The folder input carries the "/" placeholder and is editable on edit.
    // Exact match — the address placeholder also contains a "/".
    const folder = editDialog.getByPlaceholder('/', { exact: true })
    await folder.fill('/e2e/edited')
    // On edit the submit button reads "Speichern".
    await editDialog.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(editDialog).toBeHidden()
    await page.setViewportSize(DEFAULT_VIEWPORT)

    // The change is reflected: the effective-config JSON card / breadcrumb show
    // the new folder. The breadcrumb prints "<folder> /" for non-root folders.
    await expect(page.getByText('/e2e/edited', { exact: false }).first()).toBeVisible()

    // — DELETE — the detail page DeleteButton is a two-click inline confirm:
    // first click arms ("Delete"), second confirms ("Really delete?").
    await page.getByRole('button', { name: 'Delete', exact: true }).click()
    await page.getByRole('button', { name: 'Really delete?', exact: true }).click()

    // Deleting a host navigates back to /objects and the row is gone.
    await expect(page).toHaveURL(/\/objects$/)
    await expect(row(page, 'host', name)).toHaveCount(0)
  })

  test('creates a service under an existing host, then deletes it', async ({ page }) => {
    const name = `e2e-svc-${Date.now()}`
    await page.setViewportSize(TALL_VIEWPORT)
    await page.goto('/objects')

    // — CREATE — "New service" requires picking a host from the Select.
    await openCreate(page, 'service')
    const dialog = page.getByRole('dialog')
    // Service name field uses the "http" placeholder in create mode.
    await dialog.getByPlaceholder('http').fill(name)

    // Host picker: the dialog's first combobox. Open it and pick demo-gateway.
    await dialog.getByRole('combobox').first().click()
    await page.getByRole('option', { name: 'demo-gateway', exact: true }).click()

    await dialog.getByRole('button', { name: 'Create', exact: true }).click()
    await expect(dialog).toBeHidden()
    await page.setViewportSize(DEFAULT_VIEWPORT)

    // The new service shows up as "demo-gateway / <name>".
    const svc = row(page, 'service', name)
    await expect(svc).toBeVisible()
    await expect(svc).toContainText('demo-gateway')

    // — DELETE — the inline list-row DeleteButton (revealed on hover). Hover
    // the row to expose the action cluster, then two-click confirm.
    await svc.hover()
    const rowEl = page.locator('div.group', { has: svc })
    await rowEl.getByRole('button', { name: 'Delete', exact: true }).click()
    await rowEl.getByRole('button', { name: 'Really delete?', exact: true }).click()

    await expect(row(page, 'service', name)).toHaveCount(0)
  })

  test('triggers "Check now" (check-now) from an object detail', async ({ page }) => {
    await page.goto('/objects')

    // Open a stable seed host detail and fire the recheck action. The button is
    // labelled exactly "Check now" (i18n checkNow). It disables while the
    // mutation is in flight; we assert it returns to enabled (request settled)
    // rather than asserting on async re-check output, which is non-deterministic
    // for loopback checks in a fresh demo DB.
    await row(page, 'host', 'demo-gateway').click()
    await expect(page).toHaveURL(/\/objects\/[\w-]+$/)

    const checkNow = page.getByRole('button', { name: 'Check now' })
    await expect(checkNow).toBeEnabled()
    await checkNow.click()
    // Re-enabled once the POST /objects/$id/check-now resolves.
    await expect(checkNow).toBeEnabled()
  })
})
