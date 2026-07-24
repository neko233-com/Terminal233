import { invoke, isTauri } from '@tauri-apps/api/core'

export interface VaultStatus {
  exists: boolean
  unlocked: boolean
  format: string | null
  vaultPathHint: string
}

export interface TerminalPreferences {
  columns: number
  rows: number
  command: string | null
}

export interface ProfileSummary {
  id: string
  label: string
  host: string
  port: number
  username: string
  authenticationKind: 'agent' | 'password' | 'private_key' | 'private_key_file'
  tags: string[]
  terminal: TerminalPreferences
  createdAtUnixMs: number
  updatedAtUnixMs: number
}

export type ProfileAuthenticationDraft =
  | { kind: 'agent' }
  | { kind: 'password'; password: string }
  | { kind: 'private_key_file'; privateKeyPath: string }

export interface ProfileDraft {
  label: string
  host: string
  port: number
  username: string
  authentication: ProfileAuthenticationDraft
  tags: string[]
}

export type SyncProvider = 'webdav' | 'git'

export interface SyncConfigurationSummary {
  kind: 'web_dav' | 'git'
  endpointOrRemote: string
  remotePath: string | null
  branch: string | null
  provider: 'github' | 'gitea' | 'generic' | null
  credentialConfigured: boolean
}

export interface SyncTransferReceipt {
  kind: 'web_dav' | 'git'
  operation: 'upload' | 'download'
  encryptedBundleSha256: string
  bytes: number
}

export type WebDavTransferReceipt = SyncTransferReceipt & { kind: 'web_dav' }
export type GitTransferReceipt = SyncTransferReceipt & { kind: 'git' }

/** An encrypted envelope that may safely cross IPC, but contains no decrypted Vault data. */
export interface EncryptedSyncBundle {
  format: string
  exportedAtUnixMs: number
  vault: unknown
}

/** Non-sensitive result of checking a downloaded backup with the user-supplied backup password. */
export interface VerifiedSyncBundle {
  format: string
  exportedAtUnixMs: number
  encryptedBundleSha256: string
  bytes: number
  profileCount: number
  syncKind: 'web_dav' | 'git' | null
}

/** Non-sensitive result after a verified vault replacement. */
export interface VaultImportReceipt {
  status: VaultStatus
  encryptedBundleSha256: string
  bytes: number
  backupCreated: boolean
  rollbackAvailable: boolean
  rollbackPathHint: string | null
  restoredFromBackup: boolean
}

export type SyncDraft =
  | {
      provider: 'webdav'
      endpoint: string
      remotePath: string
      username: string
      secret: string
    }
  | {
      provider: 'git'
      remoteUrl: string
      branch: string
      accessToken: string
    }

export interface CommandErrorShape {
  code?: string
  message?: string
}

export function isDesktopRuntime(): boolean {
  return isTauri()
}

export function errorMessage(error: unknown, fallback = '操作未完成，请稍后重试。'): string {
  if (typeof error === 'object' && error !== null && 'message' in error) {
    const { message } = error as CommandErrorShape
    if (typeof message === 'string' && message.trim()) return message
  }
  if (typeof error === 'string' && error.trim()) return error
  return fallback
}

async function invokeDesktop<T>(command: string, args?: Record<string, unknown>): Promise<T> {
  if (!isDesktopRuntime()) {
    throw new Error('此操作需要在 NekoSSH 桌面端完成。')
  }
  return invoke<T>(command, args)
}

export async function readVaultStatus(): Promise<VaultStatus | null> {
  if (!isDesktopRuntime()) return null
  return invokeDesktop<VaultStatus>('vault_status')
}

export async function bootstrapVault(password: string): Promise<VaultStatus> {
  return invokeDesktop<VaultStatus>('vault_bootstrap', { password })
}

export async function unlockVault(password: string): Promise<VaultStatus> {
  return invokeDesktop<VaultStatus>('vault_unlock', { password })
}

export async function lockVault(): Promise<VaultStatus> {
  return invokeDesktop<VaultStatus>('vault_lock')
}

export async function changeVaultPassword(currentPassword: string, newPassword: string): Promise<void> {
  return invokeDesktop<void>('vault_change_password', { currentPassword, newPassword })
}

export async function listProfiles(): Promise<ProfileSummary[]> {
  return invokeDesktop<ProfileSummary[]>('profile_list')
}

