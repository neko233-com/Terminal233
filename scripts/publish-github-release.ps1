[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ReleaseDirectory,
    [Parameter(Mandatory)][string]$ToolchainLockPath,
    [Parameter(Mandatory)][string]$ExpectedToolchainLockSha256,
    [Parameter(Mandatory)][string]$ExpectedTagSignerFingerprint,
    [string]$Repository = 'neko233-com/nekossh',
    [string]$WindowsSmokeReceiptPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$toolchainLockModulePath = Join-Path $PSScriptRoot 'release-toolchain-lock.psm1'
Import-Module -Name $toolchainLockModulePath -Force -ErrorAction Stop
$lockedToolchain = Import-NekoSshReleaseToolchainLock `
    -Path $ToolchainLockPath `
    -ExpectedSha256 $ExpectedToolchainLockSha256
Assert-NekoSshToolchainLockOutsideRepository `
    -LockPath $lockedToolchain.LockPath `
    -RepositoryRoot $repositoryRoot
$gitExecutable = [string]$lockedToolchain.Tools.git.Path
$ghExecutable = [string]$lockedToolchain.Tools.gh.Path
if (
    $Repository -cnotmatch '^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,99})/[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,99})$' -or
    $Repository.Contains('..')
) {
    throw 'Repository must be one canonical GitHub owner/name pair.'
}
$canonicalRepositoryUrl = "https://github.com/$Repository.git"
$normalizedExpectedTagSignerFingerprint = if (
    $ExpectedTagSignerFingerprint -cmatch '^(?:[0-9A-Fa-f]{40}|[0-9A-Fa-f]{64})$'
) {
    $ExpectedTagSignerFingerprint.ToUpperInvariant()
} elseif (
    $ExpectedTagSignerFingerprint -cmatch '^SHA256:[A-Za-z0-9+/]{43}$'
) {
    $ExpectedTagSignerFingerprint
} else {
    throw (
        'ExpectedTagSignerFingerprint must be an exact 40- or 64-digit hexadecimal OpenPGP ' +
        'fingerprint or an SHA256: SSH key fingerprint.'
    )
}
Assert-NekoSshCompleteProductionToolchainCoverage
$signingPolicyPath = Join-Path $repositoryRoot 'release-signing-policy.json'
$signingPolicyModulePath = Join-Path $PSScriptRoot 'release-signing-policy.psm1'
$sourceIntegrityModulePath = Join-Path $PSScriptRoot 'git-source-integrity.psm1'
$validationScriptPath = Join-Path $PSScriptRoot 'test-windows-release-installers.ps1'
$generatedOutputRoot = [IO.Path]::GetFullPath((Join-Path $repositoryRoot 'output'))
$releaseRoot = [IO.Path]::GetFullPath((Join-Path $generatedOutputRoot 'release'))
$resolvedReleaseDirectory = (Resolve-Path -LiteralPath $ReleaseDirectory).Path
$releaseInputLocks = [Collections.Generic.List[IO.FileStream]]::new()
$lockedReleaseInputPaths = [Collections.Generic.HashSet[string]]::new(
    [StringComparer]::OrdinalIgnoreCase
)

function Test-ChildPath {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Parent
    )

    $normalizedParent = $Parent.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    return $Path.StartsWith("$normalizedParent$([IO.Path]::DirectorySeparatorChar)", [StringComparison]::OrdinalIgnoreCase)
}

function Assert-NotReparsePoint {
    param([Parameter(Mandatory)][string[]]$Paths)

    foreach ($path in $Paths) {
        $item = Get-Item -LiteralPath $path -Force
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Release paths must not traverse a reparse point: $path"
        }
    }
}

function Assert-PathHasNoReparsePoints {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$TrustedRoot
    )

    $fullRoot = [IO.Path]::GetFullPath($TrustedRoot)
    $trustedVolumeRoot = [IO.Path]::GetPathRoot($fullRoot)
    if ([string]::IsNullOrWhiteSpace($trustedVolumeRoot)) {
        throw "Trusted root does not have a filesystem root: $fullRoot"
    }
    if ($fullRoot.Length -gt $trustedVolumeRoot.Length) {
        $fullRoot = $fullRoot.TrimEnd(
            [IO.Path]::DirectorySeparatorChar,
            [IO.Path]::AltDirectorySeparatorChar
        )
    }
    $fullPath = [IO.Path]::GetFullPath($Path)
    if ($fullPath -ne $fullRoot -and -not (Test-ChildPath -Path $fullPath -Parent $fullRoot)) {
        throw "Path is outside its trusted root: $fullPath"
    }

    foreach ($candidate in @($fullRoot, $fullPath) | Select-Object -Unique) {
        $volumeRoot = [IO.Path]::GetPathRoot($candidate)
        if ([string]::IsNullOrWhiteSpace($volumeRoot)) {
            throw "Release path does not have a filesystem root: $candidate"
        }
        $current = $volumeRoot
        if (Test-Path -LiteralPath $current) {
            Assert-NotReparsePoint -Paths @($current)
        }
        $relative = $candidate.Substring($volumeRoot.Length).TrimStart(
            [IO.Path]::DirectorySeparatorChar,
            [IO.Path]::AltDirectorySeparatorChar
        )
        foreach ($segment in @($relative -split '[\\/]+' | Where-Object { $_ })) {
            $current = Join-Path $current $segment
            if (Test-Path -LiteralPath $current) {
                Assert-NotReparsePoint -Paths @($current)
            }
        }
    }
}

function Assert-TreeHasNoReparsePoints {
    param([Parameter(Mandatory)][string]$Root)

    $pending = [Collections.Generic.Stack[string]]::new()
    $pending.Push($Root)
    while ($pending.Count -gt 0) {
        $directory = $pending.Pop()
        $directoryItem = Get-Item -LiteralPath $directory -Force
        if (
            -not $directoryItem.PSIsContainer -or
            ($directoryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0
        ) {
            throw "Git administrative path is not a regular directory: $directory"
        }
        foreach ($entry in @(Get-ChildItem -LiteralPath $directory -Force)) {
            if (($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Git administrative data contains a reparse point: $($entry.FullName)"
            }
            if ($entry.PSIsContainer) {
                $pending.Push($entry.FullName)
            }
        }
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

function Get-CheckedOutput {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$ArgumentList
    )

    $output = (& $FilePath @ArgumentList 2>&1 | Out-String).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "'$FilePath $($ArgumentList -join ' ')' failed with exit code $LASTEXITCODE."
    }
    return $output
}

function ConvertTo-PublishNativeArgument {
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

function Invoke-HardenedPublishGit {
    param(
        [Parameter(Mandatory)][string[]]$ArgumentList,
        [int[]]$AllowedExitCodes = @(0)
    )

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $gitExecutable
    $startInfo.WorkingDirectory = $repositoryRoot
    $hardenedArgumentList = @(
        '--no-optional-locks',
        '--no-replace-objects',
        '-c', 'core.fsmonitor=false',
        '-c', 'core.hooksPath=NUL',
        '-c', 'core.attributesFile=NUL',
        '-c', 'core.unsetenvvars=',
        '-c', 'credential.helper=',
        '-c', 'fetch.fsckObjects=true',
        '-c', 'transfer.fsckObjects=true',
        '-c', "safe.directory=$repositoryRoot"
    ) + $ArgumentList
    $startInfo.Arguments = (
        $hardenedArgumentList |
            ForEach-Object { ConvertTo-PublishNativeArgument -Value ([string]$_) }
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
        throw 'Unable to materialize the locked Git process environment.'
    }
    foreach ($key in @($environment.Keys)) {
        if (
            [string]$key -cmatch '^(?i:GIT_)' -or
            [string]$key -cmatch '^(?i:SSL_CERT_(?:FILE|DIR)|CURL_CA_BUNDLE|HTTPS?_PROXY|ALL_PROXY|NO_PROXY)$'
        ) {
            $environment.Remove([string]$key)
        }
    }
    $environment['GIT_CONFIG_NOSYSTEM'] = '1'
    $environment['GIT_CONFIG_GLOBAL'] = 'NUL'
    $environment['GIT_CONFIG_COUNT'] = '0'
    $environment['GIT_NO_LAZY_FETCH'] = '1'
    $environment['GIT_NO_REPLACE_OBJECTS'] = '1'
    $environment['GIT_TERMINAL_PROMPT'] = '0'
    $environment['GCM_INTERACTIVE'] = 'Never'

    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) {
            throw 'The locked Git process did not start.'
        }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        if ($process.ExitCode -notin $AllowedExitCodes) {
            $summary = if ([string]::IsNullOrWhiteSpace($stderr)) {
                $stdout.Trim()
            } else {
                $stderr.Trim()
            }
            throw (
                "Locked Git command failed with exit code $($process.ExitCode): " +
                "git $($hardenedArgumentList -join ' ')`n$summary"
            )
        }
        return [pscustomobject]@{
            ExitCode = [int]$process.ExitCode
            StdOut = [string]$stdout
            StdErr = [string]$stderr
        }
    }
    finally {
        $process.Dispose()
    }
}

function Assert-NoUnsafePublishGitConfiguration {
    $result = Invoke-HardenedPublishGit `
        -ArgumentList @(
            'config',
            '--local',
            '--no-includes',
            '--name-only',
            '--list'
        )
    foreach ($name in @($result.StdOut -split "`r?`n" | Where-Object { $_ })) {
        if (
            $name -match '^(?i:(?:include|includeif)\.)' -or
            $name -match '^(?i:url\..*\.(?:push)?insteadof)$' -or
            $name -match '^(?i:credential\.)' -or
            $name -match '^(?i:hook\.)' -or
            $name -match '^(?i:http\.)' -or
            $name -match '^(?i:protocol\.)' -or
            $name -match '^(?i:fetch\.bundleURI)$' -or
            $name -match '^(?i:gc\.recentObjectsHook)$' -or
            $name -match '^(?i:extensions\.(?:partialClone|worktreeConfig))$' -or
            $name -match '^(?i:core\.(?:alternateRefsCommand|askPass|excludesFile|hooksPath|fsmonitor|sshCommand|gitProxy|unsetenvvars))$' -or
            $name -match '^(?i:remote\..*\.(?:partialCloneFilter|promisor|proxy|proxyAuthMethod|uploadpack|receivepack|vcs))$'
        ) {
            throw "Release publication rejects unsafe local Git configuration: $name"
        }
    }
}

function Protect-ReleaseInput {
    param([Parameter(Mandatory)][string]$Path)

    $fullPath = [IO.Path]::GetFullPath((Resolve-Path -LiteralPath $Path).Path)
    if ($lockedReleaseInputPaths.Contains($fullPath)) {
        return
    }
    Assert-PathHasNoReparsePoints -Path $fullPath -TrustedRoot $repositoryRoot
    $stream = [IO.FileStream]::new(
        $fullPath,
        [IO.FileMode]::Open,
        [IO.FileAccess]::Read,
        [IO.FileShare]::Read
    )
    try {
        if (-not $lockedReleaseInputPaths.Add($fullPath)) {
            throw "Release input lock bookkeeping rejected a duplicate path: $fullPath"
        }
        $releaseInputLocks.Add($stream)
    }
    catch {
        $stream.Dispose()
        throw
    }
}

function Invoke-HardenedMainFetch {
    Assert-NoUnsafePublishGitConfiguration
    [void](Invoke-HardenedPublishGit -ArgumentList @(
        'fetch',
        '--no-tags',
        '--no-recurse-submodules',
        '--no-auto-maintenance',
        '--no-write-commit-graph',
        '--prune',
        '--force',
        $canonicalRepositoryUrl,
        '+refs/heads/main:refs/remotes/origin/main'
    ))
}

function Get-HardenedPublishGitOutput {
    param(
        [Parameter(Mandatory)][string[]]$ArgumentList,
        [switch]$IncludeStandardError
    )

    $result = Invoke-HardenedPublishGit -ArgumentList $ArgumentList
    if ($IncludeStandardError) {
        return "$($result.StdOut)`n$($result.StdErr)".Trim()
    }
    return $result.StdOut.Trim()
}

