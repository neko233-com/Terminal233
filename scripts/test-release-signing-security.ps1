[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$signingScriptPath = Join-Path $PSScriptRoot 'sign-windows-smoke-receipt.ps1'
$policyModulePath = Join-Path $PSScriptRoot 'release-signing-policy.psm1'
$policyPath = Join-Path $repositoryRoot 'release-signing-policy.json'
$receiptName = 'WINDOWS_SMOKE_RECEIPT.psd1'

function Import-SigningFunctions {
    param([Parameter(Mandatory)][string[]]$Names)

    $parseErrors = $null
    $ast = [Management.Automation.Language.Parser]::ParseFile(
        $signingScriptPath,
        [ref]$null,
        [ref]$parseErrors
    )
    if ($parseErrors.Count -ne 0) {
        throw "Signing script has parser errors: $($parseErrors.Message -join '; ')"
    }
    foreach ($name in $Names) {
        $definition = $ast.Find(
            {
                param($candidate)
                $candidate -is [Management.Automation.Language.FunctionDefinitionAst] -and
                $candidate.Name -ceq $name
            },
            $true
        )
        if ($null -eq $definition) {
            throw "Signing security test could not find function '$name'."
        }
        $bodyText = $definition.Body.Extent.Text
        $bodyScript = [scriptblock]::Create(
            $bodyText.Substring(1, $bodyText.Length - 2)
        )
        Set-Item -LiteralPath "Function:\script:$name" -Value $bodyScript
    }
}

Import-SigningFunctions -Names @(
    'Get-Sha256',
    'Test-ChildPath',
    'Assert-RegularFile',
    'Assert-PathHasNoReparsePoints',
    'Get-CurrentUserSid',
    'Get-SigningAclSids',
    'Assert-DirectoryNotWritableByUntrustedPrincipal',
    'Assert-AncestorsDoNotGrantUntrustedDeleteChild',
    'Assert-RegularFileSystemDirectory',
    'Assert-PrivateSigningDirectory',
    'New-PrivateSigningDirectory',
    'Initialize-SigningNativeMethods',
    'Get-SingleLinkFileIdentity',
    'Copy-RegularFileToNewPath',
    'Restore-UnsignedReceiptFromPrivateBackup',
    'Remove-PrivateSigningDirectory',
    'Get-CheckedGitOutput',
    'Assert-TrackedInputsMatchHead',
    'Resolve-AuthenticodeTimestampUri'
)

$validTimestampUri = Resolve-AuthenticodeTimestampUri `
    -Value 'http://timestamp.example.test/authenticode'
if ($validTimestampUri.AbsoluteUri -cne 'http://timestamp.example.test/authenticode') {
    throw 'The Authenticode timestamp URI validator changed a valid endpoint.'
}
foreach ($invalidTimestampUri in @(
    'https://timestamp.example.test/authenticode',
    'http://user:secret@timestamp.example.test/authenticode',
    'http://timestamp.example.test/authenticode#fragment',
    'not-a-uri'
)) {
    $invalidTimestampRejected = $false
    try {
        Resolve-AuthenticodeTimestampUri -Value $invalidTimestampUri | Out-Null
    }
    catch {
        $invalidTimestampRejected = $_.Exception.Message -like '*Set-AuthenticodeSignature cannot use an HTTPS*'
    }
    if (-not $invalidTimestampRejected) {
        throw "The receipt timestamp gate accepted an unsafe endpoint: $invalidTimestampUri"
    }
}

$privateSigningDirectory = New-PrivateSigningDirectory
$privateSigningSids = @(Get-SigningAclSids)
$privateCopyPath = Join-Path $privateSigningDirectory 'policy-copy.json'
try {
    Copy-RegularFileToNewPath -Source $policyPath -Destination $privateCopyPath
    Assert-PrivateSigningDirectory `
        -Path $privateSigningDirectory `
        -AllowedSids $privateSigningSids
    $privateFileIdentity = Get-SingleLinkFileIdentity -Path $privateCopyPath
    if ($privateFileIdentity -cnotmatch '^[0-9a-f]{8}:[0-9a-f]{16}$') {
        throw 'Private signing file identity is not canonical.'
    }
}
finally {
    if (Test-Path -LiteralPath $privateSigningDirectory) {
        Remove-PrivateSigningDirectory `
            -Path $privateSigningDirectory `
            -AllowedSids $privateSigningSids `
            -ExpectedFiles @($privateCopyPath)
    }
}

$gitExecutable = (
    Get-Command -Name git.exe -CommandType Application -ErrorAction Stop |
        Select-Object -First 1
).Source
Assert-TrackedInputsMatchHead -Paths @('.gitattributes')

function Get-CheckedGitOutput {
    param([Parameter(Mandatory)][string[]]$ArgumentList)

    if ($ArgumentList[0] -ceq 'ls-files') {
        return 'H .gitattributes'
    }
    if ($ArgumentList[0] -ceq 'rev-parse') {
        return ('a' * 40)
    }
    if ($ArgumentList[0] -ceq 'hash-object') {
        return ('b' * 40)
    }
    throw 'The working-tree mismatch test reached an unexpected Git command.'
}
$modifiedInputRejected = $false
try {
    Assert-TrackedInputsMatchHead -Paths @('.gitattributes')
}
catch {
    $modifiedInputRejected = $_.Exception.Message -like '*does not exactly match HEAD*'
}
if (-not $modifiedInputRejected) {
    throw 'The Git trust gate did not reject mismatched working-tree bytes.'
}

