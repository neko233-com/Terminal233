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

function Get-CurrentUserSid {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    try {
        if ($null -eq $identity.User) {
            throw 'The current Windows identity does not have a security identifier.'
        }
        return [Security.Principal.SecurityIdentifier]::new($identity.User.Value)
    }
    finally {
        $identity.Dispose()
    }
}

function Get-SigningAclSids {
    return @(
        (Get-CurrentUserSid)
        [Security.Principal.SecurityIdentifier]::new('S-1-5-18')
        [Security.Principal.SecurityIdentifier]::new('S-1-5-32-544')
    )
}

function Assert-DirectoryNotWritableByUntrustedPrincipal {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)]
        [Security.Principal.SecurityIdentifier[]]$AllowedSids
    )

    $item = Get-Item -LiteralPath $Path -Force
    if (-not $item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Private signing parent must be a regular directory: $Path"
    }
    $acl = $item.GetAccessControl(
        [Security.AccessControl.AccessControlSections]::Access -bor
        [Security.AccessControl.AccessControlSections]::Owner
    )
    $owner = $acl.GetOwner([Security.Principal.SecurityIdentifier])
    $allowedSidValues = @($AllowedSids | ForEach-Object { $_.Value })
    if ($allowedSidValues -cnotcontains $owner.Value) {
        throw "Private signing parent has an untrusted owner '$($owner.Value)': $Path"
    }

    $dangerousRights = (
        [Security.AccessControl.FileSystemRights]::WriteData -bor
        [Security.AccessControl.FileSystemRights]::AppendData -bor
        [Security.AccessControl.FileSystemRights]::WriteExtendedAttributes -bor
        [Security.AccessControl.FileSystemRights]::WriteAttributes -bor
        [Security.AccessControl.FileSystemRights]::DeleteSubdirectoriesAndFiles -bor
        [Security.AccessControl.FileSystemRights]::Delete -bor
        [Security.AccessControl.FileSystemRights]::ChangePermissions -bor
        [Security.AccessControl.FileSystemRights]::TakeOwnership
    )
    foreach ($rule in $acl.GetAccessRules(
        $true,
        $true,
        [Security.Principal.SecurityIdentifier]
    )) {
        if (
            $rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow -or
            ($rule.PropagationFlags -band [Security.AccessControl.PropagationFlags]::InheritOnly) -ne 0
        ) {
            continue
        }
        if (
            $allowedSidValues -cnotcontains $rule.IdentityReference.Value -and
            ($rule.FileSystemRights -band $dangerousRights) -ne 0
        ) {
            throw (
                "Private signing parent grants write/delete control to untrusted SID " +
                "'$($rule.IdentityReference.Value)': $Path"
            )
        }
    }
}

function Assert-AncestorsDoNotGrantUntrustedDeleteChild {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)]
        [Security.Principal.SecurityIdentifier[]]$AllowedSids
    )

    $fullPath = [IO.Path]::GetFullPath($Path)
    $volumeRoot = [IO.Path]::GetPathRoot($fullPath)
    $relative = $fullPath.Substring($volumeRoot.Length).TrimStart(
        [IO.Path]::DirectorySeparatorChar,
        [IO.Path]::AltDirectorySeparatorChar
    )
    $segments = @(
        $relative.Split(
            @([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar),
            [StringSplitOptions]::RemoveEmptyEntries
        )
    )
    $allowedSidValues = @($AllowedSids | ForEach-Object { $_.Value })
    $current = $volumeRoot
    for ($index = 0; $index -lt $segments.Count; $index += 1) {
        $parent = Get-Item -LiteralPath $current -Force
        $acl = $parent.GetAccessControl(
            [Security.AccessControl.AccessControlSections]::Access
        )
        foreach ($rule in $acl.GetAccessRules(
            $true,
            $true,
            [Security.Principal.SecurityIdentifier]
        )) {
            if (
                $rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow -or
                ($rule.PropagationFlags -band [Security.AccessControl.PropagationFlags]::InheritOnly) -ne 0
            ) {
                continue
            }
            if (
                $allowedSidValues -cnotcontains $rule.IdentityReference.Value -and
                ($rule.FileSystemRights -band (
                    [Security.AccessControl.FileSystemRights]::DeleteSubdirectoriesAndFiles
                )) -ne 0
            ) {
                throw (
                    "Ancestor '$current' lets untrusted SID " +
                    "'$($rule.IdentityReference.Value)' delete signing descendants."
                )
            }
        }
        $current = Join-Path $current $segments[$index]
    }
}

