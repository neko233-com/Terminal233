[CmdletBinding()]
param(
    [string]$OutputRoot = ''
)

$ErrorActionPreference = 'Stop'
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$cargoText = Get-Content -LiteralPath (Join-Path $repositoryRoot 'terminal233\Cargo.toml') -Raw
$versionMatch = [regex]::Match($cargoText, '(?ms)^\[package\].*?^version\s*=\s*"([0-9]+\.[0-9]+\.[0-9]+)"')
if (-not $versionMatch.Success) {
    throw 'Unable to read the client version from terminal233/Cargo.toml.'
}
$version = $versionMatch.Groups[1].Value
$resolvedOutputRoot = if ([string]::IsNullOrWhiteSpace($OutputRoot)) {
    Join-Path $repositoryRoot 'terminal233\target\release\bundle\release'
} else {
    [IO.Path]::GetFullPath($OutputRoot)
}
$releaseDirectory = Join-Path $resolvedOutputRoot "Terminal233-$version"
$desktopX64WorkRoot = Join-Path $resolvedOutputRoot '.desktop-x86_64'
$desktopArm64WorkRoot = Join-Path $resolvedOutputRoot '.desktop-arm64'
$serverAmd64WorkRoot = Join-Path $resolvedOutputRoot '.server-amd64'
$serverArm64WorkRoot = Join-Path $resolvedOutputRoot '.server-arm64'
New-Item -ItemType Directory -Force -Path $releaseDirectory, $desktopX64WorkRoot, $desktopArm64WorkRoot, $serverAmd64WorkRoot, $serverArm64WorkRoot | Out-Null

function Invoke-CheckedCommand {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$ArgumentList
    )
    & $FilePath @ArgumentList
    if ($LASTEXITCODE -ne 0) {
        throw "Command failed ($LASTEXITCODE): $FilePath $($ArgumentList -join ' ')"
    }
}

function Copy-SingleArtifact {
    param(
        [Parameter(Mandatory)][string]$Directory,
        [Parameter(Mandatory)][string]$Pattern
    )
    $matches = @(Get-ChildItem -LiteralPath $Directory -Filter $Pattern -File)
    if ($matches.Count -ne 1) {
        throw "Expected one $Pattern in $Directory, found $($matches.Count)."
    }
    Copy-Item -LiteralPath $matches[0].FullName -Destination $releaseDirectory -Force
    return Join-Path $releaseDirectory $matches[0].Name
}

Invoke-CheckedCommand -FilePath 'corepack' -ArgumentList @('npm', 'run', 'build')
Invoke-CheckedCommand -FilePath 'cargo' -ArgumentList @('build', '--manifest-path', 'terminal233/Cargo.toml', '--release', '--locked')

$nativeExecutable = Join-Path $repositoryRoot 'terminal233\target\release\terminal233.exe'
& powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'package-terminal233-windows.ps1') `
    -Target 'x86_64-pc-windows-msvc' -ExecutablePath $nativeExecutable -OutputRoot $desktopX64WorkRoot
if ($LASTEXITCODE -ne 0) { throw 'Windows x86_64 desktop package failed.' }
$x64Archive = Copy-SingleArtifact -Directory $desktopX64WorkRoot -Pattern '*.zip'

Invoke-CheckedCommand -FilePath 'rustup' -ArgumentList @('target', 'add', 'aarch64-pc-windows-msvc')
Invoke-CheckedCommand -FilePath 'cargo' -ArgumentList @('build', '--manifest-path', 'terminal233/Cargo.toml', '--target', 'aarch64-pc-windows-msvc', '--release', '--locked')
$armExecutable = Join-Path $repositoryRoot 'terminal233\target\aarch64-pc-windows-msvc\release\terminal233.exe'
& powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'package-terminal233-windows.ps1') `
    -Target 'aarch64-pc-windows-msvc' -ExecutablePath $armExecutable -OutputRoot $desktopArm64WorkRoot
if ($LASTEXITCODE -ne 0) { throw 'Windows ARM64 desktop package failed.' }
$armArchive = Copy-SingleArtifact -Directory $desktopArm64WorkRoot -Pattern '*.zip'

& powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'package-terminal233-server-linux.ps1') `
    -Architecture 'amd64' -OutputRoot $serverAmd64WorkRoot
if ($LASTEXITCODE -ne 0) { throw 'Linux amd64 server package failed.' }
$linuxAmd64Archive = Copy-SingleArtifact -Directory $serverAmd64WorkRoot -Pattern '*.tar.gz'

& powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'package-terminal233-server-linux.ps1') `
    -Architecture 'arm64' -OutputRoot $serverArm64WorkRoot
if ($LASTEXITCODE -ne 0) { throw 'Linux arm64 server package failed.' }
$linuxArm64Archive = Copy-SingleArtifact -Directory $serverArm64WorkRoot -Pattern '*.tar.gz'

$assetFiles = @($x64Archive, $armArchive, $linuxAmd64Archive, $linuxArm64Archive)
$assetRecords = @(
    [ordered]@{ component = 'desktop'; platform = 'windows'; architecture = 'x86_64'; name = (Split-Path -Leaf $x64Archive); bytes = (Get-Item $x64Archive).Length; sha256 = (Get-FileHash -LiteralPath $x64Archive -Algorithm SHA256).Hash.ToLowerInvariant(); url = "https://github.com/neko233-com/Terminal233/releases/download/v$version/$(Split-Path -Leaf $x64Archive)" },
    [ordered]@{ component = 'desktop'; platform = 'windows'; architecture = 'arm64'; name = (Split-Path -Leaf $armArchive); bytes = (Get-Item $armArchive).Length; sha256 = (Get-FileHash -LiteralPath $armArchive -Algorithm SHA256).Hash.ToLowerInvariant(); url = "https://github.com/neko233-com/Terminal233/releases/download/v$version/$(Split-Path -Leaf $armArchive)" },
    [ordered]@{ component = 'server'; platform = 'linux'; architecture = 'amd64'; name = (Split-Path -Leaf $linuxAmd64Archive); bytes = (Get-Item $linuxAmd64Archive).Length; sha256 = (Get-FileHash -LiteralPath $linuxAmd64Archive -Algorithm SHA256).Hash.ToLowerInvariant(); url = "https://github.com/neko233-com/Terminal233/releases/download/v$version/$(Split-Path -Leaf $linuxAmd64Archive)" },
    [ordered]@{ component = 'server'; platform = 'linux'; architecture = 'arm64'; name = (Split-Path -Leaf $linuxArm64Archive); bytes = (Get-Item $linuxArm64Archive).Length; sha256 = (Get-FileHash -LiteralPath $linuxArm64Archive -Algorithm SHA256).Hash.ToLowerInvariant(); url = "https://github.com/neko233-com/Terminal233/releases/download/v$version/$(Split-Path -Leaf $linuxArm64Archive)" }
)

$checksumLines = foreach ($asset in $assetRecords | Sort-Object name) {
    "$($asset.sha256)  $($asset.name)"
}
Set-Content -LiteralPath (Join-Path $releaseDirectory 'SHA256SUMS.txt') -Value $checksumLines -Encoding UTF8
$notes = @(
    "# Terminal233 v$version",
    '',
    '## Release contents',
    '',
    '- Windows desktop: x86_64, ARM64',
    '- Linux sync server: amd64, arm64',
    '- Default client install root: C:\App\Terminal233',
    '',
    'SHA-256 checksums for all binary assets are in SHA256SUMS.txt.'
)
Set-Content -LiteralPath (Join-Path $releaseDirectory 'RELEASE_NOTES.md') -Value $notes -Encoding UTF8

$manifest = [ordered]@{
    schemaVersion = 1
    product = 'Terminal233'
    version = $version
    tag = "v$version"
    channel = 'stable'
    generatedAtUtc = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
    releaseUrl = "https://github.com/neko233-com/Terminal233/releases/tag/v$version"
    updateManifestUrl = 'https://github.com/neko233-com/Terminal233/releases/latest/download/terminal233-update.json'
    assets = $assetRecords
}
$manifestJson = $manifest | ConvertTo-Json -Depth 6
Set-Content -LiteralPath (Join-Path $releaseDirectory 'release-manifest.json') -Value $manifestJson -Encoding UTF8
Set-Content -LiteralPath (Join-Path $releaseDirectory 'terminal233-update.json') -Value $manifestJson -Encoding UTF8

Write-Host "Terminal233 multi-platform release package created: $releaseDirectory"
Write-Host 'Desktop: Windows x86_64, ARM64'
Write-Host 'Server: Linux amd64, arm64'