function Assert-NoConfiguredGitVerifierProgram {
    $result = Invoke-HardenedPublishGit `
        -ArgumentList @(
            'config',
            '--show-origin',
            '--get-regexp',
            '^gpg\..*program$'
        ) `
        -AllowedExitCodes @(0, 1)
    if (
        $result.ExitCode -eq 0 -or
        -not [string]::IsNullOrWhiteSpace($result.StdOut) -or
        -not [string]::IsNullOrWhiteSpace($result.StdErr)
    ) {
        throw (
            'Git system, global, or repository configuration must not select a GPG/SSH ' +
            'verification program for production tag verification.'
        )
    }
}

function Assert-LocalSignedTagIdentity {
    param(
        [Parameter(Mandatory)][string]$TagName,
        [Parameter(Mandatory)][string]$CommitSha,
        [Parameter(Mandatory)][string]$ExpectedTagObjectSha,
        [Parameter(Mandatory)][string]$ExpectedSignerFingerprint
    )

    if ($ExpectedTagObjectSha -cnotmatch '^[0-9a-f]{40}$') {
        throw 'Expected local tag object must be a full lowercase SHA-1 object identifier.'
    }
    Assert-NoConfiguredGitVerifierProgram
    $tagType = Get-HardenedPublishGitOutput -ArgumentList @(
        'cat-file',
        '-t',
        $ExpectedTagObjectSha
    )
    if ($tagType -cne 'tag') {
        throw "Expected object $ExpectedTagObjectSha is not an annotated Git tag object."
    }
    $tagRefBefore = Get-HardenedPublishGitOutput -ArgumentList @(
        'rev-parse',
        "refs/tags/$TagName"
    )
    if ($tagRefBefore -cne $ExpectedTagObjectSha) {
        throw "Local tag $TagName no longer identifies the expected signed tag object."
    }
    $tagCommit = Get-HardenedPublishGitOutput -ArgumentList @(
        'rev-parse',
        "$ExpectedTagObjectSha`^{commit}"
    )
    if ($tagCommit -cne $CommitSha) {
        throw "Signed tag object $ExpectedTagObjectSha does not identify commit $CommitSha."
    }

    $verificationConfig = if (
        $ExpectedSignerFingerprint.StartsWith('SHA256:', [StringComparison]::Ordinal)
    ) {
        @(
            '-c', 'gpg.format=ssh',
            '-c', 'gpg.ssh.program=ssh-keygen.exe'
        )
    } else {
        @(
            '-c', 'gpg.format=openpgp',
            '-c', 'gpg.program=gpg.exe',
            '-c', 'gpg.openpgp.program=gpg.exe'
        )
    }
    $tagVerification = Get-HardenedPublishGitOutput `
        -ArgumentList ($verificationConfig + @(
            'verify-tag',
            '--raw',
            $ExpectedTagObjectSha
        )) `
        -IncludeStandardError
    $escapedSigner = [regex]::Escape($ExpectedSignerFingerprint)
    $signerMatches = if (
        $ExpectedSignerFingerprint.StartsWith('SHA256:', [StringComparison]::Ordinal)
    ) {
        $tagVerification -cmatch "(?:key\s+)?$escapedSigner(?:\s|$)"
    } else {
        $tagVerification -cmatch "(?m)VALIDSIG\s+$escapedSigner(?:\s|$)"
    }
    if (-not $signerMatches) {
        throw 'Signed release tag identity does not match the independently expected fingerprint.'
    }
    $tagRefAfter = Get-HardenedPublishGitOutput -ArgumentList @(
        'rev-parse',
        "refs/tags/$TagName"
    )
    if ($tagRefAfter -cne $ExpectedTagObjectSha) {
        throw "Local tag $TagName changed while its signature was being verified."
    }
}

function ConvertTo-NormalizedReleaseText {
    param([AllowEmptyString()][string]$Value)

    return ($Value -replace "`r`n?", "`n").TrimEnd("`n")
}

function Assert-ExactMapKeys {
    param(
        [Parameter(Mandatory)]$Map,
        [Parameter(Mandatory)][string[]]$Expected,
        [Parameter(Mandatory)][string]$Context
    )

    if ($Map -isnot [Collections.IDictionary]) {
        throw "$Context must be a PowerShell data-file hashtable."
    }
    $actual = @($Map.Keys | ForEach-Object { [string]$_ } | Sort-Object -CaseSensitive)
    $expectedSorted = @($Expected | Sort-Object -CaseSensitive)
    if (($actual -join "`n") -cne ($expectedSorted -join "`n")) {
        throw "$Context fields are incomplete or unexpected."
    }
}

function Get-SourceVersion {
    $package = Get-Content -LiteralPath (Join-Path $repositoryRoot 'package.json') -Raw | ConvertFrom-Json
    Add-Type -AssemblyName System.Web.Extensions
    $serializer = New-Object System.Web.Script.Serialization.JavaScriptSerializer
    $serializer.MaxJsonLength = [int]::MaxValue
    $lock = $serializer.DeserializeObject(
        (Get-Content -LiteralPath (Join-Path $repositoryRoot 'package-lock.json') -Raw)
    )
    $tauri = Get-Content -LiteralPath (Join-Path $repositoryRoot 'src-tauri/tauri.conf.json') -Raw | ConvertFrom-Json
    $cargo = Get-Content -LiteralPath (Join-Path $repositoryRoot 'src-tauri/Cargo.toml') -Raw
    $cargoVersionMatch = [regex]::Match($cargo, '(?ms)^\[package\]\s.*?^version\s*=\s*"([^"]+)"')
    if (-not $cargoVersionMatch.Success) {
        throw 'Unable to read the package version from src-tauri/Cargo.toml.'
    }
    $webLauncherCargo = Get-Content -LiteralPath (Join-Path $repositoryRoot 'web-launcher/Cargo.toml') -Raw
    $webLauncherVersionMatch = [regex]::Match(
        $webLauncherCargo,
        '(?ms)^\[package\]\s.*?^version\s*=\s*"([^"]+)"'
    )
    if (-not $webLauncherVersionMatch.Success) {
        throw 'Unable to read the package version from web-launcher/Cargo.toml.'
    }
    $versions = @((
        [string]$package.version,
        [string]$lock['version'],
        [string]$lock['packages']['']['version'],
        [string]$tauri.version,
        [string]$cargoVersionMatch.Groups[1].Value,
        [string]$webLauncherVersionMatch.Groups[1].Value
    ) | Select-Object -Unique)
    if ($versions.Count -ne 1) {
        throw "Source versions do not match: $($versions -join ', ')."
    }
    return $versions[0]
}

function Get-MsiProperty {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Property
    )

    $installer = $null
    $database = $null
    $view = $null
    $record = $null
    try {
        $installer = New-Object -ComObject WindowsInstaller.Installer
        $database = $installer.OpenDatabase($Path, 0)
        $view = $database.OpenView(
            "SELECT ``Value`` FROM ``Property`` WHERE ``Property``='$Property'"
        )
        [void]$view.Execute()
        $record = $view.Fetch()
        if ($null -eq $record) {
            throw "MSI property is missing: $Property"
        }
        return $record.StringData(1)
    }
    finally {
        if ($null -ne $record) { [Runtime.InteropServices.Marshal]::FinalReleaseComObject($record) | Out-Null }
        if ($null -ne $view) { [Runtime.InteropServices.Marshal]::FinalReleaseComObject($view) | Out-Null }
        if ($null -ne $database) { [Runtime.InteropServices.Marshal]::FinalReleaseComObject($database) | Out-Null }
        if ($null -ne $installer) { [Runtime.InteropServices.Marshal]::FinalReleaseComObject($installer) | Out-Null }
    }
}

function Get-MsiUpgradeCode {
    param([Parameter(Mandatory)][string]$Path)

    $installer = $null
    $database = $null
    $view = $null
    $record = $null
    try {
        $installer = New-Object -ComObject WindowsInstaller.Installer
        $database = $installer.OpenDatabase($Path, 0)
        $view = $database.OpenView('SELECT `UpgradeCode` FROM `Upgrade`')
        [void]$view.Execute()
        $upgradeCodes = [Collections.Generic.List[string]]::new()
        while ($true) {
            $record = $view.Fetch()
            if ($null -eq $record) { break }
            try {
                $parsed = [guid]::Empty
                $rawValue = $record.StringData(1)
                if (-not [guid]::TryParseExact($rawValue, 'B', [ref]$parsed)) {
                    throw "MSI Upgrade table contains an invalid UpgradeCode: '$rawValue'."
                }
                $canonical = $parsed.ToString('B').ToUpperInvariant()
                if (-not $upgradeCodes.Contains($canonical)) {
                    $upgradeCodes.Add($canonical)
                }
            }
            finally {
                [Runtime.InteropServices.Marshal]::FinalReleaseComObject($record) | Out-Null
                $record = $null
            }
        }
        if ($upgradeCodes.Count -ne 1) {
            throw 'MSI Upgrade table must contain exactly one distinct UpgradeCode.'
        }
        return $upgradeCodes[0]
    }
    finally {
        if ($null -ne $record) { [Runtime.InteropServices.Marshal]::FinalReleaseComObject($record) | Out-Null }
        if ($null -ne $view) { [Runtime.InteropServices.Marshal]::FinalReleaseComObject($view) | Out-Null }
        if ($null -ne $database) { [Runtime.InteropServices.Marshal]::FinalReleaseComObject($database) | Out-Null }
        if ($null -ne $installer) { [Runtime.InteropServices.Marshal]::FinalReleaseComObject($installer) | Out-Null }
    }
}

