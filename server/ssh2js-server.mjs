import { createHash, randomBytes, randomUUID, timingSafeEqual } from 'node:crypto'
import http from 'node:http'
import { posix as path } from 'node:path'
import ssh2 from 'ssh2'
import {
  cookieValue,
  openShellChannel,
  registerRevocableOperation,
  releaseMatches,
  retireBrowserSession,
  revokeOperations,
  touchConnectionActivity,
} from './session-lifecycle.mjs'
import { OperationAdmission, OperationAdmissionError } from './operation-admission.mjs'
import { TerminalAdmission, TerminalAdmissionError } from './terminal-admission.mjs'
import { createStaticWebHandler } from './static-web.mjs'
import { connectModernSshClient } from './ssh-algorithms.mjs'
import { attachmentContentDisposition } from './content-disposition.mjs'

const { Client } = ssh2
const HOST = '127.0.0.1'
const PORT = parsePort(process.env.NEKOSSH_BACKEND_PORT ?? '7331', 'NEKOSSH_BACKEND_PORT')
const FRONTEND_PORT = parsePort(process.env.NEKOSSH_FRONTEND_PORT ?? '5173', 'NEKOSSH_FRONTEND_PORT')
const SESSION_TTL_MS = 30 * 60_000
const GRANT_TTL_MS = 5 * 60_000
const ATTACHMENT_TTL_MS = 30 * 60_000
const CONFIRM_TTL_MS = 5 * 60_000
const DELETE_TTL_MS = 60_000
const MAX_BODY_BYTES = 64 * 1024 * 1024
const MAX_JSON_BYTES = 64 * 1024
const MAX_FILE_BYTES = 16 * 1024 * 1024
const MAX_UPLOAD_TOTAL_BYTES = 45 * 1024 * 1024
const MAX_UPLOAD_FILES = 32
const MAX_SSH_OPERATIONS = 8
const MAX_SSH_OPERATIONS_PER_CONNECTION = 3
const MAX_UPLOAD_BODIES = 2
const MAX_UPLOAD_BODIES_PER_CONNECTION = 1
const MAX_UPLOAD_BODY_INFLIGHT_BYTES = MAX_BODY_BYTES * MAX_UPLOAD_BODIES
const MAX_OUTBOUND_RESPONSES = 64
const MAX_OUTBOUND_BYTES = 64 * 1024 * 1024
const MAX_DIRECTORY_ENTRIES = 10_000
const MAX_DIRECTORY_METADATA_BYTES = 4 * 1024 * 1024
const MAX_REMOTE_NAME_BYTES = 4 * 1024
const MAX_TERMINAL_OUTPUT_BYTES = 1024 * 1024
const MAX_TERMINAL_OUTPUT_CHUNK_BYTES = 32 * 1024
const MAX_TERMINAL_OUTPUT_RECORDS = 512
const MAX_TERMINAL_INPUT_BYTES = 16 * 1024
const MAX_TERMINAL_PENDING_INPUT_BYTES = 64 * 1024
const TERMINAL_DRAIN_TIMEOUT_MS = 10_000
const TERMINAL_OPEN_TIMEOUT_MS = 30_000
const SFTP_OPERATION_TIMEOUT_MS = 15 * 60_000
const SFTP_CALLBACK_TIMEOUT_MS = 60_000
const SFTP_TRANSFER_TIMEOUT_MS = 15 * 60_000
const EXEC_CHANNEL_OPEN_TIMEOUT_MS = 30_000
const EXEC_OPERATION_TIMEOUT_MS = 60_000
const MAX_TERMINALS = 12
const MAX_TERMINAL_RECORDS = 24
const MAX_BROWSER_SESSIONS = 32
const MAX_CONNECTIONS = 16
const MAX_PENDING_HOST_KEYS = 16
const MAX_DELETE_CONFIRMATIONS = 64
const MAX_AGENT_GRANTS = 64
const MAX_ATTACHMENTS = 32
const SUPPORTED_MCP_VERSION = '2025-11-25'
const MCP_TOKEN = validToken(process.env.NEKOSSH_MCP_TOKEN)
  ? process.env.NEKOSSH_MCP_TOKEN
  : randomBytes(32).toString('base64url')
const serveStaticWeb = process.env.NEKOSSH_WEB_ROOT
  ? createStaticWebHandler(process.env.NEKOSSH_WEB_ROOT)
  : null

const browserSessions = new Map()
const connections = new Map()
const pendingHostKeys = new Map()
const terminals = new Map()
const deleteConfirmations = new Map()
const agentGrants = new Map()
const attachments = new Map()
const terminalAdmission = new TerminalAdmission(MAX_TERMINALS, MAX_TERMINAL_RECORDS)
const sshOperationAdmission = new OperationAdmission({
  maxActive: MAX_SSH_OPERATIONS,
  maxPerKey: MAX_SSH_OPERATIONS_PER_CONNECTION,
})
const uploadBodyAdmission = new OperationAdmission({
  maxActive: MAX_UPLOAD_BODIES,
  maxPerKey: MAX_UPLOAD_BODIES_PER_CONNECTION,
  maxReservedBytes: MAX_UPLOAD_BODY_INFLIGHT_BYTES,
})
const outboundResponseAdmission = new OperationAdmission({
  maxActive: MAX_OUTBOUND_RESPONSES,
  maxPerKey: MAX_OUTBOUND_RESPONSES,
  maxReservedBytes: MAX_OUTBOUND_BYTES,
})

const allowedOrigins = new Set([
  `http://${HOST}:${PORT}`,
  `http://localhost:${PORT}`,
  `http://127.0.0.1:${FRONTEND_PORT}`,
  `http://localhost:${FRONTEND_PORT}`,
])

const server = http.createServer((request, response) => {
  void route(request, response).catch((error) => {
    if (!response.headersSent) sendError(response, error)
    else response.destroy()
  })
})

server.requestTimeout = 10 * 60_000
server.headersTimeout = 10_000
server.keepAliveTimeout = 5_000
server.maxConnections = 64
server.listen(PORT, HOST, () => {
  console.log(`NekoSSH Web backend: http://${HOST}:${PORT}`)
  console.log('SSH engine: bundled Node.js ssh2 (ssh2js)')
  console.log(`MCP endpoint: http://${HOST}:${PORT}/mcp`)
  if (!process.env.NEKOSSH_MCP_TOKEN) console.log(`MCP bearer token: ${MCP_TOKEN}`)
})

const reaper = setInterval(purgeExpired, 30_000)
reaper.unref()

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => {
    clearInterval(reaper)
    for (const terminal of terminals.values()) destroyTerminal(terminal)
    for (const connection of connections.values()) zeroConnection(connection)
    server.close(() => process.exit(0))
  })
}

