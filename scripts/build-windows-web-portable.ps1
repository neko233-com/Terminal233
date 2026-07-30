[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$WorkRoot,
    [Parameter(Mandatory)][string]$ArchivePath,
    [Parameter(Mandatory)][string]$Version,
    [Parameter(Mandatory)][string]$Commit,
    [Parameter(Mandatory)][string]$ThirdPartyNoticesPath,
    [Parameter(Mandatory)][string]$NodeExecutable,
    [Parameter(Mandatory)][string]$CargoExecutable,
    [Parameter(Mandatory)][string]$RustcExecutable,
    [Parameter(Mandatory)][string]$GitExecutable,
    [string]$SourceRoot,
    [string]$WorkspaceRoot,
    [string]$SignToolPath,
    [string]$ExpectedSignerThumbprint,
    [string]$ExpectedSignerSubject,
    [string]$ExpectedSignerSpkiSha256,
    [string]$TimestampUrl,
    [switch]$AllowUnsigned
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$defaultSourceRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$repositoryRoot = if ([string]::IsNullOrWhiteSpace($WorkspaceRoot)) {
    $defaultSourceRoot
} else {
    (Resolve-Path -LiteralPath $WorkspaceRoot).Path
}
$resolvedSourceRoot = if ([string]::IsNullOrWhiteSpace($SourceRoot)) {
    $defaultSourceRoot
} else {
    (Resolve-Path -LiteralPath $SourceRoot).Path
}
$outputRoot = [IO.Path]::GetFullPath((Join-Path $repositoryRoot 'output'))
$resolvedWorkRoot = [IO.Path]::GetFullPath($WorkRoot)
$launcherTargetRoot = [IO.Path]::GetFullPath("$resolvedWorkRoot-cargo")
$resolvedArchive = [IO.Path]::GetFullPath($ArchivePath)
$resolvedNotices = (Resolve-Path -LiteralPath $ThirdPartyNoticesPath).Path
$resolvedNode = (Resolve-Path -LiteralPath $NodeExecutable).Path
$resolvedCargo = (Resolve-Path -LiteralPath $CargoExecutable).Path
$resolvedRustc = (Resolve-Path -LiteralPath $RustcExecutable).Path
$resolvedGit = (Resolve-Path -LiteralPath $GitExecutable).Path
$normalizedSigner = if ([string]::IsNullOrWhiteSpace($ExpectedSignerThumbprint)) {
    $null
} else {
    ($ExpectedSignerThumbprint -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()
}
$officialNodeVersion = '24.18.1'
$officialNodeSha256 = 'ac51903c4c111815d52280b1fdcc8da067cbb37e2fe1a765097b85c3292c8582'

function Test-ChildPath {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Parent
    )

    $normalizedParent = $Parent.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    return $Path.StartsWith("$normalizedParent$([IO.Path]::DirectorySeparatorChar)", [StringComparison]::OrdinalIgnoreCase)
}

function Assert-RegularFile {
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Required regular file is missing: $Path"
    }
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Web portable sources must not be reparse points: $Path"
    }
}

function Assert-PathHasNoReparsePoints {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$TrustedRoot
    )

    $fullRoot = [IO.Path]::GetFullPath($TrustedRoot)
    $fullPath = [IO.Path]::GetFullPath($Path)
    if (
        -not [string]::Equals($fullPath, $fullRoot, [StringComparison]::OrdinalIgnoreCase) -and
        -not (Test-ChildPath -Path $fullPath -Parent $fullRoot)
    ) {
        throw "Path escaped the trusted repository root: $fullPath"
    }
    $volumeRoot = [IO.Path]::GetPathRoot($fullPath)
    if ([string]::IsNullOrWhiteSpace($volumeRoot)) {
        throw "Path does not have a filesystem root: $fullPath"
    }
    $current = $volumeRoot
    $relative = $fullPath.Substring($volumeRoot.Length).TrimStart('\', '/')
    foreach ($segment in @($relative -split '[\\/]+' | Where-Object { $_ })) {
        $current = Join-Path $current $segment
        if (-not (Test-Path -LiteralPath $current)) { continue }
        $item = Get-Item -LiteralPath $current -Force
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Web portable paths must not traverse a reparse point: $current"
        }
    }
}

