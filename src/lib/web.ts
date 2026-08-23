export interface ConnectionSummary {
  id: string
  label: string
  host: string
  port: number
  username: string
  expiresInSeconds: number
}

export type HostKeyStatus =
  | { status: 'trusted'; host: string; port: number; keyAlgorithm: string; fingerprint: string }
  | { status: 'confirmation_required'; confirmationId: string; host: string; port: number; keyAlgorithm: string; fingerprint: string }
  | { status: 'mismatch'; host: string; port: number; keyAlgorithm: string; fingerprint: string; expectedFingerprints: string[] }

export interface DirectoryEntry {
  name: string
  isDirectory: boolean
  size: number | null
  modifiedAtUnix: number | null
}

export interface DirectoryListing {
  path: string
  entries: DirectoryEntry[]
  truncated: boolean
}

export interface TerminalStarted { terminalId: string }
export interface TerminalChunk { cursor: number; dataBase64: string; bytes: number }
export interface TerminalPoll {
  cursor: number
  chunks: TerminalChunk[]
  truncated: boolean
  closed: boolean
  exitStatus: number | null
  error: string | null
}
export interface DeleteConfirmation { confirmationId: string; expiresInSeconds: number }
export interface AgentGrant { grant: string; expiresInSeconds: number }
interface BrowserSessionStarted { ok: true; releaseId: string }

export type WebAuthentication =
  | { kind: 'password'; password: string }
  | { kind: 'private_key'; privateKeyPem: string; passphrase: string | null }

export interface WebConnectionInput {
  label: string
  host: string
  port: number
  username: string
  authentication: WebAuthentication
  tags: string[]
  terminal: { columns: number; rows: number; command: null }
}

export class WebApiError extends Error {
  readonly code: string

  constructor(code: string, message: string) {
    super(message)
    this.code = code
  }
}

let browserSessionPromise: Promise<void> | null = null
let browserReleaseId: string | null = null
const browserSessionInvalidationListeners = new Set<() => void>()

function invalidateBrowserSession(expectedSession: Promise<void> | null): void {
  if (browserSessionPromise !== expectedSession) return
  const hadSession = browserSessionPromise !== null || browserReleaseId !== null
  browserSessionPromise = null
  browserReleaseId = null
  if (!hadSession) return
  for (const listener of browserSessionInvalidationListeners) {
    try {
      listener()
    } catch {
      // One UI subscriber must not prevent the request from returning the original API error.
    }
  }
}

function isBrowserSessionError(code: string): boolean {
  return code === 'browser_session_required' || code === 'unauthorized'
}

async function apiErrorFromResponse(
  response: Response,
  fallbackCode: string,
  fallbackMessage: string,
  expectedSession: Promise<void> | null,
): Promise<WebApiError> {
  const payload = await response.json().catch(() => null) as { code?: string; message?: string } | null
  const code = payload?.code ?? fallbackCode
  if (isBrowserSessionError(code)) invalidateBrowserSession(expectedSession)
  return new WebApiError(code, payload?.message ?? fallbackMessage)
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const expectedSession = browserSessionPromise
  const response = await fetch(path, {
    credentials: 'include',
    ...init,
    headers: {
      ...(init.body ? { 'content-type': 'application/json' } : {}),
      ...init.headers,
    },
  })
  if (response.status === 204) return undefined as T
  if (!response.ok) {
    throw await apiErrorFromResponse(
      response,
      'network_error',
      '本地 SSH 服务暂时不可用。',
      expectedSession,
    )
  }
  return response.json() as Promise<T>
}

export async function initializeBrowserSession(): Promise<void> {
  if (!browserSessionPromise) {
    browserSessionPromise = request<BrowserSessionStarted>('/api/browser-session', { method: 'POST' })
      .then((session) => {
        browserReleaseId = session.releaseId
      })
      .catch((error) => {
        browserSessionPromise = null
        throw error
      })
  }
  await browserSessionPromise
}

export function onBrowserSessionInvalidated(listener: () => void): () => void {
  browserSessionInvalidationListeners.add(listener)
  return () => browserSessionInvalidationListeners.delete(listener)
}

export function releaseBrowserSession(): void {
  const releaseId = browserReleaseId
  browserReleaseId = null
  browserSessionPromise = null
  if (!releaseId) return
  const body = JSON.stringify({ releaseId })
  if (typeof navigator.sendBeacon === 'function' && navigator.sendBeacon('/api/browser-session/close', body)) return
  void fetch('/api/browser-session/close', {
    method: 'POST',
    credentials: 'include',
    headers: { 'content-type': 'application/json' },
    body,
    keepalive: true,
  }).catch(() => undefined)
}

export function listConnections(): Promise<ConnectionSummary[]> {
  return request('/api/connections')
}

export function createConnection(input: WebConnectionInput): Promise<ConnectionSummary> {
  return request('/api/connections', { method: 'POST', body: JSON.stringify(input) })
}

export function disconnectConnection(id: string): Promise<void> {
  return request(`/api/connections/${id}`, { method: 'DELETE' })
}

export function prepareConnection(id: string): Promise<HostKeyStatus> {
  return request(`/api/connections/${id}/prepare`, { method: 'POST' })
}

export function trustConnection(id: string, confirmationId: string): Promise<HostKeyStatus> {
  return request(`/api/connections/${id}/trust`, { method: 'POST', body: JSON.stringify({ confirmationId }) })
}

export function createAgentGrant(id: string): Promise<AgentGrant> {
  return request(`/api/connections/${id}/agent-grant`, { method: 'POST' })
}

