import { beforeEach, describe, expect, it, vi } from 'vitest'

const tauri = vi.hoisted(() => ({
  invoke: vi.fn(),
  isTauri: vi.fn(),
}))

vi.mock('@tauri-apps/api/core', () => ({
  invoke: tauri.invoke,
  isTauri: tauri.isTauri,
}))

import {
  configureSync,
  createProfile,
  uploadGitEncryptedBundle,
  restoreWebDavEncryptedBackup,
  restoreGitEncryptedBackup,
  normalizeExternalBrowserUrl,
  openExternalBrowser,
  providerForRemote,
  uploadWebDavEncryptedBundle,
} from './desktop'

describe('desktop bridge', () => {
  beforeEach(() => {
    tauri.invoke.mockReset()
    tauri.isTauri.mockReset()
    tauri.isTauri.mockReturnValue(true)
  })

  it('serializes a Git sync setup without embedding the token in the remote URL', async () => {
    tauri.invoke.mockResolvedValue({ kind: 'git' })

    await configureSync({
      provider: 'git',
      remoteUrl: 'git@github.com:neko/nekossh-vault.git',
      branch: 'main',
      accessToken: 'example-token',
    })

    expect(tauri.invoke).toHaveBeenCalledWith('sync_configure', {
      input: {
        kind: 'git',
        provider: 'github',
        remote_url: 'git@github.com:neko/nekossh-vault.git',
        branch: 'main',
        access_token: 'example-token',
      },
    })
  })

  it('serializes a key-file profile through the narrow profile_create command', async () => {
    tauri.invoke.mockResolvedValue({ id: 'profile-id' })

    await createProfile({
      label: 'production',
      host: 'ssh.example.test',
      port: 2222,
      username: 'neko',
      authentication: { kind: 'private_key_file', privateKeyPath: 'C:/keys/id_ed25519' },
      tags: ['production'],
    })

    expect(tauri.invoke).toHaveBeenCalledWith('profile_create', {
      input: {
        label: 'production',
        host: 'ssh.example.test',
        port: 2222,
        username: 'neko',
        authentication: { kind: 'private_key_file', private_key_path: 'C:/keys/id_ed25519' },
        tags: ['production'],
        terminal: { columns: 120, rows: 32, command: null },
      },
    })
  })

  it('requests a WebDAV upload only after the desktop vault has prepared the encrypted bundle', async () => {
    tauri.invoke.mockResolvedValue({
      kind: 'web_dav',
      operation: 'upload',
      encryptedBundleSha256: 'sha256:example',
      bytes: 4096,
    })

    await expect(uploadWebDavEncryptedBundle()).resolves.toMatchObject({ operation: 'upload', bytes: 4096 })
    expect(tauri.invoke).toHaveBeenCalledWith('sync_webdav_upload', undefined)
  })

  it('delegates backup verification and replacement to the Rust restore command', async () => {
    tauri.invoke.mockResolvedValue({ backupCreated: true, rollbackAvailable: true, bytes: 4096 })

    await expect(restoreWebDavEncryptedBackup('backup-password')).resolves.toMatchObject({ rollbackAvailable: true, bytes: 4096 })
    expect(tauri.invoke).toHaveBeenCalledWith('sync_webdav_restore_import', { password: 'backup-password' })
  })

  it('requests a Git upload only through the narrow encrypted-bundle command', async () => {
    tauri.invoke.mockResolvedValue({
      kind: 'git',
      operation: 'upload',
      encryptedBundleSha256: 'sha256:example',
      bytes: 4096,
    })

    await expect(uploadGitEncryptedBundle()).resolves.toMatchObject({ kind: 'git', operation: 'upload' })
    expect(tauri.invoke).toHaveBeenCalledWith('sync_git_upload', undefined)
  })

  it('delegates a Git restore to the Rust download-and-verify command', async () => {
    tauri.invoke.mockResolvedValue({ backupCreated: true, rollbackAvailable: true, bytes: 4096 })

    await expect(restoreGitEncryptedBackup('backup-password')).resolves.toMatchObject({ rollbackAvailable: true, bytes: 4096 })
    expect(tauri.invoke).toHaveBeenCalledWith('sync_git_restore_import', { password: 'backup-password' })
  })

  it('allows only HTTPS or loopback HTTP browser destinations', () => {
    expect(normalizeExternalBrowserUrl('https://console.example.test/path')).toBe('https://console.example.test/path')
    expect(normalizeExternalBrowserUrl('http://localhost:3000')).toBe('http://localhost:3000/')
    expect(() => normalizeExternalBrowserUrl('javascript:alert(1)')).toThrow('只允许 HTTPS 地址')
    expect(() => normalizeExternalBrowserUrl('http://example.test')).toThrow('只允许 HTTPS 地址')
    expect(() => normalizeExternalBrowserUrl('https://neko:secret@example.test')).toThrow('只允许 HTTPS 地址')
  })

  it('uses a user-initiated new tab only in the web runtime', async () => {
    tauri.isTauri.mockReturnValue(false)
    const open = vi.spyOn(window, 'open').mockReturnValue(null)

    await expect(openExternalBrowser('https://console.example.test')).resolves.toBe('https://console.example.test/')

    expect(open).toHaveBeenCalledWith('https://console.example.test/', '_blank', 'noopener,noreferrer')
    expect(tauri.invoke).not.toHaveBeenCalled()
    open.mockRestore()
  })

  it('identifies common free Git hosts without trusting arbitrary URL schemes', () => {
    expect(providerForRemote('https://github.com/neko/nekossh.git')).toBe('github')
    expect(providerForRemote('ssh://git@gitea.example.test/neko/nekossh.git')).toBe('gitea')
    expect(providerForRemote('git@forge.example.test:neko/nekossh.git')).toBe('generic')
  })
})
