import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
} from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal as XTerm } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import './terminal233-ide.css'
import {
  Activity,
  ChevronLeft,
  CirclePlus,
  Download,
  File,
  Folder,
  FolderPlus,
  HardDrive,
  MoreHorizontal,
  Pencil,
  Plug,
  RefreshCw,
  Search,
  Server,
  TerminalSquare,
  Trash2,
  Upload,
  X,
} from 'lucide-react'
import {
  acknowledgeIdeDownloadChunk,
  acknowledgeIdeTerminalOutput,
  cancelIdeTransfer,
  createIdeConnection,
  createIdeDirectory,
  decodeBase64Bytes,
  deleteIdeRemote,
  disconnectIdeConnection,
  downloadIdeFiles,
  IdeTerminalInputQueue,
  ideError,
  listIdeConnections,
  listIdeDirectory,
  onIdeTransferProgress,
  prepareIdeConnection,
  rejectIdeHostKey,
  renameIdeRemote,
  resizeIdeTerminal,
  startIdeTerminal,
  trustIdeHostKey,
  uploadIdeFiles,
  closeIdeTerminal,
  type IdeConnection,
  type IdeDirectoryEntry,
  type IdeHostKeyStatus,
  type IdeTerminalEvent,
  type IdeTransferComplete,
  type IdeTransferProgress,
  type IdeDownloadEvent,
  type IdeUploadEvent,
} from './lib/ide'
import CredentialSyncPanel from './components/CredentialSyncPanel'
import type { CredentialAccount } from './lib/credential-server'

type ActiveConnection = {
  connection: IdeConnection
  fingerprint: string
  keyAlgorithm: string
  connectedAt: number
}

type ProgressState = {
  attemptId: number
  connection: IdeConnection
  phase: 'preparing' | 'awaiting_trust' | 'opening' | 'error'
  hostKey: IdeHostKeyStatus | null
  error: string | null
}

type ConnectionMenu = { x: number; y: number; connection: IdeConnection } | null
type AuthKind = 'password' | 'private_key'
type ConnectionForm = {
  label: string
  group: string
  host: string
  port: string
  username: string
  authKind: AuthKind
  password: string
  privateKeyPem: string
  passphrase: string
}
type Notice = { tone: 'error' | 'success' | 'info'; message: string }
type FileTarget = { entry: IdeDirectoryEntry; path: string; directory: string }
type FileDialog =
  | { kind: 'mkdir'; directory: string }
  | ({ kind: 'rename' | 'delete' } & FileTarget)

const MAX_DOWNLOAD_FILE_BYTES = 16 * 1024 * 1024
const MAX_DOWNLOAD_JOB_BYTES = 64 * 1024 * 1024

type DownloadFileCollector = {
  name: string
  expectedBytes: number | null
  nextOffset: number
  bytes: number
  parts: Uint8Array[]
}

type DownloadCollector = {
  files: Map<string, DownloadFileCollector>
  bytes: number
  invalid: boolean
  nextSequence: number
  expectedBytes: number | null
}

const EMPTY_FORM: ConnectionForm = {
  label: '',
  group: '个人服务器',
  host: '',
  port: '22',
  username: 'root',
  authKind: 'password',
  password: '',
  privateKeyPem: '',
  passphrase: '',
}

function connectionGroup(connection: IdeConnection): string {
  return connection.tags?.[0]?.trim() || '未分组'
}

function menuPosition(x: number, y: number, width = 170, height = 130) {
  return {
    x: Math.max(8, Math.min(x, window.innerWidth - width - 8)),
    y: Math.max(8, Math.min(y, window.innerHeight - height - 8)),
  }
}

function handleMenuKeyDown(event: ReactKeyboardEvent<HTMLElement>) {
  if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp' && event.key !== 'Home' && event.key !== 'End') return
  const items = [...event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not([disabled])')]
  if (!items.length) return
  event.preventDefault()
  const current = items.indexOf(document.activeElement as HTMLButtonElement)
  if (event.key === 'Home') items[0].focus()
  else if (event.key === 'End') items[items.length - 1].focus()
  else if (event.key === 'ArrowDown') items[(current + 1 + items.length) % items.length].focus()
  else items[(current - 1 + items.length) % items.length].focus()
}

function useModalFocus(
  open: boolean,
  onClose: () => void,
  dialogRef: { current: HTMLElement | null },
) {
  const closeRef = useRef(onClose)
  closeRef.current = onClose

  useEffect(() => {
    if (!open || !dialogRef.current) return
    const dialog = dialogRef.current
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const focusableSelector = [
      'button:not([disabled])',
      'input:not([disabled])',
      'select:not([disabled])',
      'textarea:not([disabled])',
      '[tabindex]:not([tabindex="-1"])',
    ].join(',')
    const focusInitial = () => {
      const target = dialog.querySelector<HTMLElement>('[autofocus]')
        ?? dialog.querySelector<HTMLElement>(focusableSelector)
        ?? dialog
      target.focus()
    }
    const animationFrame = window.requestAnimationFrame(focusInitial)
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        event.stopPropagation()
        closeRef.current()
        return
      }
      if (event.key !== 'Tab') return
      const focusable = [...dialog.querySelectorAll<HTMLElement>(focusableSelector)]
      if (!focusable.length) {
        event.preventDefault()
        dialog.focus()
        return
      }
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      const current = document.activeElement
      if (event.shiftKey && (current === first || !dialog.contains(current))) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && (current === last || !dialog.contains(current))) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', handleKeyDown, true)
    return () => {
      window.cancelAnimationFrame(animationFrame)
      document.removeEventListener('keydown', handleKeyDown, true)
      if (previousFocus?.isConnected) previousFocus.focus()
    }
  }, [dialogRef, open])
}

