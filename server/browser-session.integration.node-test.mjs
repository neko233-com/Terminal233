import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { generateKeyPairSync } from 'node:crypto'
import { once } from 'node:events'
import { fileURLToPath } from 'node:url'
import net from 'node:net'
import ssh2 from 'ssh2'
import test from 'node:test'

const { Server: SshServer } = ssh2

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

test('rejected shell channel leaves the authenticated SSH transport and first terminal alive', { timeout: 20_000 }, async () => {
  const backendPort = await availablePort()
  const frontendPort = await availablePort()
  const sshPort = await availablePort()
  const origin = `http://127.0.0.1:${frontendPort}`
  const baseUrl = `http://127.0.0.1:${backendPort}`
  const { privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
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
      if (context.method === 'password'
        && context.username === 'tester'
        && Buffer.from(context.password).equals(Buffer.from('fixture-password'))) context.accept()
      else context.reject()
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
        authentication: { kind: 'password', password: 'fixture-password' },
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
    const output = (await poll.json()).chunks.map((chunk) => chunk.data).join('')
    assert.match(output, /echo:still-alive/)

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
  } finally {
    child.kill()
    if (child.exitCode === null) await once(child, 'exit')
    sshServer.close()
    await once(sshServer, 'close')
  }
})
