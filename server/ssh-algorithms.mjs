/**
 * NekoSSH's Web runtime intentionally has no legacy SSH compatibility mode.
 *
 * ssh2's defaults still include ssh-rsa and HMAC-SHA1 for compatibility. Keeping an exact
 * allowlist here makes a server that only offers SHA-1-era transport algorithms fail closed
 * during negotiation instead of silently downgrading.
 */
export const MODERN_SSH_ALGORITHMS = Object.freeze({
  kex: Object.freeze([
    'curve25519-sha256',
    'curve25519-sha256@libssh.org',
    'ecdh-sha2-nistp256',
    'ecdh-sha2-nistp384',
    'ecdh-sha2-nistp521',
    'diffie-hellman-group-exchange-sha256',
    'diffie-hellman-group14-sha256',
    'diffie-hellman-group15-sha512',
    'diffie-hellman-group16-sha512',
    'diffie-hellman-group17-sha512',
    'diffie-hellman-group18-sha512',
  ]),
  serverHostKey: Object.freeze([
    'ssh-ed25519',
    'ecdsa-sha2-nistp256',
    'ecdsa-sha2-nistp384',
    'ecdsa-sha2-nistp521',
    'rsa-sha2-512',
    'rsa-sha2-256',
  ]),
  cipher: Object.freeze([
    'aes128-gcm',
    'aes128-gcm@openssh.com',
    'aes256-gcm',
    'aes256-gcm@openssh.com',
    'aes128-ctr',
    'aes192-ctr',
    'aes256-ctr',
  ]),
  hmac: Object.freeze([
    'hmac-sha2-256-etm@openssh.com',
    'hmac-sha2-512-etm@openssh.com',
    'hmac-sha2-256',
    'hmac-sha2-512',
  ]),
  compress: Object.freeze(['none']),
})

const MAX_HOST_KEY_BLOB_BYTES = 32 * 1024
const MIN_RSA_HOST_KEY_BITS = 2048
const MAX_RSA_HOST_KEY_BITS = 16 * 1024
const RSA_PUBLIC_EXPONENT_65537 = Buffer.from([0x01, 0x00, 0x01])
const ECDSA_CURVES = Object.freeze({
  'ecdsa-sha2-nistp256': Object.freeze({ curve: 'nistp256', pointBytes: 65 }),
  'ecdsa-sha2-nistp384': Object.freeze({ curve: 'nistp384', pointBytes: 97 }),
  'ecdsa-sha2-nistp521': Object.freeze({ curve: 'nistp521', pointBytes: 133 }),
})

function invalidHostKey(message) {
  const error = new Error(message)
  error.code = 'invalid_host_key'
  return error
}

class SshKeyCursor {
  constructor(bytes) {
    this.bytes = bytes
    this.offset = 0
  }

  readString() {
    if (this.offset + 4 > this.bytes.length) {
      throw invalidHostKey('SSH host key contains a truncated length field.')
    }
    const length = this.bytes.readUInt32BE(this.offset)
    this.offset += 4
    const end = this.offset + length
    if (!Number.isSafeInteger(end) || end > this.bytes.length) {
      throw invalidHostKey('SSH host key contains a truncated string field.')
    }
    const value = this.bytes.subarray(this.offset, end)
    this.offset = end
    return value
  }

  finish() {
    if (this.offset !== this.bytes.length) {
      throw invalidHostKey('SSH host key contains trailing data.')
    }
  }
}

function validateRsaHostKey(cursor) {
  const exponent = cursor.readString()
  if (!exponent.equals(RSA_PUBLIC_EXPONENT_65537)) {
    throw invalidHostKey('RSA host key must use public exponent 65537.')
  }

  let modulus = cursor.readString()
  if (modulus.length < 2) {
    throw invalidHostKey('RSA host key modulus is malformed.')
  }
  if (modulus[0] === 0) {
    if ((modulus[1] & 0x80) === 0) {
      throw invalidHostKey('RSA host key modulus is not minimally encoded.')
    }
    modulus = modulus.subarray(1)
  } else if ((modulus[0] & 0x80) !== 0) {
    throw invalidHostKey('RSA host key modulus is encoded as a negative integer.')
  }
  const modulusBits = ((modulus.length - 1) * 8) + (32 - Math.clz32(modulus[0]))
  if (modulusBits < MIN_RSA_HOST_KEY_BITS || modulusBits > MAX_RSA_HOST_KEY_BITS) {
    throw invalidHostKey(
      `RSA host key must be between ${MIN_RSA_HOST_KEY_BITS} and ${MAX_RSA_HOST_KEY_BITS} bits.`,
    )
  }
}

/**
 * Validates the complete public-key blob delivered by ssh2's hostVerifier.
 *
 * Negotiation already limits signature algorithms. This additionally rejects structurally
 * malformed keys and RSA keys whose modulus/exponent are below NekoSSH's production baseline.
 */
export function validatePresentedHostKey(publicKey) {
  const bytes = Buffer.from(publicKey)
  if (bytes.length === 0 || bytes.length > MAX_HOST_KEY_BLOB_BYTES) {
    throw invalidHostKey('SSH host key has an invalid size.')
  }

  const cursor = new SshKeyCursor(bytes)
  const algorithm = cursor.readString().toString('ascii')
  if (algorithm === 'ssh-rsa') {
    validateRsaHostKey(cursor)
  } else if (algorithm === 'ssh-ed25519') {
    if (cursor.readString().length !== 32) {
      throw invalidHostKey('Ed25519 host key must contain exactly 32 public-key bytes.')
    }
  } else if (Object.hasOwn(ECDSA_CURVES, algorithm)) {
    const expected = ECDSA_CURVES[algorithm]
    if (cursor.readString().toString('ascii') !== expected.curve) {
      throw invalidHostKey('ECDSA host key curve does not match its algorithm.')
    }
    const point = cursor.readString()
    if (point.length !== expected.pointBytes || point[0] !== 0x04) {
      throw invalidHostKey('ECDSA host key point is malformed.')
    }
  } else {
    throw invalidHostKey(`SSH host key type is not allowed: ${algorithm || 'unknown'}.`)
  }
  cursor.finish()
  return algorithm
}

export function modernSshAlgorithms() {
  return Object.fromEntries(
    Object.entries(MODERN_SSH_ALGORITHMS).map(([category, algorithms]) => [
      category,
      [...algorithms],
    ]),
  )
}

export function connectModernSshClient(client, config) {
  client.connect({
    ...config,
    algorithms: modernSshAlgorithms(),
  })
}
