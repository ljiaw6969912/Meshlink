#Requires -RunAsAdministrator
$ErrorActionPreference = 'Stop'

function Invoke-MeshlinkBuildScript([string]$Path, [hashtable]$Parameters = @{}) {
  $global:LASTEXITCODE = 0
  & $Path @Parameters | Out-Host
  if ($LASTEXITCODE -ne 0) { throw "Build step failed: $Path (exit $LASTEXITCODE)" }
}

function Invoke-MeshlinkBuildAll([string]$Root) {
  Push-Location -LiteralPath $Root
  try {
    Write-Host '[1/5] Running all Go tests...'
    & go test ./... github.com/lxn/walk -count=1 -timeout=180s | Out-Host
    if ($LASTEXITCODE -ne 0) { throw 'Go tests failed; publication was not started' }
    & go vet ./... | Out-Host
    if ($LASTEXITCODE -ne 0) { throw 'Go vet failed; publication was not started' }

    $runtime = New-MeshlinkRuntimeState
    try {
      Write-Host '[2/5] Stopping Meshlink before replacing compiled programs...'
      Stop-MeshlinkRuntime $runtime
      Assert-MeshlinkStopped
      Write-Host '[3/5] Building Windows programs...'
      Invoke-MeshlinkBuildScript -Path (Join-Path $Root 'scripts/build.ps1') -Parameters @{SkipTests=$true}
      Write-Host '[4/5] Building and verifying Linux amd64/arm64 packages...'
      Invoke-MeshlinkBuildScript -Path (Join-Path $Root 'scripts/build-linux-coordinator.ps1')
      Write-Host '[5/5] Publishing fixed packages while preserving runtime data...'
      $publication = (& (Join-Path $Root 'scripts/publish-local.ps1') -RepositoryRoot $Root -IncludeLinux | Out-String) | ConvertFrom-Json
      if (-not $publication.succeeded) { throw 'Publication verification failed' }
    } finally {
      Restore-MeshlinkRuntime $runtime
    }
    return $publication
  } finally {
    Pop-Location
  }
}

$root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$cache = Join-Path $root '.cache'
. (Join-Path $PSScriptRoot 'package-clean.ps1') -FunctionsOnly
Assert-MeshlinkNoReparsePoint $cache
New-Item -ItemType Directory -Force -Path $cache | Out-Null
# Concurrent runs must not overwrite a running build's binaries or log.
$lock = [IO.File]::Open((Join-Path $cache 'build-all.lock'), [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
$report = [ordered]@{succeeded=$false; state='running'; started_at=[datetime]::UtcNow.ToString('o'); errors=@()}
$utf8 = New-Object Text.UTF8Encoding($false)
$resultPath = Join-Path $cache 'build-all-result.json'
$transcript = $false
try {
  [IO.File]::WriteAllText($resultPath, ($report | ConvertTo-Json -Depth 8), $utf8)
  Start-Transcript -LiteralPath (Join-Path $cache 'build-all.log') -Force | Out-Null
  $transcript = $true
  foreach ($tool in @('go', 'git')) {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) { throw "Required tool is missing from PATH: $tool" }
  }
  foreach ($relative in @('VERSION', 'bin/wintun.dll', 'scripts/build.ps1', 'scripts/build-linux-coordinator.ps1', 'scripts/publish-local.ps1')) {
    if (-not (Test-Path -LiteralPath (Join-Path $root $relative) -PathType Leaf)) { throw "Required build file is missing: $relative" }
  }
  $report.publication = Invoke-MeshlinkBuildAll -Root $root
  $report.succeeded = $true
  $report.state = 'complete'
  Write-Host "Build and publication completed: $($report.publication.version)"
  Write-Host "Output: $($report.publication.directory)"
  Write-Host "Windows package: $($report.publication.archive)"
  foreach ($archive in $report.publication.linux_archives) { Write-Host "Linux package: $($archive.path)" }
} catch {
  $report.state = 'failed'
  $report.errors = @($_.Exception.Message)
  Write-Host "Build failed: $($_.Exception.Message)" -ForegroundColor Red
} finally {
  try {
    $report.finished_at = [datetime]::UtcNow.ToString('o')
    [IO.File]::WriteAllText($resultPath, ($report | ConvertTo-Json -Depth 8), $utf8)
    if ($transcript) { Stop-Transcript | Out-Null }
  } finally { $lock.Dispose() }
}
if (-not $report.succeeded) { throw ($report.errors -join '; ') }
