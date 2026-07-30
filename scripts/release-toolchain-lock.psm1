Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:ToolchainLockTypeName = 'NekoSSH.ReleaseToolchainLock'
$script:ToolchainLockSchemaVersion = 1
$script:ToolchainLockPlatform = 'windows-x64'
$script:RequiredToolNames = @(
    'powershell',
    'git',
    'gh',
    'node',
    'npm',
    'rustc',
    'cargo',
    'cargoAudit',
    'perl',
    'nasm',
    'vswhere',
    'signtool'
)
$script:StrictJsonNumberRegex = [regex]::new(
    '\G-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?',
    [Text.RegularExpressions.RegexOptions]::CultureInvariant
)

$signingPolicyModulePath = Join-Path $PSScriptRoot 'release-signing-policy.psm1'
Import-Module -Name $signingPolicyModulePath -Force -ErrorAction Stop

function Skip-ToolchainJsonWhitespace {
    param([Parameter(Mandatory)][hashtable]$State)

    while (
        $State.Index -lt $State.Text.Length -and
        $State.Text[$State.Index] -in @([char]0x20, [char]0x09, [char]0x0a, [char]0x0d)
    ) {
        $State.Index += 1
    }
}

function Read-StrictToolchainJsonString {
    param([Parameter(Mandatory)][hashtable]$State)

    if ($State.Index -ge $State.Text.Length -or $State.Text[$State.Index] -ne '"') {
        throw "Expected a JSON string at character $($State.Index)."
    }
    $start = $State.Index
    $State.Index += 1
    $closed = $false
    while ($State.Index -lt $State.Text.Length) {
        $character = $State.Text[$State.Index]
        if ([int]$character -lt 0x20) {
            throw "A JSON string contains an unescaped control character at character $($State.Index)."
        }
        if ($character -eq '"') {
            $State.Index += 1
            $closed = $true
            break
        }
        if ($character -eq '\') {
            $State.Index += 1
            if ($State.Index -ge $State.Text.Length) {
                throw 'A JSON string ends in an incomplete escape sequence.'
            }
            $escape = $State.Text[$State.Index]
            if ($escape -eq 'u') {
                if ($State.Index + 4 -ge $State.Text.Length) {
                    throw 'A JSON string contains an incomplete Unicode escape.'
                }
                $hex = $State.Text.Substring($State.Index + 1, 4)
                if ($hex -cnotmatch '^[0-9A-Fa-f]{4}$') {
                    throw "A JSON string contains an invalid Unicode escape at character $($State.Index)."
                }
                $State.Index += 5
                continue
            }
            if ($escape -notin @('"', '\', '/', 'b', 'f', 'n', 'r', 't')) {
                throw "A JSON string contains an invalid escape at character $($State.Index)."
            }
        }
        $State.Index += 1
    }
    if (-not $closed) {
        throw 'A JSON string is unterminated.'
    }

    $token = $State.Text.Substring($start, $State.Index - $start)
    try {
        return [string]($token | ConvertFrom-Json)
    }
    catch {
        throw "Unable to decode a JSON string: $($_.Exception.Message)"
    }
}

function Assert-StrictToolchainJsonValue {
    param(
        [Parameter(Mandatory)][hashtable]$State,
        [int]$Depth = 0
    )

    if ($Depth -gt 16) {
        throw 'The toolchain lock exceeds the maximum JSON nesting depth.'
    }
    Skip-ToolchainJsonWhitespace -State $State
    if ($State.Index -ge $State.Text.Length) {
        throw 'The toolchain lock ends before a JSON value is complete.'
    }

    $character = $State.Text[$State.Index]
    if ($character -eq '{') {
        $State.Index += 1
        Skip-ToolchainJsonWhitespace -State $State
        if ($State.Index -lt $State.Text.Length -and $State.Text[$State.Index] -eq '}') {
            $State.Index += 1
            return
        }
        $exactNames = New-Object 'Collections.Generic.HashSet[string]' (
            [StringComparer]::Ordinal
        )
        $caseInsensitiveNames = New-Object 'Collections.Generic.HashSet[string]' (
            [StringComparer]::OrdinalIgnoreCase
        )
        while ($true) {
            Skip-ToolchainJsonWhitespace -State $State
            $name = Read-StrictToolchainJsonString -State $State
            if (-not $exactNames.Add($name)) {
                throw "The toolchain lock contains a duplicate JSON property: '$name'."
            }
            if (-not $caseInsensitiveNames.Add($name)) {
                throw "The toolchain lock contains ambiguous JSON property casing: '$name'."
            }
            Skip-ToolchainJsonWhitespace -State $State
            if ($State.Index -ge $State.Text.Length -or $State.Text[$State.Index] -ne ':') {
                throw "Expected ':' after JSON property '$name'."
            }
            $State.Index += 1
            Assert-StrictToolchainJsonValue -State $State -Depth ($Depth + 1)
            Skip-ToolchainJsonWhitespace -State $State
            if ($State.Index -ge $State.Text.Length) {
                throw 'The toolchain lock contains an unterminated JSON object.'
            }
            if ($State.Text[$State.Index] -eq '}') {
                $State.Index += 1
                return
            }
            if ($State.Text[$State.Index] -ne ',') {
                throw "Expected ',' or '}' at character $($State.Index)."
            }
            $State.Index += 1
        }
    }

    if ($character -eq '[') {
        $State.Index += 1
        Skip-ToolchainJsonWhitespace -State $State
        if ($State.Index -lt $State.Text.Length -and $State.Text[$State.Index] -eq ']') {
            $State.Index += 1
            return
        }
        while ($true) {
            Assert-StrictToolchainJsonValue -State $State -Depth ($Depth + 1)
            Skip-ToolchainJsonWhitespace -State $State
            if ($State.Index -ge $State.Text.Length) {
                throw 'The toolchain lock contains an unterminated JSON array.'
            }
            if ($State.Text[$State.Index] -eq ']') {
                $State.Index += 1
                return
            }
            if ($State.Text[$State.Index] -ne ',') {
                throw "Expected ',' or ']' at character $($State.Index)."
            }
            $State.Index += 1
        }
    }

    if ($character -eq '"') {
        [void](Read-StrictToolchainJsonString -State $State)
        return
    }
    foreach ($literal in @('true', 'false', 'null')) {
        if (
            $State.Text.Length - $State.Index -ge $literal.Length -and
            [string]::CompareOrdinal(
                $State.Text,
                $State.Index,
                $literal,
                0,
                $literal.Length
            ) -eq 0
        ) {
            $State.Index += $literal.Length
            return
        }
    }
    $number = $script:StrictJsonNumberRegex.Match($State.Text, $State.Index)
    if ($number.Success) {
        $State.Index += $number.Length
        return
    }
    throw "Invalid JSON value at character $($State.Index)."
}

function Assert-StrictToolchainJsonDocument {
    param([Parameter(Mandatory)][string]$Text)

    $state = @{ Text = $Text; Index = 0 }
    if ($Text.Length -gt 0 -and $Text[0] -eq [char]0xfeff) {
        $state.Index = 1
    }
    Assert-StrictToolchainJsonValue -State $state
    Skip-ToolchainJsonWhitespace -State $state
    if ($state.Index -ne $Text.Length) {
        throw "The toolchain lock has trailing JSON content at character $($state.Index)."
    }
}

function Assert-ExactToolchainJsonFields {
    param(
        [Parameter(Mandatory)]$Object,
        [Parameter(Mandatory)][string[]]$Expected,
        [Parameter(Mandatory)][string]$Context
    )

    if ($null -eq $Object -or $Object -isnot [pscustomobject]) {
        throw "$Context must be a JSON object."
    }
    $actual = @($Object.PSObject.Properties.Name | Sort-Object -CaseSensitive)
    $expectedSorted = @($Expected | Sort-Object -CaseSensitive)
    if (($actual -join "`n") -cne ($expectedSorted -join "`n")) {
        throw "$Context fields are incomplete or unexpected."
    }
}

function Get-BytesSha256 {
    param([Parameter(Mandatory)][byte[]]$Bytes)

    $sha256 = [Security.Cryptography.SHA256]::Create()
    try {
        return (
            [BitConverter]::ToString($sha256.ComputeHash($Bytes))
        ).Replace('-', '').ToLowerInvariant()
    }
    finally {
        $sha256.Dispose()
    }
}

function Assert-LocalRegularToolFile {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Context
    )

    if (-not [IO.Path]::IsPathRooted($Path)) {
        throw "$Context path must be absolute."
    }
    $fullPath = [IO.Path]::GetFullPath($Path)
    if ($Path -cne $fullPath) {
        throw "$Context path must already be a canonical absolute path."
    }
    if ($fullPath -cnotmatch '^[A-Za-z]:\\') {
        throw "$Context path must be on a local drive-letter volume."
    }
    $root = [IO.Path]::GetPathRoot($fullPath)
    $drive = [IO.DriveInfo]::new($root)
    if (-not $drive.IsReady -or $drive.DriveType -ne [IO.DriveType]::Fixed) {
        throw "$Context path must be on a ready local fixed volume."
    }

    $current = $root
    $relative = $fullPath.Substring($root.Length)
    foreach ($segment in @($relative -split '[\\/]+' | Where-Object { $_ })) {
        $current = Join-Path $current $segment
        if (-not (Test-Path -LiteralPath $current)) {
            throw "$Context path does not exist: $fullPath"
        }
        $item = Get-Item -LiteralPath $current -Force
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "$Context path traverses a reparse point: $current"
        }
    }
    $file = Get-Item -LiteralPath $fullPath -Force
    if ($file.PSIsContainer) {
        throw "$Context path is not a regular file: $fullPath"
    }
    return $file
}