export default function Terminal233IdeApp() {
  const [connections, setConnections] = useState<IdeConnection[]>([])
  const [loadingConnections, setLoadingConnections] = useState(true)
  const [libraryError, setLibraryError] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const [selectedGroup, setSelectedGroup] = useState('全部')
  const [showCreate, setShowCreate] = useState(false)
  const [form, setForm] = useState(EMPTY_FORM)
  const [creating, setCreating] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [progress, setProgress] = useState<ProgressState | null>(null)
  const [active, setActive] = useState<ActiveConnection | null>(null)
  const [menu, setMenu] = useState<ConnectionMenu>(null)
  const [draggedId, setDraggedId] = useState<string | null>(null)
  const libraryLoadId = useRef(0)
  const connectionAttempt = useRef(0)
  const createAttempt = useRef(0)
  const createDialogRef = useRef<HTMLFormElement>(null)
  const menuOrigin = useRef<HTMLElement | null>(null)

  useModalFocus(showCreate, closeCreate, createDialogRef)

  useEffect(() => {
    void loadConnections()
    return () => {
      libraryLoadId.current += 1
      connectionAttempt.current += 1
      createAttempt.current += 1
    }
  }, [])

  useEffect(() => {
    const close = () => setMenu(null)
    const closeWithKeyboard = (event: globalThis.KeyboardEvent) => {
      if (event.key !== 'Escape' || !menu) return
      event.preventDefault()
      setMenu(null)
      menuOrigin.current?.focus()
    }
    window.addEventListener('click', close)
    window.addEventListener('keydown', closeWithKeyboard)
    return () => {
      window.removeEventListener('click', close)
      window.removeEventListener('keydown', closeWithKeyboard)
    }
  }, [menu])

  async function loadConnections() {
    const loadId = ++libraryLoadId.current
    setLoadingConnections(true)
    setLibraryError(null)
    try {
      const listed = await listIdeConnections()
      if (loadId === libraryLoadId.current) setConnections(listed)
    } catch (error) {
      if (loadId === libraryLoadId.current) setLibraryError(ideError(error))
    } finally {
      if (loadId === libraryLoadId.current) setLoadingConnections(false)
    }
  }

  function closeCreate() {
    createAttempt.current += 1
    setShowCreate(false)
    setCreating(false)
    setFormError(null)
    setForm(EMPTY_FORM)
  }

  function openConnectionMenu(connection: IdeConnection, x: number, y: number, origin: HTMLElement | null) {
    menuOrigin.current = origin
    setMenu({ ...menuPosition(x, y), connection })
  }

  const groups = useMemo(() => {
    const counts = new Map<string, number>()
    for (const connection of connections) {
      const group = connectionGroup(connection)
      counts.set(group, (counts.get(group) ?? 0) + 1)
    }
    return [...counts].sort(([left], [right]) => left.localeCompare(right, 'zh-CN'))
  }, [connections])

  const filtered = useMemo(() => {
    const query = search.trim().toLowerCase()
    return connections.filter((item) => {
      if (selectedGroup !== '全部' && connectionGroup(item) !== selectedGroup) return false
      return !query || `${item.label} ${item.username}@${item.host}:${item.port} ${(item.tags ?? []).join(' ')}`.toLowerCase().includes(query)
    })
  }, [connections, search, selectedGroup])

  async function beginConnection(connection: IdeConnection) {
    const attemptId = ++connectionAttempt.current
    const previousConfirmation = progress?.hostKey?.status === 'confirmation_required'
      ? progress.hostKey.confirmationId
      : null
    setMenu(null)
    setProgress({ attemptId, connection, phase: 'preparing', hostKey: null, error: null })
    if (previousConfirmation) {
      void rejectIdeHostKey(previousConfirmation).catch(() => undefined)
    }
    try {
      const hostKey = await prepareIdeConnection(connection.id)
      if (attemptId !== connectionAttempt.current) {
        if (hostKey.status === 'confirmation_required') {
          await rejectIdeHostKey(hostKey.confirmationId).catch(() => undefined)
        }
        return
      }
      if (hostKey.status === 'mismatch') {
        setProgress({ attemptId, connection, phase: 'error', hostKey, error: '服务器主机密钥发生变化，已阻止连接。' })
        return
      }
      if (hostKey.status === 'confirmation_required') {
        setProgress({ attemptId, connection, phase: 'awaiting_trust', hostKey, error: null })
        return
      }
      openWorkspace(attemptId, connection, hostKey)
    } catch (error) {
      if (attemptId === connectionAttempt.current) {
        setProgress({ attemptId, connection, phase: 'error', hostKey: null, error: ideError(error) })
      }
    }
  }

  function openWorkspace(attemptId: number, connection: IdeConnection, hostKey: IdeHostKeyStatus) {
    if (attemptId !== connectionAttempt.current || hostKey.status === 'mismatch') return
    setProgress({ attemptId, connection, phase: 'opening', hostKey, error: null })
    setActive({
      connection,
      fingerprint: hostKey.fingerprint,
      keyAlgorithm: hostKey.keyAlgorithm,
      connectedAt: Date.now(),
    })
    setProgress(null)
  }

  async function trustCurrentHost() {
    if (progress?.hostKey?.status !== 'confirmation_required') return
    const current = progress
    const confirmationId = progress.hostKey.confirmationId
    setProgress({ ...current, phase: 'opening', error: null })
    try {
      const trusted = await trustIdeHostKey(current.connection.id, confirmationId)
      if (current.attemptId === connectionAttempt.current) {
        openWorkspace(current.attemptId, current.connection, trusted)
      }
    } catch (error) {
      if (current.attemptId === connectionAttempt.current) {
        setProgress({ ...current, phase: 'error', error: ideError(error) })
      }
    }
  }

  async function closeProgress() {
    const current = progress
    connectionAttempt.current += 1
    setProgress(null)
    if (current?.hostKey?.status === 'confirmation_required') {
      await rejectIdeHostKey(current.hostKey.confirmationId).catch(() => undefined)
    }
  }

  async function submitConnection(event: FormEvent) {
    event.preventDefault()
    const portText = form.port.trim()
    const port = /^\d{1,5}$/.test(portText) ? Number(portText) : Number.NaN
    if (!Number.isSafeInteger(port) || port < 1 || port > 65_535) {
      setFormError('端口必须是 1 到 65535 之间的整数。')
      return
    }
    if (form.authKind === 'password' && !form.password) {
      setFormError('请输入 SSH 密码。')
      return
    }
    if (form.authKind === 'private_key' && !form.privateKeyPem) {
      setFormError('请粘贴 OpenSSH 或 PEM 私钥。')
      return
    }
    const attemptId = ++createAttempt.current
    setCreating(true)
    setFormError(null)
    const authentication = form.authKind === 'private_key'
      ? {
          kind: 'private_key' as const,
          privateKeyPem: form.privateKeyPem,
          passphrase: form.passphrase || null,
        }
        : { kind: 'password' as const, password: form.password }
    const input = {
      label: form.label.trim() || form.host.trim(),
      host: form.host.trim(),
      port,
      username: form.username.trim(),
      authentication,
      tags: [form.group.trim() || '个人服务器'],
      terminal: { columns: 110, rows: 34, command: null },
    }
    try {
      const create = createIdeConnection(input)
      setForm((current) => ({ ...current, password: '', privateKeyPem: '', passphrase: '' }))
      const connection = await create
      if (attemptId !== createAttempt.current) {
        await disconnectIdeConnection(connection.id).catch(() => undefined)
        return
      }
      setConnections((current) => current.some((item) => item.id === connection.id)
        ? current
        : [...current, connection])
      setShowCreate(false)
      setForm(EMPTY_FORM)
      await beginConnection(connection)
    } catch (error) {
      if (attemptId === createAttempt.current) {
        setForm((current) => ({ ...current, password: '', privateKeyPem: '', passphrase: '' }))
        setFormError(ideError(error))
      }
    } finally {
      if (attemptId === createAttempt.current) setCreating(false)
    }
  }

  async function removeConnection(connection: IdeConnection) {
    setMenu(null)
    setLibraryError(null)
    try {
      await disconnectIdeConnection(connection.id)
      setConnections((current) => current.filter((item) => item.id !== connection.id))
      if (active?.connection.id === connection.id) setActive(null)
    } catch (error) {
      setLibraryError(ideError(error))
      if (active?.connection.id === connection.id) setActive(null)
    }
  }

  async function importCloudAccount(account: CredentialAccount) {
    try {
      const connection = await createIdeConnection({
        label: account.label,
        host: account.host,
        port: account.port,
        username: account.username,
        authentication: { kind: 'password', password: account.password },
        tags: ['云端同步'],
        terminal: { columns: 110, rows: 34, command: null },
      })
      setConnections((current) => current.some((item) => item.id === connection.id) ? current : [...current, connection])
      await beginConnection(connection)
    } catch (error) {
      setLibraryError(ideError(error))
    }
  }

  function reorderConnection(connectionId: string, targetId: string) {
    if (connectionId === targetId) return
    setConnections((current) => {
      const next = [...current]
      const from = next.findIndex((item) => item.id === connectionId)
      const to = next.findIndex((item) => item.id === targetId)
      if (from < 0 || to < 0) return current
      const [moved] = next.splice(from, 1)
      next.splice(to, 0, moved)
      return next
    })
  }

  function reorder(targetId: string) {
    if (!draggedId) return
    reorderConnection(draggedId, targetId)
  }

  function moveConnection(connectionId: string, direction: -1 | 1) {
    const from = filtered.findIndex((item) => item.id === connectionId)
    const target = filtered[from + direction]
    if (from < 0 || !target) return
    reorderConnection(connectionId, target.id)
  }

  if (active) {
    return <IdeWorkspace active={active} onBack={() => setActive(null)} onDisconnect={() => void removeConnection(active.connection)} />
  }

  return (
    <main className="ide-shell" onContextMenu={() => setMenu(null)}>
      <header className="ide-titlebar">
        <div className="ide-brand"><span className="ide-brand-mark"><TerminalSquare size={18} /></span><strong>Terminal233</strong><span>rdesktop · Rust SSH IDE</span></div>
        <button className="ide-primary" onClick={() => { setFormError(null); setShowCreate(true) }} type="button"><CirclePlus size={15} />新建连接</button>
      </header>

      <div className="ide-library-layout">
        <aside className="ide-sidebar">
          <div className="ide-side-heading">连接</div>
          <button className={`ide-side-item ${selectedGroup === '全部' ? 'active' : ''}`} onClick={() => setSelectedGroup('全部')} type="button"><Server size={15} />全部<span>{connections.length}</span></button>
          {groups.map(([group, count]) => <button className={`ide-side-item ${selectedGroup === group ? 'active' : ''}`} key={group} onClick={() => setSelectedGroup(group)} type="button"><Folder size={15} />{group}<span>{count}</span></button>)}
          <CredentialSyncPanel onImport={(account) => void importCloudAccount(account)} />
          <div className="ide-side-note">本地连接信息仅保存在当前进程内。云端账号由自部署服务器加密保存，退出后清除本地会话。</div>
        </aside>

        <section className="ide-library">
          <div className="ide-library-header">
            <div><span className="ide-eyebrow">SSH CONNECTIONS</span><h1>SSH 连接</h1><p>双击或按回车连接。右键可连接或移除；拖拽、Shift + 方向键可排序。</p></div>
          </div>
          {libraryError ? <div className="ide-library-error" role="alert"><span>{libraryError}</span><button onClick={() => void loadConnections()} type="button">重试</button></div> : null}
          <div className="ide-toolbar">
            <label className="ide-search"><Search size={15} /><input aria-label="搜索连接" placeholder="搜索名称、地址或用户" value={search} onChange={(event) => setSearch(event.target.value)} /></label>
            <span>{filtered.length} 个连接</span>
          </div>
          <div className="ide-connection-table" role="table">
            <div className="ide-connection-head" role="row"><span>名称</span><span>地址</span><span>认证</span><span>状态</span><span>操作</span></div>
            {filtered.map((connection) => (
              <div
                className={`ide-connection-row ${draggedId === connection.id ? 'is-dragging' : ''}`}
                draggable
                key={connection.id}
                onContextMenu={(event) => { event.preventDefault(); event.stopPropagation(); openConnectionMenu(connection, event.clientX, event.clientY, event.currentTarget) }}
                onDoubleClick={() => void beginConnection(connection)}
                onDragStart={() => setDraggedId(connection.id)}
                onDragEnd={() => setDraggedId(null)}
                onDragOver={(event) => event.preventDefault()}
                onDrop={() => reorder(connection.id)}
                onKeyDown={(event) => {
                  if (event.target !== event.currentTarget) return
                  if (event.key === 'Enter' || event.key === ' ') {
                    event.preventDefault()
                    void beginConnection(connection)
                  } else if (event.shiftKey && event.key === 'ArrowUp') {
                    event.preventDefault()
                    moveConnection(connection.id, -1)
                  } else if (event.shiftKey && event.key === 'ArrowDown') {
                    event.preventDefault()
                    moveConnection(connection.id, 1)
                  } else if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) {
                    event.preventDefault()
                    const rect = event.currentTarget.getBoundingClientRect()
                    openConnectionMenu(connection, rect.left + 36, rect.top + 24, event.currentTarget)
                  }
                }}
                aria-label={`${connection.label}，${connection.username}@${connection.host}。回车连接，Shift 加上下方向键排序。`}
                role="row"
                tabIndex={0}
              >
                <span className="ide-connection-name"><span className="ide-host-icon"><Server size={16} /></span><span><strong>{connection.label}</strong><small>{connection.username}</small></span></span>
                <code>{connection.host}:{connection.port}</code>
                <span className="ide-auth-pill">纯 Rust puressh · 内存</span>
                <span className="ide-muted"><i />未连接</span>
                <span className="ide-row-actions"><button onClick={() => void beginConnection(connection)} type="button"><Plug size={14} />连接</button><button aria-haspopup="menu" aria-label="更多操作" onClick={(event) => { event.stopPropagation(); const rect = event.currentTarget.getBoundingClientRect(); openConnectionMenu(connection, rect.right - 150, rect.bottom + 4, event.currentTarget) }} type="button"><MoreHorizontal size={16} /></button></span>
              </div>
            ))}
            {loadingConnections && !connections.length ? <div className="ide-empty" role="status"><Server size={34} /><strong>正在读取临时连接…</strong></div> : null}
            {!loadingConnections && !filtered.length ? <div className="ide-empty"><Server size={34} /><strong>{connections.length ? '没有匹配的连接' : '还没有 SSH 连接'}</strong><span>{connections.length ? '请调整搜索词或分组。' : '新建后即可直接使用 Rust SSH 终端与 SFTP。'}</span>{connections.length ? null : <button className="ide-primary" onClick={() => setShowCreate(true)} type="button"><CirclePlus size={15} />新建连接</button>}</div> : null}
          </div>
        </section>
      </div>

      {menu ? <div aria-label={`${menu.connection.label} 操作`} className="ide-context-menu" onKeyDown={handleMenuKeyDown} role="menu" style={{ left: menu.x, top: menu.y }} onClick={(event) => event.stopPropagation()}><button autoFocus onClick={() => void beginConnection(menu.connection)} role="menuitem" type="button"><Plug size={14} />连接</button><button className="danger" onClick={() => void removeConnection(menu.connection)} role="menuitem" type="button"><Trash2 size={14} />移除临时连接</button></div> : null}

      {showCreate ? (
        <div className="ide-overlay" role="presentation">
          <form aria-labelledby="ide-create-title" aria-modal="true" autoComplete="off" className="ide-dialog ide-connect-form" onSubmit={submitConnection} ref={createDialogRef} role="dialog" tabIndex={-1}>
            <div className="ide-dialog-header">
              <div><h2 id="ide-create-title">新建临时 SSH 连接</h2><p>认证材料仅交给 Rust 内存会话，退出后自动清空。</p></div>
              <button aria-label="关闭" onClick={closeCreate} type="button"><X size={17} /></button>
            </div>
            <div className="ide-form-grid">
              <label><span>名称</span><input autoFocus disabled={creating} placeholder="例如：生产服务器" value={form.label} onChange={(event) => setForm({ ...form, label: event.target.value })} /></label>
              <label><span>分组</span><input disabled={creating} placeholder="例如：个人服务器" value={form.group} onChange={(event) => setForm({ ...form, group: event.target.value })} /></label>
              <label><span>主机</span><input disabled={creating} required placeholder="IP 或域名" value={form.host} onChange={(event) => setForm({ ...form, host: event.target.value })} /></label>
              <label><span>端口</span><input disabled={creating} inputMode="numeric" maxLength={5} pattern="[0-9]{1,5}" required value={form.port} onChange={(event) => setForm({ ...form, port: event.target.value })} /></label>
              <label><span>用户</span><input autoComplete="off" disabled={creating} required value={form.username} onChange={(event) => setForm({ ...form, username: event.target.value })} /></label>
              <label>
                <span>认证方式</span>
                <select disabled={creating} value={form.authKind} onChange={(event) => setForm({ ...form, authKind: event.target.value as AuthKind, password: '', privateKeyPem: '', passphrase: '' })}>
                  <option value="password">密码</option>
                  <option value="private_key">粘贴私钥</option>
                </select>
              </label>
              {form.authKind === 'password' ? <label className="ide-form-wide"><span>密码</span><input autoComplete="new-password" disabled={creating} required type="password" value={form.password} onChange={(event) => setForm({ ...form, password: event.target.value })} /></label> : null}
              {form.authKind === 'private_key' ? <><label className="ide-form-wide"><span>私钥</span><textarea autoComplete="off" disabled={creating} maxLength={65_536} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" required rows={6} spellCheck={false} value={form.privateKeyPem} onChange={(event) => setForm({ ...form, privateKeyPem: event.target.value })} /></label><label className="ide-form-wide"><span>私钥口令（可选）</span><input autoComplete="new-password" disabled={creating} maxLength={65_536} type="password" value={form.passphrase} onChange={(event) => setForm({ ...form, passphrase: event.target.value })} /></label></> : null}
            </div>
            {formError ? <p className="ide-error" role="alert">{formError}</p> : null}
            <div className="ide-dialog-actions"><button className="ide-secondary" onClick={closeCreate} type="button">取消</button><button className="ide-primary" disabled={creating} type="submit">{creating ? '正在创建…' : '连接'}</button></div>
          </form>
        </div>
      ) : null}
      {progress ? <ConnectionProgress progress={progress} onClose={() => void closeProgress()} onRetry={() => void beginConnection(progress.connection)} onTrust={() => void trustCurrentHost()} /> : null}
    </main>
  )
}

