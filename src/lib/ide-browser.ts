import {
  createConnection,
  createDirectory,
  commitDelete,
  disconnectConnection,
  downloadRemoteBytes,
  initializeBrowserSession,
  listConnections,
  listDirectory,
  onBrowserSessionInvalidated,
  pollTerminal,
  prepareDelete,
  prepareConnection,
  renameRemote,
  resizeTerminal,
  startTerminal,
  trustConnection,
  uploadFiles,
  writeTerminal,
  closeTerminal,
} from "./web";
import type {
  ConnectionSummary,
  DirectoryListing,
  HostKeyStatus,
  WebConnectionInput,
} from "./web";
import type {
  IdeConnection,
  IdeConnectionInput,
  IdeDirectoryEntry,
  IdeDirectoryListing,
  IdeHostKeyStatus,
  IdeTerminalEvent,
  IdeTerminalStarted,
  IdeTransferComplete,
  IdeTransferItem,
  IdeTransferProgress,
  IdeTransferStarted,
  IdeUploadEvent,
  IdeDownloadEvent,
} from "./ide";

export const IDE_RUNTIME_LABEL = "Node ssh2js";
export const IDE_TRANSPORT_LABEL = "本机 localhost API";
export const IDE_SECURITY_NOTE =
  "SSH 与 SFTP 通过本机 localhost API 使用 Node ssh2js；浏览器不直接接触凭据。";

const BROWSER_DOWNLOAD_CHUNK_BYTES = 32 * 1024;
const browserTerminalControllers = new Map<string, AbortController>();
let browserSessionInvalidationCleanup: (() => void) | null = null;

function ensureBrowserSession(): Promise<void> {
  if (!browserSessionInvalidationCleanup) {
    browserSessionInvalidationCleanup = onBrowserSessionInvalidated(() => {
      for (const controller of browserTerminalControllers.values())
        controller.abort();
      browserTerminalControllers.clear();
      browserSessionInvalidationCleanup = null;
    });
  }
  return initializeBrowserSession();
}

function mapConnection(connection: ConnectionSummary): IdeConnection {
  return { ...connection, tags: [] };
}

function mapDirectory(listing: DirectoryListing): IdeDirectoryListing {
  return {
    ...listing,
    entries: listing.entries.map((entry: IdeDirectoryEntry) => ({ ...entry })),
  };
}

function mapHostKey(status: HostKeyStatus): IdeHostKeyStatus {
  return status;
}

function randomJobId(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}

function toBase64(bytes: Uint8Array): string {
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  }
  return btoa(binary);
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, milliseconds));
}

export function ideError(error: unknown): string {
  if (typeof error === "string" && error.trim()) return error;
  if (error instanceof Error && error.message.trim()) return error.message;
  const candidate = error as { message?: string } | null;
  return typeof candidate?.message === "string" && candidate.message.trim()
    ? candidate.message
    : "操作失败，请检查连接参数和服务器状态。";
}

export function decodeBase64Bytes(value: string): Uint8Array {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1)
    bytes[index] = binary.charCodeAt(index);
  return bytes;
}

export function createIdeConnection(
  input: IdeConnectionInput,
): Promise<IdeConnection> {
  return ensureBrowserSession()
    .then(() => createConnection(input as WebConnectionInput))
    .then(mapConnection);
}

export function listIdeConnections(): Promise<IdeConnection[]> {
  return ensureBrowserSession()
    .then(() => listConnections())
    .then((connections) => connections.map(mapConnection));
}

export function disconnectIdeConnection(connectionId: string): Promise<void> {
  return ensureBrowserSession().then(() => disconnectConnection(connectionId));
}

export function prepareIdeConnection(
  connectionId: string,
): Promise<IdeHostKeyStatus> {
  return ensureBrowserSession()
    .then(() => prepareConnection(connectionId))
    .then(mapHostKey);
}

export function trustIdeHostKey(
  connectionId: string,
  confirmationId: string,
): Promise<IdeHostKeyStatus> {
  return ensureBrowserSession()
    .then(() => trustConnection(connectionId, confirmationId))
    .then(mapHostKey);
}

