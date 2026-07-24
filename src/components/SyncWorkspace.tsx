import { CheckCircle2, Cloud, FileKey2, GitBranch, HardDrive, LockKeyhole, RefreshCw, ShieldCheck, UploadCloud } from 'lucide-react'

interface SyncWorkspaceProps {
  canRestore: boolean
  lastSync: string
  onConfigure: (provider: 'webdav' | 'git') => void
  onRestore: () => void
  restoreProvider: 'WebDAV' | 'Git'
  status: string
}

export function SyncWorkspace({ canRestore, lastSync, onConfigure, onRestore, restoreProvider, status }: SyncWorkspaceProps) {
  const hasUploadedBackup = status.includes('已备份')
  const backupProvider = status.includes('Git') ? 'Git' : 'WebDAV'

  return (
    <section className="workspace sync-workspace" aria-label="端到端加密同步">
      <header className="sync-header">
        <div>
          <p>加密同步</p>
          <h1>你的连接，只属于你</h1>
          <span>凭据先由本地 Vault 加密，再交给你选择的 WebDAV 或 Git 远端。</span>
        </div>
        <div className={`sync-state ${hasUploadedBackup ? 'is-complete' : 'is-pending'}`}>
          <CheckCircle2 aria-hidden="true" size={20} />
          <div><strong>{status}</strong><small>上次同步：{lastSync}</small></div>
        </div>
      </header>

      <div className="sync-layout">
        <section className="sync-security-story" aria-labelledby="sync-security-title">
          <span className="vault-mark"><LockKeyhole size={33} /></span>
          <h2 id="sync-security-title">neko233-v1 保密箱</h2>
          <p>配置、SSH 密钥和主机备注会使用你的主密码派生密钥封装。同步服务只能保存密文。</p>
          <div className="sync-security-steps">
            <span><FileKey2 size={18} />本地加密</span>
            <i />
            <span><UploadCloud size={18} />私有远端</span>
            <i />
            <span><ShieldCheck size={18} />安全恢复</span>
          </div>
          <div className="sync-actions">
            <button className="outlined-action" onClick={() => onConfigure('webdav')} type="button"><RefreshCw size={17} />配置并备份</button>
            {canRestore ? <button className="outlined-action" onClick={onRestore} type="button"><FileKey2 size={17} />从 {restoreProvider} 恢复</button> : null}
          </div>
        </section>

        <section className="sync-destinations" aria-labelledby="backup-destination-title">
          <div className="sync-section-title"><div><p>同步目的地</p><h2 id="backup-destination-title">选择自己的免费云存储</h2></div></div>
          <button className="provider-row" onClick={() => onConfigure('webdav')} type="button">
            <span className="provider-symbol webdav-symbol"><Cloud size={23} /></span>
            <span className="provider-copy"><strong>WebDAV</strong><small>坚果云、Nextcloud、NAS 或任意 WebDAV</small></span>
            <span className="provider-connect">配置</span>
          </button>
          <button className="provider-row" onClick={() => onConfigure('git')} type="button">
            <span className="provider-symbol git-symbol"><GitBranch size={23} /></span>
            <span className="provider-copy"><strong>Git 私有仓库</strong><small>GitHub、Gitea、GitLab 或自托管仓库</small></span>
            <span className="provider-connect">配置</span>
          </button>
          <div className="sync-note"><HardDrive size={17} />不使用云端也没关系：你的数据始终保留在本地。</div>
        </section>
      </div>

      <section className="sync-log" aria-label="同步活动">
        <div><h2>最近活动</h2><button type="button">查看全部</button></div>
        <ol>
          <li><i className="online-dot" /><span><strong>本地 Vault 加密就绪</strong><small>neko233-v1 格式 · 密钥仅保留在本机</small></span><time>本地</time></li>
          <li><i className={hasUploadedBackup ? 'online-dot' : 'idle-dot'} /><span><strong>{hasUploadedBackup ? `${backupProvider} 加密备份已上传` : '尚未传输云端副本'}</strong><small>{hasUploadedBackup ? '远端只保存不可读的加密 Vault 包；恢复需要你明确确认并输入备份主密码。' : '配置 WebDAV 或 Git HTTPS 私有仓库后可上传加密副本。'}</small></span><time>{lastSync}</time></li>
        </ol>
      </section>
    </section>
  )
}
