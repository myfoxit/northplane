import { test, expect, type Page } from '@playwright/test'

// Alerts / Incidents / Events E2E (operator role, the config default).
//
// Drives the real `northplaned --demo` binary + embedded shadcn SPA. The UI is
// German (de-DE, the reference language) so every label below is the literal
// from src/i18n.ts.
//
// Determinism for the ack/resolve round-trip: demo seeds alert *rules*
// (demo-critical) and a passive service (demo-batchjob) but NO standing open
// alert. So we MINT one ourselves by submitting a passive CRITICAL check result
// for demo-batchjob. demo-critical fires on a hard state_change into CRITICAL,
// and passive results harden immediately (pipeline forces maxCheckAttempts=1 for
// source=="passive"), so a single submitted result reliably opens a CRITICAL
// alert titled "demo: demo-batchjob is CRITICAL". page.request reuses the
// operator session cookie (storageState), and operator holds objects:write +
// alerts:ack, so it can both ingest results and ack/resolve.

// Submit one passive CRITICAL result for demo-batchjob (service on demo-gateway).
// POST /api/v1/results is the Nagios-compatible batch passive-result endpoint
// (internal/api/ingress.go); 202 Accepted with accepted:1 means the pipeline
// took it.
async function pushCriticalBatchjob(page: Page, output: string): Promise<void> {
  const res = await page.request.post('/api/v1/results', {
    data: {
      results: [
        { host: 'demo-gateway', service: 'demo-batchjob', state: 2, output },
      ],
    },
  })
  expect(res.status(), await res.text()).toBe(202)
  const body = await res.json()
  expect(body.accepted, `rejected: ${JSON.stringify(body.rejected)}`).toBe(1)
}

// Submit a passive result for demo-batchjob at an explicit state. Used to force
// a deterministic state transition (→ a state_change event) regardless of the
// object's current state.
async function pushBatchjobState(page: Page, state: number, output: string): Promise<void> {
  const res = await page.request.post('/api/v1/results', {
    data: { results: [{ host: 'demo-gateway', service: 'demo-batchjob', state, output }] },
  })
  expect(res.status(), await res.text()).toBe(202)
}

// Force at least one state_change event by toggling demo-batchjob OK then
// CRITICAL — whatever its prior state, one of these two submissions changes it,
// and the pipeline emits a state_change event for the transition.
async function mintStateChange(page: Page): Promise<void> {
  await pushBatchjobState(page, 0, 'e2e recover for state_change')
  await pushBatchjobState(page, 2, 'e2e fail for state_change')
}

// Open a shadcn Select by its visible (current) value and pick an option.
async function pickSelect(page: Page, currentLabel: string | RegExp, option: string | RegExp) {
  await page.getByRole('combobox').filter({ hasText: currentLabel }).click()
  await page.getByRole('option', { name: option }).click()
}