function Get-StableToolFileObservation {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Context
    )

    $before = Assert-LocalRegularToolFile -Path $Path -Context $Context
    $beforeLength = [long]$before.Length
    $beforeWrite = $before.LastWriteTimeUtc.Ticks
    [byte[]]$bytes = [IO.File]::ReadAllBytes($Path)
    if ([long]$bytes.Length -ne $beforeLength) {
        throw "$Context changed size while it was being read."
    }
    $sha256 = Get-BytesSha256 -Bytes $bytes
    [Array]::Clear($bytes, 0, $bytes.Length)

    $authenticode = Get-AuthenticodeSignature -LiteralPath $Path
    $status = [string]$authenticode.Status
    $signature = if ($status -ceq 'NotSigned') {
        [ordered]@{
            status = 'NotSigned'
            subject = $null
            thumbprint = $null
            spkiSha256 = $null
            timestamped = $false
        }
    } elseif ($status -ceq 'Valid') {
        if ($null -eq $authenticode.SignerCertificate) {
            throw "$Context reports a valid Authenticode signature without a signer certificate."
        }
        [ordered]@{
            status = 'Valid'
            subject = [string]$authenticode.SignerCertificate.Subject
            thumbprint = $authenticode.SignerCertificate.Thumbprint.ToUpperInvariant()
            spkiSha256 = Get-CertificateSpkiSha256 -Certificate $authenticode.SignerCertificate
            timestamped = ($null -ne $authenticode.TimeStamperCertificate)
        }
    } else {
        throw "$Context has an unacceptable Authenticode status: $status"
    }

    $after = Assert-LocalRegularToolFile -Path $Path -Context $Context
    if (
        [long]$after.Length -ne $beforeLength -or
        $after.LastWriteTimeUtc.Ticks -ne $beforeWrite
    ) {
        throw "$Context changed while its identity was being observed."
    }
    [byte[]]$verificationBytes = [IO.File]::ReadAllBytes($Path)
    try {
        if ((Get-BytesSha256 -Bytes $verificationBytes) -cne $sha256) {
            throw "$Context changed while its signature was being observed."
        }
    }
    finally {
        [Array]::Clear($verificationBytes, 0, $verificationBytes.Length)
    }

    return [pscustomobject]@{
        Path = $Path
        Length = $beforeLength
        Sha256 = $sha256
        Signature = [pscustomobject]$signature
    }
}

