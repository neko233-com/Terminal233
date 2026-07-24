import { FolderTree, KeyRound, LockKeyhole, Settings2, ShieldCheck, TerminalSquare } from 'lucide-react'
import { useState } from 'react'
import type { FormEvent } from 'react'
import type { ProfileAuthenticationDraft, ProfileDraft } from '../lib/desktop'
import { errorMessage } from '../lib/desktop'
import { Modal } from './Modal'

interface ProfileDialogProps {
  onClose: () => void
  onSaved: (draft: ProfileDraft) => Promise<void>
}

type AuthenticationChoice = ProfileAuthenticationDraft['kind']
type EditorSection = 'connection' | 'authentication' | 'advanced'

export function ProfileDialog({ onClose, onSaved }: ProfileDialogProps) {
  const [label, setLabel] = useState('')
  const [host, setHost] = useState('')
  const [port, setPort] = useState('22')
  const [username, setUsername] = useState('')
  const [group, setGroup] = useState('')
  const [authenticationKind, setAuthenticationKind] = useState<AuthenticationChoice>('agent')
  const [secret, setSecret] = useState('')
  const [keyPath, setKeyPath] = useState('')
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [message, setMessage] = useState<string | null>(null)
  const [activeSection, setActiveSection] = useState<EditorSection>('connection')

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const parsedPort = Number(port)
    if (!label.trim() || !host.trim() || !username.trim()) {
      setActiveSection('connection')
      setMessage('请填写连接名称、主机地址和用户名。')
      return
    }
    if (!Number.isInteger(parsedPort) || parsedPort < 1 || parsedPort > 65535) {
      setActiveSection('connection')
      setMessage('端口必须介于 1 到 65535。')
      return
    }
    if (authenticationKind === 'private_key_file' && !keyPath.trim()) {
      setMessage('请输入私钥文件路径。')
      return
    }
    if (authenticationKind === 'password' && !secret) {
      setMessage('请输入 SSH 密码。')
      return
    }

    const authentication: ProfileAuthenticationDraft = authenticationKind === 'password'
      ? { kind: 'password', password: secret }
      : authenticationKind === 'private_key_file'
        ? { kind: 'private_key_file', privateKeyPath: keyPath }
        : { kind: 'agent' }

    setIsSubmitting(true)
    setMessage(null)
    try {
      await onSaved({
        label,
        host,
        port: parsedPort,
        username,
        authentication,
        tags: group.trim() ? [group.trim()] : [],
      })
      setSecret('')
      onClose()
    } catch (error) {
      setMessage(errorMessage(error, '连接配置未保存，请检查输入后重试。'))
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <Modal
      description="只保存真正会被使用的连接设置；密码与私钥路径仅写入本地加密 Vault。"
      icon={<TerminalSquare size={19} />}
      onClose={onClose}
      title="新建 SSH 连接"
    >
      <form className="vault-form" onSubmit={submit}>
        <div className="profile-editor">
          <nav aria-label="SSH 连接设置" className="profile-editor-nav">
            <button aria-pressed={activeSection === 'connection'} className={activeSection === 'connection' ? 'is-active' : ''} onClick={() => setActiveSection('connection')} type="button"><TerminalSquare aria-hidden="true" size={17} />基本信息</button>
            <button aria-pressed={activeSection === 'authentication'} className={activeSection === 'authentication' ? 'is-active' : ''} onClick={() => setActiveSection('authentication')} type="button"><KeyRound aria-hidden="true" size={17} />认证方式</button>
            <button aria-pressed={activeSection === 'advanced'} className={activeSection === 'advanced' ? 'is-active' : ''} onClick={() => setActiveSection('advanced')} type="button"><Settings2 aria-hidden="true" size={17} />安全边界</button>
          </nav>
          <div className="profile-editor-content">
            {activeSection === 'connection' ? (
              <div className="profile-editor-section">
                <div className="editor-section-heading"><h3>基本信息</h3><p>一个连接对应一个 SSH 目标，可按分组整理。</p></div>
                <label>
                  <span>连接名称</span>
                  <input aria-label="连接名称" autoFocus onChange={(event) => setLabel(event.target.value)} placeholder="例如：生产 Web" required value={label} />
                </label>
                <label>
                  <span>连接分组 <small>可选</small></span>
                  <input aria-label="连接分组" onChange={(event) => setGroup(event.target.value)} placeholder="例如：生产环境" value={group} />
                </label>
                <div className="form-two-columns profile-address-row">
                  <label>
                    <span>主机地址</span>
                    <input aria-label="主机地址" autoComplete="off" onChange={(event) => setHost(event.target.value)} placeholder="ssh.example.com" required value={host} />
                  </label>
                  <label>
                    <span>端口</span>
                    <input aria-label="SSH 端口" inputMode="numeric" onChange={(event) => setPort(event.target.value)} required value={port} />
                  </label>
                </div>
                <label>
                  <span>登录用户</span>
                  <input aria-label="SSH 用户名" autoComplete="username" onChange={(event) => setUsername(event.target.value)} placeholder="neko" required value={username} />
                </label>
              </div>
            ) : null}
            {activeSection === 'authentication' ? (
              <div className="profile-editor-section">
                <div className="editor-section-heading"><h3>认证方式</h3><p>优先选择 SSH Agent 或私钥文件，避免将密码暴露在系统命令行中。</p></div>
                <label>
                  <span>认证方式</span>
                  <select aria-label="SSH 认证方式" onChange={(event) => setAuthenticationKind(event.target.value as AuthenticationChoice)} value={authenticationKind}>
                    <option value="agent">SSH Agent（推荐）</option>
                    <option value="private_key_file">本机私钥文件</option>
                    <option value="password">密码（仅加密保存在 Vault）</option>
                  </select>
                </label>
                {authenticationKind === 'private_key_file' ? (
                  <label>
                    <span>私钥文件路径</span>
                    <input aria-label="私钥文件路径" autoComplete="off" onChange={(event) => setKeyPath(event.target.value)} placeholder="C:\\Users\\you\\.ssh\\id_ed25519" required value={keyPath} />
                  </label>
                ) : null}
                {authenticationKind === 'password' ? (
                  <label>
                    <span>SSH 密码</span>
                    <input aria-label="SSH 密码" autoComplete="off" onChange={(event) => setSecret(event.target.value)} required type="password" value={secret} />
                  </label>
                ) : null}
                <div className="provider-explainer profile-auth-note">
                  {authenticationKind === 'password' ? <LockKeyhole size={18} /> : <KeyRound size={18} />}
                  <span>{authenticationKind === 'password'
                    ? '密码会加密保存在 Vault。传统系统 OpenSSH 不会把它放入命令行。'
                    : '系统 OpenSSH 使用经过验证的参数启动；主密码和云同步凭据不会进入进程参数。'}</span>
                </div>
              </div>
            ) : null}
            {activeSection === 'advanced' ? (
              <div className="profile-editor-section safety-section">
                <div className="editor-section-heading"><h3>安全边界</h3><p>只展示当前真实生效的保护措施，不把未实现的代理或跳板能力伪装成配置项。</p></div>
                <div className="safety-list">
                  <span><ShieldCheck aria-hidden="true" size={18} /><b>专用 known_hosts</b><small>首次连接会由系统 OpenSSH 记录主机密钥。</small></span>
                  <span><FolderTree aria-hidden="true" size={18} /><b>凭据本地加密</b><small>连接配置与云同步凭据写入 NekoSSH Vault。</small></span>
                  <span><LockKeyhole aria-hidden="true" size={18} /><b>无命令行密码</b><small>不会将 SSH 密码写入进程参数或终端历史。</small></span>
                </div>
              </div>
            ) : null}
          </div>
        </div>
        {message ? <p className="form-message error" role="status">{message}</p> : null}
        <div className="modal-actions">
          <button className="secondary-action" disabled={isSubmitting} onClick={onClose} type="button">取消</button>
          <button className="primary-action" disabled={isSubmitting} type="submit"><ShieldCheck size={17} />{isSubmitting ? '正在保存…' : '保存到加密 Vault'}</button>
        </div>
      </form>
    </Modal>
  )
}
