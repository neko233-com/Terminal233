import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import TauriIdeApp from './TauriIdeApp'

const ide = vi.hoisted(() => ({
  acknowledgeIdeDownloadChunk: vi.fn(),
  acknowledgeIdeTerminalOutput: vi.fn(),
  cancelIdeTransfer: vi.fn(),
  closeIdeTerminal: vi.fn(),
  createIdeConnection: vi.fn(),
  createIdeDirectory: vi.fn(),
  deleteIdeRemote: vi.fn(),
  disconnectIdeConnection: vi.fn(),
  downloadIdeFiles: vi.fn(),
  listIdeConnections: vi.fn(),
  listIdeDirectory: vi.fn(),
  onIdeTransferComplete: vi.fn(),
  onIdeTransferProgress: vi.fn(),
  prepareIdeConnection: vi.fn(),
  rejectIdeHostKey: vi.fn(),
  renameIdeRemote: vi.fn(),
  resizeIdeTerminal: vi.fn(),
  startIdeTerminal: vi.fn(),
  trustIdeHostKey: vi.fn(),
  uploadIdeFiles: vi.fn(),
}))

vi.mock('./lib/ide', () => ({
  ...ide,
  decodeBase64Bytes: (value: string) => Uint8Array.from(
    atob(value),
    (character) => character.charCodeAt(0),
  ),
  ideError: (error: unknown) => error instanceof Error
    ? error.message
    : (error as { message?: string } | null)?.message ?? '操作失败。',
  IdeTerminalInputQueue: class {
    enqueue() {
      return true
    }

    close() {}
  },
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

    write(_data: unknown, callback?: () => void) {
      callback?.()
    }

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

const connection = {
  id: 'connection-1',
  label: '测试服务器',
  host: 'example.test',
  port: 22,
  username: 'root',
  tags: ['个人服务器'],
  expiresInSeconds: 1_800,
}

const trustedHost = {
  status: 'trusted' as const,
  host: connection.host,
  port: connection.port,
  keyAlgorithm: 'ssh-ed25519',
  fingerprint: 'SHA256:trusted-host',
}

function deferred<Value>() {
  let resolve!: (value: Value) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<Value>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, reject, resolve }
}

async function openCreateDialog(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByText('还没有 SSH 连接')
  await user.click(screen.getAllByRole('button', { name: '新建连接' })[0])
  return screen.getByRole('dialog', { name: '新建临时 SSH 连接' })
}

async function openWorkspace(user: ReturnType<typeof userEvent.setup>) {
  const row = await screen.findByRole('row', { name: /测试服务器/ })
  await user.click(within(row).getByRole('button', { name: '连接' }))
  return screen.findByRole('region', { name: '交互式 SSH 终端' })
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.stubGlobal('ResizeObserver', TestResizeObserver)
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
    callback(0)
    return 1
  })
  vi.stubGlobal('cancelAnimationFrame', vi.fn())

  ide.listIdeConnections.mockResolvedValue([])
  ide.disconnectIdeConnection.mockResolvedValue(undefined)
  ide.prepareIdeConnection.mockResolvedValue(trustedHost)
  ide.rejectIdeHostKey.mockResolvedValue(undefined)
  ide.trustIdeHostKey.mockResolvedValue(trustedHost)
  ide.listIdeDirectory.mockResolvedValue({
    path: '/',
    entries: [],
    truncated: false,
  })
  ide.onIdeTransferProgress.mockResolvedValue(vi.fn())
  ide.onIdeTransferComplete.mockResolvedValue(vi.fn())
  ide.startIdeTerminal.mockResolvedValue({ sessionId: 'terminal-1' })
  ide.acknowledgeIdeTerminalOutput.mockResolvedValue(undefined)
  ide.resizeIdeTerminal.mockResolvedValue(undefined)
  ide.closeIdeTerminal.mockResolvedValue(undefined)
  ide.cancelIdeTransfer.mockResolvedValue(undefined)
  ide.createIdeDirectory.mockResolvedValue(undefined)
  ide.renameIdeRemote.mockResolvedValue(undefined)
  ide.deleteIdeRemote.mockResolvedValue(undefined)
})

