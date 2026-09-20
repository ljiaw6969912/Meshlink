param(
  [switch]$PrivatePackage,
  [string]$RepositoryRoot = "",
  [switch]$CleanOnly,
  [switch]$SkipProcessStop
)

$ErrorActionPreference = "Stop"

function Test-IsAdministrator {
  $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
  $principal = New-Object Security.Principal.WindowsPrincipal($identity)
  return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-CanonicalPath([string]$Path) {
  if ([string]::IsNullOrWhiteSpace($Path)) {
    return ""
  }
  try {
    return (Get-Item -LiteralPath $Path -Force -ErrorAction Stop).FullName
  } catch {
    try {
      return (Resolve-Path -LiteralPath $Path -ErrorAction Stop).Path
    } catch {
      return [System.IO.Path]::GetFullPath($Path)
    }
  }
}

function Test-PathUnder([string]$Path, [string]$Parent) {
  if ([string]::IsNullOrWhiteSpace($Path)) {
    return $false
  }
  $candidate = Get-CanonicalPath $Path
  $root = (Get-CanonicalPath $Parent).TrimEnd('\')
  return $candidate.StartsWith($root + '\', [System.StringComparison]::OrdinalIgnoreCase)
}

function Get-ServiceExecutable([string]$PathName) {
  if ([string]::IsNullOrWhiteSpace($PathName)) {
    return ""
  }
  $value = $PathName.Trim()
  if ($value.StartsWith('"')) {
    $end = $value.IndexOf('"', 1)
    if ($end -gt 1) {
      return $value.Substring(1, $end - 1)
    }
  }
  $match = [regex]::Match($value, '^(.*?\.exe~?)(?:\s|$)', [System.Text.RegularExpressions.RegexOptions]::IgnoreCase)
  if ($match.Success) {
    return $match.Groups[1].Value
  }
  return ""
}

function Stop-RepositoryProcesses([string]$BinDirectory) {
  foreach ($service in @(Get-CimInstance Win32_Service -ErrorAction SilentlyContinue)) {
    $executable = Get-ServiceExecutable $service.PathName
    if (-not (Test-PathUnder $executable $BinDirectory)) {
      continue
    }
    if ($service.State -ne "Stopped") {
      Write-Output "Stopping repository service: $($service.Name)"
      Stop-Service -Name $service.Name -Force -ErrorAction Stop
      $controller = Get-Service -Name $service.Name -ErrorAction Stop
      $controller.WaitForStatus([System.ServiceProcess.ServiceControllerStatus]::Stopped, [TimeSpan]::FromSeconds(20))
    }
  }

  foreach ($process in @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue)) {
    if ($process.ProcessId -eq $PID -or [string]::IsNullOrWhiteSpace($process.ExecutablePath)) {
      continue
    }
    if (Test-PathUnder $process.ExecutablePath $BinDirectory) {
      Write-Output "Stopping repository process: $($process.Name) ($($process.ProcessId))"
      Stop-Process -Id $process.ProcessId -Force -ErrorAction Stop
    }
  }
}

function Remove-SafeTree([string]$Path, [string]$Root) {
  $full = [System.IO.Path]::GetFullPath($Path)
  $rootFull = [System.IO.Path]::GetFullPath($Root).TrimEnd('\')
  if (-not $full.StartsWith($rootFull + '\', [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to remove path outside repository: $full"
  }
  if (Test-Path -LiteralPath $full) {
    Remove-Item -LiteralPath $full -Recurse -Force
  }
}

if ([string]::IsNullOrWhiteSpace($RepositoryRoot)) {
  $RepositoryRoot = Join-Path $PSScriptRoot ".."
}
$root = Get-CanonicalPath $RepositoryRoot
$bin = Join-Path $root "bin"
$wintun = Join-Path $bin "wintun.dll"
$releaseScript = Join-Path $root "scripts\release.ps1"

if (-not (Test-Path -LiteralPath $wintun -PathType Leaf)) {
  throw "Required runtime is missing before cleanup: $wintun"
}
if (-not $CleanOnly) {
  if (-not (Test-Path -LiteralPath $releaseScript -PathType Leaf)) {
    throw "Release script is missing before cleanup: $releaseScript"
  }
  if ($null -eq (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go is not available in PATH"
  }
}
if (-not $SkipProcessStop -and -not (Test-IsAdministrator)) {
  throw "Administrator privileges are required to stop repository services and replace running binaries. Run build-all.bat and accept the UAC prompt."
}

if (-not $SkipProcessStop) {
  Stop-RepositoryProcesses $bin
}

Write-Output "Cleaning old repository build outputs..."
foreach ($item in @(Get-ChildItem -LiteralPath $bin -Force)) {
  if ($item.Name -ieq "wintun.dll") {
    continue
  }
  if ($item.PSIsContainer) {
    Remove-Item -LiteralPath $item.FullName -Recurse -Force
  } else {
    Remove-Item -LiteralPath $item.FullName -Force
  }
}
Remove-SafeTree (Join-Path $root "dist") $root
Remove-SafeTree (Join-Path $root "release") $root

if ($CleanOnly) {
  Write-Output "Old build outputs removed; wintun.dll and runtime data were preserved."
  exit 0
}

$releaseArgs = @{ Mode = "Development" }
if ($PrivatePackage) {
  $releaseArgs.PrivatePackage = $true
}

Write-Output "Starting verified Meshlink development build..."
& $releaseScript @releaseArgs
if ($LASTEXITCODE -ne 0) {
  throw "Meshlink build failed with exit code $LASTEXITCODE"
}

Write-Output "Build completed successfully."
Write-Output "Standard package: $(Join-Path $root ('release\meshlink-' + (Get-Content -Raw (Join-Path $root 'VERSION')).Trim() + '.zip'))"
if ($PrivatePackage) {
  Write-Output "Private package: $(Join-Path $root ('dist\private\meshlink-private-' + (Get-Content -Raw (Join-Path $root 'VERSION')).Trim() + '.zip'))"
}