function Copy-PayloadFile {
    param(
        [Parameter(Mandatory)][string]$Source,
        [Parameter(Mandatory)][string]$RelativeDestination
    )

    Assert-RegularFile -Path $Source
    if (
        [IO.Path]::IsPathRooted($RelativeDestination) -or
        $RelativeDestination.Contains(':') -or
        @($RelativeDestination -split '[\\/]' | Where-Object { $_ -in @('', '.', '..') }).Count -gt 0
    ) {
        throw "Unsafe payload destination: $RelativeDestination"
    }
    $destination = [IO.Path]::GetFullPath((Join-Path $resolvedWorkRoot $RelativeDestination))
    if (-not (Test-ChildPath -Path $destination -Parent $resolvedWorkRoot)) {
        throw "Payload destination escapes its root: $RelativeDestination"
    }
    $destinationParent = Split-Path -Parent $destination
    New-Item -ItemType Directory -Path $destinationParent -Force | Out-Null
    Copy-Item -LiteralPath $Source -Destination $destination
}

function Copy-JavaScriptTree {
    param(
        [Parameter(Mandatory)][string]$SourceRoot,
        [Parameter(Mandatory)][string]$RelativeDestination
    )

    $resolvedSource = (Resolve-Path -LiteralPath $SourceRoot).Path
    $sourceRootItem = Get-Item -LiteralPath $resolvedSource -Force
    if (($sourceRootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "JavaScript source tree is a reparse point: $resolvedSource"
    }
    $pending = [Collections.Generic.Stack[string]]::new()
    $pending.Push($resolvedSource)
    while ($pending.Count -gt 0) {
        $directory = $pending.Pop()
        foreach ($entry in @(Get-ChildItem -LiteralPath $directory -Force)) {
            if (($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "JavaScript source tree contains a reparse point: $($entry.FullName)"
            }
            if ($entry.PSIsContainer) {
                $pending.Push($entry.FullName)
                continue
            }
            if ($entry.Extension -ne '.js') {
                continue
            }
            $relative = $entry.FullName.Substring($resolvedSource.Length).TrimStart('\', '/')
            Copy-PayloadFile -Source $entry.FullName -RelativeDestination (
                Join-Path $RelativeDestination $relative
            )
        }
    }
}

function Copy-CompleteTree {
    param(
        [Parameter(Mandatory)][string]$SourceRoot,
        [Parameter(Mandatory)][string]$RelativeDestination
    )

    $resolvedSource = (Resolve-Path -LiteralPath $SourceRoot).Path
    $pending = [Collections.Generic.Stack[string]]::new()
    $pending.Push($resolvedSource)
    while ($pending.Count -gt 0) {
        $directory = $pending.Pop()
        $directoryItem = Get-Item -LiteralPath $directory -Force
        if (($directoryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Payload source tree contains a reparse point: $directory"
        }
        foreach ($entry in @(Get-ChildItem -LiteralPath $directory -Force)) {
            if (($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Payload source tree contains a reparse point: $($entry.FullName)"
            }
            if ($entry.PSIsContainer) {
                $pending.Push($entry.FullName)
            } else {
                $relative = $entry.FullName.Substring($resolvedSource.Length).TrimStart('\', '/')
                Copy-PayloadFile -Source $entry.FullName -RelativeDestination (
                    Join-Path $RelativeDestination $relative
                )
            }
        }
    }
}

function Get-SafeTreeEntries {
    param([Parameter(Mandatory)][string]$Root)

    $directories = [Collections.Generic.List[string]]::new()
    $files = [Collections.Generic.List[string]]::new()
    $pending = [Collections.Generic.Stack[string]]::new()
    $pending.Push($Root)
    while ($pending.Count -gt 0) {
        $directory = $pending.Pop()
        $directoryItem = Get-Item -LiteralPath $directory -Force
        if (($directoryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Web staging tree contains a reparse point: $directory"
        }
        $directories.Add($directory)
        foreach ($entry in @(Get-ChildItem -LiteralPath $directory -Force)) {
            if (($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Web staging tree contains a reparse point: $($entry.FullName)"
            }
            if ($entry.PSIsContainer) {
                $pending.Push($entry.FullName)
            } else {
                $files.Add($entry.FullName)
            }
        }
    }
    return [pscustomobject]@{ Directories = $directories; Files = $files }
}

function Remove-SafeTree {
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path)) { return }
    if (-not (Test-ChildPath -Path $Path -Parent $outputRoot)) {
        throw "Refusing to remove a Web staging tree outside output: $Path"
    }
    $tree = Get-SafeTreeEntries -Root $Path
    foreach ($file in $tree.Files) {
        Remove-Item -LiteralPath $file -Force
    }
    foreach ($directory in @($tree.Directories | Sort-Object Length -Descending)) {
        Remove-Item -LiteralPath $directory -Force
    }
}

function Invoke-Checked {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$ArgumentList
    )

    & $FilePath @ArgumentList
    if ($LASTEXITCODE -ne 0) {
        throw "'$FilePath $($ArgumentList -join ' ')' failed with exit code $LASTEXITCODE."
    }
}

function New-DeterministicZip {
    param(
        [Parameter(Mandatory)][string]$SourceRoot,
        [Parameter(Mandatory)][string]$Destination,
        [Parameter(Mandatory)][DateTimeOffset]$Timestamp
    )

    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $tree = Get-SafeTreeEntries -Root $SourceRoot
    if ($tree.Files.Count -gt 500) {
        throw 'Web portable payload exceeds the 500-file archive budget.'
    }
    $archive = [IO.Compression.ZipFile]::Open($Destination, [IO.Compression.ZipArchiveMode]::Create)
    try {
        foreach ($filePath in @($tree.Files | Sort-Object)) {
            $relative = $filePath.Substring($SourceRoot.Length).TrimStart('\', '/').Replace('\', '/')
            $entry = $archive.CreateEntry($relative, [IO.Compression.CompressionLevel]::Optimal)
            $entry.LastWriteTime = $Timestamp
            $input = [IO.File]::OpenRead($filePath)
            $output = $entry.Open()
            try {
                $input.CopyTo($output)
            }
            finally {
                $output.Dispose()
                $input.Dispose()
            }
        }
    }
    finally {
        $archive.Dispose()
    }
}

if (
    -not [string]::Equals(
        $resolvedSourceRoot,
        $repositoryRoot,
        [StringComparison]::OrdinalIgnoreCase
    ) -and
    -not (Test-ChildPath -Path $resolvedSourceRoot -Parent $repositoryRoot)
) {
    throw 'The Web portable source root must stay inside the verified repository.'
}
Assert-PathHasNoReparsePoints -Path $resolvedSourceRoot -TrustedRoot $repositoryRoot
if (-not (Test-ChildPath -Path $resolvedWorkRoot -Parent $outputRoot)) {
    throw 'The Web portable work root must stay below output/.'
}
if (-not (Test-ChildPath -Path $launcherTargetRoot -Parent $outputRoot)) {
    throw 'The Web launcher Cargo target must stay below output/.'
}
$archiveParent = Split-Path -Parent $resolvedArchive
if (-not (Test-ChildPath -Path $resolvedArchive -Parent $outputRoot)) {
    throw 'The Web portable archive must stay below output/.'
}
foreach ($path in @($outputRoot, $resolvedWorkRoot, $launcherTargetRoot, $resolvedArchive)) {
    Assert-PathHasNoReparsePoints -Path $path -TrustedRoot $repositoryRoot
}
if ($AllowUnsigned -and (
    -not [string]::IsNullOrWhiteSpace($SignToolPath) -or
    $null -ne $normalizedSigner -or
    -not [string]::IsNullOrWhiteSpace($ExpectedSignerSubject) -or
    -not [string]::IsNullOrWhiteSpace($ExpectedSignerSpkiSha256) -or
    -not [string]::IsNullOrWhiteSpace($TimestampUrl)
)) {
    throw '-AllowUnsigned cannot be combined with Web launcher signing parameters.'
}
if (Test-Path -LiteralPath $resolvedWorkRoot) {
    throw "Web portable work root already exists: $resolvedWorkRoot"
}
if (Test-Path -LiteralPath $launcherTargetRoot) {
    throw "Web launcher Cargo target already exists: $launcherTargetRoot"
}
if (Test-Path -LiteralPath $resolvedArchive) {
    throw "Web portable archive already exists: $resolvedArchive"
}
if (-not (Test-Path -LiteralPath $archiveParent -PathType Container)) {
    throw "Web portable archive parent does not exist: $archiveParent"
}
Assert-RegularFile -Path $resolvedNotices
Assert-RegularFile -Path $resolvedNode
Assert-RegularFile -Path $resolvedCargo
Assert-RegularFile -Path $resolvedRustc
Assert-RegularFile -Path $resolvedGit
if ((Get-FileHash -LiteralPath $resolvedNode -Algorithm SHA256).Hash.ToLowerInvariant() -ne $officialNodeSha256) {
    throw 'The build Node executable does not match the pinned official Node.js SHA-256.'
}
$nodeSignature = Get-AuthenticodeSignature -LiteralPath $resolvedNode
if (
    $nodeSignature.Status.ToString() -ne 'Valid' -or
    $null -eq $nodeSignature.SignerCertificate -or
    $nodeSignature.SignerCertificate.Subject -notmatch '(?:^|, )O=OpenJS Foundation(?:,|$)' -or
    $null -eq $nodeSignature.TimeStamperCertificate -or
    (Get-Item -LiteralPath $resolvedNode).VersionInfo.ProductVersion -ne $officialNodeVersion
) {
    throw 'The build Node executable is not the official valid timestamped pinned runtime.'
}
if (-not $AllowUnsigned) {
    if (
        [string]::IsNullOrWhiteSpace($SignToolPath) -or
        -not (Test-Path -LiteralPath $SignToolPath -PathType Leaf)
    ) {
        throw 'A formal Web portable build requires SignTool.'
    }
    if ($null -eq $normalizedSigner -or $normalizedSigner -notmatch '^[0-9A-F]{40}$') {
        throw 'A formal Web portable build requires a 40-character signer thumbprint.'
    }
    if (
        [string]::IsNullOrWhiteSpace($ExpectedSignerSubject) -or
        $ExpectedSignerSpkiSha256 -cnotmatch '^[0-9a-f]{64}$'
    ) {
        throw 'A formal Web portable build requires the policy signer Subject and SPKI SHA-256.'
    }
    if ([string]::IsNullOrWhiteSpace($TimestampUrl)) {
        throw 'A formal Web portable build requires an RFC 3161 timestamp URL.'
    }
}

$archiveCompleted = $false
try {
    New-Item -ItemType Directory -Path $resolvedWorkRoot | Out-Null
    New-Item -ItemType Directory -Path $launcherTargetRoot | Out-Null
    Copy-PayloadFile -Source (Join-Path $resolvedSourceRoot 'LICENSE') -RelativeDestination 'LICENSE'
    Copy-PayloadFile -Source $resolvedNotices -RelativeDestination 'THIRD_PARTY_NOTICES.txt'
    Copy-PayloadFile -Source $resolvedNode -RelativeDestination 'runtime/node.exe'
    Copy-PayloadFile -Source (Join-Path (Split-Path -Parent $resolvedNode) 'LICENSE') -RelativeDestination 'runtime/LICENSE'
    Copy-CompleteTree -SourceRoot (Join-Path $resolvedSourceRoot 'dist') -RelativeDestination 'app/dist'
    foreach ($serverFile in @(
        'ssh2js-server.mjs',
        'ssh-algorithms.mjs',
        'content-disposition.mjs',
        'static-web.mjs',
        'session-lifecycle.mjs',
        'operation-admission.mjs',
        'terminal-admission.mjs'
    )) {
        Copy-PayloadFile -Source (Join-Path $resolvedSourceRoot "server/$serverFile") -RelativeDestination "app/server/$serverFile"
    }

    foreach ($moduleName in @('ssh2', 'asn1', 'bcrypt-pbkdf', 'safer-buffer', 'tweetnacl')) {
        foreach ($metadataFile in @('package.json', 'LICENSE')) {
            Copy-PayloadFile `
                -Source (Join-Path $resolvedSourceRoot "node_modules/$moduleName/$metadataFile") `
                -RelativeDestination "app/node_modules/$moduleName/$metadataFile"
        }
    }
    Copy-JavaScriptTree `
        -SourceRoot (Join-Path $resolvedSourceRoot 'node_modules/ssh2/lib') `
        -RelativeDestination 'app/node_modules/ssh2/lib'
    Copy-JavaScriptTree `
        -SourceRoot (Join-Path $resolvedSourceRoot 'node_modules/asn1/lib') `
        -RelativeDestination 'app/node_modules/asn1/lib'
    foreach ($moduleFile in @(
        'bcrypt-pbkdf/index.js',
        'safer-buffer/safer.js',
        'safer-buffer/dangerous.js',
        'tweetnacl/nacl-fast.js'
    )) {
        Copy-PayloadFile `
            -Source (Join-Path $resolvedSourceRoot "node_modules/$moduleFile") `
            -RelativeDestination "app/node_modules/$moduleFile"
    }
    if (Get-ChildItem -LiteralPath $resolvedWorkRoot -Recurse -File |
        Where-Object { $_.Name -ieq 'pagent.exe' -or $_.Extension -ieq '.node' }) {
        throw 'The Web portable payload must not contain Pageant helpers or native Node add-ons.'
    }

    $utf8 = [Text.UTF8Encoding]::new($false)
    $versionRecord = [ordered]@{
        schemaVersion = 1
        product = 'NekoSSH Web'
        version = $Version
        commit = $Commit
        platform = 'windows-x64'
        nodeVersion = $officialNodeVersion
        sshRuntime = 'node-ssh2js'
        persistence = 'memory-only'
    } | ConvertTo-Json -Depth 4
    [IO.File]::WriteAllText((Join-Path $resolvedWorkRoot 'VERSION.json'), $versionRecord, $utf8)

    $payloadTree = Get-SafeTreeEntries -Root $resolvedWorkRoot
    $payloadLines = foreach ($filePath in @($payloadTree.Files | Sort-Object)) {
        $relative = $filePath.Substring($resolvedWorkRoot.Length).TrimStart('\', '/').Replace('\', '/')
        $hash = (Get-FileHash -LiteralPath $filePath -Algorithm SHA256).Hash.ToLowerInvariant()
        "$hash  *$relative"
    }
    $payloadManifestPath = Join-Path $resolvedWorkRoot 'PAYLOAD_SHA256SUMS.txt'
    [IO.File]::WriteAllLines($payloadManifestPath, $payloadLines, $utf8)
    $payloadManifestHash = (Get-FileHash -LiteralPath $payloadManifestPath -Algorithm SHA256).Hash.ToLowerInvariant()

    $previousPayloadHash = $env:NEKOSSH_WEB_PAYLOAD_SHA256
    $previousCargoTargetDirectory = $env:CARGO_TARGET_DIR
    $previousRustc = $env:RUSTC
    try {
        $env:NEKOSSH_WEB_PAYLOAD_SHA256 = $payloadManifestHash
        $env:CARGO_TARGET_DIR = $launcherTargetRoot
        $env:RUSTC = $resolvedRustc
        Invoke-Checked -FilePath $resolvedCargo -ArgumentList @(
            'build',
            '--manifest-path',
            (Join-Path $resolvedSourceRoot 'web-launcher/Cargo.toml'),
            '--locked',
            '--release'
        )
    }
    finally {
        if ($null -eq $previousPayloadHash) {
            Remove-Item Env:NEKOSSH_WEB_PAYLOAD_SHA256 -ErrorAction SilentlyContinue
        } else {
            $env:NEKOSSH_WEB_PAYLOAD_SHA256 = $previousPayloadHash
        }
        if ($null -eq $previousCargoTargetDirectory) {
            Remove-Item Env:CARGO_TARGET_DIR -ErrorAction SilentlyContinue
        } else {
            $env:CARGO_TARGET_DIR = $previousCargoTargetDirectory
        }
        if ($null -eq $previousRustc) {
            Remove-Item Env:RUSTC -ErrorAction SilentlyContinue
        } else {
            $env:RUSTC = $previousRustc
        }
    }
    $builtLauncher = Join-Path $launcherTargetRoot 'release/nekossh-web.exe'
    Copy-PayloadFile -Source $builtLauncher -RelativeDestination 'NekoSSH-Web.exe'
    $stagedLauncher = Join-Path $resolvedWorkRoot 'NekoSSH-Web.exe'
    if ($AllowUnsigned) {
        if ((Get-AuthenticodeSignature -LiteralPath $stagedLauncher).Status.ToString() -ne 'NotSigned') {
            throw 'The unsigned Web portable build unexpectedly contains a signed project launcher.'
        }
    } else {
        Invoke-Checked -FilePath $SignToolPath -ArgumentList @(
            'sign',
            '/sha1',
            $normalizedSigner,
            '/fd',
            'SHA256',
            '/tr',
            $TimestampUrl,
            '/td',
            'SHA256',
            '/v',
            $stagedLauncher
        )
        Invoke-Checked -FilePath $SignToolPath -ArgumentList @('verify', '/pa', '/all', '/v', $stagedLauncher)
    }

    $commitTimeText = (
        & $resolvedGit -C $repositoryRoot show -s --format=%cI $Commit 2>&1 |
            Out-String
    ).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw 'Unable to read the release commit timestamp.'
    }
    $commitTimestamp = [DateTimeOffset]::Parse(
        $commitTimeText,
        [Globalization.CultureInfo]::InvariantCulture
    )
    $minimumZipTimestamp = [DateTimeOffset]::new(1980, 1, 1, 0, 0, 0, [TimeSpan]::Zero)
    if ($commitTimestamp -lt $minimumZipTimestamp) {
        $commitTimestamp = $minimumZipTimestamp
    }
    New-DeterministicZip `
        -SourceRoot $resolvedWorkRoot `
        -Destination $resolvedArchive `
        -Timestamp $commitTimestamp

    $verifyArguments = @{
        ArchivePath = $resolvedArchive
        ExpectedVersion = $Version
        ExpectedCommit = $Commit
    }
    if ($AllowUnsigned) {
        $verifyArguments.AllowUnsignedLauncher = $true
    } else {
        $verifyArguments.ExpectedProjectSignerThumbprint = $normalizedSigner
        $verifyArguments.ExpectedProjectSignerSubject = $ExpectedSignerSubject
        $verifyArguments.ExpectedProjectSignerSpkiSha256 = $ExpectedSignerSpkiSha256
    }
    $verificationJson = & (
        Join-Path $resolvedSourceRoot 'scripts/verify-windows-web-portable.ps1'
    ) @verifyArguments
    if ($LASTEXITCODE -ne 0) {
        throw 'Web portable archive verification failed.'
    }
    $verification = ($verificationJson | Out-String).Trim() | ConvertFrom-Json
    $expectedSelfTestStatus = if ($AllowUnsigned) { 'skipped-unsigned' } else { 'passed' }
    if ($verification.selfTest -cne $expectedSelfTestStatus) {
        throw "Web portable verification returned an unexpected self-test status: $($verification.selfTest)"
    }
    $archiveCompleted = $true
}
finally {
    try {
        Remove-SafeTree -Path $resolvedWorkRoot
    }
    finally {
        try {
            Remove-SafeTree -Path $launcherTargetRoot
        }
        finally {
            if (-not $archiveCompleted -and (Test-Path -LiteralPath $resolvedArchive)) {
                Assert-RegularFile -Path $resolvedArchive
                Remove-Item -LiteralPath $resolvedArchive -Force
            }
        }
    }
}
