import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import { attachmentContentDisposition } from './content-disposition.mjs'

test('download Content-Disposition preserves Unicode and quotes through RFC 5987', () => {
  assert.equal(
    attachmentContentDisposition('中文"笔记.txt'),
    'attachment; filename="_____.txt"; filename*=UTF-8\'\'%E4%B8%AD%E6%96%87%22%E7%AC%94%E8%AE%B0.txt',
  )
})

test('download Content-Disposition rejects path and control characters', () => {
  for (const invalid of ['', '.', '..', '../secret', 'a/b', 'a\\b', 'line\nfeed', '\uD800']) {
    assert.throws(() => attachmentContentDisposition(invalid), TypeError)
  }
})

test('the Web download route validates the response filename before reading remote bytes', async () => {
  const source = await readFile(new URL('./ssh2js-server.mjs', import.meta.url), 'utf8')
  const routeStart = source.indexOf("suffix === 'files/download'")
  const routeEnd = source.indexOf("suffix === 'files/delete-prepare'", routeStart)
  const route = source.slice(routeStart, routeEnd)

  assert.ok(routeStart >= 0 && routeEnd > routeStart)
  assert.ok(route.indexOf('validateFileName(path.basename(remotePath))') >= 0)
  assert.ok(route.indexOf('validateFileName(path.basename(remotePath))') < route.indexOf('downloadFile(connection'))
  assert.ok(route.indexOf('attachmentContentDisposition(downloadName)') < route.indexOf('downloadFile(connection'))
})

test('the portable build, verifier, and launcher all require the header module', async () => {
  const [builder, verifier, launcher] = await Promise.all([
    readFile(new URL('../scripts/build-windows-web-portable.ps1', import.meta.url), 'utf8'),
    readFile(new URL('../scripts/verify-windows-web-portable.ps1', import.meta.url), 'utf8'),
    readFile(new URL('../web-launcher/src/main.rs', import.meta.url), 'utf8'),
  ])
  for (const source of [builder, verifier, launcher]) {
    assert.match(source, /content-disposition\.mjs/)
  }
})
