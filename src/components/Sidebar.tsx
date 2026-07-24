import {
  ChevronDown,
  Folder,
  FolderOpen,
  Plus,
  Server,
  ShieldCheck,
} from 'lucide-react'
import type { AppMode, Host } from '../data'
import { modes } from '../data'

interface SidebarProps {
  activeMode: AppMode
  hosts: Host[]
  onAddHost: () => void
  onModeChange: (mode: AppMode) => void
  onSelectHost: (hostId: string) => void
  selectedHostId: string
}

export function Sidebar({
  activeMode,
  hosts,
  onAddHost,
  onModeChange,
  onSelectHost,
  selectedHostId,
}: SidebarProps) {
  const groups = Array.from(new Set(hosts.map((host) => host.group)))

  return (
    <aside className="sidebar" aria-label="主机导航">
      <div className="sidebar-brand">
        <span className="brand-orb" aria-hidden="true">
          <span />
        </span>
        <span>nekossh</span>
      </div>

      <nav className="primary-nav" aria-label="主要工作区">
        {modes.map(({ id, label, Icon }) => (
          <button
            aria-pressed={activeMode === id}
            className={`nav-item ${activeMode === id ? 'is-active' : ''}`}
            key={id}
            onClick={() => onModeChange(id)}
            type="button"
          >
            <Icon aria-hidden="true" size={21} strokeWidth={1.75} />
            <span>{label}</span>
          </button>
        ))}
      </nav>

      <div className="sidebar-divider" />

      <section className="host-section" aria-labelledby="host-list-title">
        <div className="section-heading">
          <h2 id="host-list-title">连接 <span className="section-count">{hosts.length}</span></h2>
          <div className="section-actions">
            <button aria-label="新建 SSH 连接" className="icon-button subtle" onClick={onAddHost} type="button">
              <Plus aria-hidden="true" size={19} />
            </button>
          </div>
        </div>

        <div className="host-tree">
          {groups.map((group) => (
            <div className="host-group" key={group}>
              <div className="tree-group-label">
                <ChevronDown aria-hidden="true" size={15} />
                {group === '个人服务器' ? (
                  <FolderOpen aria-hidden="true" size={19} strokeWidth={1.6} />
                ) : (
                  <Folder aria-hidden="true" size={19} strokeWidth={1.6} />
                )}
                <span>{group}</span>
              </div>
              {hosts
                .filter((host) => host.group === group)
                .map((host) => (
                  <div
                    aria-current={host.id === selectedHostId ? 'true' : undefined}
                    className={`host-row ${host.id === selectedHostId ? 'is-selected' : ''}`}
                    key={host.id}
                  >
                    <button className="host-select" onClick={() => onSelectHost(host.id)} type="button">
                      <span className={`host-status ${host.status}`} aria-label={host.status === 'online' ? '在线' : '未连接'} />
                      <Server aria-hidden="true" size={19} strokeWidth={1.65} />
                      <span className="host-copy">
                        <strong>{host.name}</strong>
                        <small>{host.user}@{host.address}</small>
                      </span>
                    </button>
                  </div>
                ))}
            </div>
          ))}
        </div>
      </section>

      <div className="sidebar-bottom">
        <div className="encryption-hint">
          <ShieldCheck aria-hidden="true" size={21} />
          <span>端到端加密 · 本地优先</span>
        </div>
      </div>
    </aside>
  )
}
