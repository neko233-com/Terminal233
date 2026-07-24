import {
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  CircleHelp,
  Command,
  LockKeyhole,
  PanelRightOpen,
  Search,
  Settings2,
  ShieldCheck,
} from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { BrowserWorkspace } from './components/BrowserWorkspace'
import { ConnectionLibrary } from './components/ConnectionLibrary'
import { DesktopWorkspace } from './components/DesktopWorkspace'
import { Inspector } from './components/Inspector'
import { ProfileDialog } from './components/ProfileDialog'
import { Sidebar } from './components/Sidebar'
import { SyncWorkspace } from './components/SyncWorkspace'
import { TerminalWorkspace } from './components/TerminalWorkspace'
import { PasswordDialog, RestoreDialog, type SyncProvider, SyncDialog, type VaultDialogMode } from './components/VaultDialogs'
import type { AppMode, Host } from './data'
import { initialHosts } from './data'
import {
  bootstrapVault,
  changeVaultPassword,
  configureSync,
  createProfile,
  errorMessage,
  isDesktopRuntime,
  launchExternalSsh,
  listProfiles,
  openExternalBrowser,
  readSyncConfiguration,
  readVaultStatus,
  restoreGitEncryptedBackup,
  restoreWebDavEncryptedBackup,
  unlockVault,
  uploadGitEncryptedBundle,
  uploadWebDavEncryptedBundle,
} from './lib/desktop'
import type { ProfileDraft, ProfileSummary, SyncConfigurationSummary, SyncDraft, VaultStatus } from './lib/desktop'

const desktopAvailable = isDesktopRuntime()

function hostFromProfile(profile: ProfileSummary): Host {
  return {
    id: profile.id,
    profileId: profile.id,
    name: profile.label,
    user: profile.username,
    address: profile.host,
    port: profile.port,
    group: profile.tags[0] || 'Vault 配置',
    status: 'idle',
    latency: '—',
    os: '连接后读取',
    fingerprint: '首次连接时验证',
    source: 'vault',
  }
}

function syncLabel(configuration: SyncConfigurationSummary | null): string {
  if (!configuration) return '未配置云同步'
  return configuration.kind === 'web_dav' ? 'WebDAV 已配置（密文）' : 'Git 已配置（密文）'
}

function dialogModeFor(status: VaultStatus | null): VaultDialogMode {
  if (!desktopAvailable) return 'preview'
  if (!status?.exists) return 'bootstrap'
  return status.unlocked ? 'change' : 'unlock'
}

