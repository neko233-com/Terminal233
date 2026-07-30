Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:SourceIdentityTypeName = 'NekoSSH.GitSourceIdentity'
$script:SourceSnapshotTypeName = 'NekoSSH.GitSourceSnapshot'
$script:SnapshotDigestFormat = 'git-raw-blobs-sha256-v1'

function ConvertTo-NekoSshNativeArgument {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Value)

    if ($Value.Length -gt 0 -and $Value -notmatch '[\s"]') {
        return $Value
    }

    $builder = [Text.StringBuilder]::new()
    [void]$builder.Append('"')
    $backslashes = 0
    foreach ($character in $Value.ToCharArray()) {
        if ($character -eq '\') {
            $backslashes += 1
            continue
        }
        if ($character -eq '"') {
            [void]$builder.Append(('\' * (($backslashes * 2) + 1)))
            [void]$builder.Append('"')
            $backslashes = 0
            continue
        }
        if ($backslashes -gt 0) {
            [void]$builder.Append(('\' * $backslashes))
            $backslashes = 0
        }
        [void]$builder.Append($character)
    }
    if ($backslashes -gt 0) {
        [void]$builder.Append(('\' * ($backslashes * 2)))
    }
    [void]$builder.Append('"')
    return $builder.ToString()
}

function Resolve-NekoSshGitExecutable {
    param([string]$GitExecutable = 'git')

    $command = Get-Command -Name $GitExecutable -CommandType Application -ErrorAction Stop |
        Select-Object -First 1
    $path = [IO.Path]::GetFullPath([string]$command.Source)
    $item = Get-Item -LiteralPath $path -Force
    if (
        $item.PSIsContainer -or
        ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0
    ) {
        throw "Git must resolve to a regular executable, not a directory or reparse point: $path"
    }
    return $path
}

function Set-NekoSshSafeGitEnvironment {
    param([Parameter(Mandatory)]$EnvironmentVariables)

    foreach ($inheritedName in @($EnvironmentVariables.Keys)) {
        if ([string]$inheritedName -match '^(?i:GIT_)') {
            [void]$EnvironmentVariables.Remove([string]$inheritedName)
        }
    }
    $EnvironmentVariables['GIT_TERMINAL_PROMPT'] = '0'
    $EnvironmentVariables['GCM_INTERACTIVE'] = 'Never'
    $EnvironmentVariables['GIT_CONFIG_NOSYSTEM'] = '1'
    $EnvironmentVariables['GIT_CONFIG_GLOBAL'] = 'NUL'
    $EnvironmentVariables['GIT_ATTR_NOSYSTEM'] = '1'
    $EnvironmentVariables['GIT_NO_LAZY_FETCH'] = '1'
    $EnvironmentVariables['GIT_NO_REPLACE_OBJECTS'] = '1'
}

function Invoke-NekoSshGitCommand {
    param(
        [Parameter(Mandatory)][string]$GitExecutable,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string[]]$ArgumentList
    )

    $arguments = @(
        '--no-optional-locks',
        '--no-replace-objects',
        '-c', 'core.fsmonitor=false',
        '-c', 'core.hooksPath=NUL',
        '-c', 'core.attributesFile=NUL',
        '-c', 'core.excludesFile=NUL',
        '-c', "safe.directory=$RepositoryRoot",
        '-C', $RepositoryRoot
    ) + $ArgumentList
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $GitExecutable
    $startInfo.Arguments = (
        $arguments |
            ForEach-Object { ConvertTo-NekoSshNativeArgument -Value ([string]$_) }
    ) -join ' '
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.StandardOutputEncoding = [Text.UTF8Encoding]::new($false, $true)
    $startInfo.StandardErrorEncoding = [Text.UTF8Encoding]::new($false, $true)
    # Windows PowerShell 5.1 can return $null when a lazily initialized
    # EnvironmentVariables property is indexed directly. Materialize the
    # dictionary first so release verification behaves the same in pwsh and
    # the Windows inbox host used by the packaging scripts.
    $environmentVariables = $startInfo.EnvironmentVariables
    if ($null -eq $environmentVariables) {
        $environmentVariables = $startInfo.EnvironmentVariables
    }
    if ($null -eq $environmentVariables) {
        throw 'Unable to initialize the Git process environment after materialization.'
    }
    Set-NekoSshSafeGitEnvironment -EnvironmentVariables $environmentVariables

    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) {
            throw 'Git process did not start.'
        }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0) {
            $summary = $stderr.Trim()
            if ([string]::IsNullOrWhiteSpace($summary)) {
                $summary = $stdout.Trim()
            }
            throw (
                "Git command failed with exit code $($process.ExitCode): " +
                "git $($ArgumentList -join ' ')`n$summary"
            )
        }
        return [pscustomobject]@{
            StdOut = $stdout
            StdErr = $stderr
        }
    }
    finally {
        $process.Dispose()
    }
}

function Get-NekoSshGitOutput {
    param(
        [Parameter(Mandatory)][string]$GitExecutable,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string[]]$ArgumentList,
        [switch]$PreserveTrailingWhitespace
    )

    $result = Invoke-NekoSshGitCommand `
        -GitExecutable $GitExecutable `
        -RepositoryRoot $RepositoryRoot `
        -ArgumentList $ArgumentList
    if ($PreserveTrailingWhitespace) {
        return [string]$result.StdOut
    }
    return ([string]$result.StdOut).Trim()
}

function Test-NekoSshChildPath {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Parent
    )

    $fullPath = [IO.Path]::GetFullPath($Path)
    $fullParent = [IO.Path]::GetFullPath($Parent).TrimEnd(
        [IO.Path]::DirectorySeparatorChar,
        [IO.Path]::AltDirectorySeparatorChar
    )
    return $fullPath.StartsWith(
        "$fullParent$([IO.Path]::DirectorySeparatorChar)",
        [StringComparison]::OrdinalIgnoreCase
    )
}

function Assert-NekoSshRegularDirectory {
    param([Parameter(Mandatory)][string]$Path)

    $item = Get-Item -LiteralPath $Path -Force
    if (
        -not $item.PSIsContainer -or
        ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0
    ) {
        throw "Source integrity requires a regular directory, not a reparse point: $Path"
    }
}

function Assert-NekoSshPrimaryGitAdministrativeLayout {
    param([Parameter(Mandatory)][string]$RepositoryRoot)

    $gitDirectory = Join-Path $RepositoryRoot '.git'
    Assert-NekoSshRegularDirectory -Path $gitDirectory
    $pending = [Collections.Generic.Stack[string]]::new()
    $pending.Push($gitDirectory)
    while ($pending.Count -gt 0) {
        $directory = $pending.Pop()
        foreach ($entry in @(Get-ChildItem -LiteralPath $directory -Force)) {
            if (($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Release source Git administrative data contains a reparse point: $($entry.FullName)"
            }
            if ($entry.PSIsContainer) {
                $pending.Push($entry.FullName)
            }
        }
    }
    $configPath = Join-Path $gitDirectory 'config'
    $config = Get-Item -LiteralPath $configPath -Force
    if (
        $config.PSIsContainer -or
        ($config.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0
    ) {
        throw 'Release source requires a regular primary .git/config file.'
    }
    foreach ($forbidden in @(
        (Join-Path $gitDirectory 'commondir'),
        (Join-Path $gitDirectory 'config.worktree'),
        (Join-Path $gitDirectory 'objects/info/alternates'),
        (Join-Path $gitDirectory 'objects/info/http-alternates')
    )) {
        if (Test-Path -LiteralPath $forbidden) {
            throw "Release source rejects external or split Git administrative data: $forbidden"
        }
    }
}

function Remove-NekoSshOwnedTemporaryTree {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$TrustedParent
    )

    if (-not (Test-Path -LiteralPath $Path)) {
        return
    }
    $fullPath = [IO.Path]::GetFullPath($Path)
    $fullParent = [IO.Path]::GetFullPath($TrustedParent)
    if (-not (Test-NekoSshChildPath -Path $fullPath -Parent $fullParent)) {
        throw "Refusing to remove a temporary source tree outside '$fullParent': $fullPath"
    }

    $directories = [Collections.Generic.List[string]]::new()
    $files = [Collections.Generic.List[string]]::new()
    $pending = [Collections.Generic.Stack[string]]::new()
    $pending.Push($fullPath)
    while ($pending.Count -gt 0) {
        $directory = $pending.Pop()
        Assert-NekoSshRegularDirectory -Path $directory
        $directories.Add($directory)
        foreach ($entry in @(Get-ChildItem -LiteralPath $directory -Force)) {
            if (($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Temporary source tree contains a reparse point: $($entry.FullName)"
            }
            if ($entry.PSIsContainer) {
                $pending.Push($entry.FullName)
            } else {
                $files.Add($entry.FullName)
            }
        }
    }
    foreach ($file in $files) {
        Remove-Item -LiteralPath $file -Force
    }
    foreach ($directory in @($directories | Sort-Object Length -Descending)) {
        Remove-Item -LiteralPath $directory -Force
    }
}

function Add-NekoSshDigestBytes {
    param(
        [Parameter(Mandatory)][Security.Cryptography.IncrementalHash]$Digest,
        [Parameter(Mandatory)][byte[]]$Bytes,
        [int]$Offset = 0,
        [int]$Count = $Bytes.Length
    )

    $Digest.AppendData($Bytes, $Offset, $Count)
}

function Add-NekoSshDigestUInt32 {
    param(
        [Parameter(Mandatory)][Security.Cryptography.IncrementalHash]$Digest,
        [Parameter(Mandatory)][uint32]$Value
    )

    [byte[]]$bytes = [BitConverter]::GetBytes($Value)
    try {
        if ([BitConverter]::IsLittleEndian) {
            [Array]::Reverse($bytes)
        }
        Add-NekoSshDigestBytes -Digest $Digest -Bytes $bytes
    }
    finally {
        [Array]::Clear($bytes, 0, $bytes.Length)
    }
}

function Add-NekoSshDigestUInt64 {
    param(
        [Parameter(Mandatory)][Security.Cryptography.IncrementalHash]$Digest,
        [Parameter(Mandatory)][uint64]$Value
    )

    [byte[]]$bytes = [BitConverter]::GetBytes($Value)
    try {
        if ([BitConverter]::IsLittleEndian) {
            [Array]::Reverse($bytes)
        }
        Add-NekoSshDigestBytes -Digest $Digest -Bytes $bytes
    }
    finally {
        [Array]::Clear($bytes, 0, $bytes.Length)
    }
}

function Get-NekoSshGitBlobSize {
    param(
        [Parameter(Mandatory)][string]$GitExecutable,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$ObjectId
    )

    $value = Get-NekoSshGitOutput `
        -GitExecutable $GitExecutable `
        -RepositoryRoot $RepositoryRoot `
        -ArgumentList @('cat-file', '-s', $ObjectId)
    [long]$size = 0
    if (
        -not [long]::TryParse(
            $value,
            [Globalization.NumberStyles]::None,
            [Globalization.CultureInfo]::InvariantCulture,
            [ref]$size
        ) -or
        $size -lt 0
    ) {
        throw "Git blob $ObjectId has an invalid size: '$value'."
    }
    return $size
}

