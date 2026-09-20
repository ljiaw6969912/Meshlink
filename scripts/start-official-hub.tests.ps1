$ErrorActionPreference = "Stop"

$root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$launcherDir = Join-Path $root "hub-scripts"
$windowsEntry = Join-Path $launcherDir "start-hub.bat"
$linuxEntry = Join-Path $launcherDir "start-hub.sh"
$attributesEntry = Join-Path $root ".gitattributes"

function Assert-Contains([string]$Text, [string]$Expected, [string]$Message) {
  if (-not $Text.Contains($Expected)) {
    throw $Message
  }
}

foreach ($entry in @($windowsEntry, $linuxEntry)) {
  if (-not (Test-Path -LiteralPath $entry -PathType Leaf)) {
    throw "$entry is missing"
  }
}

$windowsText = Get-Content -Raw -LiteralPath $windowsEntry
Assert-Contains $windowsText '%~dp0mesh-cloudhub.exe' "Windows launcher does not use a colocated mesh-cloudhub.exe"

$linuxText = Get-Content -Raw -LiteralPath $linuxEntry
Assert-Contains $linuxText '#!/usr/bin/env sh' "Linux launcher does not use a portable POSIX shell"
Assert-Contains $linuxText 'SCRIPT_DIR=' "Linux launcher does not resolve its own directory"
Assert-Contains $linuxText '$SCRIPT_DIR/mesh-cloudhub' "Linux launcher does not use a colocated mesh-cloudhub"

if (-not (Test-Path -LiteralPath $attributesEntry -PathType Leaf)) {
  throw ".gitattributes is missing"
}
$attributesText = Get-Content -Raw -LiteralPath $attributesEntry
Assert-Contains $attributesText '*.sh text eol=lf' "Linux launchers are not protected from CRLF conversion"

foreach ($entry in @($windowsEntry, $linuxEntry)) {
  $text = Get-Content -Raw -LiteralPath $entry
  Assert-Contains $text "-listen 0.0.0.0:18080" "$entry does not bind the control API to 0.0.0.0:18080"
  Assert-Contains $text "-relay-listen 0.0.0.0:18082" "$entry does not bind Relay to 0.0.0.0:18082"
  Assert-Contains $text "Development only" "$entry does not warn about development-only exposure"

  foreach ($forbidden in @("dist", "release")) {
    if ($text.Contains($forbidden)) {
      throw "$entry writes to a packaging directory: $forbidden"
    }
  }
}

foreach ($packageScript in @("package.ps1", "package-private.ps1")) {
  $packageText = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot $packageScript)
  foreach ($launcherName in @("start-hub.bat", "start-hub.sh")) {
    if ($packageText.Contains($launcherName)) {
      throw "$packageScript includes the development Hub launcher $launcherName"
    }
  }
}

Write-Output "start-official-hub tests passed"
