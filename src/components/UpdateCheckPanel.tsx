import { useEffect, useState } from 'react'
import { Check, Download, RefreshCw, ShieldCheck } from 'lucide-react'
import {
  checkForTerminal233Update,
  currentTerminal233Version,
  type UpdateCheckResult,
} from '../lib/update-checker'

type UpdateState =
  | { kind: 'checking' }
  | { kind: 'ready'; result: UpdateCheckResult }
  | { kind: 'error'; message: string }

export default function UpdateCheckPanel() {
  const [state, setState] = useState<UpdateState>({ kind: 'checking' })

  async function check() {
    setState({ kind: 'checking' })
    try {
      setState({ kind: 'ready', result: await checkForTerminal233Update() })
    } catch (error) {
      setState({ kind: 'error', message: error instanceof Error ? error.message : '更新检查失败。' })
    }
  }

  useEffect(() => {
    void check()
    const timer = window.setInterval(() => void check(), 6 * 60 * 60 * 1000)
    return () => window.clearInterval(timer)
  }, [])

  const result = state.kind === 'ready' ? state.result : null
  return <section className="ide-update-panel">
    <div className="ide-update-head">
      <span><ShieldCheck size={13} />软件更新</span>
      <button aria-label="重新检查更新" className="ide-update-refresh" disabled={state.kind === 'checking'} onClick={() => void check()} title="重新检查更新" type="button"><RefreshCw size={12} /></button>
    </div>
    <div className="ide-update-status" aria-live="polite">
      {state.kind === 'checking' ? <span className="is-muted">正在检查…</span> : null}
      {state.kind === 'error' ? <span className="is-error">{state.message}</span> : null}
      {result && !result.updateAvailable ? <span className="is-current"><Check size={12} />已是最新版本 v{currentTerminal233Version}</span> : null}
      {result?.updateAvailable ? <>
        <strong>发现新版本 v{result.manifest.version}</strong>
        {result.asset ? <a href={result.asset.url} rel="noreferrer" target="_blank"><Download size={12} />下载 Windows {result.asset.architecture}</a> : <span className="is-muted">暂无当前平台桌面包。</span>}
        <small>SHA-256：{result.asset?.sha256.slice(0, 16) ?? '见发布清单'}…</small>
      </> : null}
    </div>
  </section>
}
