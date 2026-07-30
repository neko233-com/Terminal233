import assert from 'node:assert/strict'
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { createStaticWebHandler } from './static-web.mjs'

async function withStaticServer(run) {
  const root = await mkdtemp(join(tmpdir(), 'nekossh-static-'))
  await mkdir(join(root, 'assets'))
  await writeFile(join(root, 'index.html'), '<!doctype html><title>NekoSSH</title>')
  await writeFile(join(root, 'assets', 'index-AbCdEf12.js'), 'export const ready = true')
  const handler = createStaticWebHandler(root)
  const server = http.createServer((request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    void handler(request, response, url).then((handled) => {
      if (!handled) {
        response.writeHead(404, { 'content-type': 'text/plain' })
        response.end('not found')
      }
    }).catch((error) => {
      response.destroy(error)
    })
  })
  server.listen(0, '127.0.0.1')
  await new Promise((resolve) => server.once('listening', resolve))
  const address = server.address()
  try {
    await run(`http://127.0.0.1:${address.port}`)
  } finally {
    await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
    await rm(root, { recursive: true, force: true })
  }
}

test('production static handler serves the Web macOS entry with hardened headers', async () => {
  await withStaticServer(async (baseUrl) => {
    const response = await fetch(`${baseUrl}/`)
    assert.equal(response.status, 200)
    assert.match(await response.text(), /NekoSSH/)
    assert.equal(response.headers.get('cache-control'), 'no-store')
    assert.match(response.headers.get('content-security-policy'), /object-src 'none'/)
    assert.equal(response.headers.get('cross-origin-opener-policy'), 'same-origin')
    assert.equal(response.headers.get('x-content-type-options'), 'nosniff')

    const head = await fetch(`${baseUrl}/`, { method: 'HEAD' })
    assert.equal(head.status, 200)
    assert.equal(await head.text(), '')
  })
})

test('production static handler caches hashed assets and rejects traversal or mutation', async () => {
  await withStaticServer(async (baseUrl) => {
    const asset = await fetch(`${baseUrl}/assets/index-AbCdEf12.js`)
    assert.equal(asset.status, 200)
    assert.equal(asset.headers.get('cache-control'), 'public, max-age=31536000, immutable')
    assert.equal(asset.headers.get('content-type'), 'text/javascript; charset=utf-8')

    assert.equal((await fetch(`${baseUrl}/%2e%2e/secret.txt`)).status, 404)
    assert.equal((await fetch(`${baseUrl}/missing.js`)).status, 404)
    assert.equal((await fetch(`${baseUrl}/`, { method: 'POST' })).status, 404)
  })
})
