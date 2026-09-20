#Requires -RunAsAdministrator
param(
  [string]$SourceDirectory = 'C:\Users\Administrator\Desktop\wireguard\release\meshlink'
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$sourceRoot = (Resolve-Path -LiteralPath $SourceDirectory).Path.TrimEnd('\')
if ((Split-Path $sourceRoot -Leaf) -ne 'meshlink') { throw 'Source directory must be the meshlink release folder' }
$releaseRoot = Split-Path $sourceRoot -Parent
$destination = Join-Path $releaseRoot 'meshlink-无配置.zip'
$temporary = Join-Path $releaseRoot ('.meshlink-clean-' + [guid]::NewGuid().ToString('N') + '.tmp')
$fileNames = @(
  'bin/mesh-agent.exe', 'bin/mesh-cloudhub.exe', 'bin/mesh-desktop.exe',
  'bin/mesh-update-server.exe', 'bin/meshctl.exe', 'bin/linux/mesh-agent', 'bin/wintun.dll',
  'start-meshlink.bat', 'LICENSE', 'THIRD_PARTY_NOTICES.md', 'VERSION'
)
$metadata = Get-Content -LiteralPath (Join-Path $sourceRoot 'build-metadata.json') -Raw | ConvertFrom-Json
if ((Get-Content -LiteralPath (Join-Path $sourceRoot 'VERSION') -Raw).Trim() -cne $metadata.version) { throw 'VERSION does not match build metadata' }
$expectedHashes = @{}
foreach ($relative in $fileNames) {
  $path = Join-Path $sourceRoot $relative.Replace('/', '\')
  $expectedHashes[$relative] = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash
}
foreach ($artifact in $metadata.artifacts) {
  $relative = 'bin/' + $artifact.path
  if (-not $expectedHashes.ContainsKey($relative) -or $expectedHashes[$relative] -ne $artifact.sha256) {
    throw "Build metadata mismatch: $relative"
  }
}
if (@($metadata.artifacts).Count -ne 6) { throw 'Expected six compiled artifacts' }
if ($metadata.source) { $metadata.source.PSObject.Properties.Remove('directory') }
$instructions = @"
Meshlink $($metadata.version) — 无配置分发包

请完整解压，得到一个 meshlink 文件夹，再双击 start-meshlink.bat。
启动时会自动申请管理员权限，请允许 Windows 权限提示。

客户端：在“加入已有网络”填写服务器提供的邀请链接和接入码，点击“连接”。已有身份会自动复用，无需区分首次连接和重新连接。
服务器：在“创建服务器”设置公网地址和监听端口，点击“启动服务器”。路由器将该端口的 TCP 和 UDP 都映射至此机器。服务器本机也会加入组网，默认虚拟 IP 为 10.77.0.1。
接入码可供多台设备使用；只有重新生成接入码时旧码才失效。

本包包含程序、Wintun 驱动、启动入口、构建信息和许可说明。
不携带任何机器的网络配置、证书、私钥、邀请码、设备列表或日志。
所需配置和证书在创建或加入网络时生成。

更新已安装的机器：先停止 Meshlink 服务并退出程序，再覆盖程序文件，保留该机器原有的 configs、certs、invites、logs、profiles。
切换服务器或客户端模式时，两种身份分别保存在本机目录中。
"@
$utf8 = New-Object Text.UTF8Encoding($false)
$generated = @{
  '使用说明.txt' = $utf8.GetBytes($instructions)
  'build-metadata.json' = $utf8.GetBytes(($metadata | ConvertTo-Json -Depth 10))
}
function Get-DataHashes {
  $hashes = @{}
  foreach ($folder in @('configs', 'certs', 'invites', 'logs', 'profiles')) {
    $directory = Join-Path $sourceRoot $folder
    if (Test-Path -LiteralPath $directory) {
      Get-ChildItem -LiteralPath $directory -File -Recurse | ForEach-Object {
        $hashes[$_.FullName] = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash
      }
    }
  }
  return $hashes
}
$services = @(Get-CimInstance Win32_Service | Where-Object { $_.Name -like 'Meshlink*' -or $_.PathName -match 'mesh-(agent|cloudhub|update-server)\.exe' })
$runningServices = @($services | Where-Object State -eq 'Running' | Select-Object -ExpandProperty Name)
$processPattern = '^(mesh-(agent|desktop|cloudhub|update-server)|meshctl)\.exe$'
$desktopProcesses = @(Get-CimInstance Win32_Process | Where-Object Name -eq 'mesh-desktop.exe' | Select-Object -ExpandProperty ExecutablePath -Unique)
try {
  foreach ($service in $services) {
    if ($service.State -ne 'Stopped') {
      Stop-Service -Name $service.Name -Force
      (Get-Service -Name $service.Name).WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    }
  }
  Get-CimInstance Win32_Process | Where-Object { $_.Name -match $processPattern } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }
  Start-Sleep -Seconds 1
  if (@(Get-CimInstance Win32_Process | Where-Object { $_.Name -match $processPattern }).Count -gt 0) { throw 'Meshlink processes must stop before packaging' }
  if (@(Get-CimInstance Win32_Service | Where-Object { $_.Name -in $services.Name -and $_.State -ne 'Stopped' }).Count -gt 0) { throw 'Meshlink services must stop before packaging' }
  $before = Get-DataHashes
  $localInstructions = "Meshlink $($metadata.version) 本机运行目录`r`n`r`n此目录保留本机配置。对外分发请使用上一层的 meshlink-无配置.zip。`r`n`r`n双击 start-meshlink.bat，程序自动申请管理员权限。`r`n服务器：填写公网地址和端口，启动后将同一端口的 TCP、UDP 映射至本机。本机虚拟 IP 为 10.77.0.1。`r`n客户端：输入邀请链接和验证码，点击连接；已有身份自动复用。`r`n服务器和客户端的配置分别保存，升级时保留 configs、certs、invites、logs、profiles。`r`n"
  [IO.File]::WriteAllText((Join-Path $sourceRoot '使用说明.txt'), $localInstructions, $utf8)
  $zipStream = [IO.File]::Open($temporary, [IO.FileMode]::CreateNew)
  $zip = New-Object IO.Compression.ZipArchive($zipStream, [IO.Compression.ZipArchiveMode]::Create, $false, [Text.Encoding]::UTF8)
  try {
    foreach ($relative in $fileNames) {
      $entry = $zip.CreateEntry('meshlink/' + $relative, [IO.Compression.CompressionLevel]::Optimal)
      $entryStream = $entry.Open()
      $inputStream = [IO.File]::OpenRead((Join-Path $sourceRoot $relative.Replace('/', '\')))
      try { $inputStream.CopyTo($entryStream) } finally { $inputStream.Dispose(); $entryStream.Dispose() }
    }
    foreach ($relative in $generated.Keys) {
      $bytes = $generated[$relative]
      $entryStream = $zip.CreateEntry('meshlink/' + $relative).Open()
      try { $entryStream.Write($bytes, 0, $bytes.Length) } finally { $entryStream.Dispose() }
      $sha = [Security.Cryptography.SHA256]::Create()
      try { $expectedHashes[$relative] = [BitConverter]::ToString($sha.ComputeHash($bytes)).Replace('-', '') } finally { $sha.Dispose() }
    }
  } finally { $zip.Dispose(); $zipStream.Dispose() }

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
  $after = Get-DataHashes
  if ($before.Count -ne $after.Count) { throw 'Runtime data file count changed' }
  foreach ($file in $before.Keys) { if ($after[$file] -ne $before[$file]) { throw "Runtime data changed: $file" } }
  if (Test-Path -LiteralPath $destination) { [IO.File]::Replace($temporary, $destination, [System.Management.Automation.Language.NullString]::Value) }
  else { [IO.File]::Move($temporary, $destination) }
  [pscustomobject]@{ Archive=$destination; Files=$expectedHashes.Count; Size=(Get-Item -LiteralPath $destination).Length; PreservedDataFiles=$before.Count; BuildTime=$metadata.build_time; StoppedBeforePackaging=$true } | ConvertTo-Json
} finally {
  if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force }
  foreach ($name in $runningServices) { Start-Service -Name $name }
  foreach ($path in $desktopProcesses) { if ($path) { Start-Process -FilePath $path -WorkingDirectory (Split-Path $path -Parent) -WindowStyle Hidden } }
}
