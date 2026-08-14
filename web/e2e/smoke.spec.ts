import { test, expect } from '@playwright/test'
import { authFile } from './lib/roles'

// Harness smoke test: proves the full stack is wired — real Go server, embedded
// shadcn SPA, session-cookie auth, demo seed data, and German locale rendering.
test.describe('harness smoke', () => {
  test('operator lands on the authenticated app shell', async ({ page }) => {
    await page.goto('/')
    // Not bounced to the server-rendered login page.
    await expect(page).not.toHaveURL(/\/login/)
    // Sidebar nav renders in German (de is the reference language).
    await expect(page.getByRole('link', { name: 'Objekte', exact: true })).toBeVisible()
    await expect(page.getByRole('link', { name: 'Alarme', exact: true })).toBeVisible()
  })

  test('navigating to Objekte shows seeded demo objects', async ({ page }) => {
    await page.goto('/')
    await page.getByRole('link', { name: 'Objekte', exact: true }).click()
    await expect(page).toHaveURL(/\/objects/)
    // The demo seed creates demo-gateway / demo-web / demo-dns hosts. Object
    // rows render as links whose accessible name includes the kind + ident.
    await expect(page.getByRole('link', { name: /host demo-gateway/ })).toBeVisible()
  })

  test('unauthenticated visitor is redirected to /login', async ({ browser }) => {
    const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
    const page = await ctx.newPage()
    await page.goto('/objects')
    // The SPA shell loads, its first /api call 401s, and api.ts redirects.
    await page.waitForURL(/\/login/, { timeout: 15_000 })
    await expect(page.locator('input[name="email"]')).toBeVisible()
    await expect(page.locator('input[name="password"]')).toBeVisible()
    await ctx.close()
  })

  test('viewer role is read-only', async ({ browser }) => {
    const ctx = await browser.newContext({ storageState: authFile('viewer') })
    const page = await ctx.newPage()
    await page.goto('/')
    await expect(page).not.toHaveURL(/\/login/)
    await expect(page.getByRole('link', { name: 'Objekte', exact: true })).toBeVisible()
    await ctx.close()
  })
})