describe('Tauri SSH IDE', () => {
  it('prevents the WebView from persisting connection credentials through autofill', async () => {
    const user = userEvent.setup()
    render(<TauriIdeApp />)
    const dialog = await openCreateDialog(user)

    expect(dialog).toHaveAttribute('autocomplete', 'off')
    expect(within(dialog).getByLabelText('用户')).toHaveAttribute('autocomplete', 'off')
    expect(within(dialog).getByLabelText('密码')).toHaveAttribute('autocomplete', 'new-password')

    await user.selectOptions(within(dialog).getByRole('combobox', { name: '认证方式' }), 'private_key')
    expect(within(dialog).getByLabelText('私钥')).toHaveAttribute('autocomplete', 'off')
    expect(within(dialog).getByLabelText('私钥口令（可选）')).toHaveAttribute(
      'autocomplete',
      'new-password',
    )
  })

  it('rejects an out-of-range port without creating a connection', async () => {
    const user = userEvent.setup()
    render(<TauriIdeApp />)
    const dialog = await openCreateDialog(user)

    await user.type(within(dialog).getByLabelText('主机'), 'example.test')
    await user.type(within(dialog).getByLabelText('密码'), 'secret')
    const port = within(dialog).getByLabelText('端口')
    await user.clear(port)
    await user.type(port, '65536')
    await user.click(within(dialog).getByRole('button', { name: '连接' }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      '端口必须是 1 到 65535 之间的整数。',
    )
    expect(ide.createIdeConnection).not.toHaveBeenCalled()
  })

  it('discards a late prepare result after closing progress and rejects its confirmation', async () => {
    const user = userEvent.setup()
    const prepare = deferred<{
      status: 'confirmation_required'
      confirmationId: string
      host: string
      port: number
      keyAlgorithm: string
      fingerprint: string
    }>()
    ide.listIdeConnections.mockResolvedValue([connection])
    ide.prepareIdeConnection.mockReturnValue(prepare.promise)

    render(<TauriIdeApp />)
    const row = await screen.findByRole('row', { name: /测试服务器/ })
    await user.click(within(row).getByRole('button', { name: '连接' }))
    const progress = await screen.findByRole('dialog', { name: 'SSH 连接' })
    const closeButtons = within(progress).getAllByRole('button', { name: '关闭' })
    await user.click(closeButtons[closeButtons.length - 1])
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'SSH 连接' })).not.toBeInTheDocument())

    await act(async () => {
      prepare.resolve({
        status: 'confirmation_required',
        confirmationId: 'confirmation-late',
        host: connection.host,
        port: connection.port,
        keyAlgorithm: 'ssh-ed25519',
        fingerprint: 'SHA256:late-host',
      })
      await prepare.promise
    })

    await waitFor(() => expect(ide.rejectIdeHostKey).toHaveBeenCalledWith('confirmation-late'))
    expect(screen.queryByRole('region', { name: '交互式 SSH 终端' })).not.toBeInTheDocument()
    expect(screen.getByRole('row', { name: /测试服务器/ })).toBeVisible()
  })

  it('passes private-key authentication exactly once and clears key material immediately', async () => {
    const user = userEvent.setup()
    const create = deferred<typeof connection>()
    ide.createIdeConnection.mockReturnValue(create.promise)

    render(<TauriIdeApp />)
    const dialog = await openCreateDialog(user)
    await user.type(within(dialog).getByLabelText('主机'), 'key.example.test')
    await user.clear(within(dialog).getByLabelText('端口'))
    await user.type(within(dialog).getByLabelText('端口'), '2222')
    await user.selectOptions(within(dialog).getByRole('combobox', { name: '认证方式' }), 'private_key')
    const privateKey = within(dialog).getByLabelText('私钥')
    const passphrase = within(dialog).getByLabelText('私钥口令（可选）')
    await user.type(privateKey, '-----BEGIN OPENSSH PRIVATE KEY-----\\ntest-key')
    await user.type(passphrase, 'key-secret')
    await user.click(within(dialog).getByRole('button', { name: '连接' }))

    await waitFor(() => expect(ide.createIdeConnection).toHaveBeenCalledWith({
      label: 'key.example.test',
      host: 'key.example.test',
      port: 2222,
      username: 'root',
      authentication: {
        kind: 'private_key',
        privateKeyPem: '-----BEGIN OPENSSH PRIVATE KEY-----\\ntest-key',
        passphrase: 'key-secret',
      },
      tags: ['个人服务器'],
      terminal: { columns: 110, rows: 34, command: null },
    }))
    expect(privateKey).toHaveValue('')
    expect(passphrase).toHaveValue('')

    await user.click(within(dialog).getByRole('button', { name: '关闭' }))
    await act(async () => {
      create.resolve(connection)
      await create.promise
    })
    await waitFor(() => expect(ide.disconnectIdeConnection).toHaveBeenCalledWith(connection.id))
  })

  it('deletes the absolute SFTP path captured when the file menu opens', async () => {
    const user = userEvent.setup()
    ide.listIdeConnections.mockResolvedValue([connection])
    ide.listIdeDirectory.mockImplementation((_connectionId: string, path: string) => Promise.resolve(
      path === '/archive'
        ? {
            path: '/archive',
            entries: [{ name: 'report.txt', isDirectory: false, size: 128, modifiedAtUnix: null }],
            truncated: false,
          }
        : {
            path: '/',
            entries: [{ name: 'archive', isDirectory: true, size: null, modifiedAtUnix: null }],
            truncated: false,
          },
    ))

    render(<TauriIdeApp />)
    await openWorkspace(user)
    const folder = await screen.findByRole('button', { name: /archive，文件夹/ })
    await user.dblClick(folder)
    const report = await screen.findByRole('button', { name: /report\.txt，文件/ })

    fireEvent.contextMenu(report, { clientX: 120, clientY: 80 })
    const menu = await screen.findByRole('menu', { name: 'report.txt 操作' })
    await user.click(within(menu).getByRole('menuitem', { name: '删除' }))
    const dialog = await screen.findByRole('dialog', { name: '确认删除' })
    expect(within(dialog).getByText('/archive/report.txt')).toBeVisible()
    await user.click(within(dialog).getByRole('button', { name: '确认删除' }))

    await waitFor(() => expect(ide.deleteIdeRemote).toHaveBeenCalledWith(
      connection.id,
      '/archive/report.txt',
    ))
  })
})
