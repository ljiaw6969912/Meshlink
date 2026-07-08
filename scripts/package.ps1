param(
  [string]$OutDir = "dist",
  [string]$Name = "meshlink"
)

$ErrorActionPreference = "Stop"

$root = Resolve-Path (Join-Path $PSScriptRoot "..")
$outPath = Join-Path $root $OutDir
$packageDir = Join-Path $outPath $Name
$zipPath = Join-Path $outPath "$Name.zip"
$releaseDir = Join-Path $root "release"
$manifestPath = Join-Path $releaseDir "manifest.json"

$version = "0.1.0-dev"
if (Test-Path (Join-Path $root "VERSION")) {
  $rawVersion = (Get-Content -Raw -LiteralPath (Join-Path $root "VERSION")).Trim()
  if ($rawVersion) {
    $version = $rawVersion
  }
}
$buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$releaseZipName = "$Name-$version.zip"
$releaseZipPath = Join-Path $releaseDir $releaseZipName

if (-not ((Resolve-Path $root).Path -eq (Get-Location).Path -or $root)) {
  throw "Invalid project root"
}

New-Item -ItemType Directory -Force -Path $outPath | Out-Null
New-Item -ItemType Directory -Force -Path $releaseDir | Out-Null
if (Test-Path $packageDir) {
  Remove-Item -LiteralPath $packageDir -Recurse -Force
}
if (Test-Path $zipPath) {
  Remove-Item -LiteralPath $zipPath -Force
}
if (Test-Path $releaseZipPath) {
  Remove-Item -LiteralPath $releaseZipPath -Force
}

New-Item -ItemType Directory -Force -Path (Join-Path $packageDir "bin") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $packageDir "bin\linux") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $packageDir "configs") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $packageDir "scripts") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $packageDir "docs\ops") | Out-Null

Copy-Item -LiteralPath (Join-Path $root "bin\mesh-agent.exe") -Destination (Join-Path $packageDir "bin\mesh-agent.exe")
Copy-Item -LiteralPath (Join-Path $root "bin\mesh-desktop.exe") -Destination (Join-Path $packageDir "bin\mesh-desktop.exe")
Copy-Item -LiteralPath (Join-Path $root "bin\meshctl.exe") -Destination (Join-Path $packageDir "bin\meshctl.exe")
$linuxAgentPath = Join-Path $root "bin\linux\mesh-agent"
if (Test-Path $linuxAgentPath) {
  Copy-Item -LiteralPath $linuxAgentPath -Destination (Join-Path $packageDir "bin\linux\mesh-agent")
}
$updateServerPath = Join-Path $root "bin\mesh-update-server.exe"
if (Test-Path $updateServerPath) {
  Copy-Item -LiteralPath $updateServerPath -Destination (Join-Path $packageDir "bin\mesh-update-server.exe")
}
$wintunPath = Join-Path $root "bin\wintun.dll"
if (Test-Path $wintunPath) {
  Copy-Item -LiteralPath $wintunPath -Destination (Join-Path $packageDir "bin\wintun.dll")
}
Copy-Item -LiteralPath (Join-Path $root "configs\hub.example.json") -Destination (Join-Path $packageDir "configs\hub.example.json")
Copy-Item -LiteralPath (Join-Path $root "configs\spoke.example.json") -Destination (Join-Path $packageDir "configs\spoke.example.json")
Copy-Item -LiteralPath (Join-Path $root "README.md") -Destination (Join-Path $packageDir "README.md")
Copy-Item -LiteralPath (Join-Path $root "DEPLOY.zh-CN.md") -Destination (Join-Path $packageDir "DEPLOY.zh-CN.md")
Copy-Item -LiteralPath (Join-Path $root "VERSION") -Destination (Join-Path $packageDir "VERSION")
$licensePath = Join-Path $root "LICENSE"
if (Test-Path $licensePath) {
  Copy-Item -LiteralPath $licensePath -Destination (Join-Path $packageDir "LICENSE")
}
$thirdPartyPath = Join-Path $root "THIRD_PARTY_NOTICES.md"
if (Test-Path $thirdPartyPath) {
  Copy-Item -LiteralPath $thirdPartyPath -Destination (Join-Path $packageDir "THIRD_PARTY_NOTICES.md")
}
$repairScript = Join-Path $root "repair-meshlink-admin.ps1"
if (Test-Path $repairScript) {
  Copy-Item -LiteralPath $repairScript -Destination (Join-Path $packageDir "repair-meshlink-admin.ps1")
}
$publishUpdateScript = Join-Path $root "publish-update.bat"
if (Test-Path $publishUpdateScript) {
  Copy-Item -LiteralPath $publishUpdateScript -Destination (Join-Path $packageDir "publish-update.bat")
}
$buildLinuxAgentScript = Join-Path $root "scripts\build-linux-agent.ps1"
if (Test-Path $buildLinuxAgentScript) {
  Copy-Item -LiteralPath $buildLinuxAgentScript -Destination (Join-Path $packageDir "scripts\build-linux-agent.ps1")
}
$selfRelayE2EScript = Join-Path $root "scripts\e2e-self-relay.ps1"
if (Test-Path $selfRelayE2EScript) {
  Copy-Item -LiteralPath $selfRelayE2EScript -Destination (Join-Path $packageDir "scripts\e2e-self-relay.ps1")
}
$linuxSystemdScript = Join-Path $root "internal\deployssh\scripts\linux-systemd.sh"
if (Test-Path $linuxSystemdScript) {
  Copy-Item -LiteralPath $linuxSystemdScript -Destination (Join-Path $packageDir "scripts\linux-systemd.sh")
}
$selfRelayRunbook = Join-Path $root "docs\ops\self-hosted-relay-runbook.zh-CN.md"
if (Test-Path $selfRelayRunbook) {
  Copy-Item -LiteralPath $selfRelayRunbook -Destination (Join-Path $packageDir "docs\ops\self-hosted-relay-runbook.zh-CN.md")
}

Compress-Archive -LiteralPath $packageDir -DestinationPath $zipPath
Compress-Archive -LiteralPath $packageDir -DestinationPath $releaseZipPath
$releaseFile = Get-Item -LiteralPath $releaseZipPath
$hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $releaseZipPath).Hash.ToLowerInvariant()
$generatedAt = (Get-Date).ToUniversalTime().ToString("o")
$manifestJson = @"
{
  "product": "Meshlink",
  "version": "$version",
  "build_time": "$buildTime",
  "generated_at": "$generatedAt",
  "package": {
    "file": "$releaseZipName",
    "sha256": "$hash",
    "size": $($releaseFile.Length)
  },
  "notes": [
    "Manual update package. Keeps local configs, certificates and logs; updates binaries and example configs only."
  ]
}
"@
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText($manifestPath, $manifestJson, $utf8NoBom)
Write-Output "Created $zipPath"
Write-Output "Created $releaseZipPath"
Write-Output "Created $manifestPath"