export function startTerminal(id: string, columns = 110, rows = 34): Promise<TerminalStarted> {
  return request(`/api/connections/${id}/terminal`, { method: 'POST', body: JSON.stringify({ columns, rows }) })
}

export function writeTerminal(terminalId: string, data: string): Promise<void> {
  return request(`/api/terminals/${terminalId}/input`, { method: 'POST', body: JSON.stringify({ data }) })
}

export function resizeTerminal(terminalId: string, columns: number, rows: number): Promise<void> {
  return request(`/api/terminals/${terminalId}/resize`, { method: 'POST', body: JSON.stringify({ columns, rows }) })
}

export function pollTerminal(terminalId: string, cursor: number): Promise<TerminalPoll> {
  return request(`/api/terminals/${terminalId}/poll?cursor=${encodeURIComponent(String(cursor))}`)
}

export function closeTerminal(terminalId: string): Promise<void> {
  return request(`/api/terminals/${terminalId}`, { method: 'DELETE' })
}

export function listDirectory(id: string, path: string): Promise<DirectoryListing> {
  return request(`/api/connections/${id}/files?path=${encodeURIComponent(path)}`)
}

export function createDirectory(id: string, path: string): Promise<void> {
  return request(`/api/connections/${id}/files/mkdir`, { method: 'POST', body: JSON.stringify({ path }) })
}

export function renameRemote(id: string, fromPath: string, toPath: string, overwrite = false): Promise<void> {
  return request(`/api/connections/${id}/files/rename`, { method: 'POST', body: JSON.stringify({ fromPath, toPath, overwrite }) })
}

export function prepareDelete(id: string, path: string): Promise<DeleteConfirmation> {
  return request(`/api/connections/${id}/files/delete-prepare`, { method: 'POST', body: JSON.stringify({ path }) })
}

export function commitDelete(id: string, confirmationId: string): Promise<void> {
  return request(`/api/connections/${id}/files/delete-commit`, { method: 'POST', body: JSON.stringify({ confirmationId }) })
}

export async function uploadFiles(id: string, remoteDirectory: string, files: File[]): Promise<{ uploaded: number; bytes: number }> {
  if (files.length < 1 || files.length > 32) {
    throw new WebApiError('invalid_upload', '每次只能上传 1 到 32 个文件。')
  }
  const oversized = files.find((file) => file.size > 16 * 1024 * 1024)
  if (oversized) {
    throw new WebApiError('file_too_large', `“${oversized.name}”超过 16 MB 的单文件上限。`)
  }
  const totalBytes = files.reduce((total, file) => total + file.size, 0)
  if (totalBytes > 45 * 1024 * 1024) {
    throw new WebApiError('upload_too_large', '单次上传总量不能超过 45 MB。')
  }
  const payload = []
  for (const file of files) {
    payload.push({
      name: file.name,
      dataBase64: toBase64(await file.arrayBuffer()),
      overwrite: false,
    })
  }
  return request(`/api/connections/${id}/files/upload`, {
    method: 'POST',
    body: JSON.stringify({ remoteDirectory, files: payload }),
  })
}

export async function downloadRemote(id: string, path: string): Promise<void> {
  const { name, data } = await downloadRemoteBytes(id, path)
  const blob = new Blob([data as BlobPart])
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = name
  link.hidden = true
  document.body.append(link)
  try {
    link.click()
  } finally {
    link.remove()
    window.setTimeout(() => URL.revokeObjectURL(url), 0)
  }
}

export async function downloadRemoteBytes(id: string, path: string): Promise<{ name: string; data: Uint8Array }> {
  const expectedSession = browserSessionPromise
  const response = await fetch(`/api/connections/${id}/files/download?path=${encodeURIComponent(path)}`, { credentials: 'include' })
  if (!response.ok) {
    throw await apiErrorFromResponse(
      response,
      'download_failed',
      '下载远端文件失败。',
      expectedSession,
    )
  }
  const data = new Uint8Array(
    typeof response.arrayBuffer === 'function'
      ? await response.arrayBuffer()
      : await (await response.blob()).arrayBuffer(),
  )
  const disposition = response.headers.get('content-disposition') ?? ''
  const pathParts = path.split('/').filter(Boolean)
  return { name: downloadFileName(disposition, pathParts[pathParts.length - 1] ?? 'download'), data }
}

function downloadFileName(disposition: string, fallback: string): string {
  const candidates: string[] = []
  const extended = disposition.match(/(?:^|;)\s*filename\*\s*=\s*UTF-8''([^;]*)/i)?.[1]?.trim()
  if (extended) {
    try {
      candidates.push(decodeURIComponent(extended.replace(/^"(.*)"$/, '$1')))
    } catch {
      // Ignore malformed extended values and fall back to the quoted or remote-path basename.
    }
  }
  const quoted = disposition.match(/(?:^|;)\s*filename\s*=\s*"([^"]*)"/i)?.[1]
  if (quoted) candidates.push(quoted)
  candidates.push(fallback)
  return candidates.find(isSafeDownloadFileName) ?? 'download'
}

function isSafeDownloadFileName(value: string): boolean {
  return value.length > 0
    && value.length <= 255
    && value !== '.'
    && value !== '..'
    && !value.includes('/')
    && !value.includes('\\')
    && !/[\0-\x1f\x7f]/.test(value)
}

function toBase64(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  const chunk = 0x8000
  let binary = ''
  for (let offset = 0; offset < bytes.length; offset += chunk) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunk))
  }
  return btoa(binary)
}

export function decodeBase64(value: string): Uint8Array {
  const binary = atob(value)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
  return bytes
}
