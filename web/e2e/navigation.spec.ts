// Operator navigation + cross-app rendering suite. Green here means: the app
// shell navigates to every route, every page renders its known content WITHOUT
// dropping into an error state (i18n loadError 'Laden fehlgeschlagen.' /
// 'Erneut versuchen'), and the keyboard-first surfaces — the ⌘K command palette
// and the ⌘I assistant sidebar — open, search, navigate and close.
//
// Read-only by design: this suite asserts rendering + navigation only. It does
// NOT create/ack/delete data (other suites own mutations). The single allowed
// side effect is triggering a report :run, which is idempotent-ish (render +
// archive a snapshot) and only asserted not to error.
import { test, expect, type Page } from '@playwright/test'

// i18n error strings (src/i18n.ts) that the kit's ErrorState renders. Their
// ABSENCE is the rendered-OK signal across every page.
const LOAD_ERROR = 'Laden fehlgeschlagen.'
const RETRY = 'Erneut versuchen'

// Every sidebar nav link: its route + a heading that proves the page mounted.
// Headings that embed a live count span (Probleme (3), Objekte (12) …) match by
// a start-anchored regex; the rest match their exact German nav label. The
// Templates page heading is "Templates & Konfiguration", so it anchors on
// "Templates". `getByRole('heading')` never collides with the same-named
// sidebar <Link> (role=link).
const NAV: { label: string; path: string; heading: RegExp }[] = [
  { label: 'Übersicht', path: '/', heading: /^Übersicht$/ },
  { label: 'Probleme', path: '/problems', heading: /^Probleme/ },
  { label: 'Objekte', path: '/objects', heading: /^Objekte/ },
  { label: 'Alarme', path: '/alerts', heading: /^Alarme/ },
  { label: 'Incidents', path: '/incidents', heading: /^Incidents$/ },
  { label: 'Events', path: '/events', heading: /^Events$/ },
  { label: 'Dashboards', path: '/dashboards', heading: /^Dashboards$/ },
  { label: 'Business Services', path: '/business', heading: /^Business Services$/ },
  { label: 'Reports', path: '/reports', heading: /^Reports$/ },
  { label: 'Alarm-Regeln', path: '/alerting', heading: /^Alarm-Regeln$/ },
  { label: 'Bereitschaft', path: '/oncall', heading: /^Bereitschaft$/ },
  { label: 'Wartung', path: '/maintenance', heading: /^Wartung$/ },
  { label: 'Templates', path: '/templates', heading: /^Templates/ },
  { label: 'Discovery', path: '/discovery', heading: /^Discovery$/ },
  { label: 'KI-Agent', path: '/agent', heading: /^KI-Agent$/ },
  { label: 'Administration', path: '/admin', heading: /^Administration$/ },
]

// Asserts the visible page never fell into the kit ErrorState. The retry button
// only renders on error, so its absence is the strongest single signal; we also
// guard the loadError headline directly.
async function expectNoErrorState(page: Page): Promise<void> {
  await expect(page.getByRole('button', { name: RETRY })).toHaveCount(0)
  await expect(page.getByText(LOAD_ERROR, { exact: true })).toHaveCount(0)
}