function Assert-GitHubTagTarget {
    param(
        [Parameter(Mandatory)][string]$RepositoryName,
        [Parameter(Mandatory)][string]$TagName,
        [Parameter(Mandatory)][string]$CommitSha,
        [Parameter(Mandatory)][string]$ExpectedTagObjectSha
    )

    $githubRefJson = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
        'api',
        "repos/$RepositoryName/git/ref/tags/$TagName"
    )
    $githubRef = $githubRefJson | ConvertFrom-Json
    if (
        $githubRef.object.type -cne 'tag' -or
        [string]$githubRef.object.sha -cne $ExpectedTagObjectSha
    ) {
        throw (
            "GitHub tag $TagName does not identify the exact locally verified tag object " +
            "$ExpectedTagObjectSha."
        )
    }
    $githubTagJson = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
        'api',
        "repos/$RepositoryName/git/tags/$ExpectedTagObjectSha"
    )
    $githubTag = $githubTagJson | ConvertFrom-Json
    if ($githubTag.object.type -cne 'commit' -or $githubTag.object.sha -cne $CommitSha) {
        throw "GitHub repository tag $TagName is not bound to manifest commit $CommitSha."
    }
}

function Assert-GitHubReleaseTagRuleset {
    param(
        [Parameter(Mandatory)][string]$RepositoryName,
        [Parameter(Mandatory)][string]$TagName
    )

    if ($TagName -cnotmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
        throw 'The protected release tag must be a stable v-prefixed SemVer tag.'
    }
    $rulesetsJson = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
        'api',
        '-H',
        'Accept: application/vnd.github+json',
        '-H',
        'X-GitHub-Api-Version: 2026-03-10',
        "repos/$RepositoryName/rulesets?includes_parents=true&targets=tag&per_page=100"
    )
    $rulesets = @($rulesetsJson | ConvertFrom-Json)
    $matchingRulesets = [Collections.Generic.List[object]]::new()
    foreach ($summary in $rulesets) {
        if ($summary.PSObject.Properties.Name -cnotcontains 'id') {
            continue
        }
        $detailJson = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
            'api',
            '-H',
            'Accept: application/vnd.github+json',
            '-H',
            'X-GitHub-Api-Version: 2026-03-10',
            "repos/$RepositoryName/rulesets/$($summary.id)?includes_parents=true"
        )
        $detail = $detailJson | ConvertFrom-Json
        if (
            [string]$detail.target -cne 'tag' -or
            [string]$detail.enforcement -cne 'active' -or
            $detail.PSObject.Properties.Name -cnotcontains 'conditions' -or
            $detail.conditions.PSObject.Properties.Name -cnotcontains 'ref_name' -or
            $detail.conditions.ref_name.PSObject.Properties.Name -cnotcontains 'include' -or
            $detail.conditions.ref_name.PSObject.Properties.Name -cnotcontains 'exclude' -or
            $detail.PSObject.Properties.Name -cnotcontains 'rules' -or
            $detail.PSObject.Properties.Name -cnotcontains 'bypass_actors'
        ) {
            continue
        }
        $includes = @($detail.conditions.ref_name.include)
        $excludes = @($detail.conditions.ref_name.exclude)
        $ruleTypes = @($detail.rules | ForEach-Object { [string]$_.type })
        if (
            $includes -cnotcontains 'refs/tags/v*' -or
            $excludes.Count -ne 0 -or
            @($detail.bypass_actors).Count -ne 0 -or
            $ruleTypes -cnotcontains 'deletion' -or
            $ruleTypes -cnotcontains 'update' -or
            $ruleTypes -cnotcontains 'non_fast_forward'
        ) {
            continue
        }
        $matchingRulesets.Add($detail)
    }
    if ($matchingRulesets.Count -eq 0) {
        throw (
            'GitHub must expose an active, no-bypass tag ruleset for refs/tags/v* that ' +
            'blocks update, deletion, and non-fast-forward changes. Missing permissions, ' +
            'unsupported API responses, or weaker rules fail closed.'
        )
    }
}

function Assert-GitHubManualReleaseControls {
    param(
        [Parameter(Mandatory)][string]$RepositoryName,
        [Parameter(Mandatory)][string]$TagName
    )

    $actionsEnabled = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
        'api',
        "repos/$RepositoryName/actions/permissions",
        '--jq',
        '.enabled'
    )
    if ($actionsEnabled -ne 'false') {
        throw 'GitHub Actions must remain disabled during manual release publication.'
    }
    $immutablePolicy = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
        'api',
        '-H',
        'Accept: application/vnd.github+json',
        '-H',
        'X-GitHub-Api-Version: 2026-03-10',
        "repos/$RepositoryName/immutable-releases"
    ) | ConvertFrom-Json
    if ($immutablePolicy.enabled -isnot [bool] -or $immutablePolicy.enabled -ne $true) {
        throw 'GitHub immutable releases must remain enabled during publication.'
    }
    Assert-GitHubReleaseTagRuleset -RepositoryName $RepositoryName -TagName $TagName
}

function Assert-ManualPublishState {
    param(
        [Parameter(Mandatory)][string]$RepositoryName,
        [Parameter(Mandatory)][string]$TagName,
        [Parameter(Mandatory)][string]$CommitSha,
        [Parameter(Mandatory)][string]$ExpectedTagObjectSha,
        [Parameter(Mandatory)][string]$ExpectedTagSignerFingerprint
    )

    Invoke-HardenedMainFetch
    $currentSource = Assert-NekoSshGitSourceIntegrity `
        -RepositoryRoot $repositoryRoot `
        -GitExecutable $gitExecutable
    if ([string]$currentSource.Commit -cne $CommitSha) {
        throw 'HEAD or origin/main changed during release publication.'
    }
    $originUrl = Get-HardenedPublishGitOutput -ArgumentList @('remote', 'get-url', 'origin')
    $escapedRepository = [regex]::Escape($RepositoryName)
    if (
        $originUrl -notmatch "(?i)^(https://github\.com/$escapedRepository(?:\.git)?/?|git@github\.com:$escapedRepository(?:\.git)?)$"
    ) {
        throw "Git origin '$originUrl' does not match GitHub repository '$RepositoryName'."
    }
    Assert-LocalSignedTagIdentity `
        -TagName $TagName `
        -CommitSha $CommitSha `
        -ExpectedTagObjectSha $ExpectedTagObjectSha `
        -ExpectedSignerFingerprint $ExpectedTagSignerFingerprint
    Assert-GitHubManualReleaseControls `
        -RepositoryName $RepositoryName `
        -TagName $TagName
    Assert-GitHubTagTarget `
        -RepositoryName $RepositoryName `
        -TagName $TagName `
        -CommitSha $CommitSha `
        -ExpectedTagObjectSha $ExpectedTagObjectSha
}

function Invoke-CheckedGitHubReleaseMutation {
    param(
        [Parameter(Mandatory)][string[]]$ArgumentList,
        [Parameter(Mandatory)][string]$RepositoryName,
        [Parameter(Mandatory)][string]$TagName,
        [Parameter(Mandatory)][string]$CommitSha,
        [Parameter(Mandatory)][string]$ExpectedTagObjectSha,
        [Parameter(Mandatory)][string]$ExpectedTagSignerFingerprint
    )

    if (
        $ArgumentList.Count -lt 2 -or
        $ArgumentList[0] -cne 'release' -or
        $ArgumentList[1] -cnotin @('create', 'upload', 'edit')
    ) {
        throw 'The guarded GitHub mutation wrapper only accepts release create/upload/edit.'
    }
    Assert-ManualPublishState `
        -RepositoryName $RepositoryName `
        -TagName $TagName `
        -CommitSha $CommitSha `
        -ExpectedTagObjectSha $ExpectedTagObjectSha `
        -ExpectedTagSignerFingerprint $ExpectedTagSignerFingerprint
    Invoke-Checked -FilePath $ghExecutable -ArgumentList $ArgumentList
    Assert-ManualPublishState `
        -RepositoryName $RepositoryName `
        -TagName $TagName `
        -CommitSha $CommitSha `
        -ExpectedTagObjectSha $ExpectedTagObjectSha `
        -ExpectedTagSignerFingerprint $ExpectedTagSignerFingerprint
}

