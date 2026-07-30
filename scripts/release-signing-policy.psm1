Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:PolicyTypeName = 'NekoSSH.ReleaseSigningPolicy'
$script:SignerTypeName = 'NekoSSH.ReleaseSigningPolicySigner'
$script:ManifestTypeName = 'NekoSSH.ReleaseManifest'
$script:CodeSigningEkuOid = '1.3.6.1.5.5.7.3.3'
$script:PolicySchemaReference = './release-signing-policy.schema.json'
$script:ExpectedRepository = 'neko233-com/nekossh'
$script:StrictJsonNumberRegex = [regex]::new(
    '\G-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?',
    [Text.RegularExpressions.RegexOptions]::CultureInvariant
)

function Skip-JsonWhitespace {
    param([Parameter(Mandatory)][hashtable]$State)

    while (
        $State.Index -lt $State.Text.Length -and
        $State.Text[$State.Index] -in @([char]0x20, [char]0x09, [char]0x0a, [char]0x0d)
    ) {
        $State.Index += 1
    }
}

function Read-StrictJsonString {
    param([Parameter(Mandatory)][hashtable]$State)

    if ($State.Index -ge $State.Text.Length -or $State.Text[$State.Index] -ne '"') {
        throw "Expected a JSON string at character $($State.Index)."
    }
    $start = $State.Index
    $State.Index += 1
    $closed = $false
    while ($State.Index -lt $State.Text.Length) {
        $character = $State.Text[$State.Index]
        if ([int]$character -lt 0x20) {
            throw "A JSON string contains an unescaped control character at character $($State.Index)."
        }
        if ($character -eq '"') {
            $State.Index += 1
            $closed = $true
            break
        }
        if ($character -eq '\') {
            $State.Index += 1
            if ($State.Index -ge $State.Text.Length) {
                throw 'A JSON string ends in an incomplete escape sequence.'
            }
            $escape = $State.Text[$State.Index]
            if ($escape -eq 'u') {
                if ($State.Index + 4 -ge $State.Text.Length) {
                    throw 'A JSON string contains an incomplete Unicode escape.'
                }
                $hex = $State.Text.Substring($State.Index + 1, 4)
                if ($hex -cnotmatch '^[0-9A-Fa-f]{4}$') {
                    throw "A JSON string contains an invalid Unicode escape at character $($State.Index)."
                }
                $State.Index += 5
                continue
            }
            if ($escape -notin @('"', '\', '/', 'b', 'f', 'n', 'r', 't')) {
                throw "A JSON string contains an invalid escape at character $($State.Index)."
            }
        }
        $State.Index += 1
    }
    if (-not $closed) {
        throw 'A JSON string is unterminated.'
    }

    $token = $State.Text.Substring($start, $State.Index - $start)
    try {
        return [string]($token | ConvertFrom-Json)
    }
    catch {
        throw "Unable to decode a JSON string: $($_.Exception.Message)"
    }
}

function Assert-StrictJsonValue {
    param(
        [Parameter(Mandatory)][hashtable]$State,
        [int]$Depth = 0
    )

    if ($Depth -gt 16) {
        throw 'The JSON document exceeds the maximum nesting depth.'
    }
    Skip-JsonWhitespace -State $State
    if ($State.Index -ge $State.Text.Length) {
        throw 'The JSON document ends before a value is complete.'
    }

    $character = $State.Text[$State.Index]
    if ($character -eq '{') {
        $State.Index += 1
        Skip-JsonWhitespace -State $State
        if ($State.Index -lt $State.Text.Length -and $State.Text[$State.Index] -eq '}') {
            $State.Index += 1
            return
        }
        $exactNames = New-Object 'Collections.Generic.HashSet[string]' (
            [StringComparer]::Ordinal
        )
        $caseInsensitiveNames = New-Object 'Collections.Generic.HashSet[string]' (
            [StringComparer]::OrdinalIgnoreCase
        )
        while ($true) {
            Skip-JsonWhitespace -State $State
            $name = Read-StrictJsonString -State $State
            if (-not $exactNames.Add($name)) {
                throw "The JSON document contains a duplicate property: '$name'."
            }
            if (-not $caseInsensitiveNames.Add($name)) {
                throw "The JSON document contains ambiguous property casing: '$name'."
            }
            Skip-JsonWhitespace -State $State
            if ($State.Index -ge $State.Text.Length -or $State.Text[$State.Index] -ne ':') {
                throw "Expected ':' after JSON property '$name'."
            }
            $State.Index += 1
            Assert-StrictJsonValue -State $State -Depth ($Depth + 1)
            Skip-JsonWhitespace -State $State
            if ($State.Index -ge $State.Text.Length) {
                throw 'The JSON document contains an unterminated object.'
            }
            if ($State.Text[$State.Index] -eq '}') {
                $State.Index += 1
                return
            }
            if ($State.Text[$State.Index] -ne ',') {
                throw "Expected ',' or '}' at character $($State.Index)."
            }
            $State.Index += 1
        }
    }

    if ($character -eq '[') {
        $State.Index += 1
        Skip-JsonWhitespace -State $State
        if ($State.Index -lt $State.Text.Length -and $State.Text[$State.Index] -eq ']') {
            $State.Index += 1
            return
        }
        while ($true) {
            Assert-StrictJsonValue -State $State -Depth ($Depth + 1)
            Skip-JsonWhitespace -State $State
            if ($State.Index -ge $State.Text.Length) {
                throw 'The JSON document contains an unterminated array.'
            }
            if ($State.Text[$State.Index] -eq ']') {
                $State.Index += 1
                return
            }
            if ($State.Text[$State.Index] -ne ',') {
                throw "Expected ',' or ']' at character $($State.Index)."
            }
            $State.Index += 1
        }
    }

    if ($character -eq '"') {
        [void](Read-StrictJsonString -State $State)
        return
    }

    foreach ($literal in @('true', 'false', 'null')) {
        if (
            $State.Text.Length - $State.Index -ge $literal.Length -and
            [string]::CompareOrdinal(
                $State.Text,
                $State.Index,
                $literal,
                0,
                $literal.Length
            ) -eq 0
        ) {
            $State.Index += $literal.Length
            return
        }
    }
    $number = $script:StrictJsonNumberRegex.Match($State.Text, $State.Index)
    if ($number.Success) {
        $State.Index += $number.Length
        return
    }
    throw "Invalid JSON value at character $($State.Index)."
}

function Assert-StrictJsonDocument {
    param([Parameter(Mandatory)][string]$Text)

    $state = @{
        Text = $Text
        Index = 0
    }
    if ($Text.Length -gt 0 -and $Text[0] -eq [char]0xfeff) {
        $state.Index = 1
    }
    Assert-StrictJsonValue -State $state
    Skip-JsonWhitespace -State $state
    if ($state.Index -ne $Text.Length) {
        throw "The JSON document has trailing content at character $($state.Index)."
    }
}

function Assert-ExactJsonFields {
    param(
        [Parameter(Mandatory)]$Object,
        [Parameter(Mandatory)][string[]]$Allowed,
        [Parameter(Mandatory)][string[]]$Required,
        [Parameter(Mandatory)][string]$Context
    )

    if ($null -eq $Object -or $Object -isnot [pscustomobject]) {
        throw "$Context must be a JSON object."
    }
    $names = @($Object.PSObject.Properties.Name)
    foreach ($name in $names) {
        if ($Allowed -cnotcontains $name) {
            throw "$Context contains an unsupported field: '$name'."
        }
    }
    foreach ($name in $Required) {
        if ($names -cnotcontains $name) {
            throw "$Context is missing required field '$name'."
        }
    }
}

function ConvertTo-StrictReleaseVersion {
    param(
        [Parameter(Mandatory)][string]$Value,
        [Parameter(Mandatory)][string]$Context
    )

    if ($Value -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
        throw "$Context must be a three-component stable semantic version."
    }
    $parts = $Value.Split('.')
    foreach ($part in $parts) {
        $component = 0
        if (-not [int]::TryParse($part, [Globalization.NumberStyles]::None, [Globalization.CultureInfo]::InvariantCulture, [ref]$component)) {
            throw "$Context contains a version component larger than Int32."
        }
    }
    return [version]::Parse($Value)
}

function ConvertTo-SigningPolicyVersion {
    param(
        [Parameter(Mandatory)][string]$Value,
        [Parameter(Mandatory)][string]$Context
    )

    $match = [regex]::Match(
        $Value,
        '^(?<base>(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*))(?:-(?:(?:0|[1-9][0-9]*|[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[A-Za-z-][0-9A-Za-z-]*))*))?$',
        [Text.RegularExpressions.RegexOptions]::CultureInvariant
    )
    if (-not $match.Success) {
        throw "$Context must be a three-component semantic version with an optional prerelease suffix."
    }
    return ConvertTo-StrictReleaseVersion -Value $match.Groups['base'].Value -Context $Context
}

function ConvertTo-CanonicalThumbprint {
    param(
        [Parameter(Mandatory)][string]$Value,
        [Parameter(Mandatory)][string]$Context
    )

    if ($Value -cnotmatch '^[0-9A-Fa-f]{40}$') {
        throw "$Context must be exactly 40 hexadecimal characters."
    }
    return $Value.ToUpperInvariant()
}

function Get-CanonicalTextSha256Value {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Text)

    $utf8 = [Text.UTF8Encoding]::new($false, $true)
    $canonicalText = $Text.Replace("`r`n", "`n").Replace("`r", "`n")
    [byte[]]$canonicalBytes = $utf8.GetBytes($canonicalText)
    $sha256 = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($sha256.ComputeHash($canonicalBytes))).Replace('-', '').ToLowerInvariant()
    }
    finally {
        $sha256.Dispose()
    }
}

