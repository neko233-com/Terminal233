import { ChevronLeft, FolderTree, Globe2, Monitor, PanelLeftClose, PanelLeftOpen, Plus, TerminalSquare, X } from 'lucide-react'
import { useState } from 'react'
import type { Host } from '../data'
import { terminalGreeting } from '../data'

interface TerminalWorkspaceProps {
  connectionStatus: string
  host: Host
  onBackToLibrary: () => void
  onOpenDesktop: () => void
  onOpenBrowser: () => void
}

export function TerminalWorkspace({ connectionStatus, host, onBackToLibrary, onOpenBrowser, onOpenDesktop }: TerminalWorkspaceProps) {
  const [filePanelOpen, setFilePanelOpen] = useState(false)

  return (
    <section className="workspace terminal-workspace" aria-label={`${host.name} 会话预览`}>
      <header className="connection-tabs">
        <button aria-label="返回连接库" className="session-return" onClick={onBackToLibrary} type="button"><ChevronLeft aria-hidden="true" size={17} />连接库</button>
        <div className="connection-tab active-tab">
          <TerminalSquare aria-hidden="true" size={19} strokeWidth={1.65} />
          <span>{host.name}</span>
          <button aria-label="关闭当前连接" className="tab-icon" onClick={onBackToLibrary} type="button">
            <X aria-hidden="true" size={17} />
          </button>
        </div>
        <button aria-label="新建连接标签" className="tab-add" type="button">
          <Plus aria-hidden="true" size={20} />
        </button>
      </header>

      <nav className="mode-tabs session-tools" aria-label="当前连接视图">
        <button aria-pressed={filePanelOpen} onClick={() => setFilePanelOpen((current) => !current)} type="button">
          {filePanelOpen ? <PanelLeftClose aria-hidden="true" size={19} /> : <PanelLeftOpen aria-hidden="true" size={19} />}
          <span>文件</span>
        </button>
        <button aria-current="page" className="is-current" type="button">
          <span className="terminal-glyph">›_</span>
          <span>终端</span>
        </button>
        <button onClick={onOpenDesktop} type="button">
          <Monitor aria-hidden="true" size={19} strokeWidth={1.65} />
          <span>桌面预览</span>
        </button>
        <button onClick={onOpenBrowser} type="button"><Globe2 aria-hidden="true" size={19} /><span>Web 预览</span></button>
        <span className="mode-spacer" />
      </nav>

      <div className={`session-canvas ${filePanelOpen ? 'has-file-panel' : ''}`}>
        <aside aria-hidden={!filePanelOpen} aria-label="文件工具预览" className="session-file-panel">
          <div className="session-panel-heading"><FolderTree aria-hidden="true" size={18} /><strong>文件</strong></div>
          <div className="session-file-empty">
            <FolderTree aria-hidden="true" size={25} />
            <strong>远端文件未打开</strong>
            <span>当前版本不会模拟远端文件树。请先在系统 SSH 会话中完成连接与权限确认。</span>
          </div>
        </aside>
        <div className="terminal-surface">
          <p className="terminal-preview-note">
            {host.profileId
              ? '已读取加密保存的 SSH 配置；此处仍是安全预览。请在右侧使用“启动系统 SSH”打开真实传统命令行。'
              : '安全预览不会发起网络连接或执行本机命令。'}
          </p>
          <div aria-live="polite" className="terminal-output" role="log">
            {terminalGreeting.map((line, index) => {
              const isIdentity = line.startsWith('[ NekoSSH')
              return (
                <div className={`terminal-line ${isIdentity ? 'identity-line' : ''}`} key={`${line}-${index}`}>
                  {line || '\u00a0'}
                </div>
              )
            })}
          </div>
          <form aria-label="终端预览（只读）" className="terminal-prompt">
            <span className="prompt-user">preview</span>
            <span className="prompt-path">:~</span>
            <span className="prompt-symbol">%</span>
            <input
              aria-label="终端预览（只读）"
              disabled
              placeholder="真实命令会在独立系统终端中运行"
            />
          </form>
        </div>
      </div>

      <footer className="workspace-statusbar">
        <span className="status-copy"><i className="idle-dot" /> {connectionStatus}</span>
        <span>预览</span>
        <span className="status-menu" aria-hidden="true">☰</span>
      </footer>
    </section>
  )
}
