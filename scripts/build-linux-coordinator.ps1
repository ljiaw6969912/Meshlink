param(
  [ValidateSet("amd64", "arm64")]
  [string[]]$Architectures = @("amd64", "arm64")
)

$ErrorActionPreference = "Stop"
$root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$previousLocation = Get-Location
$oldGOOS = $env:GOOS
$oldGOARCH = $env:GOARCH
$oldCGO = $env:CGO_ENABLED

function Invoke-Go([string[]]$Arguments) {
  & go @Arguments
  if ($LASTEXITCODE -ne 0) {
    throw "go $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
  }
}

function Write-Utf8NoBom([string]$Path, [string]$Content) {
  [System.IO.File]::WriteAllText($Path, $Content, (New-Object System.Text.UTF8Encoding($false)))
}

try {
  Set-Location $root
  $version = (Get-Content -Raw -LiteralPath (Join-Path $root "VERSION")).Trim()
  if ($version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') {
    throw "VERSION is invalid"
  }
  $buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
  $revision = & git rev-parse HEAD
  if ($LASTEXITCODE -ne 0) { throw "Cannot read source revision" }
  $sourceChanges = & git status --porcelain --untracked-files=normal
  if ($LASTEXITCODE -ne 0) { throw "Cannot read source status" }
  $goVersion = & go version
  if ($LASTEXITCODE -ne 0) { throw "Cannot read Go version" }
  $cache = Join-Path $root ".cache"
  New-Item -ItemType Directory -Force -Path $cache | Out-Null

  # Build the archive helper for this host, independent of any cross-build environment.
  $env:GOOS = $null
  $env:GOARCH = $null
  $env:CGO_ENABLED = "0"
  $helper = Join-Path $cache "meshlink-linux-package.exe"
  Invoke-Go -Arguments @("build", "-trimpath", "-o", $helper, "./scripts/linux-package")

  $packages = @()
  foreach ($architecture in ($Architectures | Select-Object -Unique)) {
    $stage = Join-Path $cache "linux-coordinator/$architecture/meshlink-linux"
    foreach ($directory in @("bin", "docs", "systemd")) {
      New-Item -ItemType Directory -Force -Path (Join-Path $stage $directory) | Out-Null
    }
    $env:GOOS = "linux"
    $env:GOARCH = $architecture
    $binary = Join-Path $stage "bin/mesh-coordinator"
    $versionFlags = "-s -w -X meshlink/internal/version.Version=$version -X meshlink/internal/version.BuildTime=$buildTime"
    Invoke-Go -Arguments @("build", "-trimpath", "-ldflags", $versionFlags, "-o", $binary, "./cmd/mesh-coordinator")
    Copy-Item -LiteralPath (Join-Path $root "LICENSE") -Destination (Join-Path $stage "LICENSE") -Force
    Copy-Item -LiteralPath (Join-Path $root "THIRD_PARTY_NOTICES.md") -Destination (Join-Path $stage "THIRD_PARTY_NOTICES.md") -Force
    Copy-Item -LiteralPath (Join-Path $root "VERSION") -Destination (Join-Path $stage "VERSION") -Force
    Copy-Item -LiteralPath (Join-Path $root "docs/linux-coordinator.md") -Destination (Join-Path $stage "docs/linux-coordinator.md") -Force
    Copy-Item -LiteralPath (Join-Path $root "scripts/linux/meshlink-coordinator.service") -Destination (Join-Path $stage "systemd/meshlink-coordinator.service") -Force
    $artifacts = @()
    foreach ($relative in @("bin/mesh-coordinator", "docs/linux-coordinator.md", "systemd/meshlink-coordinator.service", "LICENSE", "THIRD_PARTY_NOTICES.md", "VERSION")) {
      $path = Join-Path $stage $relative
      $artifacts += [ordered]@{
        path = $relative
        sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
        size = (Get-Item -LiteralPath $path).Length
      }
    }
    $metadata = [ordered]@{
      schema = "meshlink-linux-build-v1"
      version = $version
      build_time = $buildTime
      target = "linux/$architecture"
      cgo_enabled = $false
      source_revision = [string]$revision
      source_dirty = [bool]$sourceChanges
      go_version = [string]$goVersion
      command = "go build -trimpath -ldflags <version, build_time, -s -w> ./cmd/mesh-coordinator"
      artifacts = $artifacts
    }
    Write-Utf8NoBom (Join-Path $stage "build-metadata.json") (($metadata | ConvertTo-Json -Depth 8) + "`n")
    $archive = Join-Path $cache "meshlink-linux-$architecture.tar.gz"
    & $helper -source $stage -output $archive
    if ($LASTEXITCODE -ne 0) { throw "Linux package verification failed for $architecture" }
    $packages += [ordered]@{file=(Split-Path $archive -Leaf); sha256=(Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant(); size=(Get-Item -LiteralPath $archive).Length}
    Write-Output "Built and verified $archive ($version; linux/$architecture)"
  }
  # Written only after every requested architecture has been verified. The
  # publisher checks this receipt before stopping services or copying files.
  $receipt = [ordered]@{schema='meshlink-linux-packages-v1'; version=$version; build_time=$buildTime; archives=$packages}
  Write-Utf8NoBom (Join-Path $cache 'linux-coordinator-packages.json') (($receipt | ConvertTo-Json -Depth 8) + "`n")
} finally {
  $env:GOOS = $oldGOOS
  $env:GOARCH = $oldGOARCH
  $env:CGO_ENABLED = $oldCGO
  Set-Location $previousLocation
}