function ConvertTo-NativeToolArgument {
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

function Get-NativeToolOutput {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$ArgumentList,
        [Parameter(Mandatory)][string]$Context
    )

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $FilePath
    $startInfo.WorkingDirectory = Split-Path -Parent $FilePath
    $startInfo.Arguments = (
        $ArgumentList |
            ForEach-Object { ConvertTo-NativeToolArgument -Value ([string]$_) }
    ) -join ' '
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.StandardOutputEncoding = [Text.UTF8Encoding]::new($false, $true)
    $startInfo.StandardErrorEncoding = [Text.UTF8Encoding]::new($false, $true)
    $environment = $startInfo.EnvironmentVariables
    if ($null -eq $environment) {
        $environment = $startInfo.EnvironmentVariables
    }
    if ($null -eq $environment) {
        throw "$Context could not materialize an isolated process environment."
    }
    $environment.Clear()
    $systemDirectory = [Environment]::SystemDirectory
    $windowsDirectory = [IO.Directory]::GetParent($systemDirectory).FullName
    $environment['SystemRoot'] = $windowsDirectory
    $environment['WINDIR'] = $windowsDirectory
    $environment['COMSPEC'] = Join-Path $systemDirectory 'cmd.exe'
    $environment['PATH'] = @(
        (Split-Path -Parent $FilePath),
        $systemDirectory,
        $windowsDirectory
    ) -join [IO.Path]::PathSeparator
    $environment['PATHEXT'] = '.COM;.EXE;.BAT;.CMD'
    $environment['VSLANG'] = '1033'
    $environment['LC_ALL'] = 'C'
    $environment['LANG'] = 'C'
    $environment['NO_COLOR'] = '1'
    $environment['GIT_TERMINAL_PROMPT'] = '0'
    $environment['GCM_INTERACTIVE'] = 'Never'
    $environment['GIT_CONFIG_NOSYSTEM'] = '1'
    $environment['GIT_CONFIG_GLOBAL'] = 'NUL'
    $environment['GIT_NO_REPLACE_OBJECTS'] = '1'
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) {
            throw "$Context did not start."
        }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0) {
            throw "$Context failed with exit code $($process.ExitCode): $($stderr.Trim())"
        }
        if (-not [string]::IsNullOrWhiteSpace($stdout)) {
            return $stdout.Trim()
        }
        return $stderr.Trim()
    }
    finally {
        $process.Dispose()
    }
}

