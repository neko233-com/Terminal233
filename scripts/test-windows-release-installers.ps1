[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ReleaseDirectory,
    [Parameter(Mandatory)][string]$ExpectedSignerThumbprint,
    [Parameter(Mandatory)][switch]$ConfirmCleanWindowsVm,
    [Parameter(Mandatory)][string]$CleanVmImageId,
    [Parameter(Mandatory)][string]$ValidationNonce,
    [switch]$FirstRelease,
    [string]$PreviousNsisPath,
    [string]$PreviousMsiPath,
    [string]$PreviousSignerThumbprint
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$releaseRoot = [IO.Path]::GetFullPath((Join-Path $repositoryRoot 'output/release'))
$resolvedReleaseDirectory = (Resolve-Path -LiteralPath $ReleaseDirectory).Path
$policyPath = Join-Path $repositoryRoot 'release-signing-policy.json'
$policyModulePath = Join-Path $PSScriptRoot 'release-signing-policy.psm1'
$sourceIntegrityModulePath = Join-Path $PSScriptRoot 'git-source-integrity.psm1'
$validationScriptPath = $PSCommandPath
$expectedRepository = 'neko233-com/nekossh'
$expectedUpgradeCode = '{44F1588E-BCFD-5973-A955-237DAF92726D}'
$normalizedSigner = ($ExpectedSignerThumbprint -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()
$normalizedPreviousSigner = if ([string]::IsNullOrWhiteSpace($PreviousSignerThumbprint)) {
    $normalizedSigner
} else {
    ($PreviousSignerThumbprint -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()
}

if (-not $ConfirmCleanWindowsVm) {
    throw 'Use -ConfirmCleanWindowsVm only on a disposable, clean Windows validation VM.'
}
if ($CleanVmImageId -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._:/@+-]{7,127}$') {
    throw 'CleanVmImageId must be an immutable image/snapshot identifier using only safe characters.'
}
if ($ValidationNonce -cnotmatch '^[0-9a-f]{64}$') {
    throw 'ValidationNonce must be a pre-issued 256-bit lowercase hexadecimal challenge.'
}
if ($normalizedSigner -cnotmatch '^[0-9A-F]{40}$') {
    throw 'ExpectedSignerThumbprint must resolve to a 40-character certificate thumbprint.'
}
$hasPreviousNsis = -not [string]::IsNullOrWhiteSpace($PreviousNsisPath)
$hasPreviousMsi = -not [string]::IsNullOrWhiteSpace($PreviousMsiPath)
if ($FirstRelease) {
    if ($hasPreviousNsis -or $hasPreviousMsi -or -not [string]::IsNullOrWhiteSpace($PreviousSignerThumbprint)) {
        throw '-FirstRelease cannot be combined with previous installer parameters.'
    }
} elseif (-not $hasPreviousNsis -or -not $hasPreviousMsi) {
    throw 'A non-first release requires both -PreviousNsisPath and -PreviousMsiPath.'
}
if (-not $FirstRelease -and $normalizedPreviousSigner -cnotmatch '^[0-9A-F]{40}$') {
    throw 'PreviousSignerThumbprint must resolve to a 40-character certificate thumbprint.'
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
        throw "Required smoke-test file is missing: $Path"
    }
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Smoke-test files must not be reparse points: $Path"
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

function ConvertTo-WindowsCommandLineArgument {
    param([AllowEmptyString()][string]$Value)

    if ($Value.Length -eq 0) { return '""' }
    if ($Value -notmatch '[\s"]') { return $Value }
    $builder = [Text.StringBuilder]::new()
    [void]$builder.Append('"')
    $backslashes = 0
    foreach ($character in $Value.ToCharArray()) {
        if ($character -eq '\') {
            $backslashes += 1
        } elseif ($character -eq '"') {
            [void]$builder.Append(('\' * ($backslashes * 2 + 1)))
            [void]$builder.Append('"')
            $backslashes = 0
        } else {
            if ($backslashes -gt 0) {
                [void]$builder.Append(('\' * $backslashes))
                $backslashes = 0
            }
            [void]$builder.Append($character)
        }
    }
    if ($backslashes -gt 0) {
        [void]$builder.Append(('\' * ($backslashes * 2)))
    }
    [void]$builder.Append('"')
    return $builder.ToString()
}

function Invoke-ValidationProcess {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$ArgumentList,
        [int[]]$AllowedExitCodes = @(0),
        [int]$TimeoutSeconds = 180,
        [switch]$CaptureOutput,
        [switch]$AllowAnyExitCode
    )

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $FilePath
    $startInfo.Arguments = (
        $ArgumentList |
            ForEach-Object { ConvertTo-WindowsCommandLineArgument -Value $_ }
    ) -join ' '
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = [bool]$CaptureOutput
    $startInfo.RedirectStandardError = [bool]$CaptureOutput
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    try {
        if (-not $process.Start()) {
            throw "Unable to start validation process: $FilePath"
        }
        $stdout = if ($CaptureOutput) { $process.StandardOutput.ReadToEndAsync() } else { $null }
        $stderr = if ($CaptureOutput) { $process.StandardError.ReadToEndAsync() } else { $null }
        if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
            $taskKill = Join-Path $env:SystemRoot 'System32/taskkill.exe'
            & $taskKill /PID ([string]$process.Id) /T /F *> $null
            $process.WaitForExit()
            throw "Validation process timed out after $TimeoutSeconds seconds: $FilePath"
        }
        if ($CaptureOutput) {
            $stdout.Wait()
            $stderr.Wait()
        }
        if (-not $AllowAnyExitCode -and $process.ExitCode -notin $AllowedExitCodes) {
            $details = if ($CaptureOutput) { "`n$($stdout.Result)`n$($stderr.Result)" } else { '' }
            throw "Validation process failed with exit code $($process.ExitCode): $FilePath$details"
        }
        return [pscustomobject]@{
            ExitCode = $process.ExitCode
            Stdout = if ($CaptureOutput) { $stdout.Result } else { '' }
            Stderr = if ($CaptureOutput) { $stderr.Result } else { '' }
        }
    }
    finally {
        $process.Dispose()
    }
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

function Get-MsiIdentity {
    param([Parameter(Mandatory)][string]$Path)

    $productCode = Get-MsiProperty -Path $Path -Property 'ProductCode'
    $parsedProductCode = [guid]::Empty
    if (-not [guid]::TryParseExact($productCode, 'B', [ref]$parsedProductCode)) {
        throw "MSI ProductCode is not a canonical braced GUID: $Path"
    }
    return [ordered]@{
        productCode = $parsedProductCode.ToString('B').ToUpperInvariant()
        productName = Get-MsiProperty -Path $Path -Property 'ProductName'
        manufacturer = Get-MsiProperty -Path $Path -Property 'Manufacturer'
        productLanguage = Get-MsiProperty -Path $Path -Property 'ProductLanguage'
        productVersion = Get-MsiProperty -Path $Path -Property 'ProductVersion'
        upgradeCode = Get-MsiUpgradeCode -Path $Path
    }
}

function Assert-PinnedMsiIdentity {
    param(
        [Parameter(Mandatory)]$Identity,
        [Parameter(Mandatory)][string]$ExpectedVersion
    )

    if (
        [string]$Identity.productVersion -cne $ExpectedVersion -or
        [string]$Identity.productName -cne 'NekoSSH' -or
        [string]$Identity.manufacturer -cne 'NekoSSH contributors' -or
        [string]$Identity.productLanguage -cne '2052' -or
        [string]$Identity.productCode -cnotmatch '^\{[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}\}$' -or
        [string]$Identity.upgradeCode -cne $expectedUpgradeCode
    ) {
        throw 'MSI identity does not match the pinned NekoSSH Windows product policy.'
    }
}

function Assert-ExpectedSignature {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)]$PolicySigner
    )

    Assert-RegularFile -Path $Path
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    if (
        $signature.Status.ToString() -ne 'Valid' -or
        $null -eq $signature.SignerCertificate -or
        $null -eq $signature.TimeStamperCertificate
    ) {
        throw "Installed or release PE lacks a valid timestamped signature: $Path"
    }
    [void](Assert-ReleaseSigningCertificate `
        -Certificate $signature.SignerCertificate `
        -Signer $PolicySigner)
}

function Assert-InstalledPayload {
    param(
        [Parameter(Mandatory)][string]$InstallRoot,
        [Parameter(Mandatory)][string]$ExpectedVersion,
        [Parameter(Mandatory)]$PolicySigner,
        [switch]$RequireUninstaller
    )

    if (-not (Test-Path -LiteralPath $InstallRoot -PathType Container)) {
        throw "Installer did not create its target directory: $InstallRoot"
    }
    $mainExecutable = Join-Path $InstallRoot 'nekossh.exe'
    Assert-ExpectedSignature -Path $mainExecutable -PolicySigner $PolicySigner
    if ((Get-Item -LiteralPath $mainExecutable).VersionInfo.ProductVersion -cne $ExpectedVersion) {
        throw "Installed NekoSSH ProductVersion is not $ExpectedVersion`: $mainExecutable"
    }
    $portableExecutables = @(
        Get-ChildItem -LiteralPath $InstallRoot -Recurse -File |
            Where-Object { $_.Extension -in @('.exe', '.dll') }
    )
    foreach ($file in $portableExecutables) {
        Assert-ExpectedSignature -Path $file.FullName -PolicySigner $PolicySigner
    }
    if ($RequireUninstaller) {
        Assert-ExpectedSignature `
            -Path (Join-Path $InstallRoot 'uninstall.exe') `
            -PolicySigner $PolicySigner
    }
    $selfTest = Invoke-ValidationProcess `
        -FilePath $mainExecutable `
        -ArgumentList @('--self-test') `
        -TimeoutSeconds 30 `
        -CaptureOutput
    $selfTestLine = @(
        $selfTest.Stdout -split "`r?`n" |
            Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    ) | Select-Object -Last 1
    try {
        $receipt = $selfTestLine | ConvertFrom-Json
    } catch {
        throw "Installed native self-test did not return valid JSON.`n$($selfTest.Stdout)"
    }
    if (
        $receipt.ok -isnot [bool] -or
        $receipt.ok -ne $true -or
        [string]$receipt.version -cne $ExpectedVersion -or
        [string]$receipt.runtime -cne 'pure-rust-puressh' -or
        [string]$receipt.persistence -cne 'memory_only'
    ) {
        throw "Installed Tauri native runtime self-test failed.`n$($selfTest.Stdout)"
    }
}

function Get-RegistryPropertyValue {
    param(
        [Parameter(Mandatory)]$Object,
        [Parameter(Mandatory)][string]$Name
    )

    $property = $Object.PSObject.Properties[$Name]
    return if ($null -eq $property) { $null } else { $property.Value }
}

function Get-NekoSshUninstallEntries {
    $roots = @(
        'Registry::HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Uninstall',
        'Registry::HKEY_LOCAL_MACHINE\Software\Microsoft\Windows\CurrentVersion\Uninstall',
        'Registry::HKEY_LOCAL_MACHINE\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall'
    )
    $entries = [Collections.Generic.List[object]]::new()
    foreach ($root in $roots) {
        if (-not (Test-Path -LiteralPath $root)) { continue }
        foreach ($entry in @(Get-ChildItem -LiteralPath $root -ErrorAction SilentlyContinue)) {
            $values = Get-ItemProperty -LiteralPath $entry.PSPath -ErrorAction SilentlyContinue
            if ($null -eq $values) { continue }
            $displayName = [string](Get-RegistryPropertyValue -Object $values -Name 'DisplayName')
            if ($displayName -cne 'NekoSSH') { continue }
            $entries.Add([pscustomobject]@{
                Path = $entry.PSPath
                DisplayVersion = [string](Get-RegistryPropertyValue -Object $values -Name 'DisplayVersion')
                InstallLocation = [string](Get-RegistryPropertyValue -Object $values -Name 'InstallLocation')
                UninstallString = [string](Get-RegistryPropertyValue -Object $values -Name 'UninstallString')
                QuietUninstallString = [string](Get-RegistryPropertyValue -Object $values -Name 'QuietUninstallString')
            })
        }
    }
    return @($entries)
}

function Get-NekoSshShortcutPaths {
    $folders = @(
        [Environment]::GetFolderPath('Desktop'),
        [Environment]::GetFolderPath('CommonDesktopDirectory'),
        [Environment]::GetFolderPath('Programs'),
        [Environment]::GetFolderPath('CommonPrograms')
    ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -Unique
    $shortcuts = [Collections.Generic.List[string]]::new()
    foreach ($folder in $folders) {
        if (-not (Test-Path -LiteralPath $folder -PathType Container)) { continue }
        foreach ($item in @(
            Get-ChildItem -LiteralPath $folder -Filter '*NekoSSH*.lnk' -File -Recurse -ErrorAction SilentlyContinue
        )) {
            $shortcuts.Add($item.FullName)
        }
    }
    return @($shortcuts)
}

function Assert-RegisteredProduct {
    param(
        [Parameter(Mandatory)][string]$InstallRoot,
        [Parameter(Mandatory)][string]$ExpectedVersion,
        [Parameter(Mandatory)][ValidateSet('nsis', 'msi')][string]$InstallerKind,
        [string]$ExpectedProductCode
    )

    $entries = @(Get-NekoSshUninstallEntries)
    if ($entries.Count -ne 1) {
        throw "Expected exactly one NekoSSH uninstall registration; found $($entries.Count)."
    }
    try {
        if ([version]$entries[0].DisplayVersion -ne [version]$ExpectedVersion) {
            throw 'NekoSSH uninstall registration has the wrong version.'
        }
    } catch {
        throw "NekoSSH uninstall registration version is invalid: $($entries[0].DisplayVersion)"
    }
    if ([string]::IsNullOrWhiteSpace($entries[0].InstallLocation)) {
        throw 'NekoSSH uninstall registration does not include InstallLocation.'
    }
    $registeredLocation = [IO.Path]::GetFullPath($entries[0].InstallLocation).TrimEnd('\', '/')
    if ($registeredLocation -cne ([IO.Path]::GetFullPath($InstallRoot).TrimEnd('\', '/'))) {
        throw 'NekoSSH uninstall registration points to the wrong install location.'
    }
    $uninstallCommand = $entries[0].UninstallString.Trim()
    if ([string]::IsNullOrWhiteSpace($uninstallCommand)) {
        throw 'NekoSSH uninstall registration does not include an uninstall command.'
    }
    if ($InstallerKind -ceq 'nsis') {
        if (-not [string]::IsNullOrWhiteSpace($ExpectedProductCode)) {
            throw 'NSIS registration validation must not receive an MSI ProductCode.'
        }
        $expectedUninstaller = [IO.Path]::GetFullPath(
            (Join-Path $InstallRoot 'uninstall.exe')
        )
        $allowedCommands = @(
            $expectedUninstaller,
            "`"$expectedUninstaller`""
        )
        if (@($allowedCommands | Where-Object {
            [string]::Equals($_, $uninstallCommand, [StringComparison]::OrdinalIgnoreCase)
        }).Count -ne 1) {
            throw 'NSIS uninstall registration does not point to the installed signed uninstaller.'
        }
    } else {
        $parsedProductCode = [guid]::Empty
        if (
            [string]::IsNullOrWhiteSpace($ExpectedProductCode) -or
            -not [guid]::TryParseExact($ExpectedProductCode, 'B', [ref]$parsedProductCode)
        ) {
            throw 'MSI registration validation requires a canonical ProductCode.'
        }
        $canonicalProductCode = $parsedProductCode.ToString('B').ToUpperInvariant()
        $msiUninstallPattern = (
            '(?i)^(?:"?(?:[A-Z]:\\[^"]*\\)?MsiExec\.exe"?)\s+/(?:I|X)\s*' +
            [regex]::Escape($canonicalProductCode) +
            '(?:\s+.*)?$'
        )
        if ($uninstallCommand -notmatch $msiUninstallPattern) {
            throw 'MSI uninstall registration does not target the installed ProductCode through MsiExec.exe.'
        }
    }
}

function Assert-NoNekoSshResidue {
    param(
        [Parameter(Mandatory)][string]$InstallRoot,
        [int]$TimeoutSeconds = 30
    )

    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    do {
        $installRootExists = Test-Path -LiteralPath $InstallRoot
        $files = if ($installRootExists) {
            @(Get-ChildItem -LiteralPath $InstallRoot -Recurse -Force -ErrorAction SilentlyContinue)
        } else {
            @()
        }
        $uninstallEntries = @(Get-NekoSshUninstallEntries)
        $productRegistry = @(
            'Registry::HKEY_CURRENT_USER\Software\NekoSSH contributors\NekoSSH',
            'Registry::HKEY_LOCAL_MACHINE\Software\NekoSSH contributors\NekoSSH'
        ) | Where-Object { Test-Path -LiteralPath $_ }
        $shortcuts = @(Get-NekoSshShortcutPaths)
        $processes = @(Get-Process -Name 'nekossh' -ErrorAction SilentlyContinue)
        if (
            -not $installRootExists -and
            $files.Count -eq 0 -and
            $uninstallEntries.Count -eq 0 -and
            $productRegistry.Count -eq 0 -and
            $shortcuts.Count -eq 0 -and
            $processes.Count -eq 0
        ) {
            return
        }
        Start-Sleep -Milliseconds 250
    } while ([DateTimeOffset]::UtcNow -lt $deadline)

    throw "NekoSSH uninstall left files, registry entries, shortcuts, or a process behind: $InstallRoot"
}

if (-not (Test-ChildPath -Path $resolvedReleaseDirectory -Parent $releaseRoot)) {
    throw 'The smoke-tested release directory must stay below output/release/.'
}
$principal = [Security.Principal.WindowsPrincipal]::new(
    [Security.Principal.WindowsIdentity]::GetCurrent()
)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Windows installer acceptance must run from an elevated PowerShell session in the clean VM.'
}
if ($env:PROCESSOR_ARCHITECTURE -ne 'AMD64') {
    throw 'Windows x64 installer acceptance requires an x64 process.'
}
if (
    @(Get-Process -Name 'nekossh' -ErrorAction SilentlyContinue).Count -ne 0 -or
    @(Get-NekoSshUninstallEntries).Count -ne 0 -or
    @(Get-NekoSshShortcutPaths).Count -ne 0
) {
    throw 'The disposable Windows VM is not clean: NekoSSH process, registration, or shortcut already exists.'
}

$manifestPath = Join-Path $resolvedReleaseDirectory 'release-manifest.json'
Assert-RegularFile -Path $manifestPath
Import-Module -Name $policyModulePath -Force
Import-Module -Name $sourceIntegrityModulePath -Force -ErrorAction Stop
$manifest = Import-NekoSshReleaseManifest -Path $manifestPath
if (
    -not ($manifest.schemaVersion -is [int] -or $manifest.schemaVersion -is [long]) -or
    [long]$manifest.schemaVersion -ne 4 -or
    [string]$manifest.repository -cne $expectedRepository -or
    $manifest.prerelease -isnot [bool] -or
    $manifest.prerelease -ne $false -or
    $manifest.publishable -isnot [bool] -or
    $manifest.publishable -ne $true -or
    [string]$manifest.version -cnotmatch '^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$' -or
    [string]$manifest.commit -cnotmatch '^[0-9a-f]{40}$'
) {
    throw 'A production Windows smoke receipt requires a publishable stable schema-v4 manifest.'
}
$version = [string]$manifest.version
$commit = [string]$manifest.commit
if ([string]$manifest.shortCommit -cne $commit.Substring(0, 12)) {
    throw 'Release manifest shortCommit does not match its commit.'
}

Invoke-Checked -FilePath 'git' -ArgumentList @('fetch', '--prune', 'origin')
$sourceIdentity = Assert-NekoSshGitSourceIntegrity -RepositoryRoot $repositoryRoot
if ([string]$sourceIdentity.Commit -cne $commit) {
    throw 'Smoke manifest, HEAD and origin/main must identify the same commit.'
}
if (
    [string]$manifest.source.branch -cne [string]$sourceIdentity.Branch -or
    [string]$manifest.source.commit -cne [string]$sourceIdentity.Commit -or
    [string]$manifest.source.tree -cne [string]$sourceIdentity.Tree -or
    [string]$manifest.source.snapshotSha256 -cne [string]$sourceIdentity.SnapshotSha256 -or
    [string]$manifest.source.digestFormat -cne [string]$sourceIdentity.DigestFormat -or
    [int]$manifest.source.trackedFileCount -ne [int]$sourceIdentity.TrackedFileCount
) {
    throw 'Smoke manifest source identity does not match the complete checked-out Git source snapshot.'
}
$originUrl = Get-CheckedOutput -FilePath 'git' -ArgumentList @('remote', 'get-url', 'origin')
if (
    $originUrl -notmatch '(?i)^(https://github\.com/neko233-com/nekossh(?:\.git)?/?|git@github\.com:neko233-com/nekossh(?:\.git)?)$'
) {
    throw "Git origin is not $expectedRepository`: $originUrl"
}
Invoke-Checked -FilePath 'gh' -ArgumentList @('auth', 'status', '--hostname', 'github.com')
$actionsEnabled = Get-CheckedOutput -FilePath 'gh' -ArgumentList @(
    'api',
    "repos/$expectedRepository/actions/permissions",
    '--jq',
    '.enabled'
)
if ($actionsEnabled -cne 'false') {
    throw 'GitHub Actions must remain disabled during clean Windows acceptance.'
}

$signingPolicy = Import-ReleaseSigningPolicy -Path $policyPath
$policySigner = Get-ReleaseSigningPolicySigner `
    -Policy $signingPolicy `
    -Version $version `
    -Thumbprint $normalizedSigner
if (
    [string]$manifest.signing.mode -cne 'authenticode' -or
    [string]$manifest.signing.expectedSignerThumbprint -cne $policySigner.Thumbprint -or
    [string]$manifest.signing.expectedSignerSubject -cne $policySigner.Subject -or
    [string]$manifest.signing.expectedSignerSpkiSha256 -cne $policySigner.SpkiSha256 -or
    [string]$manifest.signing.policySha256 -cne $signingPolicy.Sha256 -or
    $manifest.signing.timestampRequired -isnot [bool] -or
    $manifest.signing.timestampRequired -ne $true
) {
    throw 'Release manifest signing identity does not match the checked-out signing policy.'
}
$previousPolicySigner = $null

$nsisName = "NekoSSH_${version}_x64-setup.exe"
$msiName = "NekoSSH_${version}_x64_zh-CN.msi"
$webName = "NekoSSH_Web_${version}_windows_x64.zip"
$nsisPath = Join-Path $resolvedReleaseDirectory $nsisName
$msiPath = Join-Path $resolvedReleaseDirectory $msiName
$webPath = Join-Path $resolvedReleaseDirectory $webName
foreach ($path in @($nsisPath, $msiPath)) {
    Assert-ExpectedSignature -Path $path -PolicySigner $policySigner
}
$currentMsiIdentity = Get-MsiIdentity -Path $msiPath
Assert-PinnedMsiIdentity -Identity $currentMsiIdentity -ExpectedVersion $version
foreach ($field in @(
    'productCode',
    'productName',
    'manufacturer',
    'productLanguage',
    'productVersion',
    'upgradeCode'
)) {
    if ([string]$currentMsiIdentity[$field] -cne [string]$manifest.windowsInstaller.$field) {
        throw "Current MSI $field does not match the release manifest."
    }
}

$files = [ordered]@{}
foreach ($record in @($manifest.assets) + @($manifest.checksumFile) + @($manifest.releaseNotesFile)) {
    $path = [IO.Path]::GetFullPath(
        (Join-Path $resolvedReleaseDirectory ([string]$record.name))
    )
    if (-not (Test-ChildPath -Path $path -Parent $resolvedReleaseDirectory)) {
        throw "Release file escaped its directory: $($record.name)"
    }
    Assert-RegularFile -Path $path
    $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    if (
        $hash -cne [string]$record.sha256 -or
        (Get-Item -LiteralPath $path).Length -ne [long]$record.bytes
    ) {
        throw "Release file changed before Windows acceptance: $($record.name)"
    }
    $files[[string]$record.name] = $hash
}
$files['release-manifest.json'] = (
    Get-FileHash -LiteralPath $manifestPath -Algorithm SHA256
).Hash.ToLowerInvariant()

$webVerification = (
    & (Join-Path $PSScriptRoot 'verify-windows-web-portable.ps1') `
        -ArchivePath $webPath `
        -ExpectedVersion $version `
        -ExpectedCommit $commit `
        -ExpectedProjectSignerThumbprint $policySigner.Thumbprint `
        -ExpectedProjectSignerSubject $policySigner.Subject `
        -ExpectedProjectSignerSpkiSha256 $policySigner.SpkiSha256 |
        Out-String
).Trim() | ConvertFrom-Json
if ($webVerification.selfTest -cne 'passed') {
    throw 'The independent Web runtime failed clean-machine acceptance.'
}

$upgradeMode = if ($FirstRelease) { 'first-release' } else { 'previous-installers' }
$upgradeFromVersion = $null
$upgradeFromNsisSha256 = $null
$upgradeFromMsiSha256 = $null
$resolvedPreviousNsis = $null
$resolvedPreviousMsi = $null
$previousMsiIdentity = $null
if (-not $FirstRelease) {
    $resolvedPreviousNsis = (Resolve-Path -LiteralPath $PreviousNsisPath).Path
    $resolvedPreviousMsi = (Resolve-Path -LiteralPath $PreviousMsiPath).Path
    $previousNsisVersion = (Get-Item -LiteralPath $resolvedPreviousNsis).VersionInfo.ProductVersion
    $previousMsiIdentity = Get-MsiIdentity -Path $resolvedPreviousMsi
    if (
        [string]$previousMsiIdentity.productVersion -cne [string]$previousNsisVersion -or
        [version]$previousNsisVersion -ge [version]$version
    ) {
        throw 'Previous NSIS/MSI versions must match and be lower than the current release.'
    }
    $upgradeFromVersion = [string]$previousNsisVersion
    $previousPolicySigner = Get-ReleaseSigningPolicySigner `
        -Policy $signingPolicy `
        -Version $upgradeFromVersion `
        -Thumbprint $normalizedPreviousSigner
    Assert-ExpectedSignature -Path $resolvedPreviousNsis -PolicySigner $previousPolicySigner
    Assert-ExpectedSignature -Path $resolvedPreviousMsi -PolicySigner $previousPolicySigner
    Assert-PinnedMsiIdentity -Identity $previousMsiIdentity -ExpectedVersion $upgradeFromVersion
    if (
        [string]$previousMsiIdentity.productCode -ceq [string]$currentMsiIdentity.productCode
    ) {
        throw 'Major MSI upgrades require a new ProductCode.'
    }
    $upgradeFromNsisSha256 = (
        Get-FileHash -LiteralPath $resolvedPreviousNsis -Algorithm SHA256
    ).Hash.ToLowerInvariant()
    $upgradeFromMsiSha256 = (
        Get-FileHash -LiteralPath $resolvedPreviousMsi -Algorithm SHA256
    ).Hash.ToLowerInvariant()
}

$smokeId = [guid]::NewGuid().ToString('N')
$nsisInstallRoot = Join-Path $env:LOCALAPPDATA "NekoSSH-Smoke-$smokeId"
$msiInstallRoot = Join-Path $env:ProgramFiles "NekoSSH-Smoke-$smokeId"
foreach ($installRoot in @($nsisInstallRoot, $msiInstallRoot)) {
    if (Test-Path -LiteralPath $installRoot) {
        throw "A generated clean-VM install root already exists: $installRoot"
    }
}

$nsisInstalled = $false
$msiInstalled = $false
$checks = [ordered]@{}
try {
    if (-not $FirstRelease) {
        Invoke-ValidationProcess `
            -FilePath $resolvedPreviousNsis `
            -ArgumentList @('/S', "/D=$nsisInstallRoot") | Out-Null
        $nsisInstalled = $true
        Assert-InstalledPayload `
            -InstallRoot $nsisInstallRoot `
            -ExpectedVersion $upgradeFromVersion `
            -PolicySigner $previousPolicySigner `
            -RequireUninstaller
        Assert-RegisteredProduct `
            -InstallRoot $nsisInstallRoot `
            -ExpectedVersion $upgradeFromVersion `
            -InstallerKind nsis
    }

    Invoke-ValidationProcess `
        -FilePath $nsisPath `
        -ArgumentList @('/S', "/D=$nsisInstallRoot") | Out-Null
    $nsisInstalled = $true
    Assert-InstalledPayload `
        -InstallRoot $nsisInstallRoot `
        -ExpectedVersion $version `
        -PolicySigner $policySigner `
        -RequireUninstaller
    Assert-RegisteredProduct `
        -InstallRoot $nsisInstallRoot `
        -ExpectedVersion $version `
        -InstallerKind nsis
    $checks.NsisInstall = 'passed'
    $checks.NsisUpgrade = if ($FirstRelease) { 'not-applicable-first-release' } else { 'passed' }
    $checks.NsisNativeSelfTest = 'passed'
    $checks.NsisInstalledSignatures = 'passed'

    Invoke-ValidationProcess `
        -FilePath $nsisPath `
        -ArgumentList @('/S', "/D=$nsisInstallRoot") | Out-Null
    Assert-InstalledPayload `
        -InstallRoot $nsisInstallRoot `
        -ExpectedVersion $version `
        -PolicySigner $policySigner `
        -RequireUninstaller
    Assert-RegisteredProduct `
        -InstallRoot $nsisInstallRoot `
        -ExpectedVersion $version `
        -InstallerKind nsis
    $checks.NsisReinstall = 'passed'

    if (-not $FirstRelease) {
        $downgrade = Invoke-ValidationProcess `
            -FilePath $resolvedPreviousNsis `
            -ArgumentList @('/S', "/D=$nsisInstallRoot") `
            -AllowAnyExitCode
        if ($downgrade.ExitCode -in @(1641, 3010)) {
            throw 'NSIS downgrade probe unexpectedly requested a reboot.'
        }
        Assert-InstalledPayload `
            -InstallRoot $nsisInstallRoot `
            -ExpectedVersion $version `
            -PolicySigner $policySigner `
            -RequireUninstaller
        Assert-RegisteredProduct `
            -InstallRoot $nsisInstallRoot `
            -ExpectedVersion $version `
            -InstallerKind nsis
        $checks.NsisDowngradeBlocked = 'passed'
    } else {
        $checks.NsisDowngradeBlocked = 'not-applicable-first-release'
    }

    $nsisUninstaller = Join-Path $nsisInstallRoot 'uninstall.exe'
    Invoke-ValidationProcess -FilePath $nsisUninstaller -ArgumentList @('/S') | Out-Null
    Assert-NoNekoSshResidue -InstallRoot $nsisInstallRoot
    $nsisInstalled = $false
    $checks.NsisUninstall = 'passed'

    if (-not $FirstRelease) {
        Invoke-ValidationProcess `
            -FilePath (Join-Path $env:SystemRoot 'System32/msiexec.exe') `
            -ArgumentList @(
                '/i',
                $resolvedPreviousMsi,
                '/qn',
                '/norestart',
                "INSTALLDIR=$msiInstallRoot",
                'AUTOLAUNCHAPP=0'
            ) | Out-Null
        $msiInstalled = $true
        Assert-InstalledPayload `
            -InstallRoot $msiInstallRoot `
            -ExpectedVersion $upgradeFromVersion `
            -PolicySigner $previousPolicySigner
        Assert-RegisteredProduct `
            -InstallRoot $msiInstallRoot `
            -ExpectedVersion $upgradeFromVersion `
            -InstallerKind msi `
            -ExpectedProductCode ([string]$previousMsiIdentity.productCode)
    }

    Invoke-ValidationProcess `
        -FilePath (Join-Path $env:SystemRoot 'System32/msiexec.exe') `
        -ArgumentList @(
            '/i',
            $msiPath,
            '/qn',
            '/norestart',
            "INSTALLDIR=$msiInstallRoot",
            'AUTOLAUNCHAPP=0'
        ) | Out-Null
    $msiInstalled = $true
    Assert-InstalledPayload `
        -InstallRoot $msiInstallRoot `
        -ExpectedVersion $version `
        -PolicySigner $policySigner
    Assert-RegisteredProduct `
        -InstallRoot $msiInstallRoot `
        -ExpectedVersion $version `
        -InstallerKind msi `
        -ExpectedProductCode ([string]$currentMsiIdentity.productCode)
    $checks.MsiInstall = 'passed'
    $checks.MsiUpgrade = if ($FirstRelease) { 'not-applicable-first-release' } else { 'passed' }
    $checks.MsiNativeSelfTest = 'passed'
    $checks.MsiInstalledSignatures = 'passed'

    Invoke-ValidationProcess `
        -FilePath (Join-Path $env:SystemRoot 'System32/msiexec.exe') `
        -ArgumentList @(
            '/i',
            $msiPath,
            '/qn',
            '/norestart',
            "INSTALLDIR=$msiInstallRoot",
            'AUTOLAUNCHAPP=0'
        ) | Out-Null
    Assert-InstalledPayload `
        -InstallRoot $msiInstallRoot `
        -ExpectedVersion $version `
        -PolicySigner $policySigner
    Assert-RegisteredProduct `
        -InstallRoot $msiInstallRoot `
        -ExpectedVersion $version `
        -InstallerKind msi `
        -ExpectedProductCode ([string]$currentMsiIdentity.productCode)
    $checks.MsiReinstall = 'passed'

    if (-not $FirstRelease) {
        $downgrade = Invoke-ValidationProcess `
            -FilePath (Join-Path $env:SystemRoot 'System32/msiexec.exe') `
            -ArgumentList @(
                '/i',
                $resolvedPreviousMsi,
                '/qn',
                '/norestart',
                "INSTALLDIR=$msiInstallRoot",
                'AUTOLAUNCHAPP=0'
            ) `
            -AllowAnyExitCode
        if ($downgrade.ExitCode -in @(1641, 3010)) {
            throw 'MSI downgrade probe unexpectedly requested a reboot.'
        }
        Assert-InstalledPayload `
            -InstallRoot $msiInstallRoot `
            -ExpectedVersion $version `
            -PolicySigner $policySigner
        Assert-RegisteredProduct `
            -InstallRoot $msiInstallRoot `
            -ExpectedVersion $version `
            -InstallerKind msi `
            -ExpectedProductCode ([string]$currentMsiIdentity.productCode)
        $checks.MsiDowngradeBlocked = 'passed'
    } else {
        $checks.MsiDowngradeBlocked = 'not-applicable-first-release'
    }

    Invoke-ValidationProcess `
        -FilePath (Join-Path $env:SystemRoot 'System32/msiexec.exe') `
        -ArgumentList @(
            '/x',
            [string]$currentMsiIdentity.productCode,
            '/qn',
            '/norestart'
        ) | Out-Null
    Assert-NoNekoSshResidue -InstallRoot $msiInstallRoot
    $msiInstalled = $false
    $checks.MsiUninstall = 'passed'
    $checks.WebPortableSelfTest = 'passed'
    $checks.Upgrade = if ($FirstRelease) { 'not-applicable-first-release' } else { 'passed' }
}
finally {
    if ($nsisInstalled -and (Test-Path -LiteralPath (Join-Path $nsisInstallRoot 'uninstall.exe'))) {
        try {
            Invoke-ValidationProcess `
                -FilePath (Join-Path $nsisInstallRoot 'uninstall.exe') `
                -ArgumentList @('/S') | Out-Null
        } catch {
            Write-Warning "Emergency NSIS cleanup failed: $($_.Exception.Message)"
        }
    }
    if ($msiInstalled) {
        foreach ($productCode in @(
            [string]$currentMsiIdentity.productCode,
            $(if ($null -eq $previousMsiIdentity) { $null } else { [string]$previousMsiIdentity.productCode })
        ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -Unique) {
            try {
                Invoke-ValidationProcess `
                    -FilePath (Join-Path $env:SystemRoot 'System32/msiexec.exe') `
                    -ArgumentList @('/x', $productCode, '/qn', '/norestart') `
                    -AllowedExitCodes @(0, 1605) | Out-Null
            } catch {
                Write-Warning "Emergency MSI cleanup failed for $productCode`: $($_.Exception.Message)"
            }
        }
    }
}

$receiptPath = Join-Path $resolvedReleaseDirectory 'WINDOWS_SMOKE_RECEIPT.psd1'
if (Test-Path -LiteralPath $receiptPath) {
    throw "Refusing to overwrite an existing Windows smoke receipt: $receiptPath"
}
$validationScriptHash = Get-ReleaseTextSha256 -Path $validationScriptPath
$signingPolicyHash = Get-ReleaseTextSha256 -Path $policyPath
if ($signingPolicyHash -cne $signingPolicy.Sha256) {
    throw 'The signing policy changed during Windows acceptance.'
}
$quote = {
    param([string]$Value)
    return "'$($Value.Replace("'", "''"))'"
}
$lines = [Collections.Generic.List[string]]::new()
$lines.Add('@{')
$lines.Add('    SchemaVersion = 1')
$lines.Add("    Repository = $(& $quote $expectedRepository)")
$lines.Add("    Version = $(& $quote $version)")
$lines.Add("    Commit = $(& $quote $commit)")
$lines.Add("    TestedAtUtc = $(& $quote ([DateTimeOffset]::UtcNow.ToString('o')))")
$lines.Add('    CleanWindowsVm = $true')
$lines.Add('    Elevated = $true')
$lines.Add("    Architecture = 'x64'")
$lines.Add("    WindowsVersion = $(& $quote ([Environment]::OSVersion.VersionString))")
$lines.Add("    ValidationNonce = $(& $quote $ValidationNonce)")
$lines.Add("    CleanVmImageId = $(& $quote $CleanVmImageId)")
$lines.Add("    ValidationScriptSha256 = $(& $quote $validationScriptHash)")
$lines.Add("    SigningPolicySha256 = $(& $quote $signingPolicyHash)")
$lines.Add("    UpgradeMode = $(& $quote $upgradeMode)")
$lines.Add(
    $(if ($null -eq $upgradeFromVersion) {
        '    UpgradeFromVersion = $null'
    } else {
        "    UpgradeFromVersion = $(& $quote $upgradeFromVersion)"
    })
)
$lines.Add(
    $(if ($null -eq $upgradeFromNsisSha256) {
        '    UpgradeFromNsisSha256 = $null'
    } else {
        "    UpgradeFromNsisSha256 = $(& $quote $upgradeFromNsisSha256)"
    })
)
$lines.Add(
    $(if ($null -eq $upgradeFromMsiSha256) {
        '    UpgradeFromMsiSha256 = $null'
    } else {
        "    UpgradeFromMsiSha256 = $(& $quote $upgradeFromMsiSha256)"
    })
)
$lines.Add(
    $(if ($null -eq $previousPolicySigner) {
        '    UpgradeFromSignerThumbprint = $null'
    } else {
        "    UpgradeFromSignerThumbprint = $(& $quote $previousPolicySigner.Thumbprint)"
    })
)
$lines.Add('    Checks = @{')
foreach ($name in @($checks.Keys | Sort-Object)) {
    $lines.Add("        $name = $(& $quote ([string]$checks[$name]))")
}
$lines.Add('    }')
$lines.Add('    Files = @{')
foreach ($name in @($files.Keys | Sort-Object)) {
    $lines.Add("        $(& $quote $name) = $(& $quote ([string]$files[$name]))")
}
$lines.Add('    }')
$lines.Add('}')
[IO.File]::WriteAllLines($receiptPath, $lines, [Text.UTF8Encoding]::new($true))
if ((Get-AuthenticodeSignature -LiteralPath $receiptPath).Status.ToString() -ne 'NotSigned') {
    throw 'The clean VM must generate an unsigned smoke receipt.'
}
Write-Host "Unsigned Windows smoke receipt: $receiptPath"
Write-Host "Pre-issued validation nonce: $ValidationNonce"
Write-Host "Clean VM image ID: $CleanVmImageId"
Write-Host 'Copy the unchanged receipt back to the controlled signing machine, then run:'
Write-Host (
    "powershell -NoProfile -ExecutionPolicy Bypass -File scripts/sign-windows-smoke-receipt.ps1 " +
    "-ReleaseDirectory `"$resolvedReleaseDirectory`" " +
    "-ToolchainLockPath <controlled-builder-toolchain-lock-path> " +
    "-ExpectedToolchainLockSha256 <independently-recorded-64-hex-sha256> " +
    "-SignerThumbprint $normalizedSigner " +
    "-TimestampUrl <http-Authenticode-timestamp-url> " +
    "-ExpectedValidationNonce $ValidationNonce -ExpectedCleanVmImageId `"$CleanVmImageId`""
)
