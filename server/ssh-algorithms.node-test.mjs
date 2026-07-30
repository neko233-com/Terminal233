import assert from 'node:assert/strict'
import { generateKeyPairSync } from 'node:crypto'
import { once } from 'node:events'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import ssh2 from 'ssh2'

import {
  MODERN_SSH_ALGORITHMS,
  connectModernSshClient,
  modernSshAlgorithms,
  validatePresentedHostKey,
} from './ssh-algorithms.mjs'

const LEGACY_ONLY_OFFER = {
  kex: ['diffie-hellman-group1-sha1', 'diffie-hellman-group14-sha1'],
  serverHostKey: ['ssh-rsa', 'ssh-dss'],
  cipher: ['3des-cbc', 'aes128-cbc', 'arcfour'],
  hmac: ['hmac-sha1', 'hmac-md5'],
  compress: ['zlib'],
}

test('Web SSH transport uses fresh exact modern algorithm lists', () => {
  let captured
  const client = {
    connect(config) {
      captured = config
    },
  }

  connectModernSshClient(client, { host: 'example.test', username: 'root' })

  assert.equal(captured.host, 'example.test')
  assert.equal(captured.username, 'root')
  assert.deepEqual(captured.algorithms, modernSshAlgorithms())
  assert.notStrictEqual(captured.algorithms.kex, MODERN_SSH_ALGORITHMS.kex)
  assert.notStrictEqual(captured.algorithms.hmac, MODERN_SSH_ALGORITHMS.hmac)
})

test('Web SSH transport has no downgrade path to a SHA-1-only server', () => {
  const modern = modernSshAlgorithms()
  for (const [category, legacyAlgorithms] of Object.entries(LEGACY_ONLY_OFFER)) {
    assert.deepEqual(
      modern[category].filter((algorithm) => legacyAlgorithms.includes(algorithm)),
      [],
      `${category} unexpectedly accepts a legacy-only algorithm`,
    )
  }

  const flattened = Object.values(modern).flat().join('\n')
  assert.doesNotMatch(
    flattened,
    /(?:sha1|ssh-rsa|ssh-dss|cbc|arcfour|3des|blowfish|cast128|md5|ripemd)/i,
  )
})

test('Web SSH transport rejects an actual legacy-only ssh2 server', { timeout: 5_000 }, async () => {
  const { privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
  const server = new ssh2.Server({
    hostKeys: [privateKey.export({ type: 'pkcs1', format: 'pem' })],
    algorithms: {
      kex: ['diffie-hellman-group14-sha1'],
      serverHostKey: ['ssh-rsa'],
      cipher: ['aes128-cbc'],
      hmac: ['hmac-sha1'],
      compress: ['none'],
    },
  }, (connection) => {
    connection.on('error', () => {})
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')

  const client = new ssh2.Client()
  try {
    const failure = once(client, 'error')
    connectModernSshClient(client, {
      host: '127.0.0.1',
      port: server.address().port,
      username: 'test',
      readyTimeout: 2_000,
      authHandler() { return false },
      hostVerifier() { return true },
    })
    const [error] = await failure
    assert.match(error.message, /no matching|handshake/i)
  } finally {
    client.destroy()
    server.close()
    await once(server, 'close')
  }
})

test('Web SSH host-key validation rejects weak RSA keys and accepts modern key shapes', () => {
  const { privateKey: strongRsa } = generateKeyPairSync('rsa', { modulusLength: 2048 })
  const { privateKey: weakRsa } = generateKeyPairSync('rsa', { modulusLength: 1024 })
  const { privateKey: wrongExponentRsa } = generateKeyPairSync('rsa', {
    modulusLength: 2048,
    publicExponent: 3,
  })
  const { publicKey: ed25519 } = generateKeyPairSync('ed25519')

  const rsaPublicBlob = (privateKey) => {
    const parsed = ssh2.utils.parseKey(privateKey.export({ type: 'pkcs1', format: 'pem' }))
    assert.ok(!(parsed instanceof Error))
    return parsed.getPublicSSH()
  }
  const sshString = (value) => {
    const bytes = Buffer.isBuffer(value) ? value : Buffer.from(value, 'ascii')
    const length = Buffer.allocUnsafe(4)
    length.writeUInt32BE(bytes.length)
    return Buffer.concat([length, bytes])
  }
  const ed25519PublicBlob = Buffer.concat([
    sshString('ssh-ed25519'),
    sshString(Buffer.from(ed25519.export({ format: 'jwk' }).x, 'base64url')),
  ])

  assert.equal(validatePresentedHostKey(rsaPublicBlob(strongRsa)), 'ssh-rsa')
  assert.equal(validatePresentedHostKey(ed25519PublicBlob), 'ssh-ed25519')
  assert.throws(
    () => validatePresentedHostKey(rsaPublicBlob(weakRsa)),
    /between 2048 and 16384 bits/,
  )
  assert.throws(
    () => validatePresentedHostKey(rsaPublicBlob(wrongExponentRsa)),
    /public exponent 65537/,
  )
  assert.throws(
    () => validatePresentedHostKey(Buffer.from([0, 0, 0, 7, 0x73, 0x73])),
    /truncated/,
  )
})

test('the Web backend cannot bypass the modern SSH connection wrapper', async () => {
  const source = await readFile(new URL('./ssh2js-server.mjs', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /\.connect\s*\(/)
  assert.equal(
    source.match(/connectModernSshClient\s*\(/g)?.length,
    2,
    'host-key probe and authenticated connection must both use the policy wrapper',
  )
})