function Get-ObservedToolVersion {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][Collections.IDictionary]$Tools
    )

    $path = [string]$Tools[$Name].Path
    $output = switch ($Name) {
        'powershell' {
            $processPath = [IO.Path]::GetFullPath(
                [Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
            )
            if ($processPath -cne $path) {
                throw "The locked PowerShell path is not the current host process: $path"
            }
            $PSVersionTable.PSVersion.ToString()
            break
        }
        'git' {
            Get-NativeToolOutput -FilePath $path -ArgumentList @('--version') -Context 'git version probe'
            break
        }
        'gh' {
            (
                Get-NativeToolOutput -FilePath $path -ArgumentList @('--version') -Context 'gh version probe'
            ).Split(@("`r`n", "`n"), [StringSplitOptions]::RemoveEmptyEntries)[0]
            break
        }
        'node' {
            Get-NativeToolOutput -FilePath $path -ArgumentList @('--version') -Context 'Node.js version probe'
            break
        }
        'npm' {
            Get-NativeToolOutput `
                -FilePath ([string]$Tools['node'].Path) `
                -ArgumentList @($path, '--version') `
                -Context 'npm version probe'
            break
        }
        'rustc' {
            Get-NativeToolOutput -FilePath $path -ArgumentList @('--version') -Context 'rustc version probe'
            break
        }
        'cargo' {
            Get-NativeToolOutput -FilePath $path -ArgumentList @('--version') -Context 'Cargo version probe'
            break
        }
        'cargoAudit' {
            Get-NativeToolOutput -FilePath $path -ArgumentList @('--version') -Context 'cargo-audit version probe'
            break
        }
        'perl' {
            Get-NativeToolOutput -FilePath $path -ArgumentList @('-e', 'print $^V') -Context 'Perl version probe'
            break
        }
        'nasm' {
            Get-NativeToolOutput -FilePath $path -ArgumentList @('-v') -Context 'NASM version probe'
            break
        }
        { $_ -in @('vswhere', 'signtool') } {
            (Get-Item -LiteralPath $path -Force).VersionInfo.ProductVersion.Trim()
            break
        }
        default {
            throw "Unsupported tool version probe: $Name"
        }
    }
    $version = ([string]$output).Trim()
    if (
        [string]::IsNullOrWhiteSpace($version) -or
        $version.Length -gt 512 -or
        $version -match '[\r\n]'
    ) {
        throw "$Name returned an invalid exact version identity."
    }
    return $version
}

function Assert-ExpectedToolRecordShape {
    param(
        [Parameter(Mandatory)]$Record,
        [Parameter(Mandatory)][string]$Name
    )

    Assert-ExactToolchainJsonFields `
        -Object $Record `
        -Expected @('path', 'length', 'sha256', 'version', 'signature') `
        -Context "Toolchain lock tools.$Name"
    if (
        $Record.path -isnot [string] -or
        $Record.version -isnot [string] -or
        $Record.sha256 -isnot [string] -or
        [string]::IsNullOrWhiteSpace($Record.version) -or
        $Record.version.Length -gt 512 -or
        $Record.version -match '[\r\n]' -or
        $Record.sha256 -cnotmatch '^[0-9a-f]{64}$' -or
        -not ($Record.length -is [int] -or $Record.length -is [long]) -or
        [long]$Record.length -le 0
    ) {
        throw "Toolchain lock tools.$Name identity fields are invalid."
    }
    Assert-ExactToolchainJsonFields `
        -Object $Record.signature `
        -Expected @('status', 'subject', 'thumbprint', 'spkiSha256', 'timestamped') `
        -Context "Toolchain lock tools.$Name.signature"
    if ($Record.signature.timestamped -isnot [bool]) {
        throw "Toolchain lock tools.$Name.signature.timestamped must be a JSON boolean."
    }
    if ($Record.signature.status -ceq 'NotSigned') {
        if (
            $null -ne $Record.signature.subject -or
            $null -ne $Record.signature.thumbprint -or
            $null -ne $Record.signature.spkiSha256 -or
            $Record.signature.timestamped
        ) {
            throw "Unsigned toolchain lock tools.$Name must use explicit null signer fields and timestamped=false."
        }
    } elseif ($Record.signature.status -ceq 'Valid') {
        if (
            $Record.signature.subject -isnot [string] -or
            [string]::IsNullOrWhiteSpace($Record.signature.subject) -or
            $Record.signature.subject.Length -gt 2048 -or
            $Record.signature.thumbprint -isnot [string] -or
            $Record.signature.thumbprint -cnotmatch '^[0-9A-F]{40}$' -or
            $Record.signature.spkiSha256 -isnot [string] -or
            $Record.signature.spkiSha256 -cnotmatch '^[0-9a-f]{64}$'
        ) {
            throw "Signed toolchain lock tools.$Name contains an invalid signer identity."
        }
    } else {
        throw "Toolchain lock tools.$Name.signature.status must be Valid or NotSigned."
    }
}

function Test-ToolObservationMatchesExpected {
    param(
        [Parameter(Mandatory)]$Observed,
        [Parameter(Mandatory)]$Expected,
        [Parameter(Mandatory)][string]$Name,
        [switch]$IncludeVersion
    )

    if (
        [long]$Observed.Length -ne [long]$Expected.length -or
        [string]$Observed.Sha256 -cne [string]$Expected.sha256 -or
        [string]$Observed.Signature.status -cne [string]$Expected.signature.status -or
        [string]$Observed.Signature.subject -cne [string]$Expected.signature.subject -or
        [string]$Observed.Signature.thumbprint -cne [string]$Expected.signature.thumbprint -or
        [string]$Observed.Signature.spkiSha256 -cne [string]$Expected.signature.spkiSha256 -or
        [bool]$Observed.Signature.timestamped -ne [bool]$Expected.signature.timestamped -or
        ($IncludeVersion -and [string]$Observed.Version -cne [string]$Expected.version)
    ) {
        throw "Locked tool identity mismatch: $Name"
    }
}

function Get-ToolchainObservations {
    param(
        [Parameter(Mandatory)][Collections.IDictionary]$ToolPaths,
        [Collections.IDictionary]$ExpectedTools
    )

    $paths = New-Object 'Collections.Generic.HashSet[string]' (
        [StringComparer]::OrdinalIgnoreCase
    )
    $observed = [ordered]@{}
    foreach ($name in $script:RequiredToolNames) {
        if (-not $ToolPaths.Contains($name)) {
            throw "Tool path map is missing '$name'."
        }
        $path = [string]$ToolPaths[$name]
        if (-not $paths.Add($path)) {
            throw "Toolchain lock paths must be unique; duplicate path: $path"
        }
        $fileObservation = Get-StableToolFileObservation `
            -Path $path `
            -Context "Locked tool '$name'"
        $observed[$name] = [pscustomobject]@{
            Name = $name
            Path = $fileObservation.Path
            Length = $fileObservation.Length
            Sha256 = $fileObservation.Sha256
            Version = $null
            Signature = $fileObservation.Signature
        }
        if ($null -ne $ExpectedTools) {
            Test-ToolObservationMatchesExpected `
                -Observed $observed[$name] `
                -Expected $ExpectedTools[$name] `
                -Name $name
        }
    }

    foreach ($name in $script:RequiredToolNames) {
        $observed[$name].Version = Get-ObservedToolVersion -Name $name -Tools $observed
        if ($null -ne $ExpectedTools) {
            Test-ToolObservationMatchesExpected `
                -Observed $observed[$name] `
                -Expected $ExpectedTools[$name] `
                -Name $name `
                -IncludeVersion
        }
    }
    if ($observed['rustc'].Sha256 -ceq $observed['cargo'].Sha256) {
        throw (
            'Locked rustc and Cargo are byte-identical rustup-style proxies. ' +
            'Resolve and lock the actual toolchain binaries (for example with rustup which).'
        )
    }
    return $observed
}

