// Stops the demo server booted by global-setup and removes its temp dataDir
// and per-port auth dir. Best-effort: never throws (teardown failures must not
// mask test results).
import fs from 'node:fs'
import { RUNTIME_FILE, AUTH_DIR } from './lib/roles'

export default async function globalTeardown(): Promise<void> {
  try {
    const { pid, dataDir } = JSON.parse(fs.readFileSync(RUNTIME_FILE, 'utf8')) as { pid: number; dataDir: string }
    // detached child is a process-group leader: kill the whole group.
    try {
      process.kill(-pid, 'SIGTERM')
    } catch {
      try {
        process.kill(pid, 'SIGTERM')
      } catch {
        /* already gone */
      }
    }
    fs.rmSync(RUNTIME_FILE, { force: true })
    if (dataDir) fs.rmSync(dataDir, { recursive: true, force: true })
  } catch {
    /* nothing to tear down */
  }
  try {
    fs.rmSync(AUTH_DIR, { recursive: true, force: true })
  } catch {
    /* ignore */
  }
}
