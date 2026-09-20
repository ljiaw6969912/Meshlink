param(
  [Parameter(Mandatory = $true)]
  [string]$PackagePath
)

$ErrorActionPreference = "Stop"
$resolvedPackage = [System.IO.Path]::GetFullPath($PackagePath)
if (-not (Test-Path -LiteralPath $resolvedPackage -PathType Leaf)) {
  throw "Private deployment package does not exist: $resolvedPackage"
}

Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$archive = [System.IO.Compression.ZipFile]::OpenRead($resolvedPackage)
try {
  $manifestEntries = @($archive.Entries | Where-Object { $_.FullName -match '^[^/]+/manifest\.json$' })
  if ($manifestEntries.Count -ne 1) {
    throw "Package must contain exactly one root manifest.json"
  }
  $manifestEntry = $manifestEntries[0]
  $rootName = $manifestEntry.FullName.Substring(0, $manifestEntry.FullName.IndexOf('/'))
  $reader = New-Object System.IO.StreamReader($manifestEntry.Open(), [System.Text.Encoding]::UTF8, $true)
  try {
    $manifest = ($reader.ReadToEnd() | ConvertFrom-Json)
  } finally {
    $reader.Dispose()
  }
  if ($manifest.schema -ne "meshlink-private-package-v1") {
    throw "Unsupported private package manifest schema"
  }
  $expectedRoot = "meshlink-private-$($manifest.version)"
  if ($rootName -ne $expectedRoot -or [System.IO.Path]::GetFileName($resolvedPackage) -ne "$expectedRoot.zip") {
    throw "Package file, root directory, and manifest version do not match"
  }

  $entryMap = @{}
  foreach ($entry in $archive.Entries) {
    if ($entryMap.ContainsKey($entry.FullName)) {
      throw "Duplicate package entry: $($entry.FullName)"
    }
    $entryMap[$entry.FullName] = $entry
    $lowerName = $entry.FullName.ToLowerInvariant()
    if ($lowerName.EndsWith('.key') -or $lowerName.EndsWith('.pem') -or
        $lowerName.EndsWith('.p12') -or $lowerName.EndsWith('.pfx') -or
        $lowerName.EndsWith('/private-license.json')) {
      throw "Forbidden credential file in private package: $($entry.FullName)"
    }
  }

  $required = @(
    "bin/mesh-cloudhub.exe", "bin/mesh-agent.exe", "bin/mesh-desktop.exe", "bin/meshctl.exe",
    "configs/private-hub.example.env", "configs/trusted-license-keys.example.json",
    "scripts/run-private-hub.ps1", "docs/ops/private-deployment.zh-CN.md",
    "licenses/README.txt", "offline-updates/README.txt", "VERSION"
  )
  foreach ($relative in $required) {
    if (-not $entryMap.ContainsKey("$rootName/$relative")) {
      throw "Required private package file is missing: $relative"
    }
  }

  $expectedPaths = @($manifest.files | ForEach-Object { [string]$_.path })
  [string[]]$sortedPaths = @($expectedPaths)
  [System.Array]::Sort($sortedPaths, [System.StringComparer]::Ordinal)
  if (($expectedPaths -join "`n") -ne ($sortedPaths -join "`n")) {
    throw "Manifest file list is not sorted"
  }
  $sha256 = [System.Security.Cryptography.SHA256]::Create()
  try {
    foreach ($file in $manifest.files) {
      $entryName = "$rootName/$($file.path)"
      if (-not $entryMap.ContainsKey($entryName)) {
        throw "Manifest references missing file: $($file.path)"
      }
      $entry = $entryMap[$entryName]
      if ([int64]$entry.Length -ne [int64]$file.size) {
        throw "Manifest size mismatch: $($file.path)"
      }
      $stream = $entry.Open()
      try {
        $actualHash = [System.BitConverter]::ToString($sha256.ComputeHash($stream)).Replace('-', '').ToLowerInvariant()
      } finally {
        $stream.Dispose()
      }
      if ($actualHash -ne [string]$file.sha256) {
        throw "Manifest SHA-256 mismatch: $($file.path)"
      }
    }
  } finally {
    $sha256.Dispose()
  }

  $expectedEntryCount = $manifest.files.Count + 1
  if ($archive.Entries.Count -ne $expectedEntryCount) {
    throw "Package contains files not covered by manifest"
  }
  foreach ($entry in $archive.Entries) {
    if ($entry.FullName -notmatch '\.(json|txt|md|ps1|env)$') {
      continue
    }
    $textReader = New-Object System.IO.StreamReader($entry.Open(), [System.Text.Encoding]::UTF8, $true)
    try {
      $text = $textReader.ReadToEnd()
    } finally {
      $textReader.Dispose()
    }
    if ($text -match '-----BEGIN (RSA |OPENSSH |EC )?PRIVATE KEY-----') {
      throw "Private key material found in package: $($entry.FullName)"
    }
    if ($text -match '"(password|token|private_key|tls_private_key)"\s*:\s*"[^"<][^"]*"') {
      throw "Credential-like value found in package: $($entry.FullName)"
    }
  }
} finally {
  $archive.Dispose()
}

Write-Output "Verified private deployment package: $resolvedPackage"
