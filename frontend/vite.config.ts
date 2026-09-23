import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// https://vite.dev/config/
//
// The dev server proxies /api to the real backend, exactly mirroring how Caddy
// routes the same path in production (deploy/Caddyfile) - so the app can always
// call relative paths like `/api/v1/...` with no separate base-URL config and no
// CORS handling needed on the Go backend (same-origin either way).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.VITE_BACKEND_URL ?? 'http://127.0.0.1:48080',
        changeOrigin: true,
      },
    },
  },
})
