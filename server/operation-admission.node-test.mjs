import assert from 'node:assert/strict'
import test from 'node:test'
import { OperationAdmission, OperationAdmissionError } from './operation-admission.mjs'

test('operation admission rejects immediately at global and per-key limits', () => {
  const admission = new OperationAdmission({ maxActive: 3, maxPerKey: 2 })
  const releaseA1 = admission.acquire('connection-a')
  const releaseA2 = admission.acquire('connection-a')

  assert.throws(
    () => admission.acquire('connection-a'),
    (error) => error instanceof OperationAdmissionError && error.code === 'operation_connection_limit',
  )

  const releaseB = admission.acquire('connection-b')
  assert.throws(
    () => admission.acquire('connection-c'),
    (error) => error instanceof OperationAdmissionError && error.code === 'operation_global_limit',
  )
  assert.equal(admission.active, 3)
  assert.equal(admission.activeFor('connection-a'), 2)

  releaseA1()
  releaseA1()
  releaseA2()
  releaseB()
  assert.equal(admission.active, 0)
  assert.equal(admission.activeFor('connection-a'), 0)
})

test('weighted reservations enforce and release the in-flight byte budget', () => {
  const admission = new OperationAdmission({
    maxActive: 3,
    maxPerKey: 1,
    maxReservedBytes: 128,
  })
  const releaseA = admission.acquire('connection-a', 64)
  const releaseB = admission.acquire('connection-b', 64)

  assert.equal(admission.reservedBytes, 128)
  assert.throws(
    () => admission.acquire('connection-c', 1),
    (error) => error instanceof OperationAdmissionError && error.code === 'operation_memory_limit',
  )

  releaseA()
  assert.equal(admission.reservedBytes, 64)
  const releaseC = admission.acquire('connection-c', 32)
  assert.equal(admission.reservedBytes, 96)

  releaseB()
  releaseC()
  assert.equal(admission.reservedBytes, 0)
})

test('invalid limits and reservations fail without changing counters', () => {
  assert.throws(
    () => new OperationAdmission({ maxActive: 1, maxPerKey: 2 }),
    /maxPerKey/,
  )
  const admission = new OperationAdmission({ maxActive: 2, maxPerKey: 1, maxReservedBytes: 64 })
  assert.throws(() => admission.acquire('connection-a', -1), /reservedBytes/)
  assert.equal(admission.active, 0)
  assert.equal(admission.reservedBytes, 0)
})

test('shared outbound reservations enforce a 64 MiB aggregate budget', () => {
  const mebibyte = 1024 * 1024
  const admission = new OperationAdmission({
    maxActive: 64,
    maxPerKey: 64,
    maxReservedBytes: 64 * mebibyte,
  })
  const releases = Array.from({ length: 63 }, () => admission.acquire('http', mebibyte))

  assert.throws(
    () => admission.acquire('http', 2 * mebibyte),
    (error) => error instanceof OperationAdmissionError && error.code === 'operation_memory_limit',
  )
  assert.equal(admission.reservedBytes, 63 * mebibyte)

  releases.forEach((release) => release())
  assert.equal(admission.active, 0)
  assert.equal(admission.reservedBytes, 0)
})
