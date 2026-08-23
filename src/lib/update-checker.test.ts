import { describe, expect, it } from 'vitest'
import { compareVersions, parseUpdateManifest } from './update-checker'

describe('Terminal233 update manifest', () => {
  it('compares stable versions before prereleases', () => {
    expect(compareVersions('0.3.0', '0.2.0')).toBe(1)
    expect(compareVersions('0.2.0', '0.2.0')).toBe(0)
    expect(compareVersions('0.2.0-beta.1', '0.2.0')).toBe(-1)
  })

  it('accepts only a shaped HTTPS release manifest', () => {
    const manifest = parseUpdateManifest({
      schemaVersion: 1,
      product: 'Terminal233',
      version: '0.3.0',
      tag: 'v0.3.0',
      channel: 'stable',
      generatedAtUtc: '2026-08-23T00:00:00Z',
      releaseUrl: 'https://github.com/neko233-com/Terminal233/releases/tag/v0.3.0',
      updateManifestUrl: 'https://github.com/neko233-com/Terminal233/releases/latest/download/terminal233-update.json',
      assets: [{
        component: 'desktop',
        platform: 'windows',
        architecture: 'x86_64',
        name: 'Terminal233-0.3.0-windows-x86_64.zip',
        bytes: 123,
        sha256: 'a'.repeat(64),
        url: 'https://github.com/neko233-com/Terminal233/releases/download/v0.3.0/Terminal233-0.3.0-windows-x86_64.zip',
      }],
    })
    expect(manifest.assets[0]?.sha256).toHaveLength(64)
    expect(() => parseUpdateManifest({ ...manifest, releaseUrl: 'http://example.com/releases' })).toThrow('元数据无效')
  })
})
