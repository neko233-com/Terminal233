import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { PasswordDialog, RestoreDialog, SyncDialog } from './VaultDialogs'

describe('Vault dialogs', () => {
  it('validates a password change before calling the desktop callback', async () => {
    const user = userEvent.setup()
    const submit = vi.fn().mockResolvedValue(undefined)
    render(<PasswordDialog mode="change" onClose={vi.fn()} onSubmit={submit} />)

    await user.type(screen.getByLabelText('当前主密码'), 'current-password')
    await user.type(screen.getByLabelText('新主密码'), 'a-strong-new-password')
    await user.type(screen.getByLabelText('确认新主密码'), 'not-the-same')
    await user.click(screen.getByRole('button', { name: '更新主密码' }))

    expect(screen.getByRole('status')).toHaveTextContent('两次输入的新主密码不一致')
    expect(submit).not.toHaveBeenCalled()

    await user.clear(screen.getByLabelText('确认新主密码'))
    await user.type(screen.getByLabelText('确认新主密码'), 'a-strong-new-password')
    await user.click(screen.getByRole('button', { name: '更新主密码' }))

    expect(submit).toHaveBeenCalledWith({ currentPassword: 'current-password', newPassword: 'a-strong-new-password' })
    expect(await screen.findByRole('status')).toHaveTextContent('主密码已更新')
  })

  it('requires WebDAV credentials and forwards them only to the supplied save callback', async () => {
    const user = userEvent.setup()
    const saved = vi.fn().mockResolvedValue('加密备份已上传（4.0 KB）；远端只会收到不可读的 Vault 密文。')
    render(<SyncDialog defaultProvider="webdav" desktopAvailable onClose={vi.fn()} onSaved={saved} />)

    await user.clear(screen.getByLabelText('WebDAV 地址'))
    await user.type(screen.getByLabelText('WebDAV 地址'), 'https://dav.example.test/nekossh')
    await user.click(screen.getByRole('button', { name: '保存加密配置' }))
    expect(screen.getByRole('status')).toHaveTextContent('请输入 WebDAV 密码或应用专用令牌')

    await user.type(screen.getByLabelText('WebDAV 凭据'), 'app-token')
    await user.click(screen.getByRole('button', { name: '保存加密配置' }))

    expect(saved).toHaveBeenCalledWith({
      provider: 'webdav',
      endpoint: 'https://dav.example.test/nekossh',
      remotePath: 'nekossh/vault.bundle',
      username: '',
      secret: 'app-token',
    })
    expect(await screen.findByRole('status')).toHaveTextContent('加密备份已上传（4.0 KB）')
  })

  it('requires an HTTPS Git access token instead of offering an unverified SSH-agent fallback', async () => {
    const user = userEvent.setup()
    const saved = vi.fn().mockResolvedValue('Git 加密备份已上传。')
    render(<SyncDialog defaultProvider="git" desktopAvailable onClose={vi.fn()} onSaved={saved} />)

    expect(screen.getByLabelText('仓库地址')).toHaveValue('https://github.com/neko/nekossh-vault.git')
    await user.click(screen.getByRole('button', { name: '保存加密配置' }))
    expect(screen.getByRole('status')).toHaveTextContent('请输入 Git 私有仓库的访问令牌')
    expect(saved).not.toHaveBeenCalled()
  })

  it('requires an explicit confirmation before a password-verified restore', async () => {
    const user = userEvent.setup()
    const restore = vi.fn().mockResolvedValue({ rollbackAvailable: true })
    render(<RestoreDialog onClose={vi.fn()} onRestore={restore} providerLabel="WebDAV" />)

    await user.type(screen.getByLabelText('备份 Vault 的主密码'), 'recovery-password')
    await user.click(screen.getByRole('button', { name: '验证并恢复' }))
    expect(screen.getByRole('status')).toHaveTextContent('请确认你理解恢复会替换当前本地 Vault')
    expect(restore).not.toHaveBeenCalled()

    await user.click(screen.getByLabelText('确认替换本地 Vault'))
    await user.click(screen.getByRole('button', { name: '验证并恢复' }))

    expect(restore).toHaveBeenCalledWith('recovery-password')
    expect(await screen.findByRole('status')).toHaveTextContent('已保留可回滚的加密副本')
  })
})
