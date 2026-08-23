type RdesktopResponse = { success: boolean; data: unknown }
type RdesktopPush = { channelId?: string; event?: string; payload?: unknown }
type RdesktopChannelListener = (payload: unknown) => void

declare global {
  interface Window {
    __RDESKTOP_INVOKE__?: (command: string, payload?: unknown) => Promise<RdesktopResponse>
    __RDESKTOP_PUSH__?: (message: RdesktopPush) => void
  }
}

const channelListeners = new Map<string, RdesktopChannelListener>()
const eventListeners = new Map<string, Set<(payload: unknown) => void>>()

function installRdesktopPushBridge() {
  if (window.__RDESKTOP_PUSH__) return
  window.__RDESKTOP_PUSH__ = (message) => {
    if (message.channelId) channelListeners.get(message.channelId)?.(message.payload)
    if (message.event) {
      for (const listener of eventListeners.get(message.event) ?? []) listener(message.payload)
    }
  }
}

function createRdesktopChannel<T>(listener: (event: T) => void): { channelId: string } {
  installRdesktopPushBridge()
  const channelId = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  channelListeners.set(channelId, listener as RdesktopChannelListener)
  return { channelId }
}

async function invokeRdesktop<T>(command: string, payload: unknown = {}): Promise<T> {
  installRdesktopPushBridge()
  const invoke = window.__RDESKTOP_INVOKE__
  if (!invoke) throw new Error('Terminal233 原生 rdesktop 运行时不可用。')
  const response = await invoke(command, payload)
  if (!response.success) throw response.data
  return response.data as T
}

export interface IdeConnection {
  id: string
  label: string
  host: string
  port: number
  username: string
  tags: string[]
  expiresInSeconds: number
}

export interface IdeConnectionInput {
  label: string
  host: string
  port: number
  username: string
  authentication:
    | { kind: 'password'; password: string }
    | { kind: 'private_key'; privateKeyPem: string; passphrase: string | null }
  tags: string[]
  terminal: { columns: number; rows: number; command: string | null }
}

export type IdeHostKeyStatus =
  | { status: 'trusted'; host: string; port: number; keyAlgorithm: string; fingerprint: string }
  | { status: 'confirmation_required'; confirmationId: string; host: string; port: number; keyAlgorithm: string; fingerprint: string }
  | { status: 'mismatch'; host: string; port: number; keyAlgorithm: string; fingerprint: string; expectedFingerprints: string[] }

export interface IdeDirectoryEntry {
  name: string
  isDirectory: boolean
  size: number | null
  modifiedAtUnix: number | null
}

export interface IdeDirectoryListing {
  path: string
  entries: IdeDirectoryEntry[]
  truncated: boolean
}

export interface IdeTerminalStarted { sessionId: string }
export type IdeTerminalEvent =
  | { kind: 'output'; sessionId: string; sequence: number; dataBase64: string }
  | { kind: 'closed'; sessionId: string; exitStatus: number | null }
  | { kind: 'error'; sessionId: string; code: string }

export interface IdeTransferItem { fileId: string; name: string; size: number | null }
export interface IdeTransferStarted {
  jobId: string
  direction: 'upload' | 'download'
  totalFiles: number
  totalBytes: number | null
  items: IdeTransferItem[]
}
export interface IdeTransferProgress {
  jobId: string
  fileId: string
  name: string
  direction: 'upload' | 'download'
  fileBytesTransferred: number
  fileTotalBytes: number | null
  jobBytesTransferred: number
  jobTotalBytes: number | null
}
export interface IdeTransferDownloadChunk {
  jobId: string
  fileId: string
  name: string
  sequence: number
  offset: number
  dataBase64: string
}
export type IdeDownloadEvent =
  | ({ kind: 'started' } & IdeTransferStarted)
  | { kind: 'chunk'; jobId: string; fileId: string; name: string; sequence: number; offset: number; dataBase64: string }
  | ({ kind: 'completed' } & IdeTransferComplete)
export type IdeUploadEvent =
  | ({ kind: 'started' } & IdeTransferStarted)
  | ({ kind: 'progress' } & IdeTransferProgress)
  | ({ kind: 'completed' } & IdeTransferComplete)
export interface IdeTransferComplete {
  jobId: string
  direction: 'upload' | 'download'
  status: 'completed' | 'completed_with_errors' | 'cancelled'
  completedFiles: number
  failedFiles: number
  cancelledFiles: number
  bytesTransferred: number
  totalBytes: number | null
}