async function route(request, response) {
  const url = new URL(request.url ?? '/', `http://${HOST}:${PORT}`)
  if (request.method === 'GET' && url.pathname === '/healthz') return sendText(response, 200, 'ok')
  if (request.method === 'GET' && url.pathname === '/api/health') {
    return sendJson(response, 200, { status: 'ok', sshEngine: 'ssh2js', runtime: 'node', persistence: 'memory_only' })
  }
  if (url.pathname === '/mcp') return routeMcp(request, response)
  if (serveStaticWeb && await serveStaticWeb(request, response, url)) return
  if (!url.pathname.startsWith('/api/')) throw apiError(404, 'not_found', '接口不存在。')
  requireBrowserOrigin(request)

  if (request.method === 'POST' && url.pathname === '/api/browser-session/close') {
    const token = browserSessionToken(request)
    const session = browserSessions.get(token)
    const input = await readJson(request)
    if (releaseMatches(session, input.releaseId)) {
      retireBrowserSession(token, browserSessions, disconnectOwner)
    }
    return sendEmpty(response)
  }

  if (request.method === 'POST' && url.pathname === '/api/browser-session') {
    retireBrowserSession(browserSessionToken(request), browserSessions, disconnectOwner)
    purgeExpired()
    enforceCapacity(browserSessions, MAX_BROWSER_SESSIONS, 'browser_session_limit', '本地浏览器会话数量已达到上限。')
    const owner = randomUUID()
    const token = randomBytes(32).toString('base64url')
    const releaseId = randomBytes(32).toString('base64url')
    browserSessions.set(token, { owner, releaseId, expiresAt: Date.now() + SESSION_TTL_MS })
    response.setHeader('set-cookie', `nekossh_session=${token}; HttpOnly; SameSite=Strict; Path=/api`)
    return sendJson(response, 200, { ok: true, releaseId })
  }

  const owner = authenticateBrowser(request)
  const segments = url.pathname.split('/').filter(Boolean)

  if (request.method === 'GET' && url.pathname === '/api/connections') {
    const summaries = []
    for (const connection of [...connections.values()]) {
      if (connection.owner !== owner) continue
      if (connection.expiresAt <= Date.now()) {
        disconnectConnection(connection)
        continue
      }
      touchConnection(connection)
      summaries.push(connectionSummary(connection))
    }
    return sendJson(response, 200, summaries)
  }
  if (request.method === 'POST' && url.pathname === '/api/connections') {
    const input = await readJson(request)
    purgeExpired()
    const connection = createConnection(owner, input)
    const retainedConnections = [...connections.values()].filter((item) => item.owner !== owner).length
    if (retainedConnections >= MAX_CONNECTIONS) {
      zeroConnection(connection)
      throw apiError(429, 'connection_limit', '本地 SSH 连接数量已达到上限。')
    }
    disconnectOwner(owner)
    connections.set(connection.id, connection)
    return sendJson(response, 201, connectionSummary(connection))
  }

  if (segments[0] === 'api' && segments[1] === 'connections' && segments[2]) {
    const connection = requireConnection(owner, segments[2])
    const suffix = segments.slice(3).join('/')
    if (request.method === 'DELETE' && suffix === '') {
      disconnectConnection(connection)
      return sendEmpty(response)
    }
    if (request.method === 'POST' && suffix === 'prepare') {
      return sendJson(response, 200, await prepareConnection(connection))
    }
    if (request.method === 'POST' && suffix === 'trust') {
      const input = await readJson(request)
      return sendJson(response, 200, await trustHostKey(connection, input.confirmationId))
    }
    if (request.method === 'POST' && suffix === 'agent-grant') {
      for (const [id, pending] of agentGrants) if (pending.connectionId === connection.id) agentGrants.delete(id)
      purgeExpired()
      enforceCapacity(agentGrants, MAX_AGENT_GRANTS, 'agent_grant_limit', 'Agent 授权数量已达到上限。')
      const grant = randomUUID()
      agentGrants.set(grant, {
        connectionId: connection.id,
        owner,
        expiresAt: Date.now() + GRANT_TTL_MS,
        scopes: new Set(['terminal', 'files_read', 'files_write']),
      })
      return sendJson(response, 200, { grant, expiresInSeconds: GRANT_TTL_MS / 1000 })
    }
    if (request.method === 'POST' && suffix === 'terminal') {
      const input = await readJson(request)
      const terminal = await withRequestCancellation(request, response, (signal) => (
        startTerminal(connection, owner, input.columns, input.rows, signal)
      ))
      return sendJson(response, 201, terminal)
    }
    if (request.method === 'GET' && suffix === 'files') {
      const listing = await withRequestCancellation(request, response, (signal) => (
        listDirectory(connection, url.searchParams.get('path') ?? '/', signal)
      ))
      return sendJson(response, 200, listing)
    }
    if (request.method === 'POST' && suffix === 'files/mkdir') {
      const input = await readJson(request)
      await withRequestCancellation(request, response, (signal) => (
        withSftp(
          connection,
          (sftp) => sftpCall(sftp, 'mkdir', validateRemotePath(input.path), { mode: 0o755 }),
          signal,
        )
      ))
      return sendEmpty(response)
    }
    if (request.method === 'POST' && suffix === 'files/rename') {
      const input = await readJson(request)
      await withRequestCancellation(request, response, (signal) => (
        renameRemote(connection, input.fromPath, input.toPath, input.overwrite === true, signal)
      ))
      return sendEmpty(response)
    }
    if (request.method === 'POST' && suffix === 'files/upload') {
      validateDeclaredBodyLength(request, MAX_BODY_BYTES)
      const releaseBody = reserveUploadBody(connection)
      try {
        const input = await readJson(request, MAX_BODY_BYTES)
        const result = await withRequestCancellation(request, response, (signal) => (
          uploadFiles(connection, input, signal)
        ))
        return sendJson(response, 200, result)
      } finally {
        releaseBody()
      }
    }
    if (request.method === 'GET' && suffix === 'files/download') {
      const remotePath = validateRemotePath(url.searchParams.get('path') ?? '')
      const downloadName = validateFileName(path.basename(remotePath))
      const contentDisposition = attachmentContentDisposition(downloadName)
      const data = await withRequestCancellation(request, response, (signal) => (
        downloadFile(connection, remotePath, signal)
      ))
      return sendBoundedBody(response, 200, data, {
        'content-type': 'application/octet-stream',
        'content-disposition': contentDisposition,
      })
    }
    if (request.method === 'POST' && suffix === 'files/delete-prepare') {
      const input = await readJson(request)
      for (const [id, pending] of deleteConfirmations) if (pending.connectionId === connection.id) deleteConfirmations.delete(id)
      purgeExpired()
      enforceCapacity(deleteConfirmations, MAX_DELETE_CONFIRMATIONS, 'delete_confirmation_limit', '待确认删除操作数量已达到上限。')
      const confirmationId = randomUUID()
      deleteConfirmations.set(confirmationId, {
        owner,
        connectionId: connection.id,
        remotePath: validateDeletablePath(input.path),
        expiresAt: Date.now() + DELETE_TTL_MS,
      })
      return sendJson(response, 200, { confirmationId, expiresInSeconds: DELETE_TTL_MS / 1000 })
    }
    if (request.method === 'POST' && suffix === 'files/delete-commit') {
      const input = await readJson(request)
      await withRequestCancellation(request, response, (signal) => (
        commitDelete(owner, connection, input.confirmationId, signal)
      ))
      return sendEmpty(response)
    }
  }

  if (segments[0] === 'api' && segments[1] === 'terminals' && segments[2]) {
    const terminal = requireTerminal(owner, segments[2])
    const suffix = segments.slice(3).join('/')
    if (request.method === 'POST' && suffix === 'input') {
      const input = await readJson(request)
      const data = requireTerminalInput(input.data)
      if (!terminal.stream || terminal.closed) throw apiError(404, 'terminal_closed', '终端已关闭。')
      touchTerminalActivity(owner, terminal)
      await queueTerminalInput(terminal, data)
      return sendEmpty(response)
    }
    if (request.method === 'POST' && suffix === 'resize') {
      const input = await readJson(request)
      const columns = clampInteger(input.columns, 20, 500, 110)
      const rows = clampInteger(input.rows, 5, 300, 34)
      if (!terminal.stream || terminal.closed) throw apiError(404, 'terminal_closed', '终端已关闭。')
      touchTerminalActivity(owner, terminal)
      terminal.stream.setWindow(rows, columns, 0, 0)
      return sendEmpty(response)
    }
    if (request.method === 'GET' && suffix === 'poll') {
      const cursor = clampInteger(url.searchParams.get('cursor'), 0, Number.MAX_SAFE_INTEGER, 0)
      if (!terminal.closed) touchTerminalActivity(owner, terminal)
      const firstAvailableCursor = terminal.chunks[0]?.cursor ?? terminal.cursor + 1
      return sendJson(response, 200, {
        cursor: terminal.cursor,
        chunks: terminal.chunks.filter((chunk) => chunk.cursor > cursor),
        truncated: cursor < firstAvailableCursor - 1,
        closed: terminal.closed,
        exitStatus: terminal.exitStatus,
        error: terminal.error,
      })
    }
    if (request.method === 'DELETE' && suffix === '') {
      destroyTerminal(terminal)
      terminals.delete(terminal.id)
      return sendEmpty(response)
    }
  }

  throw apiError(404, 'not_found', '接口不存在。')
}

function createConnection(owner, input) {
  const label = requireString(input.label, '连接名称不能为空。', 128)
  const host = requireString(input.host, '主机不能为空。', 253)
  const port = requireInteger(input.port, 1, 65535, 'SSH 端口必须是 1 到 65535 的整数。')
  const username = requireString(input.username, '用户名不能为空。', 128)
  if (!/^[A-Za-z0-9._:-]+$/.test(host) || host.includes('..')) throw apiError(400, 'invalid_host', '主机格式无效。')
  const authenticationKind = input.authentication?.kind
  let password = Buffer.alloc(0)
  let privateKey = Buffer.alloc(0)
  let passphrase = Buffer.alloc(0)
  if (authenticationKind === 'password') {
    password = Buffer.from(
      requireString(input.authentication.password, '密码不能为空。', 16 * 1024),
      'utf8',
    )
  } else if (authenticationKind === 'private_key') {
    privateKey = Buffer.from(
      requireString(input.authentication.privateKeyPem, '私钥不能为空。', 64 * 1024),
      'utf8',
    )
    const rawPassphrase = input.authentication.passphrase
    if (
      rawPassphrase !== null
      && rawPassphrase !== undefined
      && (typeof rawPassphrase !== 'string' || Buffer.byteLength(rawPassphrase) > 16 * 1024)
    ) {
      privateKey.fill(0)
      throw apiError(400, 'invalid_input', '私钥口令无效。')
    }
    if (rawPassphrase) passphrase = Buffer.from(rawPassphrase, 'utf8')
  } else {
    throw apiError(400, 'unsupported_authentication', 'Web macOS 支持密码或内存私钥认证。')
  }
  return {
    id: randomUUID(),
    owner,
    label,
    host,
    port,
    username,
    authenticationKind,
    password,
    privateKey,
    passphrase,
    trustedFingerprint: null,
    trustedAlgorithm: null,
    client: null,
    openingClient: null,
    preparing: null,
    authenticating: null,
    pendingConfirmationId: null,
    clients: new Set(),
    closed: false,
    generation: 0,
    expiresAt: Date.now() + SESSION_TTL_MS,
  }
}

function connectionSummary(connection) {
  return {
    id: connection.id,
    label: connection.label,
    host: connection.host,
    port: connection.port,
    username: connection.username,
    expiresInSeconds: Math.max(0, Math.floor((connection.expiresAt - Date.now()) / 1000)),
  }
}

async function prepareConnection(connection) {
  const generation = requireLiveConnection(connection)
  touchConnection(connection)
  if (connection.client) {
    return { status: 'trusted', host: connection.host, port: connection.port, keyAlgorithm: connection.trustedAlgorithm, fingerprint: connection.trustedFingerprint }
  }
  const pending = connection.pendingConfirmationId ? pendingHostKeys.get(connection.pendingConfirmationId) : null
  if (pending && pending.expiresAt > Date.now()) {
    return {
      status: 'confirmation_required',
      confirmationId: connection.pendingConfirmationId,
      host: connection.host,
      port: connection.port,
      keyAlgorithm: pending.keyAlgorithm,
      fingerprint: pending.fingerprint,
    }
  }
  if (connection.pendingConfirmationId) clearPendingHostKeys(connection.id)
  if (connection.authenticating) {
    await connection.authenticating
    requireLiveConnection(connection, generation)
    if (connection.client) {
      return { status: 'trusted', host: connection.host, port: connection.port, keyAlgorithm: connection.trustedAlgorithm, fingerprint: connection.trustedFingerprint }
    }
  }
  if (connection.preparing) return connection.preparing
  const preparing = prepareConnectionOnce(connection, generation)
  connection.preparing = preparing
  try {
    return await preparing
  } finally {
    if (connection.preparing === preparing) connection.preparing = null
  }
}

