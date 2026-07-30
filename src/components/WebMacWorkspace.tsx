import {
  Activity,
  Bot,
  ChevronLeft,
  ChevronRight,
  CircleAlert,
  Copy,
  Download,
  FileText,
  Folder,
  FolderOpen,
  FolderPlus,
  HardDrive,
  Info,
  LoaderCircle,
  MonitorCog,
  MoreHorizontal,
  PanelTop,
  Plus,
  RefreshCw,
  Search,
  ShieldCheck,
  Terminal,
  Trash2,
  Upload,
  Wifi,
  X,
} from 'lucide-react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal as XTerm } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import {
  type ChangeEvent,
  type FormEvent,
  type KeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import {
  type AgentGrant,
  type ConnectionSummary,
  type DirectoryEntry,
  type HostKeyStatus,
  WebApiError,
  closeTerminal,
  commitDelete,
  createAgentGrant,
  createConnection,
  createDirectory,
  decodeBase64,
  disconnectConnection,
  downloadRemote,
  initializeBrowserSession,
  listDirectory,
  pollTerminal,
  prepareConnection,
  prepareDelete,
  releaseBrowserSession,
  renameRemote,
  resizeTerminal,
  startTerminal,
  trustConnection,
  uploadFiles,
  writeTerminal,
} from '../lib/web'
import '../web-macos.css'

type ConnectionPhase = 'booting' | 'form' | 'connecting' | 'trust' | 'desktop'
type AppWindow = 'finder' | 'terminal' | 'agent'

interface FinderEntry extends DirectoryEntry {
  absolutePath: string
}

interface ContextMenuState {
  entry: FinderEntry
  x: number
  y: number
}

interface EditDialogState {
  kind: 'mkdir' | 'rename' | 'delete'
  directory: string
  entry?: FinderEntry
  confirmationId?: string
}

interface Position {
  x: number
  y: number
}

function joinPath(parent: string, name: string): string {
  return parent === '/' ? `/${name}` : `${parent.replace(/\/+$/, '')}/${name}`
}

function parentPath(path: string): string {
  const parts = path.split('/').filter(Boolean)
  parts.pop()
  return parts.length ? `/${parts.join('/')}` : '/'
}

