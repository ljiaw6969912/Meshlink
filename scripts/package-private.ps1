param(
  [string]$BinDir = "bin",
  [string]$OutDir = "dist\private"
)

$ErrorActionPreference = "Stop"
function Get-SHA256Hex([string]$Path) {
  $sha256 = [System.Security.Cryptography.SHA256]::Create()
  $stream = [System.IO.File]::OpenRead($Path)
  try {
    return [System.BitConverter]::ToString($sha256.ComputeHash($stream)).Replace('-', '').ToLowerInvariant()
  } finally {
    $stream.Dispose()
    $sha256.Dispose()
  }
}

function Get-SortedRelativeFiles([string]$Directory) {
  [string[]]$paths = @(Get-ChildItem -LiteralPath $Directory -File -Recurse | ForEach-Object {
    $_.FullName.Substring($Directory.Length + 1).Replace('\', '/')
  })
  [System.Array]::Sort($paths, [System.StringComparer]::Ordinal)
  return $paths
}

$root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$resolvedBin = if ([System.IO.Path]::IsPathRooted($BinDir)) { [System.IO.Path]::GetFullPath($BinDir) } else { [System.IO.Path]::GetFullPath((Join-Path $root $BinDir)) }
$resolvedOut = if ([System.IO.Path]::IsPathRooted($OutDir)) { [System.IO.Path]::GetFullPath($OutDir) } else { [System.IO.Path]::GetFullPath((Join-Path $root $OutDir)) }
$version = (Get-Content -Raw -LiteralPath (Join-Path $root "VERSION")).Trim()
if (-not $version -or $version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') {
  throw "VERSION is missing or invalid"
}
$packageLeaf = "meshlink-private-$version"
$packageDir = [System.IO.Path]::GetFullPath((Join-Path $resolvedOut $packageLeaf))
$zipPath = [System.IO.Path]::GetFullPath((Join-Path $resolvedOut "$packageLeaf.zip"))
$outPrefix = $resolvedOut.TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
if (-not $packageDir.StartsWith($outPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
  throw "Package output escaped the requested output directory"
}

New-Item -ItemType Directory -Force -Path $resolvedOut | Out-Null
if (Test-Path -LiteralPath $packageDir) {
  Remove-Item -LiteralPath $packageDir -Recurse -Force
}
if (Test-Path -LiteralPath $zipPath) {
  Remove-Item -LiteralPath $zipPath -Force
}
foreach ($relative in @("bin", "configs", "scripts", "docs\ops", "licenses", "offline-updates")) {
  New-Item -ItemType Directory -Force -Path (Join-Path $packageDir $relative) | Out-Null
}

$requiredBinaries = @("mesh-cloudhub.exe", "mesh-agent.exe", "mesh-desktop.exe", "meshctl.exe")
foreach ($name in $requiredBinaries) {
  $source = Join-Path $resolvedBin $name
  if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
    throw "Required existing build artifact is missing: $source"
  }
  Copy-Item -LiteralPath $source -Destination (Join-Path $packageDir "bin\$name")
}
foreach ($optional in @("mesh-update-server.exe", "wintun.dll")) {
  $source = Join-Path $resolvedBin $optional
  if (Test-Path -LiteralPath $source -PathType Leaf) {
    Copy-Item -LiteralPath $source -Destination (Join-Path $packageDir "bin\$optional")
  }
}

Copy-Item -LiteralPath (Join-Path $root "configs\private-hub.example.env") -Destination (Join-Path $packageDir "configs\private-hub.example.env")
Copy-Item -LiteralPath (Join-Path $root "configs\trusted-license-keys.example.json") -Destination (Join-Path $packageDir "configs\trusted-license-keys.example.json")
Copy-Item -LiteralPath (Join-Path $root "scripts\run-private-hub.ps1") -Destination (Join-Path $packageDir "scripts\run-private-hub.ps1")
Copy-Item -LiteralPath (Join-Path $root "docs\ops\private-deployment.zh-CN.md") -Destination (Join-Path $packageDir "docs\ops\private-deployment.zh-CN.md")
Copy-Item -LiteralPath (Join-Path $root "VERSION") -Destination (Join-Path $packageDir "VERSION")
foreach ($optionalDoc in @("README.md", "LICENSE", "THIRD_PARTY_NOTICES.md")) {
  $source = Join-Path $root $optionalDoc
  if (Test-Path -LiteralPath $source -PathType Leaf) {
    Copy-Item -LiteralPath $source -Destination (Join-Path $packageDir $optionalDoc)
  }
}

$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText((Join-Path $packageDir "licenses\README.txt"), "Place only the currently approved signed license document in this directory. Never place a signing key here.`n", $utf8NoBom)
[System.IO.File]::WriteAllText((Join-Path $packageDir "offline-updates\README.txt"), "Place an approved signed offline manifest and its matching update ZIP here. Verification must succeed before staging.`n", $utf8NoBom)

$manifestFiles = @()
$sourceFiles = @(Get-SortedRelativeFiles $packageDir)
foreach ($relative in $sourceFiles) {
  $file = Get-Item -LiteralPath (Join-Path $packageDir $relative.Replace('/', '\'))
  $manifestFiles += [ordered]@{
    path = $relative
    sha256 = Get-SHA256Hex $file.FullName
    size = [int64]$file.Length
  }
}
$manifest = [ordered]@{
  schema = "meshlink-private-package-v1"
  product = "Meshlink Private Deployment"
  version = $version
  package = [ordered]@{ file = "$packageLeaf.zip"; root = $packageLeaf }
  files = $manifestFiles
  signing = [ordered]@{ code_signed = $false; gate = "Production release requires an external approved code-signing step." }
}
$manifestJson = ($manifest | ConvertTo-Json -Depth 8) + "`n"
[System.IO.File]::WriteAllText((Join-Path $packageDir "manifest.json"), $manifestJson, $utf8NoBom)

Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$outputStream = [System.IO.File]::Open($zipPath, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
$archive = New-Object System.IO.Compression.ZipArchive($outputStream, [System.IO.Compression.ZipArchiveMode]::Create, $false, [System.Text.Encoding]::UTF8)
try {
  $allFiles = @(Get-SortedRelativeFiles $packageDir)
  foreach ($relative in $allFiles) {
    $file = Get-Item -LiteralPath (Join-Path $packageDir $relative.Replace('/', '\'))
    $entry = $archive.CreateEntry("$packageLeaf/$relative", [System.IO.Compression.CompressionLevel]::Optimal)
    $entry.LastWriteTime = [System.DateTimeOffset]::Parse("2000-01-01T00:00:00Z")
    $entryStream = $entry.Open()
    $inputStream = [System.IO.File]::OpenRead($file.FullName)
    try {
      $inputStream.CopyTo($entryStream)
    } finally {
      $inputStream.Dispose()
      $entryStream.Dispose()
    }
  }
} finally {
  $archive.Dispose()
  $outputStream.Dispose()
}

& (Join-Path $PSScriptRoot "verify-private-package.ps1") -PackagePath $zipPath
$zipHash = Get-SHA256Hex $zipPath
Write-Output "Created deterministic private deployment package: $zipPath"
Write-Output "SHA256: $zipHash"