async function prepareConnectionOnce(connection, generation) {
  const presented = await holdHostKey(connection)
  if (!isConnectionLive(connection, generation)) {
    disposePresentedHostKey(presented)
    throw apiError(410, 'connection_closed', 'SSH 连接已关闭。')
  }
  if (connection.client) {
    disposePresentedHostKey(presented)
    return { status: 'trusted', host: connection.host, port: connection.port, keyAlgorithm: connection.trustedAlgorithm, fingerprint: connection.trustedFingerprint }
  }
  if (!connection.trustedFingerprint) {
    clearPendingHostKeys(connection.id)
    purgeExpired()
    enforceCapacity(pendingHostKeys, MAX_PENDING_HOST_KEYS, 'host_key_confirmation_limit', '待确认主机密钥数量已达到上限。')
    const confirmationId = randomUUID()
    pendingHostKeys.set(confirmationId, {
      connectionId: connection.id,
      fingerprint: presented.fingerprint,
      keyAlgorithm: presented.keyAlgorithm,
      publicKey: presented.publicKey,
      expiresAt: Date.now() + CONFIRM_TTL_MS,
    })
    connection.pendingConfirmationId = confirmationId
    return { status: 'confirmation_required', confirmationId, host: connection.host, port: connection.port, keyAlgorithm: presented.keyAlgorithm, fingerprint: presented.fingerprint }
  }
  if (!safeEqual(connection.trustedFingerprint, presented.fingerprint)) {
    disposePresentedHostKey(presented)
    return { status: 'mismatch', host: connection.host, port: connection.port, keyAlgorithm: presented.keyAlgorithm, fingerprint: presented.fingerprint, expectedFingerprints: [connection.trustedFingerprint] }
  }
  disposePresentedHostKey(presented)
  await openClient(connection)
  requireLiveConnection(connection, generation)
  return { status: 'trusted', host: connection.host, port: connection.port, keyAlgorithm: presented.keyAlgorithm, fingerprint: presented.fingerprint }
}

async function trustHostKey(connection, confirmationId) {
  const generation = requireLiveConnection(connection)
  touchConnection(connection)
  const pending = pendingHostKeys.get(confirmationId)
  if (!pending || pending.expiresAt <= Date.now() || pending.connectionId !== connection.id) {
    if (pending) disposePresentedHostKey(pending)
    pendingHostKeys.delete(confirmationId)
    if (connection.pendingConfirmationId === confirmationId) connection.pendingConfirmationId = null
    throw apiError(410, 'host_key_confirmation_expired', '主机密钥确认已过期。')
  }
  pendingHostKeys.delete(confirmationId)
  if (connection.pendingConfirmationId === confirmationId) connection.pendingConfirmationId = null
  clearPendingHostKeys(connection.id)
  if (connection.authenticating) {
    disposePresentedHostKey(pending)
    throw apiError(409, 'host_key_confirmation_in_progress', '主机密钥确认正在进行。')
  }
  const authenticating = acceptTrustedHostKey(connection, generation, pending)
  connection.authenticating = authenticating
  try {
    return await authenticating
  } finally {
    if (connection.authenticating === authenticating) connection.authenticating = null
  }
}

async function acceptTrustedHostKey(connection, generation, pending) {
  disposePresentedHostKey(pending)
  connection.trustedFingerprint = pending.fingerprint
  connection.trustedAlgorithm = pending.keyAlgorithm
  try {
    await openClient(connection)
    requireLiveConnection(connection, generation)
    return { status: 'trusted', host: connection.host, port: connection.port, keyAlgorithm: pending.keyAlgorithm, fingerprint: pending.fingerprint }
  } catch (error) {
    connection.trustedFingerprint = null
    connection.trustedAlgorithm = null
    throw error
  }
}

async function holdHostKey(connection) {
  const generation = requireLiveConnection(connection)
  let lastError
  for (let attempt = 0; attempt < 3; attempt += 1) {
    try {
      const handshake = await holdHostKeyOnce(connection, generation)
      requireLiveConnection(connection, generation)
      return handshake
    } catch (error) {
      lastError = error
      if (!isConnectionLive(connection, generation)) throw apiError(410, 'connection_closed', 'SSH 连接已关闭。')
      if (attempt < 2) await delay(750 * (attempt + 1))
    }
  }
  throw apiError(502, 'ssh_connection_failed', lastError?.level === 'protocol' ? 'SSH 握手被远端提前关闭。' : '无法读取服务器主机密钥。')
}

function holdHostKeyOnce(connection, generation) {
  return new Promise((resolve, reject) => {
    if (!isConnectionLive(connection, generation)) return reject(apiError(410, 'connection_closed', 'SSH 连接已关闭。'))
    const client = new Client()
    trackConnectionClient(connection, client)
    let settled = false
    const finish = (error) => {
      if (settled) return
      settled = true
      client.end()
      reject(error ?? apiError(502, 'ssh_connection_failed', '无法读取服务器主机密钥。'))
    }
    client.once('error', (error) => finish(error))
    client.once('close', () => finish())
    try {
      connectModernSshClient(client, {
        host: connection.host,
        port: connection.port,
        username: connection.username,
        readyTimeout: 10_000,
        keepaliveInterval: 0,
        authHandler() { return false },
        hostVerifier(publicKey) {
          if (settled) return false
          if (!isConnectionLive(connection, generation)) {
            settled = true
            client.end()
            reject(apiError(410, 'connection_closed', 'SSH 连接已关闭。'))
            return false
          }
          settled = true
          const key = Buffer.from(publicKey)
          resolve({
            publicKey: key,
            fingerprint: fingerprint(key),
            keyAlgorithm: keyAlgorithm(key),
          })
          setImmediate(() => {
            try { client.end() } catch {}
          })
          return false
        },
      })
    } catch (error) {
      finish(error)
    }
  })
}

function disposePresentedHostKey(presented) {
  presented?.publicKey?.fill(0)
}

async function openClient(connection) {
  const generation = requireLiveConnection(connection)
  if (connection.client) return connection.client
  if (connection.openingClient) return connection.openingClient
  const opening = openClientWithRetry(connection, generation)
  connection.openingClient = opening
  try {
    return await opening
  } finally {
    if (connection.openingClient === opening) connection.openingClient = null
  }
}

async function openClientWithRetry(connection, generation) {
  let lastError
  for (let attempt = 0; attempt < 2; attempt += 1) {
    try {
      const client = await openClientOnce(connection, generation)
      if (!isConnectionLive(connection, generation)) {
        client.end()
        throw apiError(410, 'connection_closed', 'SSH 连接已关闭。')
      }
      connection.client = client
      watchConnectionClient(connection, client)
      return client
    } catch (error) {
      lastError = error
      if (!isConnectionLive(connection, generation)) throw apiError(410, 'connection_closed', 'SSH 连接已关闭。')
      if (attempt === 0 && isRetryableSshError(error)) await delay(300)
      else break
    }
  }
  throw lastError
}

function watchConnectionClient(connection, client) {
  trackConnectionClient(connection, client)
  client.on('error', () => {
    if (connection.client === client) connection.client = null
  })
  client.once('close', () => {
    if (connection.client === client) connection.client = null
  })
}

function openClientOnce(connection, generation) {
  return new Promise((resolve, reject) => {
    if (!isConnectionLive(connection, generation)) return reject(apiError(410, 'connection_closed', 'SSH 连接已关闭。'))
    if (!connection.trustedFingerprint) return reject(apiError(409, 'host_key_untrusted', '请先确认服务器主机密钥。'))
    const client = new Client()
    trackConnectionClient(connection, client)
    let settled = false
    let authenticationAttempted = false
    const timer = setTimeout(() => finish(apiError(504, 'ssh_timeout', 'SSH 连接超时。')), 20_000)
    const finish = (error) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      if (!isConnectionLive(connection, generation)) {
        client.end()
        reject(apiError(410, 'connection_closed', 'SSH 连接已关闭。'))
      } else if (error) {
        client.end()
        reject(classifySshError(error, authenticationAttempted))
      } else resolve(client)
    }
    client.once('ready', () => finish())
    client.once('error', finish)
    try {
      connectModernSshClient(client, {
        host: connection.host,
        port: connection.port,
        username: connection.username,
        authHandler(methodsLeft) {
          if (authenticationAttempted || !isConnectionLive(connection, generation)) return false
          const method = connection.authenticationKind === 'private_key' ? 'publickey' : 'password'
          if (methodsLeft !== null && !methodsLeft.includes(method)) return false
          authenticationAttempted = true
          if (method === 'publickey') {
            return {
              type: 'publickey',
              username: connection.username,
              key: connection.privateKey,
              passphrase: connection.passphrase.length ? connection.passphrase : undefined,
            }
          }
          return { type: 'password', username: connection.username, password: connection.password }
        },
        readyTimeout: 10_000,
        keepaliveInterval: 10_000,
        keepaliveCountMax: 3,
        hostVerifier(publicKey) {
          return safeEqual(connection.trustedFingerprint, fingerprint(Buffer.from(publicKey)))
        },
      })
    } catch (error) {
      finish(error)
    }
  })
}

