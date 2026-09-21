$ErrorActionPreference = "Stop"

$root = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$subject = Join-Path $PSScriptRoot "build-clean.ps1"

function Assert-True([bool]$Condition, [string]$Message) {
  if (-not $Condition) {
    throw $Message
  }
}

function Write-TestFile([string]$Path, [string]$Content = "test") {
  $parent = Split-Path -Parent $Path
  New-Item -ItemType Directory -Force -Path $parent | Out-Null
  [System.IO.File]::WriteAllText($Path, $Content, [System.Text.Encoding]::ASCII)
}

function New-TestRoot([bool]$IncludeWintun) {
  $path = Join-Path ([System.IO.Path]::GetTempPath()) ("meshlink-build-clean-test-" + [guid]::NewGuid().ToString("N"))
  New-Item -ItemType Directory -Force -Path $path | Out-Null
  if ($IncludeWintun) {
    Write-TestFile (Join-Path $path "bin\wintun.dll") "wintun"
  }
  Write-TestFile (Join-Path $path "bin\mesh-agent.exe")
  Write-TestFile (Join-Path $path "bin\linux\mesh-agent")
  Write-TestFile (Join-Path $path "dist\meshlink.zip")
  Write-TestFile (Join-Path $path "release\meshlink-old.zip")
  Write-TestFile (Join-Path $path "configs\active.json")
  Write-TestFile (Join-Path $path "configs\logs\MeshlinkAgent.log")
  Write-TestFile (Join-Path $path "certs\ca-key.pem")
  Write-TestFile (Join-Path $path "invites\invites.json")
  return $path
}

$tempRoots = @()
try {
  Assert-True (Test-Path -LiteralPath $subject -PathType Leaf) "build-clean.ps1 is missing"

  $cleanRoot = New-TestRoot $true
  $tempRoots += $cleanRoot
  & powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File $subject `
    -RepositoryRoot $cleanRoot -CleanOnly -SkipProcessStop
  Assert-True ($LASTEXITCODE -eq 0) "clean-only command failed"
  Assert-True (Test-Path -LiteralPath (Join-Path $cleanRoot "bin\wintun.dll") -PathType Leaf) "wintun.dll was deleted"
  Assert-True (-not (Test-Path -LiteralPath (Join-Path $cleanRoot "bin\mesh-agent.exe"))) "old Windows binary was preserved"
  Assert-True (-not (Test-Path -LiteralPath (Join-Path $cleanRoot "bin\linux"))) "old Linux binary directory was preserved"
  Assert-True (-not (Test-Path -LiteralPath (Join-Path $cleanRoot "dist"))) "dist was preserved"
  Assert-True (-not (Test-Path -LiteralPath (Join-Path $cleanRoot "release"))) "release was preserved"
  foreach ($relative in @("configs\active.json", "configs\logs\MeshlinkAgent.log", "certs\ca-key.pem", "invites\invites.json")) {
    Assert-True (Test-Path -LiteralPath (Join-Path $cleanRoot $relative) -PathType Leaf) "$relative was deleted"
  }

  $missingRoot = New-TestRoot $false
  $tempRoots += $missingRoot
  $previousErrorAction = $ErrorActionPreference
  $ErrorActionPreference = "Continue"
  & powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File $subject `
    -RepositoryRoot $missingRoot -CleanOnly -SkipProcessStop *> $null
  $missingExitCode = $LASTEXITCODE
  $ErrorActionPreference = $previousErrorAction
  Assert-True ($missingExitCode -ne 0) "missing wintun.dll unexpectedly succeeded"
  Assert-True (Test-Path -LiteralPath (Join-Path $missingRoot "bin\mesh-agent.exe") -PathType Leaf) "cleanup started before wintun preflight failed"
  Assert-True (Test-Path -LiteralPath (Join-Path $missingRoot "dist\meshlink.zip") -PathType Leaf) "dist was deleted before wintun preflight failed"

  $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
  $principal = New-Object Security.Principal.WindowsPrincipal($identity)
  $isAdministrator = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
  if (-not $isAdministrator) {
    $adminRoot = New-TestRoot $true
    $tempRoots += $adminRoot
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    & powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File $subject `
      -RepositoryRoot $adminRoot -CleanOnly *> $null
    $adminExitCode = $LASTEXITCODE
    $ErrorActionPreference = $previousErrorAction
    Assert-True ($adminExitCode -ne 0) "non-administrator cleanup unexpectedly succeeded"
    Assert-True (Test-Path -LiteralPath (Join-Path $adminRoot "bin\mesh-agent.exe") -PathType Leaf) "cleanup started before administrator preflight failed"
  }

  Write-Output "build-clean tests passed"
} finally {
  foreach ($path in $tempRoots) {
    $full = [IO.Path]::GetFullPath($path)
    $base = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    if (-not $full.StartsWith($base, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path $full -Leaf) -notlike 'meshlink-build-clean-test-*') { throw 'Unsafe test cleanup path' }
    if (Test-Path -LiteralPath $path) {
      Remove-Item -LiteralPath $path -Recurse -Force
    }
  }
}
