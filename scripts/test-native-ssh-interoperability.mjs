import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import {
  mkdtemp,
  rmdir,
  unlink,
  writeFile,
} from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import ssh2 from 'ssh2'

const {
  Server: SshServer,
} = ssh2
const { OPEN_MODE, STATUS_CODE } = ssh2.utils.sftp
SshServer.KEEPALIVE_CLIENT_INTERVAL = 0

const SFTP = Object.freeze({
  INIT: 1,
  VERSION: 2,
  OPEN: 3,
  CLOSE: 4,
  READ: 5,
  WRITE: 6,
  LSTAT: 7,
  OPENDIR: 11,
  READDIR: 12,
  REMOVE: 13,
  RENAME: 18,
  EXTENDED: 200,
  STATUS: 101,
  HANDLE: 102,
  DATA: 103,
  NAME: 104,
  ATTRS: 105,
})
const MAX_SFTP_PACKET_BYTES = 256 * 1024
const POSIX_RENAME_EXTENSION = 'posix-rename@openssh.com'

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const fixtureUsername = 'terminal233-fixture'
const fixturePassword = 'local-interoperability-password'
const fixtureKeyPassphrase = 'local-interoperability-key-passphrase'
const fixtureFileContent = Buffer.from('Terminal233 native SFTP fixture\n', 'utf8')
const fixtureTransferFileName = '猫咪-数据.bin'
const fixtureTransferPath = `/workspace/${fixtureTransferFileName}`
const fixtureTransferContent = Buffer.alloc(150_123)
for (let index = 0; index < fixtureTransferContent.length; index += 1) {
  fixtureTransferContent[index] = ((index * 31) + 7) % 256
}
const fixedTimestamp = 1_735_689_600

const directories = new Set(['/workspace'])
const files = new Map([['/workspace/hello.txt', fixtureFileContent]])
const openHandles = new Map()
let nextHandle = 1

const observations = {
  transports: 0,
  passwordAuthentications: 0,
  publicKeyAuthentications: 0,
  publicKeySignatureHashes: new Set(),
  successfulExecutions: 0,
  sftpSessions: 0,
  directoryReads: 0,
  uploadBytesWritten: 0,
  uploadTemporaryPaths: new Set(),
  uploadFinalVisibleDuringWrite: false,
  mutations: new Set(),
}

const overwriteObservations = {
  atomicRequests: 0,
  unsupportedAtomicRequests: 0,
  permissionDeniedLstats: 0,
  ordinaryRenameRequests: 0,
  temporaryPaths: new Set(),
}

let atomicInteropPhase = false

function normalizeRemotePath(value) {
  const decoded = Buffer.isBuffer(value) ? value.toString('utf8') : String(value)
  if (
    !decoded.startsWith('/')
    || decoded.includes('\0')
    || decoded.includes('\\')
    || decoded.split('/').includes('..')
  ) {
    return null
  }
  const normalized = path.posix.normalize(decoded)
  return normalized.length > 1 ? normalized.replace(/\/+$/u, '') : normalized
}

function parentDirectory(remotePath) {
  return path.posix.dirname(remotePath)
}

function basename(remotePath) {
  return path.posix.basename(remotePath)
}

function pathExists(remotePath) {
  return directories.has(remotePath) || files.has(remotePath)
}

function directoryIsEmpty(remotePath) {
  const prefix = `${remotePath}/`
  return ![...directories, ...files.keys()].some(
    (candidate) => candidate !== remotePath && candidate.startsWith(prefix),
  )
}

function attributesFor(remotePath) {
  if (directories.has(remotePath)) {
    return {
      mode: 0o040755,
      uid: 1000,
      gid: 1000,
      size: 0,
      atime: fixedTimestamp,
      mtime: fixedTimestamp,
    }
  }
  const content = files.get(remotePath)
  if (content) {
    return {
      mode: 0o100644,
      uid: 1000,
      gid: 1000,
      size: content.length,
      atime: fixedTimestamp,
      mtime: fixedTimestamp,
    }
  }
  return null
}

function allocateHandle(record) {
  const id = nextHandle
  nextHandle += 1
  const handle = Buffer.allocUnsafe(4)
  handle.writeUInt32BE(id, 0)
  openHandles.set(id, record)
  return handle
}

