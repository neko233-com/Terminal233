[CmdletBinding()]
param(
    [string]$OutputRoot,
    [string]$NpmCliPath = $env:NEKOSSH_NPM_CLI,
    [string]$TauriSigningConfig,
    [string]$ExpectedSignerThumbprint,
    [switch]$AllowUnsigned
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$signingPolicyPath = Join-Path $repositoryRoot 'release-signing-policy.json'
$signingPolicyModulePath = Join-Path $PSScriptRoot 'release-signing-policy.psm1'
$resolvedNpmCli = if ([string]::IsNullOrWhiteSpace($NpmCliPath)) {
    $null
} else {
    (Resolve-Path -LiteralPath $NpmCliPath).Path
}
$resolvedSigningConfig = if ([string]::IsNullOrWhiteSpace($TauriSigningConfig)) {
    $null
} else {
    (Resolve-Path -LiteralPath $TauriSigningConfig).Path
}
$normalizedExpectedSigner = if ([string]::IsNullOrWhiteSpace($ExpectedSignerThumbprint)) {
    $null
} else {
    ($ExpectedSignerThumbprint -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()
}
$generatedOutputRoot = [IO.Path]::GetFullPath((Join-Path $repositoryRoot 'output'))
$releaseOutputRoot = if ([string]::IsNullOrWhiteSpace($OutputRoot)) {
    [IO.Path]::GetFullPath((Join-Path $generatedOutputRoot 'release'))
} elseif ([IO.Path]::IsPathRooted($OutputRoot)) {
    [IO.Path]::GetFullPath($OutputRoot)
} else {
    [IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputRoot))
}

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
        if (-not (Test-Path -LiteralPath $path)) {
            continue
        }
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

function Remove-OwnedFlatDirectory {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Parent
    )

    if (-not (Test-Path -LiteralPath $Path)) {
        return
    }
    if (-not (Test-ChildPath -Path $Path -Parent $Parent)) {
        throw "Refusing to clean a staging directory outside '$Parent': $Path"
    }
    Assert-PathHasNoReparsePoints -Path $Path -TrustedRoot $repositoryRoot
    Assert-NotReparsePoint -Paths @($Parent, $Path)
    $children = @(Get-ChildItem -LiteralPath $Path -Force)
    foreach ($child in $children) {
        if ($child.PSIsContainer -or (($child.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0)) {
            throw "Refusing to clean unexpected staging content: $($child.FullName)"
        }
    }
    foreach ($child in $children) {
        Remove-Item -LiteralPath $child.FullName -Force
    }
    Remove-Item -LiteralPath $Path -Force
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
            throw "Release build tree contains a reparse point: $directory"
        }
        $directories.Add($directory)
        foreach ($entry in @(Get-ChildItem -LiteralPath $directory -Force)) {
            if (($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Release build tree contains a reparse point: $($entry.FullName)"
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

function Remove-SafeBuildTree {
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path)) { return }
    $fullPath = [IO.Path]::GetFullPath($Path)
    if (-not (Test-ChildPath -Path $fullPath -Parent $generatedOutputRoot)) {
        throw "Refusing to remove a release build tree outside output/: $fullPath"
    }
    Assert-PathHasNoReparsePoints -Path $fullPath -TrustedRoot $repositoryRoot
    $tree = Get-SafeTreeEntries -Root $fullPath
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

function Invoke-NpmChecked {
    param([Parameter(Mandatory)][string[]]$ArgumentList)

    if ($null -eq $resolvedNpmCli) {
        Invoke-Checked -FilePath 'npm' -ArgumentList $ArgumentList
    } else {
        Invoke-Checked -FilePath 'node' -ArgumentList (@($resolvedNpmCli) + $ArgumentList)
    }
}

function Get-NpmCheckedOutput {
    param([Parameter(Mandatory)][string[]]$ArgumentList)

    if ($null -eq $resolvedNpmCli) {
        return Get-CheckedOutput -FilePath 'npm' -ArgumentList $ArgumentList
    }
    return Get-CheckedOutput -FilePath 'node' -ArgumentList (@($resolvedNpmCli) + $ArgumentList)
}

function Get-PackageVersion {
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
        throw "Release versions do not match: $($versions -join ', ')."
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
        $view.Execute()
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
        $view.Execute()
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

if (-not (Test-ChildPath -Path $releaseOutputRoot -Parent $generatedOutputRoot)) {
    throw "Release output must stay below '$generatedOutputRoot'."
}
Assert-PathHasNoReparsePoints -Path $releaseOutputRoot -TrustedRoot $repositoryRoot
if ($AllowUnsigned -and ($null -ne $resolvedSigningConfig -or $null -ne $normalizedExpectedSigner)) {
    throw '-AllowUnsigned cannot be combined with signing configuration or an expected signer.'
}
if (-not $AllowUnsigned) {
    if ($null -eq $resolvedSigningConfig) {
        throw 'A formal release requires -TauriSigningConfig with a local Tauri signing configuration.'
    }
    if ($null -eq $normalizedExpectedSigner -or $normalizedExpectedSigner -notmatch '^[0-9A-F]{40}$') {
        throw 'A formal release requires the expected 40-character signer certificate thumbprint.'
    }
}

$temporaryNoticesPath = $null
$temporaryTauriTargetRoot = $null
Push-Location $repositoryRoot
try {
    $branch = Get-CheckedOutput -FilePath 'git' -ArgumentList @('branch', '--show-current')
    if ($branch -ne 'main') {
        throw "Manual releases must be built from main, not '$branch'."
    }
    if (Get-CheckedOutput -FilePath 'git' -ArgumentList @('status', '--porcelain', '--untracked-files=all')) {
        throw 'Manual releases require a clean working tree.'
    }

    Invoke-Checked -FilePath 'git' -ArgumentList @('fetch', '--prune', 'origin')
    $commit = Get-CheckedOutput -FilePath 'git' -ArgumentList @('rev-parse', 'HEAD')
    $originCommit = Get-CheckedOutput -FilePath 'git' -ArgumentList @('rev-parse', 'origin/main')
    if ($commit -ne $originCommit) {
        throw "HEAD ($commit) must exactly match origin/main ($originCommit) before packaging."
    }

    $version = Get-PackageVersion
    if ($version -notmatch '^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(?:(?:0|[1-9]\d*|[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|[A-Za-z-][0-9A-Za-z-]*))*))?$') {
        throw "Release version is not an allowed SemVer value: '$version'."
    }
    $versionHasPrereleaseSuffix = $version.Contains('-')
    $shortCommit = $commit.Substring(0, 12)
    Import-Module -Name $signingPolicyModulePath -Force
    $signingPolicy = Import-ReleaseSigningPolicy -Path $signingPolicyPath
    $policySigner = if ($AllowUnsigned) {
        $null
    } else {
        Get-ReleaseSigningPolicySigner `
            -Policy $signingPolicy `
            -Version $version `
            -Thumbprint $normalizedExpectedSigner
    }
    if (-not $AllowUnsigned) {
        $signingCertificates = @(
            Get-ChildItem Cert:\CurrentUser\My, Cert:\LocalMachine\My -ErrorAction SilentlyContinue |
                Where-Object {
                    $_.Thumbprint.ToUpperInvariant() -ceq $normalizedExpectedSigner -and
                    $_.HasPrivateKey
                }
        )
        if ($signingCertificates.Count -eq 0) {
            throw 'The policy-selected code-signing certificate with an accessible private key is unavailable.'
        }
        foreach ($certificate in $signingCertificates) {
            [void](Assert-ReleaseSigningCertificate `
                -Certificate $certificate `
                -Signer $policySigner `
                -RequirePrivateKey)
        }
    }

    $package = Get-Content -LiteralPath 'package.json' -Raw | ConvertFrom-Json
    $requiredNode = (Get-Content -LiteralPath '.nvmrc' -Raw).Trim().TrimStart('v')
    $requiredNpm = ([string]$package.packageManager).Split('@')[-1]
    $toolchain = Get-Content -LiteralPath 'rust-toolchain.toml' -Raw
    $rustMatch = [regex]::Match($toolchain, '(?m)^channel\s*=\s*"([^"]+)"')
    if (-not $rustMatch.Success) {
        throw 'Unable to read the pinned Rust toolchain.'
    }
    $requiredRust = $rustMatch.Groups[1].Value

    $actualNode = (Get-CheckedOutput -FilePath 'node' -ArgumentList @('--version')).TrimStart('v')
    $actualNpm = Get-NpmCheckedOutput -ArgumentList @('--version')
    $actualRust = Get-CheckedOutput -FilePath 'rustc' -ArgumentList @('--version')
    $actualRustVerbose = Get-CheckedOutput -FilePath 'rustc' -ArgumentList @('-vV')
    $actualCargo = Get-CheckedOutput -FilePath 'cargo' -ArgumentList @('--version')
    if ($actualNode -ne $requiredNode) { throw "Node.js $requiredNode is required; found $actualNode." }
    if ($actualNpm -ne $requiredNpm) { throw "npm $requiredNpm is required; found $actualNpm." }
    if ($actualRust -notmatch "^rustc $([regex]::Escape($requiredRust)) ") {
        throw "Rust $requiredRust is required; found '$actualRust'."
    }
    if ($actualCargo -notmatch "^cargo $([regex]::Escape($requiredRust)) ") {
        throw "Cargo $requiredRust is required; found '$actualCargo'."
    }
    if ($actualRustVerbose -notmatch '(?m)^host: x86_64-pc-windows-msvc\r?$') {
        throw "The Windows x64 MSVC Rust host is required.`n$actualRustVerbose"
    }

    $vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio/Installer/vswhere.exe'
    if (-not (Test-Path -LiteralPath $vswhere -PathType Leaf)) {
        throw 'Visual Studio Installer vswhere.exe was not found.'
    }
    $visualStudioPath = Get-CheckedOutput -FilePath $vswhere -ArgumentList @(
        '-latest',
        '-products',
        '*',
        '-requires',
        'Microsoft.VisualStudio.Component.VC.Tools.x86.x64',
        '-property',
        'installationPath'
    )
    if ([string]::IsNullOrWhiteSpace($visualStudioPath)) {
        throw 'Visual Studio C++ x64 Build Tools are required.'
    }
    $windowsSdkBinRoot = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits/10/bin'
    $signTool = @(
        Get-ChildItem -Path (Join-Path $windowsSdkBinRoot '*/x64/signtool.exe') -File -ErrorAction SilentlyContinue |
            Sort-Object { [version]$_.Directory.Parent.Name } -Descending
    ) | Select-Object -First 1
    $signToolPath = if ($null -eq $signTool) { $null } else { $signTool.FullName }
    if (-not $AllowUnsigned -and $null -eq $signToolPath) {
        throw 'A formal Windows release requires the x64 Windows SDK SignTool.'
    }

    Invoke-Checked -FilePath 'perl' -ArgumentList @(
        '-MLocale::Maketext::Simple',
        '-e',
        'print qq(Perl modules ready\n)'
    )
    Invoke-Checked -FilePath 'nasm' -ArgumentList @('-v')
    $cargoAuditVersion = Get-CheckedOutput -FilePath 'cargo' -ArgumentList @('audit', '--version')
    if ($cargoAuditVersion -notmatch '\b0\.22\.2\b') {
        throw "cargo-audit 0.22.2 is required; found '$cargoAuditVersion'."
    }

    Invoke-NpmChecked -ArgumentList @('ci', '--ignore-scripts', '--no-audit', '--no-fund')
    Invoke-NpmChecked -ArgumentList @('run', 'verify')
    Invoke-NpmChecked -ArgumentList @('audit', '--audit-level=low', '--ignore-scripts')
    Invoke-Checked -FilePath 'cargo' -ArgumentList @('audit', '--file', 'src-tauri/Cargo.lock')
    Invoke-Checked -FilePath 'cargo' -ArgumentList @('audit', '--file', 'web-launcher/Cargo.lock')
    New-Item -ItemType Directory -Path $generatedOutputRoot -Force | Out-Null
    Assert-PathHasNoReparsePoints -Path $generatedOutputRoot -TrustedRoot $repositoryRoot
    $temporaryNoticesPath = Join-Path $generatedOutputRoot (
        ".third-party-notices-$PID-$([guid]::NewGuid().ToString('N')).txt"
    )
    Invoke-Checked -FilePath 'node' -ArgumentList @(
        'scripts/generate-third-party-notices.mjs',
        $temporaryNoticesPath
    )
    if (-not (Test-Path -LiteralPath $temporaryNoticesPath -PathType Leaf)) {
        throw 'Third-party notices were not generated.'
    }
    Assert-NotReparsePoint -Paths @($temporaryNoticesPath)

    $tauriTargetRoot = [IO.Path]::GetFullPath(
        (Join-Path $generatedOutputRoot ".tauri-target-$PID-$([guid]::NewGuid().ToString('N'))")
    )
    if (Test-Path -LiteralPath $tauriTargetRoot) {
        throw "Fresh Tauri target root unexpectedly exists: $tauriTargetRoot"
    }
    New-Item -ItemType Directory -Path $tauriTargetRoot | Out-Null
    $temporaryTauriTargetRoot = $tauriTargetRoot
    Assert-PathHasNoReparsePoints -Path $tauriTargetRoot -TrustedRoot $repositoryRoot
    $releaseTargetRoot = [IO.Path]::GetFullPath((Join-Path $tauriTargetRoot 'release'))
    $bundleRoot = [IO.Path]::GetFullPath((Join-Path $releaseTargetRoot 'bundle'))
    if (-not (Test-ChildPath -Path $bundleRoot -Parent $releaseTargetRoot)) {
        throw 'Refusing to clean an unexpected Tauri bundle path.'
    }

    $nsisName = "NekoSSH_${version}_x64-setup.exe"
    $msiName = "NekoSSH_${version}_x64_zh-CN.msi"
    $webPortableName = "NekoSSH_Web_${version}_windows_x64.zip"
    $appExecutable = Join-Path $releaseTargetRoot 'nekossh.exe'
    $nsisDirectory = Join-Path $bundleRoot 'nsis'
    $msiDirectory = Join-Path $bundleRoot 'msi'
    $nsisSource = Join-Path $nsisDirectory $nsisName
    $msiSource = Join-Path $msiDirectory $msiName

    foreach ($checkedPath in @(
        (Join-Path $repositoryRoot 'src-tauri'),
        $tauriTargetRoot,
        $releaseTargetRoot,
        $bundleRoot,
        $nsisDirectory,
        $msiDirectory,
        $generatedOutputRoot,
        $releaseOutputRoot
    )) {
        Assert-PathHasNoReparsePoints -Path $checkedPath -TrustedRoot $repositoryRoot
    }
    foreach ($artifact in @($appExecutable, $nsisSource, $msiSource)) {
        if (-not (Test-Path -LiteralPath $artifact)) {
            continue
        }
        Assert-NotReparsePoint -Paths @($artifact)
        if (-not (Test-Path -LiteralPath $artifact -PathType Leaf)) {
            throw "Expected generated file path is not a regular file: $artifact"
        }
        Remove-Item -LiteralPath $artifact -Force
    }

    $tauriCli = (Resolve-Path -LiteralPath 'node_modules/@tauri-apps/cli/tauri.js').Path
    $tauriArguments = @($tauriCli, 'build', '--ci', '--bundles', 'nsis,msi')
    $temporaryBuildConfig = $null
    $resourceMap = [ordered]@{}
    $resourceMap[$temporaryNoticesPath.Replace('\', '/')] = 'THIRD_PARTY_NOTICES.txt'
    $configOverride = [ordered]@{
        bundle = [ordered]@{
            resources = $resourceMap
        }
    }
    $timestampUrlForSigning = $null
    if ($null -ne $resolvedNpmCli) {
        $portableNpmCommand = "node `"$($resolvedNpmCli.Replace('\', '/'))`" run build"
        $configOverride.build = [ordered]@{
            beforeBuildCommand = $portableNpmCommand
        }
    }
    if ($null -ne $resolvedSigningConfig) {
        Assert-NotReparsePoint -Paths @($resolvedSigningConfig)
        $rawSigningConfig = Get-Content -LiteralPath $resolvedSigningConfig -Raw | ConvertFrom-Json
        $topLevelProperties = @($rawSigningConfig.PSObject.Properties.Name)
        if ($topLevelProperties.Count -ne 1 -or $topLevelProperties[0] -ne 'bundle') {
            throw 'The signing config may only contain bundle.windows signing fields.'
        }
        $bundleConfig = $rawSigningConfig.PSObject.Properties['bundle'].Value
        $bundleProperties = @($bundleConfig.PSObject.Properties.Name)
        if ($bundleProperties.Count -ne 1 -or $bundleProperties[0] -ne 'windows') {
            throw 'The signing config may only contain bundle.windows signing fields.'
        }
        $windowsConfig = $bundleConfig.PSObject.Properties['windows'].Value
        $allowedSigningProperties = @(
            'certificateThumbprint',
            'digestAlgorithm',
            'timestampUrl',
            'tsp'
        )
        $unexpectedSigningProperties = @(
            $windowsConfig.PSObject.Properties.Name |
                Where-Object { $_ -notin $allowedSigningProperties }
        )
        if ($unexpectedSigningProperties.Count -gt 0) {
            throw "Unsupported signing config fields: $($unexpectedSigningProperties -join ', ')."
        }
        $sanitizedWindowsConfig = [ordered]@{}
        foreach ($property in $windowsConfig.PSObject.Properties) {
            $sanitizedWindowsConfig[$property.Name] = $property.Value
        }
        if (-not $sanitizedWindowsConfig.Contains('certificateThumbprint')) {
            throw 'The signing config must provide certificateThumbprint.'
        }
        $configThumbprint = (
            [string]$sanitizedWindowsConfig.certificateThumbprint -replace '[^0-9A-Fa-f]',
            ''
        ).ToUpperInvariant()
        if ($configThumbprint -ne $normalizedExpectedSigner) {
            throw 'Signing config certificateThumbprint does not match -ExpectedSignerThumbprint.'
        }
        $sanitizedWindowsConfig.certificateThumbprint = $normalizedExpectedSigner
        if (
            -not $sanitizedWindowsConfig.Contains('timestampUrl') -or
            [string]::IsNullOrWhiteSpace([string]$sanitizedWindowsConfig.timestampUrl)
        ) {
            throw 'Certificate signing requires timestampUrl.'
        }
        $timestampUri = $null
        if (
            -not [Uri]::TryCreate(
                [string]$sanitizedWindowsConfig.timestampUrl,
                [UriKind]::Absolute,
                [ref]$timestampUri
            ) -or
            $timestampUri.Scheme -notin @('http', 'https') -or
            -not [string]::IsNullOrWhiteSpace($timestampUri.UserInfo)
        ) {
            throw 'timestampUrl must be an absolute HTTP or HTTPS URI without user information.'
        }
        if (
            -not $sanitizedWindowsConfig.Contains('digestAlgorithm') -or
            [string]$sanitizedWindowsConfig.digestAlgorithm -notmatch '(?i)^sha256$'
        ) {
            throw 'The signing config must explicitly use the SHA-256 Authenticode digest.'
        }
        $sanitizedWindowsConfig.digestAlgorithm = 'sha256'
        if (
            $sanitizedWindowsConfig.Contains('tsp') -and
            $sanitizedWindowsConfig.tsp -isnot [bool]
        ) {
            throw 'The signing config tsp value must be boolean.'
        }
        if (
            -not $sanitizedWindowsConfig.Contains('tsp') -or
            $sanitizedWindowsConfig.tsp -ne $true
        ) {
            throw 'Formal releases require tsp=true for an RFC 3161 timestamp.'
        }
        $timestampUrlForSigning = [string]$sanitizedWindowsConfig.timestampUrl
        $configOverride.bundle.windows = $sanitizedWindowsConfig
    }
    if ($configOverride.Count -gt 0) {
        $buildOverride = $configOverride | ConvertTo-Json -Depth 10 -Compress
        New-Item -ItemType Directory -Path $generatedOutputRoot -Force | Out-Null
        Assert-PathHasNoReparsePoints -Path $generatedOutputRoot -TrustedRoot $repositoryRoot
        $temporaryBuildConfig = Join-Path $generatedOutputRoot ".tauri-build-$PID-$([guid]::NewGuid().ToString('N')).json"
        [IO.File]::WriteAllText(
            $temporaryBuildConfig,
            $buildOverride,
            [Text.UTF8Encoding]::new($false)
        )
        $tauriArguments += @('--config', $temporaryBuildConfig)
    }
    $tauriArguments += @('--', '--locked')
    $previousCargoTargetDirectory = $env:CARGO_TARGET_DIR
    try {
        $env:CARGO_TARGET_DIR = $tauriTargetRoot
        Invoke-Checked -FilePath 'node' -ArgumentList $tauriArguments
    }
    finally {
        if ($null -eq $previousCargoTargetDirectory) {
            Remove-Item Env:CARGO_TARGET_DIR -ErrorAction SilentlyContinue
        } else {
            $env:CARGO_TARGET_DIR = $previousCargoTargetDirectory
        }
        if ($null -ne $temporaryBuildConfig -and (Test-Path -LiteralPath $temporaryBuildConfig)) {
            Assert-NotReparsePoint -Paths @($temporaryBuildConfig)
            Remove-Item -LiteralPath $temporaryBuildConfig -Force
        }
    }
    Invoke-NpmChecked -ArgumentList @('run', 'web:build')

    foreach ($checkedPath in @(
        $tauriTargetRoot,
        $releaseTargetRoot,
        $bundleRoot,
        $nsisDirectory,
        $msiDirectory
    )) {
        Assert-PathHasNoReparsePoints -Path $checkedPath -TrustedRoot $repositoryRoot
    }
    foreach ($artifact in @($appExecutable, $nsisSource, $msiSource)) {
        if (-not (Test-Path -LiteralPath $artifact -PathType Leaf)) {
            throw "Expected release artifact was not generated: $artifact"
        }
        Assert-NotReparsePoint -Paths @($artifact)
    }
    foreach ($executable in @($appExecutable, $nsisSource)) {
        if ((Get-Item -LiteralPath $executable).VersionInfo.ProductVersion -ne $version) {
            throw "Executable ProductVersion does not match $version`: $executable"
        }
    }
    $msiIdentity = [ordered]@{
        productCode = Get-MsiProperty -Path $msiSource -Property 'ProductCode'
        productName = Get-MsiProperty -Path $msiSource -Property 'ProductName'
        manufacturer = Get-MsiProperty -Path $msiSource -Property 'Manufacturer'
        productLanguage = Get-MsiProperty -Path $msiSource -Property 'ProductLanguage'
        productVersion = Get-MsiProperty -Path $msiSource -Property 'ProductVersion'
        upgradeCode = Get-MsiUpgradeCode -Path $msiSource
    }
    $parsedProductCode = [guid]::Empty
    if (-not [guid]::TryParseExact([string]$msiIdentity.productCode, 'B', [ref]$parsedProductCode)) {
        throw "MSI ProductCode is not a canonical braced GUID: $msiSource"
    }
    $msiIdentity.productCode = $parsedProductCode.ToString('B').ToUpperInvariant()
    if (
        [string]$msiIdentity.productVersion -ne $version -or
        [string]$msiIdentity.productName -ne 'NekoSSH' -or
        [string]$msiIdentity.manufacturer -ne 'NekoSSH contributors' -or
        [string]$msiIdentity.productLanguage -ne '2052' -or
        [string]$msiIdentity.productCode -cnotmatch '^\{[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}\}$' -or
        [string]$msiIdentity.upgradeCode -ne '{44F1588E-BCFD-5973-A955-237DAF92726D}'
    ) {
        throw "MSI identity does not match the pinned Windows product policy: $msiSource"
    }

    if (Get-CheckedOutput -FilePath 'git' -ArgumentList @('status', '--porcelain', '--untracked-files=all')) {
        throw 'The build changed the working tree; refusing to attribute artifacts to the original commit.'
    }
    $postBuildCommit = Get-CheckedOutput -FilePath 'git' -ArgumentList @('rev-parse', 'HEAD')
    if ($postBuildCommit -ne $commit) {
        throw "HEAD changed during the build ($commit -> $postBuildCommit)."
    }
    Invoke-Checked -FilePath 'git' -ArgumentList @('fetch', '--prune', 'origin')
    $postBuildOriginCommit = Get-CheckedOutput -FilePath 'git' -ArgumentList @('rev-parse', 'origin/main')
    if ($postBuildOriginCommit -ne $commit) {
        throw "origin/main changed during the build ($commit -> $postBuildOriginCommit)."
    }

    $signatureResults = foreach ($artifact in @($appExecutable, $nsisSource, $msiSource)) {
        $signature = Get-AuthenticodeSignature -LiteralPath $artifact
        if ($signature.Status.ToString() -eq 'Valid' -and $null -ne $policySigner) {
            [void](Assert-ReleaseSigningCertificate `
                -Certificate $signature.SignerCertificate `
                -Signer $policySigner)
        }
        [pscustomobject]@{
            Name = Split-Path $artifact -Leaf
            Status = $signature.Status.ToString()
            SignerThumbprint = if ($null -eq $signature.SignerCertificate) {
                $null
            } else {
                $signature.SignerCertificate.Thumbprint.ToUpperInvariant()
            }
            SignerSubject = if ($null -eq $signature.SignerCertificate) {
                $null
            } else {
                $signature.SignerCertificate.Subject
            }
            SignerSpkiSha256 = if ($null -eq $signature.SignerCertificate) {
                $null
            } else {
                Get-CertificateSpkiSha256 -Certificate $signature.SignerCertificate
            }
            Timestamped = $null -ne $signature.TimeStamperCertificate
        }
    }
    $unsafeSignatures = @($signatureResults | Where-Object { $_.Status -notin @('Valid', 'NotSigned') })
    if ($unsafeSignatures.Count -gt 0) {
        $summary = ($unsafeSignatures | ForEach-Object { "$($_.Name)=$($_.Status)" }) -join ', '
        throw "Unsafe Authenticode state cannot be released: $summary"
    }
    $signedArtifacts = @($signatureResults | Where-Object Status -eq 'Valid')
    $unsignedArtifacts = @($signatureResults | Where-Object Status -eq 'NotSigned')
    if ($signedArtifacts.Count -gt 0 -and $unsignedArtifacts.Count -gt 0) {
        throw 'Release artifacts must be either consistently signed or consistently unsigned.'
    }
    $isUnsigned = $unsignedArtifacts.Count -eq $signatureResults.Count
    $isPrerelease = $versionHasPrereleaseSuffix
    $isPublishable = -not $isUnsigned -and -not $versionHasPrereleaseSuffix
    if ($isUnsigned -and -not $AllowUnsigned) {
        throw 'Release artifacts are unsigned. Use -AllowUnsigned only for local validation.'
    }
    if ($isUnsigned) {
        Write-Warning 'Unsigned assets are local validation output only and are never publishable.'
    } else {
        if ($null -eq $normalizedExpectedSigner -or $normalizedExpectedSigner -notmatch '^[0-9A-F]{40}$') {
            throw 'Signed releases require the expected 40-character signer certificate thumbprint.'
        }
        $wrongSigner = @($signatureResults | Where-Object SignerThumbprint -ne $normalizedExpectedSigner)
        if ($wrongSigner.Count -gt 0) {
            $summary = ($wrongSigner | ForEach-Object { "$($_.Name)=$($_.SignerThumbprint)" }) -join ', '
            throw "Authenticode signer thumbprint mismatch: $summary"
        }
        $missingTimestamp = @($signatureResults | Where-Object Timestamped -ne $true)
        if ($missingTimestamp.Count -gt 0) {
            throw "Authenticode timestamp is missing: $(($missingTimestamp.Name) -join ', ')"
        }
    }

    New-Item -ItemType Directory -Path $releaseOutputRoot -Force | Out-Null
    Assert-PathHasNoReparsePoints -Path $releaseOutputRoot -TrustedRoot $repositoryRoot

    $releaseDirectory = [IO.Path]::GetFullPath((Join-Path $releaseOutputRoot "v$version-$shortCommit"))
    if (-not (Test-ChildPath -Path $releaseDirectory -Parent $releaseOutputRoot)) {
        throw 'Refusing to stage artifacts outside the configured release output.'
    }
    if (Test-Path -LiteralPath $releaseDirectory) {
        throw "Release output already exists: $releaseDirectory"
    }
    $partialDirectory = [IO.Path]::GetFullPath(
        (Join-Path $releaseOutputRoot ".partial-v$version-$shortCommit-$PID-$([guid]::NewGuid().ToString('N'))")
    )
    if (-not (Test-ChildPath -Path $partialDirectory -Parent $releaseOutputRoot)) {
        throw 'Refusing to create a staging directory outside the release output.'
    }
    New-Item -ItemType Directory -Path $partialDirectory | Out-Null
    Assert-PathHasNoReparsePoints -Path $partialDirectory -TrustedRoot $repositoryRoot

    try {
        $nsisDestination = Join-Path $partialDirectory $nsisName
        $msiDestination = Join-Path $partialDirectory $msiName
        $webPortableDestination = Join-Path $partialDirectory $webPortableName
        $noticesDestination = Join-Path $partialDirectory 'THIRD_PARTY_NOTICES.txt'
        Copy-Item -LiteralPath $nsisSource -Destination $nsisDestination
        Copy-Item -LiteralPath $msiSource -Destination $msiDestination
        Copy-Item -LiteralPath $temporaryNoticesPath -Destination $noticesDestination

        $webWorkRoot = [IO.Path]::GetFullPath(
            (Join-Path $generatedOutputRoot ".web-payload-$PID-$([guid]::NewGuid().ToString('N'))")
        )
        $webBuildArguments = @{
            WorkRoot = $webWorkRoot
            ArchivePath = $webPortableDestination
            Version = $version
            Commit = $commit
            ThirdPartyNoticesPath = $temporaryNoticesPath
            NodeExecutable = (Get-Command node -ErrorAction Stop).Source
        }
        if ($isUnsigned) {
            $webBuildArguments.AllowUnsigned = $true
        } else {
            $webBuildArguments.SignToolPath = $signToolPath
            $webBuildArguments.ExpectedSignerThumbprint = $normalizedExpectedSigner
            $webBuildArguments.ExpectedSignerSubject = $policySigner.Subject
            $webBuildArguments.ExpectedSignerSpkiSha256 = $policySigner.SpkiSha256
            $webBuildArguments.TimestampUrl = $timestampUrlForSigning
        }
        & (Join-Path $PSScriptRoot 'build-windows-web-portable.ps1') @webBuildArguments
        $webVerifyArguments = @{
            ArchivePath = $webPortableDestination
            ExpectedVersion = $version
            ExpectedCommit = $commit
        }
        if ($isUnsigned) {
            $webVerifyArguments.AllowUnsignedLauncher = $true
        } else {
            $webVerifyArguments.ExpectedProjectSignerThumbprint = $normalizedExpectedSigner
            $webVerifyArguments.ExpectedProjectSignerSubject = $policySigner.Subject
            $webVerifyArguments.ExpectedProjectSignerSpkiSha256 = $policySigner.SpkiSha256
        }
        $webVerification = (
            & (Join-Path $PSScriptRoot 'verify-windows-web-portable.ps1') @webVerifyArguments |
                Out-String
        ).Trim() | ConvertFrom-Json
        $expectedWebSelfTest = if ($isUnsigned) { 'skipped-unsigned' } else { 'passed' }
        if ($webVerification.selfTest -cne $expectedWebSelfTest) {
            throw "The Web portable archive returned an unexpected self-test status: $($webVerification.selfTest)"
        }

        $assetInputs = @(
            [pscustomobject]@{ Kind = 'tauri-msi'; Path = $msiDestination },
            [pscustomobject]@{ Kind = 'tauri-nsis'; Path = $nsisDestination },
            [pscustomobject]@{ Kind = 'web-portable'; Path = $webPortableDestination },
            [pscustomobject]@{ Kind = 'third-party-notices'; Path = $noticesDestination }
        )
        $assetRecords = foreach ($assetInput in @($assetInputs | Sort-Object { Split-Path $_.Path -Leaf })) {
            $file = Get-Item -LiteralPath $assetInput.Path
            [ordered]@{
                name = $file.Name
                kind = $assetInput.Kind
                bytes = $file.Length
                sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            }
        }
        $checksumLines = $assetRecords | ForEach-Object { "$($_.sha256)  $($_.name)" }
        $utf8 = [Text.UTF8Encoding]::new($false)
        $checksumPath = Join-Path $partialDirectory 'SHA256SUMS.txt'
        [IO.File]::WriteAllLines($checksumPath, $checksumLines, $utf8)

        $releaseType = if (-not $isPublishable) {
            '本次资产仅用于本机验证，不允许上传或占用 GitHub Release tag；正式发布仅接受已签名稳定版。'
        } else {
            "本次资产已通过 Authenticode 签名、时间戳和证书指纹校验（$normalizedExpectedSigner）。"
        }
        $releaseNotes = @"
# NekoSSH v$version

NekoSSH v$version 提供两个隔离的 SSH 入口：外部浏览器 Web macOS 伪桌面，以及 Tauri v2 原生 SSH IDE。

## Windows 资产

- ``$nsisName``：面向个人用户的当前用户 NSIS 安装程序
- ``$msiName``：面向管理员部署的简体中文 MSI 安装程序
- ``$webPortableName``：独立 Web macOS + 内置 Node ssh2js Windows 可携包
- ``THIRD_PARTY_NOTICES.txt``：锁定依赖的第三方许可与声明
- ``SHA256SUMS.txt``：全部发行资产的 SHA-256 校验清单

NSIS 与 MSI 是两个独立部署通道；切换格式前必须先卸载已有版本，不支持跨格式直接覆盖。

## 构建验证

- 源提交：``$commit``
- 已执行双运行时生产构建、运行时隔离检查、前端/Node/Rust 测试、Rust fmt 与 Clippy
- 已执行 npm audit 与 cargo-audit
- Web 可携包已验证签名启动器 → payload manifest → 全部文件哈希链，并完成无浏览器自检

## 签名状态

$releaseType
"@
        $releaseNotesPath = Join-Path $partialDirectory 'RELEASE_NOTES.md'
        [IO.File]::WriteAllText($releaseNotesPath, $releaseNotes, $utf8)

        $checksumFile = Get-Item -LiteralPath $checksumPath
        $releaseNotesFile = Get-Item -LiteralPath $releaseNotesPath
        $manifest = [ordered]@{
            schemaVersion = 3
            repository = 'neko233-com/nekossh'
            version = $version
            tag = "v$version"
            commit = $commit
            shortCommit = $shortCommit
            prerelease = [bool]$isPrerelease
            publishable = [bool]$isPublishable
            generatedAtUtc = [DateTime]::UtcNow.ToString('o')
            toolchain = [ordered]@{
                node = $actualNode
                npm = $actualNpm
                rustc = $actualRust
                cargo = $actualCargo
                cargoAudit = $cargoAuditVersion
                target = 'x86_64-pc-windows-msvc'
            }
            signing = [ordered]@{
                mode = if ($isUnsigned) { 'unsigned-test' } else { 'authenticode' }
                expectedSignerThumbprint = if ($isUnsigned) { $null } else { $normalizedExpectedSigner }
                expectedSignerSubject = if ($isUnsigned) { $null } else { $policySigner.Subject }
                expectedSignerSpkiSha256 = if ($isUnsigned) { $null } else { $policySigner.SpkiSha256 }
                policySha256 = $signingPolicy.Sha256
                timestampRequired = -not $isUnsigned
            }
            assets = @($assetRecords)
            webRuntime = $webVerification
            windowsInstaller = $msiIdentity
            checksumFile = [ordered]@{
                name = $checksumFile.Name
                bytes = $checksumFile.Length
                sha256 = (Get-FileHash -LiteralPath $checksumPath -Algorithm SHA256).Hash.ToLowerInvariant()
            }
            releaseNotesFile = [ordered]@{
                name = $releaseNotesFile.Name
                bytes = $releaseNotesFile.Length
                sha256 = (Get-FileHash -LiteralPath $releaseNotesPath -Algorithm SHA256).Hash.ToLowerInvariant()
            }
            signatures = @($signatureResults)
        }
        $manifestJson = $manifest | ConvertTo-Json -Depth 8
        $stagedManifestPath = Join-Path $partialDirectory 'release-manifest.json'
        [IO.File]::WriteAllText($stagedManifestPath, $manifestJson, $utf8)
        $validatedManifest = Import-NekoSshReleaseManifest -Path $stagedManifestPath
        $stagedManifestFile = Get-Item -LiteralPath $stagedManifestPath
        if (
            [long]$stagedManifestFile.Length -ne [long]$validatedManifest.SourceFileLength -or
            (Get-FileHash -LiteralPath $stagedManifestPath -Algorithm SHA256).Hash.ToLowerInvariant() -cne
                $validatedManifest.SourceFileSha256
        ) {
            throw 'The release manifest changed after strict validation.'
        }

        Move-Item -LiteralPath $partialDirectory -Destination $releaseDirectory
        $finalManifest = Import-NekoSshReleaseManifest `
            -Path (Join-Path $releaseDirectory 'release-manifest.json')
        if (
            $finalManifest.SourceFileSha256 -cne $validatedManifest.SourceFileSha256 -or
            $finalManifest.SourceFileLength -ne $validatedManifest.SourceFileLength
        ) {
            throw 'The release manifest changed while finalizing the release directory.'
        }
    }
    finally {
        if (Test-Path -LiteralPath $partialDirectory) {
            Remove-OwnedFlatDirectory -Path $partialDirectory -Parent $releaseOutputRoot
        }
    }

    Write-Host ''
    Write-Host "Manual release assets are ready: $releaseDirectory"
    $signatureResults | Format-Table -AutoSize
    Get-Content -LiteralPath (Join-Path $releaseDirectory 'SHA256SUMS.txt')
}
finally {
    try {
        if ($null -ne $temporaryTauriTargetRoot -and (Test-Path -LiteralPath $temporaryTauriTargetRoot)) {
            Remove-SafeBuildTree -Path $temporaryTauriTargetRoot
        }
        if ($null -ne $temporaryNoticesPath -and (Test-Path -LiteralPath $temporaryNoticesPath)) {
            $fullNoticesPath = [IO.Path]::GetFullPath($temporaryNoticesPath)
            if (-not (Test-ChildPath -Path $fullNoticesPath -Parent $generatedOutputRoot)) {
                throw "Refusing to clean notices outside output/: $fullNoticesPath"
            }
            Assert-NotReparsePoint -Paths @($fullNoticesPath)
            Remove-Item -LiteralPath $fullNoticesPath -Force
        }
    }
    finally {
        Pop-Location
    }
}
