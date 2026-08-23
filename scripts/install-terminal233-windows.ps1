[CmdletBinding()]
param(
    [string]$PackageRoot = '',
    [string]$ExecutablePath = '',
    [string]$FrontendPath = '',
    [string]$InstallRoot = ''
)

$ErrorActionPreference = 'Stop'
$scriptRoot = (Resolve-Path $PSScriptRoot).Path
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$sourceRoot = if (-not [string]::IsNullOrWhiteSpace($PackageRoot)) {
    (Resolve-Path -LiteralPath $PackageRoot).Path
} elseif (Test-Path -LiteralPath (Join-Path $scriptRoot 'Terminal233.exe') -PathType Leaf) {
    $scriptRoot
} else {
    $repositoryRoot
}
$resolvedExecutable = if ([string]::IsNullOrWhiteSpace($ExecutablePath)) {
    $packageExecutable = Join-Path $sourceRoot 'Terminal233.exe'
    if (Test-Path -LiteralPath $packageExecutable -PathType Leaf) {
        (Resolve-Path -LiteralPath $packageExecutable).Path
    } else {
        (Resolve-Path -LiteralPath (Join-Path $sourceRoot 'terminal233\target\release\terminal233.exe')).Path
    }
} else {
    (Resolve-Path -LiteralPath $ExecutablePath).Path
}
$resolvedFrontend = if ([string]::IsNullOrWhiteSpace($FrontendPath)) {
    (Resolve-Path -LiteralPath (Join-Path $sourceRoot 'dist')).Path
} else {
    (Resolve-Path -LiteralPath $FrontendPath).Path
}
$resolvedInstallRoot = if ([string]::IsNullOrWhiteSpace($InstallRoot)) {
    'C:\App\Terminal233'
} else {
    [IO.Path]::GetFullPath($InstallRoot)
}

if (-not (Test-Path -LiteralPath $resolvedExecutable -PathType Leaf)) {
    throw "Client executable was not found: $resolvedExecutable"
}
if (-not (Test-Path -LiteralPath $resolvedFrontend -PathType Container)) {
    throw "Frontend build directory was not found: $resolvedFrontend"
}
if ([string]::IsNullOrWhiteSpace($resolvedInstallRoot) -or $resolvedInstallRoot -eq [IO.Path]::GetPathRoot($resolvedInstallRoot)) {
    throw "Refusing to use a drive root as the install directory: $resolvedInstallRoot"
}

$installedFrontend = Join-Path $resolvedInstallRoot 'dist'
New-Item -ItemType Directory -Force -Path $resolvedInstallRoot, $installedFrontend | Out-Null
Copy-Item -LiteralPath $resolvedExecutable -Destination (Join-Path $resolvedInstallRoot 'Terminal233.exe') -Force
Get-ChildItem -LiteralPath $resolvedFrontend -Force | Copy-Item -Destination $installedFrontend -Recurse -Force

$versionOutput = (& (Join-Path $resolvedInstallRoot 'Terminal233.exe') --version | Out-String).Trim()
if ($versionOutput -notmatch '^Terminal233 \d+\.\d+\.\d+$') {
    throw "Invalid installed client version output: $versionOutput"
}
Set-Content -LiteralPath (Join-Path $resolvedInstallRoot 'Terminal233.version.txt') -Value $versionOutput -Encoding UTF8
Write-Host "Terminal233 was silently installed to $resolvedInstallRoot"
Write-Host "Installed version: $versionOutput"
