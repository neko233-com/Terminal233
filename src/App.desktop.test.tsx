import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const tauri = vi.hoisted(() => ({
  invoke: vi.fn(),
  isTauri: vi.fn(() => true),
}))

vi.mock('@tauri-apps/api/core', () => ({
  invoke: tauri.invoke,
  isTauri: tauri.isTauri,
}))

import App from './App'

const profile = {
  id: '2ec3a3b7-10d3-4cca-b2af-0d818feadf79',
  label: 'production',
  host: 'ssh.example.test',
  port: 2222,
  username: 'neko',
  authenticationKind: 'agent',
  tags: ['Production'],
  terminal: { columns: 120, rows: 32, command: null },
  createdAtUnixMs: 1,
  updatedAtUnixMs: 1,
}

describe('NekoSSH desktop workspace', () => {
  beforeEach(() => {
    tauri.invoke.mockReset()
    tauri.isTauri.mockReset()
    tauri.isTauri.mockReturnValue(true)
    tauri.invoke.mockImplementation((command: string) => {
      if (command === 'vault_status') return Promise.resolve({ exists: true, unlocked: true, format: 'neko233-v1', vaultPathHint: 'vault' })
      if (command === 'profile_list') return Promise.resolve([profile])
      if (command === 'sync_configuration') return Promise.resolve(null)
      if (command === 'profile_create') return Promise.resolve(profile)
      if (command === 'ssh_launch_external') return Promise.resolve(4242)
      if (command === 'sync_configure') return Promise.resolve({
        kind: 'git',
        endpointOrRemote: 'https://github.com/neko/nekossh-vault.git',
        remotePath: null,
        branch: 'main',
        provider: 'github',
        credentialConfigured: true,
      })
      if (command === 'sync_git_upload') return Promise.resolve({
        kind: 'git',
        operation: 'upload',
        encryptedBundleSha256: 'sha256:example',
        bytes: 4096,
      })
      return Promise.resolve(undefined)
    })
  })

  it('loads Vault profiles and launches the validated system SSH command', async () => {
    const user = userEvent.setup()
    render(<App />)

    expect(await screen.findByRole('button', { name: 'Vault 已解锁' })).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: '未连接 production neko@ssh.example.test' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '打开会话' }))
    await user.click(screen.getByRole('button', { name: /启动系统 SSH/ }))

    await waitFor(() => {
      expect(tauri.invoke).toHaveBeenCalledWith('ssh_launch_external', { profileId: profile.id })
    })
    expect(await screen.findByRole('status')).toHaveTextContent('系统 OpenSSH 已启动')
  })

  it('opens a desktop-only connection form and sends Agent profiles through profile_create', async () => {
    const user = userEvent.setup()
    render(<App />)

    await screen.findByRole('button', { name: 'Vault 已解锁' })
    await user.click(screen.getByRole('button', { name: '新建 SSH' }))
    expect(screen.getByRole('dialog', { name: '新建 SSH 连接' })).toBeInTheDocument()

    await user.type(screen.getByLabelText('连接名称'), 'lab')
    await user.type(screen.getByLabelText('连接分组'), '实验环境')
    await user.type(screen.getByLabelText('主机地址'), 'lab.example.test')
    await user.type(screen.getByLabelText('SSH 用户名'), 'neko')
    await user.click(screen.getByRole('button', { name: '保存到加密 Vault' }))

    await waitFor(() => {
      expect(tauri.invoke).toHaveBeenCalledWith('profile_create', {
        input: {
          label: 'lab',
          host: 'lab.example.test',
          port: 22,
          username: 'neko',
          authentication: { kind: 'agent' },
          tags: ['实验环境'],
          terminal: { columns: 120, rows: 32, command: null },
        },
      })
    })
    expect(screen.queryByRole('dialog', { name: '新建 SSH 连接' })).not.toBeInTheDocument()
  })

  it('uploads an encrypted Git backup through the Rust bridge after configuration', async () => {
    const user = userEvent.setup()
    render(<App />)

    await screen.findByRole('button', { name: 'Vault 已解锁' })
    await user.click(screen.getByRole('button', { name: '未配置云同步' }))
    await user.type(screen.getByLabelText('Git 访问令牌'), 'github-fine-grained-token')
    await user.click(screen.getByRole('button', { name: '保存加密配置' }))

    await waitFor(() => {
      expect(tauri.invoke).toHaveBeenCalledWith('sync_git_upload', undefined)
    })
    expect(await screen.findByRole('status')).toHaveTextContent('加密备份已推送到 Git 私有仓库')
  })
})