export interface IdeCommandErrorShape { code?: string; message?: string }

const TERMINAL_INPUT_CHUNK_BYTES = 16 * 1024
const TERMINAL_PENDING_INPUT_BYTES = 256 * 1024
const TERMINAL_BACKPRESSURE_RETRIES = 7
const textEncoder = new TextEncoder()

export function ideError(error: unknown): string {
  if (typeof error === 'string' && error.trim()) return error
  if (error instanceof Error && error.message.trim()) return error.message
  const candidate = error as IdeCommandErrorShape | null
  return typeof candidate?.message === 'string' && candidate.message.trim()
    ? candidate.message
    : '操作失败，请检查连接参数和服务器状态。'
}

export function createIdeConnection(input: IdeConnectionInput): Promise<IdeConnection> {
  return invokeRdesktop('ide_connection_create', { input })
}

export function listIdeConnections(): Promise<IdeConnection[]> {
  return invokeRdesktop('ide_connection_list')
}

export function disconnectIdeConnection(connectionId: string): Promise<void> {
  return invokeRdesktop('ide_connection_disconnect', { connectionId })
}

export function prepareIdeConnection(connectionId: string): Promise<IdeHostKeyStatus> {
  return invokeRdesktop('ide_ssh_prepare', { connectionId })
}

export function trustIdeHostKey(connectionId: string, confirmationId: string): Promise<IdeHostKeyStatus> {
  return invokeRdesktop('ide_ssh_trust_host_key', { connectionId, confirmationId })
}

export function rejectIdeHostKey(confirmationId: string): Promise<void> {
  return invokeRdesktop('ide_ssh_reject_host_key', { confirmationId })
}

export function listIdeDirectory(connectionId: string, path: string): Promise<IdeDirectoryListing> {
  return invokeRdesktop('ide_sftp_list', { connectionId, path })
}

export function createIdeDirectory(connectionId: string, path: string): Promise<void> {
  return invokeRdesktop('ide_sftp_mkdir', { connectionId, path })
}

export function renameIdeRemote(connectionId: string, fromPath: string, toPath: string, overwrite = false): Promise<void> {
  return invokeRdesktop('ide_sftp_rename', { connectionId, fromPath, toPath, overwrite })
}

export function deleteIdeRemote(connectionId: string, path: string): Promise<void> {
  return invokeRdesktop('ide_sftp_delete', { connectionId, path })
}

export async function uploadIdeFiles(
  connectionId: string,
  remoteDirectory: string,
  files: File[],
  onEvent: (event: IdeUploadEvent) => void,
): Promise<IdeTransferStarted> {
  if (files.length < 1 || files.length > 32) throw new Error('每次只能上传 1 到 32 个文件。')
  const oversized = files.find((file) => file.size > 16 * 1024 * 1024)
  if (oversized) throw new Error(`“${oversized.name}”超过 16 MB 的单文件上限。`)
  const totalBytes = files.reduce((total, file) => total + file.size, 0)
  if (totalBytes > 64 * 1024 * 1024) throw new Error('单次上传总量不能超过 64 MB。')
  const events = createRdesktopChannel<IdeUploadEvent>(onEvent)
  const inputs = []
  for (const file of files) {
    inputs.push({
      name: file.name,
      dataBase64: arrayBufferToBase64(await file.arrayBuffer()),
      overwrite: false,
    })
  }
  return invokeRdesktop('ide_sftp_upload_start', { connectionId, remoteDirectory, files: inputs, ...events })
}

export function downloadIdeFiles(
  connectionId: string,
  files: Array<{ remotePath: string; saveAs: string }>,
  onEvent: (event: IdeDownloadEvent) => void,
): Promise<IdeTransferStarted> {
  // The handler is constructed before invoke. A short remote file can therefore complete before
  // the command promise resolves without losing its first chunk or terminal status.
  const events = createRdesktopChannel<IdeDownloadEvent>(onEvent)
  return invokeRdesktop('ide_sftp_download_start', { connectionId, files, ...events })
}

export function cancelIdeTransfer(jobId: string): Promise<void> {
  return invokeRdesktop('ide_sftp_transfer_cancel', { jobId })
}

export function acknowledgeIdeDownloadChunk(jobId: string, sequence: number): Promise<void> {
  return invokeRdesktop('ide_sftp_download_ack', { jobId, sequence })
}

