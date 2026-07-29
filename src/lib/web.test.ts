import { afterEach, describe, expect, it, vi } from 'vitest'

function jsonResponse(value: unknown): Response {
  return {
    status: 200,
    ok: true,
    json: async () => value,
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
})