export async function createProfile(draft: ProfileDraft): Promise<ProfileSummary> {
  const authentication = draft.authentication.kind === 'private_key_file'
    ? { kind: 'private_key_file', private_key_path: draft.authentication.privateKeyPath }
    : draft.authentication

  return invokeDesktop<ProfileSummary>('profile_create', {
    input: {
      label: draft.label,
      host: draft.host,
      port: draft.port,
      username: draft.username,
      authentication,
      tags: draft.tags,
      terminal: { columns: 120, rows: 32, command: null },
    },
  })
}

export async function launchExternalSsh(profileId: string): Promise<number> {
  return invokeDesktop<number>('ssh_launch_external', { profileId })
}

export async function readSyncConfiguration(): Promise<SyncConfigurationSummary | null> {
  return invokeDesktop<SyncConfigurationSummary | null>('sync_configuration')
}

export async function configureSync(draft: SyncDraft): Promise<SyncConfigurationSummary> {
  const input = draft.provider === 'webdav'
    ? {
        kind: 'web_dav',
        endpoint: draft.endpoint,
        remote_path: draft.remotePath,
        username: draft.username.trim() || null,
        secret: draft.secret,
      }
    : {
        kind: 'git',
        provider: providerForRemote(draft.remoteUrl),
        remote_url: draft.remoteUrl,
        branch: draft.branch,
        access_token: draft.accessToken.trim() || null,
      }

  return invokeDesktop<SyncConfigurationSummary>('sync_configure', { input })
}

export async function uploadWebDavEncryptedBundle(): Promise<WebDavTransferReceipt> {
  return invokeDesktop<WebDavTransferReceipt>('sync_webdav_upload')
}

export async function uploadGitEncryptedBundle(): Promise<GitTransferReceipt> {
  return invokeDesktop<GitTransferReceipt>('sync_git_upload')
}

export async function downloadWebDavEncryptedBundle(): Promise<EncryptedSyncBundle> {
  return invokeDesktop<EncryptedSyncBundle>('sync_webdav_download_encrypted_bundle')
}

export async function downloadGitEncryptedBundle(): Promise<EncryptedSyncBundle> {
  return invokeDesktop<EncryptedSyncBundle>('sync_git_download_encrypted_bundle')
}

export async function verifyEncryptedSyncBundle(bundle: EncryptedSyncBundle, password: string): Promise<VerifiedSyncBundle> {
  return invokeDesktop<VerifiedSyncBundle>('sync_verify_encrypted_bundle', { bundle, password })
}

export async function importVerifiedSyncBundle(bundle: EncryptedSyncBundle, password: string): Promise<VaultImportReceipt> {
  return invokeDesktop<VaultImportReceipt>('sync_import_encrypted_bundle', { bundle, password })
}

/**
 * Downloads, verifies and replaces a WebDAV backup entirely in the Rust process. The password is
 * zeroized there; the frontend receives only the non-sensitive receipt after the operation.
 */
export async function restoreWebDavEncryptedBackup(password: string): Promise<VaultImportReceipt> {
  return invokeDesktop<VaultImportReceipt>('sync_webdav_restore_import', { password })
}

export async function restoreGitEncryptedBackup(password: string): Promise<VaultImportReceipt> {
  return invokeDesktop<VaultImportReceipt>('sync_git_restore_import', { password })
}

export function providerForRemote(remoteUrl: string): 'github' | 'gitea' | 'generic' {
  const normalized = remoteUrl.toLowerCase()
  if (normalized.includes('github.com')) return 'github'
  if (normalized.includes('gitea')) return 'gitea'
  return 'generic'
}

export function normalizeExternalBrowserUrl(value: string): string {
  let parsed: URL
  try {
    parsed = new URL(value.trim())
  } catch {
    throw new Error('请输入有效的 HTTPS 地址，或本机 HTTP 地址。')
  }

  const isLoopback = ['localhost', '127.0.0.1', '[::1]', '::1'].includes(parsed.hostname)
  const usesAllowedProtocol = parsed.protocol === 'https:' || (parsed.protocol === 'http:' && isLoopback)
  if (!usesAllowedProtocol || parsed.username || parsed.password || parsed.hash) {
    throw new Error('只允许 HTTPS 地址，或不含凭据的本机 HTTP 地址。')
  }

  return parsed.toString()
}

/**
 * Desktop builds delegate validation and launch to Rust. The web build deliberately only opens a
 * user-initiated, normalized URL; it never attempts to tunnel raw SSH from the browser.
 */
export async function openExternalBrowser(value: string): Promise<string> {
  const url = normalizeExternalBrowserUrl(value)
  if (isDesktopRuntime()) {
    await invokeDesktop<void>('browser_open_url', { url })
    return url
  }

  window.open(url, '_blank', 'noopener,noreferrer')
  return url
}