function ConnectionProgress({ progress, onClose, onRetry, onTrust }: { progress: ProgressState; onClose: () => void; onRetry: () => void; onTrust: () => void }) {
  const dialogRef = useRef<HTMLElement>(null)
  useModalFocus(true, onClose, dialogRef)
  const steps = [
    { label: '初始化配置', active: true, done: true },
    { label: '连接到服务器', active: progress.phase !== 'error', done: progress.phase !== 'preparing' && progress.phase !== 'error' },
    { label: '验证主机密钥', active: progress.phase !== 'error', done: progress.phase === 'opening' },
    { label: '验证 SSH 凭据', active: progress.phase === 'preparing' || progress.phase === 'opening', done: false },
    { label: '打开终端与文件区', active: progress.phase === 'opening', done: false },
  ]
  return <div className="ide-overlay"><section aria-busy={progress.phase === 'preparing' || progress.phase === 'opening'} aria-labelledby="ide-progress-title" aria-modal="true" className="ide-dialog ide-progress-dialog" ref={dialogRef} role="dialog" tabIndex={-1}><div className="ide-dialog-header"><div><h2 id="ide-progress-title">SSH 连接</h2><p>{progress.connection.username}@{progress.connection.host}:{progress.connection.port}</p></div><button aria-label="关闭" onClick={onClose} type="button"><X size={17} /></button></div><div className="ide-progress-body"><ol>{steps.map((step) => <li className={`${step.active ? 'active' : ''} ${step.done ? 'done' : ''}`} key={step.label}><i /><span>{step.label}</span></li>)}</ol><div className="ide-connect-log"><code>Client: pure-Rust puressh</code><code>Transport: in-process TCP</code>{progress.phase === 'preparing' ? <code role="status">正在握手、核对主机密钥并验证 SSH 凭据…</code> : null}{progress.phase === 'opening' ? <code role="status">正在重新握手、复核主机密钥并验证 SSH 凭据…</code> : null}{progress.phase === 'awaiting_trust' && progress.hostKey?.status === 'confirmation_required' ? <div className="ide-fingerprint"><span>首次连接，请核对服务器指纹</span><strong>{progress.hostKey.keyAlgorithm}</strong><code>{progress.hostKey.fingerprint}</code></div> : null}{progress.hostKey?.status === 'mismatch' ? <div className="ide-fingerprint mismatch"><span>当前服务器指纹</span><strong>{progress.hostKey.keyAlgorithm}</strong><code>{progress.hostKey.fingerprint}</code><span>已信任的指纹</span>{progress.hostKey.expectedFingerprints.map((fingerprint) => <code key={fingerprint}>{fingerprint}</code>)}</div> : null}{progress.error ? <p className="ide-error" role="alert">{progress.error}</p> : null}</div></div><div className="ide-dialog-actions">{progress.phase === 'error' ? <button className="ide-secondary" onClick={onRetry} type="button">重试</button> : null}{progress.phase === 'awaiting_trust' ? <button className="ide-primary" onClick={onTrust} type="button">信任并连接</button> : null}<button className="ide-secondary" onClick={onClose} type="button">关闭</button></div></section></div>
}

