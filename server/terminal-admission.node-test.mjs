import assert from 'node:assert/strict'
import test from 'node:test'
import { TerminalAdmission, TerminalAdmissionError } from './terminal-admission.mjs'

test('concurrent starts reserve active PTY slots before awaiting', () => {
  const admission = new TerminalAdmission(12, 24)
  const releases = Array.from({ length: 12 }, () => admission.acquire(0, 0))

  assert.equal(admission.pending, 12)
  assert.throws(() => admission.acquire(0, 0), (error) => error instanceof TerminalAdmissionError && error.code === 'terminal_limit')

  releases.forEach((release) => release())
  releases.forEach((release) => release())
  assert.equal(admission.pending, 0)
})

test('pending starts also reserve retained record capacity', () => {
  const admission = new TerminalAdmission(12, 24)
  const releases = Array.from({ length: 4 }, () => admission.acquire(0, 20))

  assert.throws(() => admission.acquire(0, 20), (error) => error instanceof TerminalAdmissionError && error.code === 'terminal_record_limit')
  releases.forEach((release) => release())
  assert.equal(admission.pending, 0)
})
