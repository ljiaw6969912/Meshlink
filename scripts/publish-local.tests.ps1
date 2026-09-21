$ErrorActionPreference = 'Stop'

function Assert-True([bool]$Condition, [string]$Message) {
  if (-not $Condition) { throw $Message }
}

function Assert-Throws([scriptblock]$Action, [string]$Message) {
  $failed = $false
  try { & $Action | Out-Null } catch { $failed = $true }
  Assert-True $failed $Message
}

# Load only helper function definitions. Never execute the elevated publication or
# packaging entry points, and never stop real services from this test.
foreach ($name in @('package-clean.ps1', 'publish-local.ps1')) {
  $path = Join-Path $PSScriptRoot $name
  Assert-True (Test-Path -LiteralPath $path -PathType Leaf) "$name is missing"
  $tokens = $null
  $parseErrors = $null
  $ast = [Management.Automation.Language.Parser]::ParseFile($path, [ref]$tokens, [ref]$parseErrors)
  Assert-True (@($parseErrors).Count -eq 0) "$name has PowerShell syntax errors"
  foreach ($definition in $ast.FindAll({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] }, $false)) {
    . ([scriptblock]::Create($definition.Extent.Text))
  }
}

$temporaryRoot = Join-Path ([IO.Path]::GetTempPath()) ('meshlink-publication-tests-' + [guid]::NewGuid().ToString('N'))
function Write-Fixture([string]$Relative, [string]$Content) {
  $path = Join-Path $temporaryRoot $Relative
  New-Item -ItemType Directory -Force -Path (Split-Path $path -Parent) | Out-Null
  [IO.File]::WriteAllText($path, $Content)
  return $path
}

