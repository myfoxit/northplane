import { defineConfig, devices } from '@playwright/test'
import { BASE_URL, authFile } from './e2e/lib/roles'

// End-to-end suite: drives the real `northplaned --demo` binary (Go backend +
// embedded shadcn SPA) through Chromium. global-setup boots a fresh demo server
// and saves a session cookie per role; tests default to the `operator` role and
// opt into admin/viewer via `test.use({ storageState: authFile('admin') })`.
//
// Locale is pinned to de-DE: the app picks language from navigator.language and
// German is the reference language (i18n.ts), so this keeps every selector
// deterministic regardless of the CI machine's locale.
//
// workers: 1 — one shared demo DB, so tests run serially and never race on
// mutations (ack an alert, create an object). Re-runnable because global-setup
// uses a fresh temp DB each run.
export default defineConfig({
  testDir: './e2e',
  testMatch: '**/*.spec.ts',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  timeout: 30_000,
  expect: { timeout: 7_000 },
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : [['list']],
  globalSetup: './e2e/global-setup.ts',
  globalTeardown: './e2e/global-teardown.ts',
  use: {
    baseURL: BASE_URL,
    locale: 'de-DE',
    timezoneId: 'Europe/Vienna',
    storageState: authFile('operator'),
    actionTimeout: 10_000,
    navigationTimeout: 15_000,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
