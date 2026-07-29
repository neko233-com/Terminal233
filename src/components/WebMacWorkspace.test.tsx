import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { WebMacWorkspace } from './WebMacWorkspace'

const api = vi.hoisted(() => ({
  initializeBrowserSession: vi.fn(),
  releaseBrowserSession: vi.fn(),
  createConnection: vi.fn(),
  disconnectConnection: vi.fn(),
  prepareConnection: vi.fn(),
  trustConnection: vi.fn(),
  listDirectory: vi.fn(),
  createDirectory: vi.fn(),
  renameRemote: vi.fn(),
  prepareDelete: vi.fn(),
  commitDelete: vi.fn(),
  uploadFiles: vi.fn(),
  downloadRemote: vi.fn(),
  startTerminal: vi.fn(),
  writeTerminal: vi.fn(),
  resizeTerminal: vi.fn(),
  pollTerminal: vi.fn(),
  closeTerminal: vi.fn(),
  createAgentGrant: vi.fn(),
}))

vi.mock('../lib/web', () => ({
  ...api,
  WebApiError: class WebApiError extends Error {
    code: string

    constructor(code: string, message: string) {
      super(message)
      this.code = code
    }
  },
  decodeBase64: (value: string) => Uint8Array.from(atob(value), (character) => character.charCodeAt(0)),
}))

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit() {}
  },
}))

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 110
    rows = 34
    loadAddon() {}
    open() {}
    write(_data: unknown, callback?: () => void) { callback?.() }
    focus() {}
    dispose() {}
    onData() {
      return { dispose() {} }
    }
  },
}))

class TestResizeObserver {
  observe() {}
  disconnect() {}
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.stubGlobal('ResizeObserver', TestResizeObserver)
  api.initializeBrowserSession.mockResolvedValue(undefined)
  api.disconnectConnection.mockResolvedValue(undefined)
  api.trustConnection.mockResolvedValue({
    status: 'trusted',
    host: 'example.test',
    port: 22,
    keyAlgorithm: 'ssh-ed25519',
    fingerprint: 'SHA256:test',
  })
  api.listDirectory.mockResolvedValue({
    path: '/',
    entries: [{ name: 'report.txt', isDirectory: false, size: 128, modifiedAtUnix: null }],
    truncated: false,
  })
  api.downloadRemote.mockResolvedValue(undefined)
  api.startTerminal.mockResolvedValue({ terminalId: 'terminal-1' })
  api.pollTerminal.mockResolvedValue({
    cursor: 0,
    chunks: [],
    truncated: false,
    closed: true,
    exitStatus: 0,
    error: null,
  })
  api.closeTerminal.mockResolvedValue(undefined)
})

describe('Web macOS SSH workspace', () => {
  it('rejects an invalid SSH port instead of silently falling back to 22', async () => {
    const user = userEvent.setup()
    api.createConnection.mockResolvedValue({
      id: 'connection-1',
      label: '测试主机',
      host: 'example.test',
      port: 2222,
      username: 'root',
      expiresInSeconds: 1800,
    })
    api.prepareConnection.mockResolvedValue({
      status: 'trusted',
      host: 'example.test',
      port: 2222,
      keyAlgorithm: 'ssh-ed25519',
      fingerprint: 'SHA256:test',
    })

    render(<WebMacWorkspace />)
    const connect = await screen.findByRole('button', { name: /连接并打开桌面/ })
    await user.type(screen.getByRole('textbox', { name: '主机地址' }), 'example.test')
    await user.type(screen.getByLabelText('SSH 密码'), 'secret')
    const port = screen.getByRole('spinbutton', { name: '端口' })
    await user.clear(port)
    await user.type(port, '70000')
    expect(connect).toBeDisabled()
    expect(api.createConnection).not.toHaveBeenCalled()

    await user.clear(port)
    await user.type(port, '2222')
    await user.click(connect)

    await waitFor(() => expect(api.createConnection).toHaveBeenCalledWith(
      expect.objectContaining({ port: 2222 }),
    ))
  })

  it('confirms the host key and exposes keyboard-operable Finder actions', async () => {
    const user = userEvent.setup()
    api.createConnection.mockResolvedValue({
      id: 'connection-1',
      label: '测试主机',
      host: 'example.test',
      port: 22,
      username: 'root',
      expiresInSeconds: 1800,
    })
    api.prepareConnection.mockResolvedValue({
      status: 'confirmation_required',
      confirmationId: 'confirmation-1',
      host: 'example.test',
      port: 22,
      keyAlgorithm: 'ssh-ed25519',
      fingerprint: 'SHA256:new-host',
    })

    render(<WebMacWorkspace />)
    await user.type(await screen.findByRole('textbox', { name: '主机地址' }), 'example.test')
    await user.type(screen.getByLabelText('SSH 密码'), 'secret')
    await user.click(screen.getByRole('button', { name: /连接并打开桌面/ }))

    expect(await screen.findByText('SHA256:new-host')).toBeVisible()
    await user.click(screen.getByRole('button', { name: /信任并继续/ }))

    const file = await screen.findByRole('button', { name: /report\.txt/ })
    file.focus()
    await user.keyboard('{Enter}')

    await waitFor(() => expect(api.downloadRemote).toHaveBeenCalledWith(
      'connection-1',
      '/report.txt',
    ))
    expect(api.startTerminal).toHaveBeenCalledWith('connection-1', 110, 34)
  })
})
