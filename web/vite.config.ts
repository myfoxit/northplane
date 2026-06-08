import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// SPEC §12.2: hashed assets (Vite default), code-splitting per route,
// dev proxy against a local northplaned (started by `make dev`).
//
// Every server-owned route is proxied so the SPA on :5173 talks to a
// fully live backend: cookies stay same-origin, the SSE stream
// (/api/v1/stream) flows through, and the server-rendered pages
// (login/setup/status) work unchanged. NP_API overrides the target
// (default matches scripts/dev.sh).
const target = process.env.NP_API ?? 'http://127.0.0.1:8443'

// The live SSE stream is long-lived and must never get a response or
// inactivity timeout, or the update channel would be cut periodically.
// Every other backend route gets a bounded timeout so a request issued
// while `make dev` is rebuilding/restarting northplaned fails fast
// (react-query then retries against the now-up backend) instead of
// hanging on an open socket for ~20s.
const PROXY_TIMEOUT_MS = 8000
type ProxyEntry = { target: string; changeOrigin: boolean; timeout?: number; proxyTimeout?: number }
const proxy: Record<string, ProxyEntry> = {
  '/api/v1/stream': { target, changeOrigin: true }, // SSE: no timeout (listed first so it wins over /api)
}
for (const path of ['/api', '/auth', '/login', '/setup', '/status', '/mcp', '/metrics', '/healthz', '/readyz']) {
  proxy[path] = { target, changeOrigin: true, timeout: PROXY_TIMEOUT_MS, proxyTimeout: PROXY_TIMEOUT_MS }
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    target: 'es2022',
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (id.includes('uplot')) return 'charts'
          if (id.includes('node_modules')) return 'vendor'
        },
      },
    },
  },
  server: { proxy },
})
