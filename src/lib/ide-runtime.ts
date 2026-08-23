// The native desktop build resolves this module to ide.ts. The browser IDE build resolves it to
// ide-browser.ts in vite.config.ts, keeping SSH runtimes separate while sharing the UI component.
export * from './ide'
