// Alerting maintenance-windows E2E (operator role).
//
// Exercises the four scheduling/suppression surfaces of the app against the
// real `northplaned --demo` backend + embedded shadcn SPA, in German (de-DE):
//
//   • Downtimes  — /maintenance ▸ "Downtimes" tab. List the seeded recurring
//     downtime on demo-batchjob; create a fixed downtime on a demo object,
//     assert it appears, then delete it.
//   • Silences   — /maintenance ▸ "Silences" tab. Create a label-selector
//     silence, assert it appears, then delete it.
//   • Wartung    — /maintenance page renders with both tabs and its create
//     control.
//   • Bereitschaft — /oncall renders the seeded demo-oncall schedule and the
//     on-call rotation (demo-alice / demo-bob).
//
// Web-first assertions throughout (no fixed sleeps). Each test is independent,
// uses a unique comment to find its own row, and deletes what it created so the
// shared demo DB stays clean for the next test.
import { test, expect, type Page } from '@playwright/test'

// A run-unique token so a test only ever matches its own created row, even if a
// previous run left residue (it shouldn't — fresh DB per run — but be safe).
const uniq = (p: string) => `${p}-${Date.now()}-${Math.floor(Math.random() * 1e6)}`

// Open /maintenance and switch to the requested tab. The page mounts on the
// "Silences" tab by default; selecting a tab swaps the panel below the tablist.
async function openMaintenanceTab(page: Page, tab: 'Silences' | 'Downtimes') {
  await page.goto('/maintenance')
  await expect(page.getByRole('heading', { name: 'Maintenance' })).toBeVisible()
  await page.getByRole('tab', { name: tab }).click()
  // The selected tab's create button is the panel's first control.
  await expect(page.getByRole('button', { name: 'Create' })).toBeVisible()
}

// Fill a shadcn DateTimeInput (a native <input type="datetime-local">) with a
// "YYYY-MM-DDTHH:mm" value derived from now + offset minutes (local TZ pinned
// to Europe/Vienna by the project config).
async function setDateTime(input: ReturnType<Page['locator']>, offsetMinutes: number) {
  const d = new Date(Date.now() + offsetMinutes * 60_000)
  const pad = (n: number) => String(n).padStart(2, '0')
  const local = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
  await input.fill(local)
}

