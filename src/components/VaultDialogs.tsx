import { CheckCircle2, Cloud, FileKey2, GitBranch, KeyRound, LockKeyhole, MonitorSmartphone, RotateCcw, ShieldCheck } from 'lucide-react'
import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import type { SyncDraft, SyncProvider as DesktopSyncProvider, VaultImportReceipt } from '../lib/desktop'
import { errorMessage } from '../lib/desktop'
import { Modal } from './Modal'

export type SyncProvider = DesktopSyncProvider
export type VaultDialogMode = 'bootstrap' | 'unlock' | 'change' | 'preview'

interface PasswordDialogProps {
  mode: VaultDialogMode
  onClose: () => void
  onSubmit: (credentials: { currentPassword: string; newPassword: string }) => Promise<void>
}

const vaultCopy: Record<Exclude<VaultDialogMode, 'preview'>, { action: string; description: string; success: string; title: string }> = {
  bootstrap: {
    action: '创建加密 Vault',
    description: '主密码只在此设备用于派生解密密钥，NekoSSH 不会上传或回传它。',
    success: '本地 Vault 已创建并解锁。',
    title: '创建本地 Vault',
  },
  unlock: {
    action: '解锁 Vault',
    description: '主密码只在此设备上处理，用于解锁已加密的连接配置。',
    success: 'Vault 已解锁。',
    title: '解锁 Vault',
  },
  change: {
    action: '更新主密码',
    description: '只在此设备上重新封装 Vault；你的 SSH 密钥和同步令牌不会暴露给前端。',
    success: '主密码已更新，本地 Vault 已重新封装。',
    title: '修改主密码',
  },
}

export function PasswordDialog({ mode, onClose, onSubmit }: PasswordDialogProps) {
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [message, setMessage] = useState<{ kind: 'error' | 'success'; text: string } | null>(null)

  if (mode === 'preview') {
    return (
      <Modal
        description="浏览器预览不会读取、保存或传输主密码。请在 NekoSSH 桌面端创建或解锁本地 Vault。"
        icon={<MonitorSmartphone size={19} />}
        onClose={onClose}
        title="Vault 仅在桌面端可用"
      >
        <div className="desktop-required-copy">
          <LockKeyhole aria-hidden="true" size={24} />
          <p>这样可以让主密码、SSH 凭据与云同步令牌始终留在受保护的本机保险库中。</p>
          <button autoFocus className="primary-action" onClick={onClose} type="button">知道了</button>
        </div>
      </Modal>
    )
  }

  const copy = vaultCopy[mode]
  const requiresCurrentPassword = mode === 'unlock' || mode === 'change'
  const requiresNewPassword = mode === 'bootstrap' || mode === 'change'

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (requiresCurrentPassword && !currentPassword) {
      setMessage({ kind: 'error', text: '请输入当前主密码。' })
      return
    }
    if (requiresNewPassword && newPassword.length < 12) {
      setMessage({ kind: 'error', text: '新主密码至少需要 12 个字符。' })
      return
    }
    if (requiresNewPassword && newPassword !== confirmation) {
      setMessage({ kind: 'error', text: '两次输入的新主密码不一致。' })
      return
    }

    setIsSubmitting(true)
    setMessage(null)
    try {
      await onSubmit({ currentPassword, newPassword })
      setCurrentPassword('')
      setNewPassword('')
      setConfirmation('')
      setMessage({ kind: 'success', text: copy.success })
    } catch (error) {
      setMessage({ kind: 'error', text: errorMessage(error, 'Vault 操作未完成，请检查主密码后重试。') })
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <Modal description={copy.description} icon={<KeyRound size={19} />} onClose={onClose} title={copy.title}>
      <form className="vault-form" onSubmit={submit}>
        {requiresCurrentPassword ? (
          <label>
            <span>{mode === 'unlock' ? '主密码' : '当前主密码'}</span>
            <input
              aria-label={mode === 'unlock' ? '主密码' : '当前主密码'}
              autoComplete="current-password"
              autoFocus
              onChange={(event) => setCurrentPassword(event.target.value)}
              required
              type="password"
              value={currentPassword}
            />
          </label>
        ) : null}
        {requiresNewPassword ? (
          <label>
            <span>新主密码</span>
            <input
              aria-label="新主密码"
              autoComplete="new-password"
              autoFocus={!requiresCurrentPassword}
              minLength={12}
              onChange={(event) => setNewPassword(event.target.value)}
              required
              type="password"
              value={newPassword}
            />
            <small>至少 12 个字符；建议交由你信任的密码管理器生成并保存。</small>
          </label>
        ) : null}
        {requiresNewPassword ? (
          <label>
            <span>确认新主密码</span>
            <input
              aria-label="确认新主密码"
              autoComplete="new-password"
              onChange={(event) => setConfirmation(event.target.value)}
              required
              type="password"
              value={confirmation}
            />
          </label>
        ) : null}
        {message ? <p className={`form-message ${message.kind}`} role="status">{message.text}</p> : null}
        <div className="modal-actions">
          <button className="secondary-action" disabled={isSubmitting} onClick={onClose} type="button">取消</button>
          <button className="primary-action" disabled={isSubmitting} type="submit"><LockKeyhole size={17} />{isSubmitting ? '正在处理…' : copy.action}</button>
        </div>
      </form>
    </Modal>
  )
}