function IdeWorkspace({ active, onBack, onDisconnect }: { active: ActiveConnection; onBack: () => void; onDisconnect: () => void }) {
  const [cwd, setCwd] = useState('/')
  const [entries, setEntries] = useState<IdeDirectoryEntry[]>([])
  const [loadingFiles, setLoadingFiles] = useState(true)
  const [listingTruncated, setListingTruncated] = useState(false)
  const [fileError, setFileError] = useState<string | null>(null)
  const [terminalState, setTerminalState] = useState<'connecting' | 'connected' | 'closed' | 'error'>('connecting')
  const [terminalGeneration, setTerminalGeneration] = useState(0)
  const [transfers, setTransfers] = useState<Map<string, IdeTransferProgress>>(() => new Map())
  const [fileMenu, setFileMenu] = useState<({ x: number; y: number } & FileTarget) | null>(null)
  const [dialog, setDialog] = useState<FileDialog | null>(null)
  const [dialogValue, setDialogValue] = useState('')
  const [operationPending, setOperationPending] = useState(false)
  const [notice, setNotice] = useState<Notice | null>(null)
  const [now, setNow] = useState(Date.now())
  const downloadParts = useRef(new Map<string, DownloadCollector>())
  const downloadJobs = useRef(new Set<string>())
  const finishedDownloadJobs = useRef(new Set<string>())
  const uploadJobs = useRef(new Set<string>())
  const workspaceActive = useRef(true)
  const cwdRef = useRef(cwd)
  const directoryGeneration = useRef(0)
  const dialogRef = useRef<HTMLElement>(null)
  const fileMenuOrigin = useRef<HTMLElement | null>(null)
  const uploadInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => { cwdRef.current = cwd }, [cwd])
  useModalFocus(Boolean(dialog), closeFileDialog, dialogRef)

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1_000)
    return () => window.clearInterval(timer)
  }, [])

  useEffect(() => {
    if (!fileMenu) return
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      setFileMenu(null)
      fileMenuOrigin.current?.focus()
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [fileMenu])

  async function load(path: string) {
    const generation = ++directoryGeneration.current
    setLoadingFiles(true)
    setFileError(null)
    try {
      const listing = await listIdeDirectory(active.connection.id, path)
      if (!workspaceActive.current || generation !== directoryGeneration.current) return
      cwdRef.current = listing.path
      setCwd(listing.path)
      setEntries(listing.entries)
      setListingTruncated(listing.truncated)
    } catch (error) {
      if (workspaceActive.current && generation === directoryGeneration.current) {
        setFileError(ideError(error))
      }
    } finally {
      if (workspaceActive.current && generation === directoryGeneration.current) {
        setLoadingFiles(false)
      }
    }
  }

  useEffect(() => { void load('/') }, [active.connection.id])

  useEffect(() => {
    let stopped = false
    workspaceActive.current = true
    let progressCleanup: (() => void) | undefined
    void onIdeTransferProgress((event) => {
      if (stopped) return
      if (downloadJobs.current.has(event.jobId)) {
        setTransfers((current) => {
          const next = new Map(current)
          next.set(event.jobId, event)
          return next
        })
      }
    }).then((unlisten) => {
      if (stopped) unlisten()
      else progressCleanup = unlisten
    }).catch((error) => {
      if (!stopped) setNotice({ tone: 'error', message: `无法监听传输进度：${ideError(error)}` })
    })
    return () => {
      stopped = true
      workspaceActive.current = false
      directoryGeneration.current += 1
      progressCleanup?.()
      for (const jobId of downloadJobs.current) void cancelIdeTransfer(jobId).catch(() => undefined)
      for (const jobId of uploadJobs.current) void cancelIdeTransfer(jobId).catch(() => undefined)
      downloadJobs.current.clear()
      uploadJobs.current.clear()
      finishedDownloadJobs.current.clear()
      downloadParts.current.clear()
    }
  }, [active.connection.id])

  function finishUpload(event: IdeTransferComplete) {
    uploadJobs.current.delete(event.jobId)
    setTransfers((current) => {
      const next = new Map(current)
      next.delete(event.jobId)
      return next
    })
    if (event.status === 'completed' && event.failedFiles === 0 && event.cancelledFiles === 0) {
      setNotice({ tone: 'success', message: `上传完成：${event.completedFiles} 个文件。` })
      void load(cwdRef.current)
    } else if (event.status === 'cancelled') {
      setNotice({ tone: 'info', message: '上传已取消。' })
    } else {
      setNotice({
        tone: 'error',
        message: `上传结束：成功 ${event.completedFiles}，失败 ${event.failedFiles}，取消 ${event.cancelledFiles}。请检查远端权限与可用空间。`,
      })
    }
  }

  async function upload(files: File[]) {
    if (!files.length) return
    setNotice(null)
    const expectedBytes = files.reduce((total, file) => total + file.size, 0)
    let channelJobId: string | null = null
    let channelStarted: Extract<IdeUploadEvent, { kind: 'started' }> | null = null
    let completedBeforeResponse = false
    let channelInvalid = false
    let highestProgressBytes = 0
    const clearUploadTracking = (...jobIds: Array<string | null>) => {
      const trackedJobIds = new Set(jobIds.filter((jobId): jobId is string => Boolean(jobId)))
      trackedJobIds.forEach((jobId) => uploadJobs.current.delete(jobId))
      setTransfers((current) => {
        const next = new Map(current)
        trackedJobIds.forEach((jobId) => next.delete(jobId))
        return next
      })
    }
    const failUploadChannel = (eventJobId: string, message: string) => {
      channelInvalid = true
      if (eventJobId) void cancelIdeTransfer(eventJobId).catch(() => undefined)
      if (channelJobId && channelJobId !== eventJobId) {
        void cancelIdeTransfer(channelJobId).catch(() => undefined)
      }
      clearUploadTracking(eventJobId, channelJobId)
      if (workspaceActive.current) setNotice({ tone: 'error', message })
    }
    const receiveUploadEvent = (event: IdeUploadEvent) => {
      if (channelInvalid) return
      if (completedBeforeResponse) {
        failUploadChannel(event.jobId, '上传任务在完成后仍收到数据，已丢弃异常结果。')
        return
      }
      if (channelJobId && channelJobId !== event.jobId) {
        failUploadChannel(event.jobId, '上传数据通道混入了其他任务，已取消传输。')
        return
      }
      channelJobId = event.jobId
      if (!workspaceActive.current) {
        if (event.kind !== 'completed') void cancelIdeTransfer(event.jobId).catch(() => undefined)
        return
      }
      if (event.kind === 'started') {
        const valid = !channelStarted
          && event.jobId.length > 0
          && event.direction === 'upload'
          && event.totalFiles === files.length
          && event.totalBytes === expectedBytes
          && event.items.length === files.length
          && new Set(event.items.map((item) => item.fileId)).size === files.length
          && event.items.every((item, index) => (
            item.fileId.length > 0
            && item.name === files[index]?.name
            && item.size === files[index]?.size
          ))
        if (!valid) {
          failUploadChannel(event.jobId, '上传任务元数据校验失败，已取消传输。')
          return
        }
        channelStarted = event
        uploadJobs.current.add(event.jobId)
        setNotice({ tone: 'info', message: `已开始上传 ${event.totalFiles} 个文件。` })
        return
      }
      if (event.kind === 'progress') {
        const item = channelStarted?.items.find((candidate) => candidate.fileId === event.fileId)
        if (
          event.direction !== 'upload'
          || !channelStarted
          || !item
          || item.name !== event.name
          || item.size !== event.fileTotalBytes
          || !isNonNegativeSafeInteger(event.fileBytesTransferred)
          || !isNonNegativeSafeInteger(event.jobBytesTransferred)
          || event.fileBytesTransferred > (event.fileTotalBytes ?? 0)
          || event.jobTotalBytes !== expectedBytes
          || event.jobBytesTransferred > expectedBytes
        ) {
          failUploadChannel(event.jobId, '上传进度校验失败，已取消传输。')
          return
        }
        // Worker progress can arrive out of order across files. Never let the aggregate progress
        // indicator move backwards, while still accepting every individually valid worker event.
        if (event.jobBytesTransferred < highestProgressBytes) return
        highestProgressBytes = event.jobBytesTransferred
        setTransfers((current) => new Map(current).set(event.jobId, event))
        return
      }
      const accountedFiles = event.completedFiles + event.failedFiles + event.cancelledFiles
      const countsAreValid = isNonNegativeSafeInteger(event.completedFiles)
        && isNonNegativeSafeInteger(event.failedFiles)
        && isNonNegativeSafeInteger(event.cancelledFiles)
      const statusIsConsistent = (
        event.status === 'completed'
        && event.completedFiles === files.length
        && event.failedFiles === 0
        && event.cancelledFiles === 0
        && event.bytesTransferred === expectedBytes
      ) || (
        event.status === 'completed_with_errors'
        && event.failedFiles > 0
      ) || (
        event.status === 'cancelled'
        && event.failedFiles === 0
        && event.cancelledFiles > 0
      )
      const validCompletion = event.direction === 'upload'
        && Boolean(channelStarted)
        && countsAreValid
        && isNonNegativeSafeInteger(event.bytesTransferred)
        && event.totalBytes === expectedBytes
        && event.bytesTransferred <= expectedBytes
        && accountedFiles === files.length
        && statusIsConsistent
      if (!validCompletion) {
        failUploadChannel(event.jobId, '上传结果校验失败，已取消传输。')
        return
      }
      completedBeforeResponse = true
      finishUpload(event)
    }
    try {
      const started = await uploadIdeFiles(
        active.connection.id,
        cwdRef.current,
        files,
        receiveUploadEvent,
      )
      if (!workspaceActive.current) {
        await cancelIdeTransfer(started.jobId).catch(() => undefined)
        return
      }
      // Upload channel callbacks may run before the invoke promise resolves. TypeScript cannot
      // infer that mutation across the callback boundary, so snapshot the observed start here.
      const observedStarted = channelStarted as Extract<IdeUploadEvent, { kind: 'started' }> | null
      const matchesChannel = !channelInvalid
        && channelJobId === started.jobId
        && observedStarted?.direction === started.direction
        && observedStarted?.totalFiles === started.totalFiles
        && observedStarted?.totalBytes === started.totalBytes
        && observedStarted?.items.length === started.items.length
        && observedStarted?.items.every((item, index) => {
          const responseItem = started.items[index]
          return item.fileId === responseItem?.fileId
            && item.name === responseItem.name
            && item.size === responseItem.size
        })
      if (!matchesChannel) {
        failUploadChannel(started.jobId, '上传启动响应与数据通道不匹配，已取消传输。')
        return
      }
      if (!completedBeforeResponse) uploadJobs.current.add(started.jobId)
    } catch (error) {
      clearUploadTracking(channelJobId)
      if (workspaceActive.current && !channelInvalid) {
        setNotice({ tone: 'error', message: ideError(error) })
      }
    }
  }

  async function download(target: FileTarget) {
    if (target.entry.isDirectory) return
    setNotice(null)
    let channelJobId: string | null = null
    const receiveDownloadEvent = (event: IdeDownloadEvent) => {
      if ((channelJobId && channelJobId !== event.jobId) || (!channelJobId && event.kind !== 'started')) {
        setNotice({ tone: 'error', message: '下载数据通道混入了其他任务，已取消传输。' })
        void cancelIdeTransfer(event.jobId).catch(() => undefined)
        return
      }
      channelJobId = event.jobId
      if (event.kind === 'completed') finishedDownloadJobs.current.add(event.jobId)
      downloadJobs.current.add(event.jobId)
      if (!workspaceActive.current) {
        if (event.kind !== 'completed') void cancelIdeTransfer(event.jobId).catch(() => undefined)
        return
      }
      if (event.kind === 'started') {
        const files = new Map<string, DownloadFileCollector>()
        const valid = event.direction === 'download'
          && event.totalFiles === event.items.length
          && event.totalFiles > 0
          && event.totalFiles <= 32
          && (event.totalBytes === null || event.totalBytes <= MAX_DOWNLOAD_JOB_BYTES)
          && event.items.length === 1
          && event.items[0]?.name === target.entry.name
          && isSafeLocalFileName(event.items[0]?.name ?? '')
        for (const item of event.items) {
          if (
            files.has(item.fileId)
            || !isSafeLocalFileName(item.name)
            || (item.size !== null && item.size > MAX_DOWNLOAD_FILE_BYTES)
          ) {
            setNotice({ tone: 'error', message: '下载任务包含无效文件元数据，已取消传输。' })
            void cancelIdeTransfer(event.jobId).catch(() => undefined)
            return
          }
          files.set(item.fileId, {
            name: item.name,
            expectedBytes: item.size,
            nextOffset: 0,
            bytes: 0,
            parts: [],
          })
        }
        const job = {
          files,
          bytes: 0,
          invalid: !valid,
          nextSequence: 1,
          expectedBytes: event.totalBytes,
        }
        downloadParts.current.set(event.jobId, job)
        if (!valid) {
          setNotice({ tone: 'error', message: '下载任务元数据校验失败，已取消传输。' })
          void cancelIdeTransfer(event.jobId).catch(() => undefined)
        }
        return
      }
      if (event.kind === 'chunk') {
        const job = downloadParts.current.get(event.jobId)
        const file = job?.files.get(event.fileId)
        try {
          if (!job || !file || file.name !== event.name) {
            throw new Error('下载数据与任务元数据不匹配，已取消传输。')
          }
          const bytes = decodeBase64Bytes(event.dataBase64)
          const nextFileBytes = file.bytes + bytes.byteLength
          const nextJobBytes = job.bytes + bytes.byteLength
          if (
            job.invalid
            || event.sequence !== job.nextSequence
            || event.offset !== file.nextOffset
            || nextFileBytes > MAX_DOWNLOAD_FILE_BYTES
            || nextJobBytes > MAX_DOWNLOAD_JOB_BYTES
          ) {
            throw new Error('下载数据顺序或大小校验失败，已取消传输。')
          }
          file.parts.push(bytes)
          job.nextSequence += 1
          file.nextOffset += bytes.byteLength
          file.bytes = nextFileBytes
          job.bytes = nextJobBytes
          job.files.set(event.fileId, file)
          downloadParts.current.set(event.jobId, job)
          // ACK only after the exact bytes have passed continuity and memory-limit checks.
          void acknowledgeIdeDownloadChunk(event.jobId, event.sequence).catch(() => {
            job.invalid = true
            setNotice({ tone: 'error', message: '下载数据确认失败，已取消传输。' })
            void cancelIdeTransfer(event.jobId).catch(() => undefined)
          })
        } catch (error) {
          if (job) {
            job.invalid = true
            downloadParts.current.set(event.jobId, job)
          }
          setNotice({ tone: 'error', message: ideError(error) })
          void cancelIdeTransfer(event.jobId).catch(() => undefined)
        }
        return
      }

      const job = downloadParts.current.get(event.jobId)
      const exactFileSizes = job
        ? [...job.files.values()].every((file) => file.expectedBytes === null || file.expectedBytes === file.bytes)
        : false
      const valid = event.status === 'completed'
        && event.bytesTransferred === job?.bytes
        && (event.totalBytes === null || event.totalBytes === job?.bytes)
        && (job?.expectedBytes === null || job?.expectedBytes === job?.bytes)
        && event.failedFiles === 0
        && event.cancelledFiles === 0
        && event.completedFiles === job?.files.size
        && exactFileSizes
        && !job?.invalid
        && Boolean(job?.files.size)
      if (valid && job) {
        for (const file of job.files.values()) {
          const blob = new Blob(file.parts.map((part) => part as BlobPart))
          const url = URL.createObjectURL(blob)
          const link = document.createElement('a')
          link.href = url
          link.download = file.name
          link.hidden = true
          document.body.append(link)
          link.click()
          link.remove()
          window.setTimeout(() => URL.revokeObjectURL(url), 0)
        }
        setNotice({ tone: 'success', message: `下载完成：${event.completedFiles} 个文件。` })
      } else if (event.status === 'cancelled') {
        setNotice({ tone: 'info', message: '下载已取消，未写入本地文件。' })
      } else {
        setNotice({ tone: 'error', message: '下载未完成或数据校验失败，未写入本地文件。' })
      }
      downloadParts.current.delete(event.jobId)
      downloadJobs.current.delete(event.jobId)
      setTransfers((current) => {
        const next = new Map(current)
        next.delete(event.jobId)
        return next
      })
    }
    try {
      const started = await downloadIdeFiles(
        active.connection.id,
        [{ remotePath: target.path, saveAs: target.entry.name }],
        receiveDownloadEvent,
      )
      const collector = downloadParts.current.get(started.jobId)
      if (
        (channelJobId !== null && channelJobId !== started.jobId)
        || started.direction !== 'download'
        || started.totalFiles !== 1
        || started.items[0]?.name !== target.entry.name
        || (collector && collector.files.size !== started.items.length)
        || (collector && collector.expectedBytes !== started.totalBytes)
        || (collector && started.items.some((item) => {
          const file = collector.files.get(item.fileId)
          return !file || file.name !== item.name || file.expectedBytes !== item.size
        }))
      ) {
        await cancelIdeTransfer(started.jobId).catch(() => undefined)
        setNotice({ tone: 'error', message: '下载启动响应与数据通道不匹配，已取消传输。' })
        return
      }
      if (finishedDownloadJobs.current.delete(started.jobId)) return
      if (!workspaceActive.current) {
        await cancelIdeTransfer(started.jobId).catch(() => undefined)
      } else {
        downloadJobs.current.add(started.jobId)
      }
    } catch (error) {
      setNotice({ tone: 'error', message: ideError(error) })
    }
  }

  async function commitDialog() {
    if (!dialog || operationPending) return
    if (dialog.kind !== 'delete' && !isSafeRemoteName(dialogValue)) {
      setNotice({ tone: 'error', message: '名称不能为空，且不能是 .、.. 或包含路径分隔符/控制字符。' })
      return
    }
    setOperationPending(true)
    setNotice(null)
    try {
      if (dialog.kind === 'mkdir') {
        await createIdeDirectory(active.connection.id, childPath(dialog.directory, dialogValue))
      }
      if (dialog.kind === 'rename') {
        await renameIdeRemote(active.connection.id, dialog.path, childPath(dialog.directory, dialogValue))
      }
      if (dialog.kind === 'delete') {
        await deleteIdeRemote(active.connection.id, dialog.path)
      }
      const completedKind = dialog.kind
      const refreshDirectory = dialog.directory
      setDialog(null)
      setDialogValue('')
      setNotice({
        tone: 'success',
        message: completedKind === 'mkdir' ? '远端文件夹已创建。' : completedKind === 'rename' ? '远端项目已重命名。' : '远端项目已删除。',
      })
      await load(refreshDirectory)
    } catch (error) {
      setNotice({ tone: 'error', message: ideError(error) })
    } finally {
      if (workspaceActive.current) setOperationPending(false)
    }
  }

  function closeFileDialog() {
    if (operationPending) return
    setDialog(null)
    setDialogValue('')
  }

  async function cancelTransferJob(jobId: string) {
    try {
      await cancelIdeTransfer(jobId)
      setNotice({ tone: 'info', message: '已请求取消传输。' })
    } catch (error) {
      setNotice({ tone: 'error', message: ideError(error) })
    }
  }

  function targetForEntry(entry: IdeDirectoryEntry): FileTarget {
    return { entry, path: childPath(cwd, entry.name), directory: cwd }
  }

  function activateFileTarget(target: FileTarget) {
    if (target.entry.isDirectory) void load(target.path)
    else void download(target)
  }

  function openFileContextMenu(target: FileTarget, x: number, y: number, origin: HTMLElement) {
    fileMenuOrigin.current = origin
    setFileMenu({ ...menuPosition(x, y), ...target })
  }

  const elapsed = Math.max(0, Math.floor((now - active.connectedAt) / 1000))
  return <main className="ide-shell ide-workspace-shell" onClick={() => setFileMenu(null)}>
    <header className="ide-titlebar ide-workspace-titlebar"><button aria-label="返回连接列表" className="ide-icon-button" onClick={onBack} title="返回连接列表" type="button"><ChevronLeft size={16} /></button><div className="ide-session-tab"><i /><Server size={14} /><strong>{active.connection.label}</strong><span>{active.connection.username}@{active.connection.host}</span></div><div className="ide-title-actions"><span title={`${active.keyAlgorithm} · ${active.fingerprint}`}>纯 Rust puressh · 主机已验证</span><button className="ide-danger-button" onClick={onDisconnect} type="button">断开并清除</button></div></header>
    <section className="ide-workspace-grid">
      <aside className="ide-file-pane" onDragOver={(event) => event.preventDefault()} onDrop={(event) => { event.preventDefault(); void upload(Array.from(event.dataTransfer.files)) }}>
        <div className="ide-pane-tabs"><span className="active"><Folder size={14} />远端文件</span></div>
        <div className="ide-file-toolbar"><button aria-label="返回上级目录" disabled={cwd === '/'} onClick={() => void load(parentPath(cwd))} title="返回上级目录" type="button"><ChevronLeft size={15} /></button><code title={cwd}>{cwd}</code><button aria-label="刷新远端目录" onClick={() => void load(cwdRef.current)} title="刷新" type="button"><RefreshCw size={14} /></button><button aria-label="新建远端文件夹" onClick={() => { setDialog({ kind: 'mkdir', directory: cwd }); setDialogValue('新建文件夹') }} title="新建文件夹" type="button"><FolderPlus size={14} /></button><button aria-label="上传文件" onClick={() => uploadInputRef.current?.click()} title="上传文件" type="button"><Upload size={14} /></button><input aria-label="选择要上传的文件" className="ide-hidden-input" multiple onChange={(event) => { void upload(Array.from(event.target.files ?? [])); event.target.value = '' }} ref={uploadInputRef} type="file" /></div>
        <div className="ide-file-list">{loadingFiles ? <div className="ide-pane-empty" role="status">正在通过 SFTP 读取…</div> : fileError ? <div className="ide-pane-empty error" role="alert">{fileError}<button onClick={() => void load(cwdRef.current)} type="button">重试</button></div> : entries.length ? entries.map((entry) => { const target = targetForEntry(entry); return <button aria-label={`${entry.name}，${entry.isDirectory ? '文件夹，回车打开' : `文件，${formatBytes(entry.size)}，回车下载`}`} className="ide-file-row" key={entry.name} onContextMenu={(event) => { event.preventDefault(); event.stopPropagation(); openFileContextMenu(target, event.clientX, event.clientY, event.currentTarget) }} onDoubleClick={() => activateFileTarget(target)} onKeyDown={(event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); activateFileTarget(target) } else if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) { event.preventDefault(); const rect = event.currentTarget.getBoundingClientRect(); openFileContextMenu(target, rect.left + 28, rect.top + 22, event.currentTarget) } }} type="button">{entry.isDirectory ? <Folder className="folder" size={16} /> : <File size={16} />}<span>{entry.name}</span><small>{entry.isDirectory ? '文件夹' : formatBytes(entry.size)}</small></button> }) : <div className="ide-pane-empty">此目录为空</div>}</div>
        <div className="ide-file-status"><span>{entries.length} 项{listingTruncated ? '（已截断）' : ''}</span><span>拖入文件即可上传</span></div>
      </aside>
      <section className="ide-terminal-pane"><div className="ide-pane-tabs"><span className="active"><TerminalSquare size={14} />终端</span><div className="ide-terminal-actions"><span aria-atomic="true" aria-live="polite" className={`ide-terminal-state ${terminalState}`} role="status">{terminalState === 'connected' ? '已连接' : terminalState === 'connecting' ? '连接中' : terminalState === 'error' ? '错误' : '已关闭'}</span>{terminalState === 'closed' || terminalState === 'error' ? <button onClick={() => { setTerminalState('connecting'); setTerminalGeneration((current) => current + 1) }} type="button">重新打开</button> : null}</div></div><XtermPane connectionId={active.connection.id} key={terminalGeneration} onState={setTerminalState} /></section>
      <aside className="ide-monitor-pane"><div className="ide-pane-tabs"><span className="active"><Activity size={14} />会话</span></div><div className="ide-monitor-card"><div className="ide-monitor-title"><Server size={16} /><strong>连接</strong></div><dl><div><dt>主机</dt><dd>{active.connection.host}:{active.connection.port}</dd></div><div><dt>用户</dt><dd>{active.connection.username}</dd></div><div><dt>状态</dt><dd className="ok">● 已验证</dd></div></dl></div><div className="ide-monitor-card"><div className="ide-monitor-title"><HardDrive size={16} /><strong>工作区</strong></div><dl><div><dt>目录</dt><dd title={cwd}>{cwd}</dd></div><div><dt>终端</dt><dd>{terminalState}</dd></div><div><dt>时长</dt><dd>{formatDuration(elapsed)}</dd></div></dl></div><div className="ide-monitor-card"><div className="ide-monitor-title"><Activity size={16} /><strong>传输</strong></div>{transfers.size ? [...transfers.values()].map((transfer) => <div className="ide-transfer" key={transfer.jobId}><span>{transfer.direction === 'upload' ? '上传' : '下载'} · {transfer.name}</span><progress aria-label={`${transfer.name} 传输进度`} max={transfer.jobTotalBytes ?? 1} value={transfer.jobBytesTransferred} /><small>{formatBytes(transfer.jobBytesTransferred)} / {formatBytes(transfer.jobTotalBytes)}</small><button onClick={() => void cancelTransferJob(transfer.jobId)} type="button">取消</button></div>) : <p className="ide-monitor-empty">当前没有传输任务</p>}</div><div className="ide-native-note">SSH 与 SFTP 均由纯 Rust puressh 进程内执行，不调用 Windows SSH、PuTTY 或 plink。</div></aside>
    </section>
    {fileMenu ? <FileContextMenu menu={fileMenu} onDownload={() => { setFileMenu(null); void download(fileMenu) }} onRename={() => { setDialog({ kind: 'rename', entry: fileMenu.entry, path: fileMenu.path, directory: fileMenu.directory }); setDialogValue(fileMenu.entry.name); setFileMenu(null) }} onDelete={() => { setDialog({ kind: 'delete', entry: fileMenu.entry, path: fileMenu.path, directory: fileMenu.directory }); setFileMenu(null) }} /> : null}
    {dialog ? <div className="ide-overlay"><section aria-labelledby="ide-file-dialog-title" aria-modal="true" className="ide-dialog ide-small-dialog" ref={dialogRef} role="dialog" tabIndex={-1}><div className="ide-dialog-header"><div><h2 id="ide-file-dialog-title">{dialog.kind === 'mkdir' ? '新建远端文件夹' : dialog.kind === 'rename' ? '重命名' : '确认删除'}</h2><p>{dialog.kind === 'delete' ? '删除后无法从 Terminal233 恢复；文件夹必须为空。' : dialog.directory}</p></div><button aria-label="关闭" disabled={operationPending} onClick={closeFileDialog} type="button"><X size={17} /></button></div>{dialog.kind === 'delete' ? <code className="ide-delete-path">{dialog.path}</code> : <label className="ide-dialog-input"><span>名称</span><input autoFocus disabled={operationPending} maxLength={255} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); void commitDialog() } }} value={dialogValue} onChange={(event) => setDialogValue(event.target.value)} /></label>}<div className="ide-dialog-actions"><button className="ide-secondary" disabled={operationPending} onClick={closeFileDialog} type="button">取消</button><button className={dialog.kind === 'delete' ? 'ide-delete' : 'ide-primary'} disabled={operationPending} onClick={() => void commitDialog()} type="button">{operationPending ? '正在处理…' : dialog.kind === 'delete' ? '确认删除' : '确定'}</button></div></section></div> : null}
    {notice ? <div aria-live={notice.tone === 'error' ? 'assertive' : 'polite'} className={`ide-toast ${notice.tone}`} role={notice.tone === 'error' ? 'alert' : 'status'}><span>{notice.message}</span><button aria-label="关闭通知" onClick={() => setNotice(null)} type="button"><X size={14} /></button></div> : null}
  </main>
}

