import { spawnSync } from 'node:child_process'
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'

const repositoryRoot = resolve(import.meta.dirname, '..')
const compareOrdinal = (left, right) => left < right ? -1 : left > right ? 1 : 0
const destination = process.argv[2]
if (!destination) throw new Error('Usage: node scripts/generate-third-party-notices.mjs <output-file>')

const sections = []
const packageLock = JSON.parse(readFileSync(join(repositoryRoot, 'package-lock.json'), 'utf8'))
for (const packagePath of Object.keys(packageLock.packages).sort()) {
  const lockRecord = packageLock.packages[packagePath]
  if (!packagePath.startsWith('node_modules/') || lockRecord.dev === true) continue
  const directory = join(repositoryRoot, packagePath)
  const packageJsonPath = join(directory, 'package.json')
  if (!existsSync(packageJsonPath)) continue
  const packageJson = JSON.parse(readFileSync(packageJsonPath, 'utf8'))
  sections.push(packageSection('npm', packageJson.name, packageJson.version, packageJson.license ?? packageJson.licenses, directory))
}

const cargo = spawnSync('cargo', [
  'metadata',
  '--locked',
  '--format-version',
  '1',
  '--filter-platform',
  'x86_64-pc-windows-msvc',
  '--manifest-path',
  join(repositoryRoot, 'src-tauri', 'Cargo.toml'),
], { cwd: repositoryRoot, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, windowsHide: true })
if (cargo.error || cargo.status !== 0) {
  throw new Error(`cargo metadata failed: ${cargo.error?.message ?? cargo.stderr ?? cargo.stdout}`)
}
const metadata = JSON.parse(cargo.stdout)
const resolvedCargoPackages = new Set(metadata.resolve.nodes.map((node) => node.id))
for (const packageRecord of metadata.packages
  .filter((record) => record.source !== null && resolvedCargoPackages.has(record.id))
  .sort((left, right) => compareOrdinal(`${left.name}@${left.version}`, `${right.name}@${right.version}`))) {
  sections.push(packageSection(
    'Cargo',
    packageRecord.name,
    packageRecord.version,
    packageRecord.license,
    dirname(packageRecord.manifest_path),
  ))
}

const uniqueSections = new Map()
for (const section of sections) uniqueSections.set(section.heading, section)
const output = [
  'NEKOSSH THIRD-PARTY NOTICES',
  '',
  'This file is generated from the exact npm and Cargo dependency trees used for the Windows build.',
  'It includes license expressions and the license/notice texts shipped by each dependency.',
  '',
  ...[...uniqueSections.values()]
    .sort((left, right) => compareOrdinal(left.heading, right.heading))
    .flatMap(({ heading, body }) => [`${'='.repeat(80)}`, heading, `${'-'.repeat(80)}`, body, '']),
].join('\n')
writeFileSync(resolve(repositoryRoot, destination), `${output}\n`, 'utf8')

function packageSection(ecosystem, name, version, license, directory) {
  const heading = `${ecosystem}: ${name}@${version}`
  const expressions = Array.isArray(license)
    ? license.map((item) => typeof item === 'string' ? item : item?.type).filter(Boolean).join(' OR ')
    : typeof license === 'object' ? license?.type : license
  const licenseFiles = readdirSync(directory, { withFileTypes: true })
    .filter((entry) => entry.isFile() && /^(?:licen[cs]e|copying|notice)(?:[._-].*)?$/i.test(entry.name))
    .map((entry) => entry.name)
    .sort()
  const texts = licenseFiles.map((file) => {
    const value = readFileSync(join(directory, file), 'utf8').replace(/\r\n/g, '\n').trim()
    return `[${file}]\n${value}`
  })
  if (texts.length === 0) texts.push('[No standalone license file was present in the installed package.]')
  return {
    heading,
    body: [`Declared license: ${normalizeLicense(expressions)}`, '', ...texts].join('\n'),
  }
}

function normalizeLicense(value) {
  if (typeof value !== 'string' || value.trim() === '') return 'Not declared'
  return value.replace(/\s+/g, ' ').trim()
}
