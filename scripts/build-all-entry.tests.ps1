$ErrorActionPreference = 'Stop'
# Runs only a disposable build stub (including elevation), never Meshlink/SCM.
# A surviving child models the desktop that the real publisher restores.
$temporary = Join-Path ([IO.Path]::GetTempPath()) ("meshlink-build-entry-tests-" + [guid]::NewGuid().ToString('N') + " space's")
try {
  $scripts = Join-Path $temporary 'scripts'
  New-Item -ItemType Directory -Path $scripts -Force | Out-Null
  $batch = Join-Path $temporary 'build-all.bat'
  Copy-Item -LiteralPath (Join-Path $PSScriptRoot '../build-all.bat') -Destination $batch
  $stub = @'
#Requires -RunAsAdministrator
$child = Start-Process -FilePath powershell.exe -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 10' -WindowStyle Hidden -PassThru
[IO.File]::WriteAllText((Join-Path $PSScriptRoot 'child.txt'), [string]$child.Id)
exit 37
'@
  [IO.File]::WriteAllText((Join-Path $scripts 'build-all.ps1'), $stub)
  & cmd.exe /d /c ('"' + $batch + '" < NUL')
  if ($LASTEXITCODE -ne 37) { throw "Batch did not propagate the build's failure exit code: $LASTEXITCODE" }
  $childId = [int][IO.File]::ReadAllText((Join-Path $scripts 'child.txt'))
  if (-not (Get-Process -Id $childId -ErrorAction SilentlyContinue)) { throw 'Batch waited for the restored child process to exit' }
  Write-Output 'build-all entry tests passed (quoted path, elevation, exit code, waiting only for build process)'
} finally {
  $full = [IO.Path]::GetFullPath($temporary)
  $base = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
  if (-not $full.StartsWith($base, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path $full -Leaf) -notlike 'meshlink-build-entry-tests-*') { throw 'Unsafe test cleanup path' }
  if (Test-Path -LiteralPath $full) { Remove-Item -LiteralPath $full -Recurse -Force }
}
