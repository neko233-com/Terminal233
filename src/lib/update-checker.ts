import packageMetadata from '../../package.json'

export type UpdateAsset = {
  component: 'desktop' | 'server'
  platform: 'windows' | 'linux'
  architecture: 'x86_64' | 'arm64' | 'amd64'
  name: string
  bytes: number
  sha256: string
  url: string
}

export type UpdateManifest = {
  schemaVersion: 1
  product: 'Terminal233'
  version: string
  tag: string
  channel: 'stable' | 'beta'
  generatedAtUtc: string
  releaseUrl: string
  updateManifestUrl: string
  assets: UpdateAsset[]
}

export type UpdateCheckResult = {
  manifest: UpdateManifest
  currentVersion: string
  updateAvailable: boolean
  asset: UpdateAsset | null
}

export const currentTerminal233Version = packageMetadata.version
export const defaultUpdateManifestUrl = (import.meta.env.VITE_TERMINAL233_UPDATE_MANIFEST_URL as string | undefined)
  ?? 'https://github.com/neko233-com/Terminal233/releases/latest/download/terminal233-update.json'

const VERSION_PATTERN = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/u
const SHA256_PATTERN = /^[0-9a-f]{64}$/u

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isAllowedUrl(value: unknown): value is string {
  if (typeof value !== 'string') return false
  try {
    const url = new URL(value)
    return url.protocol === 'https:' || (url.protocol === 'http:' && ['127.0.0.1', 'localhost'].includes(url.hostname))
  } catch {
    return false
  }
}

function isVersion(value: unknown): value is string {
  return typeof value === 'string' && VERSION_PATTERN.test(value)
}

function parseVersion(value: string): [number, number, number, string] {
  const [core, suffix = ''] = value.split('-', 2)
  const parts = core.split('.').map((part) => Number(part))
  return [parts[0] ?? 0, parts[1] ?? 0, parts[2] ?? 0, suffix]
}

export function compareVersions(left: string, right: string): number {
  const a = parseVersion(left)
  const b = parseVersion(right)
  for (let index = 0; index < 3; index += 1) {
    if (a[index] !== b[index]) return a[index] > b[index] ? 1 : -1
  }
  if (a[3] === b[3]) return 0
  if (!a[3]) return 1
  if (!b[3]) return -1
  return a[3] > b[3] ? 1 : -1
}

export function parseUpdateManifest(payload: unknown): UpdateManifest {
  if (!isRecord(payload) || payload.schemaVersion !== 1 || payload.product !== 'Terminal233') {
    throw new Error('更新清单不是受支持的 Terminal233 清单。')
  }
  if (!isVersion(payload.version) || payload.tag !== `v${payload.version}`) {
    throw new Error('更新清单版本格式无效。')
  }
  if (payload.channel !== 'stable' && payload.channel !== 'beta') throw new Error('更新清单发布通道无效。')
  if (typeof payload.generatedAtUtc !== 'string' || !isAllowedUrl(payload.releaseUrl) || !isAllowedUrl(payload.updateManifestUrl)) {
    throw new Error('更新清单元数据无效。')
  }
  if (!Array.isArray(payload.assets) || payload.assets.length === 0) throw new Error('更新清单没有发布资产。')
  const assets = payload.assets.map((value) => {
    if (!isRecord(value)
      || (value.component !== 'desktop' && value.component !== 'server')
      || (value.platform !== 'windows' && value.platform !== 'linux')
      || !['x86_64', 'arm64', 'amd64'].includes(String(value.architecture))
      || typeof value.name !== 'string'
      || typeof value.bytes !== 'number' || !Number.isSafeInteger(value.bytes) || value.bytes < 1
      || typeof value.sha256 !== 'string' || !SHA256_PATTERN.test(value.sha256)
      || !isAllowedUrl(value.url)) {
      throw new Error('更新清单包含无效资产。')
    }
    return {
      component: value.component,
      platform: value.platform,
      architecture: value.architecture,
      name: value.name,
      bytes: Number(value.bytes),
      sha256: value.sha256,
      url: value.url,
    } as UpdateAsset
  })
  return {
    schemaVersion: 1,
    product: 'Terminal233',
    version: payload.version,
    tag: payload.tag,
    channel: payload.channel,
    generatedAtUtc: payload.generatedAtUtc,
    releaseUrl: payload.releaseUrl,
    updateManifestUrl: payload.updateManifestUrl,
    assets,
  }
}

export function selectCurrentDesktopAsset(assets: UpdateAsset[]): UpdateAsset | null {
  const userAgent = typeof navigator === 'undefined' ? '' : `${navigator.userAgent} ${navigator.platform}`
  if (!/Windows/iu.test(userAgent)) return null
  const architecture = /ARM64|AARCH64/iu.test(userAgent) ? 'arm64' : 'x86_64'
  return assets.find((asset) => asset.component === 'desktop' && asset.platform === 'windows' && asset.architecture === architecture)
    ?? assets.find((asset) => asset.component === 'desktop' && asset.platform === 'windows' && asset.architecture === 'x86_64')
    ?? null
}

export async function checkForTerminal233Update(manifestUrl = defaultUpdateManifestUrl): Promise<UpdateCheckResult> {
  if (!isAllowedUrl(manifestUrl)) throw new Error('更新清单地址必须使用 HTTPS。')
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort(), 8_000)
  try {
    const response = await fetch(manifestUrl, { headers: { Accept: 'application/json' }, signal: controller.signal })
    if (!response.ok) throw new Error(`更新清单请求失败（${response.status}）。`)
    const manifest = parseUpdateManifest(await response.json())
    return {
      manifest,
      currentVersion: currentTerminal233Version,
      updateAvailable: compareVersions(manifest.version, currentTerminal233Version) > 0,
      asset: selectCurrentDesktopAsset(manifest.assets),
    }
  } finally {
    window.clearTimeout(timeout)
  }
}
