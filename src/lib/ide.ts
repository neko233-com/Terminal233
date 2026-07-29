import { invoke } from '@tauri-apps/api/core'
import { listen, type UnlistenFn } from '@tauri-apps/api/event'

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
    | { kind: 'agent' }
    | { kind: 'private_key'; privateKeyPem: string; passphrase: string | null }
    | { kind: 'private_key_file'; privateKeyPath: string }
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
export interface IdeTerminalOutput { sessionId: string; data: string }
export interface IdeTerminalClosed { sessionId: string; exitStatus: number | null }
export interface IdeTerminalError { sessionId: string; code: string }

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
  offset: number
  dataBase64: string
}
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

export function ideError(error: unknown): string {
  const candidate = error as IdeCommandErrorShape | null
  return candidate?.message ?? '操作失败，请检查连接参数和服务器状态。'
}

export function createIdeConnection(input: IdeConnectionInput): Promise<IdeConnection> {
  return invoke('ide_connection_create', { input })
}

export function listIdeConnections(): Promise<IdeConnection[]> {
  return invoke('ide_connection_list')
}

export function disconnectIdeConnection(connectionId: string): Promise<void> {
  return invoke('ide_connection_disconnect', { connectionId })
}

export function prepareIdeConnection(connectionId: string): Promise<IdeHostKeyStatus> {
  return invoke('ide_ssh_prepare', { connectionId })
}

export function trustIdeHostKey(connectionId: string, confirmationId: string): Promise<IdeHostKeyStatus> {
  return invoke('ide_ssh_trust_host_key', { connectionId, confirmationId })
}

export function rejectIdeHostKey(confirmationId: string): Promise<void> {
  return invoke('ide_ssh_reject_host_key', { confirmationId })
}

export function listIdeDirectory(connectionId: string, path: string): Promise<IdeDirectoryListing> {
  return invoke('ide_sftp_list', { connectionId, path })
}

export function createIdeDirectory(connectionId: string, path: string): Promise<void> {
  return invoke('ide_sftp_mkdir', { connectionId, path })
}

export function renameIdeRemote(connectionId: string, fromPath: string, toPath: string, overwrite = false): Promise<void> {
  return invoke('ide_sftp_rename', { connectionId, fromPath, toPath, overwrite })
}

export function deleteIdeRemote(connectionId: string, path: string): Promise<void> {
  return invoke('ide_sftp_delete', { connectionId, path })
}

export async function uploadIdeFiles(connectionId: string, remoteDirectory: string, files: File[]): Promise<IdeTransferStarted> {
  const inputs = await Promise.all(files.map(async (file) => ({
    name: file.name,
    dataBase64: arrayBufferToBase64(await file.arrayBuffer()),
    overwrite: false,
  })))
  return invoke('ide_sftp_upload_start', { connectionId, remoteDirectory, files: inputs })
}

export function downloadIdeFiles(connectionId: string, files: Array<{ remotePath: string; saveAs: string }>): Promise<IdeTransferStarted> {
  return invoke('ide_sftp_download_start', { connectionId, files })
}

export function cancelIdeTransfer(jobId: string): Promise<void> {
  return invoke('ide_sftp_transfer_cancel', { jobId })
}

export function startIdeTerminal(connectionId: string, columns = 110, rows = 34): Promise<IdeTerminalStarted> {
  return invoke('ide_terminal_start', { connectionId, columns, rows })
}

export function writeIdeTerminal(sessionId: string, data: string): Promise<void> {
  return invoke('ide_terminal_write', { sessionId, data })
}

export function resizeIdeTerminal(sessionId: string, columns: number, rows: number): Promise<void> {
  return invoke('ide_terminal_resize', { sessionId, columns, rows })
}

export function closeIdeTerminal(sessionId: string): Promise<void> {
  return invoke('ide_terminal_close', { sessionId })
}

export async function onIdeTerminalOutput(handler: (event: IdeTerminalOutput) => void): Promise<UnlistenFn> {
  return listen<IdeTerminalOutput>('ssh-terminal-output', (event) => handler(event.payload))
}

export async function onIdeTerminalClosed(handler: (event: IdeTerminalClosed) => void): Promise<UnlistenFn> {
  return listen<IdeTerminalClosed>('ssh-terminal-closed', (event) => handler(event.payload))
}

export async function onIdeTerminalError(handler: (event: IdeTerminalError) => void): Promise<UnlistenFn> {
  return listen<IdeTerminalError>('ssh-terminal-error', (event) => handler(event.payload))
}

export async function onIdeTransferProgress(handler: (event: IdeTransferProgress) => void): Promise<UnlistenFn> {
  return listen<IdeTransferProgress>('ssh-sftp-transfer-progress', (event) => handler(event.payload))
}

export async function onIdeTransferDownloadChunk(handler: (event: IdeTransferDownloadChunk) => void): Promise<UnlistenFn> {
  return listen<IdeTransferDownloadChunk>('ssh-sftp-download-chunk', (event) => handler(event.payload))
}

export async function onIdeTransferComplete(handler: (event: IdeTransferComplete) => void): Promise<UnlistenFn> {
  return listen<IdeTransferComplete>('ssh-sftp-transfer-complete', (event) => handler(event.payload))
}

function arrayBufferToBase64(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000))
  }
  return btoa(binary)
}
