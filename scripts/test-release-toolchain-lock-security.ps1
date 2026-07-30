[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ToolchainLockPath,
    [Parameter(Mandatory)][string]$ExpectedToolchainLockSha256
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$modulePath = Join-Path $PSScriptRoot 'release-toolchain-lock.psm1'
Import-Module -Name $modulePath -Force -ErrorAction Stop

$temporaryParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$testRoot = Join-Path $temporaryParent (
    "nekossh-toolchain-lock-test-$PID-$([guid]::NewGuid().ToString('N'))"
)
$utf8 = [Text.UTF8Encoding]::new($false, $true)

function Get-LowercaseFileSha256 {
    param([Parameter(Mandatory)][string]$Path)

    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Copy-LockObject {
    param([Parameter(Mandatory)]$Object)

    return ($Object | ConvertTo-Json -Depth 10 -Compress) | ConvertFrom-Json
}

function Write-LockVariant {
    param(
        [Parameter(Mandatory)]$Object,
        [Parameter(Mandatory)][string]$Name
    )

    $path = Join-Path $testRoot "$Name.json"
    [IO.File]::WriteAllText(
        $path,
        (($Object | ConvertTo-Json -Depth 10) + "`n"),
        $utf8
    )
    return $path
}

function Assert-LockRejected {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$ExpectedSha256,
        [Parameter(Mandatory)][string]$MessagePattern,
        [Parameter(Mandatory)][string]$FailureMessage
    )

    $rejected = $false
    try {
        Import-NekoSshReleaseToolchainLock `
            -Path $Path `
            -ExpectedSha256 $ExpectedSha256 | Out-Null
    }
    catch {
        $rejected = $_.Exception.Message -like $MessagePattern
    }
    if (-not $rejected) {
        throw $FailureMessage
    }
}

[void][IO.Directory]::CreateDirectory($testRoot)
try {
    $fakeRepositoryRoot = Join-Path $testRoot 'repository'
    [void][IO.Directory]::CreateDirectory($fakeRepositoryRoot)
    $insideLockPath = Join-Path $fakeRepositoryRoot 'builder-lock.json'
    $insideRejected = $false
    try {
        Assert-NekoSshToolchainLockOutsideRepository `
            -LockPath $insideLockPath `
            -RepositoryRoot $fakeRepositoryRoot
    }
    catch {
        $insideRejected = $_.Exception.Message -like '*outside the Git repository*'
    }
    if (-not $insideRejected) {
        throw 'Toolchain path gate accepted a builder lock inside the repository.'
    }
    Assert-NekoSshToolchainLockOutsideRepository `
        -LockPath (Join-Path $testRoot 'repository-sibling\builder-lock.json') `
        -RepositoryRoot $fakeRepositoryRoot
    $incompleteCoverageRejected = $false
    try {
        Assert-NekoSshCompleteProductionToolchainCoverage
    }
    catch {
        $incompleteCoverageRejected = $_.Exception.Message -like '*Production release is fail-closed*'
    }
    if (-not $incompleteCoverageRejected) {
        throw 'Phase-1 toolchain coverage unexpectedly allowed a production release.'
    }

    $sourceLockPath = (Resolve-Path -LiteralPath $ToolchainLockPath).Path
    if ((Get-LowercaseFileSha256 -Path $sourceLockPath) -cne $ExpectedToolchainLockSha256) {
        throw 'Test input toolchain lock does not match its expected SHA-256 anchor.'
    }
    $sourceText = [IO.File]::ReadAllText($sourceLockPath, $utf8)
    $sourceObject = $sourceText | ConvertFrom-Json

    $nodeInjectionPath = Join-Path $testRoot 'node-options-injection.cjs'
    $nodeInjectionSentinel = Join-Path $testRoot 'node-options-executed.txt'
    $escapedNodeSentinel = $nodeInjectionSentinel.Replace('\', '\\').Replace("'", "\'")
    [IO.File]::WriteAllText(
        $nodeInjectionPath,
        "require('node:fs').writeFileSync('$escapedNodeSentinel', 'executed')`n",
        $utf8
    )
    $previousNodeOptions = [Environment]::GetEnvironmentVariable(
        'NODE_OPTIONS',
        [EnvironmentVariableTarget]::Process
    )
    try {
        [Environment]::SetEnvironmentVariable(
            'NODE_OPTIONS',
            "--require `"$nodeInjectionPath`"",
            [EnvironmentVariableTarget]::Process
        )
        Import-NekoSshReleaseToolchainLock `
            -Path $sourceLockPath `
            -ExpectedSha256 $ExpectedToolchainLockSha256 | Out-Null
    }
    finally {
        [Environment]::SetEnvironmentVariable(
            'NODE_OPTIONS',
            $previousNodeOptions,
            [EnvironmentVariableTarget]::Process
        )
    }
    if (Test-Path -LiteralPath $nodeInjectionSentinel) {
        throw 'Locked tool version probing executed inherited NODE_OPTIONS code.'
    }

    Assert-LockRejected `
        -Path $sourceLockPath `
        -ExpectedSha256 ('0' * 64) `
        -MessagePattern '*independent expected anchor*' `
        -FailureMessage 'Toolchain lock parser accepted an incorrect independent lock hash.'

    $variant = Copy-LockObject -Object $sourceObject
    Add-Member -InputObject $variant -MemberType NoteProperty -Name unexpected -Value $true
    $variantPath = Write-LockVariant -Object $variant -Name 'unknown-field'
    Assert-LockRejected `
        -Path $variantPath `
        -ExpectedSha256 (Get-LowercaseFileSha256 -Path $variantPath) `
        -MessagePattern '*fields are incomplete or unexpected*' `
        -FailureMessage 'Toolchain lock parser accepted an unknown top-level field.'

    $variant = Copy-LockObject -Object $sourceObject
    $variant.tools.node.sha256 = '0' * 64
    $variantPath = Write-LockVariant -Object $variant -Name 'wrong-tool-hash'
    Assert-LockRejected `
        -Path $variantPath `
        -ExpectedSha256 (Get-LowercaseFileSha256 -Path $variantPath) `
        -MessagePattern '*Locked tool identity mismatch: node*' `
        -FailureMessage 'Toolchain lock parser accepted an incorrect tool SHA-256.'

    $variant = Copy-LockObject -Object $sourceObject
    $variant.tools.node.length = [long]$variant.tools.node.length + 1
    $variantPath = Write-LockVariant -Object $variant -Name 'wrong-tool-length'
    Assert-LockRejected `
        -Path $variantPath `
        -ExpectedSha256 (Get-LowercaseFileSha256 -Path $variantPath) `
        -MessagePattern '*Locked tool identity mismatch: node*' `
        -FailureMessage 'Toolchain lock parser accepted an incorrect tool length.'

    $variant = Copy-LockObject -Object $sourceObject
    $variant.tools.node.version = "$($variant.tools.node.version)-tampered"
    $variantPath = Write-LockVariant -Object $variant -Name 'wrong-version'
    Assert-LockRejected `
        -Path $variantPath `
        -ExpectedSha256 (Get-LowercaseFileSha256 -Path $variantPath) `
        -MessagePattern '*Locked tool identity mismatch: node*' `
        -FailureMessage 'Toolchain lock parser accepted an incorrect exact version.'

    $variant = Copy-LockObject -Object $sourceObject
    if ($variant.tools.node.signature.status -ceq 'Valid') {
        $variant.tools.node.signature.subject = "$($variant.tools.node.signature.subject), OU=Tampered"
    } else {
        $variant.tools.node.signature.status = 'Valid'
        $variant.tools.node.signature.subject = 'CN=Tampered'
        $variant.tools.node.signature.thumbprint = 'A' * 40
        $variant.tools.node.signature.spkiSha256 = 'b' * 64
        $variant.tools.node.signature.timestamped = $false
    }
    $variantPath = Write-LockVariant -Object $variant -Name 'wrong-signer'
    Assert-LockRejected `
        -Path $variantPath `
        -ExpectedSha256 (Get-LowercaseFileSha256 -Path $variantPath) `
        -MessagePattern '*Locked tool identity mismatch: node*' `
        -FailureMessage 'Toolchain lock parser accepted an incorrect signer identity.'

    $nodeCopyPath = Join-Path $testRoot 'node-copy.exe'
    [IO.File]::Copy([string]$sourceObject.tools.node.path, $nodeCopyPath, $false)
    $variant = Copy-LockObject -Object $sourceObject
    $nodeCopy = Get-Item -LiteralPath $nodeCopyPath -Force
    $variant.tools.node.path = $nodeCopyPath
    $variant.tools.node.length = [long]$nodeCopy.Length
    $variant.tools.node.sha256 = Get-LowercaseFileSha256 -Path $nodeCopyPath
    $variantPath = Write-LockVariant -Object $variant -Name 'path-swap'
    $variantHash = Get-LowercaseFileSha256 -Path $variantPath
    $imported = Import-NekoSshReleaseToolchainLock `
        -Path $variantPath `
        -ExpectedSha256 $variantHash
    $stream = [IO.FileStream]::new(
        $nodeCopyPath,
        [IO.FileMode]::Open,
        [IO.FileAccess]::ReadWrite,
        [IO.FileShare]::None
    )
    try {
        $stream.Position = $stream.Length - 1
        $original = $stream.ReadByte()
        $stream.Position = $stream.Length - 1
        $stream.WriteByte([byte](($original + 1) % 256))
        $stream.Flush($true)
    }
    finally {
        $stream.Dispose()
    }
    $pathSwapRejected = $false
    try {
        Assert-NekoSshReleaseToolchainLock -Toolchain $imported | Out-Null
    }
    catch {
        $pathSwapRejected = $_.Exception.Message -like '*Locked tool identity mismatch: node*'
    }
    if (-not $pathSwapRejected) {
        throw 'Toolchain revalidation accepted an in-place path swap after initial import.'
    }

    [pscustomobject]@{
        IndependentLockHash = 'passed'
        OutsideRepository = 'passed'
        IncompleteProductionCoverage = 'rejected'
        UnknownFields = 'passed'
        ToolSha256 = 'passed'
        ToolLength = 'passed'
        ExactVersion = 'passed'
        SignerIdentity = 'passed'
        PathSwap = 'passed'
        ProbeEnvironmentIsolation = 'passed'
    } | ConvertTo-Json -Compress
}
finally {
    if (Test-Path -LiteralPath $testRoot) {
        $resolved = [IO.Path]::GetFullPath($testRoot)
        $parent = $temporaryParent.TrimEnd('\', '/')
        if (
            -not $resolved.StartsWith(
                "$parent$([IO.Path]::DirectorySeparatorChar)",
                [StringComparison]::OrdinalIgnoreCase
            )
        ) {
            throw "Refusing to remove toolchain lock test data outside the temporary directory: $resolved"
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
