import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// SPEC §12.2: hashed assets (Vite default), code-splitting per route,
// dev proxy against a local northplaned.
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
  server: {
    proxy: {
      '/api': { target: 'http://localhost:8443', changeOrigin: true },
      '/auth': { target: 'http://localhost:8443', changeOrigin: true },
    },
  },
})
