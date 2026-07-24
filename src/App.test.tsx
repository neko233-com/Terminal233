import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import App from './App'

describe('NekoSSH web workspace', () => {
  it('uses the connection library as the default entry and opens a terminal-first session', async () => {
    const user = userEvent.setup()
    render(<App />)

    const workspaceNavigation = screen.getByRole('navigation', { name: '主要工作区' })
    expect(within(workspaceNavigation).getByRole('button', { name: '连接库' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByLabelText('SSH 连接库')).toBeInTheDocument()
    expect(screen.queryByText('CPU')).not.toBeInTheDocument()

    await user.type(screen.getByRole('textbox', { name: '筛选 SSH 连接' }), 'prod')
    expect(screen.getByRole('button', { name: 'prod-web-01 ubuntu' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'gitea-build runner' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '打开会话' }))
    expect(screen.getByLabelText('prod-web-01 会话预览')).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: '终端预览（只读）' })).toBeDisabled()
    expect(screen.getByText('此区域不会建立 SSH 网络连接，也不会执行本机或远端命令。')).toBeInTheDocument()
    expect(screen.getByLabelText('文件工具预览')).toHaveAttribute('aria-hidden', 'true')

    await user.click(screen.getByRole('button', { name: '文件' }))
    expect(screen.getByLabelText('文件工具预览')).toHaveAttribute('aria-hidden', 'false')
    expect(screen.getByText('远端文件未打开')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '桌面预览' }))
    expect(screen.getByLabelText('prod-web-01 桌面预览')).toBeInTheDocument()
    expect(screen.getByText('未建立 SSH 会话')).toBeInTheDocument()

    await user.click(within(workspaceNavigation).getByRole('button', { name: '会话' }))
    await user.click(screen.getByRole('button', { name: 'Web 预览' }))
    expect(screen.getByLabelText('prod-web-01 浏览器预览')).toBeInTheDocument()
    expect(screen.getByText('不会在 NekoSSH 中嵌入远端网页')).toBeInTheDocument()
  })

  it('does not collect Vault or cloud credentials in the web preview', async () => {
    const user = userEvent.setup()
    render(<App />)

    await user.click(screen.getByRole('button', { name: 'Vault Web 预览' }))
    expect(screen.getByRole('dialog', { name: 'Vault 仅在桌面端可用' })).toBeInTheDocument()
    expect(screen.queryByLabelText('当前主密码')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '知道了' }))

    await user.click(screen.getByRole('button', { name: '未配置云同步' }))
    expect(screen.getByRole('dialog', { name: '同步配置需要桌面端' })).toBeInTheDocument()
    expect(screen.queryByLabelText('WebDAV 凭据')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '知道了' }))

    await user.click(screen.getByText('新建 SSH', { exact: true, selector: 'button' }))
    expect(screen.getByRole('status')).toHaveTextContent('网页预览不会保存 SSH 凭据')
  })

  it('refuses unsafe browser URLs before opening an external tab', async () => {
    const user = userEvent.setup()
    const open = vi.spyOn(window, 'open')
    render(<App />)

    const workspaceNavigation = screen.getByRole('navigation', { name: '主要工作区' })
    await user.click(within(workspaceNavigation).getByRole('button', { name: '会话' }))
    await user.click(screen.getByRole('button', { name: 'Web 预览' }))
    const address = screen.getByRole('textbox', { name: '浏览器地址' })
    await user.clear(address)
    await user.type(address, 'javascript:alert(1)')
    await user.click(screen.getByRole('button', { name: '打开外部浏览器' }))

    expect(await screen.findByRole('status')).toHaveTextContent('只允许 HTTPS 地址')
    expect(open).not.toHaveBeenCalled()
  })

  it('explains that traditional SSH launch belongs to the desktop runtime', async () => {
    const user = userEvent.setup()
    render(<App />)

    await user.click(screen.getByRole('button', { name: '会话' }))
    await user.click(screen.getByRole('button', { name: /连接说明/ }))

    expect(screen.getByRole('status')).toHaveTextContent('网页预览不会创建 SSH 连接')
  })
})
