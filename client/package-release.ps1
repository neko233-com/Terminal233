[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$head = (& git -C $repo rev-parse HEAD).Trim()
if ((& git -C $repo branch --show-current).Trim() -ne 'main' -or (& git -C $repo status --porcelain) -or $head -ne (& git -C $repo rev-parse origin/main).Trim()) { throw 'Release packaging requires clean main matching origin/main.' }
$version = (Get-Content -LiteralPath (Join-Path $PSScriptRoot 'mygo.json') -Raw | ConvertFrom-Json).version
if ($version -notmatch '^\d+\.\d+\.\d+$') { throw 'Invalid application version.' }
[System.Diagnostics.Process]::GetCurrentProcess().PriorityClass = 'BelowNormal'
$env:GOMAXPROCS = '1'
$env:GOFLAGS = '-p=1'
Push-Location $repo
try {
    & task test
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
    & task test:desktop:windows
    if ($LASTEXITCODE -ne 0) { throw 'Desktop test failed.' }
    & task package:windows:local
    if ($LASTEXITCODE -ne 0) { throw 'Windows package failed.' }
    & task build:server:linux-amd64 build:server:linux-arm64
    if ($LASTEXITCODE -ne 0) { throw 'Linux server build failed.' }
} finally { Pop-Location }
$clientExe = Join-Path $repo 'build\release-windows\windows-amd64\Terminal233.exe'
if ((& $clientExe --verify-broadcast | Out-String).Trim() -ne 'broadcast-input: available' -or $LASTEXITCODE -ne 0) { throw 'Packaged broadcast support verification failed.' }
foreach ($target in @(
    @{ Path='build\release-windows\windows-amd64\Terminal233.exe'; OS='windows'; Arch='amd64' },
    @{ Path='build\linux-amd64\terminal233-server'; OS='linux'; Arch='amd64' },
    @{ Path='build\linux-arm64\terminal233-server'; OS='linux'; Arch='arm64' }
)) {
    $metadata = (& go version -m (Join-Path $repo $target.Path) | Out-String)
    if ($LASTEXITCODE -ne 0 -or -not $metadata.Contains("vcs.revision=$head") -or -not $metadata.Contains('vcs.modified=false') -or -not $metadata.Contains("GOOS=$($target.OS)") -or -not $metadata.Contains("GOARCH=$($target.Arch)")) { throw "Binary source or architecture verification failed: $($target.Path)" }
}
$stamp = [DateTime]::UtcNow.ToString('yyyyMMddHHmmssfff')
$output = Join-Path $repo "build\release-$version-$stamp"
New-Item -ItemType Directory -Path $output | Out-Null
$installerName = "terminal233-setup-$version-amd64.exe"
Copy-Item -LiteralPath (Join-Path $repo "build\release-windows\windows-amd64\$installerName") -Destination (Join-Path $output $installerName)
foreach ($arch in @('amd64','arm64')) {
    $archive = Join-Path $output "terminal233-server-$version-linux-$arch.tar.gz"
    $stream = [IO.File]::Create($archive)
    $gzip = [IO.Compression.GZipStream]::new($stream, [IO.Compression.CompressionLevel]::Optimal)
    $writer = [System.Formats.Tar.TarWriter]::new($gzip, $false)
    try {
        foreach ($entry in @(
            @{ Source = "build\linux-$arch\terminal233-server"; Name = 'terminal233-server'; Mode = 493 },
            @{ Source = 'server\README.md'; Name = 'README.md'; Mode = 420 },
            @{ Source = 'server\SECURITY.md'; Name = 'SECURITY.md'; Mode = 420 },
            @{ Source = 'server\deploy\terminal233-sync.service'; Name = 'terminal233-sync.service'; Mode = 420 },
            @{ Source = 'server\deploy\edge-worker.mjs'; Name = 'edge-worker.mjs'; Mode = 420 },
            @{ Source = 'LICENSE'; Name = 'LICENSE'; Mode = 420 }
        )) {
            $file = [IO.File]::OpenRead((Join-Path $repo $entry.Source))
            try {
                $tarEntry = [System.Formats.Tar.PaxTarEntry]::new([System.Formats.Tar.TarEntryType]::RegularFile, $entry.Name)
                $tarEntry.Mode = [IO.UnixFileMode]$entry.Mode
                $tarEntry.DataStream = $file
                $writer.WriteEntry($tarEntry)
            } finally { $file.Dispose() }
        }
    } finally { $writer.Dispose(); $gzip.Dispose(); $stream.Dispose() }
}
$files = @(Get-ChildItem -LiteralPath $output -File | Sort-Object Name)
$manifest = [ordered]@{ version=$version; source_commit=$head; windows_signed=$false; artifacts=@($files | ForEach-Object { [ordered]@{ name=$_.Name; sha256=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant(); bytes=$_.Length } }) }
$manifest | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $output 'manifest.json') -Encoding utf8NoBOM
$files = @(Get-ChildItem -LiteralPath $output -File | Sort-Object Name)
@($files | ForEach-Object { ((Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant() + '  ' + $_.Name) }) | Set-Content -LiteralPath (Join-Path $output 'SHA256SUMS.txt') -Encoding ascii
Write-Output "RELEASE_DIRECTORY=$output"