async function startTerminal(connection, owner, rawColumns, rawRows, signal) {
  purgeExpiredTerminals(Date.now())
  let releaseReservation
  try {
    releaseReservation = terminalAdmission.acquire(
      [...terminals.values()].filter((item) => !item.closed).length,
      terminals.size,
    )
  } catch (error) {
    if (error instanceof TerminalAdmissionError && error.code === 'terminal_limit') throw apiError(429, error.code, '终端数量已达到上限。')
    if (error instanceof TerminalAdmissionError && error.code === 'terminal_record_limit') throw apiError(429, error.code, '终端记录过多，请稍后重试。')
    throw error
  }
  let releaseOperation
  let deadline
  let client
  let admissionTail
  try {
    releaseOperation = reserveSshOperation(connection)
    deadline = createOperationDeadline(
      signal,
      TERMINAL_OPEN_TIMEOUT_MS,
      apiError(504, 'terminal_open_timeout', '打开终端通道超时，SSH 连接已重置。'),
    )
    const operationSignal = deadline.signal
    if (operationSignal.aborted) throw abortError(operationSignal)
    const generation = requireLiveConnection(connection)
    const columns = clampInteger(rawColumns, 20, 500, 110)
    const rows = clampInteger(rawRows, 5, 300, 34)
    const clientOpening = openClient(connection)
    admissionTail = clientOpening
    client = await raceWithAbort(
      clientOpening,
      operationSignal,
      () => {
        if (deadline.timedOut) resetConnectionTransport(connection)
      },
    )
    admissionTail = null
    let stream
    try {
      const opening = raceWithTimeout(
        openShellChannel(
          client,
          { term: 'xterm-256color', cols: columns, rows },
          () => isConnectionLive(connection, generation),
          () => apiError(410, 'connection_closed', 'SSH 连接已关闭。'),
        ).then((openedStream) => {
          if (!operationSignal.aborted) return openedStream
          try { openedStream.end() } catch {}
          throw abortError(operationSignal)
        }),
        TERMINAL_OPEN_TIMEOUT_MS,
        apiError(504, 'terminal_open_timeout', '打开终端通道超时，SSH 连接已重置。'),
        () => resetConnectionTransport(connection, client),
      )
      admissionTail = opening
      stream = await raceWithAbort(
        opening,
        operationSignal,
        () => {
          if (deadline.timedOut) resetConnectionTransport(connection, client)
        },
      )
      admissionTail = null
    } catch (error) {
      if (error?.publicMessage) throw error
      throw apiError(502, 'terminal_open_failed', '无法打开新的终端通道。')
    }
    if (!isConnectionLive(connection, generation)) {
      try { stream.end() } catch {}
      throw apiError(410, 'connection_closed', 'SSH 连接已关闭。')
    }
    const terminal = {
      id: randomUUID(),
      owner,
      connectionId: connection.id,
      client,
      stream,
      chunks: [],
      outputBytes: 0,
      cursor: 0,
      pendingInputBytes: 0,
      inputTail: Promise.resolve(),
      closed: false,
      exitStatus: null,
      error: null,
      expiresAt: Date.now() + SESSION_TTL_MS,
    }
    terminals.set(terminal.id, terminal)
    stream.on('data', (data) => appendTerminal(terminal, Buffer.from(data)))
    stream.on('close', (code) => closeTerminalRecord(terminal, Number.isInteger(code) ? code : null, null))
    stream.on('error', () => closeTerminalRecord(terminal, null, '终端通道错误。'))
    return { terminalId: terminal.id }
  } finally {
    if (deadline?.timedOut) resetConnectionTransport(connection, client)
    deadline?.dispose()
    const releaseAdmissions = () => {
      releaseOperation?.()
      releaseReservation()
    }
    if (admissionTail) {
      const cleanup = Promise.resolve(admissionTail).then(() => undefined, () => undefined)
      void cleanup.then(releaseAdmissions)
    } else {
      releaseAdmissions()
    }
  }
}

function appendTerminal(terminal, data) {
  if (terminal.closed || !data.length) return
  for (let offset = 0; offset < data.length; offset += MAX_TERMINAL_OUTPUT_CHUNK_BYTES) {
    const frame = data.subarray(offset, offset + MAX_TERMINAL_OUTPUT_CHUNK_BYTES)
    terminal.cursor += 1
    terminal.chunks.push({
      cursor: terminal.cursor,
      dataBase64: frame.toString('base64'),
      bytes: frame.length,
    })
    terminal.outputBytes += frame.length
  }
  while (
    terminal.chunks.length > 1
    && (terminal.outputBytes > MAX_TERMINAL_OUTPUT_BYTES || terminal.chunks.length > MAX_TERMINAL_OUTPUT_RECORDS)
  ) {
    const removed = terminal.chunks.shift()
    terminal.outputBytes -= removed.bytes
  }
}

async function queueTerminalInput(terminal, data) {
  const bytes = Buffer.byteLength(data)
  if (terminal.pendingInputBytes + bytes > MAX_TERMINAL_PENDING_INPUT_BYTES) {
    throw apiError(429, 'terminal_input_backpressure', '终端输入过快，请稍候再试。')
  }
  terminal.pendingInputBytes += bytes
  const operation = terminal.inputTail.then(() => writeTerminalWithBackpressure(terminal, data))
  terminal.inputTail = operation.catch(() => undefined)
  try {
    await operation
  } finally {
    terminal.pendingInputBytes = Math.max(0, terminal.pendingInputBytes - bytes)
  }
}

function writeTerminalWithBackpressure(terminal, data) {
  if (!terminal.stream || terminal.closed) {
    return Promise.reject(apiError(404, 'terminal_closed', '终端已关闭。'))
  }
  const stream = terminal.stream
  try {
    if (stream.write(data)) return Promise.resolve()
  } catch {
    try { stream.destroy() } catch {}
    closeTerminalRecord(terminal, null, '终端通道错误。')
    return Promise.reject(apiError(502, 'terminal_write_failed', '无法写入终端，通道已关闭。'))
  }
  return new Promise((resolve, reject) => {
    let settled = false
    const timeout = setTimeout(
      () => failTerminal(
        apiError(504, 'terminal_input_timeout', '终端输入等待服务器响应超时，通道已关闭。'),
        '终端输入阻塞，通道已关闭。',
      ),
      TERMINAL_DRAIN_TIMEOUT_MS,
    )
    const finish = (error) => {
      if (settled) return
      settled = true
      clearTimeout(timeout)
      stream.off('drain', onDrain)
      stream.off('close', onClose)
      stream.off('error', onError)
      if (error) reject(error)
      else resolve()
    }
    const failTerminal = (error, publicMessage) => {
      if (settled) return
      try { stream.destroy() } catch {}
      closeTerminalRecord(terminal, null, publicMessage)
      finish(error)
    }
    const onDrain = () => finish()
    const onClose = () => {
      closeTerminalRecord(terminal, terminal.exitStatus, terminal.error)
      finish(apiError(404, 'terminal_closed', '终端已关闭。'))
    }
    const onError = () => failTerminal(
      apiError(502, 'terminal_write_failed', '无法写入终端，通道已关闭。'),
      '终端通道错误。',
    )
    stream.once('drain', onDrain)
    stream.once('close', onClose)
    stream.once('error', onError)
  })
}

function classifySshError(error, authenticationAttempted) {
  if (error?.status && error?.code && error?.publicMessage) return error
  const code = String(error?.code ?? '').toUpperCase()
  const level = String(error?.level ?? '').toLowerCase()
  if (level.includes('authentication') || (authenticationAttempted && level === 'client-authentication')) {
    return apiError(401, 'ssh_authentication_failed', 'SSH 认证失败，请检查用户名、认证材料和服务器认证策略。')
  }
  if (code === 'ETIMEDOUT' || level.includes('timeout')) {
    return apiError(504, 'ssh_timeout', 'SSH 连接超时，请检查地址、端口和网络。')
  }
  if (code === 'ENOTFOUND' || code === 'EAI_AGAIN') {
    return apiError(502, 'ssh_dns_failed', '无法解析 SSH 主机地址。')
  }
  if (code === 'ECONNREFUSED') {
    return apiError(502, 'ssh_connection_refused', 'SSH 端口拒绝连接。')
  }
  if (level.includes('handshake') || level === 'protocol') {
    return apiError(502, 'ssh_handshake_failed', 'SSH 握手失败，服务器未返回可用响应。')
  }
  return apiError(502, 'ssh_connection_failed', '无法建立 SSH 连接。')
}

function isRetryableSshError(error) {
  return ![
    'ssh_authentication_failed',
    'host_key_untrusted',
    'connection_closed',
  ].includes(error?.code)
}

function closeTerminalRecord(terminal, exitStatus, error) {
  if (terminal.closed) return
  terminal.closed = true
  terminal.exitStatus = exitStatus
  terminal.error = error
  terminal.expiresAt = Date.now() + 30_000
  try { terminal.stream?.end() } catch {}
}

function destroyTerminal(terminal) {
  closeTerminalRecord(terminal, terminal.exitStatus, terminal.error)
  terminal.chunks.length = 0
  terminal.outputBytes = 0
  terminal.stream = null
  terminal.client = null
}

async function withRequestCancellation(request, response, operation) {
  const controller = new AbortController()
  const abort = () => {
    if (!controller.signal.aborted) {
      controller.abort(apiError(499, 'request_cancelled', '浏览器请求已取消。'))
    }
  }
  request.once('aborted', abort)
  response.once('close', abort)
  if (request.aborted || response.destroyed) abort()
  try {
    return await operation(controller.signal)
  } finally {
    request.off('aborted', abort)
    response.off('close', abort)
  }
}

function reserveSshOperation(connection) {
  try {
    return sshOperationAdmission.acquire(connection.id)
  } catch (error) {
    if (error instanceof OperationAdmissionError && error.code === 'operation_connection_limit') {
      throw apiError(429, 'connection_operation_limit', '此 SSH 连接正在处理过多操作，请稍后重试。')
    }
    if (error instanceof OperationAdmissionError && error.code === 'operation_global_limit') {
      throw apiError(429, 'ssh_operation_limit', '本地 SSH 操作已达到并发上限，请稍后重试。')
    }
    throw error
  }
}

function reserveUploadBody(connection) {
  try {
    return uploadBodyAdmission.acquire(connection.id, MAX_BODY_BYTES)
  } catch (error) {
    if (error instanceof OperationAdmissionError && error.code === 'operation_connection_limit') {
      throw apiError(429, 'connection_upload_limit', '此 SSH 连接已有上传正在进行，请稍后重试。')
    }
    if (error instanceof OperationAdmissionError
      && (error.code === 'operation_global_limit' || error.code === 'operation_memory_limit')) {
      throw apiError(429, 'upload_body_limit', '本地上传正在占用可用内存，请稍后重试。')
    }
    throw error
  }
}

