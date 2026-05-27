param(
  [string]$OutDir = "dist",
  [string]$Name = "meshlink"
)

$ErrorActionPreference = "Stop"

$root = Resolve-Path (Join-Path $PSScriptRoot "..")
$outPath = Join-Path $root $OutDir
$packageDir = Join-Path $outPath $Name
$zipPath = Join-Path $outPath "$Name.zip"

if (-not ((Resolve-Path $root).Path -eq (Get-Location).Path -or $root)) {
  throw "Invalid project root"
}

New-Item -ItemType Directory -Force -Path $outPath | Out-Null
if (Test-Path $packageDir) {
  Remove-Item -LiteralPath $packageDir -Recurse -Force
}
if (Test-Path $zipPath) {
  Remove-Item -LiteralPath $zipPath -Force
}

New-Item -ItemType Directory -Force -Path (Join-Path $packageDir "bin") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $packageDir "configs") | Out-Null

Copy-Item -LiteralPath (Join-Path $root "bin\mesh-agent.exe") -Destination (Join-Path $packageDir "bin\mesh-agent.exe")
Copy-Item -LiteralPath (Join-Path $root "bin\mesh-desktop.exe") -Destination (Join-Path $packageDir "bin\mesh-desktop.exe")
Copy-Item -LiteralPath (Join-Path $root "bin\meshctl.exe") -Destination (Join-Path $packageDir "bin\meshctl.exe")
$wintunPath = Join-Path $root "bin\wintun.dll"
if (Test-Path $wintunPath) {
  Copy-Item -LiteralPath $wintunPath -Destination (Join-Path $packageDir "bin\wintun.dll")
}
Copy-Item -LiteralPath (Join-Path $root "configs\hub.example.json") -Destination (Join-Path $packageDir "configs\hub.example.json")
Copy-Item -LiteralPath (Join-Path $root "configs\spoke.example.json") -Destination (Join-Path $packageDir "configs\spoke.example.json")
Copy-Item -LiteralPath (Join-Path $root "README.md") -Destination (Join-Path $packageDir "README.md")
Copy-Item -LiteralPath (Join-Path $root "DEPLOY.zh-CN.md") -Destination (Join-Path $packageDir "DEPLOY.zh-CN.md")
$repairScript = Join-Path $root "repair-meshlink-admin.ps1"
if (Test-Path $repairScript) {
  Copy-Item -LiteralPath $repairScript -Destination (Join-Path $packageDir "repair-meshlink-admin.ps1")
}

Compress-Archive -LiteralPath $packageDir -DestinationPath $zipPath
Write-Output "Created $zipPath"