function Add-NekoSshGitBlobToDigest {
    param(
        [Parameter(Mandatory)][string]$GitExecutable,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$ObjectId,
        [Parameter(Mandatory)][long]$ExpectedSize,
        [Parameter(Mandatory)][Security.Cryptography.IncrementalHash]$SnapshotDigest
    )

    $arguments = @(
        '--no-optional-locks',
        '--no-replace-objects',
        '-c', 'core.fsmonitor=false',
        '-c', 'core.hooksPath=NUL',
        '-c', 'core.attributesFile=NUL',
        '-c', 'core.excludesFile=NUL',
        '-c', "safe.directory=$RepositoryRoot",
        '-C', $RepositoryRoot,
        'cat-file', 'blob', $ObjectId
    )
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $GitExecutable
    $startInfo.Arguments = (
        $arguments |
            ForEach-Object { ConvertTo-NekoSshNativeArgument -Value ([string]$_) }
    ) -join ' '
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.StandardErrorEncoding = [Text.UTF8Encoding]::new($false, $true)
    $environmentVariables = $startInfo.EnvironmentVariables
    if ($null -eq $environmentVariables) {
        $environmentVariables = $startInfo.EnvironmentVariables
    }
    if ($null -eq $environmentVariables) {
        throw 'Unable to initialize the Git blob digest environment.'
    }
    Set-NekoSshSafeGitEnvironment -EnvironmentVariables $environmentVariables

    $contentDigest = [Security.Cryptography.IncrementalHash]::CreateHash(
        [Security.Cryptography.HashAlgorithmName]::SHA256
    )
    [byte[]]$buffer = [byte[]]::new(64 * 1024)
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) {
            throw "Git did not start while hashing blob $ObjectId."
        }
        $stderrTask = $process.StandardError.ReadToEndAsync()
        [long]$total = 0
        while (($read = $process.StandardOutput.BaseStream.Read($buffer, 0, $buffer.Length)) -gt 0) {
            if ($total -gt ($ExpectedSize - [long]$read)) {
                throw "Git blob $ObjectId exceeded its declared size while being hashed."
            }
            $total += [long]$read
            Add-NekoSshDigestBytes `
                -Digest $SnapshotDigest `
                -Bytes $buffer `
                -Count $read
            Add-NekoSshDigestBytes `
                -Digest $contentDigest `
                -Bytes $buffer `
                -Count $read
        }
        $process.WaitForExit()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0) {
            throw (
                "Git failed to hash blob $ObjectId with exit code " +
                "$($process.ExitCode): $($stderr.Trim())"
            )
        }
        if ($total -ne $ExpectedSize) {
            throw "Git blob $ObjectId size changed while being hashed."
        }
        [byte[]]$hash = $contentDigest.GetHashAndReset()
        try {
            return ([BitConverter]::ToString($hash)).Replace('-', '').ToLowerInvariant()
        }
        finally {
            [Array]::Clear($hash, 0, $hash.Length)
        }
    }
    finally {
        [Array]::Clear($buffer, 0, $buffer.Length)
        $contentDigest.Dispose()
        $process.Dispose()
    }
}

