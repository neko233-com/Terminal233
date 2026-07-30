import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { WebConnectionInput } from '../lib/web'
import { clampWindowPosition, WebMacWorkspace } from './WebMacWorkspace'

const api = vi.hoisted(() => ({
  initializeBrowserSession: vi.fn(),
  onBrowserSessionInvalidated: vi.fn(),
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

let browserSessionInvalidationListener: (() => void) | null = null

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('ResizeObserver', TestResizeObserver)
  browserSessionInvalidationListener = null
  api.initializeBrowserSession.mockResolvedValue(undefined)
  api.onBrowserSessionInvalidated.mockImplementation((listener: () => void) => {
    browserSessionInvalidationListener = listener
    return () => {
      if (browserSessionInvalidationListener === listener) browserSessionInvalidationListener = null
    }
  })
  api.releaseBrowserSession.mockReturnValue(undefined)
  api.createConnection.mockResolvedValue({
    id: 'connection-1',
    label: '测试主机',
    host: 'example.test',
    port: 22,
    username: 'root',
    expiresInSeconds: 1800,
  })
  api.disconnectConnection.mockResolvedValue(undefined)
  api.prepareConnection.mockResolvedValue({
    status: 'trusted',
    host: 'example.test',
    port: 22,
    keyAlgorithm: 'ssh-ed25519',
    fingerprint: 'SHA256:test',
  })
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
  api.createDirectory.mockResolvedValue(undefined)
  api.renameRemote.mockResolvedValue(undefined)
  api.prepareDelete.mockResolvedValue({ confirmationId: 'delete-1', expiresInSeconds: 60 })
  api.commitDelete.mockResolvedValue(undefined)
  api.uploadFiles.mockResolvedValue({ uploaded: 1, bytes: 128 })
  api.downloadRemote.mockResolvedValue(undefined)
  api.startTerminal.mockResolvedValue({ terminalId: 'terminal-1' })
  api.writeTerminal.mockResolvedValue(undefined)
  api.resizeTerminal.mockResolvedValue(undefined)
  api.pollTerminal.mockResolvedValue({
    cursor: 0,
    chunks: [],
    truncated: false,
    closed: true,
    exitStatus: 0,
    error: null,
  })
  api.closeTerminal.mockResolvedValue(undefined)
  api.createAgentGrant.mockResolvedValue({ grant: 'grant-1', expiresInSeconds: 300 })
})

async function openTrustedDesktop() {
  const user = userEvent.setup()
  render(<WebMacWorkspace />)
  await user.type(await screen.findByRole('textbox', { name: '主机地址' }), 'example.test')
  await user.type(screen.getByLabelText('SSH 密码'), 'secret')
  await user.click(screen.getByRole('button', { name: /连接并打开桌面/ }))
  await screen.findByRole('button', { name: /report\.txt/ })
  return user
}

describe('Web macOS SSH workspace', () => {
  it('lets the user retry browser-session initialization after a local service failure', async () => {
    const user = userEvent.setup()
    api.initializeBrowserSession.mockRejectedValueOnce(new Error('offline'))

    render(<WebMacWorkspace />)

    const retry = await screen.findByRole('button', { name: '重新连接本地服务' })
    expect(screen.getByRole('button', { name: /连接并打开桌面/ })).toBeDisabled()
    await user.click(retry)

    await waitFor(() => {
      expect(api.initializeBrowserSession).toHaveBeenCalledTimes(2)
      expect(screen.queryByRole('button', { name: '重新连接本地服务' })).not.toBeInTheDocument()
    })
  })

  it('returns to the connection sheet when a desktop API reports an expired browser session', async () => {
    const user = await openTrustedDesktop()

    act(() => browserSessionInvalidationListener?.())

    expect(await screen.findByText('本地会话已过期，请重新连接本地 SSH 服务。')).toBeInTheDocument()
    const retry = screen.getByRole('button', { name: '重新连接本地服务' })
    await user.click(retry)

    await waitFor(() => expect(api.initializeBrowserSession).toHaveBeenCalledTimes(2))
    expect(screen.queryByRole('button', { name: '重新连接本地服务' })).not.toBeInTheDocument()
  })

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

  it('submits pasted private-key authentication and clears the form copy immediately', async () => {
    const user = userEvent.setup()
    let captured: WebConnectionInput | null = null
    api.createConnection.mockImplementation((input: WebConnectionInput) => {
      captured = structuredClone(input)
      return Promise.reject(new Error('fixture stop'))
    })

    render(<WebMacWorkspace />)
    await user.type(await screen.findByRole('textbox', { name: '主机地址' }), 'example.test')
    await user.selectOptions(screen.getByRole('combobox', { name: '认证方式' }), 'private_key')
    const privateKey = screen.getByRole('textbox', { name: 'SSH 私钥' })
    const passphrase = screen.getByLabelText('私钥口令（可选）')
    fireEvent.change(privateKey, { target: { value: '-----BEGIN OPENSSH PRIVATE KEY-----\nkey-data' } })
    await user.type(passphrase, 'key-secret')
    await user.click(screen.getByRole('button', { name: /连接并打开桌面/ }))

    expect(await screen.findByRole('alert')).toHaveTextContent('操作没有完成')
    expect(captured).toEqual(expect.objectContaining({
      authentication: {
        kind: 'private_key',
        privateKeyPem: '-----BEGIN OPENSSH PRIVATE KEY-----\nkey-data',
        passphrase: 'key-secret',
      },
    }))
    expect(screen.getByRole('textbox', { name: 'SSH 私钥' })).toHaveValue('')
    expect(screen.getByLabelText('私钥口令（可选）')).toHaveValue('')
  })

  it('supports keyboard context-menu navigation, viewport clamping, and focus return', async () => {
    const user = await openTrustedDesktop()
    const file = screen.getByRole('button', { name: /report\.txt/ })

    file.focus()
    fireEvent.keyDown(file, { key: 'F10', shiftKey: true })
    const menu = await screen.findByRole('menu', { name: 'report.txt 操作' })
    const download = within(menu).getByRole('menuitem', { name: '下载文件' })
    const rename = within(menu).getByRole('menuitem', { name: '重命名' })
    const remove = within(menu).getByRole('menuitem', { name: '删除' })
    await waitFor(() => expect(download).toHaveFocus())

    await user.keyboard('{ArrowDown}')
    expect(rename).toHaveFocus()
    await user.keyboard('{End}')
    expect(remove).toHaveFocus()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(file).toHaveFocus())

    fireEvent.contextMenu(file, {
      clientX: window.innerWidth + 100,
      clientY: window.innerHeight + 100,
    })
    const clampedMenu = await screen.findByRole('menu', { name: 'report.txt 操作' })
    expect(clampedMenu).toHaveStyle({
      left: `${window.innerWidth - 168}px`,
      top: `${window.innerHeight - 120}px`,
    })
  })

  it('locks Finder mutations while pending and keeps failures visible in the dialog', async () => {
    const user = await openTrustedDesktop()
    let finishCreate!: () => void
    api.createDirectory.mockImplementation(() => new Promise<void>((resolve) => {
      finishCreate = resolve
    }))

    await user.click(screen.getByRole('button', { name: '新建文件夹' }))
    let dialog = await screen.findByRole('dialog', { name: '新建远端文件夹' })
    await user.type(within(dialog).getByRole('textbox', { name: '文件夹名称' }), 'logs')
    await user.click(within(dialog).getByRole('button', { name: '确认' }))

    expect(within(dialog).getByRole('button', { name: '处理中…' })).toBeDisabled()
    await user.click(within(dialog).getByRole('button', { name: '处理中…' }))
    expect(api.createDirectory).toHaveBeenCalledTimes(1)
    finishCreate()
    await screen.findByText('已创建文件夹“logs”。')

    api.createDirectory.mockRejectedValueOnce(new Error('mkdir failed'))
    await user.click(screen.getByRole('button', { name: '新建文件夹' }))
    dialog = await screen.findByRole('dialog', { name: '新建远端文件夹' })
    await user.type(within(dialog).getByRole('textbox', { name: '文件夹名称' }), 'broken')
    await user.click(within(dialog).getByRole('button', { name: '确认' }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('操作没有完成')
    expect(dialog).toBeVisible()
  })

  it('offers a manual terminal reopen and closes the previous terminal session', async () => {
    api.startTerminal
      .mockResolvedValueOnce({ terminalId: 'terminal-1' })
      .mockResolvedValueOnce({ terminalId: 'terminal-2' })
    const user = await openTrustedDesktop()

    const reopen = await screen.findByRole('button', { name: '重新打开终端' })
    await user.click(reopen)

    await waitFor(() => expect(api.startTerminal).toHaveBeenCalledTimes(2))
    expect(api.closeTerminal).toHaveBeenCalledWith('terminal-1')
    expect(api.startTerminal).toHaveBeenNthCalledWith(2, 'connection-1', 110, 34)
  })
})

describe('Web macOS window geometry', () => {
  it('keeps the measured window fully inside a resized viewport', () => {
    expect(clampWindowPosition(
      { x: 900, y: 700 },
      650,
      590,
      1000,
      760,
    )).toEqual({ x: 342, y: 162 })

    expect(clampWindowPosition(
      { x: -100, y: -100 },
      440,
      500,
      1000,
      760,
    )).toEqual({ x: 8, y: 42 })
  })
})
