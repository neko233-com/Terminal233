export type CredentialAccount = {
  id: string
  label: string
  host: string
  port: number
  username: string
  password: string
  hostKeyFingerprint?: string
  createdAt?: string
  updatedAt?: string
}

type ApiError = { code?: string; message?: string }

export const defaultCredentialServerUrl = (import.meta.env.VITE_TERMINAL233_SERVER_URL as string | undefined)
  ?? 'http://127.0.0.1:8787'

function normalizeUrl(value: string): string {
  const trimmed = value.trim().replace(/\/+$/u, '')
  let url: URL
  try {
    url = new URL(trimmed)
  } catch {
    throw new Error('同步服务器地址必须是有效的 HTTPS 地址。')
  }

  if (url.username || url.password) throw new Error('同步服务器地址不能包含账号或密码。')
  if (url.search || url.hash) throw new Error('同步服务器地址不能包含查询参数或片段。')

  const isLoopbackHttp = url.protocol === 'http:'
    && ['localhost', '127.0.0.1', '::1', '[::1]'].includes(url.hostname.toLowerCase())
  if (url.protocol !== 'https:' && !isLoopbackHttp) {
    throw new Error('公网同步服务器必须使用 HTTPS；HTTP 仅允许本机回环地址。')
  }
  return trimmed
}

export class CredentialServerClient {
  readonly baseUrl: string
  private token: string | null = null

  constructor(baseUrl: string) {
    this.baseUrl = normalizeUrl(baseUrl)
  }

  get authenticated(): boolean {
    return this.token !== null
  }

  async register(username: string, password: string, officialToken: string): Promise<void> {
    await this.request('/api/v1/register', {
      method: 'POST',
      body: { username, password, officialToken },
    })
  }

  async login(username: string, password: string): Promise<void> {
    const response = await this.request<{ token: string }>('/api/v1/login', {
      method: 'POST',
      body: { username, password },
    })
    this.token = response.token
  }

  logout(): void {
    const token = this.token
    this.token = null
    if (token) void this.request('/api/v1/logout', { method: 'POST', token }).catch(() => undefined)
  }

  listAccounts(): Promise<CredentialAccount[]> {
    return this.request<CredentialAccount[]>('/api/v1/accounts')
  }

  saveAccount(account: Omit<CredentialAccount, 'id' | 'createdAt' | 'updatedAt'>): Promise<CredentialAccount> {
    return this.request<CredentialAccount>('/api/v1/accounts', { method: 'POST', body: account })
  }

  deleteAccount(id: string): Promise<void> {
    return this.request<void>(`/api/v1/accounts/${encodeURIComponent(id)}`, { method: 'DELETE' })
  }

  private async request<T = unknown>(path: string, options: { method?: string; body?: unknown; token?: string } = {}): Promise<T> {
    const response = await fetch(`${this.baseUrl}${path}`, {
      method: options.method ?? 'GET',
      credentials: 'omit',
      redirect: 'error',
      headers: {
        Accept: 'application/json',
        ...(options.body === undefined ? {} : { 'Content-Type': 'application/json' }),
        ...((options.token ?? this.token) ? { Authorization: `Bearer ${options.token ?? this.token}` } : {}),
      },
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
    })
    const text = await response.text()
    let payload: unknown = null
    if (text) {
      try { payload = JSON.parse(text) } catch { payload = { message: text } }
    }
    if (!response.ok) {
      const error = payload as ApiError | null
      throw new Error(error?.message || `同步服务器请求失败（${response.status}）。`)
    }
    return payload as T
  }
}