export function startIdeTerminal(
  connectionId: string,
  onEvent: (event: IdeTerminalEvent) => void,
  columns = 110,
  rows = 34,
): Promise<IdeTerminalStarted> {
  const events = createRdesktopChannel<IdeTerminalEvent>(onEvent)
  return invokeRdesktop('ide_terminal_start', { connectionId, columns, rows, ...events })
}

export function writeIdeTerminal(sessionId: string, data: string): Promise<void> {
  return invokeRdesktop('ide_terminal_write', { sessionId, data })
}

export function resizeIdeTerminal(sessionId: string, columns: number, rows: number): Promise<void> {
  return invokeRdesktop('ide_terminal_resize', { sessionId, columns, rows })
}

export function acknowledgeIdeTerminalOutput(sessionId: string, sequence: number): Promise<void> {
  return invokeRdesktop('ide_terminal_ack_output', { sessionId, sequence })
}

export function closeIdeTerminal(sessionId: string): Promise<void> {
  return invokeRdesktop('ide_terminal_close', { sessionId })
}

export function decodeBase64Bytes(value: string): Uint8Array {
  const binary = atob(value)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
  return bytes
}

export function splitTerminalInput(data: string, maxBytes = TERMINAL_INPUT_CHUNK_BYTES): string[] {
  if (!data) return []
  if (!Number.isSafeInteger(maxBytes) || maxBytes < 4) throw new Error('终端输入分块大小无效。')
  const chunks: string[] = []
  let current = ''
  let currentBytes = 0
  for (const character of data) {
    const characterBytes = textEncoder.encode(character).byteLength
    if (current && currentBytes + characterBytes > maxBytes) {
      chunks.push(current)
      current = ''
      currentBytes = 0
    }
    current += character
    currentBytes += characterBytes
  }
  if (current) chunks.push(current)
  return chunks
}

type TerminalInputSender = (sessionId: string, data: string) => Promise<void>

export class IdeTerminalInputQueue {
  #closed = false
  #failed = false
  #pendingBytes = 0
  #tail: Promise<void> = Promise.resolve()

  constructor(
    private readonly sessionId: string,
    private readonly onError: (error: unknown) => void,
    private readonly sender: TerminalInputSender = writeIdeTerminal,
  ) {}

  enqueue(data: string): boolean {
    if (this.#closed || this.#failed) return false
    if (!data) return true
    const byteLength = textEncoder.encode(data).byteLength
    if (byteLength > TERMINAL_PENDING_INPUT_BYTES - this.#pendingBytes) return false
    const chunks = splitTerminalInput(data)
    this.#pendingBytes += byteLength
    const operation = this.#tail.then(async () => {
      if (this.#closed || this.#failed) return
      for (const chunk of chunks) {
        if (this.#closed || this.#failed) return
        await sendTerminalInputChunk(
          this.sessionId,
          chunk,
          this.sender,
          () => this.#closed || this.#failed,
        )
      }
    })
    this.#tail = operation
      .catch((error: unknown) => {
        if (!this.#closed && !this.#failed) {
          this.#failed = true
          this.onError(error)
        }
      })
      .finally(() => {
        this.#pendingBytes = Math.max(0, this.#pendingBytes - byteLength)
      })
    return true
  }

  close() {
    this.#closed = true
  }
}

async function sendTerminalInputChunk(
  sessionId: string,
  data: string,
  sender: TerminalInputSender,
  isCancelled: () => boolean,
) {
  for (let attempt = 0; ; attempt += 1) {
    if (isCancelled()) return
    try {
      await sender(sessionId, data)
      return
    } catch (error) {
      if (
        (error as IdeCommandErrorShape | null)?.code !== 'ssh_terminal_input_backpressure'
        || attempt >= TERMINAL_BACKPRESSURE_RETRIES
      ) {
        throw error
      }
      await new Promise((resolve) => window.setTimeout(resolve, 8 * (2 ** attempt)))
    }
  }
}

export async function onIdeTransferProgress(handler: (event: IdeTransferProgress) => void): Promise<() => void> {
  installRdesktopPushBridge()
  const listeners = eventListeners.get('ssh-sftp-transfer-progress') ?? new Set()
  listeners.add(handler as (payload: unknown) => void)
  eventListeners.set('ssh-sftp-transfer-progress', listeners)
  return () => listeners.delete(handler as (payload: unknown) => void)
}

function arrayBufferToBase64(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000))
  }
  return btoa(binary)
}