async function withSftp(connection, operation, signal, waitForCancellationCleanup = false) {
  const releaseOperation = reserveSshOperation(connection)
  const deadline = createOperationDeadline(
    signal,
    SFTP_OPERATION_TIMEOUT_MS,
    apiError(504, 'sftp_timeout', 'SFTP 操作超时，SSH 连接已重置。'),
  )
  const operationSignal = deadline.signal
  let client
  let sftp
  let admissionTail
  try {
    if (operationSignal.aborted) throw abortError(operationSignal)
    const generation = requireLiveConnection(connection)
    const clientOpening = openClient(connection)
    admissionTail = clientOpening
    client = await raceWithAbort(
      clientOpening,
      operationSignal,
      () => {
        if (deadline.timedOut) resetConnectionTransport(connection)
      },
    )
    admissionTail = null
    if (operationSignal.aborted) throw abortError(operationSignal)
    const sftpOpening = raceWithTimeout(
      new Promise((resolve, reject) => {
        client.sftp((error, value) => {
          if (operationSignal.aborted) {
            try { value?.end() } catch {}
            reject(abortError(operationSignal))
          } else if (!isConnectionLive(connection, generation)) {
            try { value?.end() } catch {}
            reject(apiError(410, 'connection_closed', 'SSH 连接已关闭。'))
          } else if (error) reject(error)
          else resolve(value)
        })
      }),
      SFTP_CALLBACK_TIMEOUT_MS,
      apiError(504, 'sftp_channel_timeout', '打开 SFTP 通道超时，SSH 连接已重置。'),
      () => resetConnectionTransport(connection, client),
    )
    admissionTail = sftpOpening
    sftp = await raceWithAbort(
      sftpOpening,
      operationSignal,
      () => {
        if (deadline.timedOut) resetConnectionTransport(connection, client)
      },
    )
    admissionTail = null
    requireLiveConnection(connection, generation)
    const boundedOperation = raceWithTimeout(
      Promise.resolve().then(() => operation(sftp)),
      SFTP_OPERATION_TIMEOUT_MS,
      apiError(504, 'sftp_timeout', 'SFTP 操作超时，SSH 连接已重置。'),
      () => resetConnectionTransport(connection, client),
    )
    admissionTail = boundedOperation
    const result = await raceWithAbort(
      boundedOperation,
      operationSignal,
      () => {
        try { sftp?.end() } catch {}
        if (deadline.timedOut) resetConnectionTransport(connection, client)
      },
    )
    admissionTail = null
    if (operationSignal.aborted) throw abortError(operationSignal)
    return result
  } catch (error) {
    if (error?.resetTransport) resetConnectionTransport(connection, client)
    if (error?.publicMessage) throw error
    throw apiError(502, 'sftp_failed', 'SFTP 操作失败。')
  } finally {
    try { sftp?.end() } catch {}
    if (deadline.timedOut) resetConnectionTransport(connection, client)
    deadline.dispose()
    if (admissionTail) {
      const cleanup = Promise.resolve(admissionTail).then(() => undefined, () => undefined)
      if (waitForCancellationCleanup) {
        await cleanup
        releaseOperation()
      } else {
        void cleanup.then(releaseOperation)
      }
    } else {
      releaseOperation()
    }
  }
}

function createOperationDeadline(parentSignal, timeoutMs, timeoutError) {
  const controller = new AbortController()
  let timedOut = false
  const abortFromParent = () => {
    if (!controller.signal.aborted) controller.abort(parentSignal.reason)
  }
  if (parentSignal?.aborted) abortFromParent()
  else parentSignal?.addEventListener('abort', abortFromParent, { once: true })
  const timer = setTimeout(() => {
    timedOut = true
    if (!controller.signal.aborted) controller.abort(timeoutError)
  }, timeoutMs)
  timer.unref()
  return {
    signal: controller.signal,
    get timedOut() { return timedOut },
    dispose() {
      clearTimeout(timer)
      parentSignal?.removeEventListener('abort', abortFromParent)
    },
  }
}

function raceWithAbort(operation, signal, onAbort) {
  return new Promise((resolve, reject) => {
    let settled = false
    const finish = (error, value) => {
      if (settled) return
      settled = true
      signal.removeEventListener('abort', abort)
      if (error) reject(error)
      else resolve(value)
    }
    const abort = () => {
      try { onAbort?.() } catch {}
      finish(abortError(signal))
    }
    if (signal.aborted) {
      abort()
      return
    }
    signal.addEventListener('abort', abort, { once: true })
    Promise.resolve(operation).then(
      (value) => finish(null, value),
      (error) => finish(error),
    )
  })
}

function raceWithTimeout(operation, timeoutMs, timeoutError, onTimeout) {
  return new Promise((resolve, reject) => {
    let settled = false
    const timer = setTimeout(() => {
      if (settled) return
      settled = true
      try { onTimeout?.() } catch {}
      reject(timeoutError)
    }, timeoutMs)
    timer.unref()
    Promise.resolve(operation).then(
      (value) => {
        if (settled) {
          try { value?.end?.() } catch {}
          return
        }
        settled = true
        clearTimeout(timer)
        resolve(value)
      },
      (error) => {
        if (settled) return
        settled = true
        clearTimeout(timer)
        reject(error)
      },
    )
  })
}

function resetConnectionTransport(connection, client) {
  const clients = client ? [client] : [...(connection.clients ?? [])]
  for (const candidate of clients) {
    if (connection.client === candidate) connection.client = null
    try { candidate.end() } catch {}
    try { candidate.destroy?.() } catch {}
  }
}

async function listDirectory(connection, remotePath, signal) {
  remotePath = validateRemotePath(remotePath)
  const listing = await withSftp(
    connection,
    (sftp) => readDirectoryBounded(sftp, remotePath, signal),
    signal,
  )
  const entries = listing.rows
  entries.sort((left, right) => Number(right.isDirectory) - Number(left.isDirectory) || left.name.localeCompare(right.name))
  return { path: remotePath, entries, truncated: listing.truncated }
}

async function readDirectoryBounded(sftp, remotePath, signal) {
  const handle = await sftpCall(sftp, 'opendir', remotePath)
  const rows = []
  let metadataBytes = 0
  let truncated = false
  try {
    for (;;) {
      if (signal?.aborted) throw abortError(signal)
      let batch
      try {
        batch = await sftpCall(sftp, 'readdir', handle)
      } catch (error) {
        // SSH_FX_EOF is the successful end marker for handle-based READDIR.
        if (Number(error?.code) === 1) break
        throw error
      }
      if (!Array.isArray(batch) || batch.length === 0) break
      for (const entry of batch) {
        const name = entry?.filename
        if (name === '.' || name === '..') continue
        const nameBytes = typeof name === 'string' ? Buffer.byteLength(name) : Number.POSITIVE_INFINITY
        const entryBytes = nameBytes + 128
        if (
          nameBytes > MAX_REMOTE_NAME_BYTES
          || /[\0-\x1f\x7f]/.test(name)
          || rows.length >= MAX_DIRECTORY_ENTRIES
          || metadataBytes + entryBytes > MAX_DIRECTORY_METADATA_BYTES
        ) {
          truncated = true
          return { rows, truncated }
        }
        rows.push({
          name,
          isDirectory: entry.attrs.isDirectory(),
          size: Number.isSafeInteger(entry.attrs.size) ? entry.attrs.size : null,
          modifiedAtUnix: Number.isSafeInteger(entry.attrs.mtime) ? entry.attrs.mtime : null,
        })
        metadataBytes += entryBytes
      }
    }
    return { rows, truncated }
  } finally {
    await sftpCall(sftp, 'close', handle).catch(() => undefined)
  }
}

async function uploadFiles(connection, input, signal) {
  const remoteDirectory = validateRemotePath(input.remoteDirectory)
  if (!Array.isArray(input.files) || input.files.length < 1 || input.files.length > MAX_UPLOAD_FILES) throw apiError(400, 'invalid_upload', '上传文件数量无效。')
  let bytes = 0
  await withSftp(connection, async (sftp) => {
    for (const item of input.files) {
      const name = validateFileName(item.name)
      if (typeof item.dataBase64 !== 'string' || item.dataBase64.length > Math.ceil(MAX_FILE_BYTES / 3) * 4 + 8) throw apiError(413, 'file_too_large', '文件过大。')
      const data = decodeBase64Strict(item.dataBase64)
      try {
        if (data.length > MAX_FILE_BYTES) throw apiError(413, 'file_too_large', '文件过大。')
        bytes += data.length
        if (bytes > MAX_UPLOAD_TOTAL_BYTES) throw apiError(413, 'upload_too_large', '单次上传总量不能超过 45 MiB。')
        const destination = joinRemote(remoteDirectory, name)
        if (!item.overwrite && await sftpExists(sftp, destination)) throw apiError(409, 'destination_exists', `${name} 已存在。`)
        const temporary = joinRemote(remoteDirectory, `.${name}.nekossh-${randomUUID()}.part`)
        try {
          await sftpCall(sftp, 'writeFile', temporary, data, { mode: 0o600, flag: 'wx' })
          await sftpCall(sftp, 'rename', temporary, destination)
        } catch (error) {
          await sftpCall(sftp, 'unlink', temporary).catch(() => undefined)
          throw error
        }
      } finally {
        data.fill(0)
      }
    }
  }, signal, true)
  return { uploaded: input.files.length, bytes }
}

async function downloadFile(connection, remotePath, signal) {
  return withSftp(connection, async (sftp) => {
    const stat = await sftpCall(sftp, 'lstat', remotePath)
    if (
      !stat.isFile()
      || !Number.isSafeInteger(stat.size)
      || stat.size < 0
      || stat.size > MAX_FILE_BYTES
    ) {
      throw apiError(413, 'file_too_large', '文件过大或不是普通文件。')
    }
    const stream = sftp.createReadStream(remotePath)
    const data = Buffer.allocUnsafe(MAX_FILE_BYTES)
    let bytes = 0
    try {
      for await (const chunk of stream) {
        const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
        if (bytes + buffer.length > MAX_FILE_BYTES) {
          stream.destroy()
          throw apiError(413, 'file_too_large', '文件在下载期间超过 16 MiB，已停止读取。')
        }
        buffer.copy(data, bytes)
        bytes += buffer.length
      }
      return Buffer.from(data.subarray(0, bytes))
    } finally {
      data.fill(0)
    }
  }, signal)
}

