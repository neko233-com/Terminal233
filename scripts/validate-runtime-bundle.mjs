import { readFile, readdir } from 'node:fs/promises'
import { resolve } from 'node:path'

const mode = process.argv[2]
if (mode !== 'terminal233' && mode !== 'tauri' && mode !== 'web') {
  throw new Error('Usage: node scripts/validate-runtime-bundle.mjs <terminal233|web>')
}

const assetsDirectory = resolve('dist/assets')
const sources = await Promise.all(
  (await readdir(assetsDirectory))
    .filter((name) => name.endsWith('.js'))
    .map(async (name) => [name, await readFile(resolve(assetsDirectory, name), 'utf8')]),
)
const combined = sources.map(([, source]) => source).join('\n')
const forbidden = mode === 'web'
  ? ['__TAURI_INTERNALS__', 'ide_connection_create', 'ide_terminal_start']
  : ['/api/browser-session', 'ssh_connection_attach', '/api/connections']

for (const marker of forbidden) {
  if (combined.includes(marker)) {
    throw new Error(`${mode} bundle unexpectedly contains the other runtime marker: ${marker}`)
  }
}

const expectedChunk = mode === 'web' ? 'WebMacWorkspace-' : 'Terminal233IdeApp-'
if (!sources.some(([name]) => name.startsWith(expectedChunk))) {
  throw new Error(`${mode} bundle is missing its expected ${expectedChunk} chunk.`)
}

console.log(`Validated ${mode} runtime bundle isolation across ${sources.length} JavaScript assets.`)
