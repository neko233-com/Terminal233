import { AppWindow, ChevronLeft, ChevronRight, Cloud, Folder, Globe2, Monitor, TerminalSquare, Wifi } from 'lucide-react'
import { useState } from 'react'
import type { ReactNode } from 'react'
import type { Host } from '../data'

interface DesktopWorkspaceProps {
  host: Host
  onOpenBrowser: () => void
  onOpenTerminal: () => void
}

type DesktopWindow = 'finder' | 'terminal'

export function DesktopWorkspace({ host, onOpenBrowser, onOpenTerminal }: DesktopWorkspaceProps) {
  const [focusedWindow, setFocusedWindow] = useState<DesktopWindow>('terminal')

  return (
    <section className="workspace desktop-workspace" aria-label={`${host.name} 桌面预览`}>
      <header className="desktop-menu-bar">
        <div className="desktop-menubar-left">
          <strong>nekossh</strong>
          <span>文件</span>
          <span>编辑</span>
          <span>视图</span>
          <span>连接</span>
        </div>
        <div className="desktop-menubar-right">
          <Wifi aria-hidden="true" size={17} />
          <span>视觉预览</span>
        </div>
      </header>

      <div className="desktop-canvas">
        <div className="desktop-canvas-glow" />
        <p className="desktop-preview-banner">仅视觉预览 · NekoSSH 不提供远程桌面协议，也未建立 SSH 会话</p>
        <div className="desktop-shortcuts" aria-label="桌面预览快捷方式">
          <button className="desktop-shortcut" type="button">
            <Folder aria-hidden="true" size={29} />
            <span>文稿</span>
          </button>
          <button className="desktop-shortcut" onClick={onOpenBrowser} type="button">
            <Globe2 aria-hidden="true" size={29} />
            <span>浏览器</span>
          </button>
        </div>

        <div className={`remote-window finder-window ${focusedWindow === 'finder' ? 'is-focused' : ''}`}>
          <WindowTitle title="已保存的 SSH 目标（预览）" onFocus={() => setFocusedWindow('finder')} />
          <div className="finder-toolbar">
            <button aria-label="后退" type="button"><ChevronLeft size={17} /></button>
            <button aria-label="前进" type="button"><ChevronRight size={17} /></button>
            <span>搜索主机</span>
          </div>
          <div className="finder-list">
            <FinderRow active detail={`${host.user}@${host.address}:${host.port} · 未连接`} name={host.name} />
            <FinderRow detail="通过左侧主机列表管理加密配置" name="本地 Vault" />
          </div>
          <footer className="finder-footer"><span>仅展示本地资料</span><Cloud size={16} /></footer>
        </div>

        <div className={`remote-window remote-terminal-window ${focusedWindow === 'terminal' ? 'is-focused' : ''}`}>
          <WindowTitle title={`${host.name} — SSH 视觉预览`} onFocus={() => setFocusedWindow('terminal')} />
          <div className="remote-terminal-tab"><i className="idle-dot" /> 预览 · 无远程会话 <span>×　＋</span></div>
          <div className="remote-terminal-content" onClick={() => setFocusedWindow('terminal')}>
            <p><b>NekoSSH</b> % 这是桌面视觉预览</p>
            <p>不会读取远端文件、执行命令或模拟远端结果。</p>
            <p>请选择“终端”工作区，并在桌面端启动系统 SSH。</p>
            <p className="remote-terminal-prompt"><b>preview</b> % <i /></p>
          </div>
          <footer className="remote-terminal-footer"><span><i className="idle-dot" /> 未建立 SSH 会话</span><span>预览　☰</span></footer>
        </div>
      </div>

      <nav className="desktop-dock" aria-label="桌面快捷栏">
        <DockButton active icon={<Monitor size={29} />} label="主机" />
        <DockButton icon={<TerminalSquare size={31} />} label="终端" onClick={onOpenTerminal} />
        <DockButton icon={<Globe2 size={32} />} label="浏览器" onClick={onOpenBrowser} />
        <DockButton icon={<Cloud size={30} />} label="同步" />
        <DockButton icon={<AppWindow size={30} />} label="窗口" />
      </nav>
    </section>
  )
}

function WindowTitle({ title, onFocus }: { title: string; onFocus: () => void }) {
  return (
    <header className="remote-window-title" onMouseDown={onFocus}>
      <span className="traffic-lights" aria-hidden="true"><i /><i /><i /></span>
      <span>{title}</span>
      <span aria-hidden="true" />
    </header>
  )
}

function FinderRow({ active = false, detail, name }: { active?: boolean; detail: string; name: string }) {
  return (
    <div className={`finder-row ${active ? 'active' : ''}`}>
      <Monitor aria-hidden="true" size={22} />
      <span><b>{name}</b><small>{detail}</small></span>
      <i className="idle-dot" />
    </div>
  )
}

function DockButton({ active = false, icon, label, onClick }: { active?: boolean; icon: ReactNode; label: string; onClick?: () => void }) {
  return (
    <button aria-label={label} className={`dock-button ${active ? 'is-active' : ''}`} onClick={onClick} type="button">
      {icon}
      <span>{label}</span>
      {active ? <i aria-hidden="true" /> : null}
    </button>
  )
}
