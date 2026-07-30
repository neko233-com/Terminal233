import { afterEach, describe, expect, it, vi } from 'vitest'

function jsonResponse(value: unknown): Response {
  return {
    status: 200,
    ok: true,
    json: async () => value,
  } as Response
}

function errorResponse(status: number, code: string, message: string): Response {
  return {
    status,
    ok: false,
    json: async () => ({ code, message }),
  } as Response
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.resetModules()
})

describe('web browser session lifecycle', () => {
  it('shares one initialization request across React StrictMode effects', async () => {
    const fetch = vi.fn().mockResolvedValue(jsonResponse({ ok: true, releaseId: 'release-a' }))
    vi.stubGlobal('fetch', fetch)
    const web = await import('./web')

    await Promise.all([web.initializeBrowserSession(), web.initializeBrowserSession()])

    expect(fetch).toHaveBeenCalledTimes(1)
    expect(fetch).toHaveBeenCalledWith('/api/browser-session', expect.objectContaining({ method: 'POST' }))
  })

  it('releases only the initialized page session through pagehide beacon', async () => {
    const fetch = vi.fn().mockResolvedValue(jsonResponse({ ok: true, releaseId: 'release-a' }))
    const sendBeacon = vi.fn().mockReturnValue(true)
    vi.stubGlobal('fetch', fetch)
    Object.defineProperty(navigator, 'sendBeacon', { configurable: true, value: sendBeacon })
    const web = await import('./web')
    await web.initializeBrowserSession()

    web.releaseBrowserSession()
    web.releaseBrowserSession()

    expect(sendBeacon).toHaveBeenCalledTimes(1)
    expect(sendBeacon).toHaveBeenCalledWith('/api/browser-session/close', JSON.stringify({ releaseId: 'release-a' }))
  })

  it('invalidates an expired session and creates a fresh one on retry', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ ok: true, releaseId: 'release-a' }))
      .mockResolvedValueOnce(errorResponse(401, 'browser_session_required', '会话已过期。'))
      .mockResolvedValueOnce(jsonResponse({ ok: true, releaseId: 'release-b' }))
    vi.stubGlobal('fetch', fetch)
    const web = await import('./web')
    const invalidated = vi.fn()
    const unsubscribe = web.onBrowserSessionInvalidated(invalidated)

    await web.initializeBrowserSession()
    await expect(web.listConnections()).rejects.toMatchObject({ code: 'browser_session_required' })
    expect(invalidated).toHaveBeenCalledTimes(1)

    await web.initializeBrowserSession()
    expect(fetch).toHaveBeenCalledTimes(3)
    expect(fetch.mock.calls[2]).toEqual([
      '/api/browser-session',
      expect.objectContaining({ method: 'POST' }),
    ])
    unsubscribe()
  })

  it('keeps the browser session when the SSH server rejects credentials with HTTP 401', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ ok: true, releaseId: 'release-a' }))
      .mockResolvedValueOnce(errorResponse(401, 'ssh_authentication_failed', 'SSH 认证失败。'))
    vi.stubGlobal('fetch', fetch)
    const web = await import('./web')
    const invalidated = vi.fn()
    const unsubscribe = web.onBrowserSessionInvalidated(invalidated)

    await web.initializeBrowserSession()
    await expect(web.prepareConnection('connection-a')).rejects.toMatchObject({
      code: 'ssh_authentication_failed',
    })
    await web.initializeBrowserSession()

    expect(invalidated).not.toHaveBeenCalled()
    expect(fetch).toHaveBeenCalledTimes(2)
    unsubscribe()
  })
})

describe('web downloads', () => {
  it('keeps the object URL alive through the browser download dispatch', async () => {
    vi.useFakeTimers()
    const fetch = vi.fn().mockResolvedValue({
      ok: true,
      blob: async () => new Blob(['neko']),
      headers: new Headers({ 'content-disposition': 'attachment; filename="notes.txt"' }),
    } as Response)
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:nekossh-download')
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined)
    let observedDownloadName = ''
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      observedDownloadName = this.download
    })
    vi.stubGlobal('fetch', fetch)

    try {
      const web = await import('./web')
      await web.downloadRemote('connection-a', '/home/neko/notes.txt')

      expect(click).toHaveBeenCalledTimes(1)
      expect(observedDownloadName).toBe('notes.txt')
      expect(document.querySelector('a[download="notes.txt"]')).toBeNull()
      expect(revokeObjectURL).not.toHaveBeenCalled()

      vi.runAllTimers()
      expect(revokeObjectURL).toHaveBeenCalledWith('blob:nekossh-download')
    } finally {
      vi.useRealTimers()
    }
  })

  it('prefers a safe RFC 5987 UTF-8 filename over the ASCII fallback', async () => {
    vi.useFakeTimers()
    const fetch = vi.fn().mockResolvedValue({
      ok: true,
      blob: async () => new Blob(['neko']),
      headers: new Headers({
        'content-disposition': 'attachment; filename="______.txt"; filename*=UTF-8\'\'%E4%B8%AD%E6%96%87%22%E7%AC%94%E8%AE%B0.txt',
      }),
    } as Response)
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:nekossh-unicode-download')
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined)
    let observedDownloadName = ''
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      observedDownloadName = this.download
    })
    vi.stubGlobal('fetch', fetch)

    try {
      const web = await import('./web')
      await web.downloadRemote('connection-a', '/home/neko/中文"笔记.txt')

      expect(observedDownloadName).toBe('中文"笔记.txt')
      vi.runAllTimers()
      expect(revokeObjectURL).toHaveBeenCalledWith('blob:nekossh-unicode-download')
    } finally {
      vi.useRealTimers()
    }
  })
})
