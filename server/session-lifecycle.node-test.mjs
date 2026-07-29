import assert from 'node:assert/strict'
import test from 'node:test'
import {
  cookieValue,
  openShellChannel,
  registerRevocableOperation,
  releaseMatches,
  retireBrowserSession,
  revokeOperations,
  touchConnectionActivity,
} from './session-lifecycle.mjs'

test('activity refreshes only the live owner session and parent connection', () => {
  const now = 1_000
  const connection = { owner: 'owner-a', expiresAt: now + 1 }
  const sessions = new Map([
    ['live', { owner: 'owner-a', expiresAt: now + 5 }],
    ['expired', { owner: 'owner-a', expiresAt: now }],
    ['other', { owner: 'owner-b', expiresAt: now + 5 }],
  ])

  touchConnectionActivity(connection, sessions, 30_000, now)

  assert.equal(connection.expiresAt, 31_000)
  assert.equal(sessions.get('live').expiresAt, 31_000)
  assert.equal(sessions.get('expired').expiresAt, now)
  assert.equal(sessions.get('other').expiresAt, now + 5)
})

test('session retirement is idempotent and disconnects its owner once', () => {
  const sessions = new Map([['token-a', { owner: 'owner-a', releaseId: 'release-a' }]])
  const disconnected = []

  assert.equal(retireBrowserSession('token-a', sessions, (owner) => disconnected.push(owner)), true)
  assert.equal(retireBrowserSession('token-a', sessions, (owner) => disconnected.push(owner)), false)
  assert.deepEqual(disconnected, ['owner-a'])
})

test('stale page release cannot close a replacement browser session', () => {
  const session = { releaseId: 'new-release' }

  assert.equal(releaseMatches(session, 'old-release'), false)
  assert.equal(releaseMatches(session, 'new-release'), true)
  assert.equal(cookieValue('theme=dark; nekossh_session=token-a; x=1', 'nekossh_session'), 'token-a')
})

test('shell channel failure does not close its shared SSH transport', async () => {
  let endCalls = 0
  const client = {
    end() { endCalls += 1 },
    shell(_options, callback) { callback(new Error('channel rejected')) },
  }

  await assert.rejects(
    openShellChannel(client, {}, () => true, () => new Error('connection closed')),
    /channel rejected/,
  )
  assert.equal(endCalls, 0)
})

test('attachment revocation cancels in-flight operations once', () => {
  const attachment = { revoked: false, revokeReason: null, operations: new Set() }
  const reasons = []
  registerRevocableOperation(attachment, (reason) => reasons.push(reason))

  assert.equal(revokeOperations(attachment, 'expired'), true)
  assert.equal(revokeOperations(attachment, 'closed'), false)
  assert.deepEqual(reasons, ['expired'])
  assert.equal(attachment.operations.size, 0)
})