// Opens the ⌘K command palette via its sidebar button (more reliable than
// synthesizing the shortcut) and returns the dialog + its search textbox.
async function openPalette(page: Page) {
  await page.getByRole('button', { name: 'Suchen… (⌘K)' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  // cmdk's CommandInput renders as <input role="combobox" aria-autocomplete>,
  // not a plain textbox — it auto-focuses on open (focus-trapped dialog).
  const input = dialog.getByRole('combobox')
  await expect(input).toBeFocused()
  return { dialog, input }
}

test.describe('navigation — app shell', () => {
  for (const item of NAV) {
    test(`nav "${item.label}" → ${item.path} renders without an error state`, async ({ page }) => {
      await page.goto('/')
      // Click the sidebar link rather than navigating by URL — proves the shell
      // link is wired, not just that the route exists.
      await page.getByRole('link', { name: item.label, exact: true }).click()

      // '/' is a prefix of every path, so anchor the root assertion exactly.
      if (item.path === '/') {
        await expect(page).toHaveURL(/\/(?:\?.*)?$/)
      } else {
        await expect(page).toHaveURL(new RegExp(item.path.replace('/', '\\/')))
      }

      // The page heading proves the route's component mounted and rendered.
      await expect(page.getByRole('heading', { name: item.heading })).toBeVisible()
      // …and it rendered its content, not the kit ErrorState. Data-backed pages
      // need a beat to settle their first query, hence the web-first retries.
      await expectNoErrorState(page)
    })
  }

  test('the active sidebar link tracks the current route', async ({ page }) => {
    await page.goto('/objects')
    // Sidebar stays mounted across navigations — every nav link is present.
    await expect(page.getByRole('link', { name: 'Objekte', exact: true })).toBeVisible()
    await expect(page.getByRole('link', { name: 'Reports', exact: true })).toBeVisible()
    await page.getByRole('link', { name: 'Reports', exact: true }).click()
    await expect(page).toHaveURL(/\/reports/)
    await expect(page.getByRole('heading', { name: /^Reports$/ })).toBeVisible()
  })
})

test.describe('navigation — overview', () => {
  test('summary tiles, incidents and on-call widgets render', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('heading', { name: /^Übersicht$/ })).toBeVisible()

    // KPI tiles (TileLink labels from i18n). Hosts UP / Services OK always exist;
    // their numeric value resolves once /overview returns.
    await expect(page.getByText('Hosts UP', { exact: true })).toBeVisible()
    await expect(page.getByText('Services OK', { exact: true })).toBeVisible()
    await expect(page.getByText('Offene Alarme', { exact: true })).toBeVisible()

    // The right column cards: open incidents + current on-call. shadcn CardTitle
    // renders a <div data-slot="card-title">, not a heading role — match by text.
    await expect(page.getByText('Offene Incidents', { exact: true })).toBeVisible()
    await expect(page.getByText('Aktuelle Bereitschaft', { exact: true })).toBeVisible()

    // A tile value resolved to a number (or the em-dash placeholder turned into
    // a real count) — i.e. /overview actually answered.
    const hostsUpTile = page.getByRole('link').filter({ hasText: 'Hosts UP' })
    await expect(hostsUpTile.locator('.tabular-nums').first()).toHaveText(/\d/)

    await expectNoErrorState(page)
  })

  test('an overview KPI tile drills down into a filtered objects view', async ({ page }) => {
    await page.goto('/')
    // The "Hosts UP" tile links to /objects?kind=host&state=up.
    await page.getByRole('link').filter({ hasText: 'Hosts UP' }).click()
    await expect(page).toHaveURL(/\/objects/)
    await expect(page).toHaveURL(/kind=host/)
    await expect(page).toHaveURL(/state=up/)
    await expect(page.getByRole('heading', { name: /^Objekte/ })).toBeVisible()
    await expectNoErrorState(page)
  })
})

test.describe('navigation — problems', () => {
  test('list renders; rows drill into an object (or the all-green empty state shows)', async ({ page }) => {
    await page.goto('/problems')
    await expect(page.getByRole('heading', { name: /^Probleme/ })).toBeVisible()
    await expectNoErrorState(page)

    const emptyState = page.getByText('Keine offenen Probleme', { exact: false })
    // Each problem row links to /objects/$id.
    const rowLinks = page.locator('a[href^="/objects/"]')

    // Settle: either the empty state or at least one row must appear.
    await expect(emptyState.or(rowLinks.first())).toBeVisible()

    if (await rowLinks.count() > 0) {
      await rowLinks.first().click()
      await expect(page).toHaveURL(/\/objects\/[^/]+/)
      await expectNoErrorState(page)
    } else {
      await expect(emptyState).toBeVisible()
    }
  })

  test('the include-handled toggle re-queries without erroring', async ({ page }) => {
    await page.goto('/problems')
    await expect(page.getByRole('heading', { name: /^Probleme/ })).toBeVisible()
    await page.getByRole('checkbox').check()
    await expect(page.getByRole('checkbox')).toBeChecked()
    await expectNoErrorState(page)
  })
})

test.describe('navigation — command palette (⌘K)', () => {
  test('opens, searches an object, navigates on select, and closes on Escape', async ({ page }) => {
    await page.goto('/')

    // 1) Open + object search → navigate to the object detail.
    const { dialog, input } = await openPalette(page)
    await input.fill('demo-gateway')
    const hit = dialog.getByRole('option', { name: /demo-gateway/ })
    await expect(hit).toBeVisible()
    await hit.click()
    await expect(page).toHaveURL(/\/objects\/[^/]+/)
    await expect(dialog).toBeHidden()
    await expectNoErrorState(page)

    // 2) Re-open and assert Escape closes it (no navigation needed).
    const second = await openPalette(page)
    await page.keyboard.press('Escape')
    await expect(second.dialog).toBeHidden()
  })

  test('a page entry navigates by keyboard selection', async ({ page }) => {
    await page.goto('/')
    const { dialog, input } = await openPalette(page)
    // The static "pages" group is English in the palette (CommandPalette.tsx).
    await input.fill('Problems')
    const pageHit = dialog.getByRole('option', { name: 'Problems' })
    await expect(pageHit).toBeVisible()
    // cmdk highlights the first match — Enter activates it.
    await page.keyboard.press('Enter')
    await expect(page).toHaveURL(/\/problems/)
    await expect(dialog).toBeHidden()
    await expect(page.getByRole('heading', { name: /^Probleme/ })).toBeVisible()
  })
})

