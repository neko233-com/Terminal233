import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHmac, webcrypto } from 'node:crypto';
import worker from './edge-worker.mjs';

globalThis.crypto ??= webcrypto;
const secret = 'ab'.repeat(32);

test('forwards request with an authenticated visitor and no spoofed proxy headers', async () => {
  let received;
  const env = {
    ALLOWED_HOST: 'sync.example.test', EDGE_HMAC_KEY: secret,
    SYNC: { fetch: async request => { received = request; return new Response('ok', { headers: { 'Cache-Control': 'public' } }); } },
  };
  const req = new Request('https://sync.example.test/api/v1/login?x=1', {
    method: 'POST', body: '{"username":"alice"}', headers: {
      'CF-Connecting-IP': '198.51.100.42', 'Authorization': 'Bearer user-session',
      'X-Forwarded-For': 'attacker', 'X-Nekossh-Visitor-Signature': 'forged',
    },
  });
  const res = await worker.fetch(req, env);
  assert.equal(res.status, 200);
  assert.equal(res.headers.get('Cache-Control'), 'no-store');
  assert.equal(received.url, 'http://nekossh.internal/api/v1/login?x=1');
  assert.equal(received.headers.get('authorization'), 'Bearer user-session');
  assert.equal(received.headers.get('x-forwarded-for'), null);
  assert.equal(await received.text(), '{"username":"alice"}');
  const [ts, signature] = received.headers.get('X-Nekossh-Visitor-Signature').split(':');
  const expected = createHmac('sha256', Buffer.from(secret, 'hex')).update(`${ts}\nPOST\n/api/v1/login\n198.51.100.42`).digest('hex');
  assert.equal(signature, expected);
});

test('fails closed for the wrong hostname, missing visitor or missing key', async () => {
  let calls = 0;
  const env = { ALLOWED_HOST: 'sync.example.test', EDGE_HMAC_KEY: secret, SYNC: { fetch: async () => { calls++; return new Response('ok'); } } };
  assert.equal((await worker.fetch(new Request('https://other.test/healthz'), env)).status, 403);
  assert.equal((await worker.fetch(new Request('https://sync.example.test/healthz'), env)).status, 503);
  env.EDGE_HMAC_KEY = '';
  assert.equal((await worker.fetch(new Request('https://sync.example.test/healthz', { headers: { 'CF-Connecting-IP': '198.51.100.42' } }), env)).status, 502);
  assert.equal(calls, 0);
});