function Get-NekoSshHeadInventory {
    param(
        [Parameter(Mandatory)][string]$GitExecutable,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$Commit
    )

    $raw = Get-NekoSshGitOutput `
        -GitExecutable $GitExecutable `
        -RepositoryRoot $RepositoryRoot `
        -ArgumentList @('ls-tree', '-r', '-z', '--full-tree', $Commit) `
        -PreserveTrailingWhitespace
    if ($raw.Length -eq 0 -or $raw[$raw.Length - 1] -ne [char]0) {
        throw 'Git HEAD tree inventory is empty or is not NUL-terminated.'
    }
    $records = @($raw.Split([char]0) | Where-Object { $_.Length -gt 0 })
    $entries = [Collections.Generic.SortedDictionary[string, object]]::new(
        [StringComparer]::Ordinal
    )
    $caseInsensitivePaths = [Collections.Generic.HashSet[string]]::new(
        [StringComparer]::OrdinalIgnoreCase
    )
    foreach ($record in $records) {
        $match = [regex]::Match(
            $record,
            '^(?<mode>[0-9]{6}) (?<type>[a-z]+) (?<object>[0-9a-f]{40})\t(?<path>.+)$',
            [Text.RegularExpressions.RegexOptions]::CultureInvariant
        )
        if (-not $match.Success) {
            throw 'Git HEAD tree contains a malformed inventory record.'
        }
        if (
            $match.Groups['type'].Value -cne 'blob' -or
            $match.Groups['mode'].Value -cnotin @('100644', '100755')
        ) {
            throw (
                "Release sources may only contain regular tracked files; rejected " +
                "'$($match.Groups['path'].Value)' mode=$($match.Groups['mode'].Value) " +
                "type=$($match.Groups['type'].Value)."
            )
        }
        $path = $match.Groups['path'].Value
        if (
            [IO.Path]::IsPathRooted($path) -or
            $path -match '(^|/)\.\.?(/|$)' -or
            $path.IndexOf('\') -ge 0 -or
            $path.IndexOf(':') -ge 0
        ) {
            throw "Git HEAD contains an unsafe tracked path: '$path'."
        }
        if (-not $caseInsensitivePaths.Add($path)) {
            throw "Git HEAD contains a case-insensitive path collision: '$path'."
        }
        $entries.Add($path, [pscustomobject]@{
            Path = $path
            Mode = $match.Groups['mode'].Value
            Object = $match.Groups['object'].Value
            Size = [long]0
            ContentSha256 = $null
        })
    }

    $utf8 = [Text.UTF8Encoding]::new($false, $true)
    $digest = [Security.Cryptography.IncrementalHash]::CreateHash(
        [Security.Cryptography.HashAlgorithmName]::SHA256
    )
    try {
        [byte[]]$domain = $utf8.GetBytes("NekoSSH`0$($script:SnapshotDigestFormat)`0")
        try {
            Add-NekoSshDigestBytes -Digest $digest -Bytes $domain
        }
        finally {
            [Array]::Clear($domain, 0, $domain.Length)
        }
        Add-NekoSshDigestUInt32 -Digest $digest -Value ([uint32]$entries.Count)
        foreach ($entry in $entries.Values) {
            [byte[]]$modeBytes = $utf8.GetBytes([string]$entry.Mode)
            [byte[]]$pathBytes = $utf8.GetBytes([string]$entry.Path)
            try {
                Add-NekoSshDigestUInt32 -Digest $digest -Value ([uint32]$modeBytes.Length)
                Add-NekoSshDigestBytes -Digest $digest -Bytes $modeBytes
                Add-NekoSshDigestUInt32 -Digest $digest -Value ([uint32]$pathBytes.Length)
                Add-NekoSshDigestBytes -Digest $digest -Bytes $pathBytes
            }
            finally {
                [Array]::Clear($modeBytes, 0, $modeBytes.Length)
                [Array]::Clear($pathBytes, 0, $pathBytes.Length)
            }
            $entry.Size = Get-NekoSshGitBlobSize `
                -GitExecutable $GitExecutable `
                -RepositoryRoot $RepositoryRoot `
                -ObjectId ([string]$entry.Object)
            Add-NekoSshDigestUInt64 -Digest $digest -Value ([uint64]$entry.Size)
            $entry.ContentSha256 = Add-NekoSshGitBlobToDigest `
                -GitExecutable $GitExecutable `
                -RepositoryRoot $RepositoryRoot `
                -ObjectId ([string]$entry.Object) `
                -ExpectedSize $entry.Size `
                -SnapshotDigest $digest
        }
        [byte[]]$snapshotHash = $digest.GetHashAndReset()
        try {
            $snapshotSha256 = (
                [BitConverter]::ToString($snapshotHash)
            ).Replace('-', '').ToLowerInvariant()
        }
        finally {
            [Array]::Clear($snapshotHash, 0, $snapshotHash.Length)
        }
    }
    finally {
        $digest.Dispose()
    }
    return [pscustomobject]@{
        Paths = @($entries.Keys)
        Entries = @($entries.Values)
        SnapshotSha256 = $snapshotSha256
        DigestFormat = $script:SnapshotDigestFormat
    }
}

function Resolve-NekoSshTrackedFilePath {
    param(
        [Parameter(Mandatory)][string]$TreeRoot,
        [Parameter(Mandatory)][string]$TrackedPath
    )

    $root = (Resolve-Path -LiteralPath $TreeRoot).Path
    Assert-NekoSshRegularDirectory -Path $root
    $segments = @($TrackedPath.Split('/'))
    $current = $root
    for ($index = 0; $index -lt $segments.Count; $index += 1) {
        $current = [IO.Path]::GetFullPath((Join-Path $current $segments[$index]))
        if (-not (Test-NekoSshChildPath -Path $current -Parent $root)) {
            throw "Tracked path escaped its tree root: '$TrackedPath'."
        }
        if (-not (Test-Path -LiteralPath $current)) {
            throw "Tracked path is missing from the filesystem: '$TrackedPath'."
        }
        $item = Get-Item -LiteralPath $current -Force
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Tracked path traverses a reparse point: '$TrackedPath'."
        }
        $isFinalSegment = $index -eq ($segments.Count - 1)
        if (
            ($isFinalSegment -and $item.PSIsContainer) -or
            (-not $isFinalSegment -and -not $item.PSIsContainer)
        ) {
            throw "Tracked path has an invalid file/directory shape: '$TrackedPath'."
        }
    }
    return $current
}

