import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { tanstackRouter } from '@tanstack/router-plugin/vite'
import { fileURLToPath } from 'node:url'

// Admin API of a running pulsed (make dev-up publishes it on 127.0.0.1:8080).
const api = process.env.PULSE_API ?? 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [tanstackRouter({ target: 'react', autoCodeSplitting: true }), react()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    // Embedded into the Go binary (internal/ui). emptyOutDir wipes the
    // directory; public/README.txt is copied back so the placeholder that
    // keeps `go:embed all:dist` compiling survives every build.
    outDir: '../internal/ui/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
  },
  server: {
    // /auth/oidc/* are the single sign-on redirects (outside the JSON API).
    proxy: { '/api': api, '/auth': api },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
