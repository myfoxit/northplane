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
const proxy = Object.fromEntries(
  ['/api', '/auth', '/login', '/setup', '/status', '/mcp', '/metrics', '/healthz', '/readyz']
    .map((path) => [path, { target, changeOrigin: true }]),
)

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
