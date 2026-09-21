$ErrorActionPreference = 'Stop'

function Assert-True([bool]$Condition, [string]$Message) {
  if (-not $Condition) { throw $Message }
}

# Load the real orchestration function without running the elevated entry point.
# Native builds and SCM are external boundaries; fixtures must never touch them.
$subject = Join-Path $PSScriptRoot 'build-all.ps1'
Assert-True (Test-Path -LiteralPath $subject) 'The safe one-click build pipeline is missing'
$tokens = $null
$errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($subject, [ref]$tokens, [ref]$errors)
Assert-True (@($errors).Count -eq 0) 'Build pipeline syntax is invalid'
foreach ($definition in $ast.FindAll({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] }, $false)) {
  . ([scriptblock]::Create($definition.Extent.Text))
}

$temporary = Join-Path ([IO.Path]::GetTempPath()) ('meshlink-build-all-tests-' + [guid]::NewGuid().ToString('N'))
$global:meshlinkBuildTestEvents = New-Object 'System.Collections.Generic.List[string]'
function go {
  $step = if ($args[0] -eq 'test') { 'test' } elseif ($args[0] -eq 'vet') { 'vet' } else { throw 'Unexpected Go invocation' }
  if ($step -eq 'test') { Assert-True ($args -contains 'github.com/lxn/walk') 'Walk shutdown regression tests were omitted' }
  $global:meshlinkBuildTestEvents.Add($step)
  $global:LASTEXITCODE = if ($global:meshlinkBuildTestFailure -eq $step) { 9 } else { 0 }
}
function New-MeshlinkRuntimeState { return @{ original='running' } }
function Stop-MeshlinkRuntime($State) {
  $global:meshlinkBuildTestEvents.Add('stop')
  if ($global:meshlinkBuildTestFailure -eq 'stop') { throw 'Fixture shutdown failure' }
}
function Restore-MeshlinkRuntime($State) {
  Assert-True ($State.original -eq 'running') 'Original runtime state was discarded'
  $global:meshlinkBuildTestEvents.Add('restore')
}
function Assert-MeshlinkStopped { }

try {
  $scripts = Join-Path $temporary 'scripts'
  New-Item -ItemType Directory -Path $scripts -Force | Out-Null
  New-Item -ItemType Directory -Path (Join-Path $temporary 'release/meshlink/configs') -Force | Out-Null
  $private = Join-Path $temporary 'release/meshlink/configs/active.json'
  [IO.File]::WriteAllText($private, 'existing-local-identity')
  foreach ($step in @('windows', 'linux', 'publish')) {
    $file = switch ($step) { windows { 'build.ps1' } linux { 'build-linux-coordinator.ps1' } publish { 'publish-local.ps1' } }
    $body = @'
param([switch]$SkipTests, [string]$RepositoryRoot, [switch]$IncludeLinux)
$global:meshlinkBuildTestEvents.Add('STEP')
if ('STEP' -eq 'windows' -and -not $SkipTests) { throw 'Tests would run again while the service is stopped' }
if ('STEP' -eq 'publish' -and -not $IncludeLinux) { throw 'Linux packages were omitted from publication' }
if ($global:meshlinkBuildTestFailure -eq 'STEP') { throw 'Fixture STEP failure' }
if ($global:meshlinkBuildTestFailure -eq 'STEP-exit') { $global:LASTEXITCODE = 23; return }
if ('STEP' -eq 'publish') { [pscustomobject]@{succeeded=$true; version='0.1.14'} | ConvertTo-Json }
'@
    [IO.File]::WriteAllText((Join-Path $scripts $file), $body.Replace('STEP', $step))
  }
  $cases = @(
    @{fail=''; want='test,vet,stop,windows,linux,publish,restore'},
    @{fail='test'; want='test'},
    @{fail='vet'; want='test,vet'},
    @{fail='stop'; want='test,vet,stop,restore'},
    @{fail='windows'; want='test,vet,stop,windows,restore'},
    @{fail='windows-exit'; want='test,vet,stop,windows,restore'},
    @{fail='linux'; want='test,vet,stop,windows,linux,restore'},
    @{fail='linux-exit'; want='test,vet,stop,windows,linux,restore'},
    @{fail='publish'; want='test,vet,stop,windows,linux,publish,restore'}
  )
  foreach ($case in $cases) {
    $global:meshlinkBuildTestEvents.Clear()
    $global:meshlinkBuildTestFailure = $case.fail
    $failed = $false
    try { Invoke-MeshlinkBuildAll -Root $temporary | Out-Null } catch { $failed = $true; $failureMessage = $_.Exception.Message }
    Assert-True ($failed -eq [bool]$case.fail) "Incorrect failure result at '$($case.fail)': $failureMessage"
    Assert-True (($global:meshlinkBuildTestEvents -join ',') -ceq $case.want) "Unsafe step ordering at '$($case.fail)': $($global:meshlinkBuildTestEvents -join ',')"
    Assert-True ([IO.File]::ReadAllText($private) -ceq 'existing-local-identity') 'Existing release identity was changed'
  }
  Write-Output 'build-all tests passed (success, failure ordering, runtime restoration, data preservation)'
} finally {
  $full = [IO.Path]::GetFullPath($temporary)
  $base = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
  if (-not $full.StartsWith($base, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path $full -Leaf) -notlike 'meshlink-build-all-tests-*') { throw 'Unsafe test cleanup path' }
  if (Test-Path -LiteralPath $full) { Remove-Item -LiteralPath $full -Recurse -Force }
}