function recordForHandle(handle) {
  if (!Buffer.isBuffer(handle) || handle.length !== 4) return null
  return openHandles.get(handle.readUInt32BE(0)) ?? null
}

function closeHandle(handle) {
  if (!Buffer.isBuffer(handle) || handle.length !== 4) return false
  return openHandles.delete(handle.readUInt32BE(0))
}

function listDirectory(remotePath) {
  const prefix = remotePath === '/' ? '/' : `${remotePath}/`
  const entries = []
  for (const candidate of [...directories, ...files.keys()]) {
    if (candidate === remotePath || !candidate.startsWith(prefix)) continue
    const relative = candidate.slice(prefix.length)
    if (relative.length === 0 || relative.includes('/')) continue
    const attrs = attributesFor(candidate)
    entries.push({
      filename: relative,
      longname: `${directories.has(candidate) ? 'd' : '-'}rw-r--r-- 1 fixture fixture ${attrs.size} Jan 1 00:00 ${relative}`,
      attrs,
    })
  }
  return entries.sort((left, right) => left.filename.localeCompare(right.filename))
}

function uint32(value) {
  const buffer = Buffer.allocUnsafe(4)
  buffer.writeUInt32BE(value, 0)
  return buffer
}

function uint64(value) {
  const buffer = Buffer.allocUnsafe(8)
  buffer.writeBigUInt64BE(BigInt(value), 0)
  return buffer
}

function sftpString(value) {
  const bytes = Buffer.isBuffer(value) ? value : Buffer.from(value, 'utf8')
  return Buffer.concat([uint32(bytes.length), bytes])
}

function sftpPacket(type, ...parts) {
  const payload = Buffer.concat([Buffer.from([type]), ...parts])
  assert.ok(payload.length <= MAX_SFTP_PACKET_BYTES, 'fixture SFTP packet must stay bounded')
  return Buffer.concat([uint32(payload.length), payload])
}

function sftpAttributes(attrs) {
  return Buffer.concat([
    uint32(0x0f),
    uint64(attrs.size),
    uint32(attrs.uid),
    uint32(attrs.gid),
    uint32(attrs.mode),
    uint32(attrs.atime),
    uint32(attrs.mtime),
  ])
}

