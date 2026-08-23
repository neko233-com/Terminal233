[CmdletBinding()]
param(
    [ValidateSet('amd64', 'arm64')]
    [string]$Architecture = 'amd64',
    [string]$OutputRoot = ''
)

$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$serverRoot = Join-Path $repositoryRoot 'terminal233-server'
$resolvedOutputRoot = if ([string]::IsNullOrWhiteSpace($OutputRoot)) {
    Join-Path $serverRoot 'target\release\bundle\linux'
} else {
    [IO.Path]::GetFullPath($OutputRoot)
}

$versionSource = Get-Content -LiteralPath (Join-Path $serverRoot 'internal\app\config.go') -Raw
$versionMatch = [regex]::Match($versionSource, 'var Version = "([0-9]+\.[0-9]+\.[0-9]+)"')
if (-not $versionMatch.Success) {
    throw 'Unable to read the server version from terminal233-server/internal/app/config.go.'
}
$version = $versionMatch.Groups[1].Value
if ([string]::IsNullOrWhiteSpace($resolvedOutputRoot) -or $resolvedOutputRoot -eq [IO.Path]::GetPathRoot($resolvedOutputRoot)) {
    throw "Refusing to use a drive root as the package directory: $resolvedOutputRoot"
}

$packageDirectory = Join-Path $resolvedOutputRoot "terminal233-server-$version-linux-$Architecture"
$archivePath = Join-Path $resolvedOutputRoot "terminal233-server-$version-linux-$Architecture.tar.gz"
$binaryPath = Join-Path $packageDirectory 'terminal233-server'
New-Item -ItemType Directory -Force -Path $packageDirectory | Out-Null

$oldGoos = $env:GOOS
$oldGoarch = $env:GOARCH
$oldCgo = $env:CGO_ENABLED
$env:GOOS = 'linux'
$env:GOARCH = $Architecture
$env:CGO_ENABLED = '0'
Push-Location $serverRoot
try {
    & go build -trimpath -ldflags "-s -w -X github.com/neko233-com/Terminal233/terminal233-server/internal/app.Version=$version" -o $binaryPath ./cmd/server
    if ($LASTEXITCODE -ne 0) {
        throw "Linux $Architecture server build failed."
    }
} finally {
    Pop-Location
    if ($null -eq $oldGoos) { Remove-Item Env:GOOS -ErrorAction SilentlyContinue } else { $env:GOOS = $oldGoos }
    if ($null -eq $oldGoarch) { Remove-Item Env:GOARCH -ErrorAction SilentlyContinue } else { $env:GOARCH = $oldGoarch }
    if ($null -eq $oldCgo) { Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue } else { $env:CGO_ENABLED = $oldCgo }
}

Copy-Item -LiteralPath (Join-Path $serverRoot 'README.md') -Destination $packageDirectory -Force
Copy-Item -LiteralPath (Join-Path $serverRoot '.env.example') -Destination $packageDirectory -Force
New-Item -ItemType Directory -Force -Path (Join-Path $packageDirectory 'systemd') | Out-Null
Copy-Item -LiteralPath (Join-Path $serverRoot 'deploy\terminal233-server.service') -Destination (Join-Path $packageDirectory 'systemd') -Force

$tar = Get-Command tar -ErrorAction SilentlyContinue
if ($null -eq $tar) {
    throw 'The tar command is required to create the Linux tar.gz package.'
}
$packageName = Split-Path -Leaf $packageDirectory
& $tar.Source '-czf' $archivePath '-C' $resolvedOutputRoot $packageName
if ($LASTEXITCODE -ne 0) {
    throw "Linux $Architecture server archive failed."
}

Write-Host "Terminal233 Linux server package created: $archivePath"
