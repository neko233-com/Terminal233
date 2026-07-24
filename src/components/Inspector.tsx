import { Clock3, KeyRound, LockKeyhole, MapPin, Monitor, Network, Settings2, ShieldCheck, UserRound, X } from 'lucide-react'
import type { Host } from '../data'

interface InspectorProps {
  connectionMessage: string | null
  desktopAvailable: boolean
  host: Host
  isLaunching: boolean
  onConnect: () => void
  onChangePassword: () => void
  onClose: () => void
  vaultUnlocked: boolean
}

const Detail = ({ label, value, icon: Icon }: { label: string; value: string; icon: typeof Monitor }) => (
  <div className="detail-row">
    <dt><Icon aria-hidden="true" size={18} strokeWidth={1.65} />{label}</dt>
    <dd>{value}</dd>
  </div>
)

export function Inspector({
  connectionMessage,
  desktopAvailable,
  host,
  isLaunching,
  onConnect,
  onChangePassword,
  onClose,
  vaultUnlocked,
}: InspectorProps) {
  const canLaunch = desktopAvailable && vaultUnlocked && Boolean(host.profileId)
  const connectionLabel = isLaunching
    ? '正在启动系统 SSH'
    : canLaunch
      ? '已保存，尚未连接'
      : desktopAvailable
        ? '需要已解锁的 Vault 配置'
        : '浏览器预览'

  return (
    <aside className="inspector" aria-label="连接详情">
      <header className="inspector-header">
        <h2>连接详情</h2>
        <button aria-label="关闭连接详情" className="icon-button subtle" onClick={onClose} type="button"><X size={19} /></button>
      </header>
      <div className={`connection-state-card ${canLaunch ? 'is-ready' : ''}`}>
        <span><i className="idle-dot" />{connectionLabel}</span>
        <small>{host.profileId ? `目标 ${host.address}:${host.port}` : '示例数据未发起网络连接'}</small>
      </div>
      {connectionMessage ? <p className="inspector-connection-note" role="status">{connectionMessage}</p> : null}

      <section className="inspector-section">
        <h3>常规</h3>
        <dl>
          <Detail icon={Monitor} label="主机别名" value={host.name} />
          <Detail icon={MapPin} label="地址" value={host.address} />
          <Detail icon={UserRound} label="用户名" value={host.user} />
          <Detail icon={Network} label="协议" value="SSH" />
          <Detail icon={Settings2} label="端口" value={String(host.port)} />
        </dl>
      </section>

      <section className="inspector-section">
        <div className="inspector-section-heading"><h3>安全</h3><button onClick={onChangePassword} type="button">修改主密码</button></div>
        <dl>
          <Detail icon={KeyRound} label="认证算法" value={host.profileId ? '由 OpenSSH 在会话中协商' : '预览未读取'} />
          <Detail icon={LockKeyhole} label="配置保护" value={host.profileId ? '本地 Vault 加密保存' : '预览资料，不含凭据'} />
          <Detail icon={ShieldCheck} label="主机密钥" value={host.profileId ? host.fingerprint : '预览未取得'} />
          <Detail icon={UserRound} label="凭据来源" value={host.profileId ? '本地 Vault（不向界面返回 secret）' : '无远程凭据'} />
        </dl>
      </section>

      <section className="inspector-section inspector-advanced">
        <h3>高级</h3>
        <dl>
          <Detail icon={Clock3} label="会话状态" value="仅系统终端中的真实 SSH 可报告" />
          <Detail icon={Network} label="网络选项" value="由受限 OpenSSH 参数控制" />
        </dl>
      </section>

      <div className="inspector-actions">
        <button className="primary-action" disabled={isLaunching} onClick={onConnect} type="button"><span className="terminal-glyph">›_</span>{isLaunching ? '正在启动…' : canLaunch ? '启动系统 SSH' : '连接说明'}</button>
        <button aria-label="更多连接操作" className="icon-button outlined" type="button">•••</button>
      </div>
    </aside>
  )
}
