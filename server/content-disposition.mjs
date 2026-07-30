const INVALID_FILE_NAME = /[\0-\x1f\x7f/\\]/

/**
 * Format a safe attachment Content-Disposition value with an ASCII fallback and an RFC 5987
 * UTF-8 filename. Callers still validate the remote basename before reading the file.
 */
export function attachmentContentDisposition(fileName) {
  if (
    typeof fileName !== 'string'
    || fileName.length === 0
    || fileName.length > 255
    || fileName === '.'
    || fileName === '..'
    || INVALID_FILE_NAME.test(fileName)
    || !fileName.isWellFormed()
  ) {
    throw new TypeError('Invalid attachment filename')
  }

  const fallback = [...fileName].map((character) => {
    const codePoint = character.codePointAt(0)
    return codePoint >= 0x20
      && codePoint <= 0x7e
      && character !== '"'
      && character !== '\\'
      ? character
      : '_'
  }).join('') || 'download'
  const encoded = encodeURIComponent(fileName).replace(/[!'()*]/g, (character) => (
    `%${character.charCodeAt(0).toString(16).toUpperCase()}`
  ))
  return `attachment; filename="${fallback}"; filename*=UTF-8''${encoded}`
}