function Assert-NekoSshTrackedBlobBytes {
    param(
        [Parameter(Mandatory)][string]$GitExecutable,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$TreeRoot,
        [Parameter(Mandatory)][object[]]$Entries,
        [Parameter(Mandatory)][string]$Context
    )

    foreach ($entry in $Entries) {
        $filePath = Resolve-NekoSshTrackedFilePath `
            -TreeRoot $TreeRoot `
            -TrackedPath ([string]$entry.Path)
        $rawObject = Get-NekoSshGitOutput `
            -GitExecutable $GitExecutable `
            -RepositoryRoot $RepositoryRoot `
            -ArgumentList @('hash-object', '--no-filters', '--', $filePath)
        $file = Get-Item -LiteralPath $filePath -Force
        $rawSha256 = (
            Get-FileHash -LiteralPath $filePath -Algorithm SHA256
        ).Hash.ToLowerInvariant()
        if (
            $rawObject -cnotmatch '^[0-9a-f]{40}$' -or
            $rawObject -cne [string]$entry.Object -or
            [long]$file.Length -ne [long]$entry.Size -or
            $rawSha256 -cne [string]$entry.ContentSha256
        ) {
            throw (
                "$Context tracked file bytes do not exactly match committed blob " +
                "$($entry.Object): '$($entry.Path)' actual=$rawObject/$rawSha256. Git filters and " +
                'line-ending normalization are not permitted to hide byte changes.'
            )
        }
    }
}

function Export-NekoSshGitBlob {
    param(
        [Parameter(Mandatory)][string]$GitExecutable,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$ObjectId,
        [Parameter(Mandatory)][string]$Destination
    )

    if ($ObjectId -cnotmatch '^[0-9a-f]{40}$') {
        throw "Cannot export a noncanonical Git blob identifier: '$ObjectId'."
    }
    if (Test-Path -LiteralPath $Destination) {
        throw "Refusing to overwrite an existing snapshot path: $Destination"
    }
    $arguments = @(
        '--no-optional-locks',
        '--no-replace-objects',
        '-c', 'core.fsmonitor=false',
        '-c', 'core.hooksPath=NUL',
        '-c', 'core.attributesFile=NUL',
        '-c', 'core.excludesFile=NUL',
        '-c', "safe.directory=$RepositoryRoot",
        '-C', $RepositoryRoot,
        'cat-file', 'blob', $ObjectId
    )
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $GitExecutable
    $startInfo.Arguments = (
        $arguments |
            ForEach-Object { ConvertTo-NekoSshNativeArgument -Value ([string]$_) }
    ) -join ' '
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.StandardErrorEncoding = [Text.UTF8Encoding]::new($false, $true)
    $environmentVariables = $startInfo.EnvironmentVariables
    if ($null -eq $environmentVariables) {
        $environmentVariables = $startInfo.EnvironmentVariables
    }
    if ($null -eq $environmentVariables) {
        throw 'Unable to initialize the Git blob export environment.'
    }
    Set-NekoSshSafeGitEnvironment -EnvironmentVariables $environmentVariables

    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    $stream = [IO.FileStream]::new(
        $Destination,
        [IO.FileMode]::CreateNew,
        [IO.FileAccess]::Write,
        [IO.FileShare]::None
    )
    try {
        if (-not $process.Start()) {
            throw "Git did not start while exporting blob $ObjectId."
        }
        $copyTask = $process.StandardOutput.BaseStream.CopyToAsync($stream)
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        [void]$copyTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        $stream.Flush($true)
        if ($process.ExitCode -ne 0) {
            throw (
                "Git failed to export blob $ObjectId with exit code " +
                "$($process.ExitCode): $($stderr.Trim())"
            )
        }
    }
    catch {
        $stream.Dispose()
        if (Test-Path -LiteralPath $Destination) {
            Remove-Item -LiteralPath $Destination -Force
        }
        throw
    }
    finally {
        $stream.Dispose()
        $process.Dispose()
    }
}

function Assert-NekoSshIndexFlags {
    param(
        [Parameter(Mandatory)][string]$GitExecutable,
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][int]$ExpectedCount
    )

    $raw = Get-NekoSshGitOutput `
        -GitExecutable $GitExecutable `
        -RepositoryRoot $RepositoryRoot `
        -ArgumentList @('ls-files', '-v', '-z') `
        -PreserveTrailingWhitespace
    if ($raw.Length -eq 0 -or $raw[$raw.Length - 1] -ne [char]0) {
        throw 'Git tracked-file state is empty or is not NUL-terminated.'
    }
    $records = @($raw.Split([char]0) | Where-Object { $_.Length -gt 0 })
    if ($records.Count -ne $ExpectedCount) {
        throw 'Git tracked-file state count does not match the committed tree.'
    }
    foreach ($record in $records) {
        if ($record.Length -lt 3 -or $record[1] -ne ' ') {
            throw 'Git returned a malformed tracked-file state record.'
        }
        if ($record[0] -cne 'H') {
            throw (
                "Tracked path '$($record.Substring(2))' has forbidden Git index state " +
                "'$($record[0])'. skip-worktree, assume-unchanged, unmerged, and " +
                'other hidden states are forbidden for releases.'
            )
        }
    }
}

function Assert-NekoSshSourceIdentityMatches {
    param(
        [Parameter(Mandatory)]$Actual,
        [Parameter(Mandatory)]$Expected
    )

    foreach ($field in @(
        'Branch',
        'Commit',
        'Tree',
        'SnapshotSha256',
        'DigestFormat',
        'TrackedFileCount'
    )) {
        if ($Actual.PSObject.Properties.Name -cnotcontains $field) {
            throw "Actual source identity is missing '$field'."
        }
        if ($Expected.PSObject.Properties.Name -cnotcontains $field) {
            throw "Expected source identity is missing '$field'."
        }
        if ([string]$Actual.$field -cne [string]$Expected.$field) {
            throw (
                "Git source identity changed during release processing: " +
                "$field expected='$($Expected.$field)' actual='$($Actual.$field)'."
            )
        }
    }
}

function Assert-NekoSshGitSourceIntegrity {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [string]$GitExecutable = 'git',
        $Expected
    )

    $root = (Resolve-Path -LiteralPath $RepositoryRoot).Path
    Assert-NekoSshRegularDirectory -Path $root
    Assert-NekoSshPrimaryGitAdministrativeLayout -RepositoryRoot $root
    $gitPath = Resolve-NekoSshGitExecutable -GitExecutable $GitExecutable
    $reportedRoot = Get-NekoSshGitOutput `
        -GitExecutable $gitPath `
        -RepositoryRoot $root `
        -ArgumentList @('rev-parse', '--show-toplevel')
    if (-not [string]::Equals(
        [IO.Path]::GetFullPath($reportedRoot),
        [IO.Path]::GetFullPath($root),
        [StringComparison]::OrdinalIgnoreCase
    )) {
        throw 'Release source root does not match the Git worktree root.'
    }
    $branchRef = Get-NekoSshGitOutput `
        -GitExecutable $gitPath `
        -RepositoryRoot $root `
        -ArgumentList @('symbolic-ref', '--quiet', 'HEAD')
    if ($branchRef -cne 'refs/heads/main') {
        throw "Manual releases require refs/heads/main, not '$branchRef'."
    }
    $commit = Get-NekoSshGitOutput `
        -GitExecutable $gitPath `
        -RepositoryRoot $root `
        -ArgumentList @('rev-parse', '--verify', 'HEAD^{commit}')
    $originCommit = Get-NekoSshGitOutput `
        -GitExecutable $gitPath `
        -RepositoryRoot $root `
        -ArgumentList @('rev-parse', '--verify', 'refs/remotes/origin/main^{commit}')
    if (
        $commit -cnotmatch '^[0-9a-f]{40}$' -or
        $originCommit -cne $commit
    ) {
        throw "HEAD ($commit) must exactly match origin/main ($originCommit)."
    }
    $tree = Get-NekoSshGitOutput `
        -GitExecutable $gitPath `
        -RepositoryRoot $root `
        -ArgumentList @('rev-parse', '--verify', 'HEAD^{tree}')
    if ($tree -cnotmatch '^[0-9a-f]{40}$') {
        throw 'HEAD tree is not a canonical SHA-1 object identifier.'
    }
    $inventory = Get-NekoSshHeadInventory `
        -GitExecutable $gitPath `
        -RepositoryRoot $root `
        -Commit $commit
    Assert-NekoSshIndexFlags `
        -GitExecutable $gitPath `
        -RepositoryRoot $root `
        -ExpectedCount $inventory.Paths.Count

    $indexTree = Get-NekoSshGitOutput `
        -GitExecutable $gitPath `
        -RepositoryRoot $root `
        -ArgumentList @('write-tree')
    if ($indexTree -cne $tree) {
        throw "Git index tree ($indexTree) does not exactly match HEAD tree ($tree)."
    }
    Assert-NekoSshTrackedBlobBytes `
        -GitExecutable $gitPath `
        -RepositoryRoot $root `
        -TreeRoot $root `
        -Entries $inventory.Entries `
        -Context 'Git worktree'
    $untracked = Get-NekoSshGitOutput `
        -GitExecutable $gitPath `
        -RepositoryRoot $root `
        -ArgumentList @('ls-files', '--others', '--exclude-standard', '-z') `
        -PreserveTrailingWhitespace
    if ($untracked.Length -ne 0) {
        throw 'Manual releases require a clean Git worktree, including no untracked files.'
    }

    $identity = [pscustomobject]@{
        Branch = 'main'
        Commit = $commit
        Tree = $tree
        SnapshotSha256 = $inventory.SnapshotSha256
        DigestFormat = $inventory.DigestFormat
        TrackedFileCount = [int]$inventory.Paths.Count
        GitExecutable = $gitPath
    }
    $identity.PSObject.TypeNames.Insert(0, $script:SourceIdentityTypeName)
    if ($null -ne $Expected) {
        Assert-NekoSshSourceIdentityMatches -Actual $identity -Expected $Expected
    }
    Assert-NekoSshPrimaryGitAdministrativeLayout -RepositoryRoot $root
    return $identity
}

function Assert-NekoSshSnapshotFiles {
    param(
        [Parameter(Mandatory)][string]$SnapshotRoot,
        [Parameter(Mandatory)][string[]]$TrackedPaths,
        [switch]$RequireReadOnly
    )

    $root = (Resolve-Path -LiteralPath $SnapshotRoot).Path
    Assert-NekoSshRegularDirectory -Path $root
    foreach ($trackedPath in $TrackedPaths) {
        $segments = @($trackedPath.Split('/'))
        $current = $root
        for ($index = 0; $index -lt $segments.Count; $index += 1) {
            $current = [IO.Path]::GetFullPath((Join-Path $current $segments[$index]))
            if (-not (Test-NekoSshChildPath -Path $current -Parent $root)) {
                throw "Tracked snapshot path escaped the snapshot root: '$trackedPath'."
            }
            $item = Get-Item -LiteralPath $current -Force
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Tracked snapshot path traverses a reparse point: '$trackedPath'."
            }
            $isFinalSegment = $index -eq ($segments.Count - 1)
            if (($isFinalSegment -and $item.PSIsContainer) -or (-not $isFinalSegment -and -not $item.PSIsContainer)) {
                throw "Tracked snapshot path has an invalid file/directory shape: '$trackedPath'."
            }
        }
        if (
            $RequireReadOnly -and
            ($item.Attributes -band [IO.FileAttributes]::ReadOnly) -eq 0
        ) {
            throw "Tracked snapshot file lost its read-only protection: '$trackedPath'."
        }
    }
}

