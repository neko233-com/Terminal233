export class OperationAdmissionError extends Error {
  constructor(code) {
    super(code)
    this.code = code
  }
}

export class OperationAdmission {
  #active = 0
  #activeByKey = new Map()
  #reservedBytes = 0

  constructor({ maxActive, maxPerKey, maxReservedBytes = Number.POSITIVE_INFINITY }) {
    if (!Number.isSafeInteger(maxActive) || maxActive < 1) throw new TypeError('maxActive must be a positive integer')
    if (!Number.isSafeInteger(maxPerKey) || maxPerKey < 1 || maxPerKey > maxActive) {
      throw new TypeError('maxPerKey must be a positive integer no greater than maxActive')
    }
    if (maxReservedBytes !== Number.POSITIVE_INFINITY
      && (!Number.isSafeInteger(maxReservedBytes) || maxReservedBytes < 1)) {
      throw new TypeError('maxReservedBytes must be a positive safe integer')
    }
    this.maxActive = maxActive
    this.maxPerKey = maxPerKey
    this.maxReservedBytes = maxReservedBytes
  }

  acquire(key, reservedBytes = 0) {
    if (typeof key !== 'string' || key.length === 0) throw new TypeError('key must be a non-empty string')
    if (!Number.isSafeInteger(reservedBytes) || reservedBytes < 0) throw new TypeError('reservedBytes must be a non-negative safe integer')
    if (this.#active >= this.maxActive) throw new OperationAdmissionError('operation_global_limit')
    if ((this.#activeByKey.get(key) ?? 0) >= this.maxPerKey) {
      throw new OperationAdmissionError('operation_connection_limit')
    }
    if (this.#reservedBytes + reservedBytes > this.maxReservedBytes) {
      throw new OperationAdmissionError('operation_memory_limit')
    }

    this.#active += 1
    this.#activeByKey.set(key, (this.#activeByKey.get(key) ?? 0) + 1)
    this.#reservedBytes += reservedBytes

    let released = false
    return () => {
      if (released) return
      released = true
      this.#active -= 1
      this.#reservedBytes -= reservedBytes
      const remaining = (this.#activeByKey.get(key) ?? 1) - 1
      if (remaining === 0) this.#activeByKey.delete(key)
      else this.#activeByKey.set(key, remaining)
    }
  }

  get active() {
    return this.#active
  }

  get reservedBytes() {
    return this.#reservedBytes
  }

  activeFor(key) {
    return this.#activeByKey.get(key) ?? 0
  }
}