async function renameRemote(connection, fromPath, toPath, overwrite, signal) {
  fromPath = validateDeletablePath(fromPath)
  toPath = validateDeletablePath(toPath)
  await withSftp(connection, async (sftp) => {
    if (!overwrite && await sftpExists(sftp, toPath)) throw apiError(409, 'destination_exists', '目标已存在。')
    await sftpCall(sftp, 'rename', fromPath, toPath)
  }, signal)
}

async function commitDelete(owner, connection, confirmationId, signal) {
  const pending = deleteConfirmations.get(confirmationId)
  deleteConfirmations.delete(confirmationId)
  if (!pending || pending.expiresAt <= Date.now() || pending.owner !== owner || pending.connectionId !== connection.id) {
    throw apiError(410, 'delete_confirmation_expired', '删除确认已过期。')
  }
  await withSftp(connection, async (sftp) => {
    const stat = await sftpCall(sftp, 'lstat', pending.remotePath)
    await sftpCall(sftp, stat.isDirectory() ? 'rmdir' : 'unlink', pending.remotePath)
  }, signal)
}

function sftpCall(sftp, method, ...arguments_) {
  const timeoutMs = method === 'writeFile' ? SFTP_TRANSFER_TIMEOUT_MS : SFTP_CALLBACK_TIMEOUT_MS
  return new Promise((resolve, reject) => {
    let settled = false
    const finish = (error, value) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      if (error) reject(error)
      else resolve(value)
    }
    const timer = setTimeout(() => {
      const error = apiError(504, 'sftp_idle_timeout', 'SFTP 操作等待服务器响应超时，SSH 连接已重置。')
      error.resetTransport = true
      try { sftp.end() } catch {}
      finish(error)
    }, timeoutMs)
    timer.unref()
    try {
      sftp[method](...arguments_, (error, value) => finish(error, value))
    } catch (error) {
      finish(error)
    }
  })
}

async function sftpExists(sftp, remotePath) {
  try { await sftpCall(sftp, 'lstat', remotePath); return true } catch { return false }
}

function decodeBase64Strict(value) {
  if (
    value.length % 4 !== 0
    || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(value)
  ) {
    throw apiError(400, 'invalid_upload', '上传文件编码无效。')
  }
  const data = Buffer.from(value, 'base64')
  if (data.toString('base64') !== value) {
    data.fill(0)
    throw apiError(400, 'invalid_upload', '上传文件编码无效。')
  }
  return data
}

function disconnectConnection(connection) {
  if (connection.closed) return
  connection.closed = true
  connection.generation += 1
  connections.delete(connection.id)
  for (const terminal of terminals.values()) {
    if (terminal.connectionId === connection.id) {
      destroyTerminal(terminal)
      terminals.delete(terminal.id)
    }
  }
  clearPendingHostKeys(connection.id)
  for (const [id, pending] of deleteConfirmations) if (pending.connectionId === connection.id) deleteConfirmations.delete(id)
  for (const [id, grant] of agentGrants) if (grant.connectionId === connection.id) agentGrants.delete(id)
  for (const [id, attachment] of attachments) {
    if (attachment.connectionId === connection.id) revokeAttachment(id, 'SSH 连接已关闭。')
  }
  zeroConnection(connection)
}

function disconnectOwner(owner) {
  for (const connection of [...connections.values()]) {
    if (connection.owner === owner) disconnectConnection(connection)
  }
}

function clearPendingHostKeys(connectionId) {
  for (const [id, pending] of pendingHostKeys) {
    if (pending.connectionId !== connectionId) continue
    disposePresentedHostKey(pending)
    pendingHostKeys.delete(id)
    const connection = connections.get(connectionId)
    if (connection?.pendingConfirmationId === id) connection.pendingConfirmationId = null
  }
}

function zeroConnection(connection) {
  for (const client of connection.clients ?? []) {
    try { client.end() } catch {}
    try { client.destroy?.() } catch {}
  }
  connection.clients?.clear()
  connection.client = null
  connection.openingClient = null
  connection.preparing = null
  connection.authenticating = null
  connection.pendingConfirmationId = null
  connection.password?.fill(0)
  connection.privateKey?.fill(0)
  connection.passphrase?.fill(0)
  connection.password = Buffer.alloc(0)
  connection.privateKey = Buffer.alloc(0)
  connection.passphrase = Buffer.alloc(0)
  connection.trustedFingerprint = null
}

function touchConnection(connection) {
  touchConnectionActivity(connection, browserSessions, SESSION_TTL_MS)
}

function touchTerminalActivity(owner, terminal) {
  const connection = requireConnection(owner, terminal.connectionId)
  terminal.expiresAt = Date.now() + SESSION_TTL_MS
  return connection
}

function enforceCapacity(collection, maximum, code, message) {
  if (collection.size >= maximum) throw apiError(429, code, message)
}

function isConnectionLive(connection, generation = connection?.generation) {
  return Boolean(
    connection
    && !connection.closed
    && connection.generation === generation
    && connections.get(connection.id) === connection,
  )
}

function requireLiveConnection(connection, generation = connection?.generation) {
  if (!isConnectionLive(connection, generation)) throw apiError(410, 'connection_closed', 'SSH 连接已关闭。')
  return generation
}

function trackConnectionClient(connection, client) {
  if (!connection.clients.has(client)) {
    connection.clients.add(client)
    client.once('close', () => connection.clients.delete(client))
  }
}

async function routeMcp(request, response) {
  requireMcpAuth(request)
  requireMcpOrigin(request)
  if (request.method === 'GET') throw apiError(405, 'method_not_allowed', '此 MCP 服务使用 JSON 响应模式。')
  if (request.method === 'DELETE') return sendEmpty(response)
  if (request.method !== 'POST') throw apiError(405, 'method_not_allowed', '请求方法不支持。')
  const message = await readJson(request)
  if (message.jsonrpc !== '2.0' || typeof message.method !== 'string') return sendMcpError(response, message.id ?? null, -32600, 'Invalid Request')

  if (message.method === 'initialize') {
    const requested = message.params?.protocolVersion
    if (requested && requested !== SUPPORTED_MCP_VERSION) return sendMcpError(response, message.id, -32602, `Unsupported protocol version: ${requested}`, 400)
    return sendMcpResult(response, message.id, {
      protocolVersion: SUPPORTED_MCP_VERSION,
      capabilities: { tools: { listChanged: false } },
      serverInfo: { name: 'nekossh-ssh2js', version: '0.2.0' },
      instructions: '先在 Web macOS 中连接并生成一次性 Agent 授权，再调用 ssh_connection_attach。',
    })
  }
  if (message.method.startsWith('notifications/')) return sendEmpty(response, 202)
  if (request.headers['mcp-protocol-version'] !== SUPPORTED_MCP_VERSION) {
    return sendMcpError(response, message.id, -32602, `MCP-Protocol-Version must be ${SUPPORTED_MCP_VERSION}`, 400)
  }
  if (message.method === 'ping') return sendMcpResult(response, message.id, {})
  if (message.method === 'tools/list') return sendMcpResult(response, message.id, { tools: mcpTools() })
  if (message.method === 'tools/call') {
    const controller = new AbortController()
    const abort = () => controller.abort()
    request.once('aborted', abort)
    response.once('close', abort)
    try {
      const result = await callMcpTool(message.params?.name, message.params?.arguments ?? {}, controller.signal)
      return sendMcpResult(response, message.id, { content: [{ type: 'text', text: JSON.stringify(result) }], structuredContent: result })
    } catch (error) {
      return sendMcpResult(response, message.id, { content: [{ type: 'text', text: error.publicMessage ?? 'SSH 工具执行失败。' }], isError: true })
    } finally {
      request.off('aborted', abort)
      response.off('close', abort)
    }
  }
  return sendMcpError(response, message.id, -32601, 'Method not found')
}

function mcpTools() {
  const attachment = { type: 'string', description: 'ssh_connection_attach 返回的短时 attachmentId' }
  return [
    tool('ssh_connection_attach', '兑换 Web macOS 生成的一次性授权。', { grant: { type: 'string' } }, ['grant']),
    tool('ssh_connection_close', '关闭此 Agent attachment，不关闭浏览器连接。', { attachmentId: attachment }, ['attachmentId']),
    tool('ssh_exec', '执行一次远端命令。', { attachmentId: attachment, command: { type: 'string' }, cwd: { type: 'string' } }, ['attachmentId', 'command']),
    tool('ssh_sftp_list', '列出远端目录。', { attachmentId: attachment, path: { type: 'string' } }, ['attachmentId', 'path']),
    tool('ssh_sftp_mkdir', '新建一个远端目录。', { attachmentId: attachment, path: { type: 'string' } }, ['attachmentId', 'path']),
    tool('ssh_sftp_rename', '重命名远端项目，不覆盖。', { attachmentId: attachment, fromPath: { type: 'string' }, toPath: { type: 'string' } }, ['attachmentId', 'fromPath', 'toPath']),
  ]
}

function tool(name, description, properties, required) {
  return { name, description, inputSchema: { type: 'object', properties, required, additionalProperties: false } }
}