test.describe('alerting maintenance windows (operator)', () => {
  // ——— DOWNTIMES ———————————————————————————————————————————————————————

  test('downtimes tab lists the seeded recurring downtime on demo-batchjob', async ({ page }) => {
    await openMaintenanceTab(page, 'Downtimes')

    // The demo seed creates one fixed+recurring downtime on demo-batchjob with a
    // known comment and a daily RRULE. Assert both surface in the table.
    const seededRow = page.getByRole('row', { name: /demo: nightly batch-job maintenance window/ })
    await expect(seededRow).toBeVisible()
    await expect(seededRow.getByText('fixed')).toBeVisible()
    await expect(seededRow.getByText(/FREQ=DAILY/)).toBeVisible()
  })

  test('create a fixed downtime on a demo object, see it active, then delete it', async ({ page }) => {
    await openMaintenanceTab(page, 'Downtimes')

    const comment = uniq('e2e-downtime')

    await page.getByRole('button', { name: 'Create' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: /Downtimes anlegen/i })).toBeVisible()

    // Target = a demo object. The picker searches GET /objects?q=… as you type;
    // pick demo-web-latency (a seeded service on demo-web).
    await dialog.getByPlaceholder('Objekt suchen…').fill('demo-web-latency')
    await dialog.getByRole('button', { name: /demo-web-latency/ }).click()
    // A target is now chosen: the picker shows a remove ("Remove") control
    // next to the selected-object badge.
    await expect(dialog.getByRole('button', { name: 'Entfernen' })).toBeVisible()

    // Type defaults to "fixed" — leave it. Set a window: now → +1h.
    const dts = dialog.locator('input[type="datetime-local"]')
    await setDateTime(dts.nth(0), 0)
    await setDateTime(dts.nth(1), 60)

    await dialog.getByPlaceholder('Patch-Day').fill(comment)
    await dialog.getByRole('button', { name: 'Create' }).click()

    // Dialog closes and the new downtime appears as a row identified by its
    // unique comment, typed "fixed".
    await expect(dialog).toBeHidden()
    const row = page.getByRole('row', { name: new RegExp(comment) })
    await expect(row).toBeVisible()
    await expect(row.getByText('fixed')).toBeVisible()

    // Delete it (inline arm → confirm) and assert it's gone.
    await row.getByRole('button', { name: 'Delete' }).click()
    await row.getByRole('button', { name: 'Really delete?' }).click()
    await expect(page.getByRole('row', { name: new RegExp(comment) })).toHaveCount(0)
  })

  // ——— SILENCES ————————————————————————————————————————————————————————

  test('create a silence with a label selector, see it listed, then delete it', async ({ page }) => {
    await openMaintenanceTab(page, 'Silences')

    const comment = uniq('e2e-silence')
    const selector = 'env=e2e'

    await page.getByRole('button', { name: 'Create' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: /Silences anlegen/i })).toBeVisible()

    // Matcher: a label selector. (Selector OR regex is required.)
    await dialog.getByPlaceholder('env=prod').fill(selector)
    await dialog.getByPlaceholder('DB maintenance window').fill(comment)
    // Expiry is mandatory; the dialog pre-fills now+1h, and a "1h" quick button
    // re-stamps it. Click it to make the expiry deterministic and in the future.
    await dialog.getByRole('button', { name: '1h', exact: true }).click()

    await dialog.getByRole('button', { name: 'Create' }).click()

    await expect(dialog).toBeHidden()
    const row = page.getByRole('row', { name: new RegExp(comment) })
    await expect(row).toBeVisible()
    await expect(row.getByText(selector)).toBeVisible()

    // Delete (expire early) and assert removal.
    await row.getByRole('button', { name: 'Delete' }).click()
    await row.getByRole('button', { name: 'Really delete?' }).click()
    await expect(page.getByRole('row', { name: new RegExp(comment) })).toHaveCount(0)
  })

  // ——— WARTUNG (Maintenance page smoke) ————————————————————————————————

  test('maintenance page renders both tabs and its create control', async ({ page }) => {
    await page.goto('/maintenance')
    await expect(page.getByRole('heading', { name: 'Maintenance' })).toBeVisible()

    // Both suppression surfaces are reachable as tabs.
    const silencesTab = page.getByRole('tab', { name: 'Silences' })
    const downtimesTab = page.getByRole('tab', { name: 'Downtimes' })
    await expect(silencesTab).toBeVisible()
    await expect(downtimesTab).toBeVisible()

    // Default tab is Silences: its create button is present and the empty/list
    // surface renders without error.
    await expect(page.getByRole('button', { name: 'Create' })).toBeVisible()

    // Switching tabs swaps the panel and keeps a working create control — proves
    // the seeded recurring downtime list renders too.
    await downtimesTab.click()
    await expect(page.getByRole('button', { name: 'Create' })).toBeVisible()
    await expect(page.getByRole('row', { name: /demo: nightly batch-job maintenance window/ })).toBeVisible()
  })

  // ——— BEREITSCHAFT (On-call) ——————————————————————————————————————————

  test('on-call page renders the demo-oncall schedule and its alice/bob rotation', async ({ page }) => {
    await page.goto('/oncall')
    await expect(page.getByRole('heading', { name: 'On-Call' })).toBeVisible()

    // The seeded schedule surfaces in the "Schedules" management card. The
    // card title is a styled <div>, not a heading — match it as text.
    await expect(page.getByText('Schedules', { exact: true })).toBeVisible()
    const scheduleRow = page.getByRole('row', { name: /demo-oncall/ })
    await expect(scheduleRow).toBeVisible()
    // Its IANA timezone column is rendered (Europe/Vienna in the seed).
    await expect(scheduleRow.getByText('Europe/Vienna')).toBeVisible()

    // The "who is on call now" card for demo-oncall shows a rotation participant
    // (alice or bob, depending on the current week) — assert one of them is on
    // duty right now.
    await expect(page.getByText(/demo-(alice|bob)/).first()).toBeVisible()

    // The per-schedule 14-day timeline card renders for demo-oncall, proving the
    // schedule detail (timeline/rotation) mounted. (Card title is a <div>.)
    await expect(page.getByText('demo-oncall — 14 Tage')).toBeVisible()
  })
})
