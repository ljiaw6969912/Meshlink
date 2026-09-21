#Requires -RunAsAdministrator
param(
  [string]$SourceDirectory = 'C:\Users\Administrator\Desktop\wireguard\release\meshlink',
  [switch]$FunctionsOnly
)

$ErrorActionPreference = 'Stop'

function Get-MeshlinkCompiledFiles {
  return @('mesh-agent.exe', 'mesh-cloudhub.exe', 'mesh-desktop.exe', 'mesh-update-server.exe', 'meshctl.exe', 'linux/mesh-agent')
}

function Get-MeshlinkDistributionFiles {
  return @(
    'bin/mesh-agent.exe', 'bin/mesh-cloudhub.exe', 'bin/mesh-desktop.exe',
    'bin/mesh-update-server.exe', 'bin/meshctl.exe', 'bin/linux/mesh-agent', 'bin/wintun.dll',
    'start-meshlink.bat', 'LICENSE', 'THIRD_PARTY_NOTICES.md', 'VERSION',
    'build-metadata.json', '使用说明.txt'
  )
}

function Assert-MeshlinkNoReparsePoint([string]$Path) {
  $current = [IO.Path]::GetFullPath($Path)
  while ($current) {
    if (Test-Path -LiteralPath $current) {
      $item = Get-Item -LiteralPath $current -Force
      if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Refusing to access a release path through a link: $current"
      }
    }
    $current = Split-Path $current -Parent
  }
}

