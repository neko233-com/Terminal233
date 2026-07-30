import { readFile, realpath, stat } from 'node:fs/promises'
import { extname, isAbsolute, relative, resolve, sep } from 'node:path'

const MAX_STATIC_FILE_BYTES = 16 * 1024 * 1024
const HASHED_ASSET = /^assets\/.+-[A-Za-z0-9_-]{8,}\.[A-Za-z0-9]+$/
const SECURITY_HEADERS = Object.freeze({
  'content-security-policy': [
    "default-src 'self'",
    "connect-src 'self'",
    "img-src 'self' data: blob:",
    "style-src 'self' 'unsafe-inline'",
    "font-src 'self' data:",
    "script-src 'self'",
    "object-src 'none'",
    "base-uri 'none'",
    "form-action 'none'",
    "frame-ancestors 'none'",
  ].join('; '),
  'cross-origin-opener-policy': 'same-origin',
  'cross-origin-resource-policy': 'same-origin',
  'permissions-policy': 'camera=(), display-capture=(), geolocation=(), microphone=(), payment=(), usb=()',
  'referrer-policy': 'no-referrer',
  'x-content-type-options': 'nosniff',
  'x-frame-options': 'DENY',
})

const CONTENT_TYPES = new Map([
  ['.css', 'text/css; charset=utf-8'],
  ['.html', 'text/html; charset=utf-8'],
  ['.ico', 'image/x-icon'],
  ['.jpeg', 'image/jpeg'],
  ['.jpg', 'image/jpeg'],
  ['.js', 'text/javascript; charset=utf-8'],
  ['.json', 'application/json; charset=utf-8'],
  ['.map', 'application/json; charset=utf-8'],
  ['.png', 'image/png'],
  ['.svg', 'image/svg+xml'],
  ['.webp', 'image/webp'],
  ['.woff', 'font/woff'],
  ['.woff2', 'font/woff2'],
])

export function createStaticWebHandler(rootDirectory) {
  if (typeof rootDirectory !== 'string' || rootDirectory.length === 0) {
    throw new TypeError('A static web root is required.')
  }
  const configuredRoot = resolve(rootDirectory)
  const canonicalRootPromise = realpath(configuredRoot)

  return async function serveStaticWeb(request, response, url) {
    if (request.method !== 'GET' && request.method !== 'HEAD') return false
    const requestPath = decodeRequestPath(url.pathname)
    if (requestPath === null) return false

    const relativePath = requestPath === '/' ? 'index.html' : requestPath.slice(1)
    if (!relativePath || relativePath.includes('\\') || relativePath.split('/').includes('..')) {
      return false
    }

    const canonicalRoot = await canonicalRootPromise
    const candidate = resolve(canonicalRoot, ...relativePath.split('/'))
    if (!isContainedPath(candidate, canonicalRoot)) return false

    let canonicalFile
    let fileStats
    try {
      canonicalFile = await realpath(candidate)
      if (!isContainedPath(canonicalFile, canonicalRoot)) return false
      fileStats = await stat(canonicalFile)
    } catch (error) {
      if (error?.code === 'ENOENT' || error?.code === 'ENOTDIR') return false
      throw error
    }
    if (!fileStats.isFile() || fileStats.size > MAX_STATIC_FILE_BYTES) return false

    const body = request.method === 'HEAD' ? null : await readFile(canonicalFile)
    response.writeHead(200, {
      ...SECURITY_HEADERS,
      'cache-control': HASHED_ASSET.test(relativePath)
        ? 'public, max-age=31536000, immutable'
        : 'no-store',
      'content-length': fileStats.size,
      'content-type': CONTENT_TYPES.get(extname(canonicalFile).toLowerCase())
        ?? 'application/octet-stream',
    })
    response.end(body ?? undefined)
    return true
  }
}

function decodeRequestPath(pathname) {
  try {
    const decoded = decodeURIComponent(pathname)
    if (!decoded.startsWith('/') || decoded.includes('\0')) return null
    return decoded
  } catch {
    return null
  }
}

function isContainedPath(path, root) {
  const child = relative(root, path)
  return child === '' || (!child.startsWith(`..${sep}`) && child !== '..' && !isAbsolute(child))
}