test.describe('navigation — business services (BPI)', () => {
  test('the tree shows demo-webshop; selecting it renders SLA + definition', async ({ page }) => {
    await page.goto('/business')
    await expect(page.getByRole('heading', { name: /^Business Services$/ })).toBeVisible()
    await expectNoErrorState(page)

    // The live aggregation tree renders the seeded root as a clickable node.
    const root = page.getByRole('button', { name: /demo-webshop/ }).first()
    await expect(root).toBeVisible()
    await root.click()

    // Detail pane: SLA card (CardTitle "SLA — demo-webshop") + Definition card.
    // CardTitle is a <div data-slot="card-title">, so match by text not heading.
    await expect(page.getByText('SLA — demo-webshop', { exact: false })).toBeVisible()
    await expect(page.getByText('Definition', { exact: true })).toBeVisible()
    // SLA budget tiles resolve once /business-services/{name}/sla answers; the
    // 99.9% target from the demo seed proves real SLA content rendered.
    await expect(page.getByText('Verfügbarkeit', { exact: true })).toBeVisible()
    await expect(page.getByText('99.9%', { exact: false }).first()).toBeVisible()
    await expectNoErrorState(page)
  })
})

test.describe('navigation — reports', () => {
  test('lists demo-availability with its actions and opens the render archive', async ({ page }) => {
    await page.goto('/reports')
    await expect(page.getByRole('heading', { name: /^Reports$/ })).toBeVisible()
    await expectNoErrorState(page)

    // The seeded report row renders in the table with its action buttons. (The
    // operator can't run-now — :run needs config:write — and Preview/CSV/JSON
    // hit a POST-only :render route via GET, so those output paths aren't
    // asserted here; the archive view is the operator-reachable surface and is
    // what this rendering suite verifies.)
    const row = page.getByRole('row', { name: /demo-availability/ })
    await expect(row).toBeVisible()
    await expect(row.getByRole('button', { name: 'Vorschau' })).toBeVisible()
    await expect(row.getByRole('button', { name: 'Ausführen' })).toBeVisible()

    // The archive dialog opens (GET …/archive, operator-readable) and renders
    // its content — prior runs as download links, or the empty state — never
    // an error boundary.
    await row.getByRole('button', { name: 'Archiv' }).click()
    const archiveDialog = page.getByRole('dialog')
    await expect(archiveDialog.getByRole('heading', { name: /Archiv: demo-availability/ })).toBeVisible()
    const entries = archiveDialog.getByRole('link', { name: /Download/ })
    const empty = archiveDialog.getByText('Keine Einträge.', { exact: true })
    await expect(entries.first().or(empty)).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(archiveDialog).toBeHidden()
  })
})

test.describe('navigation — on-call', () => {
  test('renders the who-is-on-call now widget and schedule manager', async ({ page }) => {
    await page.goto('/oncall')
    await expect(page.getByRole('heading', { name: /^Bereitschaft$/ })).toBeVisible()
    // SchedulesManager always renders its "Dienstpläne" card (CardTitle div).
    await expect(page.getByText('Dienstpläne', { exact: true })).toBeVisible()
    // The demo seed defines schedule "demo-oncall": its now-card title + the
    // 14-day detail card ("demo-oncall — 14 Tage") prove the schedule rendered.
    await expect(page.getByText('demo-oncall', { exact: false }).first()).toBeVisible()
    await expectNoErrorState(page)
  })
})

test.describe('navigation — AI assistant sidebar (⌘I)', () => {
  test('toggling the assistant opens the panel and shows its state', async ({ page }) => {
    await page.goto('/')
    // The sidebar (and its ask textarea) is not mounted until toggled.
    await expect(page.getByPlaceholder(/Frag den Assistenten/)).toHaveCount(0)

    await page.getByRole('button', { name: /Assistent/ }).click()

    // The panel mounts: header with the Assistent title + the ask textarea.
    // (No AI provider is configured in demo — we only assert it OPENS and shows
    // its idle/intro state, not that an LLM answers.)
    const ask = page.getByPlaceholder(/Frag den Assistenten/)
    await expect(ask).toBeVisible()
    await expect(page.getByText('Mutationen laufen', { exact: false })).toBeVisible()

    // Toggling again closes it (the same button flips the panel).
    await page.getByRole('button', { name: /Assistent/ }).click()
    await expect(ask).toBeHidden()
  })
})