function formatSize(size: number | null): string {
  if (size === null) return '—'
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  if (size < 1024 * 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)} MB`
  return `${(size / 1024 / 1024 / 1024).toFixed(1)} GB`
}

function formatTime(seconds: number | null): string {
  if (!seconds) return '—'
  return new Intl.DateTimeFormat('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }).format(new Date(seconds * 1000))
}

function chineseError(error: unknown): string {
  if (error instanceof WebApiError) {
    const dictionary: Record<string, string> = {
      ssh_connection_failed: '无法连接服务器，请检查地址、端口和网络。',
      ssh_connection_refused: 'SSH 端口拒绝连接，请确认服务正在运行。',
      ssh_dns_failed: '无法解析 SSH 主机地址，请检查域名。',
      ssh_timeout: 'SSH 连接超时，请检查地址、端口和网络。',
      ssh_handshake_failed: 'SSH 握手失败，服务器未返回可用响应。',
      ssh_authentication_failed: '认证失败，请检查用户名或密码。',
      ssh_host_key_untrusted: '需要先确认服务器主机指纹。',
      ssh_host_key_mismatch: '服务器主机指纹与当前会话记录不一致。',
      ssh_directory_list_failed: '无法读取远端目录。',
      ssh_transfer_failed: '文件传输未完成。',
      profile_not_found: '临时连接已过期，请重新连接。',
      unauthorized: '本地会话已过期，请刷新页面后重试。',
    }
    return dictionary[error.code] ?? error.message
  }
  return '操作没有完成，请稍后重试。'
}

async function discardConnection(id: string): Promise<void> {
  try { await disconnectConnection(id) } catch { /* session may already be closed */ }
}

function useMovableWindow(initial: Position) {
  const [position, setPosition] = useState(initial)
  const grabbed = useRef<{ pointerId: number; x: number; y: number; start: Position } | null>(null)

  const onPointerDown = useCallback((event: ReactPointerEvent<HTMLElement>) => {
    if ((event.target as HTMLElement).closest('button, input')) return
    event.currentTarget.setPointerCapture(event.pointerId)
    grabbed.current = { pointerId: event.pointerId, x: event.clientX, y: event.clientY, start: position }
  }, [position])

  const onPointerMove = useCallback((event: ReactPointerEvent<HTMLElement>) => {
    const drag = grabbed.current
    if (!drag || drag.pointerId !== event.pointerId) return
    const x = Math.max(8, Math.min(window.innerWidth - 260, drag.start.x + event.clientX - drag.x))
    const y = Math.max(42, Math.min(window.innerHeight - 180, drag.start.y + event.clientY - drag.y))
    setPosition({ x, y })
  }, [])

  const onPointerUp = useCallback((event: ReactPointerEvent<HTMLElement>) => {
    if (grabbed.current?.pointerId === event.pointerId) grabbed.current = null
  }, [])

  return { position, dragHandlers: { onPointerDown, onPointerMove, onPointerUp } }
}

export function WebMacWorkspace() {
  const [phase, setPhase] = useState<ConnectionPhase>('booting')
  const [connection, setConnection] = useState<ConnectionSummary | null>(null)
  const [preparation, setPreparation] = useState<HostKeyStatus | null>(null)
  const [notice, setNotice] = useState('正在启动本地 SSH 工作台…')
  const [error, setError] = useState<string | null>(null)
  const [clock, setClock] = useState(() => new Date())
  const attemptRef = useRef(0)

  useEffect(() => {
    const timer = window.setInterval(() => setClock(new Date()), 30_000)
    void (async () => {
      try {
        await initializeBrowserSession()
        setPhase('form')
      } catch (reason) {
        setError(chineseError(reason))
        setPhase('form')
      }
    })()
    return () => window.clearInterval(timer)
  }, [])

  useEffect(() => {
    const release = (event: PageTransitionEvent) => {
      if (!event.persisted) releaseBrowserSession()
    }
    window.addEventListener('pagehide', release)
    return () => window.removeEventListener('pagehide', release)
  }, [])

  const startConnection = useCallback(async (input: { label: string; host: string; port: number; username: string; password: string }) => {
    const attempt = ++attemptRef.current
    let createdId: string | null = null
    setPhase('connecting')
    setError(null)
    setNotice('正在建立临时本地会话…')
    try {
      const creation = createConnection({
        label: input.label,
        host: input.host,
        port: input.port,
        username: input.username,
        authentication: { kind: 'password', password: input.password },
        tags: [],
        terminal: { columns: 110, rows: 34, command: null },
      })
      input.password = ''
      const next = await creation
      createdId = next.id
      if (attempt !== attemptRef.current) {
        await discardConnection(next.id)
        return
      }
      setConnection(next)
      setNotice('正在连接服务器并读取主机指纹…')
      const status = await prepareConnection(next.id)
      if (attempt !== attemptRef.current) {
        await discardConnection(next.id)
        return
      }
      setPreparation(status)
      if (status.status === 'trusted') {
        setNotice('服务器身份已确认，正在打开桌面…')
        setPhase('desktop')
      } else if (status.status === 'confirmation_required') {
        setNotice('请核对服务器主机指纹。')
        setPhase('trust')
      } else {
        await discardConnection(next.id)
        if (attempt !== attemptRef.current) return
        setConnection(null)
        setPreparation(null)
        setError('服务器指纹发生变化，已阻止连接。请确认服务器身份后重新建立临时连接。')
        setPhase('form')
      }
    } catch (reason) {
      if (createdId) await discardConnection(createdId)
      if (attempt !== attemptRef.current) return
      setConnection(null)
      setPreparation(null)
      setError(chineseError(reason))
      setPhase('form')
    } finally {
      input.password = ''
    }
  }, [])

  const trustServer = useCallback(async () => {
    if (!connection || !preparation || preparation.status !== 'confirmation_required') return
    const attempt = attemptRef.current
    setPhase('connecting')
    setNotice('正在确认服务器身份…')
    try {
      await trustConnection(connection.id, preparation.confirmationId)
      if (attempt !== attemptRef.current) {
        await discardConnection(connection.id)
        return
      }
      setNotice('服务器身份已确认，正在打开桌面…')
      setPhase('desktop')
    } catch (reason) {
      await discardConnection(connection.id)
      if (attempt !== attemptRef.current) return
      setConnection(null)
      setPreparation(null)
      setError(chineseError(reason))
      setPhase('form')
    }
  }, [connection, preparation])

  const cancelConnection = useCallback(async () => {
    attemptRef.current += 1
    if (connection) await discardConnection(connection.id)
    setConnection(null)
    setPreparation(null)
    setError(null)
    setPhase('form')
    setNotice('连接已取消，临时凭据已清空。')
  }, [connection])

  const disconnect = useCallback(async () => {
    attemptRef.current += 1
    if (connection) await discardConnection(connection.id)
    setConnection(null)
    setPreparation(null)
    setPhase('form')
    setNotice('已断开，临时凭据和主机信任已清空。')
  }, [connection])

  const menuDate = new Intl.DateTimeFormat('zh-CN', { weekday: 'short', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }).format(clock)

  return (
    <main className="web-mac-root" onContextMenu={(event) => event.preventDefault()}>
      <div className="web-mac-wallpaper" />
      <header className="web-mac-menubar">
        <div className="web-mac-menu-left"><span className="web-mac-apple">●</span><strong>NekoSSH</strong><span>工作台</span><span>文件</span><span>窗口</span></div>
        <div className="web-mac-menu-right"><Wifi size={15} /><ShieldCheck size={15} /><span>{menuDate}</span></div>
      </header>

      {phase === 'desktop' && connection ? <Desktop connection={connection} onDisconnect={() => void disconnect()} /> : null}
      {phase !== 'desktop' ? (
        <ConnectionSheet
          error={error}
          notice={notice}
          phase={phase}
          preparation={preparation}
          onConnect={startConnection}
          onTrust={() => void trustServer()}
          onBack={() => void cancelConnection()}
        />
      ) : null}
    </main>
  )
}

function ConnectionSheet({
  phase,
  notice,
  error,
  preparation,
  onConnect,
  onTrust,
  onBack,
}: {
  phase: ConnectionPhase
  notice: string
  error: string | null
  preparation: HostKeyStatus | null
  onConnect: (input: { label: string; host: string; port: number; username: string; password: string }) => Promise<void>
  onTrust: () => void
  onBack: () => void
}) {
  const [label, setLabel] = useState('我的 SSH 主机')
  const [host, setHost] = useState('')
  const [port, setPort] = useState('22')
  const [username, setUsername] = useState('root')
  const [password, setPassword] = useState('')
  const busy = phase === 'booting' || phase === 'connecting'
  const isTrust = phase === 'trust' && preparation?.status === 'confirmation_required'
  const parsedPort = Number(port)
  const validPort = Number.isInteger(parsedPort) && parsedPort >= 1 && parsedPort <= 65535

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!host.trim() || !username.trim() || !password || !validPort) return
    let submittedPassword = password
    setPassword('')
    const input = { label: label.trim() || host.trim(), host: host.trim(), port: parsedPort, username: username.trim(), password: submittedPassword }
    submittedPassword = ''
    await onConnect(input)
    input.password = ''
  }

  return (
    <section aria-modal="true" className="web-connect-overlay" role="dialog">
      <div className="web-connect-card">
        <div className="web-connect-brand"><span className="web-connect-orb"><Terminal size={23} /></span><div><b>NekoSSH</b><span>独立浏览器工作台</span></div></div>
        {phase === 'form' || phase === 'booting' ? (
          <form autoComplete="off" className="web-connect-form" onSubmit={submit}>
            <div className="web-connect-heading"><div><p>快速连接</p><h1>把 SSH 主机拖进桌面</h1></div><span className="web-local-badge"><ShieldCheck size={14} />仅本机内存</span></div>
            {error ? <p className="web-form-error"><CircleAlert size={15} />{error}</p> : null}
            <label><span>显示名称</span><input value={label} onChange={(event) => setLabel(event.target.value)} placeholder="例如：火山测试机" /></label>
            <div className="web-connect-grid"><label><span>主机地址</span><input autoFocus required value={host} onChange={(event) => setHost(event.target.value)} placeholder="IP 或域名" /></label><label><span>端口</span><input aria-invalid={!validPort} inputMode="numeric" max={65535} min={1} required type="number" value={port} onChange={(event) => setPort(event.target.value)} /></label></div>
            <div className="web-connect-grid"><label><span>用户名</span><input autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} /></label><label><span>SSH 密码</span><input autoComplete="off" type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="只驻留本机内存" /></label></div>
            <button className="web-primary-button" disabled={busy || !host.trim() || !username.trim() || !password || !validPort} type="submit"><Terminal size={17} />连接并打开桌面</button>
            <p className="web-connect-foot"><ShieldCheck size={14} />不创建 Vault、不写入密码；断开或后端退出即清空。</p>
          </form>
        ) : (
          <div className="web-connect-progress">
            <div className="web-progress-title"><span className="web-progress-spinner"><LoaderCircle size={22} /></span><div><p>连接过程</p><h1>{isTrust ? '确认服务器身份' : '正在建立安全连接'}</h1></div></div>
            <ol className="web-progress-steps"><li className="is-done"><i />初始化本地会话</li><li className={phase === 'connecting' && !preparation ? 'is-current' : preparation ? 'is-done' : ''}><i />建立 SSH 连接</li><li className={isTrust || preparation ? 'is-current' : ''}><i />验证主机密钥</li><li><i />打开 Terminal 与 Finder</li></ol>
            <div className="web-connect-log">{notice}{error ? `\n${error}` : ''}</div>
            {isTrust ? <div className="web-fingerprint"><span>{preparation.keyAlgorithm}</span><code>{preparation.fingerprint}</code><small>此信任仅在当前本地后端进程中有效。</small></div> : null}
            <div className="web-progress-actions">{isTrust ? <button className="web-primary-button" onClick={onTrust} type="button"><ShieldCheck size={17} />信任并继续</button> : null}<button className="web-secondary-button" onClick={onBack} type="button">返回连接信息</button></div>
          </div>
        )}
      </div>
    </section>
  )
}

function Desktop({ connection, onDisconnect }: { connection: ConnectionSummary; onDisconnect: () => void }) {
  const [open, setOpen] = useState<Record<AppWindow, boolean>>({ finder: true, terminal: true, agent: false })
  const [active, setActive] = useState<AppWindow>('terminal')
  const finder = useMovableWindow({ x: 32, y: 92 })
  const terminal = useMovableWindow({ x: 420, y: 108 })
  const agent = useMovableWindow({ x: 680, y: 154 })

  const bringForward = (windowId: AppWindow) => setActive(windowId)
  const toggle = (windowId: AppWindow) => {
    setOpen((current) => ({ ...current, [windowId]: true }))
    bringForward(windowId)
  }
  const close = (windowId: AppWindow) => setOpen((current) => ({ ...current, [windowId]: false }))

  return <section className="web-mac-desktop" onClick={() => {}}>
    <div className="web-desktop-chip"><span className="web-desktop-chip-dot" /><div><b>{connection.label}</b><span>{connection.username}@{connection.host}</span></div><button onClick={onDisconnect} type="button">断开</button></div>
    <div className="web-desktop-icons"><button onClick={() => toggle('finder')} type="button"><span className="web-app-icon finder"><FolderOpen size={30} /></span><span>Finder</span></button><button onClick={() => toggle('terminal')} type="button"><span className="web-app-icon terminal"><Terminal size={28} /></span><span>Terminal</span></button><button onClick={() => toggle('agent')} type="button"><span className="web-app-icon agent"><Bot size={28} /></span><span>Agent</span></button></div>
    {open.finder ? <DesktopWindow active={active === 'finder'} icon={<FolderOpen size={15} />} title="Finder · 远端文件" position={finder.position} width={440} onFocus={() => bringForward('finder')} onClose={() => close('finder')} dragHandlers={finder.dragHandlers}><Finder connection={connection} /></DesktopWindow> : null}
    {open.terminal ? <DesktopWindow active={active === 'terminal'} icon={<Terminal size={15} />} title={`${connection.username}@${connection.host} — zsh`} position={terminal.position} width={Math.min(760, window.innerWidth - 450)} onFocus={() => bringForward('terminal')} onClose={() => close('terminal')} dragHandlers={terminal.dragHandlers}><TerminalPane connection={connection} /></DesktopWindow> : null}
    {open.agent ? <DesktopWindow active={active === 'agent'} icon={<Bot size={15} />} title="Agent MCP 授权" position={agent.position} width={420} onFocus={() => bringForward('agent')} onClose={() => close('agent')} dragHandlers={agent.dragHandlers}><AgentPane connection={connection} /></DesktopWindow> : null}
    <nav className="web-mac-dock" aria-label="Dock"><button aria-label="打开 Finder" className={open.finder ? 'is-open' : ''} onClick={() => toggle('finder')} type="button"><span className="web-dock-icon finder"><FolderOpen size={28} /></span></button><button aria-label="打开 Terminal" className={open.terminal ? 'is-open' : ''} onClick={() => toggle('terminal')} type="button"><span className="web-dock-icon terminal"><Terminal size={27} /></span></button><button aria-label="打开 Agent" className={open.agent ? 'is-open' : ''} onClick={() => toggle('agent')} type="button"><span className="web-dock-icon agent"><Bot size={26} /></span></button></nav>
  </section>
}

function DesktopWindow({
  title,
  icon,
  position,
  width,
  active,
  onClose,
  onFocus,
  dragHandlers,
  children,
}: {
  title: string
  icon: ReactNode
  position: Position
  width: number
  active: boolean
  onClose: () => void
  onFocus: () => void
  dragHandlers: { onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void; onPointerMove: (event: ReactPointerEvent<HTMLElement>) => void; onPointerUp: (event: ReactPointerEvent<HTMLElement>) => void }
  children: ReactNode
}) {
  return <section className={`web-mac-window ${active ? 'is-active' : ''}`} style={{ width, transform: `translate3d(${position.x}px, ${position.y}px, 0)` }} onMouseDown={onFocus}>
    <header className="web-window-titlebar" {...dragHandlers}>
      <span className="web-window-controls"><button aria-label="关闭窗口" className="close" onClick={onClose} type="button"><X size={10} /></button><i /><i /></span><span className="web-window-title">{icon}{title}</span><span className="web-window-spacer" /></header>
    <div className="web-window-body">{children}</div>
  </section>
}

function Finder({ connection }: { connection: ConnectionSummary }) {
  const [history, setHistory] = useState({ items: ['/'], index: 0 })
  const cwd = history.items[history.index] ?? '/'
  const [entries, setEntries] = useState<FinderEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [message, setMessage] = useState<string | null>(null)
  const [context, setContext] = useState<ContextMenuState | null>(null)
  const [dialog, setDialog] = useState<EditDialogState | null>(null)
  const [dialogValue, setDialogValue] = useState('')
  const [dragging, setDragging] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const cwdRef = useRef(cwd)
  const requestGeneration = useRef(0)
  const dialogReturnFocus = useRef<HTMLElement | null>(null)

  useEffect(() => { cwdRef.current = cwd }, [cwd])

  const navigate = useCallback((nextPath: string) => {
    setHistory((current) => {
      if (current.items[current.index] === nextPath) return current
      const items = [...current.items.slice(0, current.index + 1), nextPath]
      return { items, index: items.length - 1 }
    })
    setContext(null)
  }, [])

  const moveHistory = useCallback((delta: -1 | 1) => {
    setHistory((current) => ({
      ...current,
      index: Math.max(0, Math.min(current.items.length - 1, current.index + delta)),
    }))
    setContext(null)
  }, [])

  const refresh = useCallback(async () => {
    const requestedPath = cwd
    const generation = ++requestGeneration.current
    setLoading(true)
    try {
      const listing = await listDirectory(connection.id, requestedPath)
      if (generation !== requestGeneration.current || cwdRef.current !== requestedPath) return
      setEntries(listing.entries.map((entry) => ({
        ...entry,
        absolutePath: joinPath(requestedPath, entry.name),
      })))
      setMessage(listing.truncated ? '目录项目过多，仅显示前 10,000 项。' : null)
    } catch (reason) {
      if (generation !== requestGeneration.current || cwdRef.current !== requestedPath) return
      setMessage(chineseError(reason))
    } finally {
      if (generation === requestGeneration.current && cwdRef.current === requestedPath) setLoading(false)
    }
  }, [connection.id, cwd])

  useEffect(() => { void refresh() }, [refresh])

  const upload = useCallback(async (files: File[]) => {
    if (!files.length) return
    const targetDirectory = cwd
    setMessage(`正在上传 ${files.length} 个文件…`)
    try {
      const receipt = await uploadFiles(connection.id, targetDirectory, files)
      setMessage(`已上传 ${receipt.uploaded} 个文件。`)
      if (cwdRef.current === targetDirectory) await refresh()
    } catch (reason) { setMessage(chineseError(reason)) }
  }, [connection.id, cwd, refresh])

  function openDialog(kind: EditDialogState['kind'], entry?: FinderEntry, confirmationId?: string) {
    dialogReturnFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    setDialog({ kind, directory: entry ? parentPath(entry.absolutePath) : cwd, entry, confirmationId })
    setDialogValue(kind === 'rename' ? entry?.name ?? '' : '')
  }

  function closeDialog() {
    setDialog(null)
    window.setTimeout(() => dialogReturnFocus.current?.focus(), 0)
  }

  async function requestDelete(entry: FinderEntry) {
    try {
      const confirmation = await prepareDelete(connection.id, entry.absolutePath)
      openDialog('delete', entry, confirmation.confirmationId)
    } catch (reason) { setMessage(chineseError(reason)) }
  }

  async function submitDialog(event: FormEvent) {
    event.preventDefault()
    if (!dialog) return
    try {
      if (dialog.kind === 'mkdir') await createDirectory(connection.id, joinPath(dialog.directory, dialogValue.trim()))
      if (dialog.kind === 'rename' && dialog.entry) {
        await renameRemote(
          connection.id,
          dialog.entry.absolutePath,
          joinPath(parentPath(dialog.entry.absolutePath), dialogValue.trim()),
        )
      }
      if (dialog.kind === 'delete' && dialog.confirmationId) await commitDelete(connection.id, dialog.confirmationId)
      closeDialog()
      if (cwdRef.current === dialog.directory) await refresh()
    } catch (reason) { setMessage(chineseError(reason)) }
  }

  const crumbs = cwd.split('/').filter(Boolean)
  return <div className="web-finder" onClick={() => setContext(null)}>
    <input ref={inputRef} className="web-hidden-file-input" type="file" multiple onChange={(event: ChangeEvent<HTMLInputElement>) => { void upload(Array.from(event.target.files ?? [])); event.target.value = '' }} />
    <div className="web-finder-toolbar"><div className="web-finder-nav"><button aria-label="后退" disabled={history.index === 0} onClick={() => moveHistory(-1)} type="button"><ChevronLeft size={16} /></button><button aria-label="前进" disabled={history.index >= history.items.length - 1} onClick={() => moveHistory(1)} type="button"><ChevronRight size={16} /></button><span className="web-finder-path"><button onClick={() => navigate('/')} type="button">/</button>{crumbs.map((part, index) => <span key={`${part}-${index}`}> / <button onClick={() => navigate(`/${crumbs.slice(0, index + 1).join('/')}`)} type="button">{part}</button></span>)}</span></div><div className="web-finder-actions"><button aria-label="新建文件夹" onClick={() => openDialog('mkdir')} title="新建文件夹" type="button"><FolderPlus size={15} /></button><button aria-label="上传文件" onClick={() => inputRef.current?.click()} title="上传文件" type="button"><Upload size={15} /></button><button aria-label="刷新目录" onClick={() => void refresh()} title="刷新" type="button"><RefreshCw size={15} /></button></div></div>
    <div className={`web-finder-content ${dragging ? 'is-dragging' : ''}`} onDragEnter={(event) => { event.preventDefault(); setDragging(true) }} onDragOver={(event) => event.preventDefault()} onDragLeave={() => setDragging(false)} onDrop={(event) => { event.preventDefault(); setDragging(false); void upload(Array.from(event.dataTransfer.files)) }}>
      <aside className="web-finder-sidebar"><span>位置</span><button onClick={() => navigate('/')} type="button"><HardDrive size={15} />远端根目录</button><div className="web-finder-host"><MonitorCog size={15} /><span title={connection.host}>{connection.label}</span></div><div className="web-finder-side-note"><ShieldCheck size={14} />SFTP 已验证</div></aside>
      <div className="web-file-list"><div className="web-file-heading"><span>名称</span><span>修改日期</span><span>大小</span></div>{loading ? <div className="web-file-empty"><LoaderCircle className="spin" size={20} />正在读取远端目录…</div> : entries.length ? entries.map((entry) => {
        const openEntry = () => entry.isDirectory ? navigate(entry.absolutePath) : void downloadRemote(connection.id, entry.absolutePath).catch((reason) => setMessage(chineseError(reason)))
        return <button className="web-file-row" key={entry.absolutePath} onClick={(event) => { if (event.detail === 0) openEntry() }} onDoubleClick={openEntry} onContextMenu={(event) => { event.preventDefault(); event.stopPropagation(); setContext({ entry, x: event.clientX, y: event.clientY }) }} type="button"><span>{entry.isDirectory ? <Folder color="#62b6ff" fill="#62b6ff" size={17} /> : <FileText color="#d9e3f0" size={16} />}<b>{entry.name}</b></span><span>{formatTime(entry.modifiedAtUnix)}</span><span>{formatSize(entry.size)}</span></button>
      }) : <div className="web-file-empty"><FolderOpen size={27} />这个文件夹是空的</div>}{dragging ? <div className="web-drop-overlay"><Upload size={25} /><b>松开以上传到 {cwd}</b><span>文件不会写入本机磁盘路径。</span></div> : null}</div>
    </div>
    <footer aria-live="polite" className="web-finder-status"><span>{entries.length} 个项目</span><span>{message ?? '可拖入文件上传；右键打开更多操作。'}</span></footer>
    {context ? <div aria-label={`${context.entry.name} 操作`} className="web-context-menu" role="menu" style={{ left: context.x, top: context.y }} onClick={(event) => event.stopPropagation()} onKeyDown={(event) => { if (event.key === 'Escape') setContext(null) }}><button autoFocus onClick={() => { if (context.entry.isDirectory) navigate(context.entry.absolutePath); else void downloadRemote(connection.id, context.entry.absolutePath).catch((reason) => setMessage(chineseError(reason))); setContext(null) }} role="menuitem" type="button">{context.entry.isDirectory ? '打开文件夹' : '下载文件'}</button><button onClick={() => { openDialog('rename', context.entry); setContext(null) }} role="menuitem" type="button">重命名</button><button className="danger" onClick={() => { void requestDelete(context.entry); setContext(null) }} role="menuitem" type="button"><Trash2 size={14} />删除</button></div> : null}
    {dialog ? <FileDialog dialog={dialog} value={dialogValue} onValue={setDialogValue} onCancel={closeDialog} onSubmit={submitDialog} /> : null}
  </div>
}

function FileDialog({ dialog, value, onValue, onCancel, onSubmit }: { dialog: EditDialogState; value: string; onValue: (value: string) => void; onCancel: () => void; onSubmit: (event: FormEvent) => void }) {
  const title = dialog.kind === 'mkdir' ? '新建远端文件夹' : dialog.kind === 'rename' ? '重命名远端项目' : '删除远端项目'
  const form = useRef<HTMLFormElement>(null)
  useEffect(() => {
    const focusable = form.current?.querySelector<HTMLElement>('input, button')
    focusable?.focus()
  }, [])
  const keyDown = (event: KeyboardEvent<HTMLFormElement>) => {
    if (event.key === 'Escape') {
      event.preventDefault()
      onCancel()
      return
    }
    if (event.key !== 'Tab' || !form.current) return
    const focusable = [...form.current.querySelectorAll<HTMLElement>('input, button:not([disabled])')]
    if (!focusable.length) return
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault()
      first.focus()
    }
  }
  return <div className="web-file-dialog-backdrop"><form aria-labelledby="web-file-dialog-title" aria-modal="true" className="web-file-dialog" onKeyDown={keyDown} onSubmit={onSubmit} ref={form} role="dialog"><h3 id="web-file-dialog-title">{title}</h3>{dialog.kind === 'delete' ? <p>确定删除“{dialog.entry?.absolutePath}”吗？只允许删除文件、链接或空目录，不会递归删除。</p> : <label><span>{dialog.kind === 'mkdir' ? '文件夹名称' : '新名称'}</span><input value={value} onChange={(event) => onValue(event.target.value)} required /></label>}<div><button className="web-secondary-button" onClick={onCancel} type="button">取消</button><button className={dialog.kind === 'delete' ? 'web-danger-button' : 'web-primary-button'} type="submit">{dialog.kind === 'delete' ? '确认删除' : '确认'}</button></div></form></div>
}

function TerminalPane({ connection }: { connection: ConnectionSummary }) {
  const host = useRef<HTMLDivElement>(null)
  const [state, setState] = useState<'connecting' | 'connected' | 'closed' | 'error'>('connecting')

  useEffect(() => {
    if (!host.current) return
    let disposed = false
    let pollTimer: number | null = null
    let resizeTimer: number | null = null
    let startedId: string | null = null
    let cursor = 0
    let inputTail = Promise.resolve()
    let resizeTail = Promise.resolve()
    let queuedInputBytes = 0
    const terminal = new XTerm({
      cursorBlink: true,
      convertEol: false,
      fontFamily: '"SFMono-Regular", "JetBrains Mono", "Cascadia Code", Consolas, monospace',
      fontSize: 12,
      lineHeight: 1.35,
      scrollback: 10_000,
      theme: {
        background: '#0c1018',
        foreground: '#e3edf8',
        cursor: '#68f59e',
        selectionBackground: '#4d79a866',
        black: '#111820',
        brightBlack: '#66788c',
        green: '#68f59e',
        brightGreen: '#9af8bd',
        blue: '#62b6ff',
        brightBlue: '#9bd1ff',
        red: '#ff7c84',
        brightRed: '#ffadb2',
      },
    })
    const fit = new FitAddon()
    terminal.loadAddon(fit)
    terminal.open(host.current)
    fit.fit()
    terminal.write('\x1b[90m正在启动远端 PTY…\x1b[0m\r\n')

    const showError = (reason: unknown) => {
      if (disposed) return
      setState('error')
      terminal.write(`\r\n\x1b[31m[${terminalMessage(chineseError(reason))}]\x1b[0m\r\n`)
    }

    const queueInput = (data: string) => {
      if (!startedId || !data) return
      const bytes = new TextEncoder().encode(data).byteLength
      if (queuedInputBytes + bytes > 1024 * 1024) {
        terminal.write('\r\n\x1b[33m[本地输入队列已满，请等待远端响应后再粘贴。]\x1b[0m\r\n')
        return
      }
      queuedInputBytes += bytes
      const chunks = splitTerminalInput(data)
      inputTail = inputTail
        .then(async () => {
          for (const chunk of chunks) {
            if (!startedId || disposed) return
            await writeTerminal(startedId, chunk)
          }
        })
        .catch(showError)
        .finally(() => {
          queuedInputBytes = Math.max(0, queuedInputBytes - bytes)
        })
    }

    const dataDisposable = terminal.onData(queueInput)
    const resizeObserver = new ResizeObserver(() => {
      fit.fit()
      if (!startedId || disposed) return
      if (resizeTimer !== null) window.clearTimeout(resizeTimer)
      resizeTimer = window.setTimeout(() => {
        resizeTimer = null
        if (!startedId || disposed) return
        const terminalId = startedId
        resizeTail = resizeTail
          .then(() => resizeTerminal(terminalId, terminal.cols, terminal.rows))
          .catch(showError)
      }, 80)
    })
    resizeObserver.observe(host.current)

    const pollOnce = async () => {
      if (!startedId || disposed) return
      const terminalId = startedId
      try {
        const poll = await pollTerminal(terminalId, cursor)
        if (disposed || terminalId !== startedId) return
        if (poll.truncated) {
          terminal.write('\r\n\x1b[33m[输出过快，较早的终端内容已从本地缓冲区移除。]\x1b[0m\r\n')
        }
        for (const chunk of poll.chunks) {
          await new Promise<void>((resolve) => terminal.write(decodeBase64(chunk.dataBase64), resolve))
          if (disposed || terminalId !== startedId) return
        }
        cursor = poll.cursor
        if (poll.error) terminal.write(`\r\n\x1b[31m[${terminalMessage(poll.error)}]\x1b[0m\r\n`)
        if (poll.closed) {
          setState(poll.error ? 'error' : 'closed')
          terminal.write(`\r\n\x1b[90m[会话已关闭${poll.exitStatus === null ? '' : `，退出码 ${poll.exitStatus}`}]\x1b[0m\r\n`)
          return
        }
        pollTimer = window.setTimeout(() => { void pollOnce() }, poll.chunks.length ? 35 : 120)
      } catch (reason) {
        showError(reason)
      }
    }

    void startTerminal(connection.id, terminal.cols, terminal.rows).then((started) => {
      if (disposed) {
        void closeTerminal(started.terminalId).catch(() => undefined)
        return
      }
      startedId = started.terminalId
      setState('connected')
      terminal.focus()
      void pollOnce()
    }).catch(showError)

    return () => {
      disposed = true
      if (pollTimer !== null) window.clearTimeout(pollTimer)
      if (resizeTimer !== null) window.clearTimeout(resizeTimer)
      resizeObserver.disconnect()
      dataDisposable.dispose()
      if (startedId) void closeTerminal(startedId).catch(() => undefined)
      terminal.dispose()
    }
  }, [connection.id])

  const stateLabel = state === 'connected'
    ? '已连接'
    : state === 'connecting'
      ? '连接中'
      : state === 'closed'
        ? '已关闭'
        : '发生错误'

  return <div className="web-terminal-pane">
    <div className="web-terminal-toolbar">
      <span aria-live="polite"><span className={`web-live-dot ${state}`} />{stateLabel}</span>
      <span>{connection.username}@{connection.host}</span>
      <span>完整 PTY · xterm-256color</span>
    </div>
    <div aria-label={`${connection.label} 远端终端`} className="web-terminal-xterm" ref={host} role="application" />
  </div>
}

function splitTerminalInput(value: string): string[] {
  const chunks: string[] = []
  let current = ''
  let currentBytes = 0
  const encoder = new TextEncoder()
  for (const symbol of value) {
    const symbolBytes = encoder.encode(symbol).byteLength
    if (current && currentBytes + symbolBytes > 12 * 1024) {
      chunks.push(current)
      current = ''
      currentBytes = 0
    }
    current += symbol
    currentBytes += symbolBytes
  }
  if (current) chunks.push(current)
  return chunks
}

function terminalMessage(value: string): string {
  return value.replace(/[\u0000-\u001f\u007f-\u009f]/g, ' ').slice(0, 500)
}

function AgentPane({ connection }: { connection: ConnectionSummary }) {
  const [grant, setGrant] = useState<AgentGrant | null>(null)
  const [message, setMessage] = useState<string | null>(null)
  const payload = useMemo(() => grant ? JSON.stringify({ name: 'ssh_connection_attach', arguments: { grant: grant.grant } }, null, 2) : '', [grant])
  const mcpEndpoint = import.meta.env.DEV
    ? 'http://127.0.0.1:7331/mcp'
    : new URL('/mcp', window.location.origin).toString()

  async function enable() {
    try {
      const next = await createAgentGrant(connection.id)
      setGrant(next)
      setMessage('已生成一次性 Agent 授权。它不包含 SSH 密码。')
    } catch (reason) { setMessage(chineseError(reason)) }
  }

  async function copyGrant() {
    try {
      await navigator.clipboard.writeText(payload)
      setMessage('已复制到剪贴板。')
    } catch {
      setMessage('浏览器未允许访问剪贴板，请手动选择并复制。')
    }
  }

  return <div className="web-agent-pane"><div className="web-agent-hero"><span><Bot size={24} /></span><div><p>本机 MCP</p><h3>把当前会话交给 Agent</h3></div></div><p>Agent 只能使用你在浏览器中已建立的临时连接，不会收到 SSH 密码或私钥。</p>{grant ? <><div className="web-agent-code"><code>{payload}</code><button onClick={() => void copyGrant()} type="button"><Copy size={14} />复制授权调用</button></div><small>有效期约 {Math.max(1, Math.floor(grant.expiresInSeconds / 60))} 分钟；只能使用一次。</small></> : <button className="web-primary-button" onClick={() => void enable()} type="button"><Bot size={16} />生成 Agent 授权</button>}<div className="web-agent-endpoint"><span>MCP 地址</span><code>{mcpEndpoint}</code><small>认证 Token 在启动本地 Web 服务的终端中显示。</small></div>{message ? <p aria-live="polite" className="web-agent-message">{message}</p> : null}</div>
}