class SftpRequestCursor {
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

function configureAtomicSftp(channel) {
  const send = (type, ...parts) => channel.write(sftpPacket(type, ...parts))
  const status = (requestId, code, message = '') => send(
    SFTP.STATUS,
    uint32(requestId),
    uint32(code),
    sftpString(message),
    sftpString(''),
  )
  const handle = (requestId, value) => send(SFTP.HANDLE, uint32(requestId), sftpString(value))
  const data = (requestId, value) => send(SFTP.DATA, uint32(requestId), sftpString(value))
  const attrs = (requestId, value) => send(
    SFTP.ATTRS,
    uint32(requestId),
    sftpAttributes(value),
  )
  const names = (requestId, values) => send(
    SFTP.NAME,
    uint32(requestId),
    uint32(values.length),
    ...values.flatMap((value) => [
      sftpString(value.filename),
      sftpString(value.longname),
      sftpAttributes(value.attrs),
    ]),
  )

  const failMalformed = (error) => {
    process.stderr.write(`atomic SFTP fixture protocol error: ${error.message}\n`)
    channel.destroy()
  }

  const dispatch = (packet) => {
    const type = packet[0]
    const cursor = new SftpRequestCursor(packet.subarray(1))
    if (type === SFTP.INIT) {
      assert.equal(cursor.readUInt32(), 3, 'native overwrite fixture requires SFTP v3')
      send(
        SFTP.VERSION,
        uint32(3),
        sftpString(POSIX_RENAME_EXTENSION),
        sftpString('1'),
      )
      return
    }

    const requestId = cursor.readUInt32()
    if (type === SFTP.LSTAT) {
      const remotePath = normalizeRemotePath(cursor.readString())
      if (remotePath === '/workspace/permission-denied.bin') {
        overwriteObservations.permissionDeniedLstats += 1
        status(requestId, STATUS_CODE.PERMISSION_DENIED, 'permission denied by fixture')
        return
      }
      const value = remotePath ? attributesFor(remotePath) : null
      if (!value) {
        status(requestId, STATUS_CODE.NO_SUCH_FILE)
        return
      }
      attrs(requestId, value)
      return
    }
    if (type === SFTP.OPENDIR) {
      const remotePath = normalizeRemotePath(cursor.readString())
      if (!remotePath || !directories.has(remotePath)) {
        status(requestId, STATUS_CODE.NO_SUCH_FILE)
        return
      }
      handle(requestId, allocateHandle({
        kind: 'directory',
        path: remotePath,
        delivered: false,
      }))
      return
    }
    if (type === SFTP.READDIR) {
      const record = recordForHandle(cursor.readString())
      if (!record || record.kind !== 'directory') {
        status(requestId, STATUS_CODE.FAILURE)
        return
      }
      if (record.delivered) {
        status(requestId, STATUS_CODE.EOF)
        return
      }
      record.delivered = true
      const listing = listDirectory(record.path)
      if (listing.length === 0) {
        status(requestId, STATUS_CODE.EOF)
        return
      }
      names(requestId, listing)
      return
    }
    if (type === SFTP.OPEN) {
      const remotePath = normalizeRemotePath(cursor.readString())
      const flags = cursor.readUInt32()
      if (!remotePath) {
        status(requestId, STATUS_CODE.FAILURE)
        return
      }
      if ((flags & OPEN_MODE.READ) !== 0) {
        if (!files.has(remotePath)) {
          status(requestId, STATUS_CODE.NO_SUCH_FILE)
          return
        }
        handle(requestId, allocateHandle({ kind: 'read', path: remotePath }))
        return
      }
      if (
        (flags & OPEN_MODE.CREAT) === 0
        || (flags & OPEN_MODE.WRITE) === 0
        || !directories.has(parentDirectory(remotePath))
        || ((flags & OPEN_MODE.EXCL) !== 0 && pathExists(remotePath))
      ) {
        status(requestId, STATUS_CODE.FAILURE)
        return
      }
      files.set(remotePath, Buffer.alloc(0))
      if (remotePath.endsWith('.part')) overwriteObservations.temporaryPaths.add(remotePath)
      handle(requestId, allocateHandle({ kind: 'write', path: remotePath }))
      return
    }
    if (type === SFTP.WRITE) {
      const record = recordForHandle(cursor.readString())
      const offset = cursor.readUInt64()
      const chunk = cursor.readString()
      const existing = record?.kind === 'write' ? files.get(record.path) : null
      if (!record || record.kind !== 'write' || !existing) {
        status(requestId, STATUS_CODE.FAILURE)
        return
      }
      const requiredLength = offset + chunk.length
      const next = requiredLength > existing.length
        ? Buffer.alloc(requiredLength)
        : Buffer.from(existing)
      existing.copy(next)
      chunk.copy(next, offset)
      files.set(record.path, next)
      status(requestId, STATUS_CODE.OK)
      return
    }
    if (type === SFTP.READ) {
      const record = recordForHandle(cursor.readString())
      const offset = cursor.readUInt64()
      const length = cursor.readUInt32()
      const content = record?.kind === 'read' ? files.get(record.path) : null
      if (!record || record.kind !== 'read' || !content) {
        status(requestId, STATUS_CODE.FAILURE)
        return
      }
      if (offset >= content.length) {
        status(requestId, STATUS_CODE.EOF)
        return
      }
      data(requestId, content.subarray(offset, Math.min(offset + length, content.length)))
      return
    }
    if (type === SFTP.CLOSE) {
      if (!closeHandle(cursor.readString())) {
        status(requestId, STATUS_CODE.FAILURE)
        return
      }
      status(requestId, STATUS_CODE.OK)
      return
    }
    if (type === SFTP.RENAME) {
      overwriteObservations.ordinaryRenameRequests += 1
      const oldPath = normalizeRemotePath(cursor.readString())
      const newPath = normalizeRemotePath(cursor.readString())
      if (
        !oldPath
        || !newPath
        || !files.has(oldPath)
        || pathExists(newPath)
        || !directories.has(parentDirectory(newPath))
      ) {
        status(requestId, STATUS_CODE.FAILURE)
        return
      }
      const content = files.get(oldPath)
      files.delete(oldPath)
      files.set(newPath, content)
      overwriteObservations.temporaryPaths.delete(oldPath)
      status(requestId, STATUS_CODE.OK)
      return
    }
    if (type === SFTP.EXTENDED) {
      const extension = cursor.readString().toString('utf8')
      if (extension !== POSIX_RENAME_EXTENSION) {
        status(requestId, STATUS_CODE.OP_UNSUPPORTED)
        return
      }
      const oldPath = normalizeRemotePath(cursor.readString())
      const newPath = normalizeRemotePath(cursor.readString())
      if (!oldPath || !newPath) {
        status(requestId, STATUS_CODE.OP_UNSUPPORTED)
        return
      }
      if (
        oldPath === '/workspace/unsupported-source.bin'
        || newPath === '/workspace/upload-unsupported.bin'
      ) {
        overwriteObservations.unsupportedAtomicRequests += 1
        status(requestId, STATUS_CODE.OP_UNSUPPORTED)
        return
      }
      if (!files.has(oldPath) || !directories.has(parentDirectory(newPath))) {
        status(requestId, STATUS_CODE.NO_SUCH_FILE)
        return
      }
      const content = files.get(oldPath)
      files.delete(oldPath)
      files.set(newPath, content)
      overwriteObservations.temporaryPaths.delete(oldPath)
      overwriteObservations.atomicRequests += 1
      status(requestId, STATUS_CODE.OK)
      return
    }
    if (type === SFTP.REMOVE) {
      const remotePath = normalizeRemotePath(cursor.readString())
      if (!remotePath || !files.delete(remotePath)) {
        status(requestId, STATUS_CODE.NO_SUCH_FILE)
        return
      }
      overwriteObservations.temporaryPaths.delete(remotePath)
      status(requestId, STATUS_CODE.OK)
      return
    }
    status(requestId, STATUS_CODE.OP_UNSUPPORTED)
  }

  let pending = Buffer.alloc(0)
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
    } catch (error) {
      failMalformed(error)
    }
  })
  // A one-shot pure-Rust subsystem client sends SSH EOF after its bounded request batch.
  // Mirror OpenSSH sftp-server by flushing replies and closing the channel at that point.
  channel.on('end', () => channel.end())
}