export function rejectIdeHostKey(_confirmationId: string): Promise<void> {
  return Promise.resolve();
}

export function listIdeDirectory(
  connectionId: string,
  path: string,
): Promise<IdeDirectoryListing> {
  return ensureBrowserSession()
    .then(() => listDirectory(connectionId, path))
    .then(mapDirectory);
}

export function createIdeDirectory(
  connectionId: string,
  path: string,
): Promise<void> {
  return ensureBrowserSession().then(() => createDirectory(connectionId, path));
}

export function renameIdeRemote(
  connectionId: string,
  fromPath: string,
  toPath: string,
  overwrite = false,
): Promise<void> {
  return ensureBrowserSession().then(() =>
    renameRemote(connectionId, fromPath, toPath, overwrite),
  );
}

export async function deleteIdeRemote(
  connectionId: string,
  path: string,
): Promise<void> {
  await ensureBrowserSession();
  const confirmation = await prepareDelete(connectionId, path);
  await commitDelete(connectionId, confirmation.confirmationId);
}

export async function uploadIdeFiles(
  connectionId: string,
  remoteDirectory: string,
  files: File[],
  onEvent: (event: IdeUploadEvent) => void,
): Promise<IdeTransferStarted> {
  await ensureBrowserSession();
  const jobId = randomJobId("browser-upload");
  const items: IdeTransferItem[] = files.map((file, index) => ({
    fileId: `${jobId}-${index}`,
    name: file.name,
    size: file.size,
  }));
  const totalBytes = files.reduce((total, file) => total + file.size, 0);
  const started = {
    kind: "started" as const,
    jobId,
    direction: "upload" as const,
    totalFiles: files.length,
    totalBytes,
    items,
  };
  onEvent(started);
  try {
    await uploadFiles(connectionId, remoteDirectory, files);
    const completed = {
      kind: "completed" as const,
      jobId,
      direction: "upload" as const,
      status: "completed" as const,
      completedFiles: files.length,
      failedFiles: 0,
      cancelledFiles: 0,
      bytesTransferred: totalBytes,
      totalBytes,
    } satisfies IdeTransferComplete & { kind: "completed" };
    onEvent(completed);
    return started;
  } catch (error) {
    const failed = {
      kind: "completed" as const,
      jobId,
      direction: "upload" as const,
      status: "completed_with_errors" as const,
      completedFiles: 0,
      failedFiles: files.length,
      cancelledFiles: 0,
      bytesTransferred: 0,
      totalBytes,
    } satisfies IdeTransferComplete & { kind: "completed" };
    onEvent(failed);
    throw error;
  }
}

export async function downloadIdeFiles(
  connectionId: string,
  files: Array<{ remotePath: string; saveAs: string }>,
  onEvent: (event: IdeDownloadEvent) => void,
): Promise<IdeTransferStarted> {
  await ensureBrowserSession();
  const jobId = randomJobId("browser-download");
  const fetched = await Promise.all(
    files.map(async (file, index) => ({
      file,
      item: {
        fileId: `${jobId}-${index}`,
        name: file.saveAs,
        size: null as number | null,
      },
      result: await downloadRemoteBytes(connectionId, file.remotePath),
    })),
  );
  const totalBytes = fetched.reduce(
    (total, item) => total + item.result.data.byteLength,
    0,
  );
  const items = fetched.map((item) => ({
    ...item.item,
    size: item.result.data.byteLength,
  }));
  const started = {
    kind: "started" as const,
    jobId,
    direction: "download" as const,
    totalFiles: fetched.length,
    totalBytes,
    items,
  };
  onEvent(started);
  let sequence = 1;
  let bytesTransferred = 0;
  for (const item of fetched) {
    for (
      let start = 0;
      start < item.result.data.length;
      start += BROWSER_DOWNLOAD_CHUNK_BYTES
    ) {
      const bytes = item.result.data.subarray(
        start,
        Math.min(item.result.data.length, start + BROWSER_DOWNLOAD_CHUNK_BYTES),
      );
      onEvent({
        kind: "chunk",
        jobId,
        fileId: item.item.fileId,
        name: item.file.saveAs,
        sequence,
        offset: start,
        dataBase64: toBase64(bytes),
      });
      sequence += 1;
    }
    bytesTransferred += item.result.data.byteLength;
  }
  onEvent({
    kind: "completed",
    jobId,
    direction: "download",
    status: "completed",
    completedFiles: fetched.length,
    failedFiles: 0,
    cancelledFiles: 0,
    bytesTransferred,
    totalBytes,
  });
  return started;
}

