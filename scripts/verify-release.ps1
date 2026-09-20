param(
  [string]$ReleaseDir = "release",
  [string]$Version = "",
  [ValidateSet("Development", "Release")]
  [string]$ExpectedMode = "Development",
  [string]$ReferencePackagePath = ""
)

$ErrorActionPreference = "Stop"

function Get-SHA256Hex([string]$Path) {
  return (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
}

function Compare-SemVer([string]$Left, [string]$Right) {
  if ($Left -notmatch '^(\d+\.\d+\.\d+)(?:-([0-9A-Za-z.-]+))?$') {
    throw "Invalid semantic version: $Left"
  }
  $leftCore = [version]$Matches[1]
  $leftSuffix = [string]$Matches[2]
  if ($Right -notmatch '^(\d+\.\d+\.\d+)(?:-([0-9A-Za-z.-]+))?$') {
    throw "Invalid semantic version: $Right"
  }
  $rightCore = [version]$Matches[1]
  $rightSuffix = [string]$Matches[2]
  $coreComparison = $leftCore.CompareTo($rightCore)
  if ($coreComparison -ne 0) {
    return $coreComparison
  }
  if ($leftSuffix -ceq $rightSuffix) {
    return 0
  }
  if ([string]::IsNullOrEmpty($leftSuffix)) {
    return 1
  }
  if ([string]::IsNullOrEmpty($rightSuffix)) {
    return -1
  }
  return [string]::CompareOrdinal($leftSuffix, $rightSuffix)
}

function Read-ZipText($Entry, [int64]$Limit = 4194304) {
  if ($null -eq $Entry -or [int64]$Entry.Length -gt $Limit) {
    throw "ZIP text entry is missing or exceeds the size limit"
  }
  $reader = New-Object System.IO.StreamReader($Entry.Open(), [System.Text.Encoding]::UTF8, $true)
  try {
    return $reader.ReadToEnd()
  } finally {
    $reader.Dispose()
  }
}

function Get-ZipEntrySHA256($Entry) {
  $sha256 = [System.Security.Cryptography.SHA256]::Create()
  $stream = $Entry.Open()
  try {
    return [System.BitConverter]::ToString($sha256.ComputeHash($stream)).Replace('-', '').ToLowerInvariant()
  } finally {
    $stream.Dispose()
    $sha256.Dispose()
  }
}

function Assert-SafeRelativePath([string]$Path) {
  $normalized = $Path.Replace('\', '/')
  if ([string]::IsNullOrWhiteSpace($normalized) -or $normalized.StartsWith('/') -or $normalized.Contains('\') -or
      $normalized.Contains(':') -or $normalized -match '(^|/)\.\.(/|$)' -or $normalized -match '(^|/)\.(/|$)' -or
      $normalized.Contains('//') -or $normalized.EndsWith('/')) {
    throw "Unsafe release package path: $Path"
  }
  return $normalized
}

function Assert-NoSecret($Entry) {
  $name = $Entry.FullName.Replace('\', '/')
  $lower = $name.ToLowerInvariant()
  foreach ($suffix in @('.key', '.pem', '.p12', '.pfx', '.jks', '.keystore')) {
    if ($lower.EndsWith($suffix)) {
      throw "Forbidden credential file in release package: $name"
    }
  }
  $isText = $false
  foreach ($suffix in @('.json', '.md', '.txt', '.ps1', '.bat', '.env', '.yml', '.yaml', '.config')) {
    if ($lower.EndsWith($suffix)) {
      $isText = $true
      break
    }
  }
  if (-not $isText -or [int64]$Entry.Length -gt 4194304) {
    return
  }
  $text = Read-ZipText $Entry
  if ($text -match '-----BEGIN (RSA |OPENSSH |EC )?PRIVATE KEY-----') {
    throw "Private key material found in release package: $name"
  }
  if ($text -match '(?i)["'']?(password|private_key|client_secret|access_token)["'']?\s*[:=]\s*["''][^"''<\$%{][^"'']+["'']') {
    throw "Credential-like value found in release package: $name"
  }
}

function Assert-FileHash($Descriptor, [string]$Directory, [string]$Label) {
  $fileName = [string]$Descriptor.file
  if ([System.IO.Path]::GetFileName($fileName) -cne $fileName) {
    throw "$Label filename is invalid"
  }
  $path = Join-Path $Directory $fileName
  if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
    throw "$Label is missing: $fileName"
  }
  $actualHash = Get-SHA256Hex $path
  if ($actualHash -cne ([string]$Descriptor.sha256).ToLowerInvariant()) {
    throw "$Label SHA-256 mismatch: $fileName"
  }
  if ($null -ne $Descriptor.size -and [int64]$Descriptor.size -ne (Get-Item -LiteralPath $path).Length) {
    throw "$Label size mismatch: $fileName"
  }
  return $path
}

function Test-InternalGoVersion([string]$ExecutablePath, [string]$ExpectedVersion, [string]$Mode) {
  $previousErrorPreference = $ErrorActionPreference
  try {
    $ErrorActionPreference = "Continue"
    $output = @(& go version -m $ExecutablePath 2>&1)
    $exitCode = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $previousErrorPreference
  }
  if ($exitCode -ne 0) {
    if ($Mode -eq 'release') {
      throw "Cannot inspect internal Go version: $ExecutablePath"
    }
    return $false
  }
  $joined = $output -join "`n"
  $needle = "meshlink/internal/version.Version=$ExpectedVersion"
  if (-not $joined.Contains($needle)) {
    throw "Internal version mismatch in $ExecutablePath"
  }
  return $true
}

function Invoke-ReleaseVerification {
  $root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
  if ([string]::IsNullOrWhiteSpace($Version)) {
    $Version = (Get-Content -Raw -LiteralPath (Join-Path $root "VERSION")).Trim()
  }
  if ($Version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') {
    throw "Release version is missing or invalid"
  }
  $resolvedRelease = if ([System.IO.Path]::IsPathRooted($ReleaseDir)) {
    [System.IO.Path]::GetFullPath($ReleaseDir)
  } else {
    [System.IO.Path]::GetFullPath((Join-Path $root $ReleaseDir))
  }
  if (-not (Test-Path -LiteralPath $resolvedRelease -PathType Container)) {
    throw "Release directory does not exist: $resolvedRelease"
  }

  $manifestPath = Join-Path $resolvedRelease "manifest.json"
  if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
    throw "Release manifest is missing"
  }
  $manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
  $expectedModeValue = $ExpectedMode.ToLowerInvariant()
  if ($manifest.schema -ne 'meshlink-release-v1' -or [string]$manifest.version -cne $Version) {
    throw "Release manifest schema or version mismatch"
  }
  if ([string]$manifest.mode -cne $expectedModeValue) {
    throw "Release manifest mode mismatch: expected $expectedModeValue"
  }
  $expectedPackageName = "meshlink-$Version.zip"
  if ([string]$manifest.package.file -cne $expectedPackageName) {
    throw "Release package filename does not match version"
  }
  if ($expectedModeValue -eq 'release') {
    if (-not [bool]$manifest.signing.code_signed) {
      throw "Formal release must be code signed"
    }
    $certificateThumbprint = ([string]$manifest.signing.certificate_thumbprint).Replace(' ', '').ToUpperInvariant()
    if ($certificateThumbprint -notmatch '^[0-9A-F]{40}$') {
      throw "Formal release certificate thumbprint is invalid"
    }
  } else {
    if ([bool]$manifest.signing.code_signed -or -not [string]::IsNullOrWhiteSpace([string]$manifest.signing.certificate_thumbprint)) {
      throw "Development release must be explicitly unsigned"
    }
    $certificateThumbprint = ""
  }

  $packagePath = Assert-FileHash $manifest.package $resolvedRelease "Release package"
  $notesPath = Assert-FileHash $manifest.release_notes $resolvedRelease "Release notes"
  $rollbackPath = Assert-FileHash $manifest.rollback $resolvedRelease "Rollback manifest"
  if ([System.IO.Path]::GetFileName($notesPath) -cne "release-notes-$Version.zh-CN.md" -or
      [System.IO.Path]::GetFileName($rollbackPath) -cne "rollback-manifest-$Version.json") {
    throw "Release notes or rollback manifest filename does not match version"
  }

  $rollback = Get-Content -Raw -LiteralPath $rollbackPath | ConvertFrom-Json
  if ($rollback.schema -ne 'meshlink-rollback-v1' -or [string]$rollback.version -cne $Version) {
    throw "Rollback manifest identity mismatch"
  }
  $rollbackAvailable = $null -ne $rollback.package -and -not [string]::IsNullOrWhiteSpace([string]$rollback.package.file)
  if ($expectedModeValue -eq 'release' -and -not $rollbackAvailable) {
    throw "Formal release requires a verified rollback package"
  }
  $rollbackPackagePath = $null
  if ($rollbackAvailable) {
    $previousVersion = [string]$rollback.previous_version
    if ($previousVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$' -or
        [string]$rollback.package.file -cne "meshlink-$previousVersion.zip") {
      throw "Rollback package version or filename is invalid"
    }
    if ((Compare-SemVer $previousVersion $Version) -ge 0) {
      throw "Rollback previous version must be older than the release version"
    }
    $rollbackPackagePath = Assert-FileHash $rollback.package $resolvedRelease "Rollback package"
  }

  $checksumsPath = Join-Path $resolvedRelease "checksums-$Version.sha256"
  if (-not (Test-Path -LiteralPath $checksumsPath -PathType Leaf)) {
    throw "Release checksum file is missing"
  }
  $checksumMap = @{}
  foreach ($line in @(Get-Content -LiteralPath $checksumsPath)) {
    if ([string]::IsNullOrWhiteSpace($line)) {
      continue
    }
    if ($line -cnotmatch '^([0-9a-f]{64})  ([^\\/]+)$') {
      throw "Invalid checksums file line: $line"
    }
    if ($checksumMap.ContainsKey($Matches[2])) {
      throw "Duplicate checksums file entry: $($Matches[2])"
    }
    $checksumMap[$Matches[2]] = $Matches[1]
  }
  $checksumTargets = @($packagePath, $manifestPath, $notesPath, $rollbackPath)
  if ($null -ne $rollbackPackagePath) {
    $checksumTargets += $rollbackPackagePath
  }
  foreach ($path in $checksumTargets) {
    $name = [System.IO.Path]::GetFileName($path)
    if (-not $checksumMap.ContainsKey($name) -or $checksumMap[$name] -cne (Get-SHA256Hex $path)) {
      throw "Checksums SHA-256 mismatch: $name"
    }
  }

  if (-not [string]::IsNullOrWhiteSpace($ReferencePackagePath)) {
    $resolvedReference = [System.IO.Path]::GetFullPath($ReferencePackagePath)
    if (-not (Test-Path -LiteralPath $resolvedReference -PathType Leaf)) {
      throw "Reproducibility reference package is missing"
    }
    if ((Get-SHA256Hex $resolvedReference) -cne (Get-SHA256Hex $packagePath)) {
      throw "Package reproducibility check failed: repeated package bytes differ"
    }
  }

  Add-Type -AssemblyName System.IO.Compression
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  $archive = [System.IO.Compression.ZipFile]::OpenRead($packagePath)
  $extractDir = Join-Path ([System.IO.Path]::GetTempPath()) ("meshlink-release-verify-" + [guid]::NewGuid().ToString("N"))
  try {
    $entryMap = @{}
    foreach ($entry in $archive.Entries) {
      $name = $entry.FullName.Replace('\', '/')
      if ($name.StartsWith('/') -or $name.Contains(':') -or $name -match '(^|/)\.\.(/|$)') {
        throw "Unsafe release package entry: $name"
      }
      $key = $name.ToLowerInvariant()
      if ($entryMap.ContainsKey($key)) {
        throw "Duplicate release package entry: $name"
      }
      $entryMap[$key] = $entry
      Assert-NoSecret $entry
    }
    $manifestEntry = $entryMap['meshlink/manifest.json']
    $versionEntry = $entryMap['meshlink/version']
    if ($null -eq $manifestEntry -or $null -eq $versionEntry -or (Read-ZipText $versionEntry).Trim() -cne $Version) {
      throw "Package VERSION or manifest.json is missing or mismatched"
    }
    $packageManifest = Read-ZipText $manifestEntry | ConvertFrom-Json
    if ($packageManifest.schema -ne 'meshlink-package-v1' -or [string]$packageManifest.version -cne $Version -or
        [string]$packageManifest.mode -cne $expectedModeValue) {
      throw "Package manifest identity mismatch"
    }
    if ([bool]$packageManifest.signing.code_signed -ne [bool]$manifest.signing.code_signed -or
        ([string]$packageManifest.signing.certificate_thumbprint).Replace(' ', '').ToUpperInvariant() -cne $certificateThumbprint) {
      throw "Package and release signature metadata mismatch"
    }

    $requiredFiles = @(
      'VERSION', 'bin/mesh-agent.exe', 'bin/mesh-cloudhub.exe', 'bin/mesh-desktop.exe',
      'bin/mesh-update-server.exe', 'bin/meshctl.exe', 'bin/wintun.dll'
    )
    foreach ($relative in $requiredFiles) {
      if (-not $entryMap.ContainsKey(('meshlink/' + $relative).ToLowerInvariant())) {
        throw "Required release package file is missing: $relative"
      }
    }

    $paths = @($packageManifest.files | ForEach-Object { [string]$_.path })
    [string[]]$sortedPaths = @($paths)
    [System.Array]::Sort($sortedPaths, [System.StringComparer]::Ordinal)
    if (($paths -join "`n") -cne ($sortedPaths -join "`n")) {
      throw "Package manifest file list is not sorted"
    }
    $covered = @{}
    foreach ($file in @($packageManifest.files)) {
      $relative = Assert-SafeRelativePath ([string]$file.path)
      $key = ('meshlink/' + $relative).ToLowerInvariant()
      if ($covered.ContainsKey($key) -or -not $entryMap.ContainsKey($key)) {
        throw "Package manifest contains duplicate or missing file: $relative"
      }
      $entry = $entryMap[$key]
      if ([int64]$entry.Length -ne [int64]$file.size -or (Get-ZipEntrySHA256 $entry) -cne ([string]$file.sha256).ToLowerInvariant()) {
        throw "Package manifest SHA-256 or size mismatch: $relative"
      }
      $covered[$key] = $true
    }
    foreach ($entry in $archive.Entries) {
      $key = $entry.FullName.Replace('\', '/').ToLowerInvariant()
      if ($entry.FullName.EndsWith('/') -or $key -eq 'meshlink/manifest.json') {
        continue
      }
      if (-not $covered.ContainsKey($key)) {
        throw "Release package contains file not covered by manifest: $($entry.FullName)"
      }
    }

    New-Item -ItemType Directory -Force -Path $extractDir | Out-Null
    $inspected = 0
    foreach ($relative in @($requiredFiles | Where-Object { $_.EndsWith('.exe') })) {
      $entry = $entryMap[('meshlink/' + $relative).ToLowerInvariant()]
      $destination = Join-Path $extractDir ([System.IO.Path]::GetFileName($relative))
      $input = $entry.Open()
      $output = [System.IO.File]::Create($destination)
      try {
        $input.CopyTo($output)
      } finally {
        $input.Dispose()
        $output.Dispose()
      }
      if (Test-InternalGoVersion $destination $Version $expectedModeValue) {
        $inspected++
      }
      if ($expectedModeValue -eq 'release') {
        $signature = Get-AuthenticodeSignature -LiteralPath $destination
        if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid -or $null -eq $signature.SignerCertificate) {
          throw "Authenticode signature is invalid: $relative"
        }
        if ($signature.SignerCertificate.Thumbprint.Replace(' ', '').ToUpperInvariant() -cne $certificateThumbprint) {
          throw "Authenticode signer certificate mismatch: $relative"
        }
      }
    }
    if ($expectedModeValue -eq 'release' -and $inspected -ne 5) {
      throw "Formal release internal version inspection was incomplete"
    }
  } finally {
    $archive.Dispose()
    Remove-Item -LiteralPath $extractDir -Recurse -Force -ErrorAction SilentlyContinue
  }

  Write-Output "Verified Meshlink $expectedModeValue release $Version at $resolvedRelease"
}

try {
  Invoke-ReleaseVerification
} catch {
  Write-Error $_
  exit 1
}