function ConvertTo-ToolchainManifestIdentity {
    param(
        [Parameter(Mandatory)][string]$PolicySha256,
        [Parameter(Mandatory)][Collections.IDictionary]$Tools
    )

    $records = @(
        foreach ($name in $script:RequiredToolNames) {
            $tool = $Tools[$name]
            [ordered]@{
                name = $name
                length = [long]$tool.Length
                sha256 = [string]$tool.Sha256
                version = [string]$tool.Version
                signature = [ordered]@{
                    status = [string]$tool.Signature.status
                    subject = $tool.Signature.subject
                    thumbprint = $tool.Signature.thumbprint
                    spkiSha256 = $tool.Signature.spkiSha256
                    timestamped = [bool]$tool.Signature.timestamped
                }
            }
        }
    )
    return [ordered]@{
        policySha256 = $PolicySha256
        platform = $script:ToolchainLockPlatform
        target = 'x86_64-pc-windows-msvc'
        tools = $records
    }
}

function Import-NekoSshReleaseToolchainLock {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$ExpectedSha256
    )

    if ($ExpectedSha256 -cnotmatch '^[0-9a-f]{64}$') {
        throw 'Expected toolchain lock SHA-256 must be exactly 64 lowercase hexadecimal characters.'
    }
    $resolvedPath = (Resolve-Path -LiteralPath $Path).Path
    $lockFile = Assert-LocalRegularToolFile -Path $resolvedPath -Context 'Toolchain lock'
    if ($lockFile.Length -le 0 -or $lockFile.Length -gt 256KB) {
        throw 'Toolchain lock must be between 1 byte and 256 KiB.'
    }
    [byte[]]$bytes = [IO.File]::ReadAllBytes($resolvedPath)
    if ([long]$bytes.Length -ne [long]$lockFile.Length) {
        throw 'Toolchain lock changed size while it was being read.'
    }
    $actualPolicySha256 = Get-BytesSha256 -Bytes $bytes
    if ($actualPolicySha256 -cne $ExpectedSha256) {
        throw 'Toolchain lock SHA-256 does not match the independent expected anchor.'
    }
    $offset = 0
    if (
        $bytes.Length -ge 3 -and
        $bytes[0] -eq 0xef -and
        $bytes[1] -eq 0xbb -and
        $bytes[2] -eq 0xbf
    ) {
        $offset = 3
    }
    $utf8 = [Text.UTF8Encoding]::new($false, $true)
    try {
        $text = $utf8.GetString($bytes, $offset, $bytes.Length - $offset)
    }
    catch {
        throw "Toolchain lock is not valid UTF-8: $($_.Exception.Message)"
    }
    finally {
        [Array]::Clear($bytes, 0, $bytes.Length)
    }
    Assert-StrictToolchainJsonDocument -Text $text
    try {
        $lock = $text | ConvertFrom-Json
    }
    catch {
        throw "Unable to parse toolchain lock JSON: $($_.Exception.Message)"
    }
    Assert-ExactToolchainJsonFields `
        -Object $lock `
        -Expected @('schemaVersion', 'platform', 'tools') `
        -Context 'Toolchain lock'
    if (
        -not ($lock.schemaVersion -is [int] -or $lock.schemaVersion -is [long]) -or
        [long]$lock.schemaVersion -ne $script:ToolchainLockSchemaVersion -or
        $lock.platform -cne $script:ToolchainLockPlatform
    ) {
        throw 'Toolchain lock schemaVersion or platform is unsupported.'
    }
    Assert-ExactToolchainJsonFields `
        -Object $lock.tools `
        -Expected $script:RequiredToolNames `
        -Context 'Toolchain lock tools'

    $toolPaths = [ordered]@{}
    $expectedTools = [ordered]@{}
    foreach ($name in $script:RequiredToolNames) {
        $record = $lock.tools.PSObject.Properties[$name].Value
        Assert-ExpectedToolRecordShape -Record $record -Name $name
        $toolPaths[$name] = [string]$record.path
        $expectedTools[$name] = $record
    }
    $observed = Get-ToolchainObservations -ToolPaths $toolPaths -ExpectedTools $expectedTools
    $result = [pscustomobject]@{
        TypeName = $script:ToolchainLockTypeName
        LockPath = $resolvedPath
        PolicySha256 = $actualPolicySha256
        Platform = $script:ToolchainLockPlatform
        Tools = $observed
        ExpectedTools = $expectedTools
    }
    $result.PSObject.TypeNames.Insert(0, $script:ToolchainLockTypeName)
    return $result
}