function Assert-PrivateSigningDirectory {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)]
        [Security.Principal.SecurityIdentifier[]]$AllowedSids
    )

    Assert-RegularFileSystemDirectory -Path $Path
    $volumeRoot = [IO.Path]::GetPathRoot([IO.Path]::GetFullPath($Path))
    Assert-PathHasNoReparsePoints -Path $Path -TrustedRoot $volumeRoot
    $item = Get-Item -LiteralPath $Path -Force
    $acl = $item.GetAccessControl(
        [Security.AccessControl.AccessControlSections]::Access -bor
        [Security.AccessControl.AccessControlSections]::Owner
    )
    if (-not $acl.AreAccessRulesProtected) {
        throw "Private signing directory must disable inherited access rules: $Path"
    }
    $currentSid = Get-CurrentUserSid
    $owner = $acl.GetOwner([Security.Principal.SecurityIdentifier])
    if ($owner.Value -cne $currentSid.Value) {
        throw "Private signing directory owner is not the current signer identity: $Path"
    }

    $allowedSidValues = @($AllowedSids | ForEach-Object { $_.Value })
    $fullControlSids = [Collections.Generic.HashSet[string]]::new(
        [StringComparer]::Ordinal
    )
    foreach ($rule in $acl.GetAccessRules(
        $true,
        $true,
        [Security.Principal.SecurityIdentifier]
    )) {
        if (
            $rule.IsInherited -or
            $rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow -or
            $allowedSidValues -cnotcontains $rule.IdentityReference.Value -or
            ($rule.FileSystemRights -band [Security.AccessControl.FileSystemRights]::FullControl) -ne
                [Security.AccessControl.FileSystemRights]::FullControl
        ) {
            throw "Private signing directory contains an unexpected ACL entry: $Path"
        }
        [void]$fullControlSids.Add($rule.IdentityReference.Value)
    }
    foreach ($allowedSid in $allowedSidValues) {
        if (-not $fullControlSids.Contains($allowedSid)) {
            throw "Private signing directory is missing required full control for SID '$allowedSid'."
        }
    }
}

function Assert-RegularFileSystemDirectory {
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Container)) {
        throw "Required regular directory is missing: $Path"
    }
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Required directory must not be a reparse point: $Path"
    }
}

