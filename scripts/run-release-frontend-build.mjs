import { spawnSync } from 'node:child_process'
import { isAbsolute } from 'node:path'

const npmCli = process.env.NEKOSSH_RELEASE_NPM_CLI?.trim()

if (!npmCli || !isAbsolute(npmCli)) {
  console.error(
    'NEKOSSH_RELEASE_NPM_CLI must contain the absolute path validated by the Windows release packager.',
  )
  process.exitCode = 1
} else {
  const result = spawnSync(process.execPath, [npmCli, 'run', 'build'], {
    env: process.env,
    shell: false,
    stdio: 'inherit',
    windowsHide: true,
  })

  if (result.error) {
    console.error(`Unable to start the pinned npm CLI: ${result.error.message}`)
    process.exitCode = 1
  } else if (result.signal) {
    console.error(`The pinned npm CLI was terminated by signal ${result.signal}.`)
    process.exitCode = 1
  } else {
    process.exitCode = result.status ?? 1
  }
}