function Get-ReleaseTextSha256 {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Path)

    $resolvedPath = (Resolve-Path -LiteralPath $Path).Path
    $file = Get-Item -LiteralPath $resolvedPath -Force
    if ($file.PSIsContainer -or ($file.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw 'Release source text must be a regular file, not a directory or reparse point.'
    }
    [byte[]]$bytes = [IO.File]::ReadAllBytes($resolvedPath)
    $offset = 0
    if (
        $bytes.Length -ge 3 -and
        $bytes[0] -eq 0xef -and
        $bytes[1] -eq 0xbb -and
        $bytes[2] -eq 0xbf
    ) {
        $offset = 3
    }
    $utf8 = [Text.UTF8Encoding]::new($false, $true)
    try {
        $text = $utf8.GetString($bytes, $offset, $bytes.Length - $offset)
    }
    catch {
        throw "Release source text is not valid UTF-8: $($_.Exception.Message)"
    }
    return Get-CanonicalTextSha256Value -Text $text
}

function Assert-ReleaseManifestStringFields {
    param(
        [Parameter(Mandatory)]$Object,
        [Parameter(Mandatory)][string[]]$Fields,
        [Parameter(Mandatory)][string]$Context
    )

    foreach ($field in $Fields) {
        if ($Object.PSObject.Properties[$field].Value -isnot [string]) {
            throw "$Context.$field must be a JSON string."
        }
    }
}

function Assert-ReleaseManifestNonEmptyStringFields {
    param(
        [Parameter(Mandatory)]$Object,
        [Parameter(Mandatory)][string[]]$Fields,
        [Parameter(Mandatory)][string]$Context
    )

    Assert-ReleaseManifestStringFields -Object $Object -Fields $Fields -Context $Context
    foreach ($field in $Fields) {
        if ($Object.PSObject.Properties[$field].Value.Length -eq 0) {
            throw "$Context.$field must not be empty."
        }
    }
}

function Assert-LowercaseSha256 {
    param(
        [Parameter(Mandatory)][string]$Value,
        [Parameter(Mandatory)][string]$Context
    )

    if ($Value -cnotmatch '^[0-9a-f]{64}$') {
        throw "$Context must be a lowercase SHA-256 digest."
    }
}

function Assert-OptionalManifestString {
    param(
        $Value,
        [Parameter(Mandatory)][string]$Context,
        [string]$Pattern,
        [switch]$NonEmpty
    )

    if ($null -eq $Value) {
        return
    }
    if ($Value -isnot [string]) {
        throw "$Context must be null or a JSON string."
    }
    if ($NonEmpty -and $Value.Length -eq 0) {
        throw "$Context must not be empty when present."
    }
    if (-not [string]::IsNullOrEmpty($Pattern) -and $Value -cnotmatch $Pattern) {
        throw "$Context has an invalid format."
    }
}

