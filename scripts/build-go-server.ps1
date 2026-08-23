[CmdletBinding()]
param(
    [string]$OutputPath = ''
)

$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$serverRoot = Join-Path $repositoryRoot 'terminal233-server'
$resolvedOutput = if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    Join-Path $serverRoot 'bin/terminal233-server.exe'
} else {
    [IO.Path]::GetFullPath($OutputPath)
}
$outputDirectory = Split-Path -Parent $resolvedOutput
New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null
Push-Location $serverRoot
try {
    go build -o $resolvedOutput ./cmd/server
    if ($LASTEXITCODE -ne 0) {
        throw 'Go server build failed.'
    }
} finally {
    Pop-Location
}
Write-Host "Built $resolvedOutput"