function configureSftp(sftp, client) {
  sftp.once('close', () => client.end())
  sftp.on('REALPATH', (requestId, requestedPath) => {
    const remotePath = normalizeRemotePath(requestedPath)
    if (!remotePath) {
      sftp.status(requestId, STATUS_CODE.FAILURE)
      return
    }
    sftp.name(requestId, [{
      filename: remotePath,
      longname: remotePath,
      attrs: attributesFor(remotePath) ?? {},
    }])
  })

  sftp.on('OPENDIR', (requestId, requestedPath) => {
    const remotePath = normalizeRemotePath(requestedPath)
    if (!remotePath || !directories.has(remotePath)) {
      sftp.status(requestId, STATUS_CODE.NO_SUCH_FILE)
      return
    }
    sftp.handle(requestId, allocateHandle({
      kind: 'directory',
      path: remotePath,
      delivered: false,
    }))
  })

  sftp.on('READDIR', (requestId, handle) => {
    const record = recordForHandle(handle)
    if (!record || record.kind !== 'directory') {
      sftp.status(requestId, STATUS_CODE.FAILURE)
      return
    }
    if (record.delivered) {
      sftp.status(requestId, STATUS_CODE.EOF)
      return
    }
    record.delivered = true
    observations.directoryReads += 1
    const entries = listDirectory(record.path)
    if (entries.length === 0) {
      sftp.status(requestId, STATUS_CODE.EOF)
      return
    }
    sftp.name(requestId, entries)
  })

  sftp.on('CLOSE', (requestId, handle) => {
    if (!closeHandle(handle)) {
      sftp.status(requestId, STATUS_CODE.FAILURE)
      return
    }
    sftp.status(requestId, STATUS_CODE.OK)
  })

  const replyWithAttributes = (requestId, requestedPath) => {
    const remotePath = normalizeRemotePath(requestedPath)
    const attrs = remotePath ? attributesFor(remotePath) : null
    if (!attrs) {
      sftp.status(requestId, STATUS_CODE.NO_SUCH_FILE)
      return
    }
    sftp.attrs(requestId, attrs)
  }
  sftp.on('STAT', replyWithAttributes)
  sftp.on('LSTAT', replyWithAttributes)

  sftp.on('OPEN', (requestId, requestedPath, flags) => {
    const remotePath = normalizeRemotePath(requestedPath)
    if (!remotePath) {
      sftp.status(requestId, STATUS_CODE.FAILURE)
      return
    }

    if ((flags & OPEN_MODE.READ) !== 0) {
      if (!files.has(remotePath)) {
        sftp.status(requestId, STATUS_CODE.NO_SUCH_FILE)
        return
      }
      sftp.handle(requestId, allocateHandle({
        kind: 'read',
        path: remotePath,
      }))
      return
    }

    const canCreate = (flags & OPEN_MODE.CREAT) !== 0
    const canWrite = (flags & OPEN_MODE.WRITE) !== 0
    const exclusive = (flags & OPEN_MODE.EXCL) !== 0
    if (
      !canCreate
      || !canWrite
      || !directories.has(parentDirectory(remotePath))
      || (exclusive && pathExists(remotePath))
    ) {
      sftp.status(requestId, STATUS_CODE.FAILURE)
      return
    }
    files.set(remotePath, Buffer.alloc(0))
    observations.uploadTemporaryPaths.add(remotePath)
    observations.mutations.add('upload-open')
    sftp.handle(requestId, allocateHandle({
      kind: 'write',
      path: remotePath,
    }))
  })

  sftp.on('WRITE', (requestId, handle, offset, data) => {
    const record = recordForHandle(handle)
    if (
      !record
      || record.kind !== 'write'
      || !Number.isSafeInteger(offset)
      || offset < 0
      || !Buffer.isBuffer(data)
    ) {
      sftp.status(requestId, STATUS_CODE.FAILURE)
      return
    }
    const existing = files.get(record.path)
    if (!existing) {
      sftp.status(requestId, STATUS_CODE.NO_SUCH_FILE)
      return
    }
    const requiredLength = offset + data.length
    const next = requiredLength > existing.length
      ? Buffer.alloc(requiredLength)
      : Buffer.from(existing)
    existing.copy(next)
    data.copy(next, offset)
    files.set(record.path, next)
    observations.uploadBytesWritten += data.length
    observations.uploadFinalVisibleDuringWrite ||= files.has(fixtureTransferPath)
    observations.mutations.add('upload-write')
    sftp.status(requestId, STATUS_CODE.OK)
  })

  sftp.on('READ', (requestId, handle, offset, length) => {
    const record = recordForHandle(handle)
    if (
      !record
      || record.kind !== 'read'
      || !Number.isSafeInteger(offset)
      || offset < 0
      || !Number.isSafeInteger(length)
      || length <= 0
    ) {
      sftp.status(requestId, STATUS_CODE.FAILURE)
      return
    }
    const content = files.get(record.path)
    if (!content) {
      sftp.status(requestId, STATUS_CODE.NO_SUCH_FILE)
      return
    }
    if (offset >= content.length) {
      sftp.status(requestId, STATUS_CODE.EOF)
      return
    }
    sftp.data(requestId, content.subarray(offset, Math.min(offset + length, content.length)))
  })

  sftp.on('MKDIR', (requestId, requestedPath) => {
    const remotePath = normalizeRemotePath(requestedPath)
    if (
      !remotePath
      || remotePath === '/'
      || pathExists(remotePath)
      || !directories.has(parentDirectory(remotePath))
    ) {
      sftp.status(requestId, STATUS_CODE.FAILURE)
      return
    }
    directories.add(remotePath)
    observations.mutations.add('mkdir')
    sftp.status(requestId, STATUS_CODE.OK)
  })

  sftp.on('RENAME', (requestId, oldValue, newValue) => {
    const oldPath = normalizeRemotePath(oldValue)
    const newPath = normalizeRemotePath(newValue)
    if (
      !oldPath
      || !newPath
      || !files.has(oldPath)
      || pathExists(newPath)
      || !directories.has(parentDirectory(newPath))
    ) {
      sftp.status(requestId, STATUS_CODE.FAILURE)
      return
    }
    const content = files.get(oldPath)
    files.delete(oldPath)
    files.set(newPath, content)
    if (observations.uploadTemporaryPaths.has(oldPath)) {
      assert.equal(newPath, fixtureTransferPath, 'temporary upload must publish the intended path')
      assert.match(
        basename(oldPath),
        /^\.猫咪-数据\.bin\.terminal233-[0-9a-f-]+\.part$/u,
        'upload must use the production temporary-file naming contract',
      )
      observations.mutations.add('upload-atomic-rename')
    }
    observations.mutations.add('rename')
    sftp.status(requestId, STATUS_CODE.OK)
  })

  sftp.on('REMOVE', (requestId, requestedPath) => {
    const remotePath = normalizeRemotePath(requestedPath)
    if (!remotePath || !files.delete(remotePath)) {
      sftp.status(requestId, STATUS_CODE.NO_SUCH_FILE)
      return
    }
    observations.mutations.add('remove')
    sftp.status(requestId, STATUS_CODE.OK)
  })

  sftp.on('RMDIR', (requestId, requestedPath) => {
    const remotePath = normalizeRemotePath(requestedPath)
    if (
      !remotePath
      || remotePath === '/'
      || !directories.has(remotePath)
      || !directoryIsEmpty(remotePath)
    ) {
      sftp.status(requestId, STATUS_CODE.FAILURE)
      return
    }
    directories.delete(remotePath)
    observations.mutations.add('rmdir')
    sftp.status(requestId, STATUS_CODE.OK)
  })
}

