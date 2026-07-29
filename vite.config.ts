import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { loadEnv } from 'vite'

export default defineConfig(({ mode }) => {
  const browserWorkspace = mode === 'web'
  const environment = loadEnv(mode, '.', '')
  const webPort = Number(environment.NEKOSSH_FRONTEND_PORT ?? 5173)
  const backendPort = Number(environment.NEKOSSH_BACKEND_PORT ?? 7331)
  const webBackend = `http://127.0.0.1:${backendPort}`

  return {
    base: browserWorkspace ? '/' : './',
    plugins: [react()],
    // Tauri stays on its fixed loopback dev URL.  The independent browser workspace deliberately
    // uses another port and proxies only its local HTTP backend, never Tauri IPC.
    server: {
      host: '127.0.0.1',
      port: browserWorkspace ? webPort : 1420,
      strictPort: true,
      proxy: browserWorkspace
        ? {
            '/api': { target: webBackend, changeOrigin: true },
            '/mcp': { target: webBackend, changeOrigin: true },
          }
        : undefined,
    },
    test: {
      environment: 'jsdom',
      setupFiles: './src/test/setup.ts',
      css: true,
    },
  }
})
