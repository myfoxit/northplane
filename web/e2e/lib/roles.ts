// Shared constants for the E2E harness: ports, paths, and the three demo
// roles. PW_PORT lets several isolated suites (each booting its own demo
// server + DB) run in parallel — every port gets its own auth dir + runtime
// file, so authoring agents never collide. CI uses the default port.
import { fileURLToPath } from 'node:url'
import path from 'node:path'

export const PORT = Number(process.env.PW_PORT || 18973)
export const BASE_URL = `http://127.0.0.1:${PORT}`

const here = path.dirname(fileURLToPath(import.meta.url)) // web/e2e/lib
export const E2E_DIR = path.resolve(here, '..') // web/e2e
export const WEB_DIR = path.resolve(here, '../..') // web
export const REPO_ROOT = path.resolve(here, '../../..') // repo root
export const BIN = path.join(REPO_ROOT, 'bin', 'northplaned')

export const AUTH_DIR = path.join(E2E_DIR, `.auth-${PORT}`)
export const RUNTIME_FILE = path.join(E2E_DIR, `.runtime-${PORT}.json`)

// admin is created by global-setup (demo seeds only operator + viewer);
// operator/viewer are the hardcoded demo credentials (internal/demo/demo.go).
export const USERS = {
  admin: { email: 'admin@e2e.local', password: 'e2e-admin-pass-2026', name: 'E2E Admin' },
  operator: { email: 'operator@demo.local', password: 'operator-demo-2026!', name: 'operator' },
  viewer: { email: 'viewer@demo.local', password: 'viewer-demo-2026!', name: 'viewer' },
} as const

export type Role = keyof typeof USERS
export const authFile = (role: Role) => path.join(AUTH_DIR, `${role}.json`)
