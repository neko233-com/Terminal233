[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$policyModulePath = Join-Path $PSScriptRoot 'release-signing-policy.psm1'
Import-Module -Name $policyModulePath -Force -ErrorAction Stop

$temporaryParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$testRoot = Join-Path $temporaryParent (
    "nekossh-manifest-source-test-$PID-$([guid]::NewGuid().ToString('N'))"
)
$manifestPath = Join-Path $testRoot 'release-manifest.json'
$utf8 = [Text.UTF8Encoding]::new($false)
$commit = '1234567890abcdef1234567890abcdef12345678'
$webHash = '3' * 64
$toolNames = @(
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

function New-TestManifest {
    return [ordered]@{
        schemaVersion = 4
        repository = 'neko233-com/nekossh'
        version = '0.2.0'
        tag = 'v0.2.0'
        commit = $commit
        shortCommit = $commit.Substring(0, 12)
        source = [ordered]@{
            branch = 'main'
            commit = $commit
            tree = '2' * 40
            snapshotSha256 = '4' * 64
            digestFormat = 'git-raw-blobs-sha256-v1'
            trackedFileCount = 151
        }
        prerelease = $false
        publishable = $false
        generatedAtUtc = '2026-07-30T00:00:00.0000000Z'
        toolchain = [ordered]@{
            policySha256 = '9' * 64
            platform = 'windows-x64'
            target = 'x86_64-pc-windows-msvc'
            tools = @(
                foreach ($toolName in $toolNames) {
                    [ordered]@{
                        name = $toolName
                        length = 1
                        sha256 = 'a' * 64
                        version = if ($toolName -ceq 'node') { 'v24.18.1' } else { "test-$toolName-1" }
                        signature = [ordered]@{
                            status = 'NotSigned'
                            subject = $null
                            thumbprint = $null
                            spkiSha256 = $null
                            timestamped = $false
                        }
                    }
                }
            )
        }
        signing = [ordered]@{
            mode = 'unsigned-test'
            expectedSignerThumbprint = $null
            expectedSignerSubject = $null
            expectedSignerSpkiSha256 = $null
            policySha256 = '5' * 64
            timestampRequired = $false
        }
        assets = @(
            [ordered]@{
                name = 'NekoSSH_0.2.0_x64_zh-CN.msi'
                kind = 'tauri-msi'
                bytes = 1
                sha256 = '1' * 64
            },
            [ordered]@{
                name = 'NekoSSH_0.2.0_x64-setup.exe'
                kind = 'tauri-nsis'
                bytes = 1
                sha256 = '2' * 64
            },
            [ordered]@{
                name = 'NekoSSH_Web_0.2.0_windows_x64.zip'
                kind = 'web-portable'
                bytes = 1
                sha256 = $webHash
            },
            [ordered]@{
                name = 'THIRD_PARTY_NOTICES.txt'
                kind = 'third-party-notices'
                bytes = 1
                sha256 = '4' * 64
            }
        )
        webRuntime = [ordered]@{
            archiveSha256 = $webHash
            version = '0.2.0'
            launcher = [ordered]@{
                status = 'NotSigned'
                signerThumbprint = $null
                signerSubject = $null
                signerSpkiSha256 = $null
                timestamped = $false
            }
            node = [ordered]@{
                version = '24.18.1'
                sha256 = '6' * 64
                status = 'Valid'
                signerSubject = 'CN=OpenJS Foundation, O=OpenJS Foundation'
                timestamped = $true
            }
            selfTest = 'skipped-unsigned'
        }
        windowsInstaller = [ordered]@{
            productCode = '{609223C3-3B72-4B50-9EE3-7CD83BC75208}'
            productName = 'NekoSSH'
            manufacturer = 'NekoSSH contributors'
            productLanguage = '2052'
            productVersion = '0.2.0'
            upgradeCode = '{44F1588E-BCFD-5973-A955-237DAF92726D}'
        }
        checksumFile = [ordered]@{
            name = 'SHA256SUMS.txt'
            bytes = 1
            sha256 = '7' * 64
        }
        releaseNotesFile = [ordered]@{
            name = 'RELEASE_NOTES.md'
            bytes = 1
            sha256 = '8' * 64
        }
        signatures = @(
            [ordered]@{
                Name = 'nekossh.exe'
                Status = 'NotSigned'
                SignerThumbprint = $null
                SignerSubject = $null
                SignerSpkiSha256 = $null
                Timestamped = $false
            },
            [ordered]@{
                Name = 'NekoSSH_0.2.0_x64-setup.exe'
                Status = 'NotSigned'
                SignerThumbprint = $null
                SignerSubject = $null
                SignerSpkiSha256 = $null
                Timestamped = $false
            },
            [ordered]@{
                Name = 'NekoSSH_0.2.0_x64_zh-CN.msi'
                Status = 'NotSigned'
                SignerThumbprint = $null
                SignerSubject = $null
                SignerSpkiSha256 = $null
                Timestamped = $false
            }
        )
    }
}

function Write-TestManifest {
    param([Parameter(Mandatory)]$Manifest)

    [IO.File]::WriteAllText(
        $manifestPath,
        ($Manifest | ConvertTo-Json -Depth 10),
        $utf8
    )
}

function Assert-ManifestRejected {
    param(
        [Parameter(Mandatory)]$Manifest,
        [Parameter(Mandatory)][string]$MessagePattern,
        [Parameter(Mandatory)][string]$FailureMessage
    )

    Write-TestManifest -Manifest $Manifest
    $rejected = $false
    try {
        Import-NekoSshReleaseManifest -Path $manifestPath | Out-Null
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
    $manifest = New-TestManifest
    Write-TestManifest -Manifest $manifest
    $parsed = Import-NekoSshReleaseManifest -Path $manifestPath
    if (
        $parsed.schemaVersion -ne 4 -or
        $parsed.source.commit -cne $parsed.commit -or
        $parsed.source.digestFormat -cne 'git-raw-blobs-sha256-v1'
    ) {
        throw 'A valid schema-v4 source identity did not survive strict parsing.'
    }

    $manifest = New-TestManifest
    $manifest.source.commit = '9' * 40
    Assert-ManifestRejected `
        -Manifest $manifest `
        -MessagePattern '*source identity*invalid*' `
        -FailureMessage 'Manifest parser did not reject a source/top-level commit mismatch.'

    $manifest = New-TestManifest
    $manifest.source.tree = 'not-a-tree'
    Assert-ManifestRejected `
        -Manifest $manifest `
        -MessagePattern '*source identity*invalid*' `
        -FailureMessage 'Manifest parser did not reject a malformed source tree.'

    $manifest = New-TestManifest
    $manifest.source.snapshotSha256 = 'A' * 64
    Assert-ManifestRejected `
        -Manifest $manifest `
        -MessagePattern '*lowercase SHA-256*' `
        -FailureMessage 'Manifest parser did not reject a noncanonical snapshot digest.'

    $manifest = New-TestManifest
    $manifest.source.digestFormat = 'unversioned'
    Assert-ManifestRejected `
        -Manifest $manifest `
        -MessagePattern '*source identity*invalid*' `
        -FailureMessage 'Manifest parser did not reject an unknown snapshot digest format.'

    $manifest = New-TestManifest
    $manifest.source.trackedFileCount = 0
    Assert-ManifestRejected `
        -Manifest $manifest `
        -MessagePattern '*source identity*invalid*' `
        -FailureMessage 'Manifest parser did not reject an empty tracked source inventory.'

    $manifest = New-TestManifest
    $manifest.toolchain.policySha256 = 'A' * 64
    Assert-ManifestRejected `
        -Manifest $manifest `
        -MessagePattern '*toolchain.policySha256*lowercase SHA-256*' `
        -FailureMessage 'Manifest parser did not reject a noncanonical toolchain policy digest.'

    $manifest = New-TestManifest
    $manifest.toolchain.tools[0].signature.status = 'Valid'
    Assert-ManifestRejected `
        -Manifest $manifest `
        -MessagePattern '*signer fields must not be null*' `
        -FailureMessage 'Manifest parser did not reject an incomplete signed tool identity.'

    $manifest = New-TestManifest
    $manifest.toolchain.tools[1].name = 'powershell'
    Assert-ManifestRejected `
        -Manifest $manifest `
        -MessagePattern '*identity is invalid*' `
        -FailureMessage 'Manifest parser did not reject duplicate tool identities.'

    $manifest = New-TestManifest
    $manifest.schemaVersion = 3
    Assert-ManifestRejected `
        -Manifest $manifest `
        -MessagePattern '*schemaVersion*types are invalid*' `
        -FailureMessage 'Manifest parser did not fail closed on the obsolete schema.'

    [pscustomobject]@{
        SchemaV4 = 'passed'
        CommitBinding = 'passed'
        TreeFormat = 'passed'
        SnapshotSha256 = 'passed'
        DigestFormat = 'passed'
        TrackedFileCount = 'passed'
        ToolchainPolicySha256 = 'passed'
        ToolchainSignerIdentity = 'passed'
        ToolchainExactSet = 'passed'
        ObsoleteSchemaRejected = 'passed'
    } | ConvertTo-Json -Compress
}
finally {
    if (Test-Path -LiteralPath $testRoot) {
        $resolved = [IO.Path]::GetFullPath($testRoot)
        $parent = $temporaryParent.TrimEnd('\', '/')
        if (-not $resolved.StartsWith("$parent$([IO.Path]::DirectorySeparatorChar)", [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to remove manifest test data outside the temporary directory: $resolved"
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
