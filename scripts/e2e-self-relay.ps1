param(
  [Parameter(Mandatory = $true)]
  [string]$SshHost,

  [int]$SshPort = 22,

  [string]$SshUser = "root",

  [Parameter(Mandatory = $true)]
  [string]$PublicAddress,

  [int]$ListenPort = 8443,

  [string]$AgentBinary = ".\bin\linux\mesh-agent",

  [string]$PrivateKeyPath = "",

  [int]$MaxUses = 10,

  [switch]$SkipBuild
)

$ErrorActionPreference = "Stop"

$root = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $root

if (-not $SkipBuild -and -not (Test-Path -LiteralPath $AgentBinary)) {
  powershell -ExecutionPolicy Bypass -File .\scripts\build-linux-agent.ps1 -Output $AgentBinary
}

if (-not (Test-Path -LiteralPath $AgentBinary)) {
  throw "Linux mesh-agent binary not found: $AgentBinary"
}
$agentBinaryPath = (Resolve-Path -LiteralPath $AgentBinary).Path

$envNames = @(
  "MESHLINK_E2E_SELF_RELAY",
  "MESHLINK_E2E_SSH_HOST",
  "MESHLINK_E2E_SSH_PORT",
  "MESHLINK_E2E_SSH_USER",
  "MESHLINK_E2E_SSH_PASSWORD",
  "MESHLINK_E2E_SSH_PRIVATE_KEY",
  "MESHLINK_E2E_SSH_PRIVATE_KEY_PATH",
  "MESHLINK_E2E_PUBLIC_ADDRESS",
  "MESHLINK_E2E_LISTEN_PORT",
  "MESHLINK_E2E_AGENT_BINARY",
  "MESHLINK_E2E_MAX_USES"
)

$oldEnv = @{}
foreach ($name in $envNames) {
  $oldEnv[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
}

$password = $null
try {
  [Environment]::SetEnvironmentVariable("MESHLINK_E2E_SELF_RELAY", "1", "Process")
  [Environment]::SetEnvironmentVariable("MESHLINK_E2E_SSH_HOST", $SshHost, "Process")
  [Environment]::SetEnvironmentVariable("MESHLINK_E2E_SSH_PORT", [string]$SshPort, "Process")
  [Environment]::SetEnvironmentVariable("MESHLINK_E2E_SSH_USER", $SshUser, "Process")
  [Environment]::SetEnvironmentVariable("MESHLINK_E2E_PUBLIC_ADDRESS", $PublicAddress, "Process")
  [Environment]::SetEnvironmentVariable("MESHLINK_E2E_LISTEN_PORT", [string]$ListenPort, "Process")
  [Environment]::SetEnvironmentVariable("MESHLINK_E2E_AGENT_BINARY", $agentBinaryPath, "Process")
  [Environment]::SetEnvironmentVariable("MESHLINK_E2E_MAX_USES", [string]$MaxUses, "Process")

  if ($PrivateKeyPath) {
    if (-not (Test-Path -LiteralPath $PrivateKeyPath)) {
      throw "SSH private key file not found: $PrivateKeyPath"
    }
    [Environment]::SetEnvironmentVariable("MESHLINK_E2E_SSH_PRIVATE_KEY_PATH", $PrivateKeyPath, "Process")
    [Environment]::SetEnvironmentVariable("MESHLINK_E2E_SSH_PASSWORD", $null, "Process")
  } else {
    $secure = Read-Host -AsSecureString "SSH password"
    $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try {
      $password = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr)
    } finally {
      [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr)
    }
    [Environment]::SetEnvironmentVariable("MESHLINK_E2E_SSH_PASSWORD", $password, "Process")
    [Environment]::SetEnvironmentVariable("MESHLINK_E2E_SSH_PRIVATE_KEY_PATH", $null, "Process")
  }

  go test -tags e2e ./internal/onboarding -run TestE2ESelfHostedRelayDeploysAndEnrollsTwoClients -v -count=1
  if ($LASTEXITCODE -ne 0) {
    throw "self-hosted relay E2E failed with exit code $LASTEXITCODE"
  }
} finally {
  foreach ($name in $envNames) {
    [Environment]::SetEnvironmentVariable($name, $oldEnv[$name], "Process")
  }
  $password = $null
}
