import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  // Keep the Vite endpoint aligned with Tauri's `build.devUrl`.  Binding only to loopback
  // avoids accidentally exposing the development UI (and its IPC surface) on the LAN.
  server: {
    host: '127.0.0.1',
    port: 1420,
    strictPort: true,
  },
  test: {
    environment: 'jsdom',
    setupFiles: './src/test/setup.ts',
    css: true,
  },
})
