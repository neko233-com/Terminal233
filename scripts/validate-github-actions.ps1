[CmdletBinding()]
param(
    [switch]$ForceBootstrap
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$actionlintVersion = '1.7.12'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$toolCache = Join-Path $repositoryRoot '.cache/tools'

function Get-Sha256 {
    param([Parameter(Mandatory)][string]$Path)
    (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Get-Actionlint {
    $globalTool = if (-not $ForceBootstrap) {
        Get-Command actionlint -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    }
    if ($null -ne $globalTool) {
        $version = (& $globalTool.Source -version 2>&1 | Out-String)
        if ($LASTEXITCODE -eq 0 -and $version -match [regex]::Escape("v$actionlintVersion")) {
            return $globalTool.Source
        }
    }

    $archive = Join-Path $toolCache "downloads/actionlint_$($actionlintVersion)_windows_amd64.zip"
    $archiveSha256 = '6e7241b51e6817ea6a047693d8e6fed13b31819c9a0dd6c5a726e1592d22f6e9'
    $binary = Join-Path $toolCache "actionlint/$actionlintVersion/actionlint.exe"
    $binarySha256 = '54ca21be3de4c7cfa26914aa8b61bd76bf573ef3caac5f80d110558cdf241718'

    if (-not (Test-Path -LiteralPath $binary) -or (Get-Sha256 $binary) -ne $binarySha256) {
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $archive), (Split-Path -Parent $binary) | Out-Null
        if (-not (Test-Path -LiteralPath $archive) -or (Get-Sha256 $archive) -ne $archiveSha256) {
            $partial = "$archive.partial.$PID"
            try {
                Invoke-WebRequest -UseBasicParsing -Uri "https://github.com/rhysd/actionlint/releases/download/v$actionlintVersion/actionlint_$($actionlintVersion)_windows_amd64.zip" -OutFile $partial
                if ((Get-Sha256 $partial) -ne $archiveSha256) { throw 'actionlint archive SHA-256 verification failed.' }
                Move-Item -LiteralPath $partial -Destination $archive -Force
            }
            finally {
                if (Test-Path -LiteralPath $partial) { Remove-Item -LiteralPath $partial -Force }
            }
        }
        Expand-Archive -LiteralPath $archive -DestinationPath (Split-Path -Parent $binary) -Force
    }
    if ((Get-Sha256 $binary) -ne $binarySha256) { throw 'actionlint binary SHA-256 verification failed.' }
    return $binary
}

if ($env:OS -ne 'Windows_NT') { throw 'This validator bootstraps the pinned Windows actionlint release.' }
$actionlint = Get-Actionlint
Write-Host (& $actionlint -version | Out-String).Trim()

Push-Location $repositoryRoot
try {
    & $actionlint -color
    if ($LASTEXITCODE -ne 0) { throw "actionlint failed with exit code $LASTEXITCODE." }
}
finally {
    Pop-Location
}

Write-Host 'GitHub Actions validation passed.'
