import { ChevronLeft, ChevronRight, ExternalLink, Globe2, LockKeyhole, MoreHorizontal, Plus, RefreshCw, ShieldCheck } from 'lucide-react'
import { useState } from 'react'
import type { Host } from '../data'
import { errorMessage, normalizeExternalBrowserUrl } from '../lib/desktop'

interface BrowserWorkspaceProps {
  desktopAvailable: boolean
  host: Host
  onOpenExternal: (url: string) => Promise<string>
}

export function BrowserWorkspace({ desktopAvailable, host, onOpenExternal }: BrowserWorkspaceProps) {
  const [address, setAddress] = useState('https://console.neko.home')
  const [loadedAddress, setLoadedAddress] = useState(address)
  const [isRefreshing, setIsRefreshing] = useState(false)
  const [isOpeningExternal, setIsOpeningExternal] = useState(false)
  const [browserMessage, setBrowserMessage] = useState<{ kind: 'error' | 'success'; text: string } | null>(null)

  function refresh() {
    setIsRefreshing(true)
    window.setTimeout(() => setIsRefreshing(false), 550)
  }

  function loadPreview() {
    try {
      const normalized = normalizeExternalBrowserUrl(address)
      setLoadedAddress(normalized)
      setBrowserMessage({ kind: 'success', text: '地址已验证；预览不会加载远端页面，请使用右侧按钮在外部浏览器中打开。' })
    } catch (error) {
      setBrowserMessage({ kind: 'error', text: errorMessage(error) })
    }
  }

  async function openInExternalBrowser() {
    setIsOpeningExternal(true)
    setBrowserMessage(null)
    try {
      const normalized = await onOpenExternal(address)
      setLoadedAddress(normalized)
      setBrowserMessage({
        kind: 'success',
        text: desktopAvailable ? '已交由系统默认浏览器打开。' : '已在浏览器中新建安全标签。',
      })
    } catch (error) {
      setBrowserMessage({ kind: 'error', text: errorMessage(error) })
    } finally {
      setIsOpeningExternal(false)
    }
  }

  return (
    <section className="workspace browser-workspace" aria-label={`${host.name} 浏览器预览`}>
      <header className="browser-tab-strip">
        <div className="browser-tab is-active"><Globe2 size={17} /> <span>安全浏览预览 · {host.name}</span><span>×</span></div>
        <button aria-label="新建浏览器标签" className="tab-add" type="button"><Plus size={19} /></button>
        <span className="mode-spacer" />
        <button aria-label="浏览器更多操作" className="mode-action" type="button"><MoreHorizontal size={20} /></button>
      </header>
      <form
        className="browser-address-row"
        onSubmit={(event) => {
          event.preventDefault()
          loadPreview()
        }}
      >
        <button aria-label="后退" className="browser-nav-button" type="button"><ChevronLeft size={19} /></button>
        <button aria-label="前进" className="browser-nav-button" type="button"><ChevronRight size={19} /></button>
        <button aria-label="刷新页面" className={`browser-nav-button ${isRefreshing ? 'is-spinning' : ''}`} onClick={refresh} type="button"><RefreshCw size={18} /></button>
        <label className="address-bar">
          <LockKeyhole aria-hidden="true" size={15} />
          <input aria-label="浏览器地址" onChange={(event) => setAddress(event.target.value)} value={address} />
        </label>
        <button aria-label="打开外部浏览器" className="browser-nav-button" disabled={isOpeningExternal} onClick={() => void openInExternalBrowser()} type="button"><ExternalLink size={18} /></button>
      </form>

      <main className="browser-page">
        <header className="web-console-header">
          <div className="web-console-brand"><span className="web-console-symbol">n</span><strong>NekoSSH 浏览器预览</strong></div>
          <span className="browser-url-state"><ShieldCheck size={16} /> 未嵌入远端内容</span>
        </header>
        <div className="web-console-layout">
          <nav className="web-console-nav" aria-label="浏览器安全说明">
            <span className="is-active">预览模式</span>
            <span>外部打开</span>
            <span>URL 校验</span>
          </nav>
          <article className="web-console-content">
            {browserMessage ? <p className={`browser-message ${browserMessage.kind}`} role="status">{browserMessage.text}</p> : null}
            <div className="web-page-heading">
              <div><p>安全浏览</p><h2>不会在 NekoSSH 中嵌入远端网页</h2></div>
              <button className="small-primary" disabled={isOpeningExternal} onClick={() => void openInExternalBrowser()} type="button">在外部浏览器打开</button>
            </div>
            <div className="service-table-wrap browser-safety-card">
              <p><strong>待打开地址</strong><code>{loadedAddress}</code></p>
              <p>为隔离不可信网页，NekoSSH 不会将它加载进拥有 Tauri IPC 权限的 WebView，也不会代替你登录、填表或保存网页凭据。</p>
              <ul>
                <li>只允许 HTTPS，或不含凭据的本机 HTTP 地址。</li>
                <li>桌面端交给系统默认浏览器；Web 预览以安全新标签打开。</li>
                <li>地址尚未被外部浏览器打开前，不代表已访问、已登录或已连接任何服务。</li>
              </ul>
            </div>
          </article>
        </div>
      </main>
    </section>
  )
}