interface RestoreDialogProps {
  onClose: () => void
  onRestore: (password: string) => Promise<VaultImportReceipt>
  providerLabel: 'WebDAV' | 'Git'
}

/**
 * A deliberately explicit, password-verified restore action. The UI never receives the
 * downloaded bundle: Rust downloads, authenticates, backs up and replaces it as one operation.
 */
export function RestoreDialog({ onClose, onRestore, providerLabel }: RestoreDialogProps) {
  const [password, setPassword] = useState('')
  const [confirmed, setConfirmed] = useState(false)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [message, setMessage] = useState<{ kind: 'error' | 'success'; text: string } | null>(null)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!password) {
      setMessage({ kind: 'error', text: '请输入备份 Vault 的主密码，以验证加密包。' })
      return
    }
    if (!confirmed) {
      setMessage({ kind: 'error', text: '请确认你理解恢复会替换当前本地 Vault。' })
      return
    }

    setIsSubmitting(true)
    setMessage(null)
    try {
      const receipt = await onRestore(password)
      setPassword('')
      setMessage({
        kind: 'success',
        text: receipt.rollbackAvailable
          ? '备份密码已验证；本地 Vault 已恢复并锁定，已保留可回滚的加密副本。'
          : '备份密码已验证；本地 Vault 已恢复并锁定。请使用恢复后的主密码解锁。',
      })
    } catch (error) {
      setMessage({ kind: 'error', text: errorMessage(error, '恢复未完成；当前本地 Vault 未被替换。') })
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <Modal
      description="NekoSSH 会在 Rust 进程中下载并验证密文。只有备份主密码通过 AEAD 验证后，才会替换本地 Vault。"
      icon={<RotateCcw size={19} />}
      onClose={onClose}
      title={`从 ${providerLabel} 加密备份恢复`}
    >
      <form className="vault-form" onSubmit={submit}>
        <div className="encryption-banner"><FileKey2 size={21} /><span><strong>不会自动覆盖</strong><small>恢复会先留存当前 Vault 的本地加密回滚副本，并在完成后保持锁定。</small></span></div>
        <label>
          <span>备份 Vault 的主密码</span>
          <input
            aria-label="备份 Vault 的主密码"
            autoComplete="current-password"
            autoFocus
            onChange={(event) => setPassword(event.target.value)}
            required
            type="password"
            value={password}
          />
        </label>
        <label className="restore-confirmation">
          <input aria-label="确认替换本地 Vault" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} type="checkbox" />
          <span>我明白：恢复会替换当前本地 Vault；若恢复成功，需要用备份的主密码重新解锁。</span>
        </label>
        {message ? <p className={`form-message ${message.kind}`} role="status">{message.kind === 'success' ? <CheckCircle2 size={17} /> : null}{message.text}</p> : null}
        <div className="modal-actions">
          <button className="secondary-action" disabled={isSubmitting} onClick={onClose} type="button">{message?.kind === 'success' ? '完成' : '取消'}</button>
          {message?.kind !== 'success' ? <button className="primary-action" disabled={isSubmitting} type="submit"><RotateCcw size={17} />{isSubmitting ? '正在验证并恢复…' : '验证并恢复'}</button> : null}
        </div>
      </form>
    </Modal>
  )
}

interface SyncDialogProps {
  defaultProvider: SyncProvider
  desktopAvailable: boolean
  onClose: () => void
  onSaved: (draft: SyncDraft) => Promise<string>
}

const defaultDestination = (provider: SyncProvider) => provider === 'git'
  ? 'https://github.com/neko/nekossh-vault.git'
  : 'https://dav.example.com/nekossh'