try {
  $compiled = @('mesh-agent.exe', 'mesh-cloudhub.exe', 'mesh-desktop.exe', 'mesh-update-server.exe', 'meshctl.exe', 'linux/mesh-agent')
  $artifacts = @()
  foreach ($relative in $compiled) {
    $path = Write-Fixture ('bin/' + $relative) ('compiled-' + $relative)
    $artifacts += [pscustomobject]@{ path=$relative; sha256=(Get-FileHash -LiteralPath $path).Hash; size=(Get-Item -LiteralPath $path).Length }
  }
  $metadata = [pscustomobject]@{ schema='meshlink-build-v1'; version='0.1.7'; build_time='2026-09-20T10:00:00Z'; artifacts=$artifacts }
  $metadataPath = Write-Fixture 'bin/build-metadata.json' ($metadata | ConvertTo-Json -Depth 8)
  $actual = Read-MeshlinkBuildMetadata -BinDirectory (Join-Path $temporaryRoot 'bin') -Version '0.1.7'
  Assert-True ($actual.version -ceq '0.1.7') 'valid build was rejected'
  Assert-Throws { Read-MeshlinkBuildMetadata -BinDirectory (Join-Path $temporaryRoot 'bin') -Version '0.1.6' } 'a different VERSION was accepted'

  $metadata.artifacts = @($artifacts[0], $artifacts[0], $artifacts[2], $artifacts[3], $artifacts[4], $artifacts[5])
  [IO.File]::WriteAllText($metadataPath, ($metadata | ConvertTo-Json -Depth 8))
  Assert-Throws { Read-MeshlinkBuildMetadata -BinDirectory (Join-Path $temporaryRoot 'bin') -Version '0.1.7' } 'duplicate artifact hid a missing binary'
  $metadata.artifacts = $artifacts
  [IO.File]::WriteAllText($metadataPath, ($metadata | ConvertTo-Json -Depth 8))
  Write-Fixture 'bin/mesh-agent.exe' 'tampered' | Out-Null
  Assert-Throws { Read-MeshlinkBuildMetadata -BinDirectory (Join-Path $temporaryRoot 'bin') -Version '0.1.7' } 'tampered binary was accepted'

  # Runtime files are not limited to known folders. Hidden root files and a new
  # runtime directory must receive the same preservation guarantee as certs.
  foreach ($relative in @('release/meshlink/certs/key.pem', 'release/meshlink/runtime/session', 'release/meshlink/.desktop-state', 'release/meshlink/bin/device.db')) {
    Write-Fixture $relative 'private-test-fixture' | Out-Null
  }
  $releaseRoot = Join-Path $temporaryRoot 'release/meshlink'
  $before = Get-MeshlinkDataHashes -Root $releaseRoot
  Assert-True ($before.Count -eq 4) 'runtime snapshot omitted unknown or root-level data'
  Write-Fixture 'release/meshlink/bin/mesh-agent.exe' 'new-program' | Out-Null
  Assert-MeshlinkDataUnchanged -Root $releaseRoot -Before $before
  Write-Fixture 'release/meshlink/runtime/session' 'changed' | Out-Null
  Assert-Throws { Assert-MeshlinkDataUnchanged -Root $releaseRoot -Before $before } 'changed runtime data was accepted'
  Write-Fixture 'release/meshlink/runtime/session' 'private-test-fixture' | Out-Null
  Write-Fixture 'release/meshlink/runtime/new-state' 'unexpected' | Out-Null
  Assert-Throws { Assert-MeshlinkDataUnchanged -Root $releaseRoot -Before $before } 'unexpected runtime data was accepted'

  # Writes must stay inside the fixed release directory, even when a future
  # caller accidentally passes traversal or a program folder is a junction.
  Assert-Throws { Get-MeshlinkDestinationPath -Root $releaseRoot -Relative '../escape.exe' } 'path traversal escaped the release directory'
  $safe = Get-MeshlinkDestinationPath -Root $releaseRoot -Relative 'bin/mesh-agent.exe'
  Assert-True ($safe -eq (Join-Path $releaseRoot 'bin/mesh-agent.exe')) 'valid program destination was rejected'
  # Service/process APIs are replaced only inside this test process. The actual
  # shutdown/restoration code runs against a stateful Win32 fixture, never SCM.
  $script:fixtureServices = @(
    [pscustomobject]@{ Name='MeshlinkAgent'; State='Running'; StartMode='Auto'; PathName='C:\fixture\mesh-agent.exe' },
    [pscustomobject]@{ Name='MeshlinkStopped'; State='Stopped'; StartMode='Manual'; PathName='C:\fixture\mesh-cloudhub.exe' }
  )
  $script:fixtureProcesses = @(
    [pscustomobject]@{ Name='mesh-agent.exe'; ProcessId=910001; ExecutablePath='C:\fixture\mesh-agent.exe' },
    [pscustomobject]@{ Name='mesh-desktop.exe'; ProcessId=910002; ExecutablePath='C:\fixture\mesh-desktop.exe' }
  )
  $script:desktopStarts = @()
  $script:refuseStop = $false
  function Get-CimInstance([string]$ClassName) {
    if ($ClassName -eq 'Win32_Service') { return $script:fixtureServices }
    if ($ClassName -eq 'Win32_Process') { return $script:fixtureProcesses }
    throw 'Unexpected Win32 class in test'
  }
  function Invoke-CimMethod($InputObject, [string]$MethodName, [hashtable]$Arguments) {
    if ($MethodName -eq 'Change') {
      $InputObject.StartMode = if ($Arguments.StartMode -eq 'Automatic') { 'Auto' } else { $Arguments.StartMode }
    } elseif ($MethodName -eq 'StopService') {
      if ($InputObject.StartMode -ne 'Disabled') { throw 'Service recovery remained enabled during shutdown' }
      if (-not $script:refuseStop) { $InputObject.State = 'Stopped' }
    } else { throw 'Unexpected service method in test' }
    return [pscustomobject]@{ ReturnValue=0 }
  }
  function Stop-Process {
    [CmdletBinding()]
    param([int]$Id, [switch]$Force)
    if ($Id -notin @(910001, 910002)) { throw 'Refusing to stop a real process in test' }
    $script:fixtureProcesses = @($script:fixtureProcesses | Where-Object ProcessId -ne $Id)
  }
  function Start-Service([string]$Name) {
    $service = $script:fixtureServices | Where-Object Name -eq $Name
    if ($service.StartMode -eq 'Disabled') { throw 'Cannot restart a disabled service' }
    $service.State = 'Running'
  }
  function Get-Service([string]$Name) {
    $service = $script:fixtureServices | Where-Object Name -eq $Name
    $service | Add-Member -MemberType ScriptMethod -Name WaitForStatus -Value {
      param($Expected, $Timeout)
      if ($this.State -ne $Expected) { throw 'Fixture service did not reach requested state' }
    } -Force
    return $service
  }
  function Start-Process([string]$FilePath, [string]$WorkingDirectory, [string]$WindowStyle) {
    if ($FilePath -ne 'C:\fixture\mesh-desktop.exe' -or $WindowStyle -ne 'Hidden') { throw 'Unexpected desktop restart invocation' }
    $script:desktopStarts += $FilePath
  }
  $runtime = New-MeshlinkRuntimeState
  try {
    Stop-MeshlinkRuntime -State $runtime -TimeoutSeconds 2
    Assert-MeshlinkStopped
    Assert-True ($runtime.Services.MeshlinkAgent.StartMode -eq 'Auto' -and $runtime.Services.MeshlinkAgent.WasRunning) 'original service state was lost'
    Assert-True ($runtime.DesktopPaths.Count -eq 1) 'original desktop was not retained for restoration'
  } finally { Restore-MeshlinkRuntime $runtime }
  Assert-True ($script:fixtureServices[0].State -eq 'Running' -and $script:fixtureServices[0].StartMode -eq 'Auto') 'running service was not restored'
  Assert-True ($script:fixtureServices[1].State -eq 'Stopped' -and $script:fixtureServices[1].StartMode -eq 'Manual') 'previously stopped service was started or its mode changed'
  Assert-True ($script:desktopStarts.Count -eq 1) 'previous desktop was not restored exactly once'

  $script:refuseStop = $true
  $runtime = New-MeshlinkRuntimeState
  try {
    Assert-Throws { Stop-MeshlinkRuntime -State $runtime -TimeoutSeconds 0 } 'a service that never stopped was accepted'
  } finally { Restore-MeshlinkRuntime $runtime }
  Assert-True ($script:fixtureServices[0].State -eq 'Running' -and $script:fixtureServices[0].StartMode -eq 'Auto') 'failed shutdown did not restore original service mode'
  Write-Output 'publish-local tests passed (metadata, all-data preservation, containment, bounded shutdown and restoration)'
} finally {
  $resolvedTemporary = [IO.Path]::GetFullPath($temporaryRoot)
  $resolvedBase = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
  if (-not $resolvedTemporary.StartsWith($resolvedBase, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path $resolvedTemporary -Leaf) -notlike 'meshlink-publication-tests-*') {
    throw 'Refusing to clean an unexpected test directory'
  }
  if (Test-Path -LiteralPath $resolvedTemporary) { Remove-Item -LiteralPath $resolvedTemporary -Recurse -Force }
}
