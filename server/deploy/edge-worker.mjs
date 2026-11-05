// Cloudflare Workers VPC transport for the Go sync service. The Worker never
// reads credentials or logs request bodies; the backend remains loopback-only.
const forwardedHeaders = [
  'accept', 'authorization', 'content-type', 'origin', 'x-admin-totp',
  'access-control-request-method', 'access-control-request-headers',
];

function failure(status, message) {
  return new Response(message, { status, headers: { 'Cache-Control': 'no-store' } });
}

async function signVisitor(keyHex, timestamp, method, path, ip) {
  if (!/^[0-9a-f]{64}$/i.test(keyHex)) throw new Error('invalid edge configuration');
  const keyBytes = new Uint8Array(keyHex.match(/../g).map(byte => Number.parseInt(byte, 16)));
  const key = await crypto.subtle.importKey('raw', keyBytes, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const payload = new TextEncoder().encode(`${timestamp}\n${method}\n${path}\n${ip}`);
  const signature = new Uint8Array(await crypto.subtle.sign('HMAC', key, payload));
  return `${timestamp}:${Array.from(signature, byte => byte.toString(16).padStart(2, '0')).join('')}`;
}

export default {
  async fetch(request, env) {
    const incoming = new URL(request.url);
    if (incoming.protocol !== 'https:' || incoming.hostname !== env.ALLOWED_HOST) {
      return failure(403, 'Forbidden');
    }
    const ip = request.headers.get('CF-Connecting-IP');
    if (!ip || ip.length > 64 || ip.includes(',')) return failure(503, 'Visitor identity unavailable');
    const target = new URL('http://nekossh.internal');
    target.pathname = incoming.pathname;
    target.search = incoming.search;
    const headers = new Headers();
    for (const name of forwardedHeaders) {
      const value = request.headers.get(name);
      if (value !== null) headers.set(name, value);
    }
    headers.set('CF-Connecting-IP', ip);
    try {
      const timestamp = Math.floor(Date.now() / 1000).toString();
      headers.set('X-Nekossh-Visitor-Signature', await signVisitor(env.EDGE_HMAC_KEY, timestamp, request.method, target.pathname, ip));
	  const init = { method: request.method, headers, redirect: 'manual', signal: request.signal, duplex: 'half' };
      if (request.method !== 'GET' && request.method !== 'HEAD') init.body = request.body;
      const upstream = await env.SYNC.fetch(new Request(target, init));
      const responseHeaders = new Headers(upstream.headers);
      responseHeaders.set('Cache-Control', 'no-store');
      return new Response(upstream.body, { status: upstream.status, headers: responseHeaders });
    } catch {
      return failure(502, 'Sync service unavailable');
    }
  },
};
