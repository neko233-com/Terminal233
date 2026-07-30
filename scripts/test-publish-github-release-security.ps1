[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$publisherPath = Join-Path $PSScriptRoot 'publish-github-release.ps1'
$source = [IO.File]::ReadAllText($publisherPath)
$packagerPath = Join-Path $PSScriptRoot 'package-windows-release.ps1'
$packagerSource = [IO.File]::ReadAllText($packagerPath)
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile(
    $publisherPath,
    [ref]$tokens,
    [ref]$parseErrors
)
if ($parseErrors.Count -ne 0) {
    throw "Publisher AST contains parse errors: $($parseErrors[0].Message)"
}

$functions = @{}
foreach ($functionAst in $ast.FindAll(
    {
        param($node)
        $node -is [Management.Automation.Language.FunctionDefinitionAst]
    },
    $true
)) {
    $functions[$functionAst.Name] = $functionAst
}

function Get-RequiredFunctionText {
    param([Parameter(Mandatory)][string]$Name)

    if (-not $functions.ContainsKey($Name)) {
        throw "Publisher security helper is missing: $Name"
    }
    return [string]$functions[$Name].Extent.Text
}

function Assert-ContainsAll {
    param(
        [Parameter(Mandatory)][string]$Text,
        [Parameter(Mandatory)][string[]]$Fragments,
        [Parameter(Mandatory)][string]$Context
    )

    foreach ($fragment in $Fragments) {
        if (-not $Text.Contains($fragment)) {
            throw "$Context is missing the fail-closed fragment: $fragment"
        }
    }
}

$productionGateIndex = $source.IndexOf(
    'Assert-NekoSshCompleteProductionToolchainCoverage',
    [StringComparison]::Ordinal
)
$firstFunctionIndex = $source.IndexOf('function Test-ChildPath', [StringComparison]::Ordinal)
if (
    $productionGateIndex -lt 0 -or
    $firstFunctionIndex -lt 0 -or
    $productionGateIndex -gt $firstFunctionIndex
) {
    throw 'The incomplete production toolchain gate must remain before publisher function execution.'
}

$packagerSafeDirectoryBindings = [regex]::Matches(
    $packagerSource,
    [regex]::Escape('"safe.directory=$repositoryRoot"')
)
if ($packagerSafeDirectoryBindings.Count -ne 3) {
    throw 'Every packager Git process must bind safe.directory to the exact repository root.'
}

$hardenedGit = Get-RequiredFunctionText -Name 'Invoke-HardenedPublishGit'
Assert-ContainsAll `
    -Text $hardenedGit `
    -Fragments @(
        '$startInfo.FileName = $gitExecutable',
        '$startInfo.WorkingDirectory = $repositoryRoot',
        'core.hooksPath=NUL',
        'core.attributesFile=NUL',
        'core.unsetenvvars=',
        'credential.helper=',
        'fetch.fsckObjects=true',
        'transfer.fsckObjects=true',
        'safe.directory=$repositoryRoot',
        "'^(?i:GIT_)'",
        '$environment.Remove',
        'GIT_CONFIG_NOSYSTEM',
        'GIT_CONFIG_GLOBAL',
        'GIT_CONFIG_COUNT',
        'GIT_NO_REPLACE_OBJECTS',
        'GIT_TERMINAL_PROMPT',
        'GCM_INTERACTIVE'
    ) `
    -Context 'Locked Git process wrapper'

$localConfigGate = Get-RequiredFunctionText -Name 'Assert-NoUnsafePublishGitConfiguration'
Assert-ContainsAll `
    -Text $localConfigGate `
    -Fragments @(
        "'--local'",
        "'--no-includes'",
        'url\..*\.(?:push)?insteadof',
        'credential\.',
        'hook\.',
        'http\.',
        'bundleURI',
        'recentObjectsHook',
        'includeif',
        'askPass',
        'alternateRefsCommand',
        'excludesFile',
        'unsetenvvars',
        'worktreeConfig',
        'partialClone'
    ) `
    -Context 'Local Git configuration gate'

$inputProtection = Get-RequiredFunctionText -Name 'Protect-ReleaseInput'
Assert-ContainsAll `
    -Text $inputProtection `
    -Fragments @(
        '[IO.FileShare]::Read',
        '$releaseInputLocks.Add',
        '$lockedReleaseInputPaths.Add'
    ) `
    -Context 'Release input lifetime lock'
if (
    $source.IndexOf(
        'Protect-ReleaseInput -Path $resolvedSmokeReceipt',
        [StringComparison]::Ordinal
    ) -lt 0 -or
    $source.IndexOf(
        'foreach ($protectedReleaseInput in @($localFiles.Values) + @($notesPath))',
        [StringComparison]::Ordinal
    ) -lt 0
) {
    throw 'Publisher does not lock the signed receipt and every upload input for the full mutation lifetime.'
}

$mainFetch = Get-RequiredFunctionText -Name 'Invoke-HardenedMainFetch'
Assert-ContainsAll `
    -Text $mainFetch `
    -Fragments @(
        '$canonicalRepositoryUrl',
        "'--no-tags'",
        "'--force'",
        '+refs/heads/main:refs/remotes/origin/main'
    ) `
    -Context 'Canonical main fetch'

$programGate = Get-RequiredFunctionText -Name 'Assert-NoConfiguredGitVerifierProgram'
Assert-ContainsAll `
    -Text $programGate `
    -Fragments @(
        '--show-origin',
        '--get-regexp',
        '^gpg\..*program$',
        'AllowedExitCodes @(0, 1)'
    ) `
    -Context 'Git verifier-program configuration gate'

$localTagGate = Get-RequiredFunctionText -Name 'Assert-LocalSignedTagIdentity'
Assert-ContainsAll `
    -Text $localTagGate `
    -Fragments @(
        'ExpectedTagObjectSha',
        'ExpectedSignerFingerprint',
        'Assert-NoConfiguredGitVerifierProgram',
        'gpg.program=gpg.exe',
        'gpg.openpgp.program=gpg.exe',
        'gpg.ssh.program=ssh-keygen.exe',
        "'verify-tag'",
        '$ExpectedTagObjectSha',
        'VALIDSIG',
        'Local tag $TagName changed while its signature was being verified'
    ) `
    -Context 'Local signed-tag identity gate'
if ([regex]::Matches($localTagGate, [regex]::Escape('"refs/tags/$TagName"')).Count -lt 2) {
    throw 'Local signed-tag verification must compare the tag ref both before and after verification.'
}

$githubTagGate = Get-RequiredFunctionText -Name 'Assert-GitHubTagTarget'
Assert-ContainsAll `
    -Text $githubTagGate `
    -Fragments @(
        'ExpectedTagObjectSha',
        '[string]$githubRef.object.sha -cne $ExpectedTagObjectSha',
        '"repos/$RepositoryName/git/tags/$ExpectedTagObjectSha"',
        '$githubTag.object.sha -cne $CommitSha'
    ) `
    -Context 'GitHub exact tag-object gate'

$rulesetGate = Get-RequiredFunctionText -Name 'Assert-GitHubReleaseTagRuleset'
Assert-ContainsAll `
    -Text $rulesetGate `
    -Fragments @(
        'includes_parents=true&targets=tag',
        'refs/tags/v*',
        '$excludes.Count -ne 0',
        '@($detail.bypass_actors).Count -ne 0',
        "'deletion'",
        "'update'",
        "'non_fast_forward'",
        'Missing permissions',
        'fail closed'
    ) `
    -Context 'GitHub release-tag ruleset gate'

$manualState = Get-RequiredFunctionText -Name 'Assert-ManualPublishState'
Assert-ContainsAll `
    -Text $manualState `
    -Fragments @(
        'ExpectedTagObjectSha',
        'ExpectedTagSignerFingerprint',
        'Assert-LocalSignedTagIdentity',
        'Assert-GitHubManualReleaseControls',
        'Assert-GitHubTagTarget'
    ) `
    -Context 'Repeated manual publication state gate'

$releaseMutation = Get-RequiredFunctionText -Name 'Invoke-CheckedGitHubReleaseMutation'
$releaseMutationChecks = [regex]::Matches(
    $releaseMutation,
    '(?m)^\s*Assert-ManualPublishState\s+`'
)
$releaseInvokeIndex = $releaseMutation.IndexOf(
    'Invoke-Checked -FilePath $ghExecutable -ArgumentList $ArgumentList',
    [StringComparison]::Ordinal
)
if (
    $releaseMutationChecks.Count -ne 2 -or
    $releaseInvokeIndex -lt 0 -or
    $releaseMutationChecks[0].Index -gt $releaseInvokeIndex -or
    $releaseMutationChecks[1].Index -lt $releaseInvokeIndex
) {
    throw 'GitHub release mutations must be immediately enclosed by two full publication state gates.'
}
Assert-ContainsAll `
    -Text $releaseMutation `
    -Fragments @(
        "@('create', 'upload', 'edit')",
        'ExpectedTagObjectSha',
        'ExpectedTagSignerFingerprint'
    ) `
    -Context 'GitHub release mutation wrapper'

$tagPushMutation = Get-RequiredFunctionText -Name 'Invoke-CheckedTagPushMutation'
$tagPushIndex = $tagPushMutation.IndexOf("'push'", [StringComparison]::Ordinal)
if (
    $tagPushIndex -lt 0 -or
    $tagPushMutation.IndexOf(
        'Assert-LocalSignedTagIdentity',
        [StringComparison]::Ordinal
    ) -gt $tagPushIndex -or
    $tagPushMutation.IndexOf(
        'Assert-GitHubManualReleaseControls',
        [StringComparison]::Ordinal
    ) -gt $tagPushIndex -or
    $tagPushMutation.IndexOf(
        'Assert-ManualPublishState',
        [StringComparison]::Ordinal
    ) -lt $tagPushIndex
) {
    throw 'The tag push must have local signature/remote controls before it and full state verification after it.'
}
if (-not $tagPushMutation.Contains("'--no-follow-tags'")) {
    throw 'The guarded tag push must explicitly disable push.followTags.'
}
foreach ($requiredPushDisable in @("'--no-verify'", "'--no-signed'", "'--no-push-option'")) {
    if (-not $tagPushMutation.Contains($requiredPushDisable)) {
        throw "The guarded tag push is missing a fail-closed option: $requiredPushDisable"
    }
}

foreach ($forbiddenDirectMutation in @(
    'Invoke-Checked -FilePath $ghExecutable -ArgumentList $createArguments',
    'Invoke-Checked -FilePath $ghExecutable -ArgumentList $publishArguments',
    "Invoke-Checked -FilePath `$gitExecutable -ArgumentList @('push'"
)) {
    if ($source.Contains($forbiddenDirectMutation)) {
        throw "Publisher retained an unguarded mutation path: $forbiddenDirectMutation"
    }
}
if ([regex]::Matches(
    $source,
    '(?m)^\s*Invoke-CheckedGitHubReleaseMutation\s+`'
).Count -ne 3) {
    throw 'Publisher must route exactly create, upload, and public edit through the guarded release wrapper.'
}

