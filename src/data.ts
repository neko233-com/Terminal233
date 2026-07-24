import type { LucideIcon } from 'lucide-react'
import { LayoutList, TerminalSquare } from 'lucide-react'

export type AppMode = 'library' | 'connect' | 'desktop' | 'browser' | 'sync'

export type HostStatus = 'online' | 'idle'

export interface Host {
  id: string
  /** Set only for profiles that were read from the encrypted desktop Vault. */
  profileId?: string
  name: string
  user: string
  address: string
  port: number
  group: string
  status: HostStatus
  latency: string
  os: string
  fingerprint: string
  source?: 'preview' | 'vault'
}

export interface ModeItem {
  id: AppMode
  label: string
  Icon: LucideIcon
}

export const modes: ModeItem[] = [
  { id: 'library', label: '连接库', Icon: LayoutList },
  { id: 'connect', label: '会话', Icon: TerminalSquare },
]

export const initialHosts: Host[] = [
  {
    id: 'home',
    name: 'neko@home',
    user: 'neko',
    address: '192.168.1.10',
    port: 22,
    group: '个人服务器',
    status: 'idle',
    latency: '—',
    os: '仅作界面预览',
    fingerprint: '未获取（未建立会话）',
    source: 'preview',
  },
  {
    id: 'gitea',
    name: 'gitea-build',
    user: 'runner',
    address: '10.10.4.18',
    port: 22,
    group: '个人服务器',
    status: 'idle',
    latency: '—',
    os: '仅作界面预览',
    fingerprint: '未获取（未建立会话）',
    source: 'preview',
  },
  {
    id: 'lab',
    name: 'lab',
    user: 'root',
    address: '10.0.0.20',
    port: 22,
    group: '实验环境',
    status: 'idle',
    latency: '—',
    os: '仅作界面预览',
    fingerprint: '未获取（未建立会话）',
    source: 'preview',
  },
  {
    id: 'prod',
    name: 'prod-web-01',
    user: 'ubuntu',
    address: '47.100.1.23',
    port: 22,
    group: '生产环境',
    status: 'idle',
    latency: '—',
    os: '仅作界面预览',
    fingerprint: '未获取（未建立会话）',
    source: 'preview',
  },
]

export const terminalGreeting = [
  '[ NekoSSH 安全终端预览 ]',
  '',
  '此区域不会建立 SSH 网络连接，也不会执行本机或远端命令。',
  '真实传统 SSH 会话会由“启动系统 SSH”在独立系统终端中运行。',
  '',
  '提示：启动后，系统终端会在专用 known_hosts 文件中安全记录首次主机密钥。',
]