function Get-CheckedGitOutput {
    param([Parameter(Mandatory)][string[]]$ArgumentList)

    if ($ArgumentList[0] -ceq 'ls-files') {
        return 'S .gitattributes'
    }
    throw 'The skip-worktree test reached an unexpected Git command.'
}
$skipWorktreeRejected = $false
try {
    Assert-TrackedInputsMatchHead -Paths @('.gitattributes')
}
catch {
    $skipWorktreeRejected = $_.Exception.Message -like '*skip-worktree*'
}
if (-not $skipWorktreeRejected) {
    throw 'The Git trust gate did not reject skip-worktree.'
}

function Get-CheckedGitOutput {
    param([Parameter(Mandatory)][string[]]$ArgumentList)

    if ($ArgumentList[0] -ceq 'ls-files') {
        return 'h .gitattributes'
    }
    throw 'The assume-unchanged test reached an unexpected Git command.'
}
$assumeUnchangedRejected = $false
try {
    Assert-TrackedInputsMatchHead -Paths @('.gitattributes')
}
catch {
    $assumeUnchangedRejected = $_.Exception.Message -like '*Assume-unchanged*'
}
if (-not $assumeUnchangedRejected) {
    throw 'The Git trust gate did not reject assume-unchanged.'
}

$outputRoot = [IO.Path]::GetFullPath((Join-Path $repositoryRoot 'output'))
if (-not (Test-Path -LiteralPath $outputRoot -PathType Container)) {
    [void][IO.Directory]::CreateDirectory($outputRoot)
}
$recoveryRoot = Join-Path $outputRoot (
    "signing-recovery-test-$([guid]::NewGuid().ToString('N'))"
)
if (-not (Test-ChildPath -Path $recoveryRoot -Parent $outputRoot)) {
    throw 'Recovery test directory escaped output/.'
}
[void][IO.Directory]::CreateDirectory($recoveryRoot)
$protectedBackupPath = Join-Path $recoveryRoot 'protected-unsigned.psd1'
$receiptPath = Join-Path $recoveryRoot $receiptName
try {
    [IO.File]::WriteAllText(
        $protectedBackupPath,
        "@{ SchemaVersion = 1 }`r`n",
        [Text.UTF8Encoding]::new($false)
    )
    $expectedReceiptHash = Get-Sha256 -Path $protectedBackupPath
    [IO.File]::WriteAllText(
        $receiptPath,
        'changed',
        [Text.UTF8Encoding]::new($false)
    )
    Restore-UnsignedReceiptFromPrivateBackup `
        -PrivateBackupPath $protectedBackupPath `
        -ReceiptPath $receiptPath `
        -ReleaseDirectory $recoveryRoot `
        -ExpectedSha256 $expectedReceiptHash
    if ((Get-Sha256 -Path $receiptPath) -cne $expectedReceiptHash) {
        throw 'Recovery did not replace an existing failed receipt.'
    }
    Remove-Item -LiteralPath $receiptPath -Force
    Restore-UnsignedReceiptFromPrivateBackup `
        -PrivateBackupPath $protectedBackupPath `
        -ReceiptPath $receiptPath `
        -ReleaseDirectory $recoveryRoot `
        -ExpectedSha256 $expectedReceiptHash
    if ((Get-Sha256 -Path $receiptPath) -cne $expectedReceiptHash) {
        throw 'Recovery did not restore a missing receipt.'
    }
}
finally {
    foreach ($knownPath in @($receiptPath, $protectedBackupPath)) {
        if (Test-Path -LiteralPath $knownPath) {
            $knownItem = Get-Item -LiteralPath $knownPath -Force
            if (
                $knownItem.PSIsContainer -or
                ($knownItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0
            ) {
                throw "Refusing to remove an unsafe recovery test path: $knownPath"
            }
            Remove-Item -LiteralPath $knownPath -Force
        }
    }
    if (Test-Path -LiteralPath $recoveryRoot) {
        [IO.Directory]::Delete($recoveryRoot, $false)
    }
}

Import-Module $policyModulePath -Force -ErrorAction Stop
$policy = Import-ReleaseSigningPolicy -Path $policyPath
if ($policy.Sha256 -cne (Get-ReleaseTextSha256 -Path $policyPath)) {
    throw 'Signing policy parser and canonical source hash disagree.'
}
$headPolicyBlob = (
    & $gitExecutable -C $repositoryRoot rev-parse 'HEAD:release-signing-policy.json'
).Trim()
if ($LASTEXITCODE -ne 0 -or $policy.SourceGitBlobId -cne $headPolicyBlob) {
    throw 'Signing policy single-read bytes do not exactly match the HEAD blob.'
}

[pscustomobject]@{
    PowerShellVersion = $PSVersionTable.PSVersion.ToString()
    PrivateSigningAcl = 'passed'
    SingleHardLink = 'passed'
    GitHeadContent = 'passed'
    SkipWorktreeRejected = $skipWorktreeRejected
    AssumeUnchangedRejected = $assumeUnchangedRejected
    ExistingReceiptRecovery = 'passed'
    MissingReceiptRecovery = 'passed'
    PolicySingleReadHash = 'passed'
    AuthenticodeTimestampUri = 'passed'
} | ConvertTo-Json -Compress