$gitExecutable = (
    Get-Command git.exe -CommandType Application -ErrorAction Stop |
        Select-Object -First 1
).Source
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Invoke-Expression (Get-RequiredFunctionText -Name 'ConvertTo-PublishNativeArgument')
Invoke-Expression (Get-RequiredFunctionText -Name 'Invoke-HardenedPublishGit')
Invoke-Expression (Get-RequiredFunctionText -Name 'Assert-NoUnsafePublishGitConfiguration')
$injectedEnvironment = @(
    'GIT_CONFIG_COUNT',
    'GIT_CONFIG_KEY_0',
    'GIT_CONFIG_VALUE_0'
)
$savedEnvironment = @{}
foreach ($name in $injectedEnvironment) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable(
        $name,
        [EnvironmentVariableTarget]::Process
    )
}
try {
    [Environment]::SetEnvironmentVariable(
        'GIT_CONFIG_COUNT',
        '1',
        [EnvironmentVariableTarget]::Process
    )
    [Environment]::SetEnvironmentVariable(
        'GIT_CONFIG_KEY_0',
        'nekossh.releaseSecurityProbe',
        [EnvironmentVariableTarget]::Process
    )
    [Environment]::SetEnvironmentVariable(
        'GIT_CONFIG_VALUE_0',
        'poisoned',
        [EnvironmentVariableTarget]::Process
    )
    $configProbe = Invoke-HardenedPublishGit `
        -ArgumentList @('config', '--get', 'nekossh.releaseSecurityProbe') `
        -AllowedExitCodes @(0, 1)
    if (
        $configProbe.ExitCode -ne 1 -or
        -not [string]::IsNullOrWhiteSpace($configProbe.StdOut) -or
        -not [string]::IsNullOrWhiteSpace($configProbe.StdErr)
    ) {
        throw 'The locked Git process inherited a poisoned GIT_CONFIG_* value.'
    }
}
finally {
    foreach ($name in $injectedEnvironment) {
        [Environment]::SetEnvironmentVariable(
            $name,
            $savedEnvironment[$name],
            [EnvironmentVariableTarget]::Process
        )
    }
}