function New-PrivateSigningDirectory {
    $localAppData = [Environment]::GetFolderPath(
        [Environment+SpecialFolder]::LocalApplicationData
    )
    if (
        [string]::IsNullOrWhiteSpace($localAppData) -or
        -not [IO.Path]::IsPathRooted($localAppData) -or
        $localAppData.StartsWith('\\', [StringComparison]::Ordinal)
    ) {
        throw 'A local, absolute Windows LocalApplicationData directory is required for signing.'
    }
    $localAppData = [IO.Path]::GetFullPath($localAppData)
    Assert-RegularFileSystemDirectory -Path $localAppData
    Assert-PathHasNoReparsePoints `
        -Path $localAppData `
        -TrustedRoot ([IO.Path]::GetPathRoot($localAppData))

    $allowedSids = @(Get-SigningAclSids)
    Assert-AncestorsDoNotGrantUntrustedDeleteChild `
        -Path $localAppData `
        -AllowedSids $allowedSids
    Assert-DirectoryNotWritableByUntrustedPrincipal `
        -Path $localAppData `
        -AllowedSids $allowedSids
    $currentSid = Get-CurrentUserSid
    $security = [Security.AccessControl.DirectorySecurity]::new()
    $security.SetAccessRuleProtection($true, $false)
    $security.SetOwner($currentSid)
    $inheritanceFlags = (
        [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor
        [Security.AccessControl.InheritanceFlags]::ObjectInherit
    )
    foreach ($sid in $allowedSids) {
        $rule = [Security.AccessControl.FileSystemAccessRule]::new(
            $sid,
            [Security.AccessControl.FileSystemRights]::FullControl,
            $inheritanceFlags,
            [Security.AccessControl.PropagationFlags]::None,
            [Security.AccessControl.AccessControlType]::Allow
        )
        [void]$security.AddAccessRule($rule)
    }

    for ($attempt = 0; $attempt -lt 8; $attempt += 1) {
        $candidate = Join-Path $localAppData (
            "NekoSSH-signing-$PID-$([guid]::NewGuid().ToString('N'))"
        )
        if (Test-Path -LiteralPath $candidate) {
            continue
        }
        try {
            [void][IO.Directory]::CreateDirectory($candidate, $security)
            Assert-PrivateSigningDirectory -Path $candidate -AllowedSids $allowedSids
            return $candidate
        }
        catch {
            $creationFailure = $_
            if (Test-Path -LiteralPath $candidate -PathType Container) {
                try {
                    [IO.Directory]::Delete($candidate, $false)
                }
                catch {
                    Write-Warning (
                        "Unable to remove rejected private signing directory '$candidate': " +
                        $_.Exception.Message
                    )
                }
            }
            throw $creationFailure
        }
    }
    throw 'Unable to allocate a unique private signing directory.'
}

function Initialize-SigningNativeMethods {
    if ($null -ne ('NekoSSH.SigningNativeMethods' -as [type])) {
        return
    }
    Add-Type -Language CSharp -ErrorAction Stop -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;

namespace NekoSSH
{
    [StructLayout(LayoutKind.Sequential)]
    public struct SigningFileInformation
    {
        public uint FileAttributes;
        public System.Runtime.InteropServices.ComTypes.FILETIME CreationTime;
        public System.Runtime.InteropServices.ComTypes.FILETIME LastAccessTime;
        public System.Runtime.InteropServices.ComTypes.FILETIME LastWriteTime;
        public uint VolumeSerialNumber;
        public uint FileSizeHigh;
        public uint FileSizeLow;
        public uint NumberOfLinks;
        public uint FileIndexHigh;
        public uint FileIndexLow;
    }

    public static class SigningNativeMethods
    {
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool GetFileInformationByHandle(
            SafeFileHandle file,
            out SigningFileInformation information
        );

        public static SigningFileInformation GetFileInformation(SafeFileHandle file)
        {
            SigningFileInformation information;
            if (!GetFileInformationByHandle(file, out information))
            {
                throw new Win32Exception(Marshal.GetLastWin32Error());
            }
            return information;
        }
    }
}
'@
}

function Get-SingleLinkFileIdentity {
    param([Parameter(Mandatory)][string]$Path)

    Assert-RegularFile -Path $Path
    Initialize-SigningNativeMethods
    $stream = [IO.File]::Open(
        $Path,
        [IO.FileMode]::Open,
        [IO.FileAccess]::Read,
        [IO.FileShare]::Read
    )
    try {
        $information = [NekoSSH.SigningNativeMethods]::GetFileInformation(
            $stream.SafeFileHandle
        )
        if ($information.NumberOfLinks -ne 1) {
            throw "Private signing files must have exactly one hard link: $Path"
        }
        return (
            '{0:x8}:{1:x8}{2:x8}' -f
            $information.VolumeSerialNumber,
            $information.FileIndexHigh,
            $information.FileIndexLow
        )
    }
    finally {
        $stream.Dispose()
    }
}

function Copy-RegularFileToNewPath {
    param(
        [Parameter(Mandatory)][string]$Source,
        [Parameter(Mandatory)][string]$Destination
    )

    Assert-RegularFile -Path $Source
    $sourceStream = [IO.File]::Open(
        $Source,
        [IO.FileMode]::Open,
        [IO.FileAccess]::Read,
        [IO.FileShare]::Read
    )
    try {
        $destinationStream = [IO.FileStream]::new(
            $Destination,
            [IO.FileMode]::CreateNew,
            [IO.FileAccess]::Write,
            [IO.FileShare]::None,
            4096,
            [IO.FileOptions]::WriteThrough
        )
        try {
            $sourceStream.CopyTo($destinationStream)
            $destinationStream.Flush($true)
        }
        finally {
            $destinationStream.Dispose()
        }
    }
    finally {
        $sourceStream.Dispose()
    }
}

function Restore-UnsignedReceiptFromPrivateBackup {
    param(
        [Parameter(Mandatory)][string]$PrivateBackupPath,
        [Parameter(Mandatory)][string]$ReceiptPath,
        [Parameter(Mandatory)][string]$ReleaseDirectory,
        [Parameter(Mandatory)][string]$ExpectedSha256
    )

    Assert-RegularFile -Path $PrivateBackupPath
    [void](Get-SingleLinkFileIdentity -Path $PrivateBackupPath)
    if ((Get-Sha256 -Path $PrivateBackupPath) -cne $ExpectedSha256) {
        throw 'The protected unsigned receipt backup changed before recovery.'
    }
    if ((Get-AuthenticodeSignature -LiteralPath $PrivateBackupPath).Status.ToString() -cne 'NotSigned') {
        throw 'The protected recovery receipt is no longer unsigned.'
    }
    $restorePath = Join-Path $ReleaseDirectory (
        ".$receiptName.restore-$PID-$([guid]::NewGuid().ToString('N')).tmp.psd1"
    )
    $displacedPath = Join-Path $ReleaseDirectory (
        ".$receiptName.displaced-$PID-$([guid]::NewGuid().ToString('N')).tmp.psd1"
    )
    try {
        Copy-RegularFileToNewPath -Source $PrivateBackupPath -Destination $restorePath
        if ((Get-Sha256 -Path $restorePath) -cne $ExpectedSha256) {
            throw 'The release-directory recovery copy does not match the protected backup.'
        }
        Assert-PathHasNoReparsePoints -Path $ReleaseDirectory -TrustedRoot $repositoryRoot
        Assert-PathHasNoReparsePoints -Path $restorePath -TrustedRoot $repositoryRoot
        if (Test-Path -LiteralPath $ReceiptPath) {
            Assert-RegularFile -Path $ReceiptPath
            Assert-PathHasNoReparsePoints -Path $ReceiptPath -TrustedRoot $repositoryRoot
            if (Test-Path -LiteralPath $displacedPath) {
                throw 'The generated displaced-receipt path already exists.'
            }
            [IO.File]::Replace($restorePath, $ReceiptPath, $displacedPath, $true)
        } else {
            [IO.File]::Move($restorePath, $ReceiptPath)
        }
        Assert-RegularFile -Path $ReceiptPath
        Assert-PathHasNoReparsePoints -Path $ReceiptPath -TrustedRoot $repositoryRoot
        if ((Get-Sha256 -Path $ReceiptPath) -cne $ExpectedSha256) {
            throw 'The unsigned smoke receipt recovery hash is invalid.'
        }
        $restoredSignature = Get-AuthenticodeSignature -LiteralPath $ReceiptPath
        if (
            $restoredSignature.Status.ToString() -cne 'NotSigned' -or
            $null -ne $restoredSignature.SignerCertificate -or
            $null -ne $restoredSignature.TimeStamperCertificate
        ) {
            throw 'The restored smoke receipt is not in its original unsigned state.'
        }
        if (Test-Path -LiteralPath $displacedPath) {
            Assert-RegularFile -Path $displacedPath
            Assert-PathHasNoReparsePoints -Path $displacedPath -TrustedRoot $repositoryRoot
            Remove-Item -LiteralPath $displacedPath -Force
        }
    }
    finally {
        if (Test-Path -LiteralPath $restorePath) {
            Assert-RegularFile -Path $restorePath
            Assert-PathHasNoReparsePoints -Path $restorePath -TrustedRoot $repositoryRoot
            Remove-Item -LiteralPath $restorePath -Force
        }
        if (Test-Path -LiteralPath $displacedPath) {
            Assert-RegularFile -Path $displacedPath
            Assert-PathHasNoReparsePoints -Path $displacedPath -TrustedRoot $repositoryRoot
            Remove-Item -LiteralPath $displacedPath -Force
        }
    }
}

function Remove-PrivateSigningDirectory {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)]
        [Security.Principal.SecurityIdentifier[]]$AllowedSids,
        [Parameter(Mandatory)][string[]]$ExpectedFiles
    )

    Assert-PrivateSigningDirectory -Path $Path -AllowedSids $AllowedSids
    $expectedPaths = @(
        $ExpectedFiles |
            ForEach-Object { [IO.Path]::GetFullPath($_) }
    )
    $actualItems = @(Get-ChildItem -LiteralPath $Path -Force)
    foreach ($item in $actualItems) {
        if (
            $item.PSIsContainer -or
            ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or
            $expectedPaths -cnotcontains [IO.Path]::GetFullPath($item.FullName)
        ) {
            throw "Private signing directory contains an unexpected entry: $($item.FullName)"
        }
    }
    foreach ($expectedPath in $expectedPaths) {
        if (Test-Path -LiteralPath $expectedPath) {
            Assert-RegularFile -Path $expectedPath
            Remove-Item -LiteralPath $expectedPath -Force
        }
    }
    [IO.Directory]::Delete($Path, $false)
}