function Invoke-CheckedTagPushMutation {
    param(
        [Parameter(Mandatory)][string]$RepositoryName,
        [Parameter(Mandatory)][string]$TagName,
        [Parameter(Mandatory)][string]$CommitSha,
        [Parameter(Mandatory)][string]$ExpectedTagObjectSha,
        [Parameter(Mandatory)][string]$ExpectedTagSignerFingerprint
    )

    Invoke-HardenedMainFetch
    $currentSource = Assert-NekoSshGitSourceIntegrity `
        -RepositoryRoot $repositoryRoot `
        -GitExecutable $gitExecutable
    if ([string]$currentSource.Commit -cne $CommitSha) {
        throw 'HEAD or origin/main changed immediately before tag publication.'
    }
    Assert-LocalSignedTagIdentity `
        -TagName $TagName `
        -CommitSha $CommitSha `
        -ExpectedTagObjectSha $ExpectedTagObjectSha `
        -ExpectedSignerFingerprint $ExpectedTagSignerFingerprint
    Assert-GitHubManualReleaseControls `
        -RepositoryName $RepositoryName `
        -TagName $TagName
    $remoteBefore = Get-HardenedPublishGitOutput -ArgumentList @(
        'ls-remote',
        $canonicalRepositoryUrl,
        "refs/tags/$TagName"
    )
    if (-not [string]::IsNullOrWhiteSpace($remoteBefore)) {
        throw "Remote tag $TagName appeared before its guarded push."
    }
    [void](Invoke-HardenedPublishGit -ArgumentList @(
        'push',
        '--no-verify',
        '--no-follow-tags',
        '--no-signed',
        '--no-push-option',
        $canonicalRepositoryUrl,
        "refs/tags/$TagName"
    ))
    Assert-ManualPublishState `
        -RepositoryName $RepositoryName `
        -TagName $TagName `
        -CommitSha $CommitSha `
        -ExpectedTagObjectSha $ExpectedTagObjectSha `
        -ExpectedTagSignerFingerprint $ExpectedTagSignerFingerprint
}

function Remove-OwnedFlatDirectory {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Parent
    )

    if (-not (Test-Path -LiteralPath $Path)) {
        return
    }
    if (-not (Test-ChildPath -Path $Path -Parent $Parent)) {
        throw "Refusing to clean a verification directory outside '$Parent': $Path"
    }
    Assert-PathHasNoReparsePoints -Path $Path -TrustedRoot $repositoryRoot
    Assert-NotReparsePoint -Paths @($Parent, $Path)
    $children = @(Get-ChildItem -LiteralPath $Path -Force)
    foreach ($child in $children) {
        if ($child.PSIsContainer -or (($child.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0)) {
            throw "Refusing to clean unexpected verification content: $($child.FullName)"
        }
    }
    foreach ($child in $children) {
        Remove-Item -LiteralPath $child.FullName -Force
    }
    Remove-Item -LiteralPath $Path -Force
}

if (-not (Test-ChildPath -Path $resolvedReleaseDirectory -Parent $releaseRoot)) {
    throw "Release directory must stay below '$releaseRoot'."
}
Assert-PathHasNoReparsePoints -Path $resolvedReleaseDirectory -TrustedRoot $repositoryRoot

$manifestPath = Join-Path $resolvedReleaseDirectory 'release-manifest.json'
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
    throw 'Release manifest is missing.'
}
Assert-PathHasNoReparsePoints -Path $manifestPath -TrustedRoot $repositoryRoot
Import-Module -Name $signingPolicyModulePath -Force
Import-Module -Name $sourceIntegrityModulePath -Force -ErrorAction Stop
$manifest = Import-NekoSshReleaseManifest -Path $manifestPath
if (
    -not ($manifest.schemaVersion -is [int] -or $manifest.schemaVersion -is [long]) -or
    [long]$manifest.schemaVersion -ne 4
) {
    throw 'Unsupported release manifest schema.'
}
[void](Assert-NekoSshReleaseManifestToolchainIdentity `
    -ManifestToolchain $manifest.toolchain `
    -Toolchain $lockedToolchain)
if ([string]$manifest.repository -ne $Repository) { throw 'Release manifest repository does not match.' }
$isPrerelease = if ($manifest.prerelease -is [bool]) {
    $manifest.prerelease
} else {
    throw 'Release manifest prerelease must be a JSON boolean.'
}
if ($manifest.publishable -isnot [bool] -or $manifest.publishable -ne $true) {
    throw 'The release manifest is local validation output and is not publishable.'
}
$version = [string]$manifest.version
if ($version -notmatch '^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(?:(?:0|[1-9]\d*|[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|[A-Za-z-][0-9A-Za-z-]*))*))?$') {
    throw "Invalid release version: '$version'."
}
if ($isPrerelease -ne $version.Contains('-')) {
    throw 'Release manifest prerelease state must exactly match the SemVer prerelease suffix.'
}
if ($isPrerelease) {
    throw 'The production GitHub publisher only accepts a signed stable SemVer release.'
}
if ((Get-SourceVersion) -ne $version) {
    throw 'Release manifest version does not match the checked-out source version.'
}
$signingPolicy = Import-ReleaseSigningPolicy -Path $signingPolicyPath
$tag = "v$version"
if ([string]$manifest.tag -ne $tag) { throw 'Release manifest tag does not match its version.' }
$commit = [string]$manifest.commit
if ($commit -notmatch '^[0-9a-f]{40}$') { throw 'Release manifest commit is not a full Git SHA.' }
$shortCommit = $commit.Substring(0, 12)
if ([string]$manifest.shortCommit -cne $shortCommit) {
    throw 'Release manifest shortCommit does not match commit.'
}
if ((Split-Path $resolvedReleaseDirectory -Leaf) -ne "v$version-$shortCommit") {
    throw 'Release directory name does not match the manifest version and commit.'
}
$generatedAt = [DateTimeOffset]::MinValue
if (
    -not [DateTimeOffset]::TryParseExact(
        [string]$manifest.generatedAtUtc,
        'o',
        [Globalization.CultureInfo]::InvariantCulture,
        [Globalization.DateTimeStyles]::RoundtripKind,
        [ref]$generatedAt
    ) -or
    $generatedAt -gt [DateTimeOffset]::UtcNow.AddMinutes(5) -or
    $generatedAt -lt [DateTimeOffset]::UtcNow.AddDays(-7)
) {
    throw 'Release artifacts must have been generated within the last seven days.'
}
$requiredNode = (Get-Content -LiteralPath (Join-Path $repositoryRoot '.nvmrc') -Raw).Trim().TrimStart('v')
$sourcePackage = Get-Content -LiteralPath (Join-Path $repositoryRoot 'package.json') -Raw | ConvertFrom-Json
$requiredNpm = ([string]$sourcePackage.packageManager).Split('@')[-1]
$toolchainFile = Get-Content -LiteralPath (Join-Path $repositoryRoot 'rust-toolchain.toml') -Raw
$requiredRustMatch = [regex]::Match($toolchainFile, '(?m)^channel\s*=\s*"([^"]+)"')
$manifestToolsByName = @{}
foreach ($tool in $manifest.toolchain.tools) {
    $manifestToolsByName[[string]$tool.name] = $tool
}
if (
    -not $requiredRustMatch.Success -or
    ([string]$manifestToolsByName['node'].version).TrimStart('v') -cne $requiredNode -or
    [string]$manifestToolsByName['npm'].version -cne $requiredNpm -or
    [string]$manifestToolsByName['rustc'].version -cnotmatch "^rustc $([regex]::Escape($requiredRustMatch.Groups[1].Value)) " -or
    [string]$manifestToolsByName['cargo'].version -cnotmatch "^cargo $([regex]::Escape($requiredRustMatch.Groups[1].Value)) " -or
    [string]$manifestToolsByName['cargoAudit'].version -cnotmatch '\b0\.22\.2\b' -or
    [string]$manifest.toolchain.target -cne 'x86_64-pc-windows-msvc'
) {
    throw 'Release manifest toolchain identity does not match the pinned Windows build environment.'
}

$nsisName = "NekoSSH_${version}_x64-setup.exe"
$msiName = "NekoSSH_${version}_x64_zh-CN.msi"
$webPortableName = "NekoSSH_Web_${version}_windows_x64.zip"
$noticesName = 'THIRD_PARTY_NOTICES.txt'
$checksumName = 'SHA256SUMS.txt'
$notesName = 'RELEASE_NOTES.md'
$manifestName = 'release-manifest.json'
$smokeReceiptName = 'WINDOWS_SMOKE_RECEIPT.psd1'
$expectedAssetKinds = [ordered]@{}
$expectedAssetKinds[$msiName] = 'tauri-msi'
$expectedAssetKinds[$nsisName] = 'tauri-nsis'
$expectedAssetKinds[$webPortableName] = 'web-portable'
$expectedAssetKinds[$noticesName] = 'third-party-notices'
$expectedAssetNames = @($expectedAssetKinds.Keys | Sort-Object)
$manifestAssets = @($manifest.assets)
if ($manifestAssets.Count -ne 4) { throw 'Release manifest must describe the exact four Windows assets.' }
if ((@($manifestAssets.name | Sort-Object) -join "`n") -ne ($expectedAssetNames -join "`n")) {
    throw 'Release manifest asset names are not the expected NSIS/MSI/Web/notices set.'
}
foreach ($record in $manifestAssets) {
    if ([string]$record.kind -ne [string]$expectedAssetKinds[[string]$record.name]) {
        throw "Release manifest asset kind is invalid: $($record.name)"
    }
}
if ([string]$manifest.checksumFile.name -ne $checksumName) {
    throw 'Release manifest checksum filename is invalid.'
}
if ([string]$manifest.releaseNotesFile.name -ne $notesName) {
    throw 'Release manifest notes filename is invalid.'
}

$localFiles = @{}
$manifestRecordsByName = @{}
foreach ($record in @($manifestAssets) + @($manifest.checksumFile) + @($manifest.releaseNotesFile)) {
    $name = [string]$record.name
    if ($manifestRecordsByName.ContainsKey($name)) {
        throw "Duplicate release manifest record: $name"
    }
    $path = [IO.Path]::GetFullPath((Join-Path $resolvedReleaseDirectory $name))
    if (-not (Test-ChildPath -Path $path -Parent $resolvedReleaseDirectory)) {
        throw "Release asset escaped its directory: $name"
    }
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Release asset is missing: $path"
    }
    Assert-NotReparsePoint -Paths @($path)
    $file = Get-Item -LiteralPath $path
    $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($file.Length -ne [long]$record.bytes -or $hash -ne [string]$record.sha256) {
        throw "Release asset no longer matches its manifest: $name"
    }
    $localFiles[$name] = $path
    $manifestRecordsByName[$name] = $record
}
$manifestFile = Get-Item -LiteralPath $manifestPath
$manifestLocalRecord = [pscustomobject]@{
    name = $manifestName
    bytes = $manifestFile.Length
    sha256 = (Get-FileHash -LiteralPath $manifestPath -Algorithm SHA256).Hash.ToLowerInvariant()
}
if (
    [long]$manifestLocalRecord.bytes -ne [long]$manifest.SourceFileLength -or
    [string]$manifestLocalRecord.sha256 -cne [string]$manifest.SourceFileSha256
) {
    throw 'release-manifest.json changed after strict validation.'
}
$localFiles[$manifestName] = $manifestPath
$manifestRecordsByName[$manifestName] = $manifestLocalRecord
$notesPath = $localFiles[$notesName]
if ((Get-Item -LiteralPath $localFiles[$nsisName]).VersionInfo.ProductVersion -cne $version) {
    throw 'NSIS ProductVersion does not match the release manifest.'
}
$actualMsiIdentity = [ordered]@{
    productCode = Get-MsiProperty -Path $localFiles[$msiName] -Property 'ProductCode'
    productName = Get-MsiProperty -Path $localFiles[$msiName] -Property 'ProductName'
    manufacturer = Get-MsiProperty -Path $localFiles[$msiName] -Property 'Manufacturer'
    productLanguage = Get-MsiProperty -Path $localFiles[$msiName] -Property 'ProductLanguage'
    productVersion = Get-MsiProperty -Path $localFiles[$msiName] -Property 'ProductVersion'
    upgradeCode = Get-MsiUpgradeCode -Path $localFiles[$msiName]
}
$parsedProductCode = [guid]::Empty
if (-not [guid]::TryParseExact([string]$actualMsiIdentity.productCode, 'B', [ref]$parsedProductCode)) {
    throw 'MSI ProductCode is not a canonical braced GUID.'
}
$actualMsiIdentity.productCode = $parsedProductCode.ToString('B').ToUpperInvariant()
foreach ($field in @(
    'productCode',
    'productName',
    'manufacturer',
    'productLanguage',
    'productVersion',
    'upgradeCode'
)) {
    if ([string]$actualMsiIdentity[$field] -cne [string]$manifest.windowsInstaller.$field) {
        throw "MSI $field does not match the release manifest."
    }
}
if (
    [string]$actualMsiIdentity.productVersion -cne $version -or
    [string]$actualMsiIdentity.productName -cne 'NekoSSH' -or
    [string]$actualMsiIdentity.manufacturer -cne 'NekoSSH contributors' -or
    [string]$actualMsiIdentity.productLanguage -cne '2052' -or
    [string]$actualMsiIdentity.productCode -cnotmatch '^\{[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}\}$' -or
    [string]$actualMsiIdentity.upgradeCode -cne '{44F1588E-BCFD-5973-A955-237DAF92726D}'
) {
    throw 'MSI identity does not match the pinned Windows product policy.'
}

$expectedChecksumLines = $manifestAssets |
    Sort-Object name |
    ForEach-Object { "$($_.sha256)  $($_.name)" }
$actualChecksumLines = @(Get-Content -LiteralPath $localFiles[$checksumName])
if (($expectedChecksumLines -join "`n") -ne ($actualChecksumLines -join "`n")) {
    throw 'SHA256SUMS.txt does not match the release manifest.'
}

$signatureRecords = @($manifest.signatures)
if ($signatureRecords.Count -ne 3) { throw 'Release manifest must contain three distributed Tauri signature records.' }
$expectedSignatureNames = @($msiName, $nsisName, 'nekossh.exe') | Sort-Object
if ((@($signatureRecords.Name | Sort-Object) -join "`n") -ne ($expectedSignatureNames -join "`n")) {
    throw 'Release manifest Tauri signature records are incomplete or unexpected.'
}
$signingMode = [string]$manifest.signing.mode
$isUnsigned = $signingMode -eq 'unsigned-test'
if ($isUnsigned) {
    throw 'Unsigned artifacts are local validation output and can never be published to GitHub.'
} else {
    if ($signingMode -ne 'authenticode') {
        throw 'Release manifest signing mode is invalid.'
    }
    $expectedSigner = [string]$manifest.signing.expectedSignerThumbprint
    $policySigner = Get-ReleaseSigningPolicySigner `
        -Policy $signingPolicy `
        -Version $version `
        -Thumbprint $expectedSigner
    if (
        $expectedSigner -notmatch '^[0-9A-F]{40}$' -or
        $manifest.signing.timestampRequired -isnot [bool] -or
        $manifest.signing.timestampRequired -ne $true
    ) {
        throw 'Signed release manifest signing metadata is invalid.'
    }
    if (
        [string]$manifest.signing.expectedSignerSubject -cne $policySigner.Subject -or
        [string]$manifest.signing.expectedSignerSpkiSha256 -cne $policySigner.SpkiSha256 -or
        [string]$manifest.signing.policySha256 -cne $signingPolicy.Sha256
    ) {
        throw 'Signed release identity does not match the version-controlled signing policy.'
    }
    if (@($signatureRecords | Where-Object { $_.Status -ne 'Valid' -or $_.Timestamped -ne $true }).Count -ne 0) {
        throw 'A signed release requires valid, timestamped Authenticode signatures.'
    }
    if (@($signatureRecords | Where-Object SignerThumbprint -ne $expectedSigner).Count -ne 0) {
        throw 'Signed release manifest signer thumbprints are inconsistent.'
    }
    if (@($signatureRecords | Where-Object {
        $_.SignerSubject -cne $policySigner.Subject -or
        $_.SignerSpkiSha256 -cne $policySigner.SpkiSha256
    }).Count -ne 0) {
        throw 'Signed release manifest signer identities are inconsistent with policy.'
    }
}

