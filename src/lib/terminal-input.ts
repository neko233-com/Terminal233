const TERMINAL_INPUT_CHUNK_BYTES = 16 * 1024
const TERMINAL_PENDING_INPUT_BYTES = 256 * 1024
const TERMINAL_BACKPRESSURE_RETRIES = 7
const textEncoder = new TextEncoder()

export function splitTerminalInput(data: string, maxBytes = TERMINAL_INPUT_CHUNK_BYTES): string[] {
  if (!data) return []
  if (!Number.isSafeInteger(maxBytes) || maxBytes < 4) throw new Error('终端输入分块大小无效。')
  const chunks: string[] = []
  let current = ''
  let currentBytes = 0
  for (const character of data) {
    const characterBytes = textEncoder.encode(character).byteLength
    if (current && currentBytes + characterBytes > maxBytes) {
      chunks.push(current)
      current = ''
      currentBytes = 0
    }
    current += character
    currentBytes += characterBytes
  }
  if (current) chunks.push(current)
  return chunks
}

type TerminalInputSender = (sessionId: string, data: string) => Promise<void>

export class IdeTerminalInputQueue {
  #closed = false
  #failed = false
  #pendingBytes = 0
  #tail: Promise<void> = Promise.resolve()

  constructor(
    private readonly sessionId: string,
    private readonly onError: (error: unknown) => void,
    private readonly sender: TerminalInputSender,
  ) {}

  enqueue(data: string): boolean {
    if (this.#closed || this.#failed) return false
    if (!data) return true
    const byteLength = textEncoder.encode(data).byteLength
    if (byteLength > TERMINAL_PENDING_INPUT_BYTES - this.#pendingBytes) return false
    const chunks = splitTerminalInput(data)
    this.#pendingBytes += byteLength
    const operation = this.#tail.then(async () => {
      if (this.#closed || this.#failed) return
      for (const chunk of chunks) {
        if (this.#closed || this.#failed) return
        await sendTerminalInputChunk(this.sessionId, chunk, this.sender, () => this.#closed || this.#failed)
      }
    })
    this.#tail = operation
      .catch((error: unknown) => {
        if (!this.#closed && !this.#failed) {
          this.#failed = true
          this.onError(error)
        }
      })
      .finally(() => {
        this.#pendingBytes = Math.max(0, this.#pendingBytes - byteLength)
      })
    return true
  }

  close() {
    this.#closed = true
  }
}

async function sendTerminalInputChunk(
  sessionId: string,
  data: string,
  sender: TerminalInputSender,
  isCancelled: () => boolean,
) {
  for (let attempt = 0; ; attempt += 1) {
    if (isCancelled()) return
    try {
      await sender(sessionId, data)
      return
    } catch (error) {
      if (
        (error as { code?: string } | null)?.code !== 'ssh_terminal_input_backpressure'
        || attempt >= TERMINAL_BACKPRESSURE_RETRIES
      ) {
        throw error
      }
      await new Promise((resolve) => window.setTimeout(resolve, 8 * (2 ** attempt)))
    }
  }
}
