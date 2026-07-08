param(
  [string]$Output = ".\bin\linux\mesh-agent"
)

$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $root

New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Output) | Out-Null

$version = "0.1.0-dev"
if (Test-Path "VERSION") {
  $rawVersion = (Get-Content -Raw -LiteralPath "VERSION").Trim()
  if ($rawVersion) {
    $version = $rawVersion
  }
}
$buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$versionFlags = "-X meshlink/internal/version.Version=$version -X meshlink/internal/version.BuildTime=$buildTime"

$oldGOOS = $env:GOOS
$oldGOARCH = $env:GOARCH
$oldCGO = $env:CGO_ENABLED
try {
  $env:GOOS = "linux"
  $env:GOARCH = "amd64"
  $env:CGO_ENABLED = "0"
  go build -ldflags "$versionFlags" -o $Output .\cmd\mesh-agent
} finally {
  $env:GOOS = $oldGOOS
  $env:GOARCH = $oldGOARCH
  $env:CGO_ENABLED = $oldCGO
}
