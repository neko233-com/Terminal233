export class TerminalAdmissionError extends Error {
  constructor(code) {
    super(code)
    this.code = code
  }
}

export class TerminalAdmission {
  #pending = 0

  constructor(maxActive, maxRecords) {
    this.maxActive = maxActive
    this.maxRecords = maxRecords
  }

  acquire(activeCount, recordCount) {
    if (activeCount + this.#pending >= this.maxActive) throw new TerminalAdmissionError('terminal_limit')
    if (recordCount + this.#pending >= this.maxRecords) throw new TerminalAdmissionError('terminal_record_limit')
    this.#pending += 1
    let released = false
    return () => {
      if (released) return
      released = true
      this.#pending -= 1
    }
  }

  get pending() {
    return this.#pending
  }
}
