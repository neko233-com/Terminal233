import { useEffect, useMemo, useRef, useState, type DragEvent, type FormEvent, type MouseEvent as ReactMouseEvent } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal as XTerm } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import './tauri-ide.css'
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
  createIdeConnection,
  createIdeDirectory,
  deleteIdeRemote,
  disconnectIdeConnection,
  downloadIdeFiles,
  ideError,
  listIdeConnections,
  listIdeDirectory,
  onIdeTerminalClosed,
  onIdeTerminalError,
  onIdeTerminalOutput,
  onIdeTransferComplete,
  onIdeTransferDownloadChunk,
  onIdeTransferProgress,
  prepareIdeConnection,
  rejectIdeHostKey,
  renameIdeRemote,
  resizeIdeTerminal,
  startIdeTerminal,
  trustIdeHostKey,
  uploadIdeFiles,
  writeIdeTerminal,
  closeIdeTerminal,
  type IdeConnection,
  type IdeDirectoryEntry,
  type IdeHostKeyStatus,
  type IdeTransferProgress,
} from './lib/ide'

type ActiveConnection = {
  connection: IdeConnection
  fingerprint: string
  keyAlgorithm: string
  connectedAt: number
}

type ProgressState = {
  connection: IdeConnection
  phase: 'preparing' | 'awaiting_trust' | 'opening' | 'error'
  hostKey: IdeHostKeyStatus | null
  error: string | null
}

type ConnectionMenu = { x: number; y: number; connection: IdeConnection } | null

const EMPTY_FORM = { label: '', group: '个人服务器', host: '', port: '22', username: 'root', password: '' }

function connectionGroup(connection: IdeConnection): string {
  return connection.tags?.[0]?.trim() || '未分组'
}