async function callMcpTool(name, arguments_, requestSignal) {
  if (name === 'ssh_connection_attach') {
    const grantId = requireString(arguments_.grant, 'grant 无效。', 128)
    const grant = agentGrants.get(grantId)
    agentGrants.delete(grantId)
    const connection = grant ? connections.get(grant.connectionId) : null
    if (!grant || grant.expiresAt <= Date.now() || !connection || connection.expiresAt <= Date.now() || !isConnectionLive(connection)) {
      if (connection?.expiresAt <= Date.now()) disconnectConnection(connection)
      throw publicError('授权已过期。')
    }
    purgeExpired()
    enforceCapacity(attachments, MAX_ATTACHMENTS, 'attachment_limit', 'Agent attachment 数量已达到上限。')
    touchConnection(connection)
    const attachmentId = randomUUID()
    const attachment = {
      connectionId: grant.connectionId,
      scopes: grant.scopes,
      expiresAt: Date.now() + ATTACHMENT_TTL_MS,
      revoked: false,
      revokeReason: null,
      operations: new Set(),
      timer: null,
    }
    attachment.timer = setTimeout(() => revokeAttachment(attachmentId, 'Agent attachment 已过期。'), ATTACHMENT_TTL_MS)
    attachment.timer.unref()
    attachments.set(attachmentId, attachment)
    return { attachmentId, connection: connectionSummary(connection), expiresInSeconds: ATTACHMENT_TTL_MS / 1000 }
  }
  const attachment = requireAttachment(arguments_.attachmentId)
  const connection = connections.get(attachment.connectionId)
  if (name === 'ssh_connection_close') {
    revokeAttachment(arguments_.attachmentId, 'Agent attachment 已关闭。')
    return { closed: true }
  }
  if (name === 'ssh_exec') {
    requireScope(attachment, 'terminal')
    return withAttachmentOperation(attachment, requestSignal, (signal) => (
      executeCommand(connection, requireString(arguments_.command, '命令无效。', 32 * 1024), arguments_.cwd, signal)
    ))
  }
  if (name === 'ssh_sftp_list') {
    requireScope(attachment, 'files_read')
    return withAttachmentOperation(attachment, requestSignal, (signal) => listDirectory(connection, arguments_.path, signal))
  }
  if (name === 'ssh_sftp_mkdir') {
    requireScope(attachment, 'files_write')
    await withAttachmentOperation(attachment, requestSignal, (signal) => (
      withSftp(connection, (sftp) => sftpCall(sftp, 'mkdir', validateRemotePath(arguments_.path), { mode: 0o755 }), signal)
    ))
    return { created: true }
  }
  if (name === 'ssh_sftp_rename') {
    requireScope(attachment, 'files_write')
    await withAttachmentOperation(attachment, requestSignal, (signal) => (
      renameRemote(connection, arguments_.fromPath, arguments_.toPath, false, signal)
    ))
    return { renamed: true }
  }
  throw publicError('未知工具。')
}

async function executeCommand(connection, command, rawCwd, signal) {
  if (command.includes('\0')) throw publicError('命令包含无效字符。')
  const releaseOperation = reserveSshOperation(connection)
  const deadline = createOperationDeadline(
    signal,
    EXEC_OPERATION_TIMEOUT_MS,
    apiError(504, 'exec_timeout', '远端命令执行超时，SSH 连接已重置。'),
  )
  const operationSignal = deadline.signal
  let client
  let activeChannel
  let admissionTail
  try {
    if (operationSignal.aborted) throw abortError(operationSignal)
    const generation = requireLiveConnection(connection)
    const cwd = rawCwd ? validateRemotePath(rawCwd) : null
    const remoteCommand = cwd ? `cd -- ${shellQuote(cwd)} && ${command}` : command
    const clientOpening = openClient(connection)
    admissionTail = clientOpening
    client = await raceWithAbort(
      clientOpening,
      operationSignal,
      () => {
        if (deadline.timedOut) resetConnectionTransport(connection)
      },
    )
    admissionTail = null
    requireLiveConnection(connection, generation)
    if (operationSignal.aborted) throw abortError(operationSignal)
    const channelOpening = raceWithTimeout(
      new Promise((resolve, reject) => {
        try {
          client.exec(remoteCommand, (error, openedChannel) => {
            if (operationSignal.aborted) {
              try { openedChannel?.close() } catch {}
              reject(abortError(operationSignal))
            } else if (!isConnectionLive(connection, generation)) {
              try { openedChannel?.close() } catch {}
              reject(apiError(410, 'connection_closed', 'SSH 连接已关闭。'))
            } else if (error) reject(error)
            else resolve(openedChannel)
          })
        } catch (error) {
          reject(error)
        }
      }),
      EXEC_CHANNEL_OPEN_TIMEOUT_MS,
      apiError(504, 'exec_channel_timeout', '打开远端命令通道超时，SSH 连接已重置。'),
      () => resetConnectionTransport(connection, client),
    )
    admissionTail = channelOpening
    activeChannel = await raceWithAbort(
      channelOpening,
      operationSignal,
      () => {
        if (deadline.timedOut) resetConnectionTransport(connection, client)
      },
    )
    admissionTail = null
    const completion = new Promise((resolve, reject) => {
      let settled = false
      const outputBuffer = Buffer.allocUnsafe(MAX_TERMINAL_OUTPUT_BYTES)
      let outputBytes = 0
      let truncated = false
      const timer = setTimeout(() => {
        try { activeChannel.close() } catch {}
        resetConnectionTransport(connection, client)
        finish(apiError(504, 'exec_timeout', '远端命令执行超时，SSH 连接已重置。'))
      }, EXEC_OPERATION_TIMEOUT_MS)
      timer.unref()
      const finish = (error, result) => {
        if (settled) return
        settled = true
        clearTimeout(timer)
        activeChannel.off('data', add)
        activeChannel.stderr.off('data', add)
        activeChannel.off('close', onClose)
        activeChannel.off('error', onError)
        outputBuffer.fill(0)
        if (error) reject(error)
        else resolve(result)
      }
      const add = (data) => {
        if (settled || truncated) return
        const buffer = Buffer.isBuffer(data) ? data : Buffer.from(data)
        const remaining = MAX_TERMINAL_OUTPUT_BYTES - outputBytes
        if (buffer.length > remaining) {
          if (remaining > 0) buffer.copy(outputBuffer, outputBytes, 0, remaining)
          outputBytes += remaining
          truncated = true
          activeChannel.close()
        } else {
          buffer.copy(outputBuffer, outputBytes)
          outputBytes += buffer.length
        }
      }
      const onClose = (code) => {
        if (!isConnectionLive(connection, generation)) {
          finish(apiError(410, 'connection_closed', 'SSH 连接已关闭。'))
        } else {
          finish(null, {
            output: outputBuffer.subarray(0, outputBytes).toString('utf8'),
            exitStatus: Number.isInteger(code) ? code : -1,
            workingDirectory: cwd ?? '',
            truncated,
          })
        }
      }
      const onError = (error) => finish(error)
      activeChannel.on('data', add)
      activeChannel.stderr.on('data', add)
      activeChannel.on('close', onClose)
      activeChannel.on('error', onError)
    })
    admissionTail = completion
    const result = await raceWithAbort(
      completion,
      operationSignal,
      () => {
        try { activeChannel?.close() } catch {}
        if (deadline.timedOut) resetConnectionTransport(connection, client)
      },
    )
    admissionTail = null
    return result
  } catch (error) {
    if (error?.publicMessage) throw error
    throw publicError('远端命令执行失败。')
  } finally {
    if (deadline.timedOut) resetConnectionTransport(connection, client)
    deadline.dispose()
    if (admissionTail) {
      const cleanup = Promise.resolve(admissionTail).then(() => undefined, () => undefined)
      void cleanup.then(releaseOperation)
    } else {
      releaseOperation()
    }
  }
}

function requireAttachment(id) {
  const attachmentId = requireString(id, 'attachmentId 无效。', 128)
  const attachment = attachments.get(attachmentId)
  const connection = attachment ? connections.get(attachment.connectionId) : null
  if (!attachment || attachment.revoked || attachment.expiresAt <= Date.now() || !connection || connection.expiresAt <= Date.now() || !isConnectionLive(connection)) {
    revokeAttachment(attachmentId, 'Agent attachment 已过期。')
    if (connection?.expiresAt <= Date.now()) disconnectConnection(connection)
    throw publicError('Agent attachment 已过期。')
  }
  touchConnection(connection)
  return attachment
}

async function withAttachmentOperation(attachment, requestSignal, operation) {
  const controller = new AbortController()
  const abortFromRequest = () => controller.abort('Agent 请求已取消。')
  if (requestSignal?.aborted) abortFromRequest()
  else requestSignal?.addEventListener('abort', abortFromRequest, { once: true })
  const unregister = registerRevocableOperation(attachment, (reason) => controller.abort(reason))
  try {
    if (controller.signal.aborted) throw abortError(controller.signal)
    return await operation(controller.signal)
  } finally {
    unregister()
    requestSignal?.removeEventListener('abort', abortFromRequest)
  }
}

function revokeAttachment(id, reason) {
  const attachment = attachments.get(id)
  attachments.delete(id)
  if (attachment) {
    clearTimeout(attachment.timer)
    revokeOperations(attachment, reason)
  }
}

function abortError(signal) {
  const reason = signal?.reason
  if (reason?.publicMessage) return reason
  return publicError(typeof reason === 'string' && reason ? reason : '操作已取消。')
}

function requireScope(attachment, scope) {
  if (!attachment.scopes.has(scope)) throw publicError('此 attachment 没有相应权限。')
}

function authenticateBrowser(request) {
  const token = browserSessionToken(request)
  const session = browserSessions.get(token)
  if (!session || session.expiresAt <= Date.now()) {
    retireBrowserSession(token, browserSessions, disconnectOwner)
    throw apiError(401, 'browser_session_required', '浏览器会话已过期，请刷新页面。')
  }
  session.expiresAt = Date.now() + SESSION_TTL_MS
  return session.owner
}

function browserSessionToken(request) {
  return cookieValue(request.headers.cookie, 'nekossh_session')
}