function XtermPane({ connectionId, onState }: { connectionId: string; onState: (state: 'connecting' | 'connected' | 'closed' | 'error') => void }) {
  const host = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!host.current) return
    let disposed = false
    let sessionId: string | null = null
    let provisionalSessionId: string | null = null
    let sessionEnded = false
    let terminalFailed = false
    let expectedOutputSequence = 1
    let inputQueue: IdeTerminalInputQueue | null = null
    let pendingResize: { columns: number; rows: number } | null = null
    let resizing = false
    const reducedMotion = typeof window.matchMedia === 'function'
      ? window.matchMedia('(prefers-reduced-motion: reduce)')
      : null
    const terminal = new XTerm({
      cursorBlink: !reducedMotion?.matches,
      convertEol: false,
      fontFamily: '"JetBrains Mono", "Cascadia Code", Consolas, monospace',
      fontSize: 13,
      lineHeight: 1.25,
      scrollback: 8000,
      theme: { background: '#0b0e13', foreground: '#d9e1eb', cursor: '#4ade80', selectionBackground: '#31577a88', black: '#11151b', brightBlack: '#687384', green: '#4ade80', brightGreen: '#86efac', blue: '#60a5fa', brightBlue: '#93c5fd' },
    })
    const fit = new FitAddon()
    terminal.loadAddon(fit)
    terminal.open(host.current)
    fit.fit()
    const syncReducedMotion = () => {
      terminal.options.cursorBlink = !reducedMotion?.matches
    }
    reducedMotion?.addEventListener('change', syncReducedMotion)

    const knownSessionId = () => sessionId ?? provisionalSessionId
    const closeKnownSession = () => {
      const id = knownSessionId()
      if (id) void closeIdeTerminal(id).catch(() => undefined)
    }
    const failTerminal = (error: unknown) => {
      if (disposed || terminalFailed || sessionEnded) return
      terminalFailed = true
      inputQueue?.close()
      onState('error')
      terminal.write(`\r\n\x1b[31m[${sanitizeTerminalNotice(ideError(error))}]\x1b[0m\r\n`)
      closeKnownSession()
    }
    const flushResize = async () => {
      if (resizing || disposed || terminalFailed || sessionEnded) return
      const id = sessionId
      if (!id) return
      resizing = true
      try {
        while (pendingResize && !disposed && !terminalFailed && !sessionEnded) {
          const size = pendingResize
          pendingResize = null
          await resizeIdeTerminal(id, size.columns, size.rows)
        }
      } catch (error) {
        failTerminal(error)
      } finally {
        resizing = false
        if (pendingResize && !disposed && !terminalFailed && !sessionEnded) void flushResize()
      }
    }
    const handleTerminalEvent = (event: IdeTerminalEvent) => {
      if (provisionalSessionId && event.sessionId !== provisionalSessionId) {
        failTerminal(new Error('终端事件与当前会话不匹配，会话已安全关闭。'))
        return
      }
      provisionalSessionId = event.sessionId
      if (disposed) {
        closeKnownSession()
        return
      }
      if (event.kind === 'output') {
        if (sessionEnded || terminalFailed) return
        try {
          if (event.sequence !== expectedOutputSequence) {
            throw new Error('终端输出顺序异常，会话已安全关闭。')
          }
          expectedOutputSequence += 1
          const bytes = decodeBase64Bytes(event.dataBase64)
          terminal.write(bytes, () => {
            if (disposed || terminalFailed || sessionEnded) return
            void acknowledgeIdeTerminalOutput(event.sessionId, event.sequence).catch(failTerminal)
          })
        } catch {
          failTerminal(new Error('终端返回了无法解析的原始字节，会话已安全关闭。'))
        }
        return
      }
      if (event.kind === 'error') {
        if (!terminalFailed) {
          terminalFailed = true
          inputQueue?.close()
          onState('error')
          terminal.write(`\r\n\x1b[31m[${terminalEventError(event.code)}]\x1b[0m\r\n`)
          closeKnownSession()
        }
        return
      }
      sessionEnded = true
      inputQueue?.close()
      if (!terminalFailed) onState('closed')
      terminal.write(`\r\n\x1b[90m[会话已关闭${event.exitStatus === null ? '' : `，退出码 ${event.exitStatus}`}]\x1b[0m\r\n`)
    }

    const resizeObserver = new ResizeObserver(() => {
      fit.fit()
      pendingResize = { columns: terminal.cols, rows: terminal.rows }
      void flushResize()
    })
    resizeObserver.observe(host.current)
    const dataDisposable = terminal.onData((data) => {
      if (!inputQueue || terminalFailed || sessionEnded) return
      if (!inputQueue.enqueue(data)) {
        failTerminal(new Error('终端输入积压超过安全上限；为避免丢失或乱序，会话已关闭。'))
      }
    })
    onState('connecting')
    void (async () => {
      try {
        const started = await startIdeTerminal(
          connectionId,
          handleTerminalEvent,
          terminal.cols,
          terminal.rows,
        )
        if (provisionalSessionId && provisionalSessionId !== started.sessionId) {
          void closeIdeTerminal(started.sessionId).catch(() => undefined)
          failTerminal(new Error('终端启动响应与事件会话不匹配，会话已安全关闭。'))
          return
        }
        sessionId = started.sessionId
        provisionalSessionId = started.sessionId
        if (disposed || sessionEnded || terminalFailed) {
          await closeIdeTerminal(started.sessionId).catch(() => undefined)
          return
        }
        inputQueue = new IdeTerminalInputQueue(started.sessionId, failTerminal)
        pendingResize = { columns: terminal.cols, rows: terminal.rows }
        void flushResize()
        onState('connected')
        terminal.focus()
      } catch (error) {
        failTerminal(error)
      }
    })()
    return () => {
      disposed = true
      inputQueue?.close()
      reducedMotion?.removeEventListener('change', syncReducedMotion)
      resizeObserver.disconnect()
      dataDisposable.dispose()
      closeKnownSession()
      terminal.dispose()
    }
  }, [connectionId, onState])
  return <div aria-label="交互式 SSH 终端" className="ide-xterm" ref={host} role="region" />
}