export default function TauriIdeApp() {
  const [connections, setConnections] = useState<IdeConnection[]>([])
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

  useEffect(() => {
    void listIdeConnections().then(setConnections).catch(() => setConnections([]))
  }, [])

  useEffect(() => {
    const close = () => setMenu(null)
    window.addEventListener('click', close)
    return () => window.removeEventListener('click', close)
  }, [])

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
    setProgress({ connection, phase: 'preparing', hostKey: null, error: null })
    try {
      const hostKey = await prepareIdeConnection(connection.id)
      if (hostKey.status === 'mismatch') {
        setProgress({ connection, phase: 'error', hostKey, error: '服务器主机密钥发生变化，已阻止连接。' })
        return
      }
      if (hostKey.status === 'confirmation_required') {
        setProgress({ connection, phase: 'awaiting_trust', hostKey, error: null })
        return
      }
      openWorkspace(connection, hostKey)
    } catch (error) {
      setProgress({ connection, phase: 'error', hostKey: null, error: ideError(error) })
    }
  }

  function openWorkspace(connection: IdeConnection, hostKey: IdeHostKeyStatus) {
    if (hostKey.status === 'mismatch') return
    setProgress({ connection, phase: 'opening', hostKey, error: null })
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
    setProgress({ ...progress, phase: 'opening', error: null })
    try {
      const trusted = await trustIdeHostKey(progress.connection.id, progress.hostKey.confirmationId)
      openWorkspace(progress.connection, trusted)
    } catch (error) {
      setProgress({ ...progress, phase: 'error', error: ideError(error) })
    }
  }

  async function closeProgress() {
    if (progress?.hostKey?.status === 'confirmation_required') {
      await rejectIdeHostKey(progress.hostKey.confirmationId).catch(() => undefined)
    }
    setProgress(null)
  }

  async function submitConnection(event: FormEvent) {
    event.preventDefault()
    setCreating(true)
    setFormError(null)
    const password = form.password
    try {
      const connection = await createIdeConnection({
        label: form.label.trim() || form.host.trim(),
        host: form.host.trim(),
        port: Number(form.port),
        username: form.username.trim(),
        authentication: { kind: 'password', password },
        tags: [form.group.trim() || '个人服务器'],
        terminal: { columns: 110, rows: 34, command: null },
      })
      setForm((current) => ({ ...current, password: '' }))
      setConnections((current) => [...current, connection])
      setShowCreate(false)
      setForm(EMPTY_FORM)
      await beginConnection(connection)
    } catch (error) {
      setForm((current) => ({ ...current, password: '' }))
      setFormError(ideError(error))
    } finally {
      setCreating(false)
    }
  }

  async function removeConnection(connection: IdeConnection) {
    await disconnectIdeConnection(connection.id).catch(() => undefined)
    setConnections((current) => current.filter((item) => item.id !== connection.id))
    if (active?.connection.id === connection.id) setActive(null)
    setMenu(null)
  }

  function reorder(targetId: string) {
    if (!draggedId || draggedId === targetId) return
    setConnections((current) => {
      const next = [...current]
      const from = next.findIndex((item) => item.id === draggedId)
      const to = next.findIndex((item) => item.id === targetId)
      if (from < 0 || to < 0) return current
      const [moved] = next.splice(from, 1)
      next.splice(to, 0, moved)
      return next
    })
  }

  function moveConnection(connectionId: string, direction: -1 | 1) {
    setConnections((current) => {
      const next = [...current]
      const from = next.findIndex((item) => item.id === connectionId)
      const to = from + direction
      if (from < 0 || to < 0 || to >= next.length) return current
      ;[next[from], next[to]] = [next[to], next[from]]
      return next
    })
  }

  if (active) {
    return <IdeWorkspace active={active} onBack={() => setActive(null)} onDisconnect={() => void removeConnection(active.connection)} />
  }

  return (
    <main className="ide-shell" onContextMenu={() => setMenu(null)}>
      <header className="ide-titlebar">
        <div className="ide-brand"><span className="ide-brand-mark"><TerminalSquare size={18} /></span><strong>NekoSSH</strong><span>Rust SSH IDE</span></div>
        <button className="ide-primary" onClick={() => setShowCreate(true)} type="button"><CirclePlus size={15} />新建连接</button>
      </header>

      <div className="ide-library-layout">
        <aside className="ide-sidebar">
          <div className="ide-side-heading">连接</div>
          <button className={`ide-side-item ${selectedGroup === '全部' ? 'active' : ''}`} onClick={() => setSelectedGroup('全部')} type="button"><Server size={15} />全部<span>{connections.length}</span></button>
          {groups.map(([group, count]) => <button className={`ide-side-item ${selectedGroup === group ? 'active' : ''}`} key={group} onClick={() => setSelectedGroup(group)} type="button"><Folder size={15} />{group}<span>{count}</span></button>)}
          <div className="ide-side-note">连接信息仅保存在当前进程内。退出后自动清空。</div>
        </aside>

        <section className="ide-library">
          <div className="ide-library-header">
            <div><span className="ide-eyebrow">SSH CONNECTIONS</span><h1>SSH 连接</h1><p>双击连接。右键可连接或移除；拖拽可调整顺序。</p></div>
          </div>
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
                onContextMenu={(event) => { event.preventDefault(); event.stopPropagation(); setMenu({ x: event.clientX, y: event.clientY, connection }) }}
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
                  }
                }}
                aria-label={`${connection.label}，${connection.username}@${connection.host}。回车连接，Shift 加上下方向键排序。`}
                role="row"
                tabIndex={0}
              >
                <span className="ide-connection-name"><span className="ide-host-icon"><Server size={16} /></span><span><strong>{connection.label}</strong><small>{connection.username}</small></span></span>
                <code>{connection.host}:{connection.port}</code>
                <span className="ide-auth-pill">Rust ssh2 · 内存</span>
                <span className="ide-muted"><i />未连接</span>
                <span className="ide-row-actions"><button onClick={() => void beginConnection(connection)} type="button"><Plug size={14} />连接</button><button aria-label="更多操作" onClick={(event) => { event.stopPropagation(); const rect = event.currentTarget.getBoundingClientRect(); setMenu({ x: rect.right - 150, y: rect.bottom + 4, connection }) }} type="button"><MoreHorizontal size={16} /></button></span>
              </div>
            ))}
            {!filtered.length ? <div className="ide-empty"><Server size={34} /><strong>还没有 SSH 连接</strong><span>新建后即可直接使用 Rust SSH 终端与 SFTP。</span><button className="ide-primary" onClick={() => setShowCreate(true)} type="button"><CirclePlus size={15} />新建连接</button></div> : null}
          </div>
        </section>
      </div>

      {menu ? <div className="ide-context-menu" style={{ left: menu.x, top: menu.y }} onClick={(event) => event.stopPropagation()}><button onClick={() => void beginConnection(menu.connection)} type="button"><Plug size={14} />连接</button><button className="danger" onClick={() => void removeConnection(menu.connection)} type="button"><Trash2 size={14} />移除</button></div> : null}

      {showCreate ? <div className="ide-overlay" role="presentation"><form className="ide-dialog ide-connect-form" onSubmit={submitConnection}><div className="ide-dialog-header"><div><h2>新建临时 SSH 连接</h2><p>密码只交给 Rust 内存会话，不写入磁盘。</p></div><button aria-label="关闭" onClick={() => { setShowCreate(false); setForm((current) => ({ ...current, password: '' })) }} type="button"><X size={17} /></button></div><div className="ide-form-grid"><label><span>名称</span><input autoFocus placeholder="例如：生产服务器" value={form.label} onChange={(event) => setForm({ ...form, label: event.target.value })} /></label><label><span>分组</span><input placeholder="例如：个人服务器" value={form.group} onChange={(event) => setForm({ ...form, group: event.target.value })} /></label><label><span>主机</span><input required placeholder="IP 或域名" value={form.host} onChange={(event) => setForm({ ...form, host: event.target.value })} /></label><label><span>端口</span><input required inputMode="numeric" value={form.port} onChange={(event) => setForm({ ...form, port: event.target.value })} /></label><label><span>用户</span><input autoComplete="username" required value={form.username} onChange={(event) => setForm({ ...form, username: event.target.value })} /></label><label><span>密码</span><input autoComplete="current-password" required type="password" value={form.password} onChange={(event) => setForm({ ...form, password: event.target.value })} /></label></div>{formError ? <p className="ide-error">{formError}</p> : null}<div className="ide-dialog-actions"><button className="ide-secondary" onClick={() => setShowCreate(false)} type="button">取消</button><button className="ide-primary" disabled={creating} type="submit">{creating ? '正在创建…' : '连接'}</button></div></form></div> : null}
      {progress ? <ConnectionProgress progress={progress} onClose={() => void closeProgress()} onRetry={() => void beginConnection(progress.connection)} onTrust={() => void trustCurrentHost()} /> : null}
    </main>
  )
}

