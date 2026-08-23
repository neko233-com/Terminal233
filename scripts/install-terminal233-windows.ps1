[CmdletBinding()]
param(
    [string]$ExecutablePath = '',
    [string]$FrontendPath = '',
    [string]$InstallRoot = ''
)

$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$resolvedExecutable = if ([string]::IsNullOrWhiteSpace($ExecutablePath)) {
    Join-Path $repositoryRoot 'terminal233\target\release\terminal233.exe'
} else {
    (Resolve-Path -LiteralPath $ExecutablePath).Path
}
$resolvedFrontend = if ([string]::IsNullOrWhiteSpace($FrontendPath)) {
    Join-Path $repositoryRoot 'dist'
} else {
    (Resolve-Path -LiteralPath $FrontendPath).Path
}
$resolvedInstallRoot = if ([string]::IsNullOrWhiteSpace($InstallRoot)) {
    Join-Path $env:LOCALAPPDATA 'Programs\Terminal233'
} else {
    [IO.Path]::GetFullPath($InstallRoot)
}

if (-not (Test-Path -LiteralPath $resolvedExecutable -PathType Leaf)) {
    throw "找不到客户端可执行文件：$resolvedExecutable"
}
if (-not (Test-Path -LiteralPath $resolvedFrontend -PathType Container)) {
    throw "找不到前端构建目录：$resolvedFrontend"
}
if ([string]::IsNullOrWhiteSpace($resolvedInstallRoot) -or $resolvedInstallRoot -eq [IO.Path]::GetPathRoot($resolvedInstallRoot)) {
    throw "拒绝使用盘符根目录作为安装目录：$resolvedInstallRoot"
}

$installedFrontend = Join-Path $resolvedInstallRoot 'dist'
New-Item -ItemType Directory -Force -Path $resolvedInstallRoot, $installedFrontend | Out-Null
Copy-Item -LiteralPath $resolvedExecutable -Destination (Join-Path $resolvedInstallRoot 'terminal233.exe') -Force
Get-ChildItem -LiteralPath $resolvedFrontend -Force | Copy-Item -Destination $installedFrontend -Recurse -Force

$versionOutput = (& (Join-Path $resolvedInstallRoot 'terminal233.exe') --version | Out-String).Trim()
if ($versionOutput -notmatch '^Terminal233 \d+\.\d+\.\d+$') {
    throw "已安装客户端版本输出无效：$versionOutput"
}
Set-Content -LiteralPath (Join-Path $resolvedInstallRoot 'terminal233.version.txt') -Value $versionOutput -Encoding UTF8
Write-Host "Terminal233 已静默安装到 $resolvedInstallRoot"
Write-Host "已安装版本：$versionOutput"
