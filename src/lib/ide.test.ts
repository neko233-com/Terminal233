import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const tauri = vi.hoisted(() => ({
  invoke: vi.fn(),
  channels: [] as Array<{ onmessage: (event: unknown) => void }>,
  listen: vi.fn(),
}))

vi.mock('@tauri-apps/api/core', () => ({
  invoke: tauri.invoke,
  Channel: class<T> {
    onmessage: (event: T) => void

    constructor(onmessage: (event: T) => void) {
      this.onmessage = onmessage
      tauri.channels.push(this as { onmessage: (event: unknown) => void })
    }
  },
}))

vi.mock('@tauri-apps/api/event', () => ({
  listen: tauri.listen,
}))

import {
  IdeTerminalInputQueue,
  decodeBase64Bytes,
  ideError,
  splitTerminalInput,
  startIdeTerminal,
  uploadIdeFiles,
} from './ide'

describe('Tauri IDE terminal bridge', () => {
  beforeEach(() => {
    tauri.invoke.mockReset()
    tauri.listen.mockReset()
    tauri.channels.length = 0
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('preserves arbitrary PTY bytes instead of replacing invalid UTF-8', () => {
    expect([...decodeBase64Bytes('AP/DKA==')]).toEqual([0x00, 0xff, 0xc3, 0x28])
  })

  it('normalizes Tauri, JavaScript, and string errors without hiding useful feedback', () => {
    expect(ideError({ code: 'profile_not_found', message: '连接已过期。' })).toBe('连接已过期。')
    expect(ideError(new Error('网络不可用。'))).toBe('网络不可用。')
    expect(ideError('已取消。')).toBe('已取消。')
    expect(ideError(null)).toBe('操作失败，请检查连接参数和服务器状态。')
  })

  it('splits large Unicode paste input on code-point boundaries within the Rust byte limit', () => {
    const chunks = splitTerminalInput(`${'a'.repeat(16_380)}🐈${'b'.repeat(20)}`)

    expect(chunks.length).toBe(2)
    expect(chunks.join('')).toBe(`${'a'.repeat(16_380)}🐈${'b'.repeat(20)}`)
    expect(chunks.every((chunk) => new TextEncoder().encode(chunk).byteLength <= 16 * 1024)).toBe(true)
  })

  it('installs a session-owned channel before invoking terminal start', async () => {
    const onEvent = vi.fn()
    tauri.invoke.mockImplementation((command: string, arguments_: Record<string, unknown>) => {
      expect(command).toBe('ide_terminal_start')
      expect(arguments_.events).toBe(tauri.channels[0])
      tauri.channels[0].onmessage({
        kind: 'output',
        sessionId: 'session-1',
        sequence: 1,
        dataBase64: 'aGk=',
      })
      return Promise.resolve({ sessionId: 'session-1' })
    })

    await expect(startIdeTerminal('connection-1', onEvent, 120, 36)).resolves.toEqual({
      sessionId: 'session-1',
    })
    expect(onEvent).toHaveBeenCalledWith(expect.objectContaining({
      kind: 'output',
      sequence: 1,
    }))
  })

  it('installs an upload-owned channel before invoke and preserves fast completion', async () => {
    const received: unknown[] = []
    const file = {
      name: 'fast.txt',
      size: 2,
      arrayBuffer: () => Promise.resolve(new Uint8Array([0x68, 0x69]).buffer),
    } as File
    const started = {
      jobId: 'upload-1',
      direction: 'upload',
      totalFiles: 1,
      totalBytes: 2,
      items: [{ fileId: 'file-1', name: file.name, size: 2 }],
    }
    tauri.invoke.mockImplementation((command: string, arguments_: Record<string, unknown>) => {
      expect(command).toBe('ide_sftp_upload_start')
      expect(arguments_.events).toBe(tauri.channels[0])
      expect(arguments_.files).toEqual([{
        name: 'fast.txt',
        dataBase64: 'aGk=',
        overwrite: false,
      }])
      tauri.channels[0].onmessage({ kind: 'started', ...started })
      tauri.channels[0].onmessage({
        kind: 'completed',
        jobId: started.jobId,
        direction: 'upload',
        status: 'completed',
        completedFiles: 1,
        failedFiles: 0,
        cancelledFiles: 0,
        bytesTransferred: 2,
        totalBytes: 2,
      })
      return Promise.resolve(started)
    })

    await expect(uploadIdeFiles('connection-1', '/', [file], (event) => received.push(event)))
      .resolves.toEqual(started)
    expect(received).toEqual([
      expect.objectContaining({ kind: 'started', jobId: 'upload-1' }),
      expect.objectContaining({ kind: 'completed', jobId: 'upload-1' }),
    ])
  })

  it('serializes input and retries bounded Rust queue backpressure without reordering', async () => {
    vi.useFakeTimers()
    const sender = vi.fn()
      .mockRejectedValueOnce({ code: 'ssh_terminal_input_backpressure' })
      .mockResolvedValue(undefined)
    const onError = vi.fn()
    const queue = new IdeTerminalInputQueue('session-1', onError, sender)

    expect(queue.enqueue('first')).toBe(true)
    expect(queue.enqueue('second')).toBe(true)
    await vi.runAllTimersAsync()

    expect(sender.mock.calls.map((call) => call[1])).toEqual(['first', 'first', 'second'])
    expect(onError).not.toHaveBeenCalled()
    queue.close()
  })

  it('rejects an oversized pending paste before any partial input is sent', async () => {
    const sender = vi.fn().mockResolvedValue(undefined)
    const queue = new IdeTerminalInputQueue('session-1', vi.fn(), sender)

    expect(queue.enqueue('x'.repeat(256 * 1024 + 1))).toBe(false)
    await Promise.resolve()
    expect(sender).not.toHaveBeenCalled()
  })

  it('stops queued backpressure retries after the terminal closes', async () => {
    vi.useFakeTimers()
    const sender = vi.fn().mockRejectedValue({ code: 'ssh_terminal_input_backpressure' })
    const queue = new IdeTerminalInputQueue('session-1', vi.fn(), sender)

    expect(queue.enqueue('pending')).toBe(true)
    await Promise.resolve()
    queue.close()
    await vi.runAllTimersAsync()

    expect(sender).toHaveBeenCalledTimes(1)
  })
})