function cargoExecutable() {
  const configured = process.env.NEKOSSH_RELEASE_CARGO
  if (configured === undefined || configured.length === 0) return 'cargo'
  if (!path.isAbsolute(configured)) {
    throw new Error('NEKOSSH_RELEASE_CARGO must be an absolute executable path')
  }
  return configured
}

async function runCargoTest(port, privateKeyPath, testName) {
  const child = spawn(cargoExecutable(), [
    'test',
    '--manifest-path',
    'terminal233/Cargo.toml',
    '--locked',
    '--all-features',
    testName,
    '--',
    '--exact',
    '--nocapture',
  ], {
    cwd: repositoryRoot,
    env: {
      ...process.env,
      NEKOSSH_NATIVE_INTEROP: '1',
      NEKOSSH_NATIVE_INTEROP_HOST: '127.0.0.1',
      NEKOSSH_NATIVE_INTEROP_PORT: String(port),
      NEKOSSH_NATIVE_INTEROP_USERNAME: fixtureUsername,
      NEKOSSH_NATIVE_INTEROP_PASSWORD: fixturePassword,
      NEKOSSH_NATIVE_INTEROP_PRIVATE_KEY_PATH: privateKeyPath,
      NEKOSSH_NATIVE_INTEROP_PRIVATE_KEY_PASSPHRASE: fixtureKeyPassphrase,
      CARGO_INCREMENTAL: '0',
    },
    stdio: 'inherit',
    windowsHide: true,
    shell: false,
  })

  const timeout = setTimeout(() => child.kill(), 900_000)
  timeout.unref()
  try {
    const [exitCode, signal] = await once(child, 'exit')
    if (exitCode !== 0) {
      throw new Error(`native SSH interoperability test failed (${exitCode ?? signal})`)
    }
  } finally {
    clearTimeout(timeout)
  }
}

