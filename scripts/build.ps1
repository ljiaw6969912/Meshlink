param(
  [switch]$Package
)

$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $root

New-Item -ItemType Directory -Force -Path "bin" | Out-Null

go test ./...
go build -o .\bin\mesh-agent.exe .\cmd\mesh-agent
go build -o .\bin\meshctl.exe .\cmd\meshctl
go build -ldflags "-H windowsgui" -o .\bin\mesh-desktop.exe .\cmd\mesh-desktop

if ($Package) {
  powershell -ExecutionPolicy Bypass -File .\scripts\package.ps1
}

