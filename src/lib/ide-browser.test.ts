import { beforeEach, describe, expect, it, vi } from 'vitest'

const web = vi.hoisted(() => ({
  closeTerminal: vi.fn(),
  createConnection: vi.fn(),
  createDirectory: vi.fn(),
  commitDelete: vi.fn(),
  disconnectConnection: vi.fn(),
  downloadRemoteBytes: vi.fn(),
  initializeBrowserSession: vi.fn(),
  listConnections: vi.fn(),
  listDirectory: vi.fn(),
  onBrowserSessionInvalidated: vi.fn(),
  pollTerminal: vi.fn(),
  prepareConnection: vi.fn(),
  prepareDelete: vi.fn(),
  renameRemote: vi.fn(),
  resizeTerminal: vi.fn(),
  startTerminal: vi.fn(),
  trustConnection: vi.fn(),
  uploadFiles: vi.fn(),
  writeTerminal: vi.fn(),
}))

vi.mock('./web', () => web)

import {
  createIdeConnection,
  listIdeConnections,
  startIdeTerminal,
  writeIdeTerminal,
} from './ide-browser'

describe('browser Terminal233 runtime adapter', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    web.initializeBrowserSession.mockResolvedValue(undefined)
    web.onBrowserSessionInvalidated.mockReturnValue(() => undefined)
    web.createConnection.mockResolvedValue({
      id: 'connection-1',
      label: '浏览器测试',
      host: 'example.test',
      port: 22,
      username: 'root',
      expiresInSeconds: 1_800,
    })
    web.listConnections.mockResolvedValue([])
    web.startTerminal.mockResolvedValue({ terminalId: 'terminal-1' })
    web.pollTerminal
      .mockResolvedValueOnce({
        chunks: [{ cursor: 1, dataBase64: 'aGk=' }],
        closed: false,
        exitStatus: null,
        error: null,
      })
      .mockResolvedValueOnce({ chunks: [], closed: true, exitStatus: 0, error: null })
    web.writeTerminal.mockResolvedValue(undefined)
  })

  it('uses the localhost web backend and maps the shared connection contract', async () => {
    await expect(listIdeConnections()).resolves.toEqual([])
    await expect(createIdeConnection({
      label: '浏览器测试',
      host: 'example.test',
      port: 22,
      username: 'root',
      authentication: { kind: 'password', password: 'ephemeral' },
      tags: ['本地'],
      terminal: { columns: 110, rows: 34, command: null },
    })).resolves.toMatchObject({ id: 'connection-1', tags: [] })
    expect(web.initializeBrowserSession).toHaveBeenCalled()
    expect(web.createConnection).toHaveBeenCalled()
  })

  it('translates browser polling into the shared terminal event stream', async () => {
    const events: unknown[] = []
    await expect(startIdeTerminal('connection-1', (event) => events.push(event))).resolves.toEqual({ sessionId: 'terminal-1' })
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(events).toEqual([
      { kind: 'output', sessionId: 'terminal-1', sequence: 1, dataBase64: 'aGk=' },
      { kind: 'closed', sessionId: 'terminal-1', exitStatus: 0 },
    ])
    await expect(writeIdeTerminal('terminal-1', 'pwd\n')).resolves.toBeUndefined()
    expect(web.writeTerminal).toHaveBeenCalledWith('terminal-1', 'pwd\n')
  })
})