const hostPrivateKey = ssh2.utils.generateKeyPairSync('ed25519', {}).private
const generatedClientKey = ssh2.utils.generateKeyPairSync('rsa', {
  bits: 3072,
  format: 'new',
  cipher: 'aes256-ctr',
  passphrase: fixtureKeyPassphrase,
})
const clientPrivateKeyPem = generatedClientKey.private
const parsedClientKey = ssh2.utils.parseKey(clientPrivateKeyPem, fixtureKeyPassphrase)
assert.ok(!(parsedClientKey instanceof Error), 'fixture private key must parse')

const clients = new Set()
const server = new SshServer({
  hostKeys: [hostPrivateKey],
  algorithms: {
    kex: ['curve25519-sha256'],
    serverHostKey: ['ssh-ed25519'],
    cipher: ['aes256-gcm@openssh.com'],
    hmac: ['hmac-sha2-256'],
  },
  debug: process.env.NEKOSSH_INTEROP_DEBUG === '1'
    ? (message) => process.stderr.write(`SSH fixture debug: ${message}\n`)
    : undefined,
}, (client) => {
  clients.add(client)
  observations.transports += 1
  client.on('error', (error) => {
    if (!['ECONNABORTED', 'ECONNRESET'].includes(error.code)) {
      process.stderr.write(`SSH fixture transport error: ${error.message}\n`)
    }
  })
  client.on('close', () => clients.delete(client))
  client.on('authentication', (context) => {
    if (process.env.NEKOSSH_INTEROP_PROGRESS === '1') {
      process.stderr.write(`SSH fixture authentication: ${JSON.stringify({
        method: context.method,
        usernameMatches: context.username === fixtureUsername,
        algorithm: context.key?.algo ?? null,
        fixtureAlgorithm: parsedClientKey.type,
        hashAlgorithm: context.hashAlgo ?? null,
        keyMatches: context.key
          ? Buffer.from(context.key.data).equals(parsedClientKey.getPublicSSH())
          : null,
        hasSignature: Boolean(context.signature),
      })}\n`)
    }
    if (
      context.method === 'password'
      && context.username === fixtureUsername
      && Buffer.from(context.password).equals(Buffer.from(fixturePassword))
    ) {
      observations.passwordAuthentications += 1
      context.accept()
      return
    }
    if (
      context.method === 'publickey'
      && context.username === fixtureUsername
      && context.key.algo === parsedClientKey.type
      && ['sha256', 'sha512'].includes(context.hashAlgo)
      && Buffer.from(context.key.data).equals(parsedClientKey.getPublicSSH())
      && (
        !context.signature
        || parsedClientKey.verify(context.blob, context.signature, context.hashAlgo)
      )
    ) {
      observations.publicKeyAuthentications += 1
      if (context.signature) observations.publicKeySignatureHashes.add(context.hashAlgo)
      context.accept()
      return
    }
    context.reject()
  })
  client.on('ready', () => {
    client.on('session', (accept) => {
      const session = accept()
      session.on('exec', (acceptExec, rejectExec, info) => {
        const command = String(info.command)
        const output = command.includes('native-password-ok')
          ? 'native-password-ok\n'
          : command.includes('native-key-ok')
            ? 'native-key-ok\n'
            : null
        if (!output) {
          rejectExec()
          return
        }
        const channel = acceptExec()
        observations.successfulExecutions += 1
        channel.once('close', () => client.end())
        channel.exit(0)
        channel.end(output)
      })
      if (atomicInteropPhase) {
        session.on('subsystem', (acceptSubsystem, rejectSubsystem, info) => {
          if (info.name !== 'sftp') {
            rejectSubsystem()
            return
          }
          observations.sftpSessions += 1
          configureAtomicSftp(acceptSubsystem())
        })
      } else {
        session.on('sftp', (acceptSftp) => {
          observations.sftpSessions += 1
          configureSftp(acceptSftp(), client)
        })
      }
    })
  })
})

