import {
  ChevronDown,
  ChevronUp,
  CirclePlus,
  CloudCog,
  FolderTree,
  KeyRound,
  Search,
  TerminalSquare,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import type { Host } from '../data'

interface ConnectionLibraryProps {
  hosts: Host[]
  notice: string | null
  onAddHost: () => void
  onOpenSession: (hostId: string) => void
  onOpenSync: () => void
  onQueryChange: (query: string) => void
  onSelectHost: (hostId: string) => void
  query: string
  selectedHostId: string
}

type SortKey = 'name' | 'address'

function authenticationLabel(host: Host): string {
  return host.profileId ? '已加密保存' : '预览配置'
}

export function ConnectionLibrary({
  hosts,
  notice,
  onAddHost,
  onOpenSession,
  onOpenSync,
  onQueryChange,
  onSelectHost,
  query,
  selectedHostId,
}: ConnectionLibraryProps) {
  const [sort, setSort] = useState<{ key: SortKey; direction: 'asc' | 'desc' }>({ key: 'name', direction: 'asc' })
  const filteredHosts = useMemo(() => {
    const normalizedQuery = query.trim().toLocaleLowerCase()
    const matchingHosts = normalizedQuery
      ? hosts.filter((host) => [host.name, host.user, host.address, host.group].some((value) => value.toLocaleLowerCase().includes(normalizedQuery)))
      : hosts

    return [...matchingHosts].sort((left, right) => {
      const comparison = left[sort.key].localeCompare(right[sort.key], 'zh-CN')
      return sort.direction === 'asc' ? comparison : -comparison
    })
  }, [hosts, query, sort])

  function changeSort(key: SortKey) {
    setSort((current) => current.key === key
      ? { key, direction: current.direction === 'asc' ? 'desc' : 'asc' }
      : { key, direction: 'asc' })
  }

  const SortIndicator = ({ column }: { column: SortKey }) => sort.key === column
    ? sort.direction === 'asc' ? <ChevronUp aria-hidden="true" size={14} /> : <ChevronDown aria-hidden="true" size={14} />
    : <ChevronDown aria-hidden="true" className="table-sort-muted" size={14} />

  return (
    <section className="connection-library" aria-label="SSH 连接库">
      <header className="library-header">
        <div>
          <span className="eyebrow">SSH CONNECTIONS</span>
          <h1>连接库</h1>
          <p>管理加密保存的 SSH 目标；打开会话前不会发起网络连接。</p>
        </div>
        <div className="library-header-actions">
          <button className="library-quiet-action" onClick={onOpenSync} type="button"><CloudCog aria-hidden="true" size={17} />加密同步</button>
          <button className="primary-action library-add-action" onClick={onAddHost} type="button"><CirclePlus aria-hidden="true" size={18} />新建 SSH</button>
        </div>
      </header>

      {notice ? <p className="library-notice" role="status">{notice}</p> : null}

      <div className="library-toolbar">
        <label className="library-search">
          <Search aria-hidden="true" size={17} />
          <input aria-label="筛选 SSH 连接" onChange={(event) => onQueryChange(event.target.value)} placeholder="筛选名称、地址、用户或分组" value={query} />
        </label>
        <span className="library-count"><FolderTree aria-hidden="true" size={16} />{filteredHosts.length} 个连接</span>
      </div>

      <div className="connection-table-wrap">
        <table className="connection-table">
          <thead>
            <tr>
              <th scope="col"><span className="sr-only">状态</span></th>
              <th scope="col">
                <button className="table-heading-button" onClick={() => changeSort('name')} type="button">名称<SortIndicator column="name" /></button>
              </th>
              <th scope="col">
                <button className="table-heading-button" onClick={() => changeSort('address')} type="button">地址<SortIndicator column="address" /></button>
              </th>
              <th scope="col">认证</th>
              <th scope="col">分组</th>
              <th scope="col">状态</th>
              <th scope="col"><span className="sr-only">操作</span></th>
            </tr>
          </thead>
          <tbody>
            {filteredHosts.map((host) => (
              <tr className={host.id === selectedHostId ? 'is-selected' : ''} key={host.id}>
                <td><span aria-label="未连接" className="library-status-dot" /></td>
                <td>
                  <button className="host-cell" onClick={() => onSelectHost(host.id)} type="button">
                    <span className="host-cell-icon"><TerminalSquare aria-hidden="true" size={18} /></span>
                    <span><strong>{host.name}</strong><small>{host.user}</small></span>
                  </button>
                </td>
                <td><code>{host.address}:{host.port}</code></td>
                <td><span className={`auth-badge ${host.profileId ? 'is-vault' : ''}`}><KeyRound aria-hidden="true" size={14} />{authenticationLabel(host)}</span></td>
                <td><span className="group-badge">{host.group}</span></td>
                <td><span className="library-state">未连接</span></td>
                <td>
                  <div className="table-actions">
                    <button className="session-open-button" onClick={() => onOpenSession(host.id)} type="button">打开会话</button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {filteredHosts.length === 0 ? (
          <div className="library-empty-state">
            <Search aria-hidden="true" size={22} />
            <strong>没有匹配的连接</strong>
            <span>修改筛选条件，或创建一个新的 SSH 连接。</span>
          </div>
        ) : null}
      </div>

      <footer className="library-footer">
        <span><KeyRound aria-hidden="true" size={16} />凭据仅保存在桌面端的加密 Vault 中</span>
        <span>传统命令行由系统 OpenSSH 启动</span>
      </footer>
    </section>
  )
}
