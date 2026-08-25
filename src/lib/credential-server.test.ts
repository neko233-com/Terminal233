import { afterEach, describe, expect, it, vi } from 'vitest'
import { CredentialServerClient } from './credential-server'

describe('CredentialServerClient transport security', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('requires HTTPS for public synchronization servers', () => {
    expect(() => new CredentialServerClient('http://sshcloud.neko233.com'))
      .toThrow('公网同步服务器必须使用 HTTPS')
    expect(() => new CredentialServerClient('https://sshcloud.neko233.com')).not.toThrow()
  })

  it('allows HTTP only for local development loopback addresses', () => {
    expect(() => new CredentialServerClient('http://127.0.0.1:8787')).not.toThrow()
    expect(() => new CredentialServerClient('http://localhost:8787')).not.toThrow()
    expect(() => new CredentialServerClient('http://[::1]:8787')).not.toThrow()
  })

  it('rejects credentials, query strings, and fragments in the server URL', () => {
    expect(() => new CredentialServerClient('https://user:secret@sshcloud.neko233.com'))
      .toThrow('不能包含账号或密码')
    expect(() => new CredentialServerClient('https://sshcloud.neko233.com/?token=secret'))
      .toThrow('不能包含查询参数或片段')
    expect(() => new CredentialServerClient('https://sshcloud.neko233.com/#secret'))
      .toThrow('不能包含查询参数或片段')
  })

  it('omits cookies and refuses redirects while using the bearer token in memory', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(
      JSON.stringify({ token: 'memory-token' }),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    ))
    const client = new CredentialServerClient('https://sshcloud.neko233.com')

    await client.login('cloud-user', 'cloud-pass')

    expect(fetchMock).toHaveBeenCalledWith('https://sshcloud.neko233.com/api/v1/login', expect.objectContaining({
      credentials: 'omit',
      redirect: 'error',
      body: JSON.stringify({ username: 'cloud-user', password: 'cloud-pass' }),
    }))
    expect(client.authenticated).toBe(true)
  })
})