function Assert-ReleaseManifestFileRecord {
    param(
        [Parameter(Mandatory)]$Record,
        [Parameter(Mandatory)][string]$Context,
        [switch]$IncludeKind
    )

    $fields = @('name', 'bytes', 'sha256')
    if ($IncludeKind) { $fields += 'kind' }
    Assert-ExactJsonFields -Object $Record -Allowed $fields -Required $fields -Context $Context
    $stringFields = @('name', 'sha256')
    if ($IncludeKind) { $stringFields += 'kind' }
    Assert-ReleaseManifestNonEmptyStringFields -Object $Record -Fields $stringFields -Context $Context
    if (
        -not ($Record.bytes -is [int] -or $Record.bytes -is [long]) -or
        [long]$Record.bytes -le 0
    ) {
        throw "$Context.bytes must be a positive JSON integer."
    }
    if (
        [IO.Path]::IsPathRooted($Record.name) -or
        [IO.Path]::GetFileName($Record.name) -cne $Record.name -or
        $Record.name -in @('.', '..')
    ) {
        throw "$Context.name must be a single relative file name."
    }
    Assert-LowercaseSha256 -Value $Record.sha256 -Context "$Context.sha256"
    if (
        $IncludeKind -and
        $Record.kind -cnotin @(
            'tauri-msi',
            'tauri-nsis',
            'web-portable',
            'third-party-notices'
        )
    ) {
        throw "$Context.kind is unsupported."
    }
}