const temporaryDirectory = await mkdtemp(path.join(tmpdir(), 'terminal233-native-interop-'))
const privateKeyPath = path.join(temporaryDirectory, 'fixture-private-key.pem')
await writeFile(privateKeyPath, clientPrivateKeyPem, { mode: 0o600, flag: 'wx' })

try {
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert.ok(address && typeof address === 'object')
  await runCargoTest(
    address.port,
    privateKeyPath,
    'ssh::tests::native_ssh_interoperability',
  )

  files.set('/workspace/no-overwrite-source.bin', Buffer.from('no-overwrite-source'))
  files.set('/workspace/no-overwrite-target.bin', Buffer.from('existing-target'))
  files.set('/workspace/permission-source.bin', Buffer.from('permission-source'))
  files.set('/workspace/permission-denied.bin', Buffer.from('permission-target'))
  files.set('/workspace/atomic-source.bin', Buffer.from('atomic-source'))
  files.set('/workspace/atomic-target.bin', Buffer.from('atomic-target'))
  files.set('/workspace/unsupported-source.bin', Buffer.from('unsupported-source'))
  files.set('/workspace/unsupported-target.bin', Buffer.from('unsupported-target'))
  files.set('/workspace/upload-existing.bin', Buffer.from('old-upload'))
  files.set('/workspace/upload-unsupported.bin', Buffer.from('old-unsupported-upload'))
  atomicInteropPhase = true
  await runCargoTest(
    address.port,
    privateKeyPath,
    'sftp_transfer::tests::native_sftp_overwrite_interoperability',
  )

  assert.ok(observations.transports >= 12, 'native client must open fresh bounded transports')
  assert.ok(observations.passwordAuthentications >= 9, 'password path must authenticate')
  assert.ok(observations.publicKeyAuthentications >= 2, 'private-key path must authenticate')
  assert.ok(
    observations.publicKeySignatureHashes.size >= 1,
    'RSA private-key authentication must use an RFC 8332 SHA-2 signature',
  )
  assert.ok(
    [...observations.publicKeySignatureHashes].every((hash) => ['sha256', 'sha512'].includes(hash)),
    'RSA private-key authentication must never fall back to SHA-1',
  )
  assert.equal(observations.successfulExecutions, 2)
  assert.ok(observations.sftpSessions >= 8, 'SFTP operations must use the in-process client')
  assert.ok(observations.directoryReads >= 2)
  assert.deepEqual(
    [...observations.mutations].sort(),
    [
      'mkdir',
      'remove',
      'rename',
      'rmdir',
      'upload-atomic-rename',
      'upload-open',
      'upload-write',
    ],
  )
  assert.equal(files.has('/workspace/hello.txt'), false)
  assert.equal(files.has('/workspace/renamed.txt'), false)
  assert.equal(directories.has('/workspace/new-directory'), false)
  assert.equal(observations.uploadFinalVisibleDuringWrite, false)
  assert.equal(observations.uploadBytesWritten, fixtureTransferContent.length)
  assert.deepEqual(files.get(fixtureTransferPath), fixtureTransferContent)
  assert.equal(
    [...files.keys()].some((remotePath) => remotePath.endsWith('.part')),
    false,
    'successful upload must not leave a temporary object behind',
  )
  assert.equal(openHandles.size, 0, 'all native SFTP file and directory handles must close')
  assert.ok(
    overwriteObservations.atomicRequests >= 2,
    'native overwrite must use the posix-rename extension for rename and upload',
  )
  assert.equal(
    overwriteObservations.unsupportedAtomicRequests,
    2,
    'unsupported atomic rename and upload must fail closed',
  )
  assert.ok(
    overwriteObservations.permissionDeniedLstats >= 1,
    'permission-denied LSTAT must reach the production client',
  )
  assert.equal(
    overwriteObservations.ordinaryRenameRequests,
    0,
    'existing-target tests must never fall back to ordinary v3 rename',
  )
  assert.equal(
    overwriteObservations.temporaryPaths.size,
    0,
    'all atomic overwrite temporary paths must be published or removed',
  )

  process.stdout.write(`${JSON.stringify({
    status: 'passed',
    transports: observations.transports,
    passwordAuthentications: observations.passwordAuthentications,
    publicKeyAuthentications: observations.publicKeyAuthentications,
    publicKeySignatureHashes: [...observations.publicKeySignatureHashes].sort(),
    successfulExecutions: observations.successfulExecutions,
    sftpSessions: observations.sftpSessions,
    directoryReads: observations.directoryReads,
    uploadBytesWritten: observations.uploadBytesWritten,
    mutations: [...observations.mutations].sort(),
    atomicOverwriteRequests: overwriteObservations.atomicRequests,
    unsupportedAtomicOverwriteRequests: overwriteObservations.unsupportedAtomicRequests,
    permissionDeniedLstats: overwriteObservations.permissionDeniedLstats,
  })}\n`)
} finally {
  for (const client of clients) {
    client.end()
    // ssh2 Server clients do not expose a public destroy method. Force-close the fixture-only
    // socket after the protocol disconnect so a failed test cannot keep Node alive.
    client._sock?.destroy()
  }
  // ssh2.Server wraps net.Server but does not expose its `listening` property. Its public
  // address() method returns null when no listener exists, and close(callback) provides a
  // race-free completion signal.
  if (server.address() !== null) {
    await new Promise((resolve, reject) => {
      server.close((error) => {
        if (error) reject(error)
        else resolve()
      })
    })
  }
  // The fixture key is generated only for this local test and is removed deterministically.
  await unlink(privateKeyPath).catch(() => {})
  await rmdir(temporaryDirectory).catch(() => {})
}
