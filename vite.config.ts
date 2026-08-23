import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { loadEnv } from 'vite'

declare const process: { cwd: () => string }

export default defineConfig(({ mode }) => {
  const browserWorkspace = mode === 'web'
  const browserIde = mode === 'browser'
  const environment = loadEnv(mode, '.', '')
  const webPort = Number(environment.TERMINAL233_FRONTEND_PORT ?? environment.NEKOSSH_FRONTEND_PORT ?? 5173)
  const browserIdePort = Number(environment.TERMINAL233_BROWSER_FRONTEND_PORT ?? environment.TERMINAL233_FRONTEND_PORT ?? 1420)
  const backendPort = Number(environment.TERMINAL233_BACKEND_PORT ?? environment.NEKOSSH_BACKEND_PORT ?? 7331)
  const webBackend = `http://127.0.0.1:${backendPort}`
  const projectRoot = process.cwd().replaceAll('\\', '/')
  const runtimeImplementation = `${projectRoot}/src/lib/${browserIde ? 'ide-browser.ts' : 'ide.ts'}`

  return {
    base: browserWorkspace || browserIde ? '/' : './',
    plugins: [react()],
    resolve: {
      alias: [
        { find: './lib/ide-runtime', replacement: runtimeImplementation },
        { find: /[\\/]src[\\/]lib[\\/]ide-runtime(?:\.ts)?$/, replacement: runtimeImplementation },
      ],
    },
    // The native Terminal233 client stays on its fixed loopback dev URL. The independent browser
    // workspace deliberately uses another port and proxies only its local HTTP backend.
    server: {
      host: '127.0.0.1',
      port: browserWorkspace ? webPort : browserIde ? browserIdePort : 1420,
      strictPort: true,
      proxy: browserWorkspace || browserIde
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