function Import-NekoSshReleaseManifest {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Path)

    $resolvedPath = (Resolve-Path -LiteralPath $Path).Path
    $file = Get-Item -LiteralPath $resolvedPath -Force
    if ($file.PSIsContainer -or ($file.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw 'The release manifest must be a regular file, not a directory or reparse point.'
    }
    if ($file.Length -le 0 -or $file.Length -gt 256KB) {
        throw 'The release manifest must be between 1 byte and 256 KiB.'
    }
    [byte[]]$bytes = [IO.File]::ReadAllBytes($resolvedPath)
    if (
        $bytes.Length -le 0 -or
        $bytes.Length -gt 256KB -or
        [long]$bytes.Length -ne [long]$file.Length
    ) {
        throw 'The release manifest changed size while it was being read.'
    }
    $sourceSha256 = [Security.Cryptography.SHA256]::Create()
    try {
        $sourceFileSha256 = (
            [BitConverter]::ToString($sourceSha256.ComputeHash($bytes))
        ).Replace('-', '').ToLowerInvariant()
    }
    finally {
        $sourceSha256.Dispose()
    }
    $offset = 0
    if (
        $bytes.Length -ge 3 -and
        $bytes[0] -eq 0xef -and
        $bytes[1] -eq 0xbb -and
        $bytes[2] -eq 0xbf
    ) {
        $offset = 3
    }
    $utf8 = [Text.UTF8Encoding]::new($false, $true)
    try {
        $text = $utf8.GetString($bytes, $offset, $bytes.Length - $offset)
    }
    catch {
        throw "The release manifest is not valid UTF-8: $($_.Exception.Message)"
    }
    Assert-StrictJsonDocument -Text $text
    try {
        $manifest = $text | ConvertFrom-Json
    }
    catch {
        throw "Unable to parse the release manifest JSON: $($_.Exception.Message)"
    }

    $topLevelFields = @(
        'schemaVersion',
        'repository',
        'version',
        'tag',
        'commit',
        'shortCommit',
        'prerelease',
        'publishable',
        'generatedAtUtc',
        'toolchain',
        'signing',
        'assets',
        'webRuntime',
        'windowsInstaller',
        'checksumFile',
        'releaseNotesFile',
        'signatures'
    )
    Assert-ExactJsonFields `
        -Object $manifest `
        -Allowed $topLevelFields `
        -Required $topLevelFields `
        -Context 'Release manifest'
    Assert-ReleaseManifestNonEmptyStringFields `
        -Object $manifest `
        -Fields @('repository', 'version', 'tag', 'commit', 'shortCommit', 'generatedAtUtc') `
        -Context 'Release manifest'
    if (
        -not ($manifest.schemaVersion -is [int] -or $manifest.schemaVersion -is [long]) -or
        [long]$manifest.schemaVersion -ne 3 -or
        $manifest.prerelease -isnot [bool] -or
        $manifest.publishable -isnot [bool]
    ) {
        throw 'Release manifest schemaVersion/prerelease/publishable types are invalid.'
    }
    [void](ConvertTo-SigningPolicyVersion `
        -Value $manifest.version `
        -Context 'Release manifest version')
    $versionIsPrerelease = $manifest.version.Contains('-')
    $generatedAt = [DateTimeOffset]::MinValue
    if (
        $manifest.repository -cne $script:ExpectedRepository -or
        $manifest.tag -cne "v$($manifest.version)" -or
        $manifest.commit -cnotmatch '^[0-9a-f]{40}$' -or
        $manifest.shortCommit -cnotmatch '^[0-9a-f]{12}$' -or
        $manifest.shortCommit -cne $manifest.commit.Substring(0, 12) -or
        $manifest.prerelease -ne $versionIsPrerelease -or
        $manifest.generatedAtUtc -cnotmatch (
            '^[0-9]{4}-[0-9]{2}-[0-9]{2}T' +
            '[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,7})?Z$'
        ) -or
        -not [DateTimeOffset]::TryParse(
            $manifest.generatedAtUtc,
            [Globalization.CultureInfo]::InvariantCulture,
            [Globalization.DateTimeStyles]::AssumeUniversal,
            [ref]$generatedAt
        )
    ) {
        throw 'Release manifest identity, commit, prerelease, or generatedAtUtc is invalid.'
    }

    $toolchainFields = @('node', 'npm', 'rustc', 'cargo', 'cargoAudit', 'target')
    Assert-ExactJsonFields `
        -Object $manifest.toolchain `
        -Allowed $toolchainFields `
        -Required $toolchainFields `
        -Context 'Release manifest toolchain'
    Assert-ReleaseManifestNonEmptyStringFields `
        -Object $manifest.toolchain `
        -Fields $toolchainFields `
        -Context 'Release manifest toolchain'
    if ($manifest.toolchain.target -cne 'x86_64-pc-windows-msvc') {
        throw 'Release manifest toolchain.target is unsupported.'
    }

    $signingFields = @(
        'mode',
        'expectedSignerThumbprint',
        'expectedSignerSubject',
        'expectedSignerSpkiSha256',
        'policySha256',
        'timestampRequired'
    )
    Assert-ExactJsonFields `
        -Object $manifest.signing `
        -Allowed $signingFields `
        -Required $signingFields `
        -Context 'Release manifest signing'
    Assert-ReleaseManifestNonEmptyStringFields `
        -Object $manifest.signing `
        -Fields @('mode', 'policySha256') `
        -Context 'Release manifest signing'
    Assert-OptionalManifestString `
        -Value $manifest.signing.expectedSignerThumbprint `
        -Context 'Release manifest signing.expectedSignerThumbprint' `
        -Pattern '^[0-9A-F]{40}$' `
        -NonEmpty
    Assert-OptionalManifestString `
        -Value $manifest.signing.expectedSignerSubject `
        -Context 'Release manifest signing.expectedSignerSubject' `
        -NonEmpty
    Assert-OptionalManifestString `
        -Value $manifest.signing.expectedSignerSpkiSha256 `
        -Context 'Release manifest signing.expectedSignerSpkiSha256' `
        -Pattern '^[0-9a-f]{64}$' `
        -NonEmpty
    if ($manifest.signing.timestampRequired -isnot [bool]) {
        throw 'Release manifest signing.timestampRequired must be a JSON boolean.'
    }
    Assert-LowercaseSha256 `
        -Value $manifest.signing.policySha256 `
        -Context 'Release manifest signing.policySha256'
    if ($manifest.signing.mode -cnotin @('authenticode', 'unsigned-test')) {
        throw 'Release manifest signing.mode is unsupported.'
    }
    $isAuthenticode = $manifest.signing.mode -ceq 'authenticode'
    if (
        $isAuthenticode -and (
            $null -eq $manifest.signing.expectedSignerThumbprint -or
            $null -eq $manifest.signing.expectedSignerSubject -or
            $null -eq $manifest.signing.expectedSignerSpkiSha256 -or
            $manifest.signing.timestampRequired -ne $true
        )
    ) {
        throw 'Authenticode release manifests require a complete signer identity and timestamp.'
    }
    if (
        -not $isAuthenticode -and (
            $null -ne $manifest.signing.expectedSignerThumbprint -or
            $null -ne $manifest.signing.expectedSignerSubject -or
            $null -ne $manifest.signing.expectedSignerSpkiSha256 -or
            $manifest.signing.timestampRequired -ne $false
        )
    ) {
        throw 'Unsigned test release manifests must not claim a signer or timestamp.'
    }
    $expectedPublishable = $isAuthenticode -and -not $manifest.prerelease
    if ($manifest.publishable -ne $expectedPublishable) {
        throw 'Release manifest publishable does not match signing mode and prerelease state.'
    }

    if ($manifest.assets -isnot [Array] -or $manifest.assets.Count -ne 4) {
        throw 'Release manifest assets must be a four-item JSON array.'
    }
    $assetNames = New-Object 'Collections.Generic.HashSet[string]' (
        [StringComparer]::OrdinalIgnoreCase
    )
    $assetKinds = New-Object 'Collections.Generic.HashSet[string]' (
        [StringComparer]::Ordinal
    )
    for ($index = 0; $index -lt $manifest.assets.Count; $index += 1) {
        Assert-ReleaseManifestFileRecord `
            -Record $manifest.assets[$index] `
            -Context "Release manifest assets[$index]" `
            -IncludeKind
        if (-not $assetNames.Add($manifest.assets[$index].name)) {
            throw 'Release manifest asset names must be unique on Windows.'
        }
        if (-not $assetKinds.Add($manifest.assets[$index].kind)) {
            throw 'Release manifest asset kinds must be unique.'
        }
    }
    $expectedAssetKinds = @(
        'tauri-msi',
        'tauri-nsis',
        'third-party-notices',
        'web-portable'
    )
    if (
        (@($assetKinds | Sort-Object) -join "`n") -cne
        ($expectedAssetKinds -join "`n")
    ) {
        throw 'Release manifest does not contain the exact required asset kind set.'
    }
    Assert-ReleaseManifestFileRecord `
        -Record $manifest.checksumFile `
        -Context 'Release manifest checksumFile'
    Assert-ReleaseManifestFileRecord `
        -Record $manifest.releaseNotesFile `
        -Context 'Release manifest releaseNotesFile'
    if (
        $manifest.checksumFile.name -cne 'SHA256SUMS.txt' -or
        $manifest.releaseNotesFile.name -cne 'RELEASE_NOTES.md'
    ) {
        throw 'Release manifest metadata file names are invalid.'
    }

    $installerFields = @(
        'productCode',
        'productName',
        'manufacturer',
        'productLanguage',
        'productVersion',
        'upgradeCode'
    )
    Assert-ExactJsonFields `
        -Object $manifest.windowsInstaller `
        -Allowed $installerFields `
        -Required $installerFields `
        -Context 'Release manifest windowsInstaller'
    Assert-ReleaseManifestNonEmptyStringFields `
        -Object $manifest.windowsInstaller `
        -Fields $installerFields `
        -Context 'Release manifest windowsInstaller'
    if (
        $manifest.windowsInstaller.productCode -cnotmatch (
            '^\{[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-' +
            '[0-9A-F]{4}-[0-9A-F]{12}\}$'
        ) -or
        $manifest.windowsInstaller.productName -cne 'NekoSSH' -or
        $manifest.windowsInstaller.manufacturer -cne 'NekoSSH contributors' -or
        $manifest.windowsInstaller.productLanguage -cne '2052' -or
        $manifest.windowsInstaller.productVersion -cne $manifest.version -or
        $manifest.windowsInstaller.upgradeCode -cne (
            '{44F1588E-BCFD-5973-A955-237DAF92726D}'
        )
    ) {
        throw 'Release manifest windowsInstaller identity is invalid.'
    }

    $webFields = @('archiveSha256', 'version', 'launcher', 'node', 'selfTest')
    Assert-ExactJsonFields `
        -Object $manifest.webRuntime `
        -Allowed $webFields `
        -Required $webFields `
        -Context 'Release manifest webRuntime'
    Assert-ReleaseManifestNonEmptyStringFields `
        -Object $manifest.webRuntime `
        -Fields @('archiveSha256', 'version', 'selfTest') `
        -Context 'Release manifest webRuntime'
    Assert-LowercaseSha256 `
        -Value $manifest.webRuntime.archiveSha256 `
        -Context 'Release manifest webRuntime.archiveSha256'
    $webAsset = @(
        $manifest.assets |
            Where-Object { $_.kind -ceq 'web-portable' }
    )[0]
    $expectedWebSelfTest = if ($isAuthenticode) { 'passed' } else { 'skipped-unsigned' }
    if (
        $manifest.webRuntime.archiveSha256 -cne $webAsset.sha256 -or
        $manifest.webRuntime.version -cne $manifest.version -or
        $manifest.webRuntime.selfTest -cne $expectedWebSelfTest
    ) {
        throw 'Release manifest webRuntime is not bound to its asset, version, and signing mode.'
    }
    $launcherFields = @(
        'status',
        'signerThumbprint',
        'signerSubject',
        'signerSpkiSha256',
        'timestamped'
    )
    Assert-ExactJsonFields `
        -Object $manifest.webRuntime.launcher `
        -Allowed $launcherFields `
        -Required $launcherFields `
        -Context 'Release manifest webRuntime.launcher'
    Assert-ReleaseManifestNonEmptyStringFields `
        -Object $manifest.webRuntime.launcher `
        -Fields @('status') `
        -Context 'Release manifest webRuntime.launcher'
    Assert-OptionalManifestString `
        -Value $manifest.webRuntime.launcher.signerThumbprint `
        -Context 'Release manifest webRuntime.launcher.signerThumbprint' `
        -Pattern '^[0-9A-F]{40}$' `
        -NonEmpty
    Assert-OptionalManifestString `
        -Value $manifest.webRuntime.launcher.signerSubject `
        -Context 'Release manifest webRuntime.launcher.signerSubject' `
        -NonEmpty
    Assert-OptionalManifestString `
        -Value $manifest.webRuntime.launcher.signerSpkiSha256 `
        -Context 'Release manifest webRuntime.launcher.signerSpkiSha256' `
        -Pattern '^[0-9a-f]{64}$' `
        -NonEmpty
    if ($manifest.webRuntime.launcher.timestamped -isnot [bool]) {
        throw 'Release manifest webRuntime.launcher.timestamped must be a JSON boolean.'
    }
    $expectedLauncherStatus = if ($isAuthenticode) { 'Valid' } else { 'NotSigned' }
    if (
        $manifest.webRuntime.launcher.status -cne $expectedLauncherStatus -or
        $manifest.webRuntime.launcher.signerThumbprint -cne (
            $manifest.signing.expectedSignerThumbprint
        ) -or
        $manifest.webRuntime.launcher.signerSubject -cne (
            $manifest.signing.expectedSignerSubject
        ) -or
        $manifest.webRuntime.launcher.signerSpkiSha256 -cne (
            $manifest.signing.expectedSignerSpkiSha256
        ) -or
        $manifest.webRuntime.launcher.timestamped -ne $isAuthenticode
    ) {
        throw 'Release manifest Web launcher signature does not match the signing policy.'
    }
    $nodeFields = @('version', 'sha256', 'status', 'signerSubject', 'timestamped')
    Assert-ExactJsonFields `
        -Object $manifest.webRuntime.node `
        -Allowed $nodeFields `
        -Required $nodeFields `
        -Context 'Release manifest webRuntime.node'
    Assert-ReleaseManifestNonEmptyStringFields `
        -Object $manifest.webRuntime.node `
        -Fields @('version', 'sha256', 'status', 'signerSubject') `
        -Context 'Release manifest webRuntime.node'
    if ($manifest.webRuntime.node.timestamped -isnot [bool]) {
        throw 'Release manifest webRuntime.node.timestamped must be a JSON boolean.'
    }
    Assert-LowercaseSha256 `
        -Value $manifest.webRuntime.node.sha256 `
        -Context 'Release manifest webRuntime.node.sha256'
    if (
        $manifest.webRuntime.node.version -cne $manifest.toolchain.node.TrimStart('v') -or
        $manifest.webRuntime.node.status -cne 'Valid' -or
        $manifest.webRuntime.node.timestamped -ne $true
    ) {
        throw 'Release manifest embedded Node runtime identity is invalid.'
    }

    if ($manifest.signatures -isnot [Array] -or $manifest.signatures.Count -ne 3) {
        throw 'Release manifest signatures must be a three-item JSON array.'
    }
    $signatureFields = @(
        'Name',
        'Status',
        'SignerThumbprint',
        'SignerSubject',
        'SignerSpkiSha256',
        'Timestamped'
    )
    $signatureNames = New-Object 'Collections.Generic.HashSet[string]' (
        [StringComparer]::OrdinalIgnoreCase
    )
    for ($index = 0; $index -lt $manifest.signatures.Count; $index += 1) {
        $record = $manifest.signatures[$index]
        Assert-ExactJsonFields `
            -Object $record `
            -Allowed $signatureFields `
            -Required $signatureFields `
            -Context "Release manifest signatures[$index]"
        Assert-ReleaseManifestNonEmptyStringFields `
            -Object $record `
            -Fields @('Name', 'Status') `
            -Context "Release manifest signatures[$index]"
        Assert-OptionalManifestString `
            -Value $record.SignerThumbprint `
            -Context "Release manifest signatures[$index].SignerThumbprint" `
            -Pattern '^[0-9A-F]{40}$' `
            -NonEmpty
        Assert-OptionalManifestString `
            -Value $record.SignerSubject `
            -Context "Release manifest signatures[$index].SignerSubject" `
            -NonEmpty
        Assert-OptionalManifestString `
            -Value $record.SignerSpkiSha256 `
            -Context "Release manifest signatures[$index].SignerSpkiSha256" `
            -Pattern '^[0-9a-f]{64}$' `
            -NonEmpty
        if ($record.Timestamped -isnot [bool]) {
            throw "Release manifest signatures[$index].Timestamped must be a JSON boolean."
        }
        if (
            [IO.Path]::IsPathRooted($record.Name) -or
            [IO.Path]::GetFileName($record.Name) -cne $record.Name -or
            -not $signatureNames.Add($record.Name)
        ) {
            throw 'Release manifest signature names must be unique relative file names.'
        }
        if (
            $record.Status -cne $expectedLauncherStatus -or
            $record.SignerThumbprint -cne $manifest.signing.expectedSignerThumbprint -or
            $record.SignerSubject -cne $manifest.signing.expectedSignerSubject -or
            $record.SignerSpkiSha256 -cne $manifest.signing.expectedSignerSpkiSha256 -or
            $record.Timestamped -ne $isAuthenticode
        ) {
            throw "Release manifest signatures[$index] does not match the signing policy."
        }
    }
    $msiAsset = @($manifest.assets | Where-Object { $_.kind -ceq 'tauri-msi' })[0]
    $nsisAsset = @($manifest.assets | Where-Object { $_.kind -ceq 'tauri-nsis' })[0]
    $expectedSignatureNames = @(
        $msiAsset.name,
        $nsisAsset.name,
        'nekossh.exe'
    ) | Sort-Object
    if (
        (@($signatureNames | Sort-Object) -join "`n") -cne
        ($expectedSignatureNames -join "`n")
    ) {
        throw 'Release manifest does not contain the exact required signature record set.'
    }
    $launcherSignature = @(
        $manifest.signatures |
            Where-Object { $_.Name -ceq 'nekossh.exe' }
    )[0]
    if (
        $manifest.webRuntime.launcher.status -cne $launcherSignature.Status -or
        $manifest.webRuntime.launcher.signerThumbprint -cne (
            $launcherSignature.SignerThumbprint
        ) -or
        $manifest.webRuntime.launcher.signerSubject -cne (
            $launcherSignature.SignerSubject
        ) -or
        $manifest.webRuntime.launcher.signerSpkiSha256 -cne (
            $launcherSignature.SignerSpkiSha256
        ) -or
        $manifest.webRuntime.launcher.timestamped -ne $launcherSignature.Timestamped
    ) {
        throw 'Release manifest Web launcher signature record is inconsistent.'
    }

    $manifest.PSObject.TypeNames.Insert(0, $script:ManifestTypeName)
    $manifest | Add-Member -NotePropertyName SourceFileSha256 -NotePropertyValue $sourceFileSha256
    $manifest | Add-Member -NotePropertyName SourceFileLength -NotePropertyValue ([long]$bytes.Length)
    return $manifest
}