export function SyncDialog({ defaultProvider, desktopAvailable, onClose, onSaved }: SyncDialogProps) {
  const [provider, setProvider] = useState<SyncProvider>(defaultProvider)
  const [destination, setDestination] = useState(defaultDestination(defaultProvider))
  const [remotePath, setRemotePath] = useState('nekossh/vault.bundle')
  const [username, setUsername] = useState('')
  const [secret, setSecret] = useState('')
  const [branch, setBranch] = useState('main')
  const [accessToken, setAccessToken] = useState('')
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [message, setMessage] = useState<{ kind: 'error' | 'success'; text: string } | null>(null)

  useEffect(() => {
    setProvider(defaultProvider)
    setDestination(defaultDestination(defaultProvider))
    setRemotePath('nekossh/vault.bundle')
    setUsername('')
    setSecret('')
    setBranch('main')
    setAccessToken('')
    setMessage(null)
  }, [defaultProvider])

  if (!desktopAvailable) {
    return (
      <Modal
        description="浏览器预览不会收集或保存 WebDAV 密码、Git 令牌与 SSH 凭据。"
        icon={<MonitorSmartphone size={19} />}
        onClose={onClose}
        title="同步配置需要桌面端"
      >
        <div className="desktop-required-copy">
          <ShieldCheck aria-hidden="true" size={24} />
          <p>请在桌面端将配置放入加密 Vault；远端仅能获得 neko233-v1 格式的密文副本。</p>
          <button autoFocus className="primary-action" onClick={onClose} type="button">知道了</button>
        </div>
      </Modal>
    )
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (provider === 'webdav' && !secret) {
      setMessage({ kind: 'error', text: '请输入 WebDAV 密码或应用专用令牌。' })
      return
    }
    if (provider === 'git' && !accessToken) {
      setMessage({ kind: 'error', text: '请输入 Git 私有仓库的访问令牌。' })
      return
    }

    setIsSubmitting(true)
    setMessage(null)
    try {
      const successMessage = await onSaved(provider === 'webdav'
        ? { provider, endpoint: destination, remotePath, username, secret }
        : { provider, remoteUrl: destination, branch, accessToken })
      setSecret('')
      setAccessToken('')
      setMessage({ kind: 'success', text: successMessage })
    } catch (error) {
      setMessage({ kind: 'error', text: errorMessage(error, '同步配置未保存，请检查地址和凭据。') })
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <Modal
      description="配置会先进入本地 Vault；令牌和密码不会显示在同步记录、命令行参数或远端地址中。"
      icon={<ShieldCheck size={19} />}
      onClose={onClose}
      title="配置加密云同步"
    >
      <form className="vault-form sync-dialog-form" onSubmit={submit}>
        <div className="encryption-banner"><FileKey2 size={21} /><span><strong>端到端加密已开启</strong><small>neko233-v1 是格式标识；每个 Vault 使用独立随机数据密钥。</small></span></div>
        <label>
          <span>备份方式</span>
          <select aria-label="备份方式" onChange={(event) => setProvider(event.target.value as SyncProvider)} value={provider}>
            <option value="webdav">WebDAV</option>
            <option value="git">Git 私有仓库（GitHub / Gitea）</option>
          </select>
        </label>
        <label>
          <span>{provider === 'git' ? '仓库地址' : 'WebDAV 地址'}</span>
          <input aria-label={provider === 'git' ? '仓库地址' : 'WebDAV 地址'} onChange={(event) => setDestination(event.target.value)} required value={destination} />
        </label>
        {provider === 'webdav' ? (
          <>
            <label>
              <span>远端文件路径</span>
              <input aria-label="远端文件路径" onChange={(event) => setRemotePath(event.target.value)} required value={remotePath} />
              <small>使用相对路径，例如 neko233/vault.bundle。</small>
            </label>
            <label>
              <span>用户名（可选）</span>
              <input aria-label="WebDAV 用户名" autoComplete="username" onChange={(event) => setUsername(event.target.value)} value={username} />
            </label>
            <label>
              <span>密码或应用令牌</span>
              <input aria-label="WebDAV 凭据" autoComplete="off" onChange={(event) => setSecret(event.target.value)} type="password" value={secret} />
            </label>
          </>
        ) : (
          <>
            <label>
              <span>分支</span>
              <input aria-label="Git 分支" onChange={(event) => setBranch(event.target.value)} required value={branch} />
            </label>
            <label>
              <span>访问令牌</span>
              <input aria-label="Git 访问令牌" autoComplete="off" onChange={(event) => setAccessToken(event.target.value)} type="password" value={accessToken} />
              <small>仅接受 HTTPS 私有仓库。令牌只在本机 Vault 和内存中的 Git 凭据回调中使用，不会进入地址、命令行或日志。</small>
            </label>
          </>
        )}
        <div className="provider-explainer">
          {provider === 'git' ? <GitBranch size={18} /> : <Cloud size={18} />}
          <span>{provider === 'git' ? '支持 GitHub、Gitea 和兼容 Git 的 HTTPS 私有仓库。' : '可连接 NAS、Nextcloud、坚果云及其他兼容服务。'}</span>
        </div>
        {message ? <p className={`form-message ${message.kind}`} role="status">{message.kind === 'success' ? <CheckCircle2 size={17} /> : null}{message.text}</p> : null}
        <div className="modal-actions">
          <button className="secondary-action" disabled={isSubmitting} onClick={onClose} type="button">{message?.kind === 'success' ? '完成' : '取消'}</button>
          {message?.kind !== 'success' ? <button className="primary-action" disabled={isSubmitting} type="submit"><ShieldCheck size={17} />{isSubmitting ? '正在保存…' : '保存加密配置'}</button> : null}
        </div>
      </form>
    </Modal>
  )
}