function ConnectionProgress({ progress, onClose, onRetry, onTrust }: { progress: ProgressState; onClose: () => void; onRetry: () => void; onTrust: () => void }) {
  const steps = [
    { label: '初始化配置', active: true, done: true },
    { label: '连接到服务器', active: progress.phase !== 'error', done: progress.phase !== 'preparing' && progress.phase !== 'error' },
    { label: '验证主机密钥', active: progress.phase !== 'error', done: progress.phase === 'opening' },
    { label: '打开终端与文件区', active: progress.phase === 'opening', done: false },
  ]
  return <div className="ide-overlay"><section className="ide-dialog ide-progress-dialog"><div className="ide-dialog-header"><div><h2>SSH 连接</h2><p>{progress.connection.username}@{progress.connection.host}:{progress.connection.port}</p></div><button aria-label="关闭" onClick={onClose} type="button"><X size={17} /></button></div><div className="ide-progress-body"><ol>{steps.map((step) => <li className={`${step.active ? 'active' : ''} ${step.done ? 'done' : ''}`} key={step.label}><i /><span>{step.label}</span></li>)}</ol><div className="ide-connect-log"><code>Client: Rust ssh2 / libssh2</code><code>Transport: in-process TCP</code>{progress.phase === 'preparing' ? <code>正在握手并读取服务器主机密钥…</code> : null}{progress.phase === 'awaiting_trust' && progress.hostKey?.status === 'confirmation_required' ? <div className="ide-fingerprint"><span>首次连接，请核对指纹</span><strong>{progress.hostKey.keyAlgorithm}</strong><code>{progress.hostKey.fingerprint}</code></div> : null}{progress.error ? <p className="ide-error">{progress.error}</p> : null}</div></div><div className="ide-dialog-actions">{progress.phase === 'error' ? <button className="ide-secondary" onClick={onRetry} type="button">重试</button> : null}{progress.phase === 'awaiting_trust' ? <button className="ide-primary" onClick={onTrust} type="button">信任并连接</button> : null}<button className="ide-secondary" onClick={onClose} type="button">关闭</button></div></section></div>
}