$originalRepositoryRoot = $repositoryRoot
$unsafeConfigRepository = Join-Path (
    [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
) "nekossh-publisher-config-test-$PID-$([guid]::NewGuid().ToString('N'))"
try {
    & $gitExecutable init --quiet $unsafeConfigRepository
    if ($LASTEXITCODE -ne 0) {
        throw 'Unable to initialize the publisher local-config security fixture.'
    }
    & $gitExecutable -C $unsafeConfigRepository config `
        'url.https://attacker.invalid/.pushInsteadOf' `
        'https://github.com/'
    if ($LASTEXITCODE -ne 0) {
        throw 'Unable to configure the publisher pushInsteadOf security fixture.'
    }
    & $gitExecutable -C $unsafeConfigRepository config core.askPass 'C:\attacker.exe'
    if ($LASTEXITCODE -ne 0) {
        throw 'Unable to configure the publisher askPass security fixture.'
    }
    $repositoryRoot = $unsafeConfigRepository
    $unsafeConfigRejected = $false
    try {
        Assert-NoUnsafePublishGitConfiguration
    }
    catch {
        $unsafeConfigRejected = $_.Exception.Message -like (
            '*rejects unsafe local Git configuration:*'
        )
    }
    if (-not $unsafeConfigRejected) {
        throw 'Publisher accepted local pushInsteadOf/core.askPass execution configuration.'
    }
    & $gitExecutable -C $unsafeConfigRepository config --unset-all `
        'url.https://attacker.invalid/.pushInsteadOf'
    if ($LASTEXITCODE -ne 0) {
        throw 'Unable to clear the publisher pushInsteadOf security fixture.'
    }
    & $gitExecutable -C $unsafeConfigRepository config --unset-all core.askPass
    if ($LASTEXITCODE -ne 0) {
        throw 'Unable to clear the publisher askPass security fixture.'
    }
    & $gitExecutable -C $unsafeConfigRepository config hook.pre-push.event pre-push
    if ($LASTEXITCODE -ne 0) {
        throw 'Unable to configure the publisher config-defined hook fixture.'
    }
    & $gitExecutable -C $unsafeConfigRepository config hook.pre-push.command 'C:\attacker.exe'
    if ($LASTEXITCODE -ne 0) {
        throw 'Unable to configure the publisher hook command fixture.'
    }
    $hookConfigRejected = $false
    try {
        Assert-NoUnsafePublishGitConfiguration
    }
    catch {
        $hookConfigRejected = $_.Exception.Message -like (
            '*rejects unsafe local Git configuration: hook.*'
        )
    }
    if (-not $hookConfigRejected) {
        throw 'Publisher accepted config-defined hook command execution.'
    }
}
finally {
    $repositoryRoot = $originalRepositoryRoot
    if (Test-Path -LiteralPath $unsafeConfigRepository) {
        $resolvedUnsafeConfigRepository = [IO.Path]::GetFullPath($unsafeConfigRepository)
        $temporaryParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/')
        if (-not $resolvedUnsafeConfigRepository.StartsWith(
            "$temporaryParent$([IO.Path]::DirectorySeparatorChar)",
            [StringComparison]::OrdinalIgnoreCase
        )) {
            throw "Refusing to remove publisher test data outside temp: $resolvedUnsafeConfigRepository"
        }
        Remove-Item -LiteralPath $resolvedUnsafeConfigRepository -Recurse -Force
    }
}

[pscustomobject]@{
    Ast = 'passed'
    ProductionNoGo = 'retained'
    GitConfigInjection = 'rejected-static-and-process'
    ScopedSafeDirectory = 'passed'
    UnsafeLocalGitConfig = 'rejected'
    LocalTagObject = 'reverified'
    GitHubTagObject = 'exact'
    TagRuleset = 'required-no-bypass'
    ReleaseMutations = 'guarded-before-after'
    TagPush = 'guarded-before-after'
} | ConvertTo-Json -Compress
