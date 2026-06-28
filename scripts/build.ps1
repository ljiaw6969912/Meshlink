param(
  [switch]$Package
)

$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $root

New-Item -ItemType Directory -Force -Path "bin" | Out-Null

$version = "0.1.0-dev"
if (Test-Path "VERSION") {
  $rawVersion = (Get-Content -Raw -LiteralPath "VERSION").Trim()
  if ($rawVersion) {
    $version = $rawVersion
  }
}
$buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$versionFlags = "-X meshlink/internal/version.Version=$version -X meshlink/internal/version.BuildTime=$buildTime"

go test ./...
go build -ldflags "$versionFlags" -o .\bin\mesh-agent.exe .\cmd\mesh-agent
go build -ldflags "$versionFlags" -o .\bin\meshctl.exe .\cmd\meshctl
go build -ldflags "$versionFlags" -o .\bin\mesh-update-server.exe .\cmd\mesh-update-server
go build -ldflags "-H windowsgui $versionFlags" -o .\bin\mesh-desktop.exe .\cmd\mesh-desktop

if ($Package) {
  powershell -ExecutionPolicy Bypass -File .\scripts\package.ps1
}
