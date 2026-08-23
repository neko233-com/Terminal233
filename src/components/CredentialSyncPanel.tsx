import { useRef, useState } from 'react'
import {
  CredentialServerClient,
  defaultCredentialServerUrl,
  type CredentialAccount,
} from '../lib/credential-server'

export default function CredentialSyncPanel({ onImport }: { onImport: (account: CredentialAccount) => void }) {
  const [open, setOpen] = useState(false)
  const [serverUrl, setServerUrl] = useState(defaultCredentialServerUrl)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [officialToken, setOfficialToken] = useState('')
  const [registerMode, setRegisterMode] = useState(false)
  const [client, setClient] = useState<CredentialServerClient | null>(null)
  const [accounts, setAccounts] = useState<CredentialAccount[]>([])
  const [accountForm, setAccountForm] = useState({ label: '', host: '', port: '22', sshUsername: 'root', sshPassword: '' })
  const [pending, setPending] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const clientRef = useRef<CredentialServerClient | null>(null)

  function createClientForUrl() {
    const next = new CredentialServerClient(serverUrl)
    return next
  }

  async function submitAuth() {
    setPending(true)
    setNotice(null)
    try {
      const next = clientRef.current?.baseUrl === serverUrl.trim().replace(/\/+$/u, '')
        ? clientRef.current
        : createClientForUrl()
      if (registerMode) {
        await next.register(username, password, officialToken)
        clientRef.current = null
        setClient(null)
        setRegisterMode(false)
        setNotice('注册成功，请使用账号密码登录。')
      } else {
        await next.login(username, password)
        clientRef.current = next
        setClient(next)
        setAccounts(await next.listAccounts())
        setNotice('同步服务器登录成功。')
      }
    } catch (error) {
      setNotice(error instanceof Error ? error.message : '同步服务器操作失败。')
    } finally {
      setPending(false)
      setPassword('')
      setOfficialToken('')
    }
  }

  async function refresh() {
    if (!client) return
    setPending(true)
    try { setAccounts(await client.listAccounts()); setNotice('账号列表已刷新。') }
    catch (error) { setNotice(error instanceof Error ? error.message : '读取云端账号失败。') }
    finally { setPending(false) }
  }

  async function saveAccount() {
    if (!client) return
    const port = Number(accountForm.port)
    if (!accountForm.host.trim() || !accountForm.sshUsername.trim() || !accountForm.sshPassword || !Number.isInteger(port) || port < 1 || port > 65_535) {
      setNotice('请完整填写 SSH 地址、用户、密码和有效端口。')
      return
    }
    setPending(true)
    setNotice(null)
    try {
      await client.saveAccount({
        label: accountForm.label.trim() || accountForm.host.trim(),
        host: accountForm.host.trim(),
        port,
        username: accountForm.sshUsername.trim(),
        password: accountForm.sshPassword,
      })
      setAccountForm((current) => ({ ...current, label: '', sshPassword: '' }))
      setAccounts(await client.listAccounts())
      setNotice('SSH 凭据已验证并保存。')
    } catch (error) {
      setNotice(error instanceof Error ? error.message : 'SSH 验证失败，未保存。')
      setAccountForm((current) => ({ ...current, sshPassword: '' }))
    } finally {
      setPending(false)
    }
  }

  function logout() {
    client?.logout()
    clientRef.current = null
    setClient(null)
    setAccounts([])
    setNotice('已退出同步服务器。')
  }

  return <section className="ide-sync-panel">
    <button className="ide-sync-toggle" onClick={() => setOpen((value) => !value)} type="button">
      <span><i className={client ? 'is-online' : ''} />云端同步</span><small>{client ? accounts.length + ' / 1000' : '自部署'}</small>
    </button>
    {open ? <div className="ide-sync-body">
      <label><span>服务器地址</span><input disabled={pending || !!client} value={serverUrl} onChange={(event) => setServerUrl(event.target.value)} /></label>
      {!client ? <>
        <label><span>账号</span><input autoComplete="username" disabled={pending} value={username} onChange={(event) => setUsername(event.target.value)} /></label>
        <label><span>{registerMode ? '登录密码' : '密码'}</span><input autoComplete={registerMode ? 'new-password' : 'current-password'} disabled={pending} type="password" value={password} onChange={(event) => setPassword(event.target.value)} /></label>
        {registerMode ? <label><span>官方注册 token</span><input disabled={pending} type="password" value={officialToken} onChange={(event) => setOfficialToken(event.target.value)} /></label> : null}
        {notice ? <p className="ide-sync-notice" role="status">{notice}</p> : null}
        <div className="ide-sync-actions"><button className="ide-primary" disabled={pending} onClick={() => void submitAuth()} type="button">{pending ? '处理中…' : registerMode ? '注册' : '登录'}</button><button className="ide-link-button" disabled={pending} onClick={() => setRegisterMode((value) => !value)} type="button">{registerMode ? '返回登录' : '注册新账号'}</button></div>
      </> : <>
        <div className="ide-sync-account-head"><span>已登录：{username || '同步账号'}</span><button className="ide-link-button" onClick={logout} type="button">退出</button></div>
        <div className="ide-sync-add">
          <strong>添加 SSH 账号</strong>
          <label><span>名称</span><input disabled={pending} placeholder="生产服务器" value={accountForm.label} onChange={(event) => setAccountForm({ ...accountForm, label: event.target.value })} /></label>
          <div className="ide-sync-inline"><label><span>主机</span><input disabled={pending} placeholder="example.com" value={accountForm.host} onChange={(event) => setAccountForm({ ...accountForm, host: event.target.value })} /></label><label><span>端口</span><input disabled={pending} inputMode="numeric" value={accountForm.port} onChange={(event) => setAccountForm({ ...accountForm, port: event.target.value })} /></label></div>
          <label><span>用户</span><input disabled={pending} value={accountForm.sshUsername} onChange={(event) => setAccountForm({ ...accountForm, sshUsername: event.target.value })} /></label>
          <label><span>SSH 密码</span><input disabled={pending} type="password" value={accountForm.sshPassword} onChange={(event) => setAccountForm({ ...accountForm, sshPassword: event.target.value })} /></label>
          <button className="ide-primary ide-sync-save" disabled={pending} onClick={() => void saveAccount()} type="button">{pending ? '正在验证…' : '验证并保存'}</button>
        </div>
        {notice ? <p className="ide-sync-notice" role="status">{notice}</p> : null}
        <div className="ide-sync-list"><div className="ide-sync-list-head"><strong>已保存账号</strong><button className="ide-link-button" disabled={pending} onClick={() => void refresh()} type="button">刷新</button></div>{accounts.map((account) => <div className="ide-sync-account" key={account.id}><span><b>{account.label}</b><small>{account.username}@{account.host}:{account.port}</small></span><button className="ide-link-button" disabled={pending} onClick={() => onImport(account)} type="button">导入</button></div>)}{!accounts.length ? <small className="ide-sync-empty">暂无已验证 SSH 账号。</small> : null}</div>
      </>}
    </div> : null}
  </section>
}
