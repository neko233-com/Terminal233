[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ReleaseDirectory,
    [Parameter(Mandatory)][string]$SignerThumbprint,
    [Parameter(Mandatory)][string]$TimestampUrl,
    [Parameter(Mandatory)][string]$ExpectedValidationNonce,
    [Parameter(Mandatory)][string]$ExpectedCleanVmImageId
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$policyPath = Join-Path $repositoryRoot 'release-signing-policy.json'
$policySchemaPath = Join-Path $repositoryRoot 'release-signing-policy.schema.json'
$policyModulePath = Join-Path $PSScriptRoot 'release-signing-policy.psm1'
$signingScriptPath = $PSCommandPath
$validationScriptPath = Join-Path $PSScriptRoot 'test-windows-release-installers.ps1'
$webVerifierPath = Join-Path $PSScriptRoot 'verify-windows-web-portable.ps1'
$receiptName = 'WINDOWS_SMOKE_RECEIPT.psd1'
$expectedRepository = 'neko233-com/nekossh'

function Get-Sha256 {
    param([Parameter(Mandatory)][string]$Path)

    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Test-ChildPath {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Parent
    )

    $normalizedParent = $Parent.TrimEnd(
        [IO.Path]::DirectorySeparatorChar,
        [IO.Path]::AltDirectorySeparatorChar
    )
    return $Path.StartsWith(
        "$normalizedParent$([IO.Path]::DirectorySeparatorChar)",
        [StringComparison]::OrdinalIgnoreCase
    )
}

function Assert-RegularFile {
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Required regular file is missing: $Path"
    }
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Release signing inputs must not be reparse points: $Path"
    }
}