test.describe('alerts', () => {
  test('alerts list renders (rows or empty state)', async ({ page }) => {
    await page.goto('/')
    await page.getByRole('link', { name: 'Alarme' }).click()
    await expect(page).toHaveURL(/\/alerts/)
    // Heading with the live count, e.g. "Alarme (3)".
    await expect(page.getByRole('heading', { name: /Alarme/ })).toBeVisible()
    // Both filter Selects are present (severity + status).
    await expect(page.getByRole('combobox')).toHaveCount(2)
    // Either alert rows are shown, or the explicit empty state. The default
    // status filter is "offen + quittiert" so this captures whatever is open.
    const empty = page.getByText('Keine Einträge.')
    const rows = page.locator('div.group').filter({ has: page.getByText(/seit/) })
    await expect.poll(async () =>
      (await empty.count()) > 0 || (await rows.count()) > 0,
    ).toBe(true)
  })

  test('filter by status drives the URL and the "all"/default option clears it', async ({ page }) => {
    await page.goto('/alerts')
    await expect(page.getByRole('heading', { name: /Alarme/ })).toBeVisible()

    // Pick "nur offen" -> ?status=open
    await pickSelect(page, /offen \+ quittiert/, 'nur offen')
    await expect(page).toHaveURL(/[?&]status=open(?:&|$)/)

    // Pick "geschlossen" -> ?status=resolved,expired
    await pickSelect(page, /nur offen/, 'geschlossen')
    await expect(page).toHaveURL(/status=resolved/)

    // Back to the combined default — status param is the default value again.
    await pickSelect(page, /geschlossen/, /offen \+ quittiert/)
    await expect(page).toHaveURL(/status=open%2Cacked|status=open,acked/)
  })

  test('filter by severity drives the URL and "alle Severities" clears it', async ({ page }) => {
    await page.goto('/alerts')
    await expect(page.getByRole('heading', { name: /Alarme/ })).toBeVisible()

    // Severity Select starts on the "alle Severities" sentinel.
    await pickSelect(page, /alle Severities/, 'critical')
    await expect(page).toHaveURL(/[?&]severity=critical(?:&|$)/)

    await pickSelect(page, 'critical', 'warning')
    await expect(page).toHaveURL(/[?&]severity=warning(?:&|$)/)

    // The "__all__" sentinel option clears the severity filter from the URL.
    await pickSelect(page, 'warning', 'alle Severities')
    await expect(page).not.toHaveURL(/severity=/)
  })

  test('incidents view renders', async ({ page }) => {
    await page.goto('/')
    await page.getByRole('link', { name: 'Incidents' }).click()
    await expect(page).toHaveURL(/\/incidents/)
    await expect(page.getByRole('heading', { name: 'Incidents' })).toBeVisible()
    // Either incident cards or the empty state (demo opens no standing incident).
    await expect(page.getByText('Keine Einträge.').or(page.getByText('Lade…')).first())
      .toBeVisible({ timeout: 10_000 })
  })

  test('ack + resolve round-trip on a freshly-minted CRITICAL alert', async ({ page }) => {
    const marker = `e2e-batchjob-critical ${Date.now()}`
    // 1) Mint a deterministic open CRITICAL alert for demo-batchjob.
    await pushCriticalBatchjob(page, marker)

    // 2) Show only open alerts so the row is unambiguous, and the CRITICAL one
    //    surfaces. Poll the list (the pipeline + rule engine are async).
    await page.goto('/alerts?status=open&severity=critical')
    await expect(page.getByRole('heading', { name: /Alarme/ })).toBeVisible()

    const alertRow = page.locator('div.group').filter({ hasText: 'demo-batchjob' }).first()
    await expect.poll(async () => {
      // The list auto-refreshes via SSE invalidation, but nudge it on slower
      // boots by re-fetching the route.
      if (await alertRow.count() === 0) await page.reload()
      return alertRow.count()
    }, { timeout: 20_000, intervals: [500, 1000, 1500] }).toBeGreaterThan(0)

    await expect(alertRow).toBeVisible()
    // Row carries the CRITICAL severity badge (exact, lower-case), the rule
    // title ("demo: demo-batchjob is CRITICAL") and the "open" status text.
    await expect(alertRow.getByText('critical', { exact: true })).toBeVisible()
    await expect(alertRow.getByText(/is CRITICAL/)).toBeVisible()
    await expect(alertRow.getByText(/^open ·/)).toBeVisible()

    // 3) ACK: hover to reveal the actions, click "Quittieren".
    await alertRow.hover()
    await alertRow.getByRole('button', { name: 'Quittieren' }).click()

    // Ack confirmation dialog: consequence text + confirm button.
    const ackDialog = page.getByRole('dialog')
    await expect(ackDialog).toBeVisible()
    await expect(ackDialog.getByText('Laufende Eskalationen werden gestoppt.')).toBeVisible()
    // The confirm button reuses the t('ack') label ("Quittieren"); pick it
    // inside the dialog (the row's button is hidden behind the modal overlay).
    await ackDialog.getByRole('button', { name: 'Quittieren' }).click()
    await expect(ackDialog).not.toBeVisible()

    // 4) Assert acknowledged: with status=open the acked alert drops out of the
    //    list; confirm it reappears (status "acked") under the open+acked view.
    await expect.poll(async () => alertRow.count(), { timeout: 15_000 }).toBe(0)

    await page.goto('/alerts?status=open,acked&severity=critical')
    const ackedRow = page.locator('div.group').filter({ hasText: 'demo-batchjob' }).first()
    await expect.poll(async () => {
      if (await ackedRow.count() === 0) await page.reload()
      return ackedRow.count()
    }, { timeout: 15_000, intervals: [500, 1000] }).toBeGreaterThan(0)
    await expect(ackedRow.getByText(/^acked/)).toBeVisible()

    // 5) RESOLVE: hover, click "Schließen". Acked alerts keep the resolve button.
    await ackedRow.hover()
    await ackedRow.getByRole('button', { name: 'Schließen' }).click()

    // 6) Assert it leaves the open+acked list (status -> resolved).
    await expect.poll(async () => ackedRow.count(), { timeout: 15_000 }).toBe(0)

    // And it now shows up under the "geschlossen" (resolved,expired) view.
    await page.goto('/alerts?status=resolved,expired&severity=critical')
    const resolvedRow = page.locator('div.group').filter({ hasText: 'demo-batchjob' }).first()
    await expect.poll(async () => {
      if (await resolvedRow.count() === 0) await page.reload()
      return resolvedRow.count()
    }, { timeout: 15_000, intervals: [500, 1000] }).toBeGreaterThan(0)
    await expect(resolvedRow.getByText(/resolved/)).toBeVisible()
  })
})

