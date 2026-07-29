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
export interface TerminalChunk { cursor: number; data: string }
export interface TerminalPoll { cursor: number; chunks: TerminalChunk[]; closed: boolean; exitStatus: number | null; error: string | null }
export interface DeleteConfirmation { confirmationId: string; expiresInSeconds: number }
export interface AgentGrant { grant: string; expiresInSeconds: number }
interface BrowserSessionStarted { ok: true; releaseId: string }

export interface PasswordConnectionInput {
  label: string
  host: string
  port: number
  username: string
  authentication: { kind: 'password'; password: string }
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

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
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
    const payload = await response.json().catch(() => null) as { code?: string; message?: string } | null
    throw new WebApiError(payload?.code ?? 'network_error', payload?.message ?? '本地 SSH 服务暂时不可用。')
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

export function createConnection(input: PasswordConnectionInput): Promise<ConnectionSummary> {
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
  const payload = await Promise.all(files.map(async (file) => ({
    name: file.name,
    dataBase64: toBase64(await file.arrayBuffer()),
    overwrite: false,
  })))
  return request(`/api/connections/${id}/files/upload`, {
    method: 'POST',
    body: JSON.stringify({ remoteDirectory, files: payload }),
  })
}

export async function downloadRemote(id: string, path: string): Promise<void> {
  const response = await fetch(`/api/connections/${id}/files/download?path=${encodeURIComponent(path)}`, { credentials: 'include' })
  if (!response.ok) {
    const payload = await response.json().catch(() => null) as { code?: string; message?: string } | null
    throw new WebApiError(payload?.code ?? 'download_failed', payload?.message ?? '下载远端文件失败。')
  }
  const blob = await response.blob()
  const disposition = response.headers.get('content-disposition') ?? ''
  const matched = disposition.match(/filename="([^"]+)"/)
  const pathParts = path.split('/').filter(Boolean)
  const name = matched?.[1] ?? pathParts[pathParts.length - 1] ?? 'download'
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = name
  link.click()
  URL.revokeObjectURL(url)
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
