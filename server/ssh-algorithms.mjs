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