foreach ($installerName in @($nsisName, $msiName)) {
    $signature = Get-AuthenticodeSignature -LiteralPath $localFiles[$installerName]
    if (
        $signature.Status.ToString() -ne 'Valid' -or
        $null -eq $signature.SignerCertificate -or
        $signature.SignerCertificate.Thumbprint.ToUpperInvariant() -ne $expectedSigner -or
        $null -eq $signature.TimeStamperCertificate
    ) {
        throw "Signed release asset failed Authenticode revalidation: $installerName"
    }
    [void](Assert-ReleaseSigningCertificate `
        -Certificate $signature.SignerCertificate `
        -Signer $policySigner)
}

$webVerifyArguments = @{
    ArchivePath = $localFiles[$webPortableName]
    ExpectedVersion = $version
    ExpectedCommit = $commit
    ExpectedProjectSignerThumbprint = $expectedSigner
    ExpectedProjectSignerSubject = $policySigner.Subject
    ExpectedProjectSignerSpkiSha256 = $policySigner.SpkiSha256
}
$localWebVerification = (
    & (Join-Path $PSScriptRoot 'verify-windows-web-portable.ps1') @webVerifyArguments |
        Out-String
).Trim() | ConvertFrom-Json
if (
    [string]$localWebVerification.selfTest -cne [string]$manifest.webRuntime.selfTest -or
    [string]$localWebVerification.version -cne [string]$manifest.webRuntime.version -or
    [string]$localWebVerification.archiveSha256 -cne
        [string]$manifestRecordsByName[$webPortableName].sha256 -or
    [string]$localWebVerification.node.version -cne [string]$manifest.webRuntime.node.version -or
    [string]$localWebVerification.node.sha256 -cne [string]$manifest.webRuntime.node.sha256 -or
    [string]$localWebVerification.node.status -cne [string]$manifest.webRuntime.node.status -or
    [string]$localWebVerification.node.signerSubject -cne
        [string]$manifest.webRuntime.node.signerSubject -or
    $localWebVerification.node.timestamped -ne $manifest.webRuntime.node.timestamped -or
    [string]$localWebVerification.launcher.status -cne
        [string]$manifest.webRuntime.launcher.status -or
    [string]$localWebVerification.launcher.signerThumbprint -cne
        [string]$manifest.webRuntime.launcher.signerThumbprint -or
    [string]$localWebVerification.launcher.signerSubject -cne
        [string]$manifest.webRuntime.launcher.signerSubject -or
    [string]$localWebVerification.launcher.signerSpkiSha256 -cne
        [string]$manifest.webRuntime.launcher.signerSpkiSha256 -or
    $localWebVerification.launcher.timestamped -ne
        $manifest.webRuntime.launcher.timestamped
) {
    throw 'The independent Web portable runtime no longer matches its verified manifest.'
}

