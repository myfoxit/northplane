import { test, expect, type Page } from '@playwright/test'
import { authFile } from './lib/roles'

// Alerting-config E2E. Drives the real `northplaned --demo` binary + embedded
// shadcn SPA. The /alerting page has three tabs — Alarm-Regeln (rules), Gruppen
// (groups), Eskalationen (escalations). German is the reference locale, so every
// selector uses the exact de-DE strings from src/i18n.ts. Each test is
// self-contained: it opens /alerting fresh, uses a unique name, and deletes
// whatever it creates so the shared demo DB stays clean for the next test.
//
// PERMISSION MODEL (internal/api/rules.go + internal/model/admin.go):
//   - CRUD on alert-rules / alert-groups / escalation-policies needs
//     `config:write`, which only the `admin` role holds (`*:*`). The default
//     `operator` role is read/write for OPERATIONS (ack, downtime, oncall) but
//     NOT for configuration — so create/edit/delete tests run as admin via
//     test.use({ storageState: authFile('admin') }).
//   - Read-only listing, the inline rule tester (`:test`) and the escalation
//     simulator (`:simulate`) only need `alerts:read`, which operator has — so
//     those run under the default operator role.

// The alerting dialogs (full rule form, escalation policy with a step card) are
// tall. Use a roomy viewport so the whole dialog — including its footer buttons
// and the simulate/test result blocks — fits on screen and stays clickable.
test.use({ viewport: { width: 1440, height: 1600 } })

// A throwaway, DNS-safe name (resources are keyed by name, lowercase).
const uniq = (prefix: string) =>
  `${prefix}-${Date.now().toString(36)}-${Math.floor(Math.random() * 1e4).toString(36)}`

// Land on /alerting and select one of the three tabs by its exact label.
async function gotoTab(page: Page, label: 'Alarm-Regeln' | 'Gruppen' | 'Eskalationen') {
  await page.goto('/alerting')
  const tab = page.getByRole('tab', { name: label })
  await expect(tab).toBeVisible()
  await tab.click()
  await expect(tab).toHaveAttribute('data-state', 'active')
}

// The rule/policy dialogs are taller than the viewport. shadcn centres the
// dialog with a CSS transform (no scroll container), so a button near the
// bottom is rendered partly below the fold: Playwright sees it visible + stable
// but "outside of the viewport" and refuses to click. These footer/section
// buttons are genuinely interactable, so dispatch the click directly.
async function clickInDialog(page: Page, name: string | RegExp) {
  const btn = page.getByRole('dialog').getByRole('button', { name })
  await expect(btn).toBeVisible()
  await expect(btn).toBeEnabled()
  await btn.scrollIntoViewIfNeeded()
  await btn.click({ force: true })
}

test.describe('Alerting · Alarm-Regeln (rules)', () => {
  test('lists the seeded demo alert-rules', async ({ page }) => {
    await gotoTab(page, 'Alarm-Regeln')
    // demo-critical is the canonical CEL rule in the demo seed.
    await expect(page.getByRole('cell', { name: 'demo-critical' })).toBeVisible()
    // demo-heartbeat-rule is the heartbeat-style seed rule.
    await expect(page.getByRole('cell', { name: 'demo-heartbeat-rule' })).toBeVisible()
  })

  test('runs the inline tester for an existing rule (Regel testen)', async ({ page }) => {
    await gotoTab(page, 'Alarm-Regeln')
    const row = page.getByRole('row', { name: /demo-critical/ })
    await expect(row).toBeVisible()
    // The per-row FlaskConical button is labelled "Regel testen".
    await row.getByRole('button', { name: 'Regel testen' }).click()
    // It opens a result dialog titled "Regel testen: demo-critical".
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByText('Regel testen: demo-critical')).toBeVisible()
    // The tester renders a "<n> Events würden matchen, <m> Alarme entstehen."
    // summary regardless of how many history events match.
    await expect(dialog.getByText(/Events würden matchen/)).toBeVisible()
    await expect(dialog.getByText(/Alarme entstehen/)).toBeVisible()
  })

  // — Mutations need config:write ⇒ admin role. —
  test.describe('as admin', () => {
    test.use({ storageState: authFile('admin') })

    test('creates, tests inline, edits and deletes a CEL rule', async ({ page }) => {
    const name = uniq('e2e-rule')
    await gotoTab(page, 'Alarm-Regeln')

    // — CREATE —
    await page.getByRole('button', { name: 'Regel anlegen' }).click()
    let dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Regel anlegen')).toBeVisible()

    await dialog.getByPlaceholder('host-down-critical').fill(name)
    // CEL source is the default radio; fill the match-expression textarea.
    const cel = dialog.getByPlaceholder('event.type == "state_change" && event.severity == "critical"')
    await cel.fill('event.severity == "critical"')

    // — TEST (in-dialog runner against a hand-written demo event) —
    // The Demo-Event panel has its own "Ausführen" button + JSON textarea
    // pre-filled with a critical state_change, which matches our expression.
    await clickInDialog(page, 'Ausführen')
    await expect(dialog.getByText(/Events würden matchen/)).toBeVisible()
    // The sample event is critical, so exactly one event matches.
    await expect(dialog.getByText(/1 Events würden matchen/)).toBeVisible()

    // Save the new rule.
    await clickInDialog(page, 'Speichern')
    await expect(dialog).toBeHidden()

    // It now appears in the table.
    await expect(page.getByRole('cell', { name, exact: true })).toBeVisible()

    // — EDIT — reopen, change the CEL expression, save.
    await page.getByRole('row', { name: new RegExp(name) }).getByRole('button', { name: 'Bearbeiten' }).click()
    dialog = page.getByRole('dialog')
    await expect(dialog.getByText(`Bearbeiten: ${name}`)).toBeVisible()
    // Name input is disabled when editing.
    await expect(dialog.getByPlaceholder('host-down-critical')).toBeDisabled()
    const celEdit = dialog.getByPlaceholder('event.type == "state_change" && event.severity == "critical"')
    await celEdit.fill('event.severity == "critical" && event.type == "state_change"')
    await clickInDialog(page, 'Speichern')
    await expect(dialog).toBeHidden()

    // — DELETE — reopen, arm the inline confirm, confirm.
    await page.getByRole('row', { name: new RegExp(name) }).getByRole('button', { name: 'Bearbeiten' }).click()
    dialog = page.getByRole('dialog')
    await clickInDialog(page, 'Löschen')
    await clickInDialog(page, 'Wirklich löschen?')
    await expect(dialog).toBeHidden()

    // Gone from the table.
    await expect(page.getByRole('cell', { name, exact: true })).toHaveCount(0)
    })
  })
})