function FileContextMenu({ menu, onDownload, onRename, onDelete }: { menu: { x: number; y: number; entry: IdeDirectoryEntry }; onDownload: () => void; onRename: () => void; onDelete: () => void }) {
  return <div aria-label={`${menu.entry.name} 操作`} className="ide-context-menu" onKeyDown={handleMenuKeyDown} role="menu" style={{ left: menu.x, top: menu.y }} onClick={(event) => event.stopPropagation()}>{!menu.entry.isDirectory ? <button autoFocus onClick={onDownload} role="menuitem" type="button"><Download size={14} />下载</button> : <button autoFocus onClick={onRename} role="menuitem" type="button"><Pencil size={14} />重命名</button>}{!menu.entry.isDirectory ? <button onClick={onRename} role="menuitem" type="button"><Pencil size={14} />重命名</button> : null}<button className="danger" onClick={onDelete} role="menuitem" type="button"><Trash2 size={14} />删除</button></div>
}

function childPath(cwd: string, name: string) {
  return cwd === '/' ? `/${name}` : `${cwd.replace(/\/+$/, '')}/${name}`
}

function isSafeRemoteName(name: string) {
  return name.length > 0
    && name.length <= 255
    && name !== '.'
    && name !== '..'
    && !name.includes('/')
    && !name.includes('\\')
    && ![...name].some((character) => {
      const code = character.codePointAt(0) ?? 0
      return code === 0 || code === 127 || code < 32
    })
}