function Get-ResolvedGitExecutable {
    $commands = @(
        Get-Command -Name git.exe -CommandType Application -ErrorAction Stop
    )
    if ($commands.Count -eq 0) {
        throw 'A Git application executable is required.'
    }
    $gitPath = [IO.Path]::GetFullPath([string]$commands[0].Source)
    if ($gitPath.StartsWith('\\', [StringComparison]::Ordinal)) {
        throw 'The Git executable must be installed on a local Windows volume.'
    }
    Assert-RegularFile -Path $gitPath
    Assert-PathHasNoReparsePoints `
        -Path $gitPath `
        -TrustedRoot ([IO.Path]::GetPathRoot($gitPath))
    $gitSignature = Get-AuthenticodeSignature -LiteralPath $gitPath
    if (
        $gitSignature.Status.ToString() -cne 'Valid' -or
        $null -eq $gitSignature.SignerCertificate -or
        $null -eq $gitSignature.TimeStamperCertificate
    ) {
        throw 'The Git executable must have a valid, trusted, timestamped Authenticode signature.'
    }
    return $gitPath
}

function Get-CheckedGitOutput {
    param([Parameter(Mandatory)][string[]]$ArgumentList)

    $output = & $gitExecutable -C $repositoryRoot @ArgumentList 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "Git command failed: git.exe $($ArgumentList -join ' ')`n$($output -join "`n")"
    }
    return ($output -join "`n").Trim()
}