function Get-MeshlinkDestinationPath([string]$Root, [string]$Relative) {
  if ([IO.Path]::IsPathRooted($Relative) -or $Relative -match '(^|[\\/])\.\.([\\/]|$)') {
    throw 'Release destination must be a relative path without traversal'
  }
  $prefix = [IO.Path]::GetFullPath($Root).TrimEnd('\') + '\'
  $path = [IO.Path]::GetFullPath((Join-Path $Root $Relative.Replace('/', '\')))
  if (-not $path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'Release destination escaped its root' }
  Assert-MeshlinkNoReparsePoint $path
  return $path
}

function Read-MeshlinkBuildMetadata([string]$BinDirectory, [string]$Version, [string]$MetadataPath = '') {
  if (-not $MetadataPath) { $MetadataPath = Join-Path $BinDirectory 'build-metadata.json' }
  $metadata = Get-Content -LiteralPath $MetadataPath -Raw -Encoding UTF8 | ConvertFrom-Json
  if ($metadata.schema -cne 'meshlink-build-v1' -or $metadata.version -cne $Version) { throw 'VERSION or schema does not match build metadata' }
  $expected = @(Get-MeshlinkCompiledFiles)
  if (@($metadata.artifacts).Count -ne $expected.Count) { throw 'Expected six compiled artifacts' }
  $seen = @{}
  foreach ($artifact in $metadata.artifacts) {
    $relative = [string]$artifact.path
    if ($relative -cnotin $expected -or $seen.ContainsKey($relative)) { throw "Unexpected or duplicate build artifact: $relative" }
    $seen[$relative] = $true
    $file = Get-Item -LiteralPath (Join-Path $BinDirectory $relative.Replace('/', '\'))
    if ($file.PSIsContainer -or $file.Length -ne $artifact.size -or (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash -ne $artifact.sha256) {
      throw "Build metadata mismatch: $relative"
    }
  }
  return $metadata
}

function Get-MeshlinkDataHashes([string]$Root) {
  $hashes = @{}
  if (-not (Test-Path -LiteralPath $Root)) { return $hashes }
  Assert-MeshlinkNoReparsePoint $Root
  $prefix = [IO.Path]::GetFullPath($Root).TrimEnd('\') + '\'
  $distribution = @(Get-MeshlinkDistributionFiles)
  $pending = New-Object 'System.Collections.Generic.Queue[string]'
  $pending.Enqueue($Root)
  while ($pending.Count -gt 0) {
    $directory = $pending.Dequeue()
    foreach ($item in @(Get-ChildItem -LiteralPath $directory -Force)) {
      if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Runtime data contains a link: $($item.FullName)" }
      if ($item.PSIsContainer) { $pending.Enqueue($item.FullName); continue }
      $relative = $item.FullName.Substring($prefix.Length).Replace('\', '/')
      if ($relative -notin $distribution) { $hashes[$relative] = (Get-FileHash -LiteralPath $item.FullName -Algorithm SHA256).Hash }
    }
  }
  return $hashes
}

function Assert-MeshlinkDataUnchanged([string]$Root, [hashtable]$Before) {
  $after = Get-MeshlinkDataHashes $Root
  if ($Before.Count -ne $after.Count) { throw 'Runtime data file count changed' }
  foreach ($relative in $Before.Keys) {
    if ($after[$relative] -ne $Before[$relative]) { throw "Runtime data changed: $relative" }
  }
}

function Get-MeshlinkServices {
  return @(Get-CimInstance Win32_Service | Where-Object { $_.Name -like 'Meshlink*' -or $_.PathName -match 'mesh-(agent|cloudhub|update-server)\.exe' })
}

function Get-MeshlinkProcesses {
  return @(Get-CimInstance Win32_Process | Where-Object { $_.Name -match '^(mesh-(agent|desktop|cloudhub|update-server)|meshctl)\.exe$' })
}

function New-MeshlinkRuntimeState {
  return @{ Services=@{}; DesktopPaths=@{} }
}

function Assert-MeshlinkStopped {
  if (@(Get-MeshlinkServices | Where-Object State -ne 'Stopped').Count -gt 0) { throw 'Meshlink services must stop before changing release artifacts' }
  if (@(Get-MeshlinkProcesses).Count -gt 0) { throw 'Meshlink processes must stop before changing release artifacts' }
}

function Stop-MeshlinkRuntime([hashtable]$State, [int]$TimeoutSeconds = 45) {
  $deadline = [datetime]::UtcNow.AddSeconds($TimeoutSeconds)
  $quietChecks = 0
  do {
    # Disable before stopping: pending SCM recovery actions cannot restart a
    # disabled service. Keep the original mode even if shutdown later fails.
    foreach ($service in @(Get-MeshlinkServices)) {
      if (-not $State.Services.ContainsKey($service.Name)) {
        $State.Services[$service.Name] = @{ StartMode=$service.StartMode; WasRunning=($service.State -in @('Running', 'Start Pending')); RestoreStartMode=$false }
      }
      if ($service.StartMode -ne 'Disabled') {
        $State.Services[$service.Name].RestoreStartMode = $true
        $change = Invoke-CimMethod -InputObject $service -MethodName Change -Arguments @{ StartMode='Disabled' }
        if ($change.ReturnValue -ne 0) { throw "Could not disable service recovery: $($service.Name) ($($change.ReturnValue))" }
      }
      if ($service.State -ne 'Stopped') {
        $stop = Invoke-CimMethod -InputObject $service -MethodName StopService
        if ($stop.ReturnValue -notin @(0, 5, 6, 10)) { throw "Could not request service stop: $($service.Name) ($($stop.ReturnValue))" }
      }
    }
    foreach ($process in @(Get-MeshlinkProcesses)) {
      if ($process.Name -ieq 'mesh-desktop.exe' -and $process.ExecutablePath) { $State.DesktopPaths[$process.ExecutablePath] = $true }
      try { Stop-Process -Id $process.ProcessId -Force -ErrorAction Stop } catch {
        if (Get-Process -Id $process.ProcessId -ErrorAction SilentlyContinue) { throw }
      }
    }
    $activeServices = @(Get-MeshlinkServices | Where-Object State -ne 'Stopped')
    if ($activeServices.Count -eq 0 -and @(Get-MeshlinkProcesses).Count -eq 0) { $quietChecks++ } else { $quietChecks = 0 }
    if ($quietChecks -ge 2) { Assert-MeshlinkStopped; return }
    Start-Sleep -Milliseconds 250
  } while ([datetime]::UtcNow -lt $deadline)
  throw 'Timed out waiting for all Meshlink services and processes to stop; no release files may be overwritten'
}

function Restore-MeshlinkRuntime([hashtable]$State) {
  $failures = New-Object 'System.Collections.Generic.List[string]'
  foreach ($name in $State.Services.Keys) {
    $saved = $State.Services[$name]
    if (-not $saved.RestoreStartMode) { continue }
    try {
      $service = Get-CimInstance Win32_Service | Where-Object Name -eq $name
      if (-not $service) { throw 'service disappeared' }
      $mode = if ($saved.StartMode -eq 'Auto') { 'Automatic' } else { $saved.StartMode }
      $change = Invoke-CimMethod -InputObject $service -MethodName Change -Arguments @{ StartMode=$mode }
      if ($change.ReturnValue -ne 0) { throw "start mode restore returned $($change.ReturnValue)" }
    } catch { $failures.Add("Restore service mode $name`: $($_.Exception.Message)") }
  }
  foreach ($name in $State.Services.Keys) {
    if (-not $State.Services[$name].WasRunning) { continue }
    try {
      Start-Service -Name $name
      (Get-Service -Name $name).WaitForStatus('Running', [TimeSpan]::FromSeconds(30))
    } catch { $failures.Add("Restart service $name`: $($_.Exception.Message)") }
  }
  foreach ($path in $State.DesktopPaths.Keys) {
    try { Start-Process -FilePath $path -WorkingDirectory (Split-Path $path -Parent) -WindowStyle Hidden | Out-Null }
    catch { $failures.Add("Restart desktop: $($_.Exception.Message)") }
  }
  if ($failures.Count -gt 0) { throw ($failures -join '; ') }
}

if ($FunctionsOnly) { return }

Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$sourceRoot = (Resolve-Path -LiteralPath $SourceDirectory).Path.TrimEnd('\')
if ((Split-Path $sourceRoot -Leaf) -ne 'meshlink') { throw 'Source directory must be the meshlink release folder' }
$releaseRoot = Split-Path $sourceRoot -Parent
$destination = Get-MeshlinkDestinationPath -Root $releaseRoot -Relative 'meshlink-无配置.zip'
$temporary = Join-Path $releaseRoot ('.meshlink-clean-' + [guid]::NewGuid().ToString('N') + '.tmp')
$fileNames = @(Get-MeshlinkDistributionFiles | Where-Object { $_ -notin @('build-metadata.json', '使用说明.txt') })
$runtime = New-MeshlinkRuntimeState
$before = $null
$failures = New-Object 'System.Collections.Generic.List[string]'
$result = $null
try {
  Stop-MeshlinkRuntime $runtime
  $before = Get-MeshlinkDataHashes $sourceRoot
  $version = (Get-Content -LiteralPath (Join-Path $sourceRoot 'VERSION') -Raw -Encoding UTF8).Trim()
  $metadata = Read-MeshlinkBuildMetadata -BinDirectory (Join-Path $sourceRoot 'bin') -Version $version -MetadataPath (Join-Path $sourceRoot 'build-metadata.json')
  $expectedHashes = @{}
  foreach ($relative in $fileNames) {
    $path = Get-MeshlinkDestinationPath -Root $sourceRoot -Relative $relative
    $expectedHashes[$relative] = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash
  }
  if ($metadata.source) { $metadata.source.PSObject.Properties.Remove('directory') }
  $instructions = @"
Meshlink $($metadata.version) — 无配置分发包

请完整解压，得到一个 meshlink 文件夹，再双击 start-meshlink.bat。
启动时会自动申请管理员权限，请允许 Windows 权限提示。

客户端：在“加入已有网络”填写服务器提供的邀请链接和接入码，点击“连接”。已有身份会自动复用，无需区分首次连接和重新连接。
服务器：在“创建服务器”设置公网地址和监听端口，点击“启动服务器”。路由器将该端口的 TCP 和 UDP 都映射至此机器。服务器本机也会加入组网，默认虚拟 IP 为 10.77.0.1。
接入码可供多台设备使用；只有重新生成接入码时旧码才失效。

关闭桌面窗口后，组网服务继续在后台运行。已安装的服务随 Windows 自动启动。
再次打开程序会恢复上次的服务器或客户端模式，并显示保存的连接信息。
控制连接或心跳暂时失败时，客户端会自动重连；上线后会主动发现并连接其他节点。
需要停止组网时，请使用界面中的停止或断开操作。
“连接详情”可以查看完整设备信息；顶部“设备”菜单提供重命名、禁用和移除，“诊断”菜单提供一键诊断及远程桌面诊断。

本包包含程序、Wintun 驱动、启动入口、构建信息和许可说明。
不携带任何机器的网络配置、证书、私钥、邀请码、设备列表、桌面状态或日志。
所需配置和证书在创建或加入网络时生成。

更新已安装的机器：先停止 Meshlink 服务并退出程序，再覆盖程序文件，保留全部原有运行数据，包括 configs、certs、invites、logs、profiles 和 runtime。
切换服务器或客户端模式时，两种身份分别保存在本机目录中。
"@
  $utf8 = New-Object Text.UTF8Encoding($false)
  $generated = @{
    '使用说明.txt' = $utf8.GetBytes($instructions)
    'build-metadata.json' = $utf8.GetBytes(($metadata | ConvertTo-Json -Depth 10))
  }
  Assert-MeshlinkStopped
  $localInstructions = "Meshlink $version 本机运行目录`r`n`r`n此目录保留本机配置。对外分发请使用上一层的 meshlink-无配置.zip。`r`n`r`n双击 start-meshlink.bat，程序自动申请管理员权限。`r`n关闭窗口后组网服务继续运行，已安装的服务随 Windows 自动启动。重新打开会恢复上次模式和保存的连接信息。`r`n控制连接或心跳失败后自动重连，上线后主动发现并连接其他节点。需要停止组网时，请使用停止或断开操作。`r`n服务器：填写公网地址和端口，启动后将同一端口的 TCP、UDP 映射至本机。本机虚拟 IP 为 10.77.0.1。`r`n客户端：输入邀请链接和接入码，点击连接；已有身份自动复用。`r`n升级时保留全部本机运行数据，包括 configs、certs、invites、logs、profiles 和 runtime。`r`n"
  [IO.File]::WriteAllText((Get-MeshlinkDestinationPath -Root $sourceRoot -Relative '使用说明.txt'), $localInstructions, $utf8)
  $zipStream = [IO.File]::Open($temporary, [IO.FileMode]::CreateNew)
  try {
    $zip = New-Object IO.Compression.ZipArchive($zipStream, [IO.Compression.ZipArchiveMode]::Create, $true, [Text.Encoding]::UTF8)
    try {
      foreach ($relative in $fileNames) {
        $entryStream = $zip.CreateEntry('meshlink/' + $relative, [IO.Compression.CompressionLevel]::Optimal).Open()
        try {
          $inputStream = [IO.File]::OpenRead((Get-MeshlinkDestinationPath -Root $sourceRoot -Relative $relative))
          try { $inputStream.CopyTo($entryStream) } finally { $inputStream.Dispose() }
        } finally { $entryStream.Dispose() }
      }
      foreach ($relative in $generated.Keys) {
        $bytes = $generated[$relative]
        $entryStream = $zip.CreateEntry('meshlink/' + $relative).Open()
        try { $entryStream.Write($bytes, 0, $bytes.Length) } finally { $entryStream.Dispose() }
        $sha = [Security.Cryptography.SHA256]::Create()
        try { $expectedHashes[$relative] = [BitConverter]::ToString($sha.ComputeHash($bytes)).Replace('-', '') } finally { $sha.Dispose() }
      }
    } finally { $zip.Dispose() }
  } finally { $zipStream.Dispose() }

  $zip = [IO.Compression.ZipFile]::OpenRead($temporary)
  try {
    if ($zip.Entries.Count -ne $expectedHashes.Count) { throw 'Unexpected archive file count' }
    $seen = @{}
    foreach ($entry in $zip.Entries) {
      if (-not $entry.FullName.StartsWith('meshlink/')) { throw 'Invalid archive root' }
      $relative = $entry.FullName.Substring(9)
      if (-not $expectedHashes.ContainsKey($relative) -or $seen.ContainsKey($relative)) { throw "Unexpected archive entry: $relative" }
      $seen[$relative] = $true
      $inputStream = $entry.Open()
      $sha = [Security.Cryptography.SHA256]::Create()
      try { $hash = [BitConverter]::ToString($sha.ComputeHash($inputStream)).Replace('-', '') } finally { $sha.Dispose(); $inputStream.Dispose() }
      if ($hash -ne $expectedHashes[$relative]) { throw "Archive hash mismatch: $relative" }
    }
  } finally { $zip.Dispose() }
  Assert-MeshlinkDataUnchanged -Root $sourceRoot -Before $before
  Assert-MeshlinkStopped
  if (Test-Path -LiteralPath $destination) { [IO.File]::Replace($temporary, $destination, [System.Management.Automation.Language.NullString]::Value) }
  else { [IO.File]::Move($temporary, $destination) }
  $result = [pscustomobject]@{ Archive=$destination; Files=$expectedHashes.Count; Size=(Get-Item -LiteralPath $destination).Length; PreservedDataFiles=$before.Count; BuildTime=$metadata.build_time; StoppedBeforePackaging=$true }
} catch { $failures.Add($_.Exception.Message) }
finally {
  try { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force } } catch { $failures.Add($_.Exception.Message) }
  try { if ($null -ne $before) { Assert-MeshlinkDataUnchanged -Root $sourceRoot -Before $before } } catch { $failures.Add($_.Exception.Message) }
  try { Restore-MeshlinkRuntime $runtime } catch { $failures.Add($_.Exception.Message) }
}
if ($failures.Count -gt 0) { throw ($failures -join '; ') }
$result | ConvertTo-Json
