// Boots a fresh, isolated Northplane demo server for the E2E run and produces
// authenticated storage states for all three roles. Determinism: a brand-new
// temp dataDir each run, so the idempotent demo seed starts from a clean DB and
// no test-created data survives between runs.
//
// Sequence: bootstrap-admin (mints a *:* token + initialises the DB) ->
// serve --demo (seeds operator/viewer users + demo objects/alerts/dashboards)
// -> create a known-password admin user via the API -> form-login each role
// and save its np_session cookie as a Playwright storageState.
import { request } from '@playwright/test'
import { spawn, execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import crypto from 'node:crypto'
import { PORT, BASE_URL, BIN, AUTH_DIR, RUNTIME_FILE, USERS, authFile, type Role } from './lib/roles'

async function waitReady(url: string, timeoutMs = 30000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url)
      if (res.ok) return
    } catch {
      /* not up yet */
    }
    await new Promise((r) => setTimeout(r, 250))
  }
  throw new Error(`server did not become ready at ${url} within ${timeoutMs}ms`)
}

export default async function globalSetup(): Promise<void> {
  if (!fs.existsSync(BIN)) {
    throw new Error(`northplaned binary not found at ${BIN}.\nBuild it first: 'make build' (or 'make web build' for a fresh UI embed).`)
  }
  fs.mkdirSync(AUTH_DIR, { recursive: true })

  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'np-e2e-'))
  const keyFile = path.join(dataDir, 'secret.key')
  fs.writeFileSync(keyFile, crypto.randomBytes(32).toString('hex'), { mode: 0o600 })
  const cfgPath = path.join(dataDir, 'config.yaml')
  fs.writeFileSync(
    cfgPath,
    `listen: "127.0.0.1:${PORT}"\nbaseUrl: "${BASE_URL}"\ndataDir: ${dataDir}/data\nlogLevel: warn\nlogFormat: text\nsecretKeyFile: ${keyFile}\n`,
  )

  // 1) bootstrap admin token (initialises the DB + migrations)
  const out = execFileSync(BIN, ['bootstrap-admin', '-config', cfgPath], { encoding: 'utf8' })
  const token = out.match(/np_[A-Za-z0-9._-]+/)?.[0]
  if (!token) throw new Error(`could not parse admin token from bootstrap-admin output:\n${out}`)

  // 2) start the demo server (detached so we own the process group for teardown)
  const logFd = fs.openSync(path.join(dataDir, 'server.log'), 'a')
  const proc = spawn(BIN, ['serve', '-config', cfgPath, '--demo'], {
    stdio: ['ignore', logFd, logFd],
    detached: true,
    // Suppress the break-glass default-admin seed: the suite provisions its
    // own admin below and the last-admin-guard test depends on it being the
    // ONLY enabled local admin.
    env: { ...process.env, NP_DEFAULT_ADMIN_DISABLED: '1' },
  })
  proc.unref()
  try {
    await waitReady(`${BASE_URL}/readyz`)
  } catch (err) {
    const log = fs.existsSync(path.join(dataDir, 'server.log')) ? fs.readFileSync(path.join(dataDir, 'server.log'), 'utf8') : ''
    throw new Error(`${(err as Error).message}\n--- server.log ---\n${log}`, { cause: err })
  }

  // 3) create a known-password admin (demo seeds only operator + viewer)
  const api = await request.newContext({ baseURL: BASE_URL, extraHTTPHeaders: { Authorization: `Bearer ${token}` } })
  const created = await api.post('/api/v1/users', {
    data: { name: USERS.admin.name, email: USERS.admin.email, password: USERS.admin.password, roles: ['admin'] },
  })
  if (![200, 201, 409].includes(created.status())) {
    throw new Error(`creating admin user failed: ${created.status()} ${await created.text()}`)
  }
  await api.dispose()

  // 4) form-login each role -> save its np_session cookie
  for (const role of Object.keys(USERS) as Role[]) {
    const u = USERS[role]
    const ctx = await request.newContext({ baseURL: BASE_URL })
    const res = await ctx.post('/login', { form: { email: u.email, password: u.password }, maxRedirects: 0 })
    if (res.status() !== 302) throw new Error(`login as ${role} (${u.email}) failed: ${res.status()} ${await res.text()}`)
    await ctx.storageState({ path: authFile(role) })
    await ctx.dispose()
  }

  fs.writeFileSync(RUNTIME_FILE, JSON.stringify({ pid: proc.pid, dataDir, port: PORT }))
  console.log(`[e2e] demo server ready on ${BASE_URL} (pid ${proc.pid}, data ${dataDir})`)
}