function Import-ReleaseSigningPolicy {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Path)

    $resolvedPath = (Resolve-Path -LiteralPath $Path).Path
    $file = Get-Item -LiteralPath $resolvedPath -Force
    if ($file.PSIsContainer -or ($file.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw 'The release signing policy must be a regular file, not a directory or reparse point.'
    }
    if ($file.Length -le 0 -or $file.Length -gt 65536) {
        throw 'The release signing policy must be between 1 byte and 64 KiB.'
    }
    $utf8 = [Text.UTF8Encoding]::new($false, $true)
    try {
        [byte[]]$bytes = [IO.File]::ReadAllBytes($resolvedPath)
        if ($bytes.Length -le 0 -or $bytes.Length -gt 65536) {
            throw 'The release signing policy changed size while it was being read.'
        }
        $offset = 0
        if (
            $bytes.Length -ge 3 -and
            $bytes[0] -eq 0xef -and
            $bytes[1] -eq 0xbb -and
            $bytes[2] -eq 0xbf
        ) {
            $offset = 3
        }
        $text = $utf8.GetString($bytes, $offset, $bytes.Length - $offset)
    }
    catch {
        throw "The release signing policy is not valid UTF-8: $($_.Exception.Message)"
    }
    Assert-StrictJsonDocument -Text $text
    try {
        $json = $text | ConvertFrom-Json
    }
    catch {
        throw "Unable to parse the release signing policy JSON: $($_.Exception.Message)"
    }

    Assert-ExactJsonFields `
        -Object $json `
        -Allowed @('$schema', 'schemaVersion', 'repository', 'windowsAuthenticode') `
        -Required @('$schema', 'schemaVersion', 'repository', 'windowsAuthenticode') `
        -Context 'Release signing policy'
    if ([string]$json.'$schema' -cne $script:PolicySchemaReference) {
        throw "Release signing policy '`$schema' must be '$script:PolicySchemaReference'."
    }
    if (
        -not ($json.schemaVersion -is [int] -or $json.schemaVersion -is [long]) -or
        [long]$json.schemaVersion -ne 1
    ) {
        throw 'Release signing policy schemaVersion must be the integer 1.'
    }
    if ($json.repository -isnot [string] -or [string]$json.repository -cne $script:ExpectedRepository) {
        throw "Release signing policy repository must be '$script:ExpectedRepository'."
    }

    Assert-ExactJsonFields `
        -Object $json.windowsAuthenticode `
        -Allowed @('allowedSigners') `
        -Required @('allowedSigners') `
        -Context 'windowsAuthenticode'
    if ($json.windowsAuthenticode.allowedSigners -isnot [Array]) {
        throw 'windowsAuthenticode.allowedSigners must be a JSON array.'
    }
    $sourceSigners = @($json.windowsAuthenticode.allowedSigners)
    if ($sourceSigners.Count -gt 32) {
        throw 'windowsAuthenticode.allowedSigners may contain at most 32 entries.'
    }

    $seenThumbprints = New-Object 'Collections.Generic.HashSet[string]' (
        [StringComparer]::Ordinal
    )
    $normalizedSigners = [Collections.Generic.List[object]]::new()
    for ($index = 0; $index -lt $sourceSigners.Count; $index += 1) {
        $entry = $sourceSigners[$index]
        $context = "windowsAuthenticode.allowedSigners[$index]"
        Assert-ExactJsonFields `
            -Object $entry `
            -Allowed @('thumbprint', 'subject', 'spkiSha256', 'validFromVersion', 'validThroughVersion') `
            -Required @('thumbprint', 'subject', 'spkiSha256', 'validFromVersion', 'validThroughVersion') `
            -Context $context
        foreach ($field in @('thumbprint', 'subject', 'spkiSha256', 'validFromVersion', 'validThroughVersion')) {
            if ($entry.$field -isnot [string]) {
                throw "$context.$field must be a JSON string."
            }
        }
        if ([string]$entry.thumbprint -cnotmatch '^[0-9A-F]{40}$') {
            throw "$context.thumbprint must be a canonical uppercase certificate thumbprint."
        }
        $thumbprint = ConvertTo-CanonicalThumbprint -Value $entry.thumbprint -Context "$context.thumbprint"
        if (-not $seenThumbprints.Add($thumbprint)) {
            throw "windowsAuthenticode.allowedSigners contains duplicate thumbprint '$thumbprint'."
        }
        $subject = [string]$entry.subject
        if (
            [string]::IsNullOrWhiteSpace($subject) -or
            $subject.Length -gt 1024 -or
            $subject -match '[\x00-\x1F\x7F]'
        ) {
            throw "$context.subject must be a non-empty certificate subject without control characters."
        }
        $spkiSha256 = [string]$entry.spkiSha256
        if ($spkiSha256 -cnotmatch '^[0-9a-f]{64}$') {
            throw "$context.spkiSha256 must be a canonical lowercase SHA-256 value."
        }
        $validFrom = ConvertTo-StrictReleaseVersion `
            -Value $entry.validFromVersion `
            -Context "$context.validFromVersion"
        $validThrough = ConvertTo-StrictReleaseVersion `
            -Value $entry.validThroughVersion `
            -Context "$context.validThroughVersion"
        if ($validFrom -gt $validThrough) {
            throw "$context has an inverted release version range."
        }

        $signer = [pscustomobject]@{
            Thumbprint = $thumbprint
            Subject = $subject
            SpkiSha256 = $spkiSha256
            ValidFromVersion = [string]$entry.validFromVersion
            ValidThroughVersion = [string]$entry.validThroughVersion
            ValidFrom = $validFrom
            ValidThrough = $validThrough
        }
        $signer.PSObject.TypeNames.Insert(0, $script:SignerTypeName)
        $normalizedSigners.Add($signer)
    }

    $policy = [pscustomobject]@{
        Path = $resolvedPath
        Sha256 = Get-CanonicalTextSha256Value -Text $text
        SchemaVersion = 1
        Repository = $script:ExpectedRepository
        WindowsAuthenticode = [pscustomobject]@{
            AllowedSigners = [object[]]$normalizedSigners.ToArray()
        }
    }
    $policy.PSObject.TypeNames.Insert(0, $script:PolicyTypeName)
    return $policy
}