if (-not $isPrerelease) {
    $resolvedSmokeReceipt = if ([string]::IsNullOrWhiteSpace($WindowsSmokeReceiptPath)) {
        Join-Path $resolvedReleaseDirectory $smokeReceiptName
    } else {
        [IO.Path]::GetFullPath($WindowsSmokeReceiptPath)
    }
    if (
        (Split-Path $resolvedSmokeReceipt -Leaf) -ne $smokeReceiptName -or
        -not (Test-ChildPath -Path $resolvedSmokeReceipt -Parent $resolvedReleaseDirectory) -or
        -not (Test-Path -LiteralPath $resolvedSmokeReceipt -PathType Leaf)
    ) {
        throw "A stable release requires $smokeReceiptName inside its release directory."
    }
    Assert-PathHasNoReparsePoints -Path $resolvedSmokeReceipt -TrustedRoot $repositoryRoot
    Protect-ReleaseInput -Path $resolvedSmokeReceipt
    $smokeSignature = Get-AuthenticodeSignature -LiteralPath $resolvedSmokeReceipt
    if (
        $smokeSignature.Status.ToString() -ne 'Valid' -or
        $null -eq $smokeSignature.SignerCertificate -or
        $smokeSignature.SignerCertificate.Thumbprint.ToUpperInvariant() -ne $expectedSigner -or
        $null -eq $smokeSignature.TimeStamperCertificate
    ) {
        throw 'The Windows smoke receipt is not signed and timestamped by the expected project signer.'
    }
    [void](Assert-ReleaseSigningCertificate `
        -Certificate $smokeSignature.SignerCertificate `
        -Signer $policySigner)
    $smokeReceipt = Import-PowerShellDataFile -LiteralPath $resolvedSmokeReceipt
    $expectedReceiptFields = @(
        'SchemaVersion',
        'Repository',
        'Version',
        'Commit',
        'TestedAtUtc',
        'CleanWindowsVm',
        'Elevated',
        'Architecture',
        'WindowsVersion',
        'ValidationNonce',
        'CleanVmImageId',
        'ValidationScriptSha256',
        'SigningPolicySha256',
        'UpgradeMode',
        'UpgradeFromVersion',
        'UpgradeFromNsisSha256',
        'UpgradeFromMsiSha256',
        'UpgradeFromSignerThumbprint',
        'Checks',
        'Files'
    )
    Assert-ExactMapKeys `
        -Map $smokeReceipt `
        -Expected $expectedReceiptFields `
        -Context 'Windows smoke receipt'
    if (
        -not ($smokeReceipt.SchemaVersion -is [int] -or $smokeReceipt.SchemaVersion -is [long]) -or
        [long]$smokeReceipt.SchemaVersion -ne 1 -or
        [string]$smokeReceipt.Repository -cne $Repository -or
        [string]$smokeReceipt.Version -cne $version -or
        [string]$smokeReceipt.Commit -cne $commit -or
        [string]$smokeReceipt.WindowsVersion -ceq '' -or
        $smokeReceipt.CleanWindowsVm -ne $true -or
        $smokeReceipt.CleanWindowsVm -isnot [bool] -or
        $smokeReceipt.Elevated -ne $true -or
        $smokeReceipt.Elevated -isnot [bool] -or
        [string]$smokeReceipt.Architecture -cne 'x64' -or
        [string]$smokeReceipt.ValidationNonce -cnotmatch '^[0-9a-f]{64}$' -or
        [string]$smokeReceipt.CleanVmImageId -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._:/@+-]{7,127}$' -or
        [string]$smokeReceipt.ValidationScriptSha256 -cne
            (Get-ReleaseTextSha256 -Path $validationScriptPath) -or
        [string]$smokeReceipt.SigningPolicySha256 -cne $signingPolicy.Sha256
    ) {
        throw 'The Windows smoke receipt identity or environment does not match the release.'
    }
    $testedAt = [DateTimeOffset]::MinValue
    if (
        -not [DateTimeOffset]::TryParseExact(
            [string]$smokeReceipt.TestedAtUtc,
            'o',
            [Globalization.CultureInfo]::InvariantCulture,
            [Globalization.DateTimeStyles]::RoundtripKind,
            [ref]$testedAt
        ) -or
        $testedAt -gt [DateTimeOffset]::UtcNow.AddMinutes(5) -or
        $testedAt -lt [DateTimeOffset]::UtcNow.AddDays(-7)
    ) {
        throw 'The Windows smoke receipt must be generated within seven days of publication.'
    }
    $expectedSmokeChecks = @(
        'MsiDowngradeBlocked',
        'MsiInstall',
        'MsiInstalledSignatures',
        'MsiNativeSelfTest',
        'MsiReinstall',
        'MsiUninstall',
        'MsiUpgrade',
        'NsisDowngradeBlocked',
        'NsisInstall',
        'NsisInstalledSignatures',
        'NsisNativeSelfTest',
        'NsisReinstall',
        'NsisUninstall',
        'NsisUpgrade',
        'Upgrade',
        'WebPortableSelfTest'
    )
    Assert-ExactMapKeys `
        -Map $smokeReceipt.Checks `
        -Expected $expectedSmokeChecks `
        -Context 'Windows smoke receipt Checks'
    $upgradeStatus = [string]$smokeReceipt.Checks['Upgrade']
    if ($upgradeStatus -cnotin @('passed', 'not-applicable-first-release')) {
        throw 'Windows smoke receipt upgrade status is invalid.'
    }
    $smokeUpgradeMode = [string]$smokeReceipt.UpgradeMode
    if ($upgradeStatus -ceq 'not-applicable-first-release') {
        if (
            $smokeUpgradeMode -cne 'first-release' -or
            $null -ne $smokeReceipt.UpgradeFromVersion -or
            $null -ne $smokeReceipt.UpgradeFromNsisSha256 -or
            $null -ne $smokeReceipt.UpgradeFromMsiSha256 -or
            $null -ne $smokeReceipt.UpgradeFromSignerThumbprint
        ) {
            throw 'First-release smoke metadata is inconsistent.'
        }
        $smokePreviousPolicySigner = $null
    } elseif (
        $smokeUpgradeMode -cne 'previous-installers' -or
        [string]$smokeReceipt.UpgradeFromVersion -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' -or
        [version]$smokeReceipt.UpgradeFromVersion -ge [version]$version -or
        [string]$smokeReceipt.UpgradeFromNsisSha256 -cnotmatch '^[0-9a-f]{64}$' -or
        [string]$smokeReceipt.UpgradeFromMsiSha256 -cnotmatch '^[0-9a-f]{64}$' -or
        [string]$smokeReceipt.UpgradeFromSignerThumbprint -cnotmatch '^[0-9A-F]{40}$'
    ) {
        throw 'Upgrade smoke metadata is incomplete or invalid.'
    } else {
        $smokePreviousPolicySigner = Get-ReleaseSigningPolicySigner `
            -Policy $signingPolicy `
            -Version ([string]$smokeReceipt.UpgradeFromVersion) `
            -Thumbprint ([string]$smokeReceipt.UpgradeFromSignerThumbprint)
    }
    foreach ($checkName in $expectedSmokeChecks) {
        $expectedStatus = if (
            $upgradeStatus -ceq 'not-applicable-first-release' -and
            $checkName -cin @(
                'MsiUpgrade',
                'MsiDowngradeBlocked',
                'NsisUpgrade',
                'NsisDowngradeBlocked',
                'Upgrade'
            )
        ) {
            'not-applicable-first-release'
        } else {
            'passed'
        }
        if ([string]$smokeReceipt.Checks[$checkName] -cne $expectedStatus) {
            throw "Windows smoke receipt check has an invalid status: $checkName"
        }
    }
    $smokeBoundRecords = @(
        $manifestAssets
    ) + @(
        $manifest.checksumFile,
        $manifest.releaseNotesFile,
        $manifestLocalRecord
    )
    Assert-ExactMapKeys `
        -Map $smokeReceipt.Files `
        -Expected @($smokeBoundRecords.name) `
        -Context 'Windows smoke receipt Files'
    foreach ($record in $smokeBoundRecords) {
        if ([string]$smokeReceipt.Files[[string]$record.name] -ne [string]$record.sha256) {
            throw "Windows smoke receipt is not bound to the release file: $($record.name)"
        }
    }
    $smokeFile = Get-Item -LiteralPath $resolvedSmokeReceipt
    $smokeRecord = [pscustomobject]@{
        name = $smokeReceiptName
        bytes = $smokeFile.Length
        sha256 = (Get-FileHash -LiteralPath $resolvedSmokeReceipt -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    $localFiles[$smokeReceiptName] = $resolvedSmokeReceipt
    $manifestRecordsByName[$smokeReceiptName] = $smokeRecord
} elseif (-not [string]::IsNullOrWhiteSpace($WindowsSmokeReceiptPath)) {
    throw 'Prereleases must not present a stable-production Windows smoke receipt.'
}

$expectedReleaseType = "本次资产已通过 Authenticode 签名、时间戳和证书指纹校验（$expectedSigner）。"
$deterministicReleaseNotes = @"
# NekoSSH v$version

NekoSSH v$version 提供两个隔离的 SSH 入口：外部浏览器 Web macOS 伪桌面，以及 Tauri v2 原生 SSH IDE。

## Windows 资产

- ``$nsisName``：面向个人用户的当前用户 NSIS 安装程序
- ``$msiName``：面向管理员部署的简体中文 MSI 安装程序
- ``$webPortableName``：独立 Web macOS + 内置 Node ssh2js Windows 可携包
- ``$noticesName``：锁定依赖的第三方许可与声明
- ``SHA256SUMS.txt``：全部发行资产的 SHA-256 校验清单

NSIS 与 MSI 是两个独立部署通道；切换格式前必须先卸载已有版本，不支持跨格式直接覆盖。

## 构建验证

- 源提交：``$commit``
- 已执行双运行时生产构建、运行时隔离检查、前端/Node/Rust 测试、Rust fmt 与 Clippy
- 已执行 npm audit 与 cargo-audit
- Web 可携包已验证签名启动器 → payload manifest → 全部文件哈希链，并完成无浏览器自检

## 签名状态

$expectedReleaseType
"@
if (
    (ConvertTo-NormalizedReleaseText -Value ([IO.File]::ReadAllText($notesPath))) -ne
        (ConvertTo-NormalizedReleaseText -Value $deterministicReleaseNotes)
) {
    throw 'Release notes are not the deterministic notes for this source and signing manifest.'
}
foreach ($protectedReleaseInput in @($localFiles.Values) + @($notesPath)) {
    Protect-ReleaseInput -Path ([string]$protectedReleaseInput)
}
$gitDirectoryPath = Join-Path $repositoryRoot '.git'
$gitConfigPath = Join-Path $gitDirectoryPath 'config'
if (
    -not (Test-Path -LiteralPath $gitDirectoryPath -PathType Container) -or
    -not (Test-Path -LiteralPath $gitConfigPath -PathType Leaf)
) {
    throw 'Release publication requires a regular primary Git worktree with .git/config.'
}
Assert-TreeHasNoReparsePoints -Root $gitDirectoryPath
foreach ($forbiddenGitAdministrativePath in @(
    (Join-Path $gitDirectoryPath 'commondir'),
    (Join-Path $gitDirectoryPath 'config.worktree'),
    (Join-Path $gitDirectoryPath 'objects/info/alternates'),
    (Join-Path $gitDirectoryPath 'objects/info/http-alternates')
)) {
    if (Test-Path -LiteralPath $forbiddenGitAdministrativePath) {
        throw "Release publication rejects split/external Git administrative data: $forbiddenGitAdministrativePath"
    }
}
Protect-ReleaseInput -Path $gitConfigPath
Assert-NoUnsafePublishGitConfiguration

Push-Location $repositoryRoot
try {
    [void](Assert-NekoSshReleaseManifestToolchainIdentity `
        -ManifestToolchain $manifest.toolchain `
        -Toolchain $lockedToolchain)
    Invoke-HardenedMainFetch
    $publishSourceIdentity = Assert-NekoSshGitSourceIntegrity `
        -RepositoryRoot $repositoryRoot `
        -GitExecutable $gitExecutable
    if ([string]$publishSourceIdentity.Commit -cne $commit) {
        throw "Manifest, HEAD and origin/main must all point to $commit."
    }
    if (
        [string]$manifest.source.branch -cne [string]$publishSourceIdentity.Branch -or
        [string]$manifest.source.commit -cne [string]$publishSourceIdentity.Commit -or
        [string]$manifest.source.tree -cne [string]$publishSourceIdentity.Tree -or
        [string]$manifest.source.snapshotSha256 -cne [string]$publishSourceIdentity.SnapshotSha256 -or
        [string]$manifest.source.digestFormat -cne [string]$publishSourceIdentity.DigestFormat -or
        [int]$manifest.source.trackedFileCount -ne [int]$publishSourceIdentity.TrackedFileCount
    ) {
        throw 'Release manifest source identity does not match the complete checked-out Git source snapshot.'
    }
    $originUrl = Get-HardenedPublishGitOutput -ArgumentList @('remote', 'get-url', 'origin')
    $escapedRepository = [regex]::Escape($Repository)
    if (
        $originUrl -notmatch "(?i)^(https://github\.com/$escapedRepository(?:\.git)?/?|git@github\.com:$escapedRepository(?:\.git)?)$"
    ) {
        throw "Git origin '$originUrl' does not match GitHub repository '$Repository'."
    }

    Invoke-Checked -FilePath $ghExecutable -ArgumentList @('auth', 'status', '--hostname', 'github.com')
    [void](Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @('release', 'verify', '--help'))
    [void](Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @('release', 'verify-asset', '--help'))
    $actionsEnabled = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
        'api',
        "repos/$Repository/actions/permissions",
        '--jq',
        '.enabled'
    )
    if ($actionsEnabled -ne 'false') {
        throw 'GitHub Actions must remain disabled before publishing a manual release.'
    }
    Assert-GitHubReleaseTagRuleset -RepositoryName $Repository -TagName $tag
    Invoke-Checked -FilePath $ghExecutable -ArgumentList @(
        'api',
        '--method',
        'PUT',
        '-H',
        'Accept: application/vnd.github+json',
        '-H',
        'X-GitHub-Api-Version: 2026-03-10',
        "repos/$Repository/immutable-releases"
    )
    $immutablePolicy = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
        'api',
        '-H',
        'Accept: application/vnd.github+json',
        '-H',
        'X-GitHub-Api-Version: 2026-03-10',
        "repos/$Repository/immutable-releases"
    ) | ConvertFrom-Json
    if ($immutablePolicy.enabled -isnot [bool] -or $immutablePolicy.enabled -ne $true) {
        throw 'GitHub immutable releases must be enabled before creating the draft.'
    }
    Assert-GitHubManualReleaseControls -RepositoryName $Repository -TagName $tag
    if (-not $isPrerelease) {
        $stableTagOutput = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
            'api',
            '--paginate',
            "repos/$Repository/releases?per_page=100",
            '--jq',
            '.[] | select(.draft == false and .prerelease == false) | .tag_name'
        )
        $priorStableTags = @(
            $stableTagOutput -split "`r?`n" |
                Where-Object { -not [string]::IsNullOrWhiteSpace($_) -and $_ -ne $tag }
        )
        if ($smokeUpgradeMode -eq 'first-release' -and $priorStableTags.Count -ne 0) {
            throw 'A first-release smoke receipt is invalid because a prior stable GitHub Release exists.'
        }
        if ($smokeUpgradeMode -eq 'previous-installers') {
            $priorStableVersions = @(
                $priorStableTags |
                    Where-Object { $_ -match '^v(\d+\.\d+\.\d+)$' } |
                    ForEach-Object { [version]($_.Substring(1)) } |
                    Sort-Object -Descending
            )
            if ($priorStableVersions.Count -eq 0) {
                throw 'An upgrade smoke receipt requires a prior stable semantic-version GitHub Release.'
            }
            if ([version]$smokeReceipt.UpgradeFromVersion -ne $priorStableVersions[0]) {
                throw 'The upgrade smoke receipt must start from the latest prior stable GitHub Release.'
            }
            $priorStableTag = "v$($smokeReceipt.UpgradeFromVersion)"
            $priorRelease = (
                Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
                    'release',
                    'view',
                    $priorStableTag,
                    '--repo',
                    $Repository,
                    '--json',
                    'tagName,isDraft,isImmutable,isPrerelease,assets'
                )
            ) | ConvertFrom-Json
            if (
                [string]$priorRelease.tagName -cne $priorStableTag -or
                $priorRelease.isDraft -ne $false -or
                $priorRelease.isPrerelease -ne $false -or
                $priorRelease.isImmutable -ne $true
            ) {
                throw 'The prior stable release must be published, stable, and immutable.'
            }
            $priorNsisName = "NekoSSH_$($smokeReceipt.UpgradeFromVersion)_x64-setup.exe"
            $priorMsiName = "NekoSSH_$($smokeReceipt.UpgradeFromVersion)_x64_zh-CN.msi"
            foreach ($priorAssetExpectation in @(
                [pscustomobject]@{
                    Name = $priorNsisName
                    Sha256 = [string]$smokeReceipt.UpgradeFromNsisSha256
                },
                [pscustomobject]@{
                    Name = $priorMsiName
                    Sha256 = [string]$smokeReceipt.UpgradeFromMsiSha256
                }
            )) {
                $matchingAssets = @(
                    $priorRelease.assets |
                        Where-Object { [string]$_.name -ceq $priorAssetExpectation.Name }
                )
                if (
                    $matchingAssets.Count -ne 1 -or
                    [long]$matchingAssets[0].size -le 0 -or
                    [string]$matchingAssets[0].state -cne 'uploaded' -or
                    [string]$matchingAssets[0].digest -cne "sha256:$($priorAssetExpectation.Sha256)"
                ) {
                    throw "The prior immutable release asset does not match the clean-VM receipt: $($priorAssetExpectation.Name)"
                }
            }
        }
    }

    & $ghExecutable release view $tag --repo $Repository --json tagName *> $null
    $releaseAlreadyExists = $LASTEXITCODE -eq 0

    $localTagResult = Invoke-HardenedPublishGit `
        -ArgumentList @('show-ref', '--verify', '--quiet', "refs/tags/$tag") `
        -AllowedExitCodes @(0, 1)
    if ($localTagResult.ExitCode -eq 1) {
        throw (
            "Local tag $tag is missing. Production publishing never creates tags; " +
            'create and verify a cryptographically signed annotated tag in the controlled signing ceremony.'
        )
    }
    $localTagObject = Get-HardenedPublishGitOutput -ArgumentList @(
        'rev-parse',
        "refs/tags/$tag"
    )
    Assert-LocalSignedTagIdentity `
        -TagName $tag `
        -CommitSha $commit `
        -ExpectedTagObjectSha $localTagObject `
        -ExpectedSignerFingerprint $normalizedExpectedTagSignerFingerprint

    $remoteTag = Get-HardenedPublishGitOutput -ArgumentList @(
        'ls-remote',
        $canonicalRepositoryUrl,
        "refs/tags/$tag"
    )
    if ([string]::IsNullOrWhiteSpace($remoteTag)) {
        Invoke-CheckedTagPushMutation `
            -RepositoryName $Repository `
            -TagName $tag `
            -CommitSha $commit `
            -ExpectedTagObjectSha $localTagObject `
            -ExpectedTagSignerFingerprint $normalizedExpectedTagSignerFingerprint
    } else {
        Assert-ManualPublishState `
            -RepositoryName $Repository `
            -TagName $tag `
            -CommitSha $commit `
            -ExpectedTagObjectSha $localTagObject `
            -ExpectedTagSignerFingerprint $normalizedExpectedTagSignerFingerprint
    }

    $publishedNameInputs = @($expectedAssetNames) + @($checksumName, $manifestName)
    $publishedNameInputs += $smokeReceiptName
    $expectedPublishedNames = @($publishedNameInputs | Sort-Object)
    $assetLabels = @{}
    $assetLabels[$nsisName] = "NekoSSH $version NSIS 安装程序"
    $assetLabels[$msiName] = "NekoSSH $version 简体中文 MSI"
    $assetLabels[$webPortableName] = "NekoSSH $version 独立 Web macOS Windows 可携包"
    $assetLabels[$noticesName] = '第三方许可与声明'
    $assetLabels[$checksumName] = 'SHA-256 校验清单'
    $assetLabels[$manifestName] = '提交与签名验证 manifest'
    $assetLabels[$smokeReceiptName] = '已签名 Windows 安装/升级/卸载验收凭据'

    foreach ($name in @($expectedPublishedNames) + @($notesName)) {
        $record = $manifestRecordsByName[$name]
        $path = $localFiles[$name]
        $file = Get-Item -LiteralPath $path
        $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($file.Length -ne [long]$record.bytes -or $hash -ne [string]$record.sha256) {
            throw "Release input changed after manifest validation: $name"
        }
    }
    $expectedReleaseNotes = $deterministicReleaseNotes
    $expectedReleaseName = "NekoSSH $tag"

    if (-not $releaseAlreadyExists) {
        $createArguments = @(
            'release',
            'create',
            $tag
        )
        foreach ($name in $expectedPublishedNames) {
            $createArguments += "$($localFiles[$name])#$($assetLabels[$name])"
        }
        $createArguments += @(
            '--repo',
            $Repository,
            '--verify-tag',
            '--draft',
            '--title',
            "NekoSSH $tag",
            '--notes-file',
            $notesPath
        )
        Invoke-CheckedGitHubReleaseMutation `
            -ArgumentList $createArguments `
            -RepositoryName $Repository `
            -TagName $tag `
            -CommitSha $commit `
            -ExpectedTagObjectSha $localTagObject `
            -ExpectedTagSignerFingerprint $normalizedExpectedTagSignerFingerprint
    }

    $releaseJson = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
        'release',
        'view',
        $tag,
        '--repo',
        $Repository,
        '--json',
        'name,tagName,isDraft,isImmutable,isPrerelease,url,assets,body'
    )
    $release = $releaseJson | ConvertFrom-Json
    if (
        [string]$release.name -cne $expectedReleaseName -or
        $release.tagName -ne $tag -or
        (ConvertTo-NormalizedReleaseText -Value ([string]$release.body)) -ne
            (ConvertTo-NormalizedReleaseText -Value $expectedReleaseNotes)
    ) {
        throw 'GitHub Release tag or notes do not match the manifest.'
    }
    if (
        ($release.isDraft -eq $true -and $release.isImmutable -ne $false) -or
        ($release.isDraft -eq $false -and $release.isImmutable -ne $true)
    ) {
        throw 'GitHub Release draft/immutable state is unsafe.'
    }
    $unexpectedPublishedNames = @(
        $release.assets.name |
            Where-Object { $_ -notin $expectedPublishedNames }
    )
    if ($unexpectedPublishedNames.Count -gt 0) {
        throw "GitHub Release contains unexpected assets: $($unexpectedPublishedNames -join ', ')."
    }
    foreach ($asset in @($release.assets)) {
        $manifestRecord = $manifestRecordsByName[[string]$asset.name]
        if (
            [long]$asset.size -ne [long]$manifestRecord.bytes -or
            [string]$asset.state -cne 'uploaded' -or
            [string]$asset.digest -cne "sha256:$($manifestRecord.sha256)"
        ) {
            throw "GitHub Release asset metadata does not match the manifest: $($asset.name)"
        }
    }

    $missingPublishedNames = @(
        $expectedPublishedNames |
            Where-Object { $_ -notin @($release.assets.name) }
    )
    if ($missingPublishedNames.Count -gt 0) {
        if ($release.isDraft -ne $true) {
            throw "Published GitHub Release is missing assets: $($missingPublishedNames -join ', ')."
        }
        foreach ($name in $missingPublishedNames) {
            $record = $manifestRecordsByName[$name]
            $path = $localFiles[$name]
            $file = Get-Item -LiteralPath $path
            $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($file.Length -ne [long]$record.bytes -or $hash -ne [string]$record.sha256) {
                throw "Release input changed before draft repair: $name"
            }
            $uploadArguments = @(
                'release',
                'upload',
                $tag,
                "$path#$($assetLabels[$name])",
                '--repo',
                $Repository
            )
            Invoke-CheckedGitHubReleaseMutation `
                -ArgumentList $uploadArguments `
                -RepositoryName $Repository `
                -TagName $tag `
                -CommitSha $commit `
                -ExpectedTagObjectSha $localTagObject `
                -ExpectedTagSignerFingerprint $normalizedExpectedTagSignerFingerprint
        }
        $releaseJson = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
            'release',
            'view',
            $tag,
            '--repo',
            $Repository,
            '--json',
            'name,tagName,isDraft,isImmutable,isPrerelease,url,assets,body'
        )
        $release = $releaseJson | ConvertFrom-Json
    }
    if ((@($release.assets.name | Sort-Object) -join "`n") -ne ($expectedPublishedNames -join "`n")) {
        throw 'Draft GitHub Release assets do not match the expected set.'
    }
    foreach ($asset in @($release.assets)) {
        $manifestRecord = $manifestRecordsByName[[string]$asset.name]
        if (
            [long]$asset.size -ne [long]$manifestRecord.bytes -or
            [string]$asset.state -cne 'uploaded' -or
            [string]$asset.digest -cne "sha256:$($manifestRecord.sha256)"
        ) {
            throw "Draft asset metadata mismatch: $($asset.name)"
        }
    }

    $verificationDirectory = [IO.Path]::GetFullPath(
        (Join-Path $resolvedReleaseDirectory ".download-verification-$PID-$([guid]::NewGuid().ToString('N'))")
    )
    if (-not (Test-ChildPath -Path $verificationDirectory -Parent $resolvedReleaseDirectory)) {
        throw 'Refusing to create a download verification directory outside the release assets.'
    }
    New-Item -ItemType Directory -Path $verificationDirectory | Out-Null
    Assert-PathHasNoReparsePoints -Path $verificationDirectory -TrustedRoot $repositoryRoot
    try {
        Invoke-Checked -FilePath $ghExecutable -ArgumentList @(
            'release',
            'download',
            $tag,
            '--repo',
            $Repository,
            '--dir',
            $verificationDirectory
        )
        $downloadedFiles = @(Get-ChildItem -LiteralPath $verificationDirectory -File)
        if ((@($downloadedFiles.Name | Sort-Object) -join "`n") -ne ($expectedPublishedNames -join "`n")) {
            throw 'Downloaded release assets do not match the published asset set.'
        }
        foreach ($downloaded in $downloadedFiles) {
            $downloadedHash = (Get-FileHash -LiteralPath $downloaded.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            $manifestHash = [string]$manifestRecordsByName[$downloaded.Name].sha256
            if ($downloadedHash -ne $manifestHash) {
                throw "Downloaded release asset hash mismatch: $($downloaded.Name)"
            }
        }
        $downloadedWebArguments = @{
            ArchivePath = (Join-Path $verificationDirectory $webPortableName)
            ExpectedVersion = $version
            ExpectedCommit = $commit
            ExpectedProjectSignerThumbprint = $expectedSigner
            ExpectedProjectSignerSubject = $policySigner.Subject
            ExpectedProjectSignerSpkiSha256 = $policySigner.SpkiSha256
        }
        $downloadedWebVerification = (
            & (Join-Path $PSScriptRoot 'verify-windows-web-portable.ps1') @downloadedWebArguments |
                Out-String
        ).Trim() | ConvertFrom-Json
        if ($downloadedWebVerification.selfTest -ne 'passed') {
            throw 'The downloaded draft Web runtime failed its packaged self-test.'
        }
    }
    finally {
        Remove-OwnedFlatDirectory -Path $verificationDirectory -Parent $resolvedReleaseDirectory
    }

    $prePublishJson = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
        'release',
        'view',
        $tag,
        '--repo',
        $Repository,
        '--json',
        'name,tagName,isDraft,isImmutable,isPrerelease,url,assets,body'
    )
    $prePublishRelease = $prePublishJson | ConvertFrom-Json
    if (
        [string]$prePublishRelease.name -cne $expectedReleaseName -or
        $prePublishRelease.tagName -ne $tag -or
        (ConvertTo-NormalizedReleaseText -Value ([string]$prePublishRelease.body)) -ne
            (ConvertTo-NormalizedReleaseText -Value $expectedReleaseNotes) -or
        (@($prePublishRelease.assets.name | Sort-Object) -join "`n") -ne ($expectedPublishedNames -join "`n") -or
        ($prePublishRelease.isDraft -eq $true -and $prePublishRelease.isImmutable -ne $false) -or
        ($prePublishRelease.isDraft -eq $false -and $prePublishRelease.isImmutable -ne $true)
    ) {
        throw 'Draft changed after download verification.'
    }
    foreach ($asset in @($prePublishRelease.assets)) {
        $record = $manifestRecordsByName[[string]$asset.name]
        if (
            [long]$asset.size -ne [long]$record.bytes -or
            [string]$asset.state -cne 'uploaded' -or
            [string]$asset.digest -cne "sha256:$($record.sha256)"
        ) {
            throw "Draft asset metadata changed after download verification: $($asset.name)"
        }
    }
    Assert-ManualPublishState `
        -RepositoryName $Repository `
        -TagName $tag `
        -CommitSha $commit `
        -ExpectedTagObjectSha $localTagObject `
        -ExpectedTagSignerFingerprint $normalizedExpectedTagSignerFingerprint

    if ($prePublishRelease.isDraft -eq $true) {
        $publicationGateJson = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
            'release',
            'view',
            $tag,
            '--repo',
            $Repository,
            '--json',
            'name,tagName,isDraft,isImmutable,isPrerelease,url,assets,body'
        )
        $publicationGateRelease = $publicationGateJson | ConvertFrom-Json
        if (
            [string]$publicationGateRelease.name -cne $expectedReleaseName -or
            $publicationGateRelease.tagName -ne $tag -or
            $publicationGateRelease.isDraft -ne $true -or
            $publicationGateRelease.isImmutable -ne $false -or
            (ConvertTo-NormalizedReleaseText -Value ([string]$publicationGateRelease.body)) -ne
                (ConvertTo-NormalizedReleaseText -Value $expectedReleaseNotes) -or
            (@($publicationGateRelease.assets.name | Sort-Object) -join "`n") -ne
                ($expectedPublishedNames -join "`n")
        ) {
            throw 'Release draft changed at the publication gate.'
        }
        foreach ($asset in @($publicationGateRelease.assets)) {
            $record = $manifestRecordsByName[[string]$asset.name]
            if (
                [long]$asset.size -ne [long]$record.bytes -or
                [string]$asset.state -cne 'uploaded' -or
                [string]$asset.digest -cne "sha256:$($record.sha256)"
            ) {
                throw "Release draft asset metadata changed at the publication gate: $($asset.name)"
            }
        }

        $publishArguments = @(
            'release',
            'edit',
            $tag,
            '--repo',
            $Repository,
            '--draft=false'
        )
        $publishArguments += @('--prerelease=false', '--latest')
        [void](Assert-NekoSshReleaseManifestToolchainIdentity `
            -ManifestToolchain $manifest.toolchain `
            -Toolchain $lockedToolchain)
        Invoke-CheckedGitHubReleaseMutation `
            -ArgumentList $publishArguments `
            -RepositoryName $Repository `
            -TagName $tag `
            -CommitSha $commit `
            -ExpectedTagObjectSha $localTagObject `
            -ExpectedTagSignerFingerprint $normalizedExpectedTagSignerFingerprint
    }

    $finalReleaseJson = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
        'release',
        'view',
        $tag,
        '--repo',
        $Repository,
        '--json',
        'name,tagName,isDraft,isImmutable,isPrerelease,url,assets,body'
    )
    $finalRelease = $finalReleaseJson | ConvertFrom-Json
    if (
        [string]$finalRelease.name -cne $expectedReleaseName -or
        $finalRelease.tagName -ne $tag -or
        $finalRelease.isDraft -ne $false -or
        $finalRelease.isImmutable -ne $true -or
        $finalRelease.isPrerelease -ne $false -or
        (ConvertTo-NormalizedReleaseText -Value ([string]$finalRelease.body)) -ne
            (ConvertTo-NormalizedReleaseText -Value $expectedReleaseNotes) -or
        (@($finalRelease.assets.name | Sort-Object) -join "`n") -ne ($expectedPublishedNames -join "`n")
    ) {
        throw 'Published GitHub Release does not match the verified draft.'
    }
    foreach ($asset in @($finalRelease.assets)) {
        $manifestRecord = $manifestRecordsByName[[string]$asset.name]
        if (
            [long]$asset.size -ne [long]$manifestRecord.bytes -or
            [string]$asset.state -cne 'uploaded' -or
            [string]$asset.digest -cne "sha256:$($manifestRecord.sha256)"
        ) {
            throw "Published asset metadata changed after draft verification: $($asset.name)"
        }
    }

    $publishedVerificationDirectory = [IO.Path]::GetFullPath(
        (Join-Path $resolvedReleaseDirectory ".published-verification-$PID-$([guid]::NewGuid().ToString('N'))")
    )
    if (-not (Test-ChildPath -Path $publishedVerificationDirectory -Parent $resolvedReleaseDirectory)) {
        throw 'Refusing to create a published verification directory outside the release assets.'
    }
    New-Item -ItemType Directory -Path $publishedVerificationDirectory | Out-Null
    Assert-PathHasNoReparsePoints -Path $publishedVerificationDirectory -TrustedRoot $repositoryRoot
    try {
        Invoke-Checked -FilePath $ghExecutable -ArgumentList @(
            'release',
            'download',
            $tag,
            '--repo',
            $Repository,
            '--dir',
            $publishedVerificationDirectory
        )
        $publishedDownloads = @(Get-ChildItem -LiteralPath $publishedVerificationDirectory -File)
        if ((@($publishedDownloads.Name | Sort-Object) -join "`n") -ne ($expectedPublishedNames -join "`n")) {
            throw 'Published downloads do not match the expected asset set.'
        }
        foreach ($downloaded in $publishedDownloads) {
            $downloadedHash = (Get-FileHash -LiteralPath $downloaded.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            $manifestHash = [string]$manifestRecordsByName[$downloaded.Name].sha256
            if ($downloadedHash -ne $manifestHash) {
                throw "Published release asset hash mismatch: $($downloaded.Name)"
            }
        }
        $publishedWebArguments = @{
            ArchivePath = (Join-Path $publishedVerificationDirectory $webPortableName)
            ExpectedVersion = $version
            ExpectedCommit = $commit
            ExpectedProjectSignerThumbprint = $expectedSigner
            ExpectedProjectSignerSubject = $policySigner.Subject
            ExpectedProjectSignerSpkiSha256 = $policySigner.SpkiSha256
        }
        $publishedWebVerification = (
            & (Join-Path $PSScriptRoot 'verify-windows-web-portable.ps1') @publishedWebArguments |
                Out-String
        ).Trim() | ConvertFrom-Json
        if ($publishedWebVerification.selfTest -ne 'passed') {
            throw 'The published Web runtime failed its packaged self-test.'
        }
    }
    finally {
        Remove-OwnedFlatDirectory -Path $publishedVerificationDirectory -Parent $resolvedReleaseDirectory
    }
    Assert-ManualPublishState `
        -RepositoryName $Repository `
        -TagName $tag `
        -CommitSha $commit `
        -ExpectedTagObjectSha $localTagObject `
        -ExpectedTagSignerFingerprint $normalizedExpectedTagSignerFingerprint
    $postPublishStateJson = Get-CheckedOutput -FilePath $ghExecutable -ArgumentList @(
        'release',
        'view',
        $tag,
        '--repo',
        $Repository,
        '--json',
        'name,tagName,isDraft,isImmutable,isPrerelease,url,assets,body'
    )
    $postPublishState = $postPublishStateJson | ConvertFrom-Json
    if (
        [string]$postPublishState.name -cne $expectedReleaseName -or
        $postPublishState.tagName -ne $tag -or
        $postPublishState.isDraft -ne $false -or
        $postPublishState.isImmutable -ne $true -or
        $postPublishState.isPrerelease -ne $false -or
        (ConvertTo-NormalizedReleaseText -Value ([string]$postPublishState.body)) -ne
            (ConvertTo-NormalizedReleaseText -Value $expectedReleaseNotes) -or
        (@($postPublishState.assets.name | Sort-Object) -join "`n") -ne ($expectedPublishedNames -join "`n")
    ) {
        throw 'Published GitHub Release changed during final verification.'
    }
    foreach ($asset in @($postPublishState.assets)) {
        $record = $manifestRecordsByName[[string]$asset.name]
        if (
            [long]$asset.size -ne [long]$record.bytes -or
            [string]$asset.state -cne 'uploaded' -or
            [string]$asset.digest -cne "sha256:$($record.sha256)"
        ) {
            throw "Published asset metadata changed during final verification: $($asset.name)"
        }
    }
    $releaseAttestationVerified = $false
    for ($attempt = 1; $attempt -le 30; $attempt += 1) {
        & $ghExecutable release verify $tag --repo $Repository --format json *> $null
        if ($LASTEXITCODE -eq 0) {
            $releaseAttestationVerified = $true
            break
        }
        if ($attempt -lt 30) {
            Start-Sleep -Seconds 2
        }
    }
    if (-not $releaseAttestationVerified) {
        throw 'GitHub did not provide a valid immutable release attestation.'
    }
    foreach ($name in $expectedPublishedNames) {
        $record = $manifestRecordsByName[$name]
        $path = $localFiles[$name]
        if (
            (Get-Item -LiteralPath $path).Length -ne [long]$record.bytes -or
            (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -ne
                [string]$record.sha256
        ) {
            throw "Local release asset changed before attestation verification: $name"
        }
        Invoke-Checked -FilePath $ghExecutable -ArgumentList @(
            'release',
            'verify-asset',
            $tag,
            $path,
            '--repo',
            $Repository
        )
    }

    Write-Host "GitHub Release verified: $($finalRelease.url)"
}
finally {
    try {
        for ($lockIndex = $releaseInputLocks.Count - 1; $lockIndex -ge 0; $lockIndex -= 1) {
            $releaseInputLocks[$lockIndex].Dispose()
        }
        $releaseInputLocks.Clear()
        $lockedReleaseInputPaths.Clear()
    }
    finally {
        Pop-Location
    }
}
