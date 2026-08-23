[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Push-Location (Join-Path $repositoryRoot 'terminal233-server')
try {
    go test ./...
    if ($LASTEXITCODE -ne 0) {
        throw 'Go server tests failed.'
    }
} finally {
    Pop-Location
}