function Get-ReleaseSigningPolicySigner {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]$Policy,
        [Parameter(Mandatory)][string]$Version,
        [Parameter(Mandatory)][string]$Thumbprint
    )

    if ($Policy.PSObject.TypeNames -cnotcontains $script:PolicyTypeName) {
        throw 'Policy must be returned by Import-ReleaseSigningPolicy.'
    }
    $releaseVersion = ConvertTo-SigningPolicyVersion -Value $Version -Context 'Release version'
    $canonicalThumbprint = ConvertTo-CanonicalThumbprint -Value $Thumbprint -Context 'Signer thumbprint'
    $matchingThumbprints = @(
        $Policy.WindowsAuthenticode.AllowedSigners |
            Where-Object { $_.Thumbprint -ceq $canonicalThumbprint }
    )
    if ($matchingThumbprints.Count -eq 0) {
        throw "Thumbprint '$canonicalThumbprint' is not allowed by the release signing policy."
    }
    if ($matchingThumbprints.Count -ne 1) {
        throw "Thumbprint '$canonicalThumbprint' is ambiguous in the release signing policy."
    }
    $signer = $matchingThumbprints[0]
    if ($releaseVersion -lt $signer.ValidFrom -or $releaseVersion -gt $signer.ValidThrough) {
        throw "Thumbprint '$canonicalThumbprint' is not allowed for release version '$Version'."
    }
    return $signer
}

