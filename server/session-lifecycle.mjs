export function cookieValue(cookieHeader, name) {
  if (typeof cookieHeader !== 'string' || !name) return null
  for (const part of cookieHeader.split(';')) {
    const separator = part.indexOf('=')
    if (separator < 0 || part.slice(0, separator).trim() !== name) continue
    const value = part.slice(separator + 1).trim()
    return value || null
  }
  return null
}

export function touchConnectionActivity(connection, browserSessions, ttlMs, now = Date.now()) {
  connection.expiresAt = now + ttlMs
  for (const session of browserSessions.values()) {
    if (session.owner === connection.owner && session.expiresAt > now) {
      session.expiresAt = now + ttlMs
    }
  }
}

export function retireBrowserSession(token, browserSessions, disconnectOwner) {
  if (!token) return false
  const session = browserSessions.get(token)
  browserSessions.delete(token)
  if (!session) return false
  disconnectOwner(session.owner)
  return true
}

export function releaseMatches(session, releaseId) {
  return Boolean(session && typeof releaseId === 'string' && releaseId.length > 0 && session.releaseId === releaseId)
}

export function registerRevocableOperation(resource, cancel) {
  if (resource.revoked) {
    cancel(resource.revokeReason)
    return () => {}
  }
  resource.operations.add(cancel)
  return () => resource.operations.delete(cancel)
}

export function revokeOperations(resource, reason) {
  if (resource.revoked) return false
  resource.revoked = true
  resource.revokeReason = reason
  const operations = [...resource.operations]
  resource.operations.clear()
  for (const cancel of operations) cancel(reason)
  return true
}

export function openShellChannel(client, options, isLive, connectionClosedError) {
  return new Promise((resolve, reject) => {
    client.shell(options, (error, channel) => {
      if (!isLive()) {
        try { channel?.end() } catch {}
        reject(connectionClosedError())
      } else if (error) {
        try { channel?.end() } catch {}
        reject(error)
      } else {
        resolve(channel)
      }
    })
  })
}
