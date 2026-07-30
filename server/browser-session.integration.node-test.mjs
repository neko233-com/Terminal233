import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { generateKeyPairSync } from 'node:crypto'
import { once } from 'node:events'
import { posix as posixPath } from 'node:path'
import { fileURLToPath } from 'node:url'
import net from 'node:net'
import ssh2 from 'ssh2'
import test from 'node:test'

const { Server: SshServer } = ssh2
const { OPEN_MODE, STATUS_CODE } = ssh2.utils.sftp

const SFTP_PACKET = Object.freeze({
  INIT: 1,
  VERSION: 2,
  OPEN: 3,
  CLOSE: 4,
  WRITE: 6,
  LSTAT: 7,
  REMOVE: 13,
  RENAME: 18,
  EXTENDED: 200,
  STATUS: 101,
  HANDLE: 102,
  ATTRS: 105,
})
const MAX_SFTP_PACKET_BYTES = 256 * 1024
const POSIX_RENAME_EXTENSION = 'posix-rename@openssh.com'

async function availablePort() {
  const probe = net.createServer()
  probe.listen(0, '127.0.0.1')
  await once(probe, 'listening')
  const port = probe.address().port
  probe.close()
  await once(probe, 'close')
  return port
}

async function waitForBackend(baseUrl, child) {
  for (let attempt = 0; attempt < 80; attempt += 1) {
    if (child.exitCode !== null) throw new Error(`backend exited with ${child.exitCode}`)
    try {
      const response = await fetch(`${baseUrl}/healthz`)
      if (response.ok) return
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 50))
  }
  throw new Error('backend did not become ready')
}

function cookieFrom(response) {
  return response.headers.get('set-cookie')?.split(';', 1)[0] ?? ''
}

async function waitForCondition(predicate, message) {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (predicate()) return
    await new Promise((resolve) => setTimeout(resolve, 20))
  }
  throw new Error(message)
}

function createInMemorySftpFixture() {
  const now = Math.floor(Date.now() / 1000)
  const entries = new Map([
    ['/', { kind: 'directory', data: null, mtime: now }],
  ])
  const operations = new Set()
  let openedChannels = 0
  let closedChannels = 0

  const normalize = (remotePath) => {
    const normalized = posixPath.normalize(remotePath)
    return normalized.startsWith('/') ? normalized : `/${normalized}`
  }
  const parentOf = (remotePath) => {
    const parent = posixPath.dirname(remotePath)
    return parent === '.' ? '/' : parent
  }
  const attrsFor = (entry) => ({
    mode: entry.kind === 'directory' ? 0o040755 : 0o100644,
    size: entry.kind === 'directory' ? 0 : entry.data.length,
    uid: 1000,
    gid: 1000,
    atime: entry.mtime,
    mtime: entry.mtime,
  })
  const respondMissing = (sftp, requestId) => {
    sftp.status(requestId, STATUS_CODE.NO_SUCH_FILE)
  }

  const attach = (sftp) => {
    openedChannels += 1
    const handles = new Map()
    let nextHandleId = 1
    let channelClosed = false

    const finishChannel = () => {
      if (channelClosed) return
      channelClosed = true
      handles.clear()
      closedChannels += 1
    }
    sftp.once('close', finishChannel)

    const createHandle = (value) => {
      const handle = Buffer.allocUnsafe(4)
      handle.writeUInt32BE(nextHandleId, 0)
      handles.set(nextHandleId, value)
      nextHandleId += 1
      return handle
    }
    const resolveHandle = (handle) => {
      if (!Buffer.isBuffer(handle) || handle.length !== 4) return null
      return handles.get(handle.readUInt32BE(0)) ?? null
    }

    sftp.on('LSTAT', (requestId, rawPath) => {
      operations.add('LSTAT')
      const remotePath = normalize(rawPath)
      const entry = entries.get(remotePath)
      if (!entry) {
        respondMissing(sftp, requestId)
        return
      }
      sftp.attrs(requestId, attrsFor(entry))
    })

    sftp.on('OPENDIR', (requestId, rawPath) => {
      operations.add('OPENDIR')
      const remotePath = normalize(rawPath)
      const entry = entries.get(remotePath)
      if (!entry || entry.kind !== 'directory') {
        respondMissing(sftp, requestId)
        return
      }
      sftp.handle(requestId, createHandle({ kind: 'directory', remotePath, read: false }))
    })

    sftp.on('READDIR', (requestId, handle) => {
      operations.add('READDIR')
      const opened = resolveHandle(handle)
      if (!opened || opened.kind !== 'directory') {
        sftp.status(requestId, STATUS_CODE.FAILURE)
        return
      }
      if (opened.read) {
        sftp.status(requestId, STATUS_CODE.EOF)
        return
      }
      opened.read = true
      const prefix = opened.remotePath === '/' ? '/' : `${opened.remotePath}/`
      const listing = [...entries.entries()]
        .filter(([remotePath]) => (
          remotePath !== opened.remotePath
          && remotePath.startsWith(prefix)
          && !remotePath.slice(prefix.length).includes('/')
        ))
        .map(([remotePath, entry]) => ({
          filename: posixPath.basename(remotePath),
          longname: posixPath.basename(remotePath),
          attrs: attrsFor(entry),
        }))
      if (listing.length === 0) {
        sftp.status(requestId, STATUS_CODE.EOF)
        return
      }
      sftp.name(requestId, listing)
    })

    sftp.on('OPEN', (requestId, rawPath, flags) => {
      operations.add('OPEN')
      const remotePath = normalize(rawPath)
      const existing = entries.get(remotePath)
      const wantsRead = (flags & OPEN_MODE.READ) !== 0
      const wantsWrite = (flags & OPEN_MODE.WRITE) !== 0

      if (wantsWrite) {
        const parent = entries.get(parentOf(remotePath))
        if (!parent || parent.kind !== 'directory') {
          respondMissing(sftp, requestId)
          return
        }
        if (existing?.kind === 'directory' || (existing && (flags & OPEN_MODE.EXCL))) {
          sftp.status(requestId, STATUS_CODE.FAILURE)
          return
        }
        if (!existing && !(flags & OPEN_MODE.CREAT)) {
          respondMissing(sftp, requestId)
          return
        }
        const data = !existing || (flags & OPEN_MODE.TRUNC)
          ? Buffer.alloc(0)
          : Buffer.from(existing.data)
        entries.set(remotePath, { kind: 'file', data, mtime: now })
      } else if (!wantsRead || !existing || existing.kind !== 'file') {
        respondMissing(sftp, requestId)
        return
      }

      sftp.handle(requestId, createHandle({
        kind: 'file',
        remotePath,
        readable: wantsRead,
        writable: wantsWrite,
      }))
    })

    sftp.on('WRITE', (requestId, handle, rawOffset, data) => {
      operations.add('WRITE')
      const opened = resolveHandle(handle)
      const entry = opened ? entries.get(opened.remotePath) : null
      if (!opened?.writable || !entry || entry.kind !== 'file') {
        sftp.status(requestId, STATUS_CODE.FAILURE)
        return
      }
      const offset = Number(rawOffset)
      const requiredLength = offset + data.length
      if (!Number.isSafeInteger(offset) || offset < 0 || !Number.isSafeInteger(requiredLength)) {
        sftp.status(requestId, STATUS_CODE.FAILURE)
        return
      }
      if (requiredLength > entry.data.length) {
        const expanded = Buffer.alloc(requiredLength)
        entry.data.copy(expanded)
        entry.data = expanded
      }
      data.copy(entry.data, offset)
      entry.mtime = Math.floor(Date.now() / 1000)
      sftp.status(requestId, STATUS_CODE.OK)
    })

    sftp.on('READ', (requestId, handle, rawOffset, length) => {
      operations.add('READ')
      const opened = resolveHandle(handle)
      const entry = opened ? entries.get(opened.remotePath) : null
      if (!opened?.readable || !entry || entry.kind !== 'file') {
        sftp.status(requestId, STATUS_CODE.FAILURE)
        return
      }
      const offset = Number(rawOffset)
      if (!Number.isSafeInteger(offset) || offset < 0 || offset >= entry.data.length) {
        sftp.status(requestId, STATUS_CODE.EOF)
        return
      }
      sftp.data(requestId, entry.data.subarray(offset, Math.min(entry.data.length, offset + length)))
    })

    sftp.on('CLOSE', (requestId, handle) => {
      operations.add('CLOSE')
      if (!Buffer.isBuffer(handle) || handle.length !== 4 || !handles.delete(handle.readUInt32BE(0))) {
        sftp.status(requestId, STATUS_CODE.FAILURE)
        return
      }
      sftp.status(requestId, STATUS_CODE.OK)
    })

    sftp.on('MKDIR', (requestId, rawPath) => {
      operations.add('MKDIR')
      const remotePath = normalize(rawPath)
      const parent = entries.get(parentOf(remotePath))
      if (entries.has(remotePath) || !parent || parent.kind !== 'directory') {
        sftp.status(requestId, STATUS_CODE.FAILURE)
        return
      }
      entries.set(remotePath, { kind: 'directory', data: null, mtime: now })
      sftp.status(requestId, STATUS_CODE.OK)
    })

    sftp.on('RENAME', (requestId, rawFromPath, rawToPath) => {
      operations.add('RENAME')
      const fromPath = normalize(rawFromPath)
      const toPath = normalize(rawToPath)
      const entry = entries.get(fromPath)
      const parent = entries.get(parentOf(toPath))
      if (!entry || entries.has(toPath) || !parent || parent.kind !== 'directory') {
        sftp.status(requestId, STATUS_CODE.FAILURE)
        return
      }
      entries.delete(fromPath)
      entries.set(toPath, entry)
      sftp.status(requestId, STATUS_CODE.OK)
    })

    sftp.on('REMOVE', (requestId, rawPath) => {
      operations.add('REMOVE')
      const remotePath = normalize(rawPath)
      const entry = entries.get(remotePath)
      if (!entry || entry.kind !== 'file') {
        respondMissing(sftp, requestId)
        return
      }
      entries.delete(remotePath)
      sftp.status(requestId, STATUS_CODE.OK)
    })

    sftp.on('RMDIR', (requestId, rawPath) => {
      operations.add('RMDIR')
      const remotePath = normalize(rawPath)
      const entry = entries.get(remotePath)
      const prefix = `${remotePath}/`
      if (
        remotePath === '/'
        || !entry
        || entry.kind !== 'directory'
        || [...entries.keys()].some((candidate) => candidate.startsWith(prefix))
      ) {
        sftp.status(requestId, STATUS_CODE.FAILURE)
        return
      }
      entries.delete(remotePath)
      sftp.status(requestId, STATUS_CODE.OK)
    })
  }

  return {
    attach,
    entries,
    operations,
    get openedChannels() { return openedChannels },
    get closedChannels() { return closedChannels },
  }
}