export function cancelIdeTransfer(_jobId: string): Promise<void> {
  return Promise.resolve();
}

export function acknowledgeIdeDownloadChunk(
  _jobId: string,
  _sequence: number,
): Promise<void> {
  return Promise.resolve();
}

export async function startIdeTerminal(
  connectionId: string,
  onEvent: (event: IdeTerminalEvent) => void,
  columns = 110,
  rows = 34,
): Promise<IdeTerminalStarted> {
  await ensureBrowserSession();
  const started = await startTerminal(connectionId, columns, rows);
  const controller = new AbortController();
  browserTerminalControllers.set(started.terminalId, controller);
  void pollBrowserTerminal(started.terminalId, onEvent, controller);
  return { sessionId: started.terminalId };
}

async function pollBrowserTerminal(
  sessionId: string,
  onEvent: (event: IdeTerminalEvent) => void,
  controller: AbortController,
): Promise<void> {
  let cursor = 0;
  let sequence = 1;
  try {
    while (!controller.signal.aborted) {
      const poll = await pollTerminal(sessionId, cursor);
      for (const chunk of poll.chunks) {
        if (controller.signal.aborted) return;
        onEvent({
          kind: "output",
          sessionId,
          sequence,
          dataBase64: chunk.dataBase64,
        });
        sequence += 1;
        cursor = chunk.cursor;
      }
      if (poll.error) {
        onEvent({
          kind: "error",
          sessionId,
          code: "browser_terminal_poll_failed",
        });
        return;
      }
      if (poll.closed) {
        onEvent({ kind: "closed", sessionId, exitStatus: poll.exitStatus });
        return;
      }
      await delay(poll.chunks.length ? 0 : 50);
    }
  } catch {
    if (!controller.signal.aborted)
      onEvent({
        kind: "error",
        sessionId,
        code: "browser_terminal_poll_failed",
      });
  } finally {
    browserTerminalControllers.delete(sessionId);
  }
}

export function writeIdeTerminal(
  sessionId: string,
  data: string,
): Promise<void> {
  return ensureBrowserSession().then(() => writeTerminal(sessionId, data));
}

export function resizeIdeTerminal(
  sessionId: string,
  columns: number,
  rows: number,
): Promise<void> {
  return ensureBrowserSession().then(() =>
    resizeTerminal(sessionId, columns, rows),
  );
}

export function acknowledgeIdeTerminalOutput(
  _sessionId: string,
  _sequence: number,
): Promise<void> {
  return Promise.resolve();
}

export async function closeIdeTerminal(sessionId: string): Promise<void> {
  browserTerminalControllers.get(sessionId)?.abort();
  browserTerminalControllers.delete(sessionId);
  await ensureBrowserSession();
  await closeTerminal(sessionId);
}

export function onIdeTransferProgress(
  _handler: (event: IdeTransferProgress) => void,
): Promise<() => void> {
  return Promise.resolve(() => undefined);
}

export { IdeTerminalInputQueue } from "./terminal-input";
export type {
  IdeConnection,
  IdeConnectionInput,
  IdeDirectoryEntry,
  IdeDirectoryListing,
  IdeHostKeyStatus,
  IdeTerminalEvent,
  IdeTerminalStarted,
  IdeTransferComplete,
  IdeTransferItem,
  IdeTransferProgress,
  IdeTransferStarted,
  IdeUploadEvent,
} from "./ide";
