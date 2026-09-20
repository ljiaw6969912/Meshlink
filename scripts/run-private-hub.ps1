param(
  [Parameter(Mandatory = $true)]
  [string]$DeploymentID,
  [Parameter(Mandatory = $true)]
  [string]$OrganizationID,
  [string]$Listen = "0.0.0.0:18080",
  [string]$RelayListen = "",
  [string]$LicenseFile = "",
  [string]$TrustedKeysFile = ""
)

$ErrorActionPreference = "Stop"
$packageRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$hubPath = Join-Path $packageRoot "bin\mesh-cloudhub.exe"
if (-not (Test-Path -LiteralPath $hubPath -PathType Leaf)) {
  throw "Private Hub executable is missing: $hubPath"
}
if (-not $LicenseFile) {
  $LicenseFile = Join-Path $packageRoot "licenses\private-license.json"
}
if (-not $TrustedKeysFile) {
  $TrustedKeysFile = Join-Path $packageRoot "configs\trusted-license-keys.json"
}
if (-not (Test-Path -LiteralPath $TrustedKeysFile -PathType Leaf)) {
  throw "Trusted public key configuration is missing: $TrustedKeysFile"
}

$arguments = @(
  "-listen", $Listen,
  "-private-deployment-id", $DeploymentID,
  "-private-organization-id", $OrganizationID,
  "-private-license-file", $LicenseFile,
  "-private-trusted-keys-file", $TrustedKeysFile
)
if ($RelayListen) {
  $arguments += @("-relay-listen", $RelayListen)
}

& $hubPath @arguments
exit $LASTEXITCODE