function New-DerLength {
    param([Parameter(Mandatory)][ValidateRange(0, 2147483647)][int]$Length)

    if ($Length -lt 128) {
        return ,[byte[]]@([byte]$Length)
    }
    $bytes = [Collections.Generic.List[byte]]::new()
    $remaining = [uint32]$Length
    while ($remaining -gt 0) {
        $bytes.Insert(0, [byte]($remaining -band 0xff))
        $remaining = $remaining -shr 8
    }
    $result = [byte[]]::new($bytes.Count + 1)
    $result[0] = [byte](0x80 -bor $bytes.Count)
    $bytes.CopyTo($result, 1)
    return ,$result
}

function New-DerElement {
    param(
        [Parameter(Mandatory)][byte]$Tag,
        [Parameter(Mandatory)][byte[]]$Content
    )

    [byte[]]$length = New-DerLength -Length $Content.Length
    $result = [byte[]]::new(1 + $length.Length + $Content.Length)
    $result[0] = $Tag
    [Array]::Copy($length, 0, $result, 1, $length.Length)
    [Array]::Copy($Content, 0, $result, 1 + $length.Length, $Content.Length)
    return ,$result
}

function Add-DerOidComponent {
    param(
        [Parameter(Mandatory)][AllowEmptyCollection()][Collections.Generic.List[byte]]$Destination,
        [Parameter(Mandatory)][uint64]$Value
    )

    $encoded = [Collections.Generic.List[byte]]::new()
    do {
        $encoded.Insert(0, [byte]($Value -band 0x7f))
        $Value = $Value -shr 7
    } while ($Value -gt 0)
    for ($index = 0; $index -lt $encoded.Count; $index += 1) {
        $octet = $encoded[$index]
        if ($index -lt $encoded.Count - 1) {
            $octet = [byte]($octet -bor 0x80)
        }
        $Destination.Add($octet)
    }
}