function Assert-NekoSshReleaseToolchainLock {
    [CmdletBinding()]
    param([Parameter(Mandatory)]$Toolchain)

    if (
        $null -eq $Toolchain -or
        $Toolchain.PSObject.TypeNames -cnotcontains $script:ToolchainLockTypeName
    ) {
        throw 'Toolchain must come from Import-NekoSshReleaseToolchainLock.'
    }
    $lockFile = Assert-LocalRegularToolFile -Path $Toolchain.LockPath -Context 'Toolchain lock'
    [byte[]]$bytes = [IO.File]::ReadAllBytes($Toolchain.LockPath)
    try {
        if (
            [long]$bytes.Length -ne [long]$lockFile.Length -or
            (Get-BytesSha256 -Bytes $bytes) -cne $Toolchain.PolicySha256
        ) {
            throw 'Toolchain lock changed after it was imported.'
        }
    }
    finally {
        [Array]::Clear($bytes, 0, $bytes.Length)
    }
    $paths = [ordered]@{}
    foreach ($name in $script:RequiredToolNames) {
        $paths[$name] = [string]$Toolchain.ExpectedTools[$name].path
    }
    $observed = Get-ToolchainObservations `
        -ToolPaths $paths `
        -ExpectedTools $Toolchain.ExpectedTools
    $Toolchain.Tools = $observed
    return $Toolchain
}

