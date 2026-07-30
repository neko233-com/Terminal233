[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ArchivePath,
    [Parameter(Mandatory)][string]$ExpectedVersion,
    [Parameter(Mandatory)][string]$ExpectedCommit,
    [string]$ExpectedProjectSignerThumbprint,
    [string]$ExpectedProjectSignerSubject,
    [string]$ExpectedProjectSignerSpkiSha256,
    [switch]$AllowUnsignedLauncher
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$officialNodeVersion = '24.18.1'
$officialNodeSha256 = 'ac51903c4c111815d52280b1fdcc8da067cbb37e2fe1a765097b85c3292c8582'
$resolvedArchive = (Resolve-Path -LiteralPath $ArchivePath).Path
$archiveItem = Get-Item -LiteralPath $resolvedArchive -Force
if (-not $archiveItem.PSIsContainer -and (($archiveItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -eq 0)) {
    # Expected path shape.
} else {
    throw 'The Web portable archive must be a regular non-reparse file.'
}
$normalizedSigner = if ([string]::IsNullOrWhiteSpace($ExpectedProjectSignerThumbprint)) {
    $null
} else {
    ($ExpectedProjectSignerThumbprint -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()
}
if ($AllowUnsignedLauncher -and $null -ne $normalizedSigner) {
    throw '-AllowUnsignedLauncher cannot be combined with an expected project signer.'
}
if ($AllowUnsignedLauncher -and (
    -not [string]::IsNullOrWhiteSpace($ExpectedProjectSignerSubject) -or
    -not [string]::IsNullOrWhiteSpace($ExpectedProjectSignerSpkiSha256)
)) {
    throw '-AllowUnsignedLauncher cannot be combined with signer identity metadata.'
}
if (-not $AllowUnsignedLauncher -and ($null -eq $normalizedSigner -or $normalizedSigner -notmatch '^[0-9A-F]{40}$')) {
    throw 'Signed Web portable verification requires a 40-character project signer thumbprint.'
}
if (-not $AllowUnsignedLauncher -and (
    [string]::IsNullOrWhiteSpace($ExpectedProjectSignerSubject) -or
    $ExpectedProjectSignerSpkiSha256 -cnotmatch '^[0-9a-f]{64}$'
)) {
    throw 'Signed Web portable verification requires the policy signer Subject and SPKI SHA-256.'
}
if ($ExpectedCommit -cnotmatch '^[0-9a-f]{40}$') {
    throw 'ExpectedCommit must be a full lowercase Git commit SHA.'
}
Import-Module -Name (Join-Path $PSScriptRoot 'release-signing-policy.psm1') -Force

function Test-ChildPath {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Parent
    )

    $normalizedParent = $Parent.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    return $Path.StartsWith("$normalizedParent$([IO.Path]::DirectorySeparatorChar)", [StringComparison]::OrdinalIgnoreCase)
}

function Get-SafeTreeEntries {
    param([Parameter(Mandatory)][string]$Root)

    $rootItem = Get-Item -LiteralPath $Root -Force
    if (($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Verification tree root is a reparse point: $Root"
    }
    $directories = [Collections.Generic.List[string]]::new()
    $files = [Collections.Generic.List[string]]::new()
    $pending = [Collections.Generic.Stack[string]]::new()
    $pending.Push($rootItem.FullName)
    while ($pending.Count -gt 0) {
        $directory = $pending.Pop()
        $directories.Add($directory)
        foreach ($entry in @(Get-ChildItem -LiteralPath $directory -Force)) {
            if (($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Verification tree contains a reparse point: $($entry.FullName)"
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
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Parent
    )

    if (-not (Test-Path -LiteralPath $Path)) { return }
    $fullPath = [IO.Path]::GetFullPath($Path)
    $fullParent = [IO.Path]::GetFullPath($Parent)
    if (-not (Test-ChildPath -Path $fullPath -Parent $fullParent)) {
        throw "Refusing to remove a verification tree outside its parent: $fullPath"
    }
    $tree = Get-SafeTreeEntries -Root $fullPath
    foreach ($file in $tree.Files) {
        Remove-Item -LiteralPath $file -Force
    }
    foreach ($directory in @($tree.Directories | Sort-Object Length -Descending)) {
        Remove-Item -LiteralPath $directory -Force
    }
}

function Expand-SafeArchive {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Destination
    )

    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $destinationRoot = [IO.Path]::GetFullPath($Destination)
    New-Item -ItemType Directory -Path $destinationRoot | Out-Null
    $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    $archive = [IO.Compression.ZipFile]::OpenRead($Path)
    try {
        $fileCount = 0
        [long]$totalBytes = 0
        foreach ($entry in $archive.Entries) {
            $entryName = $entry.FullName.Replace('\', '/')
            $isDirectory = $entryName.EndsWith('/')
            $relativeName = if ($isDirectory) { $entryName.TrimEnd('/') } else { $entryName }
            $segments = @($relativeName.Split('/'))
            if (
                [string]::IsNullOrWhiteSpace($relativeName) -or
                $entryName.Contains([char]0) -or
                $entryName.Contains(':') -or
                $entryName.StartsWith('/') -or
                @($segments | Where-Object { $_ -in @('', '.', '..') }).Count -gt 0
            ) {
                throw "Unsafe Web archive entry path: $entryName"
            }
            foreach ($segment in $segments) {
                $deviceBase = $segment.Split('.')[0].TrimEnd(' ', '.')
                if (
                    $segment.Length -gt 255 -or
                    $segment -match '[\x00-\x1F<>:"/\\|?*]' -or
                    $segment.EndsWith('.') -or
                    $segment.EndsWith(' ') -or
                    $segment.Normalize([Text.NormalizationForm]::FormC) -cne $segment -or
                    $deviceBase -match '(?i)^(?:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])$'
                ) {
                    throw "Web archive entry uses an unsafe Windows path segment: $entryName"
                }
            }
            $unixMode = ($entry.ExternalAttributes -shr 16) -band 0xF000
            $windowsAttributes = $entry.ExternalAttributes -band 0xFFFF
            if (
                $unixMode -eq 0xA000 -or
                ($windowsAttributes -band [int][IO.FileAttributes]::ReparsePoint) -ne 0
            ) {
                throw "Web archive links and reparse entries are forbidden: $entryName"
            }
            $target = [IO.Path]::GetFullPath((Join-Path $destinationRoot $relativeName))
            if (-not (Test-ChildPath -Path $target -Parent $destinationRoot)) {
                throw "Web archive entry escapes the extraction root: $entryName"
            }
            if (-not $seen.Add($target)) {
                throw "Duplicate Web archive entry: $entryName"
            }
            if ($isDirectory) {
                New-Item -ItemType Directory -Path $target -Force | Out-Null
                continue
            }
            $fileCount += 1
            $totalBytes += $entry.Length
            if ($fileCount -gt 500 -or $entry.Length -gt 128MB -or $totalBytes -gt 256MB) {
                throw 'Web archive exceeds its file count or uncompressed-size budget.'
            }
            $parent = Split-Path -Parent $target
            New-Item -ItemType Directory -Path $parent -Force | Out-Null
            $source = $entry.Open()
            $destinationStream = [IO.File]::Open($target, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
            try {
                $source.CopyTo($destinationStream)
            }
            finally {
                $destinationStream.Dispose()
                $source.Dispose()
            }
        }
    }
    finally {
        $archive.Dispose()
    }
    Get-SafeTreeEntries -Root $destinationRoot | Out-Null
}

if ($null -eq ('NekoSshRelease.KillOnCloseJob' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;

namespace NekoSshRelease {
    public static class KillOnCloseJob {
        private const UInt32 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE = 0x00002000;
        private const Int32 JobObjectExtendedLimitInformation = 9;

        [StructLayout(LayoutKind.Sequential)]
        private struct JOBOBJECT_BASIC_LIMIT_INFORMATION {
            public Int64 PerProcessUserTimeLimit;
            public Int64 PerJobUserTimeLimit;
            public UInt32 LimitFlags;
            public UIntPtr MinimumWorkingSetSize;
            public UIntPtr MaximumWorkingSetSize;
            public UInt32 ActiveProcessLimit;
            public UIntPtr Affinity;
            public UInt32 PriorityClass;
            public UInt32 SchedulingClass;
        }

        [StructLayout(LayoutKind.Sequential)]
        private struct IO_COUNTERS {
            public UInt64 ReadOperationCount;
            public UInt64 WriteOperationCount;
            public UInt64 OtherOperationCount;
            public UInt64 ReadTransferCount;
            public UInt64 WriteTransferCount;
            public UInt64 OtherTransferCount;
        }

        [StructLayout(LayoutKind.Sequential)]
        private struct JOBOBJECT_EXTENDED_LIMIT_INFORMATION {
            public JOBOBJECT_BASIC_LIMIT_INFORMATION BasicLimitInformation;
            public IO_COUNTERS IoInfo;
            public UIntPtr ProcessMemoryLimit;
            public UIntPtr JobMemoryLimit;
            public UIntPtr PeakProcessMemoryUsed;
            public UIntPtr PeakJobMemoryUsed;
        }

        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern IntPtr CreateJobObject(IntPtr jobAttributes, string name);

        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool SetInformationJobObject(
            IntPtr job,
            Int32 informationClass,
            ref JOBOBJECT_EXTENDED_LIMIT_INFORMATION information,
            UInt32 informationLength
        );

        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool AssignProcessToJobObject(IntPtr job, IntPtr process);

        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool CloseHandle(IntPtr handle);

        public static IntPtr Create(IntPtr processHandle) {
            IntPtr job = CreateJobObject(IntPtr.Zero, null);
            if (job == IntPtr.Zero) {
                throw new Win32Exception(Marshal.GetLastWin32Error(), "CreateJobObject failed.");
            }
            try {
                JOBOBJECT_EXTENDED_LIMIT_INFORMATION information =
                    new JOBOBJECT_EXTENDED_LIMIT_INFORMATION();
                information.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
                UInt32 size = (UInt32)Marshal.SizeOf(typeof(JOBOBJECT_EXTENDED_LIMIT_INFORMATION));
                if (!SetInformationJobObject(
                    job,
                    JobObjectExtendedLimitInformation,
                    ref information,
                    size
                )) {
                    throw new Win32Exception(
                        Marshal.GetLastWin32Error(),
                        "SetInformationJobObject failed."
                    );
                }
                if (!AssignProcessToJobObject(job, processHandle)) {
                    throw new Win32Exception(
                        Marshal.GetLastWin32Error(),
                        "AssignProcessToJobObject failed."
                    );
                }
                return job;
            } catch {
                CloseHandle(job);
                throw;
            }
        }

        public static void Close(IntPtr job) {
            if (job != IntPtr.Zero && !CloseHandle(job)) {
                throw new Win32Exception(Marshal.GetLastWin32Error(), "CloseHandle failed.");
            }
        }
    }
}
'@
}

function Invoke-CapturedProcess {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$ArgumentList,
        [int]$TimeoutSeconds = 30
    )

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $FilePath
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($variable in @(
        'NODE_OPTIONS',
        'NODE_PATH',
        'NODE_EXTRA_CA_CERTS',
        'NODE_COMPILE_CACHE',
        'NODE_COMPILE_CACHE_PORTABLE',
        'NODE_REDIRECT_WARNINGS',
        'NODE_REPL_EXTERNAL_MODULE',
        'NODE_V8_COVERAGE',
        'NEKOSSH_MCP_TOKEN',
        'NEKOSSH_DEBUG'
    )) {
        $startInfo.EnvironmentVariables.Remove($variable)
    }
    $startInfo.EnvironmentVariables['NODE_ENV'] = 'production'
    foreach ($argument in $ArgumentList) {
        if ($argument -notmatch '^--[a-z-]+$') {
            throw "Unsafe verification process argument: $argument"
        }
    }
    $startInfo.Arguments = $ArgumentList -join ' '
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    $jobHandle = [IntPtr]::Zero
    $processStarted = $false
    try {
        if (-not $process.Start()) {
            throw "Unable to start: $FilePath"
        }
        $processStarted = $true
        try {
            $jobHandle = [NekoSshRelease.KillOnCloseJob]::Create($process.Handle)
        }
        catch {
            if (-not $process.HasExited) {
                $process.Kill()
                $process.WaitForExit()
            }
            throw
        }
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
            [NekoSshRelease.KillOnCloseJob]::Close($jobHandle)
            $jobHandle = [IntPtr]::Zero
            $process.WaitForExit()
            throw "Process timed out after $TimeoutSeconds seconds: $FilePath"
        }
        $stdout.Wait()
        $stderr.Wait()
        if ($process.ExitCode -ne 0) {
            throw "Process failed with exit code $($process.ExitCode): $FilePath`n$($stdout.Result)`n$($stderr.Result)"
        }
        return [pscustomobject]@{ Stdout = $stdout.Result; Stderr = $stderr.Result }
    }
    finally {
        if ($jobHandle -ne [IntPtr]::Zero) {
            [NekoSshRelease.KillOnCloseJob]::Close($jobHandle)
        } elseif ($processStarted -and -not $process.HasExited) {
            $process.Kill()
            $process.WaitForExit()
        }
        $process.Dispose()
    }
}

$archiveParent = Split-Path -Parent $resolvedArchive
$verificationRoot = [IO.Path]::GetFullPath(
    (Join-Path $archiveParent ".web-portable-verification-$PID-$([guid]::NewGuid().ToString('N'))")
)
if (-not (Test-ChildPath -Path $verificationRoot -Parent $archiveParent)) {
    throw 'Refusing to extract the Web archive outside its parent.'
}

try {
    Expand-SafeArchive -Path $resolvedArchive -Destination $verificationRoot
    $launcherPath = Join-Path $verificationRoot 'NekoSSH-Web.exe'
    $nodePath = Join-Path $verificationRoot 'runtime/node.exe'
    foreach ($required in @(
        $launcherPath,
        $nodePath,
        (Join-Path $verificationRoot 'PAYLOAD_SHA256SUMS.txt'),
        (Join-Path $verificationRoot 'VERSION.json'),
        (Join-Path $verificationRoot 'app/dist/index.html'),
        (Join-Path $verificationRoot 'app/server/ssh2js-server.mjs')
    )) {
        if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
            throw "Web portable archive is missing a required file: $required"
        }
    }

    $versionRecord = Get-Content -LiteralPath (Join-Path $verificationRoot 'VERSION.json') -Raw | ConvertFrom-Json
    $versionFields = @($versionRecord.PSObject.Properties.Name | Sort-Object)
    $expectedVersionFields = @(
        'commit',
        'nodeVersion',
        'persistence',
        'platform',
        'product',
        'schemaVersion',
        'sshRuntime',
        'version'
    ) | Sort-Object
    if (
        ($versionFields -join "`n") -ne ($expectedVersionFields -join "`n") -or
        -not ($versionRecord.schemaVersion -is [int] -or $versionRecord.schemaVersion -is [long]) -or
        [long]$versionRecord.schemaVersion -ne 1 -or
        [string]$versionRecord.product -cne 'NekoSSH Web' -or
        [string]$versionRecord.version -cne $ExpectedVersion -or
        [string]$versionRecord.commit -cne $ExpectedCommit -or
        [string]$versionRecord.platform -cne 'windows-x64' -or
        [string]$versionRecord.nodeVersion -cne $officialNodeVersion -or
        [string]$versionRecord.sshRuntime -cne 'node-ssh2js' -or
        [string]$versionRecord.persistence -cne 'memory-only'
    ) {
        throw 'Web portable VERSION.json does not match the exact release runtime identity.'
    }

    $launcherSignature = Get-AuthenticodeSignature -LiteralPath $launcherPath
    if ($launcherSignature.Status.ToString() -notin @('Valid', 'NotSigned')) {
        throw "Unsafe Web launcher signature state: $($launcherSignature.Status)"
    }
    if ($AllowUnsignedLauncher) {
        if ($launcherSignature.Status.ToString() -ne 'NotSigned') {
            throw 'Unsigned local validation must contain an unsigned project launcher.'
        }
    } else {
        if (
            $launcherSignature.Status.ToString() -ne 'Valid' -or
            $null -eq $launcherSignature.SignerCertificate -or
            $launcherSignature.SignerCertificate.Thumbprint.ToUpperInvariant() -ne $normalizedSigner -or
            $launcherSignature.SignerCertificate.Subject -cne $ExpectedProjectSignerSubject -or
            (Get-CertificateSpkiSha256 -Certificate $launcherSignature.SignerCertificate) -cne
                $ExpectedProjectSignerSpkiSha256 -or
            $null -eq $launcherSignature.TimeStamperCertificate
        ) {
            throw 'The Web launcher does not have the expected valid timestamped project signature.'
        }
    }

    $nodeHash = (Get-FileHash -LiteralPath $nodePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($nodeHash -ne $officialNodeSha256) {
        throw 'Bundled node.exe does not match the official pinned Node.js SHA-256.'
    }
    if ((Get-Item -LiteralPath $nodePath).VersionInfo.ProductVersion -ne $officialNodeVersion) {
        throw 'Bundled node.exe ProductVersion does not match the pinned Node.js version.'
    }
    $nodeSignature = Get-AuthenticodeSignature -LiteralPath $nodePath
    if (
        $nodeSignature.Status.ToString() -ne 'Valid' -or
        $null -eq $nodeSignature.SignerCertificate -or
        $nodeSignature.SignerCertificate.Subject -notmatch '(?:^|, )O=OpenJS Foundation(?:,|$)' -or
        $null -eq $nodeSignature.TimeStamperCertificate
    ) {
        throw 'Bundled node.exe does not have the expected valid timestamped OpenJS signature.'
    }
    $portableExecutables = @(Get-ChildItem -LiteralPath $verificationRoot -Recurse -File |
        Where-Object { $_.Extension -in @('.exe', '.dll') })
    $expectedExecutablePaths = @($launcherPath, $nodePath) | Sort-Object
    if ((@($portableExecutables.FullName | Sort-Object) -join "`n") -ne ($expectedExecutablePaths -join "`n")) {
        throw 'Web portable archive contains an unexpected EXE or DLL.'
    }

    $selfTestStatus = 'skipped-unsigned'
    if (-not $AllowUnsignedLauncher) {
        $versionOutput = Invoke-CapturedProcess -FilePath $launcherPath -ArgumentList @('--version') -TimeoutSeconds 5
        if ($versionOutput.Stdout.Trim() -ne "NekoSSH Web $ExpectedVersion") {
            throw 'Web launcher version output does not match the expected release version.'
        }
        $selfTest = Invoke-CapturedProcess -FilePath $launcherPath -ArgumentList @('--self-test') -TimeoutSeconds 30
        $selfTestLine = @(
            $selfTest.Stdout -split "`r?`n" |
                Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
        ) | Select-Object -Last 1
        try {
            $selfTestReceipt = $selfTestLine | ConvertFrom-Json
        } catch {
            throw "Web launcher self-test did not return valid JSON.`n$($selfTest.Stdout)"
        }
        if (
            $selfTestReceipt.ok -isnot [bool] -or
            $selfTestReceipt.ok -ne $true -or
            [string]$selfTestReceipt.version -cne $ExpectedVersion -or
            [string]$selfTestReceipt.runtime -cne 'node-ssh2js' -or
            [string]$selfTestReceipt.persistence -cne 'memory_only'
        ) {
            throw "Web launcher self-test did not return its success receipt.`n$($selfTest.Stdout)"
        }
        $selfTestStatus = 'passed'
    }

    [ordered]@{
        archiveSha256 = (Get-FileHash -LiteralPath $resolvedArchive -Algorithm SHA256).Hash.ToLowerInvariant()
        version = $ExpectedVersion
        launcher = [ordered]@{
            status = $launcherSignature.Status.ToString()
            signerThumbprint = if ($null -eq $launcherSignature.SignerCertificate) {
                $null
            } else {
                $launcherSignature.SignerCertificate.Thumbprint.ToUpperInvariant()
            }
            signerSubject = if ($null -eq $launcherSignature.SignerCertificate) {
                $null
            } else {
                $launcherSignature.SignerCertificate.Subject
            }
            signerSpkiSha256 = if ($null -eq $launcherSignature.SignerCertificate) {
                $null
            } else {
                Get-CertificateSpkiSha256 -Certificate $launcherSignature.SignerCertificate
            }
            timestamped = $null -ne $launcherSignature.TimeStamperCertificate
        }
        node = [ordered]@{
            version = $officialNodeVersion
            sha256 = $nodeHash
            status = $nodeSignature.Status.ToString()
            signerSubject = $nodeSignature.SignerCertificate.Subject
            timestamped = $true
        }
        selfTest = $selfTestStatus
    } | ConvertTo-Json -Depth 6 -Compress
}
finally {
    Remove-SafeTree -Path $verificationRoot -Parent $archiveParent
}