test.describe('events', () => {
  test('events list renders generated events', async ({ page }) => {
    // The demo seeds alerting *config* but emits no events at boot (a first OK
    // check is not a transition). Mint a deterministic state_change so the log
    // has rows, then assert they render as <details>/<summary> entries.
    await mintStateChange(page)
    await page.goto('/')
    await page.getByRole('link', { name: 'Events' }).click()
    await expect(page).toHaveURL(/\/events/)
    await expect(page.getByRole('heading', { name: 'Events' })).toBeVisible()
    const eventRows = page.locator('details')
    await expect.poll(async () => {
      if (await eventRows.count() === 0) await page.reload()
      return eventRows.count()
    }, { timeout: 20_000, intervals: [500, 1000, 1500] }).toBeGreaterThan(0)
  })

  test('filter by event type (the __all__ sentinel Select) constrains the rows', async ({ page }) => {
    await mintStateChange(page)
    await page.goto('/events')
    await expect(page.getByRole('heading', { name: 'Events' })).toBeVisible()
    await expect.poll(async () => {
      if (await page.locator('details').count() === 0) await page.reload()
      return page.locator('details').count()
    }, { timeout: 20_000, intervals: [500, 1000, 1500] }).toBeGreaterThan(0)

    // Type Select starts on the "alle Typen" sentinel; narrow to state_change.
    await pickSelect(page, /alle Typen/, 'state_change')
    // The type badge is the one with the w-32 class (the severity badge uses
    // colour classes, not w-32) — so this reads only the *type* of each row.
    // After filtering, every visible row must be a state_change.
    const typeBadges = page.locator('details summary span.w-32')
    await expect.poll(async () => {
      const types = await typeBadges.allInnerTexts()
      return types.length > 0 && types.every((t) => t.trim() === 'state_change')
    }, { timeout: 10_000 }).toBe(true)

    // Clearing back to the sentinel restores the (>=) unfiltered list.
    await pickSelect(page, 'state_change', 'alle Typen')
    await expect.poll(async () => page.locator('details').count(), { timeout: 10_000 })
      .toBeGreaterThan(0)
  })

  test('event type filter rides the request URL (types= query param)', async ({ page }) => {
    // The Events query is severity/type-aware server-side; the UI exposes the
    // type Select. Assert the filter change re-issues the /events request with
    // the chosen type in the query string.
    const requests: string[] = []
    page.on('request', (r) => {
      if (r.url().includes('/api/v1/events?')) requests.push(r.url())
    })
    await page.goto('/events')
    await expect(page.getByRole('heading', { name: 'Events' })).toBeVisible()
    await expect.poll(async () => requests.length, { timeout: 15_000 }).toBeGreaterThan(0)

    await pickSelect(page, /alle Typen/, 'notification')
    // A new /events request carrying types=notification is fired.
    await expect.poll(async () =>
      requests.some((u) => /[?&]types=notification(?:&|$)/.test(u)),
    { timeout: 10_000 }).toBe(true)
  })

  test('NDJSON export control is present and points at the export endpoint', async ({ page }) => {
    await page.goto('/events')
    await expect(page.getByRole('heading', { name: 'Events' })).toBeVisible()
    const exportLink = page.getByRole('link', { name: /NDJSON Export/ })
    await expect(exportLink).toBeVisible()
    await expect(exportLink).toHaveAttribute('href', /\/api\/v1\/events:export/)
  })
})