function Assert-PathHasNoReparsePoints {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$TrustedRoot
    )

    $fullPath = [IO.Path]::GetFullPath($Path)
    $fullRoot = [IO.Path]::GetFullPath($TrustedRoot).TrimEnd(
        [IO.Path]::DirectorySeparatorChar,
        [IO.Path]::AltDirectorySeparatorChar
    )
    if (
        -not [string]::Equals($fullPath, $fullRoot, [StringComparison]::OrdinalIgnoreCase) -and
        -not (Test-ChildPath -Path $fullPath -Parent $fullRoot)
    ) {
        throw "Path escaped the trusted repository root: $fullPath"
    }
    $volumeRoot = [IO.Path]::GetPathRoot($fullPath)
    if ([string]::IsNullOrWhiteSpace($volumeRoot)) {
        throw "Path does not have a trusted Windows volume root: $fullPath"
    }
    $relative = $fullPath.Substring($volumeRoot.Length).TrimStart(
        [IO.Path]::DirectorySeparatorChar,
        [IO.Path]::AltDirectorySeparatorChar
    )
    $current = $volumeRoot
    if (-not (Test-Path -LiteralPath $current -PathType Container)) {
        throw "Expected volume root is missing: $current"
    }
    if (((Get-Item -LiteralPath $current -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Release signing paths must not traverse a reparse-point volume root: $current"
    }
    if (-not [string]::IsNullOrWhiteSpace($relative)) {
        foreach ($segment in $relative.Split(
            @([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar),
            [StringSplitOptions]::RemoveEmptyEntries
        )) {
            $current = Join-Path $current $segment
            if (-not (Test-Path -LiteralPath $current)) {
                throw "Expected path component is missing: $current"
            }
            if (((Get-Item -LiteralPath $current -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Release signing paths must not traverse a reparse point: $current"
            }
        }
    }
}

function Get-CheckedGitOutput {
    param([Parameter(Mandatory)][string[]]$ArgumentList)

    $output = & git -C $repositoryRoot @ArgumentList 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "Git command failed: git $($ArgumentList -join ' ')`n$($output -join "`n")"
    }
    return ($output -join "`n").Trim()
}

function Invoke-CheckedGit {
    param([Parameter(Mandatory)][string[]]$ArgumentList)

    [void](Get-CheckedGitOutput -ArgumentList $ArgumentList)
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
        throw "$Context fields are incomplete or unexpected. Expected: $($expectedSorted -join ', ')."
    }
}

function Assert-ManifestRecord {
    param(
        [Parameter(Mandatory)]$Record,
        [Parameter(Mandatory)][string]$ExpectedName,
        [Parameter(Mandatory)][string]$ReleaseRoot
    )

    if ([string]$Record.name -cne $ExpectedName) {
        throw "Release manifest record name is invalid: '$($Record.name)'."
    }
    if (
        -not ($Record.bytes -is [int] -or $Record.bytes -is [long]) -or
        [long]$Record.bytes -le 0
    ) {
        throw "Release manifest byte count is invalid: $ExpectedName"
    }
    if ([string]$Record.sha256 -cnotmatch '^[0-9a-f]{64}$') {
        throw "Release manifest SHA-256 is invalid: $ExpectedName"
    }
    $path = [IO.Path]::GetFullPath((Join-Path $ReleaseRoot $ExpectedName))
    if (-not (Test-ChildPath -Path $path -Parent $ReleaseRoot)) {
        throw "Release manifest file escaped its directory: $ExpectedName"
    }
    Assert-RegularFile -Path $path
    $file = Get-Item -LiteralPath $path
    if (
        $file.Length -ne [long]$Record.bytes -or
        (Get-Sha256 -Path $path) -cne [string]$Record.sha256
    ) {
        throw "Release file does not match its manifest record: $ExpectedName"
    }
    return $path
}

function Assert-ExpectedAuthenticodeSignature {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$ExpectedThumbprint,
        [Parameter(Mandatory)]$PolicySigner
    )

    Assert-RegularFile -Path $Path
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    if (
        $signature.Status.ToString() -cne 'Valid' -or
        $null -eq $signature.SignerCertificate -or
        $signature.SignerCertificate.Thumbprint.ToUpperInvariant() -cne $ExpectedThumbprint -or
        $null -eq $signature.TimeStamperCertificate
    ) {
        throw "Release file lacks the expected valid, timestamped Authenticode signature: $Path"
    }
    [void](Assert-ReleaseSigningCertificate `
        -Certificate $signature.SignerCertificate `
        -Signer $PolicySigner)
}

function Assert-ReceiptData {
    param(
        [Parameter(Mandatory)]$Receipt,
        [Parameter(Mandatory)]$Manifest,
        [Parameter(Mandatory)][hashtable]$ReleaseRecords,
        [Parameter(Mandatory)][string]$ManifestHash,
        [Parameter(Mandatory)][string]$ValidationScriptHash,
        [Parameter(Mandatory)][string]$SigningPolicyHash,
        [Parameter(Mandatory)][string]$ExpectedNonce,
        [Parameter(Mandatory)][string]$ExpectedImageId,
        [Parameter(Mandatory)]$Policy
    )

    $receiptFields = @(
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
    Assert-ExactMapKeys -Map $Receipt -Expected $receiptFields -Context 'Windows smoke receipt'
    if (
        -not ($Receipt.SchemaVersion -is [int] -or $Receipt.SchemaVersion -is [long]) -or
        [long]$Receipt.SchemaVersion -ne 1 -or
        [string]$Receipt.Repository -cne $expectedRepository -or
        [string]$Receipt.Version -cne [string]$Manifest.version -or
        [string]$Receipt.Commit -cne [string]$Manifest.commit -or
        $Receipt.CleanWindowsVm -isnot [bool] -or
        $Receipt.CleanWindowsVm -ne $true -or
        $Receipt.Elevated -isnot [bool] -or
        $Receipt.Elevated -ne $true -or
        [string]$Receipt.Architecture -cne 'x64' -or
        [string]::IsNullOrWhiteSpace([string]$Receipt.WindowsVersion)
    ) {
        throw 'The Windows smoke receipt identity or clean-VM attestation is invalid.'
    }
    $testedAt = [DateTimeOffset]::MinValue
    if (
        -not [DateTimeOffset]::TryParseExact(
            [string]$Receipt.TestedAtUtc,
            'o',
            [Globalization.CultureInfo]::InvariantCulture,
            [Globalization.DateTimeStyles]::RoundtripKind,
            [ref]$testedAt
        ) -or
        $testedAt -gt [DateTimeOffset]::UtcNow.AddMinutes(5) -or
        $testedAt -lt [DateTimeOffset]::UtcNow.AddDays(-7)
    ) {
        throw 'The Windows smoke receipt must have a valid UTC timestamp from the last seven days.'
    }
    if (
        [string]$Receipt.ValidationNonce -cnotmatch '^[0-9a-f]{64}$' -or
        [string]$Receipt.ValidationNonce -cne $ExpectedNonce
    ) {
        throw 'The Windows smoke receipt validation nonce is invalid or does not match the signing challenge.'
    }
    if (
        [string]$Receipt.CleanVmImageId -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._:/@+-]{7,127}$' -or
        [string]$Receipt.CleanVmImageId -cne $ExpectedImageId
    ) {
        throw 'The Windows smoke receipt clean-VM image ID is invalid or unexpected.'
    }
    if (
        [string]$Receipt.ValidationScriptSha256 -cnotmatch '^[0-9a-f]{64}$' -or
        [string]$Receipt.ValidationScriptSha256 -cne $ValidationScriptHash
    ) {
        throw 'The Windows smoke receipt is not bound to the checked-out validation script.'
    }
    if (
        [string]$Receipt.SigningPolicySha256 -cnotmatch '^[0-9a-f]{64}$' -or
        [string]$Receipt.SigningPolicySha256 -cne $SigningPolicyHash
    ) {
        throw 'The Windows smoke receipt is not bound to the checked-out signing policy.'
    }

    $expectedChecks = @(
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
    Assert-ExactMapKeys -Map $Receipt.Checks -Expected $expectedChecks -Context 'Windows smoke receipt Checks'
    $upgradeStatus = [string]$Receipt.Checks['Upgrade']
    if ($upgradeStatus -cnotin @('passed', 'not-applicable-first-release')) {
        throw 'Windows smoke receipt Upgrade status is invalid.'
    }
    if ($upgradeStatus -ceq 'not-applicable-first-release') {
        if (
            [string]$Receipt.UpgradeMode -cne 'first-release' -or
            $null -ne $Receipt.UpgradeFromVersion -or
            $null -ne $Receipt.UpgradeFromNsisSha256 -or
            $null -ne $Receipt.UpgradeFromMsiSha256 -or
            $null -ne $Receipt.UpgradeFromSignerThumbprint
        ) {
            throw 'First-release smoke validation metadata is inconsistent.'
        }
        foreach ($checkName in @(
            'MsiUpgrade',
            'MsiDowngradeBlocked',
            'NsisUpgrade',
            'NsisDowngradeBlocked',
            'Upgrade'
        )) {
            if ([string]$Receipt.Checks[$checkName] -cne 'not-applicable-first-release') {
                throw "First-release smoke validation status is inconsistent: $checkName"
            }
        }
    }
    else {
        if (
            [string]$Receipt.UpgradeMode -cne 'previous-installers' -or
            [string]$Receipt.UpgradeFromVersion -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' -or
            [version]$Receipt.UpgradeFromVersion -ge [version]$Receipt.Version -or
            [string]$Receipt.UpgradeFromNsisSha256 -cnotmatch '^[0-9a-f]{64}$' -or
            [string]$Receipt.UpgradeFromMsiSha256 -cnotmatch '^[0-9a-f]{64}$' -or
            [string]$Receipt.UpgradeFromSignerThumbprint -cnotmatch '^[0-9A-F]{40}$'
        ) {
            throw 'Upgrade smoke validation metadata is incomplete or invalid.'
        }
        [void](Get-ReleaseSigningPolicySigner `
            -Policy $Policy `
            -Version ([string]$Receipt.UpgradeFromVersion) `
            -Thumbprint ([string]$Receipt.UpgradeFromSignerThumbprint))
    }
    foreach ($checkName in $expectedChecks) {
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
        if ([string]$Receipt.Checks[$checkName] -cne $expectedStatus) {
            throw "Windows smoke validation status is invalid: $checkName"
        }
    }

    $expectedFileNames = @($ReleaseRecords.Keys) + @('release-manifest.json')
    Assert-ExactMapKeys -Map $Receipt.Files -Expected $expectedFileNames -Context 'Windows smoke receipt Files'
    foreach ($name in $ReleaseRecords.Keys) {
        if ([string]$Receipt.Files[$name] -cne [string]$ReleaseRecords[$name].sha256) {
            throw "Windows smoke receipt is not bound to release file '$name'."
        }
    }
    if ([string]$Receipt.Files['release-manifest.json'] -cne $ManifestHash) {
        throw 'Windows smoke receipt is not bound to release-manifest.json.'
    }
}

if ($env:OS -ne 'Windows_NT') {
    throw 'Windows smoke receipt signing requires Windows Authenticode support.'
}
if ($ExpectedValidationNonce -cnotmatch '^[0-9a-f]{64}$') {
    throw 'ExpectedValidationNonce must be exactly 64 lowercase hexadecimal characters.'
}
if ($ExpectedCleanVmImageId -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._:/@+-]{7,127}$') {
    throw 'ExpectedCleanVmImageId must contain 8-128 safe image-identity characters.'
}
$timestampUri = $null
if (
    -not [Uri]::TryCreate($TimestampUrl, [UriKind]::Absolute, [ref]$timestampUri) -or
    $timestampUri.Scheme -notin @('http', 'https') -or
    -not [string]::IsNullOrWhiteSpace($timestampUri.UserInfo)
) {
    throw 'TimestampUrl must be an absolute HTTP or HTTPS URI without user information.'
}

foreach ($path in @(
    $policyPath,
    $policySchemaPath,
    $policyModulePath,
    $signingScriptPath,
    $validationScriptPath,
    $webVerifierPath
)) {
    Assert-RegularFile -Path $path
    Assert-PathHasNoReparsePoints -Path $path -TrustedRoot $repositoryRoot
}
$canonicalSignerThumbprint = $SignerThumbprint.ToUpperInvariant()
if ($canonicalSignerThumbprint -cnotmatch '^[0-9A-F]{40}$') {
    throw 'SignerThumbprint must be exactly 40 hexadecimal characters.'
}

$gitRoot = Get-CheckedGitOutput -ArgumentList @('rev-parse', '--show-toplevel')
if (-not [string]::Equals(
    [IO.Path]::GetFullPath($gitRoot),
    [IO.Path]::GetFullPath($repositoryRoot),
    [StringComparison]::OrdinalIgnoreCase
)) {
    throw 'The signing script is not running from the expected Git worktree.'
}
if ((Get-CheckedGitOutput -ArgumentList @('symbolic-ref', '--short', 'HEAD')) -cne 'main') {
    throw 'Windows smoke receipts may only be signed from branch main.'
}
$trackedInputs = @(
    'release-signing-policy.json',
    'release-signing-policy.schema.json',
    'release-manifest.schema.json',
    'scripts/release-signing-policy.psm1',
    'scripts/sign-windows-smoke-receipt.ps1',
    'scripts/test-windows-release-installers.ps1',
    'scripts/verify-windows-web-portable.ps1'
)
foreach ($trackedInput in $trackedInputs) {
    Invoke-CheckedGit -ArgumentList @('ls-files', '--error-unmatch', '--', $trackedInput)
}
$worktreeStatus = Get-CheckedGitOutput -ArgumentList @('status', '--porcelain=v1', '--untracked-files=all')
if (-not [string]::IsNullOrWhiteSpace($worktreeStatus)) {
    throw "Refusing to sign from a dirty Git worktree.`n$worktreeStatus"
}
$originUrl = Get-CheckedGitOutput -ArgumentList @('remote', 'get-url', 'origin')
if (
    $originUrl -cnotmatch '^(?:https://github\.com/neko233-com/nekossh(?:\.git)?|git@github\.com:neko233-com/nekossh\.git|ssh://git@github\.com/neko233-com/nekossh\.git)$'
) {
    throw "Git origin does not match $expectedRepository."
}
Invoke-CheckedGit -ArgumentList @('fetch', '--quiet', 'origin', 'main')
$headCommit = Get-CheckedGitOutput -ArgumentList @('rev-parse', 'HEAD')
$originCommit = Get-CheckedGitOutput -ArgumentList @('rev-parse', 'refs/remotes/origin/main')
if ($headCommit -cne $originCommit) {
    throw "Local main ($headCommit) does not exactly match origin/main ($originCommit)."
}
Import-Module $policyModulePath -Force -ErrorAction Stop
$policy = Import-ReleaseSigningPolicy -Path $policyPath
if ($policy.Repository -cne $expectedRepository) {
    throw 'Release signing policy repository is invalid.'
}

$releaseRoot = [IO.Path]::GetFullPath((Join-Path $repositoryRoot 'output/release'))
$candidateReleaseDirectory = if ([IO.Path]::IsPathRooted($ReleaseDirectory)) {
    [IO.Path]::GetFullPath($ReleaseDirectory)
} else {
    [IO.Path]::GetFullPath((Join-Path (Get-Location).Path $ReleaseDirectory))
}
if (-not (Test-ChildPath -Path $candidateReleaseDirectory -Parent $releaseRoot)) {
    throw "ReleaseDirectory must stay below '$releaseRoot'."
}
Assert-PathHasNoReparsePoints -Path $candidateReleaseDirectory -TrustedRoot $repositoryRoot
$resolvedReleaseDirectory = (Resolve-Path -LiteralPath $candidateReleaseDirectory).Path
if (-not [string]::Equals(
    $candidateReleaseDirectory.TrimEnd('\'),
    $resolvedReleaseDirectory.TrimEnd('\'),
    [StringComparison]::OrdinalIgnoreCase
)) {
    throw 'ReleaseDirectory changed identity while being resolved.'
}

$manifestPath = Join-Path $resolvedReleaseDirectory 'release-manifest.json'
$receiptPath = Join-Path $resolvedReleaseDirectory $receiptName
Assert-RegularFile -Path $manifestPath
Assert-RegularFile -Path $receiptPath
Assert-PathHasNoReparsePoints -Path $manifestPath -TrustedRoot $repositoryRoot
Assert-PathHasNoReparsePoints -Path $receiptPath -TrustedRoot $repositoryRoot
$unsignedReceiptHash = Get-Sha256 -Path $receiptPath
$unsignedSignature = Get-AuthenticodeSignature -LiteralPath $receiptPath
if (
    $unsignedSignature.Status.ToString() -cne 'NotSigned' -or
    $null -ne $unsignedSignature.SignerCertificate -or
    $null -ne $unsignedSignature.TimeStamperCertificate
) {
    throw "The input $receiptName must be an unsigned (NotSigned) clean-VM receipt."
}

$manifest = Import-NekoSshReleaseManifest -Path $manifestPath
if (
    -not ($manifest.schemaVersion -is [int] -or $manifest.schemaVersion -is [long]) -or
    [long]$manifest.schemaVersion -ne 3 -or
    [string]$manifest.repository -cne $expectedRepository -or
    $manifest.prerelease -isnot [bool] -or
    $manifest.prerelease -ne $false -or
    $manifest.publishable -isnot [bool] -or
    $manifest.publishable -ne $true -or
    [string]$manifest.commit -cne $headCommit -or
    [string]$manifest.commit -cnotmatch '^[0-9a-f]{40}$' -or
    [string]$manifest.version -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' -or
    [string]$manifest.tag -cne "v$($manifest.version)"
) {
    throw 'The release manifest is not a publishable stable schema-v3 release for the checked-out commit.'
}
$version = [string]$manifest.version
$shortCommit = $headCommit.Substring(0, 12)
if ((Split-Path $resolvedReleaseDirectory -Leaf) -cne "v$version-$shortCommit") {
    throw 'ReleaseDirectory name does not match the manifest version and commit.'
}
$policySigner = Get-ReleaseSigningPolicySigner `
    -Policy $policy `
    -Version $version `
    -Thumbprint $canonicalSignerThumbprint
if (
    [string]$manifest.signing.mode -cne 'authenticode' -or
    [string]$manifest.signing.expectedSignerThumbprint -cne $canonicalSignerThumbprint -or
    [string]$manifest.signing.expectedSignerSubject -cne $policySigner.Subject -or
    [string]$manifest.signing.expectedSignerSpkiSha256 -cne $policySigner.SpkiSha256 -or
    [string]$manifest.signing.policySha256 -cne $policy.Sha256 -or
    $manifest.signing.timestampRequired -isnot [bool] -or
    $manifest.signing.timestampRequired -ne $true
) {
    throw 'Release manifest signing metadata does not match the policy-selected signer.'
}

$nsisName = "NekoSSH_${version}_x64-setup.exe"
$msiName = "NekoSSH_${version}_x64_zh-CN.msi"
$webName = "NekoSSH_Web_${version}_windows_x64.zip"
$noticesName = 'THIRD_PARTY_NOTICES.txt'
$expectedKinds = [ordered]@{}
$expectedKinds[$msiName] = 'tauri-msi'
$expectedKinds[$nsisName] = 'tauri-nsis'
$expectedKinds[$webName] = 'web-portable'
$expectedKinds[$noticesName] = 'third-party-notices'
$manifestAssets = @($manifest.assets)
if ($manifestAssets.Count -ne 4) {
    throw 'Release manifest must contain the exact four expected Windows assets.'
}
$records = @{}
$paths = @{}
foreach ($asset in $manifestAssets) {
    $name = [string]$asset.name
    if (
        @($expectedKinds.Keys) -cnotcontains $name -or
        [string]$asset.kind -cne [string]$expectedKinds[$name]
    ) {
        throw "Unexpected release manifest asset or kind: '$name'."
    }
    if ($records.ContainsKey($name)) {
        throw "Duplicate release manifest asset: '$name'."
    }
    $records[$name] = $asset
    $paths[$name] = Assert-ManifestRecord `
        -Record $asset `
        -ExpectedName $name `
        -ReleaseRoot $resolvedReleaseDirectory
}
if ($records.Count -ne $expectedKinds.Count) {
    throw 'Release manifest asset set is incomplete.'
}
foreach ($specialRecord in @(
    [pscustomobject]@{ Record = $manifest.checksumFile; Name = 'SHA256SUMS.txt' },
    [pscustomobject]@{ Record = $manifest.releaseNotesFile; Name = 'RELEASE_NOTES.md' }
)) {
    $name = [string]$specialRecord.Name
    if ($records.ContainsKey($name)) {
        throw "Duplicate release manifest record: '$name'."
    }
    $records[$name] = $specialRecord.Record
    $paths[$name] = Assert-ManifestRecord `
        -Record $specialRecord.Record `
        -ExpectedName $name `
        -ReleaseRoot $resolvedReleaseDirectory
}

$expectedDirectoryNames = @($records.Keys) + @('release-manifest.json', $receiptName)
$actualDirectoryNames = @(
    Get-ChildItem -LiteralPath $resolvedReleaseDirectory -Force |
        ForEach-Object {
            if ($_.PSIsContainer) {
                throw "ReleaseDirectory contains an unexpected directory: $($_.Name)"
            }
            $_.Name
        } |
        Sort-Object -CaseSensitive
)
$expectedDirectoryNames = @($expectedDirectoryNames | Sort-Object -CaseSensitive)
if (($actualDirectoryNames -join "`n") -cne ($expectedDirectoryNames -join "`n")) {
    throw 'ReleaseDirectory contains missing or unexpected files.'
}

$expectedChecksumLines = @(
    $manifestAssets |
        Sort-Object name |
        ForEach-Object { "$($_.sha256)  $($_.name)" }
)
$actualChecksumLines = @(Get-Content -LiteralPath $paths['SHA256SUMS.txt'])
if (($actualChecksumLines -join "`n") -cne ($expectedChecksumLines -join "`n")) {
    throw 'SHA256SUMS.txt does not exactly match the release manifest.'
}

$signatureRecords = @($manifest.signatures)
$expectedSignatureNames = @($msiName, $nsisName, 'nekossh.exe') | Sort-Object
if (
    $signatureRecords.Count -ne 3 -or
    (@($signatureRecords.Name | Sort-Object) -join "`n") -cne ($expectedSignatureNames -join "`n")
) {
    throw 'Release manifest signature records are incomplete or unexpected.'
}
foreach ($signatureRecord in $signatureRecords) {
    if (
        [string]$signatureRecord.Status -cne 'Valid' -or
        [string]$signatureRecord.SignerThumbprint -cne $canonicalSignerThumbprint -or
        [string]$signatureRecord.SignerSubject -cne $policySigner.Subject -or
        [string]$signatureRecord.SignerSpkiSha256 -cne $policySigner.SpkiSha256 -or
        $signatureRecord.Timestamped -isnot [bool] -or
        $signatureRecord.Timestamped -ne $true
    ) {
        throw "Release manifest contains an invalid signature record: $($signatureRecord.Name)"
    }
}
Assert-ExpectedAuthenticodeSignature `
    -Path $paths[$nsisName] `
    -ExpectedThumbprint $canonicalSignerThumbprint `
    -PolicySigner $policySigner
Assert-ExpectedAuthenticodeSignature `
    -Path $paths[$msiName] `
    -ExpectedThumbprint $canonicalSignerThumbprint `
    -PolicySigner $policySigner

$webVerification = (
    & $webVerifierPath `
        -ArchivePath $paths[$webName] `
        -ExpectedVersion $version `
        -ExpectedCommit $headCommit `
        -ExpectedProjectSignerThumbprint $canonicalSignerThumbprint `
        -ExpectedProjectSignerSubject $policySigner.Subject `
        -ExpectedProjectSignerSpkiSha256 $policySigner.SpkiSha256 |
        Out-String
).Trim() | ConvertFrom-Json
if (
    [string]$webVerification.archiveSha256 -cne [string]$records[$webName].sha256 -or
    [string]$webVerification.version -cne [string]$manifest.webRuntime.version -or
    [string]$webVerification.selfTest -cne [string]$manifest.webRuntime.selfTest -or
    [string]$webVerification.node.version -cne [string]$manifest.webRuntime.node.version -or
    [string]$webVerification.node.sha256 -cne [string]$manifest.webRuntime.node.sha256 -or
    [string]$webVerification.node.status -cne [string]$manifest.webRuntime.node.status -or
    [string]$webVerification.node.signerSubject -cne
        [string]$manifest.webRuntime.node.signerSubject -or
    $webVerification.node.timestamped -ne $manifest.webRuntime.node.timestamped -or
    [string]$webVerification.launcher.status -cne
        [string]$manifest.webRuntime.launcher.status -or
    [string]$webVerification.launcher.signerThumbprint -cne
        [string]$manifest.webRuntime.launcher.signerThumbprint -or
    [string]$webVerification.launcher.signerSubject -cne
        [string]$manifest.webRuntime.launcher.signerSubject -or
    [string]$webVerification.launcher.signerSpkiSha256 -cne
        [string]$manifest.webRuntime.launcher.signerSpkiSha256 -or
    $webVerification.launcher.timestamped -ne
        $manifest.webRuntime.launcher.timestamped
) {
    throw 'The Web portable runtime failed release signing revalidation.'
}

$validationScriptHash = Get-ReleaseTextSha256 -Path $validationScriptPath
$manifestHash = Get-Sha256 -Path $manifestPath
if (
    $manifest.SourceFileSha256 -cne $manifestHash -or
    [long]$manifest.SourceFileLength -ne (Get-Item -LiteralPath $manifestPath).Length
) {
    throw 'release-manifest.json changed after strict validation.'
}
$receipt = Import-PowerShellDataFile -LiteralPath $receiptPath
Assert-ReceiptData `
    -Receipt $receipt `
    -Manifest $manifest `
    -ReleaseRecords $records `
    -ManifestHash $manifestHash `
    -ValidationScriptHash $validationScriptHash `
    -SigningPolicyHash $policy.Sha256 `
    -ExpectedNonce $ExpectedValidationNonce `
    -ExpectedImageId $ExpectedCleanVmImageId `
    -Policy $policy

$certificates = @(
    Get-ChildItem Cert:\CurrentUser\My, Cert:\LocalMachine\My -ErrorAction SilentlyContinue |
        Where-Object {
            $_.Thumbprint.ToUpperInvariant() -ceq $canonicalSignerThumbprint -and
            $_.HasPrivateKey
        }
)
if ($certificates.Count -eq 0) {
    throw 'The policy-selected code-signing certificate with an accessible private key is unavailable.'
}
$rawCertificateHashes = @(
    $certificates |
        ForEach-Object {
            $sha256 = [Security.Cryptography.SHA256]::Create()
            try {
                ([BitConverter]::ToString($sha256.ComputeHash($_.RawData))).Replace('-', '')
            }
            finally {
                $sha256.Dispose()
            }
        } |
        Sort-Object -Unique
)
if ($rawCertificateHashes.Count -ne 1) {
    throw 'Multiple different certificates unexpectedly share the configured thumbprint.'
}
foreach ($candidate in $certificates) {
    [void](Assert-ReleaseSigningCertificate `
        -Certificate $candidate `
        -Signer $policySigner `
        -RequirePrivateKey)
}
$certificate = $certificates[0]

$temporaryReceiptPath = Join-Path $resolvedReleaseDirectory (
    ".$receiptName.signing-$PID-$([guid]::NewGuid().ToString('N')).tmp.psd1"
)
$backupReceiptPath = Join-Path $resolvedReleaseDirectory (
    ".$receiptName.unsigned-backup-$PID-$([guid]::NewGuid().ToString('N')).tmp.psd1"
)
$preserveUnsignedBackup = $false
try {
    [IO.File]::Copy($receiptPath, $temporaryReceiptPath, $false)
    Assert-RegularFile -Path $temporaryReceiptPath
    if ((Get-AuthenticodeSignature -LiteralPath $temporaryReceiptPath).Status.ToString() -cne 'NotSigned') {
        throw 'The temporary receipt copy is not in a clean unsigned state.'
    }
    $signedTemporaryReceipt = Set-AuthenticodeSignature `
        -FilePath $temporaryReceiptPath `
        -Certificate $certificate `
        -HashAlgorithm SHA256 `
        -TimestampServer $TimestampUrl
    if (
        $signedTemporaryReceipt.Status.ToString() -cne 'Valid' -or
        $null -eq $signedTemporaryReceipt.SignerCertificate -or
        $signedTemporaryReceipt.SignerCertificate.Thumbprint.ToUpperInvariant() -cne $canonicalSignerThumbprint -or
        $null -eq $signedTemporaryReceipt.TimeStamperCertificate
    ) {
        throw 'The smoke receipt did not receive the expected valid, timestamped signature.'
    }
    [void](Assert-ReleaseSigningCertificate `
        -Certificate $signedTemporaryReceipt.SignerCertificate `
        -Signer $policySigner)
    $temporaryReceiptData = Import-PowerShellDataFile -LiteralPath $temporaryReceiptPath
    Assert-ReceiptData `
        -Receipt $temporaryReceiptData `
        -Manifest $manifest `
        -ReleaseRecords $records `
        -ManifestHash $manifestHash `
        -ValidationScriptHash $validationScriptHash `
        -SigningPolicyHash $policy.Sha256 `
        -ExpectedNonce $ExpectedValidationNonce `
        -ExpectedImageId $ExpectedCleanVmImageId `
        -Policy $policy
    if ((Get-Sha256 -Path $receiptPath) -cne $unsignedReceiptHash) {
        throw 'The unsigned smoke receipt changed during signing.'
    }

    Assert-PathHasNoReparsePoints -Path $resolvedReleaseDirectory -TrustedRoot $repositoryRoot
    Assert-PathHasNoReparsePoints -Path $receiptPath -TrustedRoot $repositoryRoot
    Assert-PathHasNoReparsePoints -Path $temporaryReceiptPath -TrustedRoot $repositoryRoot
    Assert-RegularFile -Path $receiptPath
    Assert-RegularFile -Path $temporaryReceiptPath
    if (Test-Path -LiteralPath $backupReceiptPath) {
        throw 'The generated unsigned-receipt backup path was unexpectedly created before replacement.'
    }
    [IO.File]::Replace($temporaryReceiptPath, $receiptPath, $backupReceiptPath, $true)
    Assert-RegularFile -Path $receiptPath
    Assert-PathHasNoReparsePoints -Path $receiptPath -TrustedRoot $repositoryRoot
    $finalSignature = Get-AuthenticodeSignature -LiteralPath $receiptPath
    if (
        $finalSignature.Status.ToString() -cne 'Valid' -or
        $null -eq $finalSignature.SignerCertificate -or
        $finalSignature.SignerCertificate.Thumbprint.ToUpperInvariant() -cne $canonicalSignerThumbprint -or
        $null -eq $finalSignature.TimeStamperCertificate
    ) {
        throw 'The atomically installed smoke receipt signature failed final validation.'
    }
    [void](Assert-ReleaseSigningCertificate `
        -Certificate $finalSignature.SignerCertificate `
        -Signer $policySigner)
    $finalReceipt = Import-PowerShellDataFile -LiteralPath $receiptPath
    Assert-ReceiptData `
        -Receipt $finalReceipt `
        -Manifest $manifest `
        -ReleaseRecords $records `
        -ManifestHash $manifestHash `
        -ValidationScriptHash $validationScriptHash `
        -SigningPolicyHash $policy.Sha256 `
        -ExpectedNonce $ExpectedValidationNonce `
        -ExpectedImageId $ExpectedCleanVmImageId `
        -Policy $policy
    Remove-Item -LiteralPath $backupReceiptPath -Force
}
catch {
    if (
        (Test-Path -LiteralPath $backupReceiptPath -PathType Leaf) -and
        (Test-Path -LiteralPath $receiptPath -PathType Leaf)
    ) {
        try {
            $failedReceiptPath = "$temporaryReceiptPath.failed"
            [IO.File]::Replace($backupReceiptPath, $receiptPath, $failedReceiptPath, $true)
            if (Test-Path -LiteralPath $failedReceiptPath -PathType Leaf) {
                Remove-Item -LiteralPath $failedReceiptPath -Force
            }
        }
        catch {
            $preserveUnsignedBackup = $true
            Write-Warning "The original unsigned receipt backup could not be restored automatically; preserve it at '$backupReceiptPath'."
        }
    }
    throw
}
finally {
    $cleanupPaths = @($temporaryReceiptPath)
    if (-not $preserveUnsignedBackup) {
        $cleanupPaths += $backupReceiptPath
    }
    foreach ($temporaryPath in $cleanupPaths) {
        if (Test-Path -LiteralPath $temporaryPath -PathType Leaf) {
            Remove-Item -LiteralPath $temporaryPath -Force
        }
    }
}

Write-Host "Signed and revalidated Windows smoke receipt: $receiptPath"
