[CmdletBinding()]
param(
    [ValidateSet('x86_64-pc-windows-msvc', 'aarch64-pc-windows-msvc')]
    [string]$Target = 'x86_64-pc-windows-msvc',
    [string]$ExecutablePath = '',
    [string]$FrontendPath = '',
    [string]$OutputRoot = ''
)

$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if ($Target -eq 'aarch64-pc-windows-msvc') {
    throw 'ARM64 desktop packaging is disabled in the local workflow; enable it only from an explicit publish CI workflow.'
}
$architecture = if ($Target -eq 'aarch64-pc-windows-msvc') { 'arm64' } else { 'x86_64' }
$defaultExecutable = if ($Target -eq 'x86_64-pc-windows-msvc') {
    $nativePath = Join-Path $repositoryRoot 'terminal233\target\release\terminal233.exe'
    if (Test-Path -LiteralPath $nativePath -PathType Leaf) {
        $nativePath
    } else {
        Join-Path $repositoryRoot "terminal233\target\$Target\release\terminal233.exe"
    }
} else {
    Join-Path $repositoryRoot "terminal233\target\$Target\release\terminal233.exe"
}
$resolvedExecutable = if ([string]::IsNullOrWhiteSpace($ExecutablePath)) {
    (Resolve-Path -LiteralPath $defaultExecutable).Path
} else {
    (Resolve-Path -LiteralPath $ExecutablePath).Path
}
$resolvedFrontend = if ([string]::IsNullOrWhiteSpace($FrontendPath)) {
    (Resolve-Path -LiteralPath (Join-Path $repositoryRoot 'dist')).Path
} else {
    (Resolve-Path -LiteralPath $FrontendPath).Path
}
$resolvedOutputRoot = if ([string]::IsNullOrWhiteSpace($OutputRoot)) {
    Join-Path $repositoryRoot 'terminal233\target\release\bundle\windows'
} else {
    [IO.Path]::GetFullPath($OutputRoot)
}

if (-not (Test-Path -LiteralPath $resolvedExecutable -PathType Leaf)) {
    throw "Client executable was not found: $resolvedExecutable"
}
if (-not (Test-Path -LiteralPath $resolvedFrontend -PathType Container)) {
    throw "Frontend build directory was not found: $resolvedFrontend"
}
if ([string]::IsNullOrWhiteSpace($resolvedOutputRoot) -or $resolvedOutputRoot -eq [IO.Path]::GetPathRoot($resolvedOutputRoot)) {
    throw "Refusing to use a drive root as the package directory: $resolvedOutputRoot"
}

$cargoManifest = Join-Path $repositoryRoot 'terminal233\Cargo.toml'
$versionLine = Get-Content -LiteralPath $cargoManifest |
    Select-String -Pattern '^\s*version\s*=\s*"(\d+\.\d+\.\d+)"' |
    Select-Object -First 1
if ($null -eq $versionLine) {
    throw "Could not read the client version from Cargo.toml: $cargoManifest"
}
$version = $versionLine.Matches[0].Groups[1].Value

if ($Target -eq 'x86_64-pc-windows-msvc') {
    $versionOutput = (& $resolvedExecutable --version | Out-String).Trim()
    if ($versionOutput -notmatch '^Terminal233 (\d+\.\d+\.\d+)$') {
        throw "Invalid client version output: $versionOutput"
    }
    if ($Matches[1] -ne $version) {
        throw "Executable version does not match Cargo.toml: $versionOutput / $version"
    }
} else {
    Write-Host 'Skipping the ARM64 executable runtime check on this host architecture.'
}
$packageDirectory = Join-Path $resolvedOutputRoot "Terminal233-$version-windows-$architecture"
$zipPath = "$packageDirectory.zip"
$packageFrontend = Join-Path $packageDirectory 'dist'

New-Item -ItemType Directory -Force -Path $packageDirectory, $packageFrontend | Out-Null
Copy-Item -LiteralPath $resolvedExecutable -Destination (Join-Path $packageDirectory 'Terminal233.exe') -Force
Get-ChildItem -LiteralPath $resolvedFrontend -Force | Copy-Item -Destination $packageFrontend -Recurse -Force
foreach ($scriptName in @('install-terminal233-windows.ps1', 'uninstall-terminal233-windows.ps1')) {
    Copy-Item -LiteralPath (Join-Path $repositoryRoot "scripts\$scriptName") -Destination $packageDirectory -Force
}
foreach ($document in @('README.md', 'LICENSE')) {
    $documentPath = Join-Path $repositoryRoot $document
    if (Test-Path -LiteralPath $documentPath -PathType Leaf) {
        Copy-Item -LiteralPath $documentPath -Destination $packageDirectory -Force
    }
}

$manifest = [ordered]@{
    product = 'Terminal233'
    version = $version
    platform = "windows-$architecture"
    target = $Target
    architecture = $architecture
    renderer = 'rdesktop-webview'
    executable = 'Terminal233.exe'
    frontend = 'dist'
    defaultInstallRoot = 'C:\App\Terminal233'
    webView2 = 'system-runtime'
}
$manifest | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath (Join-Path $packageDirectory 'terminal233.manifest.json') -Encoding UTF8
Compress-Archive -Path (Join-Path $packageDirectory '*') -DestinationPath $zipPath -Force

Write-Host "Terminal233 Windows desktop package created: $packageDirectory"
Write-Host "ZIP: $zipPath"
