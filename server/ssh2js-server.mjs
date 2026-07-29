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
import { TerminalAdmission, TerminalAdmissionError } from './terminal-admission.mjs'

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
const MAX_UPLOAD_FILES = 32
const MAX_TERMINAL_OUTPUT_BYTES = 1024 * 1024
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

const browserSessions = new Map()
const connections = new Map()
const pendingHostKeys = new Map()
const terminals = new Map()
const deleteConfirmations = new Map()
const agentGrants = new Map()
const attachments = new Map()
const terminalAdmission = new TerminalAdmission(MAX_TERMINALS, MAX_TERMINAL_RECORDS)

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

server.requestTimeout = 30_000
server.headersTimeout = 10_000
server.keepAliveTimeout = 5_000
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
      return sendJson(response, 201, await startTerminal(connection, owner, input.columns, input.rows))
    }
    if (request.method === 'GET' && suffix === 'files') {
      return sendJson(response, 200, await listDirectory(connection, url.searchParams.get('path') ?? '/'))
    }
    if (request.method === 'POST' && suffix === 'files/mkdir') {
      const input = await readJson(request)
      await withSftp(connection, (sftp) => sftpCall(sftp, 'mkdir', validateRemotePath(input.path), { mode: 0o755 }))
      return sendEmpty(response)
    }
    if (request.method === 'POST' && suffix === 'files/rename') {
      const input = await readJson(request)
      await renameRemote(connection, input.fromPath, input.toPath, input.overwrite === true)
      return sendEmpty(response)
    }
    if (request.method === 'POST' && suffix === 'files/upload') {
      const input = await readJson(request, MAX_BODY_BYTES)
      return sendJson(response, 200, await uploadFiles(connection, input))
    }
    if (request.method === 'GET' && suffix === 'files/download') {
      const remotePath = validateRemotePath(url.searchParams.get('path') ?? '')
      const data = await downloadFile(connection, remotePath)
      response.writeHead(200, {
        'content-type': 'application/octet-stream',
        'content-length': data.length,
        'content-disposition': `attachment; filename="${safeFileName(path.basename(remotePath))}"`,
        'cache-control': 'no-store',
      })
      return response.end(data)
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
      await commitDelete(owner, connection, input.confirmationId)
      return sendEmpty(response)
    }
  }

  if (segments[0] === 'api' && segments[1] === 'terminals' && segments[2]) {
    const terminal = requireTerminal(owner, segments[2])
    const suffix = segments.slice(3).join('/')
    if (request.method === 'POST' && suffix === 'input') {
      const input = await readJson(request)
      const data = requireString(input.data, '终端输入无效。', 16 * 1024)
      if (!terminal.stream || terminal.closed) throw apiError(404, 'terminal_closed', '终端已关闭。')
      touchTerminalActivity(owner, terminal)
      terminal.stream.write(data)
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
      return sendJson(response, 200, {
        cursor: terminal.cursor,
        chunks: terminal.chunks.filter((chunk) => chunk.cursor > cursor),
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
  const port = clampInteger(input.port, 1, 65535, 22)
  const username = requireString(input.username, '用户名不能为空。', 128)
  if (input.authentication?.kind !== 'password') throw apiError(400, 'unsupported_authentication', 'Web macOS 当前使用密码认证。')
  const password = requireString(input.authentication.password, '密码不能为空。', 16 * 1024)
  if (!/^[A-Za-z0-9._:-]+$/.test(host) || host.includes('..')) throw apiError(400, 'invalid_host', '主机格式无效。')
  return {
    id: randomUUID(),
    owner,
    label,
    host,
    port,
    username,
    password: Buffer.from(password, 'utf8'),
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
    client.connect({
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
      if (attempt === 0) await delay(300)
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
    let passwordAttempted = false
    const timer = setTimeout(() => finish(apiError(504, 'ssh_timeout', 'SSH 连接超时。')), 20_000)
    const finish = (error) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      if (error || !isConnectionLive(connection, generation)) {
        client.end()
        reject(error
          ? apiError(502, 'ssh_connection_failed', 'SSH 连接或认证失败。')
          : apiError(410, 'connection_closed', 'SSH 连接已关闭。'))
      } else resolve(client)
    }
    client.once('ready', () => finish())
    client.once('error', finish)
    client.connect({
      host: connection.host,
      port: connection.port,
      username: connection.username,
      authHandler(methodsLeft) {
        if (passwordAttempted || !isConnectionLive(connection, generation)) return false
        if (methodsLeft !== null && !methodsLeft.includes('password')) return false
        passwordAttempted = true
        return { type: 'password', username: connection.username, password: connection.password }
      },
      readyTimeout: 10_000,
      keepaliveInterval: 10_000,
      keepaliveCountMax: 3,
      hostVerifier(publicKey) {
        return safeEqual(connection.trustedFingerprint, fingerprint(Buffer.from(publicKey)))
      },
    })
  })
}

async function startTerminal(connection, owner, rawColumns, rawRows) {
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
  try {
    const generation = requireLiveConnection(connection)
    const columns = clampInteger(rawColumns, 20, 500, 110)
    const rows = clampInteger(rawRows, 5, 300, 34)
    const client = await openClient(connection)
    let stream
    try {
      stream = await openShellChannel(
        client,
        { term: 'xterm-256color', cols: columns, rows },
        () => isConnectionLive(connection, generation),
        () => apiError(410, 'connection_closed', 'SSH 连接已关闭。'),
      )
    } catch (error) {
      if (error?.code === 'connection_closed') throw error
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
      closed: false,
      exitStatus: null,
      error: null,
      expiresAt: Date.now() + SESSION_TTL_MS,
    }
    terminals.set(terminal.id, terminal)
    stream.on('data', (data) => appendTerminal(terminal, Buffer.from(data).toString('utf8')))
    stream.on('close', (code) => closeTerminalRecord(terminal, Number.isInteger(code) ? code : null, null))
    stream.on('error', () => closeTerminalRecord(terminal, null, '终端通道错误。'))
    return { terminalId: terminal.id }
  } finally {
    releaseReservation()
  }
}

function appendTerminal(terminal, data) {
  if (terminal.closed || !data) return
  const bytes = Buffer.byteLength(data)
  terminal.cursor += 1
  terminal.chunks.push({ cursor: terminal.cursor, data })
  terminal.outputBytes += bytes
  while (terminal.outputBytes > MAX_TERMINAL_OUTPUT_BYTES && terminal.chunks.length > 1) {
    const removed = terminal.chunks.shift()
    terminal.outputBytes -= Buffer.byteLength(removed.data)
  }
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

async function withSftp(connection, operation, signal) {
  if (signal?.aborted) throw abortError(signal)
  const generation = requireLiveConnection(connection)
  const client = await openClient(connection)
  if (signal?.aborted) throw abortError(signal)
  let sftp
  let rejectOpening
  const abort = () => {
    try { sftp?.end() } catch {}
    rejectOpening?.(abortError(signal))
  }
  signal?.addEventListener('abort', abort, { once: true })
  try {
    sftp = await new Promise((resolve, reject) => {
      rejectOpening = reject
      client.sftp((error, value) => {
        rejectOpening = null
        if (signal?.aborted) {
          try { value?.end() } catch {}
          reject(abortError(signal))
        } else if (!isConnectionLive(connection, generation)) {
          try { value?.end() } catch {}
          reject(apiError(410, 'connection_closed', 'SSH 连接已关闭。'))
        } else if (error) reject(error)
        else resolve(value)
      })
    })
    requireLiveConnection(connection, generation)
    const result = await operation(sftp)
    if (signal?.aborted) throw abortError(signal)
    return result
  } catch (error) {
    if (error?.publicMessage) throw error
    throw apiError(502, 'sftp_failed', 'SFTP 操作失败。')
  } finally {
    signal?.removeEventListener('abort', abort)
    try { sftp?.end() } catch {}
  }
}

async function listDirectory(connection, remotePath, signal) {
  remotePath = validateRemotePath(remotePath)
  const rows = await withSftp(connection, (sftp) => sftpCall(sftp, 'readdir', remotePath), signal)
  const entries = rows.slice(0, 10_000).map((entry) => ({
    name: entry.filename,
    isDirectory: entry.attrs.isDirectory(),
    size: Number.isSafeInteger(entry.attrs.size) ? entry.attrs.size : null,
    modifiedAtUnix: Number.isSafeInteger(entry.attrs.mtime) ? entry.attrs.mtime : null,
  })).filter((entry) => entry.name !== '.' && entry.name !== '..')
  entries.sort((left, right) => Number(right.isDirectory) - Number(left.isDirectory) || left.name.localeCompare(right.name))
  return { path: remotePath, entries, truncated: rows.length > entries.length }
}

async function uploadFiles(connection, input) {
  const remoteDirectory = validateRemotePath(input.remoteDirectory)
  if (!Array.isArray(input.files) || input.files.length < 1 || input.files.length > MAX_UPLOAD_FILES) throw apiError(400, 'invalid_upload', '上传文件数量无效。')
  let bytes = 0
  await withSftp(connection, async (sftp) => {
    for (const item of input.files) {
      const name = validateFileName(item.name)
      if (typeof item.dataBase64 !== 'string' || item.dataBase64.length > Math.ceil(MAX_FILE_BYTES / 3) * 4 + 8) throw apiError(413, 'file_too_large', '文件过大。')
      const data = Buffer.from(item.dataBase64, 'base64')
      if (data.length > MAX_FILE_BYTES) throw apiError(413, 'file_too_large', '文件过大。')
      bytes += data.length
      const destination = joinRemote(remoteDirectory, name)
      if (!item.overwrite && await sftpExists(sftp, destination)) throw apiError(409, 'destination_exists', `${name} 已存在。`)
      const temporary = joinRemote(remoteDirectory, `.${name}.nekossh-${randomUUID()}.part`)
      try {
        await sftpCall(sftp, 'writeFile', temporary, data, { mode: 0o600, flag: 'wx' })
        await sftpCall(sftp, 'rename', temporary, destination)
      } catch (error) {
        await sftpCall(sftp, 'unlink', temporary).catch(() => undefined)
        throw error
      } finally {
        data.fill(0)
      }
    }
  })
  return { uploaded: input.files.length, bytes }
}

async function downloadFile(connection, remotePath) {
  return withSftp(connection, async (sftp) => {
    const stat = await sftpCall(sftp, 'stat', remotePath)
    if (stat.isDirectory() || stat.size > MAX_FILE_BYTES) throw apiError(413, 'file_too_large', '文件过大或不是普通文件。')
    const data = await sftpCall(sftp, 'readFile', remotePath)
    if (data.length > MAX_FILE_BYTES) throw apiError(413, 'file_too_large', '文件过大。')
    return data
  })
}

async function renameRemote(connection, fromPath, toPath, overwrite, signal) {
  fromPath = validateDeletablePath(fromPath)
  toPath = validateDeletablePath(toPath)
  await withSftp(connection, async (sftp) => {
    if (!overwrite && await sftpExists(sftp, toPath)) throw apiError(409, 'destination_exists', '目标已存在。')
    await sftpCall(sftp, 'rename', fromPath, toPath)
  }, signal)
}

async function commitDelete(owner, connection, confirmationId) {
  const pending = deleteConfirmations.get(confirmationId)
  deleteConfirmations.delete(confirmationId)
  if (!pending || pending.expiresAt <= Date.now() || pending.owner !== owner || pending.connectionId !== connection.id) {
    throw apiError(410, 'delete_confirmation_expired', '删除确认已过期。')
  }
  await withSftp(connection, async (sftp) => {
    const stat = await sftpCall(sftp, 'lstat', pending.remotePath)
    await sftpCall(sftp, stat.isDirectory() ? 'rmdir' : 'unlink', pending.remotePath)
  })
}

function sftpCall(sftp, method, ...arguments_) {
  return new Promise((resolve, reject) => sftp[method](...arguments_, (error, value) => error ? reject(error) : resolve(value)))
}

async function sftpExists(sftp, remotePath) {
  try { await sftpCall(sftp, 'lstat', remotePath); return true } catch { return false }
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
  connection.password = Buffer.alloc(0)
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
  if (signal?.aborted) throw abortError(signal)
  const generation = requireLiveConnection(connection)
  const cwd = rawCwd ? validateRemotePath(rawCwd) : null
  const remoteCommand = cwd ? `cd -- ${shellQuote(cwd)} && ${command}` : command
  const client = await openClient(connection)
  requireLiveConnection(connection, generation)
  if (signal?.aborted) throw abortError(signal)
  try {
    return await new Promise((resolve, reject) => {
      let settled = false
      let activeChannel
      const finish = (error, result) => {
        if (settled) return
        settled = true
        signal?.removeEventListener('abort', abort)
        if (error) reject(error)
        else resolve(result)
      }
      const abort = () => {
        try { activeChannel?.close() } catch {}
        finish(abortError(signal))
      }
      signal?.addEventListener('abort', abort, { once: true })
      try {
        client.exec(remoteCommand, (error, openedChannel) => {
          if (settled || signal?.aborted) {
            try { openedChannel?.close() } catch {}
            return finish(abortError(signal))
          }
          if (!isConnectionLive(connection, generation)) {
            try { openedChannel?.close() } catch {}
            return finish(apiError(410, 'connection_closed', 'SSH 连接已关闭。'))
          }
          if (error) return finish(error)
          activeChannel = openedChannel
          const chunks = []
          let size = 0
          let truncated = false
          const add = (data) => {
            if (truncated) return
            const buffer = Buffer.from(data)
            if (size + buffer.length > MAX_TERMINAL_OUTPUT_BYTES) {
              chunks.push(buffer.subarray(0, Math.max(0, MAX_TERMINAL_OUTPUT_BYTES - size)))
              truncated = true
              openedChannel.close()
            } else chunks.push(buffer)
            size += buffer.length
          }
          openedChannel.on('data', add)
          openedChannel.stderr.on('data', add)
          openedChannel.on('close', (code) => {
            if (signal?.aborted) finish(abortError(signal))
            else if (!isConnectionLive(connection, generation)) finish(apiError(410, 'connection_closed', 'SSH 连接已关闭。'))
            else finish(null, { output: Buffer.concat(chunks).toString('utf8'), exitStatus: Number.isInteger(code) ? code : -1, workingDirectory: cwd ?? '', truncated })
          })
          openedChannel.on('error', (error) => finish(error))
        })
      } catch (error) {
        finish(error)
      }
    })
  } catch (error) {
    if (error?.publicMessage) throw error
    throw publicError('远端命令执行失败。')
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
  if (!value.startsWith('/') || value.includes('\0') || value.split('/').some((segment) => segment === '..')) throw apiError(400, 'invalid_remote_path', '远端路径无效。')
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

function readJson(request, maximumBytes = MAX_JSON_BYTES) {
  return new Promise((resolve, reject) => {
    const chunks = []
    let size = 0
    request.on('data', (chunk) => {
      size += chunk.length
      if (size > maximumBytes) {
        reject(apiError(413, 'body_too_large', '请求内容过大。'))
        request.destroy()
      } else chunks.push(chunk)
    })
    request.on('end', () => {
      try { resolve(chunks.length ? JSON.parse(Buffer.concat(chunks).toString('utf8')) : {}) }
      catch { reject(apiError(400, 'invalid_json', 'JSON 格式无效。')) }
    })
    request.on('error', reject)
  })
}

function requireString(value, message, maxBytes) {
  if (typeof value !== 'string' || !value.trim() || Buffer.byteLength(value) > maxBytes) throw apiError(400, 'invalid_input', message)
  return value
}

function clampInteger(value, minimum, maximum, fallback) {
  const parsed = Number(value)
  if (!Number.isInteger(parsed)) return fallback
  return Math.min(maximum, Math.max(minimum, parsed))
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

function safeFileName(value) {
  return validateFileName(value).replaceAll('"', '_')
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
  response.writeHead(status, { 'content-type': 'application/json; charset=utf-8', 'content-length': body.length, 'cache-control': 'no-store', ...headers })
  response.end(body)
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