function New-NekoSshGitSourceSnapshot {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)][string]$DestinationRoot,
        [Parameter(Mandatory)][string]$TrustedParent,
        [Parameter(Mandatory)]$Identity,
        [string]$GitExecutable = 'git',
        [switch]$ReadOnlyTrackedFiles
    )

    $root = (Resolve-Path -LiteralPath $RepositoryRoot).Path
    $parent = (Resolve-Path -LiteralPath $TrustedParent).Path
    Assert-NekoSshRegularDirectory -Path $parent
    $destination = [IO.Path]::GetFullPath($DestinationRoot)
    if (-not (Test-NekoSshChildPath -Path $destination -Parent $parent)) {
        throw "Source snapshot must stay below '$parent'."
    }
    if (Test-Path -LiteralPath $destination) {
        throw "Source snapshot destination already exists: $destination"
    }

    $currentIdentity = Assert-NekoSshGitSourceIntegrity `
        -RepositoryRoot $root `
        -GitExecutable $GitExecutable `
        -Expected $Identity
    $gitPath = $currentIdentity.GitExecutable
    $inventory = Get-NekoSshHeadInventory `
        -GitExecutable $gitPath `
        -RepositoryRoot $root `
        -Commit $currentIdentity.Commit
    [void][IO.Directory]::CreateDirectory($destination)
    Assert-NekoSshRegularDirectory -Path $destination
    try {
        foreach ($entry in $inventory.Entries) {
            $segments = @(([string]$entry.Path).Split('/'))
            $currentParent = $destination
            for ($index = 0; $index -lt ($segments.Count - 1); $index += 1) {
                $currentParent = [IO.Path]::GetFullPath(
                    (Join-Path $currentParent $segments[$index])
                )
                if (-not (Test-NekoSshChildPath -Path $currentParent -Parent $destination)) {
                    throw "Snapshot directory escaped its root: '$($entry.Path)'."
                }
                if (-not (Test-Path -LiteralPath $currentParent)) {
                    [void][IO.Directory]::CreateDirectory($currentParent)
                }
                Assert-NekoSshRegularDirectory -Path $currentParent
            }
            $destinationPath = [IO.Path]::GetFullPath(
                (Join-Path $currentParent $segments[$segments.Count - 1])
            )
            if (-not (Test-NekoSshChildPath -Path $destinationPath -Parent $destination)) {
                throw "Snapshot file escaped its root: '$($entry.Path)'."
            }
            Export-NekoSshGitBlob `
                -GitExecutable $gitPath `
                -RepositoryRoot $root `
                -ObjectId ([string]$entry.Object) `
                -Destination $destinationPath
        }
        Assert-NekoSshSnapshotFiles `
            -SnapshotRoot $destination `
            -TrackedPaths $inventory.Paths
        Assert-NekoSshTrackedBlobBytes `
            -GitExecutable $gitPath `
            -RepositoryRoot $root `
            -TreeRoot $destination `
            -Entries $inventory.Entries `
            -Context 'Exported source snapshot'
        if ($ReadOnlyTrackedFiles) {
            foreach ($trackedPath in $inventory.Paths) {
                $platformPath = $trackedPath.Replace('/', [IO.Path]::DirectorySeparatorChar)
                $item = Get-Item -LiteralPath (Join-Path $destination $platformPath) -Force
                $item.IsReadOnly = $true
            }
            Assert-NekoSshSnapshotFiles `
                -SnapshotRoot $destination `
                -TrackedPaths $inventory.Paths `
                -RequireReadOnly
        }
        $snapshot = [pscustomobject]@{
            Path = $destination
            Branch = $currentIdentity.Branch
            Commit = $currentIdentity.Commit
            Tree = $currentIdentity.Tree
            SnapshotSha256 = $currentIdentity.SnapshotSha256
            DigestFormat = $currentIdentity.DigestFormat
            TrackedFileCount = $currentIdentity.TrackedFileCount
            ReadOnlyTrackedFiles = [bool]$ReadOnlyTrackedFiles
            GitExecutable = $gitPath
        }
        $snapshot.PSObject.TypeNames.Insert(0, $script:SourceSnapshotTypeName)
        return $snapshot
    }
    catch {
        Remove-NekoSshOwnedTemporaryTree `
            -Path $destination `
            -TrustedParent $parent
        throw
    }
}