function rawSftpUInt32(value) {
  const buffer = Buffer.allocUnsafe(4)
  buffer.writeUInt32BE(value, 0)
  return buffer
}

function rawSftpUInt64(value) {
  const buffer = Buffer.allocUnsafe(8)
  buffer.writeBigUInt64BE(BigInt(value), 0)
  return buffer
}

function rawSftpString(value) {
  const bytes = Buffer.isBuffer(value) ? value : Buffer.from(value, 'utf8')
  return Buffer.concat([rawSftpUInt32(bytes.length), bytes])
}

function rawSftpPacket(type, ...parts) {
  const payload = Buffer.concat([Buffer.from([type]), ...parts])
  assert.ok(payload.length <= MAX_SFTP_PACKET_BYTES, 'fixture SFTP packet must stay bounded')
  return Buffer.concat([rawSftpUInt32(payload.length), payload])
}

class RawSftpCursor {
  constructor(bytes) {
    this.bytes = bytes
    this.offset = 0
  }

  readUInt32() {
    if (this.offset + 4 > this.bytes.length) throw new Error('truncated SFTP uint32')
    const value = this.bytes.readUInt32BE(this.offset)
    this.offset += 4
    return value
  }

  readUInt64() {
    if (this.offset + 8 > this.bytes.length) throw new Error('truncated SFTP uint64')
    const value = this.bytes.readBigUInt64BE(this.offset)
    this.offset += 8
    if (value > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error('oversized SFTP uint64')
    return Number(value)
  }

  readString() {
    const length = this.readUInt32()
    if (this.offset + length > this.bytes.length) throw new Error('truncated SFTP string')
    const value = this.bytes.subarray(this.offset, this.offset + length)
    this.offset += length
    return value
  }
}

function createAtomicOverwriteSftpFixture() {
  const now = Math.floor(Date.now() / 1000)
  const entries = new Map([
    ['/', { kind: 'directory', data: null, mtime: now }],
    ['/workspace', { kind: 'directory', data: null, mtime: now }],
    ['/workspace/upload-target.bin', {
      kind: 'file',
      data: Buffer.from('old-upload'),
      mtime: now,
    }],
    ['/workspace/rename-source.bin', {
      kind: 'file',
      data: Buffer.from('new-rename'),
      mtime: now,
    }],
    ['/workspace/rename-target.bin', {
      kind: 'file',
      data: Buffer.from('old-rename'),
      mtime: now,
    }],
    ['/workspace/permission-source.bin', {
      kind: 'file',
      data: Buffer.from('permission-source'),
      mtime: now,
    }],
    ['/workspace/permission-denied.bin', {
      kind: 'file',
      data: Buffer.from('permission-target'),
      mtime: now,
    }],
    ['/workspace/unsupported-upload.bin', {
      kind: 'file',
      data: Buffer.from('old-unsupported-upload'),
      mtime: now,
    }],
    ['/workspace/unsupported-source.bin', {
      kind: 'file',
      data: Buffer.from('unsupported-source'),
      mtime: now,
    }],
    ['/workspace/unsupported-target.bin', {
      kind: 'file',
      data: Buffer.from('unsupported-target'),
      mtime: now,
    }],
  ])
  let advertisePosixRename = true
  let rejectPosixRename = false
  let openedChannels = 0
  let closedChannels = 0
  let atomicRequests = 0
  let unsupportedAtomicRequests = 0
  let ordinaryRenameRequests = 0
  let permissionDeniedLstats = 0
  let removeRequests = 0

  const normalize = (rawPath) => {
    const value = Buffer.isBuffer(rawPath) ? rawPath.toString('utf8') : String(rawPath)
    if (!value.startsWith('/') || value.includes('\0') || value.includes('\\')) return null
    const normalized = posixPath.normalize(value)
    if (normalized.split('/').includes('..')) return null
    return normalized
  }
  const parentOf = (remotePath) => posixPath.dirname(remotePath)
  const attrsFor = (entry) => ({
    mode: entry.kind === 'directory' ? 0o040755 : 0o100644,
    size: entry.kind === 'directory' ? 0 : entry.data.length,
    uid: 1000,
    gid: 1000,
    atime: entry.mtime,
    mtime: entry.mtime,
  })
  const encodeAttrs = (entry) => {
    const attrs = attrsFor(entry)
    return Buffer.concat([
      rawSftpUInt32(0x0f),
      rawSftpUInt64(attrs.size),
      rawSftpUInt32(attrs.uid),
      rawSftpUInt32(attrs.gid),
      rawSftpUInt32(attrs.mode),
      rawSftpUInt32(attrs.atime),
      rawSftpUInt32(attrs.mtime),
    ])
  }

  const attach = (channel) => {
    openedChannels += 1
    let channelClosed = false
    let pending = Buffer.alloc(0)
    let nextHandle = 1
    const handles = new Map()
    const finish = () => {
      if (channelClosed) return
      channelClosed = true
      handles.clear()
      closedChannels += 1
    }
    channel.once('close', finish)

    const send = (type, ...parts) => channel.write(rawSftpPacket(type, ...parts))
    const status = (requestId, code, message = '') => send(
      SFTP_PACKET.STATUS,
      rawSftpUInt32(requestId),
      rawSftpUInt32(code),
      rawSftpString(message),
      rawSftpString(''),
    )
    const allocateHandle = (record) => {
      const value = Buffer.allocUnsafe(4)
      value.writeUInt32BE(nextHandle, 0)
      handles.set(nextHandle, record)
      nextHandle += 1
      return value
    }
    const resolveHandle = (value) => {
      if (!Buffer.isBuffer(value) || value.length !== 4) return null
      return handles.get(value.readUInt32BE(0)) ?? null
    }
    const deleteHandle = (value) => (
      Buffer.isBuffer(value)
      && value.length === 4
      && handles.delete(value.readUInt32BE(0))
    )

    const dispatch = (packet) => {
      const type = packet[0]
      const cursor = new RawSftpCursor(packet.subarray(1))
      if (type === SFTP_PACKET.INIT) {
        assert.equal(cursor.readUInt32(), 3)
        const extension = advertisePosixRename
          ? [rawSftpString(POSIX_RENAME_EXTENSION), rawSftpString('1')]
          : []
        send(SFTP_PACKET.VERSION, rawSftpUInt32(3), ...extension)
        return
      }

      const requestId = cursor.readUInt32()
      if (type === SFTP_PACKET.LSTAT) {
        const remotePath = normalize(cursor.readString())
        if (remotePath === '/workspace/permission-denied.bin') {
          permissionDeniedLstats += 1
          status(requestId, STATUS_CODE.PERMISSION_DENIED, 'permission denied by fixture')
          return
        }
        const entry = remotePath ? entries.get(remotePath) : null
        if (!entry) {
          status(requestId, STATUS_CODE.NO_SUCH_FILE)
          return
        }
        send(SFTP_PACKET.ATTRS, rawSftpUInt32(requestId), encodeAttrs(entry))
        return
      }
      if (type === SFTP_PACKET.OPEN) {
        const remotePath = normalize(cursor.readString())
        const flags = cursor.readUInt32()
        const existing = remotePath ? entries.get(remotePath) : null
        const parent = remotePath ? entries.get(parentOf(remotePath)) : null
        if (
          !remotePath
          || (flags & OPEN_MODE.WRITE) === 0
          || (flags & OPEN_MODE.CREAT) === 0
          || !parent
          || parent.kind !== 'directory'
          || existing
        ) {
          status(requestId, STATUS_CODE.FAILURE)
          return
        }
        entries.set(remotePath, { kind: 'file', data: Buffer.alloc(0), mtime: now })
        send(
          SFTP_PACKET.HANDLE,
          rawSftpUInt32(requestId),
          rawSftpString(allocateHandle({ remotePath, writable: true })),
        )
        return
      }
      if (type === SFTP_PACKET.WRITE) {
        const opened = resolveHandle(cursor.readString())
        const offset = cursor.readUInt64()
        const chunk = cursor.readString()
        const entry = opened ? entries.get(opened.remotePath) : null
        if (!opened?.writable || !entry || entry.kind !== 'file') {
          status(requestId, STATUS_CODE.FAILURE)
          return
        }
        const requiredLength = offset + chunk.length
        const data = requiredLength > entry.data.length
          ? Buffer.alloc(requiredLength)
          : Buffer.from(entry.data)
        entry.data.copy(data)
        chunk.copy(data, offset)
        entry.data = data
        status(requestId, STATUS_CODE.OK)
        return
      }
      if (type === SFTP_PACKET.CLOSE) {
        if (!deleteHandle(cursor.readString())) {
          status(requestId, STATUS_CODE.FAILURE)
          return
        }
        status(requestId, STATUS_CODE.OK)
        return
      }
      if (type === SFTP_PACKET.REMOVE) {
        removeRequests += 1
        const remotePath = normalize(cursor.readString())
        const entry = remotePath ? entries.get(remotePath) : null
        if (!remotePath || !entry || entry.kind !== 'file') {
          status(requestId, STATUS_CODE.NO_SUCH_FILE)
          return
        }
        entries.delete(remotePath)
        status(requestId, STATUS_CODE.OK)
        return
      }
      if (type === SFTP_PACKET.RENAME) {
        ordinaryRenameRequests += 1
        const fromPath = normalize(cursor.readString())
        const toPath = normalize(cursor.readString())
        const entry = fromPath ? entries.get(fromPath) : null
        const parent = toPath ? entries.get(parentOf(toPath)) : null
        if (!fromPath || !toPath || !entry || entries.has(toPath) || parent?.kind !== 'directory') {
          status(requestId, STATUS_CODE.FAILURE)
          return
        }
        entries.delete(fromPath)
        entries.set(toPath, entry)
        status(requestId, STATUS_CODE.OK)
        return
      }
      if (type === SFTP_PACKET.EXTENDED) {
        const extension = cursor.readString().toString('utf8')
        if (extension !== POSIX_RENAME_EXTENSION) {
          unsupportedAtomicRequests += 1
          status(requestId, STATUS_CODE.OP_UNSUPPORTED)
          return
        }
        const fromPath = normalize(cursor.readString())
        const toPath = normalize(cursor.readString())
        if (!fromPath || !toPath || rejectPosixRename) {
          unsupportedAtomicRequests += 1
          status(requestId, STATUS_CODE.OP_UNSUPPORTED)
          return
        }
        const entry = entries.get(fromPath)
        const parent = entries.get(parentOf(toPath))
        if (!entry || parent?.kind !== 'directory') {
          status(requestId, STATUS_CODE.NO_SUCH_FILE)
          return
        }
        entries.delete(fromPath)
        entries.set(toPath, entry)
        atomicRequests += 1
        status(requestId, STATUS_CODE.OK)
        return
      }
      status(requestId, STATUS_CODE.OP_UNSUPPORTED)
    }

    channel.on('data', (chunk) => {
      pending = Buffer.concat([pending, chunk])
      try {
        while (pending.length >= 4) {
          const packetLength = pending.readUInt32BE(0)
          if (packetLength < 1 || packetLength > MAX_SFTP_PACKET_BYTES) {
            throw new Error(`invalid SFTP packet length ${packetLength}`)
          }
          if (pending.length < packetLength + 4) return
          const packet = pending.subarray(4, packetLength + 4)
          pending = pending.subarray(packetLength + 4)
          dispatch(packet)
        }
      } catch {
        channel.destroy()
      }
    })
  }

  return {
    attach,
    entries,
    set advertisePosixRename(value) { advertisePosixRename = value },
    set rejectPosixRename(value) { rejectPosixRename = value },
    get openedChannels() { return openedChannels },
    get closedChannels() { return closedChannels },
    get atomicRequests() { return atomicRequests },
    get unsupportedAtomicRequests() { return unsupportedAtomicRequests },
    get ordinaryRenameRequests() { return ordinaryRenameRequests },
    get permissionDeniedLstats() { return permissionDeniedLstats },
    get removeRequests() { return removeRequests },
  }
}

test('browser session rotation cleans owner resources and stale release is harmless', { timeout: 20_000 }, async () => {
  const backendPort = await availablePort()
  const frontendPort = await availablePort()
  const origin = `http://127.0.0.1:${frontendPort}`
  const baseUrl = `http://127.0.0.1:${backendPort}`
  const child = spawn(process.execPath, [fileURLToPath(new URL('./ssh2js-server.mjs', import.meta.url))], {
    env: {
      ...process.env,
      NEKOSSH_BACKEND_PORT: String(backendPort),
      NEKOSSH_FRONTEND_PORT: String(frontendPort),
      NEKOSSH_MCP_TOKEN: 'integration-test-token-000000000000',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  })

  try {
    await waitForBackend(baseUrl, child)
    const firstSession = await fetch(`${baseUrl}/api/browser-session`, { method: 'POST', headers: { origin } })
    assert.equal(firstSession.status, 200)
    const firstCookie = cookieFrom(firstSession)
    const firstRelease = (await firstSession.json()).releaseId

    const create = (cookie, host) => fetch(`${baseUrl}/api/connections`, {
      method: 'POST',
      headers: { origin, cookie, 'content-type': 'application/json' },
      body: JSON.stringify({
        label: host,
        host,
        port: 22,
        username: 'tester',
        authentication: { kind: 'password', password: 'temporary-test-password' },
      }),
    })
    const list = (cookie) => fetch(`${baseUrl}/api/connections`, { headers: { origin, cookie } })

    assert.equal((await create(firstCookie, 'host-one.test')).status, 201)
    const invalid = await fetch(`${baseUrl}/api/connections`, {
      method: 'POST',
      headers: { origin, cookie: firstCookie, 'content-type': 'application/json' },
      body: JSON.stringify({ label: 'invalid', host: '', port: 22, username: 'tester', authentication: { kind: 'password', password: '' } }),
    })
    assert.equal(invalid.status, 400)
    assert.equal((await (await list(firstCookie)).json()).length, 1)

    const invalidPort = await fetch(`${baseUrl}/api/connections`, {
      method: 'POST',
      headers: { origin, cookie: firstCookie, 'content-type': 'application/json' },
      body: JSON.stringify({
        label: 'invalid-port',
        host: 'host.test',
        port: 0,
        username: 'tester',
        authentication: { kind: 'password', password: 'temporary-test-password' },
      }),
    })
    assert.equal(invalidPort.status, 400)
    assert.equal((await (await list(firstCookie)).json()).length, 1)

    assert.equal((await create(firstCookie, 'host-two.test')).status, 201)
    const replaced = await (await list(firstCookie)).json()
    assert.deepEqual(replaced.map((item) => item.host), ['host-two.test'])

    const secondSession = await fetch(`${baseUrl}/api/browser-session`, {
      method: 'POST',
      headers: { origin, cookie: firstCookie },
    })
    const secondCookie = cookieFrom(secondSession)
    const secondRelease = (await secondSession.json()).releaseId
    assert.equal((await (await list(secondCookie)).json()).length, 0)

    const staleClose = await fetch(`${baseUrl}/api/browser-session/close`, {
      method: 'POST',
      headers: { origin, cookie: secondCookie, 'content-type': 'application/json' },
      body: JSON.stringify({ releaseId: firstRelease }),
    })
    assert.equal(staleClose.status, 204)
    assert.equal((await list(secondCookie)).status, 200)

    const matchingClose = await fetch(`${baseUrl}/api/browser-session/close`, {
      method: 'POST',
      headers: { origin, cookie: secondCookie, 'content-type': 'application/json' },
      body: JSON.stringify({ releaseId: secondRelease }),
    })
    assert.equal(matchingClose.status, 204)
    assert.equal((await list(secondCookie)).status, 401)
  } finally {
    child.kill()
    if (child.exitCode === null) await once(child, 'exit')
  }
})

test('Web ssh2 runtime completes real in-memory SFTP lifecycle and releases the SSH transport', { timeout: 25_000 }, async () => {
  const backendPort = await availablePort()
  const frontendPort = await availablePort()
  const sshPort = await availablePort()
  const origin = `http://127.0.0.1:${frontendPort}`
  const baseUrl = `http://127.0.0.1:${backendPort}`
  const { privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
  const fixture = createInMemorySftpFixture()
  const serverClients = new Set()
  const sshServer = new SshServer({
    hostKeys: [privateKey.export({ type: 'pkcs1', format: 'pem' })],
  }, (client) => {
    serverClients.add(client)
    client.on('error', () => {})
    client.once('close', () => serverClients.delete(client))
    client.on('authentication', (context) => {
      if (
        context.method === 'password'
        && context.username === 'tester'
        && Buffer.from(context.password).equals(Buffer.from('fixture-password'))
      ) {
        context.accept()
        return
      }
      context.reject()
    })
    client.on('ready', () => {
      client.on('session', (accept) => {
        const session = accept()
        session.on('sftp', (acceptSftp) => fixture.attach(acceptSftp()))
      })
    })
  })
  sshServer.listen(sshPort, '127.0.0.1')
  await once(sshServer, 'listening')

  const child = spawn(process.execPath, [fileURLToPath(new URL('./ssh2js-server.mjs', import.meta.url))], {
    env: {
      ...process.env,
      NEKOSSH_BACKEND_PORT: String(backendPort),
      NEKOSSH_FRONTEND_PORT: String(frontendPort),
      NEKOSSH_MCP_TOKEN: 'integration-test-token-000000000000',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  })

  try {
    await waitForBackend(baseUrl, child)
    const browserSession = await fetch(`${baseUrl}/api/browser-session`, {
      method: 'POST',
      headers: { origin },
    })
    assert.equal(browserSession.status, 200)
    const cookie = cookieFrom(browserSession)
    const { releaseId } = await browserSession.json()
    const api = (resource, init = {}) => fetch(`${baseUrl}${resource}`, {
      ...init,
      headers: { origin, cookie, ...init.headers },
    })
    const postJson = (resource, body) => api(resource, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(body),
    })

    const created = await postJson('/api/connections', {
      label: 'SFTP fixture',
      host: '127.0.0.1',
      port: sshPort,
      username: 'tester',
      authentication: { kind: 'password', password: 'fixture-password' },
    })
    assert.equal(created.status, 201)
    const connection = await created.json()

    const prepared = await api(`/api/connections/${connection.id}/prepare`, { method: 'POST' })
    assert.equal(prepared.status, 200)
    const confirmation = await prepared.json()
    assert.equal(confirmation.status, 'confirmation_required')
    const trusted = await postJson(`/api/connections/${connection.id}/trust`, {
      confirmationId: confirmation.confirmationId,
    })
    assert.equal(trusted.status, 200)
    assert.equal((await trusted.json()).status, 'trusted')
    await waitForCondition(
      () => serverClients.size === 1,
      `expected one authenticated SSH transport, found ${serverClients.size}`,
    )

    const mkdir = await postJson(`/api/connections/${connection.id}/files/mkdir`, {
      path: '/workspace',
    })
    assert.equal(mkdir.status, 204)

    const rootListingResponse = await api(
      `/api/connections/${connection.id}/files?path=${encodeURIComponent('/')}`,
    )
    assert.equal(rootListingResponse.status, 200)
    const rootListing = await rootListingResponse.json()
    assert.equal(rootListing.path, '/')
    assert.equal(rootListing.truncated, false)
    assert.deepEqual(rootListing.entries.map(({ name, isDirectory }) => ({ name, isDirectory })), [
      { name: 'workspace', isDirectory: true },
    ])

    const filename = '猫咪.bin'
    const uploadedBytes = Buffer.concat([
      Buffer.from('NekoSSH SFTP\n', 'utf8'),
      Buffer.from([0, 1, 2, 127, 128, 254, 255]),
      Buffer.from('真实字节', 'utf8'),
    ])
    const upload = await postJson(`/api/connections/${connection.id}/files/upload`, {
      remoteDirectory: '/workspace',
      files: [{
        name: filename,
        dataBase64: uploadedBytes.toString('base64'),
        overwrite: false,
      }],
    })
    assert.equal(upload.status, 200)
    assert.deepEqual(await upload.json(), { uploaded: 1, bytes: uploadedBytes.length })

    const workspaceListingResponse = await api(
      `/api/connections/${connection.id}/files?path=${encodeURIComponent('/workspace')}`,
    )
    assert.equal(workspaceListingResponse.status, 200)
    const workspaceListing = await workspaceListingResponse.json()
    assert.deepEqual(workspaceListing.entries.map(({ name, isDirectory, size }) => ({
      name,
      isDirectory,
      size,
    })), [{
      name: filename,
      isDirectory: false,
      size: uploadedBytes.length,
    }])

    const originalPath = `/workspace/${filename}`
    const download = await api(
      `/api/connections/${connection.id}/files/download?path=${encodeURIComponent(originalPath)}`,
    )
    assert.equal(download.status, 200)
    assert.equal(download.headers.get('content-type'), 'application/octet-stream')
    assert.deepEqual(Buffer.from(await download.arrayBuffer()), uploadedBytes)

    const renamedPath = '/workspace/renamed.bin'
    const rename = await postJson(`/api/connections/${connection.id}/files/rename`, {
      fromPath: originalPath,
      toPath: renamedPath,
      overwrite: false,
    })
    assert.equal(rename.status, 204)
    assert.equal(fixture.entries.has(originalPath), false)
    assert.deepEqual(fixture.entries.get(renamedPath)?.data, uploadedBytes)

    const prepareDelete = async (remotePath) => {
      const response = await postJson(`/api/connections/${connection.id}/files/delete-prepare`, {
        path: remotePath,
      })
      assert.equal(response.status, 200)
      return (await response.json()).confirmationId
    }
    const deleteFile = await postJson(`/api/connections/${connection.id}/files/delete-commit`, {
      confirmationId: await prepareDelete(renamedPath),
    })
    assert.equal(deleteFile.status, 204)
    assert.equal(fixture.entries.has(renamedPath), false)

    const deleteDirectory = await postJson(`/api/connections/${connection.id}/files/delete-commit`, {
      confirmationId: await prepareDelete('/workspace'),
    })
    assert.equal(deleteDirectory.status, 204)
    assert.deepEqual([...fixture.entries.keys()], ['/'])

    const emptyRootResponse = await api(
      `/api/connections/${connection.id}/files?path=${encodeURIComponent('/')}`,
    )
    assert.equal(emptyRootResponse.status, 200)
    assert.deepEqual((await emptyRootResponse.json()).entries, [])

    await waitForCondition(
      () => fixture.openedChannels === fixture.closedChannels,
      `SFTP channel leak: opened ${fixture.openedChannels}, closed ${fixture.closedChannels}`,
    )
    assert.ok(fixture.openedChannels >= 8)
    assert.deepEqual(
      [...fixture.operations].sort(),
      ['CLOSE', 'LSTAT', 'MKDIR', 'OPEN', 'OPENDIR', 'READ', 'READDIR', 'REMOVE', 'RENAME', 'RMDIR', 'WRITE'],
    )

    const closeSession = await postJson('/api/browser-session/close', { releaseId })
    assert.equal(closeSession.status, 204)
    await waitForCondition(
      () => serverClients.size === 0,
      `browser session close left ${serverClients.size} SSH transport(s) open`,
    )
    assert.equal((await api('/api/connections')).status, 401)
  } finally {
    if (child.exitCode === null && child.signalCode === null) {
      const backendClosed = once(child, 'close')
      child.kill()
      await backendClosed
    }
    for (const client of serverClients) {
      try { client.end() } catch {}
    }
    const serverClosed = once(sshServer, 'close')
    sshServer.close()
    await serverClosed
  }
})

test('Web SFTP v3 overwrite is fail-closed and uses only the OpenSSH POSIX rename extension', { timeout: 25_000 }, async () => {
  const backendPort = await availablePort()
  const frontendPort = await availablePort()
  const sshPort = await availablePort()
  const origin = `http://127.0.0.1:${frontendPort}`
  const baseUrl = `http://127.0.0.1:${backendPort}`
  const { privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
  const fixture = createAtomicOverwriteSftpFixture()
  const serverClients = new Set()
  const sshServer = new SshServer({
    hostKeys: [privateKey.export({ type: 'pkcs1', format: 'pem' })],
  }, (client) => {
    serverClients.add(client)
    client.on('error', () => {})
    client.once('close', () => serverClients.delete(client))
    client.on('authentication', (context) => {
      if (
        context.method === 'password'
        && context.username === 'tester'
        && Buffer.from(context.password).equals(Buffer.from('fixture-password'))
      ) {
        context.accept()
        return
      }
      context.reject()
    })
    client.on('ready', () => {
      client.on('session', (accept) => {
        const session = accept()
        session.on('subsystem', (acceptSubsystem, rejectSubsystem, info) => {
          if (info.name !== 'sftp') {
            rejectSubsystem()
            return
          }
          fixture.attach(acceptSubsystem())
        })
      })
    })
  })
  sshServer.listen(sshPort, '127.0.0.1')
  await once(sshServer, 'listening')

  const child = spawn(process.execPath, [fileURLToPath(new URL('./ssh2js-server.mjs', import.meta.url))], {
    env: {
      ...process.env,
      NEKOSSH_BACKEND_PORT: String(backendPort),
      NEKOSSH_FRONTEND_PORT: String(frontendPort),
      NEKOSSH_MCP_TOKEN: 'integration-test-token-000000000000',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  })

  try {
    await waitForBackend(baseUrl, child)
    const browserSession = await fetch(`${baseUrl}/api/browser-session`, {
      method: 'POST',
      headers: { origin },
    })
    assert.equal(browserSession.status, 200)
    const cookie = cookieFrom(browserSession)
    const { releaseId } = await browserSession.json()
    const api = (resource, init = {}) => fetch(`${baseUrl}${resource}`, {
      ...init,
      headers: { origin, cookie, ...init.headers },
    })
    const postJson = (resource, body) => api(resource, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(body),
    })

    const created = await postJson('/api/connections', {
      label: 'SFTP atomic overwrite fixture',
      host: '127.0.0.1',
      port: sshPort,
      username: 'tester',
      authentication: { kind: 'password', password: 'fixture-password' },
    })
    assert.equal(created.status, 201)
    const connection = await created.json()
    const prepared = await api(`/api/connections/${connection.id}/prepare`, { method: 'POST' })
    const confirmation = await prepared.json()
    assert.equal(confirmation.status, 'confirmation_required')
    const trusted = await postJson(`/api/connections/${connection.id}/trust`, {
      confirmationId: confirmation.confirmationId,
    })
    assert.equal(trusted.status, 200)

    const oldUpload = Buffer.from('old-upload')
    const noOverwriteUpload = await postJson(
      `/api/connections/${connection.id}/files/upload`,
      {
        remoteDirectory: '/workspace',
        files: [{
          name: 'upload-target.bin',
          dataBase64: Buffer.from('must-not-replace').toString('base64'),
          overwrite: false,
        }],
      },
    )
    assert.equal(noOverwriteUpload.status, 409)
    assert.equal((await noOverwriteUpload.json()).code, 'destination_exists')
    assert.deepEqual(fixture.entries.get('/workspace/upload-target.bin')?.data, oldUpload)

    const noOverwriteRename = await postJson(
      `/api/connections/${connection.id}/files/rename`,
      {
        fromPath: '/workspace/rename-source.bin',
        toPath: '/workspace/rename-target.bin',
        overwrite: false,
      },
    )
    assert.equal(noOverwriteRename.status, 409)
    assert.equal((await noOverwriteRename.json()).code, 'destination_exists')
    assert.deepEqual(
      fixture.entries.get('/workspace/rename-source.bin')?.data,
      Buffer.from('new-rename'),
    )
    assert.deepEqual(
      fixture.entries.get('/workspace/rename-target.bin')?.data,
      Buffer.from('old-rename'),
    )

    const deniedRename = await postJson(
      `/api/connections/${connection.id}/files/rename`,
      {
        fromPath: '/workspace/permission-source.bin',
        toPath: '/workspace/permission-denied.bin',
        overwrite: false,
      },
    )
    assert.equal(deniedRename.status, 502)
    assert.equal((await deniedRename.json()).code, 'sftp_failed')
    assert.equal(fixture.permissionDeniedLstats, 1)
    assert.deepEqual(
      fixture.entries.get('/workspace/permission-source.bin')?.data,
      Buffer.from('permission-source'),
    )
    assert.deepEqual(
      fixture.entries.get('/workspace/permission-denied.bin')?.data,
      Buffer.from('permission-target'),
    )

    const replacementUpload = Buffer.from('atomic-upload')
    const overwriteUpload = await postJson(
      `/api/connections/${connection.id}/files/upload`,
      {
        remoteDirectory: '/workspace',
        files: [{
          name: 'upload-target.bin',
          dataBase64: replacementUpload.toString('base64'),
          overwrite: true,
        }],
      },
    )
    assert.equal(overwriteUpload.status, 200)
    assert.deepEqual(
      fixture.entries.get('/workspace/upload-target.bin')?.data,
      replacementUpload,
    )

    const overwriteRename = await postJson(
      `/api/connections/${connection.id}/files/rename`,
      {
        fromPath: '/workspace/rename-source.bin',
        toPath: '/workspace/rename-target.bin',
        overwrite: true,
      },
    )
    assert.equal(overwriteRename.status, 204)
    assert.equal(fixture.entries.has('/workspace/rename-source.bin'), false)
    assert.deepEqual(
      fixture.entries.get('/workspace/rename-target.bin')?.data,
      Buffer.from('new-rename'),
    )
    assert.equal(fixture.atomicRequests, 2)

    fixture.advertisePosixRename = false
    const unsupportedUpload = await postJson(
      `/api/connections/${connection.id}/files/upload`,
      {
        remoteDirectory: '/workspace',
        files: [{
          name: 'unsupported-upload.bin',
          dataBase64: Buffer.from('must-not-replace').toString('base64'),
          overwrite: true,
        }],
      },
    )
    assert.equal(unsupportedUpload.status, 409)
    const unsupportedUploadError = await unsupportedUpload.json()
    assert.equal(unsupportedUploadError.code, 'sftp_atomic_overwrite_unsupported')
    assert.match(unsupportedUploadError.message, /不支持安全原子覆盖/u)
    assert.deepEqual(
      fixture.entries.get('/workspace/unsupported-upload.bin')?.data,
      Buffer.from('old-unsupported-upload'),
    )
    assert.ok(fixture.removeRequests >= 1, 'failed upload must remove its temporary object')

    fixture.advertisePosixRename = true
    fixture.rejectPosixRename = true
    const unsupportedRename = await postJson(
      `/api/connections/${connection.id}/files/rename`,
      {
        fromPath: '/workspace/unsupported-source.bin',
        toPath: '/workspace/unsupported-target.bin',
        overwrite: true,
      },
    )
    assert.equal(unsupportedRename.status, 409)
    const unsupportedRenameError = await unsupportedRename.json()
    assert.equal(unsupportedRenameError.code, 'sftp_atomic_overwrite_unsupported')
    assert.match(unsupportedRenameError.message, /原文件已保留/u)
    assert.equal(fixture.unsupportedAtomicRequests, 1)
    assert.deepEqual(
      fixture.entries.get('/workspace/unsupported-source.bin')?.data,
      Buffer.from('unsupported-source'),
    )
    assert.deepEqual(
      fixture.entries.get('/workspace/unsupported-target.bin')?.data,
      Buffer.from('unsupported-target'),
    )

    assert.equal(
      fixture.ordinaryRenameRequests,
      0,
      'overwrite must never fall back to ordinary SSH_FXP_RENAME v3',
    )
    assert.equal(
      [...fixture.entries.keys()].some((remotePath) => remotePath.endsWith('.part')),
      false,
      'failed and successful overwrite uploads must clean every .part object',
    )
    await waitForCondition(
      () => fixture.openedChannels === fixture.closedChannels,
      `atomic SFTP channel leak: opened ${fixture.openedChannels}, closed ${fixture.closedChannels}`,
    )

    const closeSession = await postJson('/api/browser-session/close', { releaseId })
    assert.equal(closeSession.status, 204)
    await waitForCondition(
      () => serverClients.size === 0,
      `browser session close left ${serverClients.size} SSH transport(s) open`,
    )
  } finally {
    if (child.exitCode === null && child.signalCode === null) {
      const backendClosed = once(child, 'close')
      child.kill()
      await backendClosed
    }
    for (const client of serverClients) {
      try { client.end() } catch {}
    }
    const serverClosed = once(sshServer, 'close')
    sshServer.close()
    await serverClosed
  }
})

test('rejected shell channel leaves the authenticated SSH transport and first terminal alive', { timeout: 20_000 }, async () => {
  const backendPort = await availablePort()
  const frontendPort = await availablePort()
  const sshPort = await availablePort()
  const origin = `http://127.0.0.1:${frontendPort}`
  const baseUrl = `http://127.0.0.1:${backendPort}`
  const { privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
  const { privateKey: clientPrivateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
  const clientKeyPassphrase = 'fixture-key-passphrase'
  const clientPrivateKeyPem = clientPrivateKey.export({
    type: 'pkcs1',
    format: 'pem',
    cipher: 'aes-256-cbc',
    passphrase: clientKeyPassphrase,
  })
  const clientParsedKey = ssh2.utils.parseKey(clientPrivateKeyPem, clientKeyPassphrase)
  assert.ok(!(clientParsedKey instanceof Error))
  let transportCount = 0
  let shellCount = 0
  let resolveExecStarted
  const execStarted = new Promise((resolve) => { resolveExecStarted = resolve })
  const sshServer = new SshServer({
    hostKeys: [privateKey.export({ type: 'pkcs1', format: 'pem' })],
  }, (client) => {
    transportCount += 1
    client.on('error', () => {})
    client.on('authentication', (context) => {
      if (
        context.method === 'password'
        && context.username === 'tester'
        && Buffer.from(context.password).equals(Buffer.from('fixture-password'))
      ) {
        context.accept()
        return
      }
      if (
        context.method !== 'publickey'
        || context.username !== 'tester'
        || context.key.algo !== clientParsedKey.type
        || !Buffer.from(context.key.data).equals(clientParsedKey.getPublicSSH())
        || (
          context.signature
          && !clientParsedKey.verify(context.blob, context.signature, context.hashAlgo)
        )
      ) {
        context.reject()
        return
      }
      context.accept()
    })
    client.on('ready', () => {
      client.on('session', (accept) => {
        const session = accept()
        session.on('pty', (acceptPty) => acceptPty())
        session.on('exec', (acceptExec) => {
          acceptExec()
          resolveExecStarted()
        })
        session.on('shell', (acceptShell, rejectShell) => {
          shellCount += 1
          if (shellCount === 2) {
            rejectShell()
            return
          }
          const channel = acceptShell()
          channel.write(`terminal-${shellCount}-ready\n`)
          channel.on('data', (data) => channel.write(`echo:${data.toString('utf8')}`))
        })
      })
    })
  })
  sshServer.listen(sshPort, '127.0.0.1')
  await once(sshServer, 'listening')

  const child = spawn(process.execPath, [fileURLToPath(new URL('./ssh2js-server.mjs', import.meta.url))], {
    env: {
      ...process.env,
      NEKOSSH_BACKEND_PORT: String(backendPort),
      NEKOSSH_FRONTEND_PORT: String(frontendPort),
      NEKOSSH_MCP_TOKEN: 'integration-test-token-000000000000',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  })

  try {
    await waitForBackend(baseUrl, child)
    const browserSession = await fetch(`${baseUrl}/api/browser-session`, { method: 'POST', headers: { origin } })
    const cookie = cookieFrom(browserSession)
    const connectionResponse = await fetch(`${baseUrl}/api/connections`, {
      method: 'POST',
      headers: { origin, cookie, 'content-type': 'application/json' },
      body: JSON.stringify({
        label: 'fixture',
        host: '127.0.0.1',
        port: sshPort,
        username: 'tester',
        authentication: {
          kind: 'private_key',
          privateKeyPem: clientPrivateKeyPem,
          passphrase: clientKeyPassphrase,
        },
      }),
    })
    const connection = await connectionResponse.json()
    const prepare = await fetch(`${baseUrl}/api/connections/${connection.id}/prepare`, { method: 'POST', headers: { origin, cookie } })
    const confirmation = await prepare.json()
    assert.equal(confirmation.status, 'confirmation_required')
    const trusted = await fetch(`${baseUrl}/api/connections/${connection.id}/trust`, {
      method: 'POST',
      headers: { origin, cookie, 'content-type': 'application/json' },
      body: JSON.stringify({ confirmationId: confirmation.confirmationId }),
    })
    assert.equal(trusted.status, 200)

    const startTerminal = () => fetch(`${baseUrl}/api/connections/${connection.id}/terminal`, {
      method: 'POST',
      headers: { origin, cookie, 'content-type': 'application/json' },
      body: JSON.stringify({ columns: 100, rows: 30 }),
    })
    const first = await startTerminal()
    assert.equal(first.status, 201)
    const firstTerminal = await first.json()
    assert.equal((await startTerminal()).status, 502)

    const write = await fetch(`${baseUrl}/api/terminals/${firstTerminal.terminalId}/input`, {
      method: 'POST',
      headers: { origin, cookie, 'content-type': 'application/json' },
      body: JSON.stringify({ data: 'still-alive\n' }),
    })
    assert.equal(write.status, 204)
    await new Promise((resolve) => setTimeout(resolve, 30))
    const poll = await fetch(`${baseUrl}/api/terminals/${firstTerminal.terminalId}/poll?cursor=0`, { headers: { origin, cookie } })
    const pollPayload = await poll.json()
    assert.equal(pollPayload.truncated, false)
    assert.ok(pollPayload.chunks.every((chunk) => chunk.bytes === Buffer.from(chunk.dataBase64, 'base64').length))
    const output = pollPayload.chunks
      .map((chunk) => Buffer.from(chunk.dataBase64, 'base64'))
    const terminalOutput = Buffer.concat(output).toString('utf8')
    assert.match(terminalOutput, /echo:still-alive/)

    assert.equal((await startTerminal()).status, 201)
    assert.equal(transportCount, 2)

    const grantResponse = await fetch(`${baseUrl}/api/connections/${connection.id}/agent-grant`, { method: 'POST', headers: { origin, cookie } })
    const grant = await grantResponse.json()
    let requestId = 0
    const mcpCall = (name, arguments_) => fetch(`${baseUrl}/mcp`, {
      method: 'POST',
      headers: {
        authorization: 'Bearer integration-test-token-000000000000',
        'content-type': 'application/json',
        'mcp-protocol-version': '2025-11-25',
      },
      body: JSON.stringify({
        jsonrpc: '2.0',
        id: ++requestId,
        method: 'tools/call',
        params: { name, arguments: arguments_ },
      }),
    })
    const attached = await (await mcpCall('ssh_connection_attach', { grant: grant.grant })).json()
    const attachmentId = attached.result.structuredContent.attachmentId
    const runningExec = mcpCall('ssh_exec', { attachmentId, command: 'long-running-fixture' })
    await execStarted
    const closed = await (await mcpCall('ssh_connection_close', { attachmentId })).json()
    assert.equal(closed.result.structuredContent.closed, true)
    const cancelled = await (await runningExec).json()
    assert.equal(cancelled.result.isError, true)
    assert.equal((await startTerminal()).status, 201)
    assert.equal(transportCount, 2)

    const createPasswordConnection = async (password) => {
      const response = await fetch(`${baseUrl}/api/connections`, {
        method: 'POST',
        headers: { origin, cookie, 'content-type': 'application/json' },
        body: JSON.stringify({
          label: 'password-fixture',
          host: '127.0.0.1',
          port: sshPort,
          username: 'tester',
          authentication: { kind: 'password', password },
        }),
      })
      assert.equal(response.status, 201)
      return response.json()
    }
    const prepareAndTrust = async (connectionId) => {
      const prepared = await (await fetch(
        `${baseUrl}/api/connections/${connectionId}/prepare`,
        { method: 'POST', headers: { origin, cookie } },
      )).json()
      assert.equal(prepared.status, 'confirmation_required')
      return fetch(`${baseUrl}/api/connections/${connectionId}/trust`, {
        method: 'POST',
        headers: { origin, cookie, 'content-type': 'application/json' },
        body: JSON.stringify({ confirmationId: prepared.confirmationId }),
      })
    }

    const wrongPassword = await createPasswordConnection('wrong-password')
    const rejectedPassword = await prepareAndTrust(wrongPassword.id)
    assert.equal(rejectedPassword.status, 401)
    assert.equal((await rejectedPassword.json()).code, 'ssh_authentication_failed')

    const passwordConnection = await createPasswordConnection('fixture-password')
    assert.equal((await prepareAndTrust(passwordConnection.id)).status, 200)
    const passwordTerminal = await fetch(
      `${baseUrl}/api/connections/${passwordConnection.id}/terminal`,
      {
        method: 'POST',
        headers: { origin, cookie, 'content-type': 'application/json' },
        body: JSON.stringify({ columns: 100, rows: 30 }),
      },
    )
    assert.equal(passwordTerminal.status, 201)
  } finally {
    child.kill()
    if (child.exitCode === null) await once(child, 'exit')
    sshServer.close()
    await once(sshServer, 'close')
  }
})