function IdeWorkspace({ active, onBack, onDisconnect }: { active: ActiveConnection; onBack: () => void; onDisconnect: () => void }) {
  const [cwd, setCwd] = useState('/')
  const [entries, setEntries] = useState<IdeDirectoryEntry[]>([])
  const [loadingFiles, setLoadingFiles] = useState(true)
  const [fileError, setFileError] = useState<string | null>(null)
  const [terminalState, setTerminalState] = useState<'connecting' | 'connected' | 'closed' | 'error'>('connecting')
  const [transfer, setTransfer] = useState<IdeTransferProgress | null>(null)
  const [fileMenu, setFileMenu] = useState<{ x: number; y: number; entry: IdeDirectoryEntry } | null>(null)
  const [dialog, setDialog] = useState<{ kind: 'mkdir' | 'rename' | 'delete'; entry?: IdeDirectoryEntry } | null>(null)
  const [dialogValue, setDialogValue] = useState('')
  const [operationError, setOperationError] = useState<string | null>(null)
  const downloadParts = useRef(new Map<string, { name: string; parts: Array<{ offset: number; bytes: Uint8Array }> }>())

  async function load(path: string) {
    setLoadingFiles(true)
    setFileError(null)
    try {
      const listing = await listIdeDirectory(active.connection.id, path)
      setCwd(listing.path)
      setEntries(listing.entries)
    } catch (error) {
      setFileError(ideError(error))
    } finally {
      setLoadingFiles(false)
    }
  }

  useEffect(() => { void load('/') }, [active.connection.id])

  useEffect(() => {
    let stopped = false
    const cleanups: Array<() => void> = []
    void (async () => {
      cleanups.push(await onIdeTransferProgress((event) => {
        if (!stopped) setTransfer(event)
      }))
      cleanups.push(await onIdeTransferDownloadChunk((event) => {
        const current = downloadParts.current.get(event.jobId) ?? { name: event.name, parts: [] }
        current.parts.push({ offset: event.offset, bytes: decodeBase64(event.dataBase64) })
        downloadParts.current.set(event.jobId, current)
      }))
      cleanups.push(await onIdeTransferComplete((event) => {
        if (event.direction === 'download' && event.status === 'completed') {
          const download = downloadParts.current.get(event.jobId)
          if (download) {
            download.parts.sort((left, right) => left.offset - right.offset)
            const blob = new Blob(download.parts.map((part) => part.bytes as BlobPart))
            const url = URL.createObjectURL(blob)
            const link = document.createElement('a')
            link.href = url
            link.download = download.name
            link.click()
            URL.revokeObjectURL(url)
          }
        }
        downloadParts.current.delete(event.jobId)
        if (!stopped) {
          setTransfer(null)
          void load(cwd)
        }
      }))
    })()
    return () => { stopped = true; cleanups.forEach((cleanup) => cleanup()) }
  }, [active.connection.id, cwd])

  async function upload(files: File[]) {
    if (!files.length) return
    setOperationError(null)
    try {
      await uploadIdeFiles(active.connection.id, cwd, files)
    } catch (error) {
      setOperationError(ideError(error))
    }
  }

  async function download(entry: IdeDirectoryEntry) {
    if (entry.isDirectory) return
    setOperationError(null)
    try {
      const started = await downloadIdeFiles(active.connection.id, [{ remotePath: childPath(cwd, entry.name), saveAs: entry.name }])
      downloadParts.current.set(started.jobId, { name: entry.name, parts: [] })
    } catch (error) {
      setOperationError(ideError(error))
    }
  }

  async function commitDialog() {
    if (!dialog) return
    setOperationError(null)
    try {
      if (dialog.kind === 'mkdir') await createIdeDirectory(active.connection.id, childPath(cwd, dialogValue.trim()))
      if (dialog.kind === 'rename' && dialog.entry) await renameIdeRemote(active.connection.id, childPath(cwd, dialog.entry.name), childPath(cwd, dialogValue.trim()))
      if (dialog.kind === 'delete' && dialog.entry) await deleteIdeRemote(active.connection.id, childPath(cwd, dialog.entry.name))
      setDialog(null)
      setDialogValue('')
      await load(cwd)
    } catch (error) {
      setOperationError(ideError(error))
    }
  }

  const elapsed = Math.max(0, Math.floor((Date.now() - active.connectedAt) / 1000))
  return <main className="ide-shell ide-workspace-shell" onClick={() => setFileMenu(null)}>
    <header className="ide-titlebar ide-workspace-titlebar"><button className="ide-icon-button" onClick={onBack} type="button"><ChevronLeft size={16} /></button><div className="ide-session-tab"><i /><Server size={14} /><strong>{active.connection.label}</strong><span>{active.connection.username}@{active.connection.host}</span></div><div className="ide-title-actions"><span>Rust ssh2 · 主机已验证</span><button className="ide-danger-button" onClick={onDisconnect} type="button">断开</button></div></header>
    <section className="ide-workspace-grid">
      <aside className="ide-file-pane" onDragOver={(event) => event.preventDefault()} onDrop={(event) => { event.preventDefault(); void upload(Array.from(event.dataTransfer.files)) }}>
        <div className="ide-pane-tabs"><span className="active"><Folder size={14} />远端文件</span></div>
        <div className="ide-file-toolbar"><button disabled={cwd === '/'} onClick={() => void load(parentPath(cwd))} type="button"><ChevronLeft size={15} /></button><code title={cwd}>{cwd}</code><button onClick={() => void load(cwd)} type="button"><RefreshCw size={14} /></button><button onClick={() => { setDialog({ kind: 'mkdir' }); setDialogValue('新建文件夹') }} type="button"><FolderPlus size={14} /></button><label><Upload size={14} /><input multiple onChange={(event) => { void upload(Array.from(event.target.files ?? [])); event.target.value = '' }} type="file" /></label></div>
        <div className="ide-file-list">{loadingFiles ? <div className="ide-pane-empty">正在通过 SFTP 读取…</div> : fileError ? <div className="ide-pane-empty error">{fileError}</div> : entries.map((entry) => <button className="ide-file-row" key={entry.name} onContextMenu={(event) => { event.preventDefault(); event.stopPropagation(); setFileMenu({ x: event.clientX, y: event.clientY, entry }) }} onDoubleClick={() => entry.isDirectory ? void load(childPath(cwd, entry.name)) : void download(entry)} type="button">{entry.isDirectory ? <Folder className="folder" size={16} /> : <File size={16} />}<span>{entry.name}</span><small>{entry.isDirectory ? '文件夹' : formatBytes(entry.size)}</small></button>)}</div>
        <div className="ide-file-status"><span>{entries.length} 项</span><span>拖入文件即可上传</span></div>
      </aside>
      <section className="ide-terminal-pane"><div className="ide-pane-tabs"><span className="active"><TerminalSquare size={14} />终端</span><span className={`ide-terminal-state ${terminalState}`}>{terminalState === 'connected' ? '已连接' : terminalState === 'connecting' ? '连接中' : terminalState === 'error' ? '错误' : '已关闭'}</span></div><XtermPane connectionId={active.connection.id} onState={setTerminalState} /></section>
      <aside className="ide-monitor-pane"><div className="ide-pane-tabs"><span className="active"><Activity size={14} />会话</span></div><div className="ide-monitor-card"><div className="ide-monitor-title"><Server size={16} /><strong>连接</strong></div><dl><div><dt>主机</dt><dd>{active.connection.host}:{active.connection.port}</dd></div><div><dt>用户</dt><dd>{active.connection.username}</dd></div><div><dt>状态</dt><dd className="ok">● 已验证</dd></div></dl></div><div className="ide-monitor-card"><div className="ide-monitor-title"><HardDrive size={16} /><strong>工作区</strong></div><dl><div><dt>目录</dt><dd title={cwd}>{cwd}</dd></div><div><dt>终端</dt><dd>{terminalState}</dd></div><div><dt>时长</dt><dd>{formatDuration(elapsed)}</dd></div></dl></div><div className="ide-monitor-card"><div className="ide-monitor-title"><Activity size={16} /><strong>传输</strong></div>{transfer ? <div className="ide-transfer"><span>{transfer.direction === 'upload' ? '上传' : '下载'} · {transfer.name}</span><progress max={transfer.jobTotalBytes ?? 1} value={transfer.jobBytesTransferred} /><small>{formatBytes(transfer.jobBytesTransferred)} / {formatBytes(transfer.jobTotalBytes)}</small></div> : <p className="ide-monitor-empty">当前没有传输任务</p>}</div><div className="ide-native-note">SSH 与 SFTP 均由 Rust `ssh2` 进程内执行，不调用 Windows SSH、PuTTY 或 plink。</div></aside>
    </section>
    {fileMenu ? <FileContextMenu menu={fileMenu} cwd={cwd} onDownload={() => void download(fileMenu.entry)} onRename={() => { setDialog({ kind: 'rename', entry: fileMenu.entry }); setDialogValue(fileMenu.entry.name); setFileMenu(null) }} onDelete={() => { setDialog({ kind: 'delete', entry: fileMenu.entry }); setFileMenu(null) }} /> : null}
    {dialog ? <div className="ide-overlay"><section className="ide-dialog ide-small-dialog"><div className="ide-dialog-header"><div><h2>{dialog.kind === 'mkdir' ? '新建远端文件夹' : dialog.kind === 'rename' ? '重命名' : '确认删除'}</h2><p>{dialog.kind === 'delete' ? `将删除 ${dialog.entry?.name}。文件夹必须为空。` : cwd}</p></div></div>{dialog.kind !== 'delete' ? <label className="ide-dialog-input"><span>名称</span><input autoFocus value={dialogValue} onChange={(event) => setDialogValue(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') void commitDialog() }} /></label> : null}<div className="ide-dialog-actions"><button className="ide-secondary" onClick={() => setDialog(null)} type="button">取消</button><button className={dialog.kind === 'delete' ? 'ide-delete' : 'ide-primary'} onClick={() => void commitDialog()} type="button">{dialog.kind === 'delete' ? '删除' : '确定'}</button></div></section></div> : null}
    {operationError ? <div className="ide-toast error" onClick={() => setOperationError(null)}>{operationError}</div> : null}
  </main>
}

function XtermPane({ connectionId, onState }: { connectionId: string; onState: (state: 'connecting' | 'connected' | 'closed' | 'error') => void }) {
  const host = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!host.current) return
    let disposed = false
    let sessionId: string | null = null
    const cleanups: Array<() => void> = []
    const terminal = new XTerm({
      cursorBlink: true,
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
    const resizeObserver = new ResizeObserver(() => {
      fit.fit()
      if (sessionId) void resizeIdeTerminal(sessionId, terminal.cols, terminal.rows)
    })
    resizeObserver.observe(host.current)
    const dataDisposable = terminal.onData((data) => { if (sessionId) void writeIdeTerminal(sessionId, data) })
    onState('connecting')
    void (async () => {
      cleanups.push(await onIdeTerminalOutput((event) => {
        if (event.sessionId === sessionId) terminal.write(event.data)
      }))
      cleanups.push(await onIdeTerminalClosed((event) => {
        if (event.sessionId === sessionId) {
          onState('closed')
          terminal.write(`\r\n\x1b[90m[会话已关闭${event.exitStatus === null ? '' : `，退出码 ${event.exitStatus}`}]\x1b[0m\r\n`)
        }
      }))
      cleanups.push(await onIdeTerminalError((event) => {
        if (event.sessionId === sessionId) {
          onState('error')
          terminal.write(`\r\n\x1b[31m[终端错误：${event.code}]\x1b[0m\r\n`)
        }
      }))
      try {
        const started = await startIdeTerminal(connectionId, terminal.cols, terminal.rows)
        if (disposed) {
          await closeIdeTerminal(started.sessionId).catch(() => undefined)
          return
        }
        sessionId = started.sessionId
        onState('connected')
        terminal.focus()
      } catch (error) {
        onState('error')
        terminal.write(`\x1b[31m${ideError(error)}\x1b[0m\r\n`)
      }
    })()
    return () => {
      disposed = true
      resizeObserver.disconnect()
      dataDisposable.dispose()
      cleanups.forEach((cleanup) => cleanup())
      if (sessionId) void closeIdeTerminal(sessionId)
      terminal.dispose()
    }
  }, [connectionId, onState])
  return <div className="ide-xterm" ref={host} />
}

function FileContextMenu({ menu, onDownload, onRename, onDelete }: { menu: { x: number; y: number; entry: IdeDirectoryEntry }; cwd: string; onDownload: () => void; onRename: () => void; onDelete: () => void }) {
  return <div className="ide-context-menu" style={{ left: menu.x, top: menu.y }} onClick={(event) => event.stopPropagation()}>{!menu.entry.isDirectory ? <button onClick={onDownload} type="button"><Download size={14} />下载</button> : null}<button onClick={onRename} type="button"><Pencil size={14} />重命名</button><button className="danger" onClick={onDelete} type="button"><Trash2 size={14} />删除</button></div>
}

function childPath(cwd: string, name: string) {
  return cwd === '/' ? `/${name}` : `${cwd.replace(/\/+$/, '')}/${name}`
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

function decodeBase64(value: string) {
  const binary = atob(value)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
  return bytes
}