function isSafeLocalFileName(name: string) {
  return isSafeRemoteName(name) && !name.includes(':')
}

function isNonNegativeSafeInteger(value: number): boolean {
  return Number.isSafeInteger(value) && value >= 0
}

function parentPath(path: string) {
  const parts = path.split('/').filter(Boolean)
  parts.pop()
  return `/${parts.join('/')}`
}

function formatBytes(bytes: number | null) {
  if (bytes === null) return '—'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 ** 2) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 ** 2).toFixed(1)} MB`
}

function formatDuration(seconds: number) {
  if (seconds < 60) return `${seconds} 秒`
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分`
  return `${Math.floor(seconds / 3600)} 时 ${Math.floor((seconds % 3600) / 60)} 分`
}

function terminalEventError(code: string) {
  switch (code) {
    case 'ssh_terminal_output_backpressure':
      return '终端渲染长时间没有确认输出，会话已关闭以保护内存。'
    case 'ssh_terminal_output_delivery_failed':
      return '终端输出无法送达当前窗口，会话已关闭。'
    case 'ssh_terminal_output_sequence_exhausted':
      return '终端输出序列已耗尽，会话已关闭。'
    default:
      return 'SSH 终端传输中断，请检查网络与服务器状态。'
  }
}

function sanitizeTerminalNotice(value: string) {
  return value.replace(/[\u0000-\u001f\u007f-\u009f]/g, ' ').trim()
}