function formatByteCount(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

function App() {
  const [activeMode, setActiveMode] = useState<AppMode>('library')
  const [hosts, setHosts] = useState(initialHosts)
  const [selectedHostId, setSelectedHostId] = useState('home')
  const [inspectorOpen, setInspectorOpen] = useState(false)
  const [libraryQuery, setLibraryQuery] = useState('')
  const [passwordDialogOpen, setPasswordDialogOpen] = useState(false)
  const [vaultDialogMode, setVaultDialogMode] = useState<VaultDialogMode>(() => dialogModeFor(null))
  const [profileDialogOpen, setProfileDialogOpen] = useState(false)
  const [syncDialogOpen, setSyncDialogOpen] = useState(false)
  const [restoreDialogOpen, setRestoreDialogOpen] = useState(false)
  const [restoreProvider, setRestoreProvider] = useState<'web_dav' | 'git' | null>(null)
  const [syncProvider, setSyncProvider] = useState<SyncProvider>('git')
  const [syncStatus, setSyncStatus] = useState('未配置云同步')
  const [syncConfiguration, setSyncConfiguration] = useState<SyncConfigurationSummary | null>(null)
  const [lastSync, setLastSync] = useState('尚未传输')
  const [vaultStatus, setVaultStatus] = useState<VaultStatus | null>(null)
  const [isVaultChecking, setIsVaultChecking] = useState(desktopAvailable)
  const [vaultIssue, setVaultIssue] = useState<string | null>(null)
  const [connectionMessage, setConnectionMessage] = useState<string | null>(null)
  const [isLaunching, setIsLaunching] = useState(false)

  const selectedHost = useMemo(
    () => hosts.find((host) => host.id === selectedHostId) ?? hosts[0],
    [hosts, selectedHostId],
  )
  const vaultUnlocked = Boolean(vaultStatus?.unlocked)

  async function refreshDesktopState(preferredProfileId?: string) {
    if (!desktopAvailable) return

    setIsVaultChecking(true)
    setVaultIssue(null)
    try {
      const status = await readVaultStatus()
      setVaultStatus(status)
      if (!status?.unlocked) {
        setHosts(initialHosts)
        setSelectedHostId('home')
        setSyncStatus('未配置云同步')
        setSyncConfiguration(null)
        setLastSync('尚未传输')
        return
      }

      const [profiles, configuration] = await Promise.all([listProfiles(), readSyncConfiguration()])
      if (profiles.length) {
        const vaultHosts = profiles.map(hostFromProfile)
        setHosts(vaultHosts)
        setSelectedHostId((current) => preferredProfileId && vaultHosts.some((host) => host.id === preferredProfileId)
          ? preferredProfileId
          : vaultHosts.some((host) => host.id === current)
            ? current
            : vaultHosts[0].id)
      } else {
        setHosts(initialHosts)
        setSelectedHostId('home')
      }
      setSyncStatus(syncLabel(configuration))
      setSyncConfiguration(configuration)
      setLastSync(configuration ? '已配置，等待传输' : '尚未传输')
    } catch (error) {
      setVaultIssue(errorMessage(error, '无法读取本地 Vault 状态。'))
    } finally {
      setIsVaultChecking(false)
    }
  }

  useEffect(() => {
    void refreshDesktopState()
  }, [])

  function openSyncDialog(provider: SyncProvider = 'git') {
    setSyncProvider(provider)
    setSyncDialogOpen(true)
  }

  function openRestoreDialog() {
    if (!desktopAvailable) {
      setConnectionMessage('浏览器预览不会下载或替换本地 Vault；请在 NekoSSH 桌面端恢复。')
      return
    }
    if (!vaultUnlocked) {
      setConnectionMessage('先解锁当前 Vault，才能读取已加密保存的 WebDAV 配置。')
      openVaultDialog()
      return
    }
    if (!syncConfiguration) {
      setConnectionMessage('请先配置 WebDAV 或 Git 加密备份，才能执行受密码验证保护的恢复。')
      return
    }
    setRestoreProvider(syncConfiguration.kind)
    setRestoreDialogOpen(true)
  }

  function openVaultDialog() {
    if (desktopAvailable && isVaultChecking) {
      setConnectionMessage('正在读取 Vault 状态，请稍后再试。')
      return
    }
    setVaultDialogMode(dialogModeFor(vaultStatus))
    setPasswordDialogOpen(true)
  }

  function addHost() {
    if (!desktopAvailable) {
      setConnectionMessage('网页预览不会保存 SSH 凭据。请在 NekoSSH 桌面端创建并加密保存连接配置。')
      return
    }
    if (!vaultUnlocked) {
      setConnectionMessage('先创建或解锁 Vault，才能安全保存 SSH 连接。')
      openVaultDialog()
      return
    }
    setProfileDialogOpen(true)
  }

  function changeWorkspace(mode: AppMode) {
    setActiveMode(mode)
    setInspectorOpen(mode === 'connect')
  }

  function openSession(hostId = selectedHostId) {
    setSelectedHostId(hostId)
    setActiveMode('connect')
    setInspectorOpen(true)
  }

  function updateLibraryQuery(query: string) {
    setLibraryQuery(query)
    if (activeMode !== 'library') {
      setActiveMode('library')
      setInspectorOpen(false)
    }
  }

  function toggleInspector() {
    if (activeMode !== 'connect') {
      openSession()
      return
    }
    setInspectorOpen((current) => !current)
  }

  async function submitVault(mode: VaultDialogMode, credentials: { currentPassword: string; newPassword: string }) {
    if (mode === 'bootstrap') {
      await bootstrapVault(credentials.newPassword)
    } else if (mode === 'unlock') {
      await unlockVault(credentials.currentPassword)
    } else if (mode === 'change') {
      await changeVaultPassword(credentials.currentPassword, credentials.newPassword)
    }
    await refreshDesktopState()
  }

  async function saveSyncConfiguration(draft: SyncDraft): Promise<string> {
    const configuration = await configureSync(draft)
    setSyncConfiguration(configuration)
    if (draft.provider === 'webdav') {
      try {
        const receipt = await uploadWebDavEncryptedBundle()
        setSyncStatus('WebDAV 已备份（端到端加密）')
        setLastSync(`刚刚 · ${formatByteCount(receipt.bytes)}`)
        return `加密备份已上传（${formatByteCount(receipt.bytes)}）；远端只会收到不可读的 Vault 密文。`
      } catch (error) {
        setSyncStatus('WebDAV 已配置（上传失败）')
        setLastSync('上传失败')
        throw error
      }
    }

    try {
      const receipt = await uploadGitEncryptedBundle()
      setSyncStatus('Git 已备份（端到端加密）')
      setLastSync(`刚刚 · ${formatByteCount(receipt.bytes)}`)
      return `加密备份已推送到 Git 私有仓库（${formatByteCount(receipt.bytes)}）；提交中只包含不可读的 Vault 密文。`
    } catch (error) {
      setSyncStatus('Git 已配置（上传失败）')
      setLastSync('上传失败')
      throw error
    }
  }

  async function restoreFromCloud(password: string) {
    const provider = restoreProvider === 'git' ? 'Git' : 'WebDAV'
    const receipt = restoreProvider === 'git'
      ? await restoreGitEncryptedBackup(password)
      : await restoreWebDavEncryptedBackup(password)
    await refreshDesktopState()
    setSyncStatus(`已从 ${provider} 恢复（Vault 已锁定）`)
    setLastSync(`刚刚恢复 · ${formatByteCount(receipt.bytes)}`)
    setConnectionMessage('已验证备份主密码并恢复本地 Vault。当前 Vault 保持锁定，请使用恢复后的主密码解锁。')
    return receipt
  }

  async function saveProfile(draft: ProfileDraft) {
    const profile = await createProfile(draft)
    await refreshDesktopState(profile.id)
    setConnectionMessage(`“${profile.label}” 已加密保存到本地 Vault。可通过“启动系统 SSH”开始传统命令行会话。`)
  }

  async function connectSelectedHost() {
    if (!desktopAvailable) {
      setConnectionMessage('网页预览不会创建 SSH 连接；请在 NekoSSH 桌面端使用已加密的 Vault 配置。')
      return
    }
    if (!vaultUnlocked) {
      setConnectionMessage('先解锁 Vault，才能读取并启动连接配置。')
      openVaultDialog()
      return
    }
    if (!selectedHost?.profileId) {
      setConnectionMessage('当前是预览主机。创建真实 SSH 配置后，桌面端会使用验证过的 OpenSSH 参数启动会话。')
      return
    }

    setIsLaunching(true)
    setConnectionMessage(null)
    try {
      const processId = await launchExternalSsh(selectedHost.profileId)
      setConnectionMessage(`系统 OpenSSH 已启动（进程 ${processId}）。凭据不会写入命令行参数。`)
    } catch (error) {
      setConnectionMessage(errorMessage(error, '无法启动系统 OpenSSH。'))
    } finally {
      setIsLaunching(false)
    }
  }

  const vaultLabel = !desktopAvailable
    ? 'Vault Web 预览'
    : isVaultChecking
      ? 'Vault 正在检查'
      : vaultIssue
        ? 'Vault 需要检查'
        : !vaultStatus?.exists
          ? 'Vault 未创建'
          : vaultUnlocked
            ? 'Vault 已解锁'
            : 'Vault 已锁定'
  const terminalConnectionStatus = !desktopAvailable
    ? '浏览器预览 · 未发起 SSH 网络连接'
    : selectedHost?.profileId && vaultUnlocked
      ? `已保存 SSH 目标 · 未连接 · ${selectedHost.address}:${selectedHost.port}`
      : '等待已解锁的 Vault 配置'

  if (!selectedHost) return null

  return (
    <div className="app-shell">
      <header className="app-topbar">
        <div className="window-controls" aria-label="窗口控制"><i /><i /><i /></div>
        <div className="topbar-navigation">
          <button aria-label="后退" className="icon-button subtle" type="button"><ChevronLeft size={20} /></button>
          <button aria-label="前进" className="icon-button subtle" type="button"><ChevronRight size={20} /></button>
          <button aria-label="切换布局" className="icon-button subtle" type="button"><PanelRightOpen size={20} /></button>
        </div>
        <label className="global-search">
          <Search aria-hidden="true" size={19} />
          <input
            aria-label="搜索主机或命令"
            onChange={(event) => updateLibraryQuery(event.target.value)}
            onFocus={() => changeWorkspace('library')}
            placeholder="搜索连接、地址或用户"
            value={libraryQuery}
          />
          <kbd><Command size={11} />K</kbd>
        </label>
        <div className="topbar-actions">
          <button className="sync-menu-button" onClick={() => openSyncDialog('git')} type="button">
            <ShieldCheck aria-hidden="true" size={19} />
            <span>{syncStatus}</span>
            <ChevronDown aria-hidden="true" size={15} />
          </button>
          <button
            aria-label={vaultLabel}
            className="vault-menu-button"
            disabled={desktopAvailable && isVaultChecking}
            onClick={openVaultDialog}
            title={vaultIssue ?? undefined}
            type="button"
          >
            <LockKeyhole aria-hidden="true" size={18} />
            <span>{vaultLabel}</span>
            <ChevronDown aria-hidden="true" size={15} />
          </button>
          <button aria-label="帮助" className="icon-button subtle top-help" type="button"><CircleHelp size={20} /></button>
          <button aria-label="打开连接详情" className="icon-button subtle top-settings" onClick={toggleInspector} type="button"><Settings2 size={21} /></button>
        </div>
      </header>

      <div className={`app-content ${inspectorOpen && activeMode === 'connect' ? '' : 'inspector-is-closed'}`}>
        <Sidebar
          activeMode={activeMode}
          hosts={hosts}
          onAddHost={addHost}
          onModeChange={changeWorkspace}
          onSelectHost={setSelectedHostId}
          selectedHostId={selectedHostId}
        />
        <main className="main-workspace">
          {activeMode === 'library' ? (
            <ConnectionLibrary
              hosts={hosts}
              notice={connectionMessage}
              onAddHost={addHost}
              onOpenSession={openSession}
              onOpenSync={() => changeWorkspace('sync')}
              onQueryChange={updateLibraryQuery}
              onSelectHost={setSelectedHostId}
              query={libraryQuery}
              selectedHostId={selectedHostId}
            />
          ) : null}
          {activeMode === 'connect' ? (
            <TerminalWorkspace
              connectionStatus={terminalConnectionStatus}
              host={selectedHost}
              onBackToLibrary={() => changeWorkspace('library')}
              onOpenBrowser={() => changeWorkspace('browser')}
              onOpenDesktop={() => changeWorkspace('desktop')}
            />
          ) : null}
          {activeMode === 'desktop' ? (
            <DesktopWorkspace host={selectedHost} onOpenBrowser={() => changeWorkspace('browser')} onOpenTerminal={() => changeWorkspace('connect')} />
          ) : null}
          {activeMode === 'browser' ? <BrowserWorkspace desktopAvailable={desktopAvailable} host={selectedHost} onOpenExternal={openExternalBrowser} /> : null}
          {activeMode === 'sync' ? <SyncWorkspace canRestore={desktopAvailable && vaultUnlocked && Boolean(syncConfiguration)} lastSync={lastSync} onConfigure={openSyncDialog} onRestore={openRestoreDialog} restoreProvider={syncConfiguration?.kind === 'git' ? 'Git' : 'WebDAV'} status={syncStatus} /> : null}
        </main>
        {inspectorOpen && activeMode === 'connect' ? (
          <Inspector
            connectionMessage={connectionMessage}
            desktopAvailable={desktopAvailable}
            host={selectedHost}
            isLaunching={isLaunching}
            onChangePassword={openVaultDialog}
            onClose={() => setInspectorOpen(false)}
            onConnect={() => void connectSelectedHost()}
            vaultUnlocked={vaultUnlocked}
          />
        ) : null}
      </div>

      <footer className="app-footer">
        <span className="footer-security"><ShieldCheck aria-hidden="true" size={20} />端到端加密 · 本地优先</span>
        <span>{activeMode === 'library' ? `${hosts.length} 个 SSH 连接` : `${selectedHost.name} · SSH 预览`}　│　<PanelRightOpen size={18} /></span>
      </footer>

      {passwordDialogOpen ? (
        <PasswordDialog
          mode={vaultDialogMode}
          onClose={() => setPasswordDialogOpen(false)}
          onSubmit={(credentials) => submitVault(vaultDialogMode, credentials)}
        />
      ) : null}
      {syncDialogOpen ? (
        <SyncDialog
          defaultProvider={syncProvider}
          desktopAvailable={desktopAvailable}
          onClose={() => setSyncDialogOpen(false)}
          onSaved={saveSyncConfiguration}
        />
      ) : null}
      {restoreDialogOpen && restoreProvider ? <RestoreDialog onClose={() => setRestoreDialogOpen(false)} onRestore={restoreFromCloud} providerLabel={restoreProvider === 'git' ? 'Git' : 'WebDAV'} /> : null}
      {profileDialogOpen ? <ProfileDialog onClose={() => setProfileDialogOpen(false)} onSaved={saveProfile} /> : null}
    </div>
  )
}

export default App