function Assert-NekoSshToolchainLockOutsideRepository {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$LockPath,
        [Parameter(Mandatory)][string]$RepositoryRoot
    )

    $fullLockPath = [IO.Path]::GetFullPath($LockPath)
    $fullRepositoryRoot = [IO.Path]::GetFullPath($RepositoryRoot).TrimEnd(
        [IO.Path]::DirectorySeparatorChar,
        [IO.Path]::AltDirectorySeparatorChar
    )
    if (
        [string]::Equals(
            $fullLockPath,
            $fullRepositoryRoot,
            [StringComparison]::OrdinalIgnoreCase
        ) -or
        $fullLockPath.StartsWith(
            "$fullRepositoryRoot$([IO.Path]::DirectorySeparatorChar)",
            [StringComparison]::OrdinalIgnoreCase
        )
    ) {
        throw 'The builder toolchain lock must be stored outside the Git repository.'
    }
}

function Assert-NekoSshCompleteProductionToolchainCoverage {
    [CmdletBinding()]
    param()

    throw (
        'Production release is fail-closed until an independently anchored builder definition ' +
        'binds the complete transitive release environment: rustdoc and the Rust sysroot; the ' +
        'installed Tauri CLI/native binding; MSVC, Windows SDK, NSIS/WiX executables plus their ' +
        'compiler/linker input trees; npm/Cargo registries, caches and configuration; Git tag ' +
        'signature verifiers, trust stores and the noninteractive credential path; clean-VM ' +
        'validation tools; and protected check/use identities that close tool replacement ' +
        'TOCTOU. The current phase-1 lock is sufficient only for -AllowUnsigned validation.'
    )
}