function requireConnection(owner, id) {
  const connection = connections.get(id)
  if (!connection || connection.owner !== owner) throw apiError(404, 'connection_not_found', 'SSH 连接不存在或已过期。')
  if (connection.expiresAt <= Date.now()) {
    disconnectConnection(connection)
    throw apiError(404, 'connection_not_found', 'SSH 连接不存在或已过期。')
  }
  touchConnection(connection)
  return connection
}

function requireTerminal(owner, id) {
  const terminal = terminals.get(id)
  if (!terminal || terminal.owner !== owner) throw apiError(404, 'terminal_not_found', '终端不存在或已过期。')
  if (terminal.expiresAt <= Date.now()) {
    destroyTerminal(terminal)
    terminals.delete(id)
    throw apiError(404, 'terminal_not_found', '终端不存在或已过期。')
  }
  return terminal
}

function purgeExpiredTerminals(now) {
  for (const [id, terminal] of terminals) {
    if (terminal.expiresAt <= now) {
      destroyTerminal(terminal)
      terminals.delete(id)
    }
  }
}

function requireBrowserOrigin(request) {
  const origin = request.headers.origin
  if (origin && !allowedOrigins.has(origin)) throw apiError(403, 'invalid_origin', '请求来源不允许。')
  if (request.method !== 'GET' && !origin) throw apiError(403, 'origin_required', '缺少浏览器来源。')
}

function requireMcpOrigin(request) {
  const origin = request.headers.origin
  if (origin && !allowedOrigins.has(origin)) throw apiError(403, 'invalid_origin', '请求来源不允许。')
}

function requireMcpAuth(request) {
  const value = request.headers.authorization ?? ''
  const prefix = 'Bearer '
  if (!value.startsWith(prefix) || !safeEqual(value.slice(prefix.length), MCP_TOKEN)) throw apiError(401, 'unauthorized', 'MCP Bearer token 无效。')
}

function purgeExpired() {
  const now = Date.now()
  for (const [id, session] of [...browserSessions]) {
    if (session.expiresAt <= now) retireBrowserSession(id, browserSessions, disconnectOwner)
  }
  for (const connection of [...connections.values()]) if (connection.expiresAt <= now) disconnectConnection(connection)
  purgeExpiredTerminals(now)
  for (const [id, pending] of pendingHostKeys) {
    if (pending.expiresAt <= now) {
      disposePresentedHostKey(pending)
      pendingHostKeys.delete(id)
      const connection = connections.get(pending.connectionId)
      if (connection?.pendingConfirmationId === id) connection.pendingConfirmationId = null
    }
  }
  for (const [id, pending] of deleteConfirmations) if (pending.expiresAt <= now) deleteConfirmations.delete(id)
  for (const [id, grant] of agentGrants) if (grant.expiresAt <= now) agentGrants.delete(id)
  for (const [id, attachment] of attachments) {
    if (attachment.expiresAt <= now) revokeAttachment(id, 'Agent attachment 已过期。')
  }
}

function validateRemotePath(value) {
  value = requireString(value, '远端路径无效。', 4096)
  if (!value.startsWith('/') || /[\0-\x1f\x7f]/.test(value) || value.split('/').some((segment) => segment === '..')) throw apiError(400, 'invalid_remote_path', '远端路径无效。')
  return path.normalize(value)
}

function validateDeletablePath(value) {
  value = validateRemotePath(value)
  if (value === '/' || value === '.' || value === '..') throw apiError(400, 'invalid_remote_path', '不能操作远端根目录。')
  return value
}

function validateFileName(value) {
  value = requireString(value, '文件名无效。', 255)
  if (value === '.' || value === '..' || value.includes('/') || value.includes('\\') || /[\0-\x1f]/.test(value)) throw apiError(400, 'invalid_file_name', '文件名无效。')
  return value
}

function joinRemote(directory, name) {
  return validateRemotePath(path.join(directory, validateFileName(name)))
}

function fingerprint(publicKey) {
  return `SHA256:${createHash('sha256').update(publicKey).digest('base64').replace(/=+$/, '')}`
}

function keyAlgorithm(publicKey) {
  if (publicKey.length < 5) return 'unknown'
  const length = publicKey.readUInt32BE(0)
  if (length < 1 || length > 128 || 4 + length > publicKey.length) return 'unknown'
  return publicKey.subarray(4, 4 + length).toString('ascii')
}

function validateDeclaredBodyLength(request, maximumBytes) {
  declaredBodyLength(request, maximumBytes)
}

function declaredBodyLength(request, maximumBytes) {
  const value = request.headers['content-length']
  if (value === undefined) return null
  if (typeof value !== 'string' || !/^(?:0|[1-9]\d*)$/.test(value)) {
    throw apiError(400, 'invalid_content_length', 'Content-Length 无效。')
  }
  const bytes = Number(value)
  if (!Number.isSafeInteger(bytes)) throw apiError(413, 'body_too_large', '请求内容过大。')
  if (bytes > maximumBytes) throw apiError(413, 'body_too_large', '请求内容过大。')
  return bytes
}

function readJson(request, maximumBytes = MAX_JSON_BYTES) {
  return new Promise((resolve, reject) => {
    const declaredBytes = declaredBodyLength(request, maximumBytes)
    const body = Buffer.allocUnsafe(declaredBytes ?? maximumBytes)
    let size = 0
    let settled = false
    const finish = (error, value) => {
      if (settled) return
      settled = true
      body.fill(0)
      if (error) reject(error)
      else resolve(value)
    }
    request.on('data', (chunk) => {
      if (settled) return
      if (size + chunk.length > maximumBytes || size + chunk.length > body.length) {
        finish(apiError(413, 'body_too_large', '请求内容过大。'))
        request.destroy()
        return
      }
      chunk.copy(body, size)
      size += chunk.length
    })
    request.on('end', () => {
      if (settled) return
      try {
        finish(null, size ? JSON.parse(body.subarray(0, size).toString('utf8')) : {})
      } catch {
        finish(apiError(400, 'invalid_json', 'JSON 格式无效。'))
      }
    })
    request.on('error', (error) => finish(error))
  })
}

function requireString(value, message, maxBytes) {
  if (typeof value !== 'string' || !value.trim() || Buffer.byteLength(value) > maxBytes) throw apiError(400, 'invalid_input', message)
  return value
}

function requireTerminalInput(value) {
  if (
    typeof value !== 'string'
    || value.length === 0
    || Buffer.byteLength(value) > MAX_TERMINAL_INPUT_BYTES
  ) {
    throw apiError(400, 'invalid_input', '终端输入无效。')
  }
  return value
}

function clampInteger(value, minimum, maximum, fallback) {
  const parsed = Number(value)
  if (!Number.isInteger(parsed)) return fallback
  return Math.min(maximum, Math.max(minimum, parsed))
}

function requireInteger(value, minimum, maximum, message) {
  const parsed = Number(value)
  if (!Number.isInteger(parsed) || parsed < minimum || parsed > maximum) {
    throw apiError(400, 'invalid_input', message)
  }
  return parsed
}

function parsePort(value, name) {
  const port = Number(value)
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error(`${name} must be a valid TCP port`)
  return port
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}

function safeEqual(left, right) {
  if (typeof left !== 'string' || typeof right !== 'string') return false
  const a = Buffer.from(left)
  const b = Buffer.from(right)
  return a.length === b.length && timingSafeEqual(a, b)
}

function validToken(value) {
  return typeof value === 'string' && value.length >= 24
}

function shellQuote(value) {
  return `'${value.replaceAll("'", "'\"'\"'")}'`
}

function apiError(status, code, publicMessage) {
  return Object.assign(new Error(code), { status, code, publicMessage })
}

function publicError(publicMessage) {
  return Object.assign(new Error('tool_error'), { publicMessage })
}

function sendError(response, error) {
  if (process.env.NEKOSSH_DEBUG === '1') console.error(error)
  sendJson(response, error.status ?? 500, { code: error.code ?? 'internal_error', message: error.publicMessage ?? '本地 SSH 服务发生错误。' })
}

function sendJson(response, status, value, headers = {}) {
  const body = Buffer.from(JSON.stringify(value))
  return sendBoundedBody(response, status, body, {
    'content-type': 'application/json; charset=utf-8',
    ...headers,
  })
}

function sendBoundedBody(response, status, body, headers = {}) {
  let release
  try {
    release = outboundResponseAdmission.acquire('http', body.length)
  } catch (error) {
    body.fill(0)
    if (response.headersSent) {
      response.destroy()
      return response
    }
    const rejection = Buffer.from(JSON.stringify({
      code: 'outbound_response_limit',
      message: '本地响应缓冲已达到上限，请稍后重试。',
    }))
    response.writeHead(503, {
      'content-type': 'application/json; charset=utf-8',
      'content-length': rejection.length,
      'cache-control': 'no-store',
      connection: 'close',
    })
    response.end(rejection, () => rejection.fill(0))
    return response
  }

  let released = false
  const cleanup = () => {
    if (released) return
    released = true
    response.off('finish', cleanup)
    response.off('close', cleanup)
    response.off('error', cleanup)
    body.fill(0)
    release()
  }
  response.once('finish', cleanup)
  response.once('close', cleanup)
  response.once('error', cleanup)
  try {
    response.writeHead(status, {
      'content-length': body.length,
      'cache-control': 'no-store',
      ...headers,
    })
    response.end(body)
  } catch (error) {
    cleanup()
    throw error
  }
  return response
}

function sendText(response, status, value) {
  response.writeHead(status, { 'content-type': 'text/plain; charset=utf-8', 'cache-control': 'no-store' })
  response.end(value)
}

function sendEmpty(response, status = 204) {
  response.writeHead(status, { 'cache-control': 'no-store' })
  response.end()
}

function sendMcpResult(response, id, result) {
  sendJson(response, 200, { jsonrpc: '2.0', id, result }, { 'mcp-protocol-version': SUPPORTED_MCP_VERSION })
}

function sendMcpError(response, id, code, message, status = 200) {
  sendJson(response, status, { jsonrpc: '2.0', id, error: { code, message } }, { 'mcp-protocol-version': SUPPORTED_MCP_VERSION })
}