test.describe('Alerting · Eskalationen (escalation policies)', () => {
  test('lists demo-escalation', async ({ page }) => {
    await gotoTab(page, 'Eskalationen')
    await expect(page.getByRole('cell', { name: 'demo-escalation' })).toBeVisible()
  })

  test('simulates demo-escalation and renders the timeline', async ({ page }) => {
    await gotoTab(page, 'Eskalationen')
    await page.getByRole('row', { name: /demo-escalation/ }).getByRole('button', { name: 'Bearbeiten' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Bearbeiten: demo-escalation')).toBeVisible()

    // The simulator button is enabled for a saved policy. There are two
    // "Simulieren" controls (the section label + the button) — click the button.
    await clickInDialog(page, 'Simulieren')

    // The dry-run renders an ordered list — one <listitem> per step, each with a
    // "+<after>" delay marker. demo-escalation has at least one step (step 0
    // notifies demo-ops, then +15m unless acked).
    const steps = dialog.getByRole('listitem')
    await expect(steps.first()).toBeVisible()
    await expect(steps).not.toHaveCount(0)
    // Each timeline row prints a monospace "+<duration>" offset.
    await expect(dialog.getByText(/^\+/).first()).toBeVisible()
  })

  // — Mutations need config:write ⇒ admin role. —
  test.describe('as admin', () => {
    test.use({ storageState: authFile('admin') })

    test('creates and deletes a minimal escalation policy', async ({ page }) => {
    const name = uniq('e2e-policy')
    await gotoTab(page, 'Eskalationen')

    // — CREATE — "Anlegen" opens a dialog pre-seeded with one empty step.
    await page.getByRole('button', { name: 'Anlegen' }).click()
    let dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Anlegen', { exact: true })).toBeVisible()
    await dialog.getByPlaceholder('standard-oncall').fill(name)

    // Wire step 1 to notify a contact (demo-alice) so the policy is meaningful.
    // The notify radios are unlabelled inputs wrapped in a <label> with text;
    // click the exact label text to select the "Kontakt" option (not the
    // "Kontaktgruppe" label, which also contains "Kontakt").
    await dialog.locator('label').filter({ hasText: /^Kontakt$/ }).click()
    // The contact Select is now the only combobox shown for this step.
    await dialog.getByRole('combobox').click()
    await page.getByRole('option', { name: 'demo-alice' }).click()

    await clickInDialog(page, 'Speichern')
    await expect(dialog).toBeHidden()
    await expect(page.getByRole('cell', { name, exact: true })).toBeVisible()

    // — DELETE —
    await page.getByRole('row', { name: new RegExp(name) }).getByRole('button', { name: 'Bearbeiten' }).click()
    dialog = page.getByRole('dialog')
    await clickInDialog(page, 'Löschen')
    await clickInDialog(page, 'Wirklich löschen?')
    await expect(dialog).toBeHidden()
    await expect(page.getByRole('cell', { name, exact: true })).toHaveCount(0)
    })
  })
})

test.describe('Alerting · Gruppen (alert groups)', () => {
  test('lists demo-storm', async ({ page }) => {
    await gotoTab(page, 'Gruppen')
    await expect(page.getByRole('cell', { name: 'demo-storm' })).toBeVisible()
  })

  // — Mutations need config:write ⇒ admin role. —
  test.describe('as admin', () => {
    test.use({ storageState: authFile('admin') })

    test('creates and deletes an alert group', async ({ page }) => {
    const name = uniq('e2e-group')
    await gotoTab(page, 'Gruppen')

    // — CREATE —
    await page.getByRole('button', { name: 'Anlegen' }).click()
    let dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Anlegen', { exact: true })).toBeVisible()
    await dialog.getByPlaceholder('per-host-flood').fill(name)
    // Add a group-by label key via the ListEditor (type + Enter). The list
    // input's placeholder is exactly "host" (the name input's "per-host-flood"
    // also substring-matches "host", so pin exact).
    const groupByInput = dialog.getByPlaceholder('host', { exact: true })
    await groupByInput.fill('host')
    await groupByInput.press('Enter')
    // The accepted key renders as a chip.
    await expect(dialog.getByText('host', { exact: true })).toBeVisible()

    await clickInDialog(page, 'Speichern')
    await expect(dialog).toBeHidden()
    await expect(page.getByRole('cell', { name, exact: true })).toBeVisible()

    // — DELETE —
    await page.getByRole('row', { name: new RegExp(name) }).getByRole('button', { name: 'Bearbeiten' }).click()
    dialog = page.getByRole('dialog')
    await clickInDialog(page, 'Löschen')
    await clickInDialog(page, 'Wirklich löschen?')
    await expect(dialog).toBeHidden()
    await expect(page.getByRole('cell', { name, exact: true })).toHaveCount(0)
    })
  })
})