function Get-NekoSshReleaseToolchainManifestIdentity {
    [CmdletBinding()]
    param([Parameter(Mandatory)]$Toolchain)

    [void](Assert-NekoSshReleaseToolchainLock -Toolchain $Toolchain)
    return ConvertTo-ToolchainManifestIdentity `
        -PolicySha256 $Toolchain.PolicySha256 `
        -Tools $Toolchain.Tools
}

function Assert-NekoSshReleaseManifestToolchainIdentity {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]$ManifestToolchain,
        [Parameter(Mandatory)]$Toolchain
    )

    $expected = Get-NekoSshReleaseToolchainManifestIdentity -Toolchain $Toolchain
    $expectedJson = $expected | ConvertTo-Json -Depth 8 -Compress
    $actualJson = $ManifestToolchain | ConvertTo-Json -Depth 8 -Compress
    if ($actualJson -cne $expectedJson) {
        throw 'Release manifest toolchain identity does not match the independently anchored builder lock.'
    }
    return $Toolchain
}

function New-NekoSshReleaseToolchainLockDocument {
    [CmdletBinding()]
    param([Parameter(Mandatory)][Collections.IDictionary]$ToolPaths)

    $actualNames = @($ToolPaths.Keys | ForEach-Object { [string]$_ } | Sort-Object -CaseSensitive)
    $expectedNames = @($script:RequiredToolNames | Sort-Object -CaseSensitive)
    if (($actualNames -join "`n") -cne ($expectedNames -join "`n")) {
        throw 'Tool path map must contain the exact required tool name set.'
    }
    $canonicalPaths = [ordered]@{}
    foreach ($name in $script:RequiredToolNames) {
        $canonicalPaths[$name] = [IO.Path]::GetFullPath(
            (Resolve-Path -LiteralPath ([string]$ToolPaths[$name])).Path
        )
    }
    $observed = Get-ToolchainObservations -ToolPaths $canonicalPaths
    $toolRecords = [ordered]@{}
    foreach ($name in $script:RequiredToolNames) {
        $tool = $observed[$name]
        $toolRecords[$name] = [ordered]@{
            path = [string]$tool.Path
            length = [long]$tool.Length
            sha256 = [string]$tool.Sha256
            version = [string]$tool.Version
            signature = [ordered]@{
                status = [string]$tool.Signature.status
                subject = $tool.Signature.subject
                thumbprint = $tool.Signature.thumbprint
                spkiSha256 = $tool.Signature.spkiSha256
                timestamped = [bool]$tool.Signature.timestamped
            }
        }
    }
    return [ordered]@{
        schemaVersion = $script:ToolchainLockSchemaVersion
        platform = $script:ToolchainLockPlatform
        tools = $toolRecords
    }
}

Export-ModuleMember -Function @(
    'Import-NekoSshReleaseToolchainLock',
    'Assert-NekoSshReleaseToolchainLock',
    'Assert-NekoSshToolchainLockOutsideRepository',
    'Assert-NekoSshCompleteProductionToolchainCoverage',
    'Get-NekoSshReleaseToolchainManifestIdentity',
    'Assert-NekoSshReleaseManifestToolchainIdentity',
    'New-NekoSshReleaseToolchainLockDocument'
)