function Invoke-CheckedGit {
    param([Parameter(Mandatory)][string[]]$ArgumentList)

    [void](Get-CheckedGitOutput -ArgumentList $ArgumentList)
}

function Assert-TrackedInputsMatchHead {
    param([Parameter(Mandatory)][string[]]$Paths)

    foreach ($trackedPath in $Paths) {
        $workingPath = [IO.Path]::GetFullPath((Join-Path $repositoryRoot $trackedPath))
        if (-not (Test-ChildPath -Path $workingPath -Parent $repositoryRoot)) {
            throw "Trusted signing input escaped the repository: $trackedPath"
        }
        Assert-RegularFile -Path $workingPath
        Assert-PathHasNoReparsePoints -Path $workingPath -TrustedRoot $repositoryRoot
        $indexState = Get-CheckedGitOutput -ArgumentList @(
            'ls-files',
            '-v',
            '--',
            $trackedPath
        )
        if ($indexState -cne "H $trackedPath") {
            throw (
                "Trusted signing input '$trackedPath' has an unexpected Git index flag/state " +
                "'$indexState'. Assume-unchanged and skip-worktree are forbidden."
            )
        }
        $headBlob = Get-CheckedGitOutput -ArgumentList @(
            'rev-parse',
            "HEAD:$trackedPath"
        )
        $indexBlob = Get-CheckedGitOutput -ArgumentList @(
            'rev-parse',
            ":$trackedPath"
        )
        $worktreeBlob = Get-CheckedGitOutput -ArgumentList @(
            'hash-object',
            '--no-filters',
            '--',
            $trackedPath
        )
        if (
            $headBlob -cnotmatch '^[0-9a-f]{40}$' -or
            $indexBlob -cne $headBlob -or
            $worktreeBlob -cne $headBlob
        ) {
            throw "Trusted signing input '$trackedPath' does not exactly match HEAD."
        }
    }
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
$gitExecutable = Get-ResolvedGitExecutable
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
    '.gitattributes',
    'release-signing-policy.json',
    'release-signing-policy.schema.json',
    'release-manifest.schema.json',
    'scripts/release-signing-policy.psm1',
    'scripts/sign-windows-smoke-receipt.ps1',
    'scripts/test-windows-release-installers.ps1',
    'scripts/verify-windows-web-portable.ps1'
)
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
Assert-TrackedInputsMatchHead -Paths $trackedInputs
$worktreeStatus = Get-CheckedGitOutput -ArgumentList @(
    'status',
    '--porcelain=v1',
    '--untracked-files=all'
)
if (-not [string]::IsNullOrWhiteSpace($worktreeStatus)) {
    throw "Refusing to sign from a dirty Git worktree.`n$worktreeStatus"
}
Import-Module $policyModulePath -Force -ErrorAction Stop
$policy = Import-ReleaseSigningPolicy -Path $policyPath
if ($policy.Repository -cne $expectedRepository) {
    throw 'Release signing policy repository is invalid.'
}
$headPolicyBlob = Get-CheckedGitOutput -ArgumentList @(
    'rev-parse',
    'HEAD:release-signing-policy.json'
)
if ($policy.SourceGitBlobId -cne $headPolicyBlob) {
    throw 'The parsed release signing policy bytes do not exactly match HEAD.'
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

$privateSigningDirectory = $null
$privateSigningSids = @()
$protectedUnsignedReceiptPath = $null
$protectedSigningReceiptPath = $null
$temporaryReceiptPath = Join-Path $resolvedReleaseDirectory (
    ".$receiptName.installing-$PID-$([guid]::NewGuid().ToString('N')).tmp.psd1"
)
$backupReceiptPath = Join-Path $resolvedReleaseDirectory (
    ".$receiptName.unsigned-backup-$PID-$([guid]::NewGuid().ToString('N')).tmp.psd1"
)
$receiptWasReplaced = $false
$signingCompleted = $false
$preservePrivateSigningDirectory = $false
try {
    Assert-TrackedInputsMatchHead -Paths $trackedInputs
    $privateSigningDirectory = New-PrivateSigningDirectory
    $privateSigningSids = @(Get-SigningAclSids)
    $protectedUnsignedReceiptPath = Join-Path (
        $privateSigningDirectory
    ) 'unsigned-receipt.psd1'
    $protectedSigningReceiptPath = Join-Path (
        $privateSigningDirectory
    ) 'receipt-to-sign.psd1'
    Copy-RegularFileToNewPath `
        -Source $receiptPath `
        -Destination $protectedUnsignedReceiptPath
    if ((Get-Sha256 -Path $protectedUnsignedReceiptPath) -cne $unsignedReceiptHash) {
        throw 'The protected unsigned receipt backup does not match the validated input.'
    }
    $protectedBackupSignature = Get-AuthenticodeSignature `
        -LiteralPath $protectedUnsignedReceiptPath
    if (
        $protectedBackupSignature.Status.ToString() -cne 'NotSigned' -or
        $null -ne $protectedBackupSignature.SignerCertificate -or
        $null -ne $protectedBackupSignature.TimeStamperCertificate
    ) {
        throw 'The protected receipt backup is not in a clean unsigned state.'
    }
    [void](Get-SingleLinkFileIdentity -Path $protectedUnsignedReceiptPath)
    Copy-RegularFileToNewPath `
        -Source $protectedUnsignedReceiptPath `
        -Destination $protectedSigningReceiptPath
    if ((Get-Sha256 -Path $protectedSigningReceiptPath) -cne $unsignedReceiptHash) {
        throw 'The private signing copy does not match the validated receipt.'
    }
    [void](Get-SingleLinkFileIdentity -Path $protectedSigningReceiptPath)
    Assert-PrivateSigningDirectory `
        -Path $privateSigningDirectory `
        -AllowedSids $privateSigningSids
    $signedTemporaryReceipt = Set-AuthenticodeSignature `
        -FilePath $protectedSigningReceiptPath `
        -Certificate $certificate `
        -HashAlgorithm SHA256 `
        -TimestampServer $TimestampUrl
    Assert-PrivateSigningDirectory `
        -Path $privateSigningDirectory `
        -AllowedSids $privateSigningSids
    [void](Get-SingleLinkFileIdentity -Path $protectedSigningReceiptPath)
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
    $temporaryReceiptData = Import-PowerShellDataFile `
        -LiteralPath $protectedSigningReceiptPath
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
    $signedReceiptHash = Get-Sha256 -Path $protectedSigningReceiptPath
    if ((Get-Sha256 -Path $receiptPath) -cne $unsignedReceiptHash) {
        throw 'The unsigned smoke receipt changed during signing.'
    }

    Copy-RegularFileToNewPath `
        -Source $protectedSigningReceiptPath `
        -Destination $temporaryReceiptPath
    if ((Get-Sha256 -Path $temporaryReceiptPath) -cne $signedReceiptHash) {
        throw 'The release-directory installation copy changed after private signing.'
    }
    $installationSignature = Get-AuthenticodeSignature -LiteralPath $temporaryReceiptPath
    if (
        $installationSignature.Status.ToString() -cne 'Valid' -or
        $null -eq $installationSignature.SignerCertificate -or
        $installationSignature.SignerCertificate.Thumbprint.ToUpperInvariant() -cne
            $canonicalSignerThumbprint -or
        $null -eq $installationSignature.TimeStamperCertificate
    ) {
        throw 'The release-directory installation copy has an invalid signature.'
    }
    $installationReceipt = Import-PowerShellDataFile -LiteralPath $temporaryReceiptPath
    Assert-ReceiptData `
        -Receipt $installationReceipt `
        -Manifest $manifest `
        -ReleaseRecords $records `
        -ManifestHash $manifestHash `
        -ValidationScriptHash $validationScriptHash `
        -SigningPolicyHash $policy.Sha256 `
        -ExpectedNonce $ExpectedValidationNonce `
        -ExpectedImageId $ExpectedCleanVmImageId `
        -Policy $policy

    Assert-PathHasNoReparsePoints -Path $resolvedReleaseDirectory -TrustedRoot $repositoryRoot
    Assert-PathHasNoReparsePoints -Path $receiptPath -TrustedRoot $repositoryRoot
    Assert-PathHasNoReparsePoints -Path $temporaryReceiptPath -TrustedRoot $repositoryRoot
    Assert-RegularFile -Path $receiptPath
    Assert-RegularFile -Path $temporaryReceiptPath
    if (
        (Get-Sha256 -Path $receiptPath) -cne $unsignedReceiptHash -or
        (Get-Sha256 -Path $temporaryReceiptPath) -cne $signedReceiptHash
    ) {
        throw 'A smoke receipt changed immediately before atomic installation.'
    }
    if (Test-Path -LiteralPath $backupReceiptPath) {
        throw 'The generated unsigned-receipt backup path was unexpectedly created before replacement.'
    }
    [IO.File]::Replace($temporaryReceiptPath, $receiptPath, $backupReceiptPath, $true)
    $receiptWasReplaced = $true
    Assert-RegularFile -Path $receiptPath
    Assert-PathHasNoReparsePoints -Path $receiptPath -TrustedRoot $repositoryRoot
    if ((Get-Sha256 -Path $receiptPath) -cne $signedReceiptHash) {
        throw 'The atomically installed smoke receipt hash is invalid.'
    }
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
    Assert-RegularFile -Path $backupReceiptPath
    Assert-PathHasNoReparsePoints -Path $backupReceiptPath -TrustedRoot $repositoryRoot
    if ((Get-Sha256 -Path $backupReceiptPath) -cne $unsignedReceiptHash) {
        throw 'The atomic replacement backup does not match the original unsigned receipt.'
    }
    Remove-Item -LiteralPath $backupReceiptPath -Force
    $signingCompleted = $true
}
catch {
    $signingFailure = $_
    $replacementEvidence = $receiptWasReplaced
    if (
        -not $replacementEvidence -and
        -not $signingCompleted -and
        -not [string]::IsNullOrWhiteSpace($protectedUnsignedReceiptPath) -and
        (Test-Path -LiteralPath $protectedUnsignedReceiptPath -PathType Leaf)
    ) {
        if (Test-Path -LiteralPath $backupReceiptPath -PathType Leaf) {
            $replacementEvidence = $true
        } elseif (Test-Path -LiteralPath $receiptPath -PathType Leaf) {
            try {
                $replacementEvidence = (
                    (Get-Sha256 -Path $receiptPath) -cne $unsignedReceiptHash
                )
            }
            catch {
                $replacementEvidence = $true
            }
        } else {
            $replacementEvidence = $true
        }
    }
    if ($replacementEvidence -and -not $signingCompleted) {
        try {
            Restore-UnsignedReceiptFromPrivateBackup `
                -PrivateBackupPath $protectedUnsignedReceiptPath `
                -ReceiptPath $receiptPath `
                -ReleaseDirectory $resolvedReleaseDirectory `
                -ExpectedSha256 $unsignedReceiptHash
            if (Test-Path -LiteralPath $backupReceiptPath) {
                Assert-RegularFile -Path $backupReceiptPath
                Assert-PathHasNoReparsePoints `
                    -Path $backupReceiptPath `
                    -TrustedRoot $repositoryRoot
                if ((Get-Sha256 -Path $backupReceiptPath) -cne $unsignedReceiptHash) {
                    throw 'The release-directory unsigned backup changed during recovery.'
                }
                Remove-Item -LiteralPath $backupReceiptPath -Force
            }
            $receiptWasReplaced = $false
        }
        catch {
            $preservePrivateSigningDirectory = $true
            Write-Warning (
                'The original unsigned receipt could not be restored automatically. ' +
                "Preserve the protected backup at '$protectedUnsignedReceiptPath'. " +
                "Recovery error: $($_.Exception.Message)"
            )
        }
    }
    throw $signingFailure
}
finally {
    if (Test-Path -LiteralPath $temporaryReceiptPath) {
        try {
            Assert-RegularFile -Path $temporaryReceiptPath
            Assert-PathHasNoReparsePoints `
                -Path $temporaryReceiptPath `
                -TrustedRoot $repositoryRoot
            Remove-Item -LiteralPath $temporaryReceiptPath -Force
        }
        catch {
            Write-Warning "Unable to remove installation temporary file safely: $($_.Exception.Message)"
        }
    }
    if (
        -not $preservePrivateSigningDirectory -and
        -not [string]::IsNullOrWhiteSpace($privateSigningDirectory) -and
        (Test-Path -LiteralPath $privateSigningDirectory)
    ) {
        try {
            Remove-PrivateSigningDirectory `
                -Path $privateSigningDirectory `
                -AllowedSids $privateSigningSids `
                -ExpectedFiles @(
                    $protectedUnsignedReceiptPath,
                    $protectedSigningReceiptPath
                )
        }
        catch {
            Write-Warning (
                "Unable to remove the private signing directory safely; inspect '$privateSigningDirectory'. " +
                $_.Exception.Message
            )
        }
    }
}

Write-Host "Signed and revalidated Windows smoke receipt: $receiptPath"