function New-DerOid {
    param([Parameter(Mandatory)][string]$Oid)

    $components = $Oid.Split('.')
    if ($components.Count -lt 2) {
        throw "Invalid public-key algorithm OID: '$Oid'."
    }
    $values = [Collections.Generic.List[uint64]]::new()
    foreach ($component in $components) {
        $value = [uint64]0
        if (
            $component -cnotmatch '^(0|[1-9][0-9]*)$' -or
            -not [uint64]::TryParse(
                $component,
                [Globalization.NumberStyles]::None,
                [Globalization.CultureInfo]::InvariantCulture,
                [ref]$value
            )
        ) {
            throw "Invalid public-key algorithm OID: '$Oid'."
        }
        $values.Add($value)
    }
    if ($values[0] -gt 2 -or ($values[0] -lt 2 -and $values[1] -gt 39)) {
        throw "Invalid public-key algorithm OID: '$Oid'."
    }
    if ($values[0] -eq 2 -and $values[1] -gt ([uint64]::MaxValue - 80)) {
        throw "Public-key algorithm OID is too large: '$Oid'."
    }

    $content = [Collections.Generic.List[byte]]::new()
    Add-DerOidComponent -Destination $content -Value ([uint64]($values[0] * 40 + $values[1]))
    for ($index = 2; $index -lt $values.Count; $index += 1) {
        Add-DerOidComponent -Destination $content -Value $values[$index]
    }
    return ,(New-DerElement -Tag 0x06 -Content $content.ToArray())
}

function Get-CertificateSpkiSha256 {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [Security.Cryptography.X509Certificates.X509Certificate2]$Certificate
    )

    if ($null -eq $Certificate.PublicKey -or [string]::IsNullOrWhiteSpace($Certificate.PublicKey.Oid.Value)) {
        throw 'The certificate does not contain a supported public key.'
    }
    [byte[]]$algorithmOid = New-DerOid -Oid $Certificate.PublicKey.Oid.Value
    $rawAlgorithmParameters = $Certificate.PublicKey.EncodedParameters.RawData
    [byte[]]$algorithmParameters = if ($null -eq $rawAlgorithmParameters) {
        [byte[]]@()
    } else {
        [byte[]]$rawAlgorithmParameters
    }
    [byte[]]$algorithmContent = @($algorithmOid) + @($algorithmParameters)
    [byte[]]$algorithmIdentifier = New-DerElement -Tag 0x30 -Content $algorithmContent

    $rawKeyValue = $Certificate.PublicKey.EncodedKeyValue.RawData
    if ($null -eq $rawKeyValue -or $rawKeyValue.Length -eq 0) {
        throw 'The certificate public-key value is empty.'
    }
    [byte[]]$keyValue = $rawKeyValue
    $bitStringContent = [byte[]]::new($keyValue.Length + 1)
    $bitStringContent[0] = 0
    [Array]::Copy($keyValue, 0, $bitStringContent, 1, $keyValue.Length)
    [byte[]]$subjectPublicKey = New-DerElement -Tag 0x03 -Content $bitStringContent
    [byte[]]$spkiContent = @($algorithmIdentifier) + @($subjectPublicKey)
    [byte[]]$spki = New-DerElement -Tag 0x30 -Content $spkiContent

    $sha256 = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($sha256.ComputeHash($spki))).Replace('-', '').ToLowerInvariant()
    }
    finally {
        $sha256.Dispose()
    }
}

function Assert-ReleaseSigningCertificate {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [Security.Cryptography.X509Certificates.X509Certificate2]$Certificate,
        [Parameter(Mandatory)]$Signer,
        [switch]$RequirePrivateKey
    )

    if ($Signer.PSObject.TypeNames -cnotcontains $script:SignerTypeName) {
        throw 'Signer must be returned by Get-ReleaseSigningPolicySigner.'
    }
    $certificateThumbprint = ConvertTo-CanonicalThumbprint `
        -Value ([string]$Certificate.Thumbprint) `
        -Context 'Certificate thumbprint'
    if ($certificateThumbprint -cne $Signer.Thumbprint) {
        throw 'The certificate thumbprint does not match the release signing policy.'
    }
    if (-not [string]::Equals([string]$Certificate.Subject, [string]$Signer.Subject, [StringComparison]::Ordinal)) {
        throw 'The certificate Subject does not exactly match the release signing policy.'
    }
    $spkiSha256 = Get-CertificateSpkiSha256 -Certificate $Certificate
    if ($spkiSha256 -cne $Signer.SpkiSha256) {
        throw 'The certificate SPKI SHA-256 does not match the release signing policy.'
    }
    if ($RequirePrivateKey -and -not $Certificate.HasPrivateKey) {
        throw 'The release signing certificate does not have an accessible private key.'
    }

    $ekuExtensions = @(
        $Certificate.Extensions |
            Where-Object { $_.Oid.Value -eq '2.5.29.37' }
    )
    if ($ekuExtensions.Count -ne 1) {
        throw 'The release signing certificate must contain exactly one Enhanced Key Usage extension.'
    }
    $ekuExtension = $ekuExtensions[0] -as [Security.Cryptography.X509Certificates.X509EnhancedKeyUsageExtension]
    if ($null -eq $ekuExtension) {
        $ekuExtension = [Security.Cryptography.X509Certificates.X509EnhancedKeyUsageExtension]::new(
            $ekuExtensions[0],
            $ekuExtensions[0].Critical
        )
    }
    $codeSigningUsages = @(
        $ekuExtension.EnhancedKeyUsages |
            Where-Object { $_.Value -eq $script:CodeSigningEkuOid }
    )
    if ($codeSigningUsages.Count -ne 1) {
        throw "The release signing certificate must allow Code Signing EKU $script:CodeSigningEkuOid."
    }
    if ($RequirePrivateKey) {
        $now = [DateTime]::UtcNow
        if ($now -lt $Certificate.NotBefore.ToUniversalTime() -or $now -gt $Certificate.NotAfter.ToUniversalTime()) {
            throw 'The release signing certificate is not currently within its validity period.'
        }
    }
    return $Certificate
}

Export-ModuleMember -Function @(
    'Import-NekoSshReleaseManifest',
    'Import-ReleaseSigningPolicy',
    'Get-ReleaseSigningPolicySigner',
    'Get-ReleaseTextSha256',
    'Get-CertificateSpkiSha256',
    'Assert-ReleaseSigningCertificate'
)