function Assert-NekoSshGitSourceSnapshotIntegrity {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$RepositoryRoot,
        [Parameter(Mandatory)]$Snapshot,
        [string]$GitExecutable = 'git'
    )

    $root = (Resolve-Path -LiteralPath $RepositoryRoot).Path
    $snapshotRoot = (Resolve-Path -LiteralPath ([string]$Snapshot.Path)).Path
    $gitPath = Resolve-NekoSshGitExecutable -GitExecutable $GitExecutable
    $inventory = Get-NekoSshHeadInventory `
        -GitExecutable $gitPath `
        -RepositoryRoot $root `
        -Commit ([string]$Snapshot.Commit)
    if (
        [string]$inventory.SnapshotSha256 -cne [string]$Snapshot.SnapshotSha256 -or
        [string]$inventory.DigestFormat -cne [string]$Snapshot.DigestFormat -or
        [int]$inventory.Paths.Count -ne [int]$Snapshot.TrackedFileCount
    ) {
        throw 'Source snapshot metadata no longer matches its committed Git inventory.'
    }
    Assert-NekoSshSnapshotFiles `
        -SnapshotRoot $snapshotRoot `
        -TrackedPaths $inventory.Paths `
        -RequireReadOnly:([bool]$Snapshot.ReadOnlyTrackedFiles)
    Assert-NekoSshTrackedBlobBytes `
        -GitExecutable $gitPath `
        -RepositoryRoot $root `
        -TreeRoot $snapshotRoot `
        -Entries $inventory.Entries `
        -Context 'Tracked source snapshot'
    return $Snapshot
}

Export-ModuleMember -Function @(
    'Assert-NekoSshGitSourceIntegrity',
    'New-NekoSshGitSourceSnapshot',
    'Assert-NekoSshGitSourceSnapshotIntegrity'
)
