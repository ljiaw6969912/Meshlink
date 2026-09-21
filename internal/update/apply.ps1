$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSHOME 'Modules\Microsoft.PowerShell.Utility\Microsoft.PowerShell.Utility.psd1') -Force
$WaitPid = @@WAIT_PID@@
$BaseDir = @@BASE_DIR@@
$PackagePath = @@PACKAGE_PATH@@
$Version = @@VERSION@@
$ExpectedPackageHash = @@PACKAGE_HASH@@
$ServiceName = @@SERVICE_NAME@@
$UpdateDir = Join-Path $BaseDir 'updates'
$LogPath = Join-Path $UpdateDir ('apply-update-' + $Version + '.log')
$Stage = Join-Path $UpdateDir 'stage'
$LastKnownGoodRoot = Join-Path $UpdateDir 'last-known-good'
$script:BackupReady = $false
$script:BackupRecords = @()
$BackupDir = $LastKnownGoodRoot
$script:Services = @()
$script:Quiesced = $false
$script:Changed = $false
$BaseDir = [IO.Path]::GetFullPath($BaseDir).TrimEnd('\', '/')

function Assert-NoReparse([string]$Path) {
  $Current = [IO.Path]::GetFullPath($Path)
  while ($Current) {
    if (Test-Path -LiteralPath $Current) {
      $Item = Get-Item -LiteralPath $Current -Force
      if (($Item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw ('reparse point rejected: ' + $Current) }
    }
    $Current = Split-Path -Parent $Current
  }
}

function Remove-UpdateTree([string]$Path) {
  $Full = [IO.Path]::GetFullPath($Path)
  $Prefix = [IO.Path]::GetFullPath($UpdateDir).TrimEnd('\', '/') + '\'
  if (-not $Full.StartsWith($Prefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'cleanup path escaped updates directory' }
  Assert-NoReparse $Full
  if (Test-Path -LiteralPath $Full) {
    foreach ($Item in @(Get-ChildItem -LiteralPath $Full -Recurse -Force)) { Assert-NoReparse $Item.FullName }
    Remove-Item -LiteralPath $Full -Recurse -Force
  }
}

function Assert-ManagedPath([string]$RelativePath) {
  if ($RelativePath -cmatch '[\\:]' -or $RelativePath -match '(^|/)\.{1,2}(/|$)' -or $RelativePath -match '[. ](/|$)') { throw ('unsafe package path: ' + $RelativePath) }
  $Allowed = @('VERSION','manifest.json','README.md','DEPLOY.zh-CN.md','THIRD_PARTY_NOTICES.md','LICENSE','build-metadata.json','使用说明.txt','start-meshlink.bat',
    'bin/mesh-agent.exe','bin/mesh-cloudhub.exe','bin/mesh-desktop.exe','bin/mesh-update-server.exe','bin/meshctl.exe','bin/wintun.dll','bin/linux/mesh-agent')
  if ($RelativePath -cnotin $Allowed -and $RelativePath -cnotmatch '^scripts/[A-Za-z0-9][A-Za-z0-9_.-]*\.(ps1|bat|cmd|sh)$') { throw ('package file is not allowlisted: ' + $RelativePath) }
}

function Get-OwnedServices {
  @(Get-CimInstance Win32_Service | Where-Object {
    $Command = [string]$_.PathName
    $Exe = ''
    if ($Command -match '^\s*"([^"]+)"') { $Exe = $Matches[1] }
    elseif ($Command -match '^\s*(.+?\.exe)(?:\s|$)') { $Exe = $Matches[1] }
    $Exe -and (Test-OwnedExecutable $Exe)
  })
}

function Test-OwnedExecutable([string]$Exe) {
  if (-not $Exe) { return $false }
  $Full = [IO.Path]::GetFullPath($Exe)
  $Prefix = $BaseDir + '\'
  return $Full.StartsWith($Prefix, [StringComparison]::OrdinalIgnoreCase) -and ([IO.Path]::GetFileName($Full) -in @('mesh-agent.exe','mesh-cloudhub.exe','mesh-desktop.exe','mesh-update-server.exe','meshctl.exe'))
}

function Stop-OwnedRuntime {
  foreach ($Entry in $script:Services) {
    # Disabled prevents SCM recovery actions from relaunching a terminated service.
    Set-Service -Name $Entry.name -StartupType Disabled
    $Svc = Get-Service -Name $Entry.name
    if ($Svc.Status -ne 'Stopped') { Stop-Service -Name $Entry.name -Force }
    $Svc.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
  }
  foreach ($Process in @(Get-CimInstance Win32_Process | Where-Object { Test-OwnedExecutable ([string]$_.ExecutablePath) })) {
    if ([int]$Process.ProcessId -eq $PID) { throw 'installer must run outside Meshlink processes' }
    Stop-Process -Id $Process.ProcessId -Force -ErrorAction Stop
  }
  $Deadline = (Get-Date).AddSeconds(30)
  do {
    $Live = @(Get-CimInstance Win32_Process | Where-Object { Test-OwnedExecutable ([string]$_.ExecutablePath) })
    if ($Live.Count -eq 0) { break }
    Start-Sleep -Milliseconds 200
  } while ((Get-Date) -lt $Deadline)
  if ($Live.Count -ne 0) { throw 'Meshlink processes did not exit; refusing to overwrite files' }
  foreach ($Entry in $script:Services) {
    if ((Get-Service -Name $Entry.name).Status -ne 'Stopped') { throw ('service did not stop: ' + $Entry.name) }
  }
}

function Restore-Services {
  foreach ($Entry in $script:Services) {
    $Mode = switch ($Entry.mode) { 'Auto' { 'Automatic' }; 'Manual' { 'Manual' }; 'Disabled' { 'Disabled' }; default { throw 'unknown service start mode' } }
    if ($Entry.running -and $Mode -eq 'Disabled') { Set-Service -Name $Entry.name -StartupType Manual }
    else { Set-Service -Name $Entry.name -StartupType $Mode }
    if ($null -ne $Entry.delayed) { Set-ItemProperty -LiteralPath ('HKLM:\SYSTEM\CurrentControlSet\Services\' + $Entry.name) -Name DelayedAutoStart -Value ([int]$Entry.delayed) }
    if ($Entry.running) {
      Start-Service -Name $Entry.name
      (Get-Service -Name $Entry.name).WaitForStatus('Running', [TimeSpan]::FromSeconds(30))
      if ((Get-Service -Name $Entry.name).Status -ne 'Running') { throw ('service failed to restart: ' + $Entry.name) }
      if ($Mode -eq 'Disabled') { Set-Service -Name $Entry.name -StartupType Disabled }
    }
  }
}

function Write-Result([string]$Status, [string]$Message) {
  $Document = [ordered]@{version=$Version; status=$Status; message=$Message; completed_at=(Get-Date).ToUniversalTime().ToString('o'); services=@($script:Services | Where-Object { $_.running } | ForEach-Object { $_.name })}
  $ResultPath = Join-Path $UpdateDir 'update-result.json'
  Assert-NoReparse $ResultPath
  $Document | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $ResultPath -Encoding UTF8
}

function Start-Desktop {
  $Desktop = Join-Path $BaseDir 'bin\mesh-desktop.exe'
  Assert-NoReparse $Desktop
  if (Test-Path -LiteralPath $Desktop -PathType Leaf) { Start-Process -FilePath $Desktop -ArgumentList '-update-result' -WorkingDirectory $BaseDir }
}

function Resolve-PackageFile([string]$RelativePath) {
  Assert-ManagedPath $RelativePath
  $Normalized = $RelativePath.Replace('/', '\')
  if ([System.IO.Path]::IsPathRooted($Normalized) -or $Normalized -match '(^|[\\/])\.\.([\\/]|$)' -or $Normalized -match '^[A-Za-z]:') {
    throw ('unsafe package manifest path: ' + $RelativePath)
  }
  $FullPath = [System.IO.Path]::GetFullPath((Join-Path $PackageRoot $Normalized))
  $Prefix = $PackageRoot.TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
  if (-not $FullPath.StartsWith($Prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw ('package manifest path escaped package root: ' + $RelativePath)
  }
  Assert-NoReparse $FullPath
  return $FullPath
}

function Backup-Target([string]$RelativePath) {
  $Target = Join-Path $BaseDir $RelativePath
  Assert-NoReparse $Target
  $Existed = Test-Path -LiteralPath $Target -PathType Leaf
  if ($Existed) {
    $BackupPath = Join-Path $BackupDir $RelativePath
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $BackupPath) | Out-Null
    Copy-Item -LiteralPath $Target -Destination $BackupPath -Force
  }
  $script:BackupRecords += [ordered]@{ path = $RelativePath.Replace('\', '/'); existed = [bool]$Existed }
}

function Restore-LastKnownGood {
  if (-not $script:BackupReady) {
    return
  }
  foreach ($Record in $script:BackupRecords) {
    $RelativePath = ([string]$Record.path).Replace('/', '\')
    $Target = Join-Path $BaseDir $RelativePath
    Assert-NoReparse $Target
    if ([bool]$Record.existed) {
      $BackupPath = Join-Path $BackupDir $RelativePath
      New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Target) | Out-Null
      Copy-Item -LiteralPath $BackupPath -Destination $Target -Force
    } else {
      Remove-Item -LiteralPath $Target -Force -ErrorAction SilentlyContinue
    }
  }
}

Assert-NoReparse $BaseDir
Assert-NoReparse $UpdateDir
New-Item -ItemType Directory -Force -Path $UpdateDir | Out-Null
$Lock = $null
try {
  $LockPath = Join-Path $UpdateDir 'apply.lock'
  Assert-NoReparse $LockPath
  $Lock = [IO.File]::Open($LockPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
} catch { throw 'another installer is active or update lock is inaccessible' }
try { Start-Transcript -Path $LogPath -Append | Out-Null } catch {}
try {
  Assert-NoReparse $PackagePath
  Remove-UpdateTree $Stage
  New-Item -ItemType Directory -Force -Path $Stage | Out-Null
  # Validate every ZIP member before extraction: no traversal, alternate streams,
  # duplicate names, symlinks, or unallowlisted runtime data.
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  $Archive = [IO.Compression.ZipFile]::OpenRead($PackagePath)
  try {
    # Keep the ZIP read-locked from hash verification through extraction.
    if ($ExpectedPackageHash) {
      if ($ExpectedPackageHash -notmatch '^[0-9a-fA-F]{64}$' -or (Get-FileHash -Algorithm SHA256 -LiteralPath $PackagePath).Hash -ine $ExpectedPackageHash) { throw 'downloaded package SHA256 mismatch' }
    }
    $ZipNames = @{}
    foreach ($Entry in $Archive.Entries) {
      $Name = $Entry.FullName
      if ($Name -cnotmatch '^meshlink/' -or $Name -match '[\\:]' -or $Name -match '(^|/)\.{1,2}(/|$)' -or (($Entry.ExternalAttributes -shr 16) -band 0xF000) -eq 0xA000) { throw ('unsafe ZIP entry: ' + $Name) }
      if ($ZipNames.ContainsKey($Name)) { throw ('duplicate ZIP entry: ' + $Name) }
      $ZipNames[$Name] = $true
      if ($Name.EndsWith('/')) {
        if ($Name -cnotin @('meshlink/','meshlink/bin/','meshlink/bin/linux/','meshlink/scripts/')) { throw ('unexpected ZIP directory: ' + $Name) }
      } else { Assert-ManagedPath $Name.Substring(9) }
    }
    foreach ($Entry in $Archive.Entries) {
      if ($Entry.FullName.EndsWith('/')) { continue }
      $Destination = Join-Path $Stage $Entry.FullName
      Assert-NoReparse $Destination
      New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Destination) | Out-Null
      [IO.Compression.ZipFileExtensions]::ExtractToFile($Entry, $Destination, $false)
    }
  } finally { $Archive.Dispose() }

  $Dirs = @(Get-ChildItem -LiteralPath $Stage -Directory)
  $RootFiles = @(Get-ChildItem -LiteralPath $Stage -File)
  if ($Dirs.Count -ne 1 -or $RootFiles.Count -ne 0 -or $Dirs[0].Name -ne 'meshlink') {
    throw 'update package must contain exactly one meshlink root directory'
  }
  $PackageRoot = $Dirs[0].FullName

  $ManifestPath = Join-Path $PackageRoot 'manifest.json'
  if (-not (Test-Path -LiteralPath $ManifestPath -PathType Leaf)) {
    throw 'required package manifest.json is missing'
  }
  $PackageManifest = Get-Content -Encoding UTF8 -Raw -LiteralPath $ManifestPath | ConvertFrom-Json
  if ($PackageManifest.schema -ne 'meshlink-package-v1' -or $PackageManifest.product -ne 'Meshlink') {
    throw 'unsupported package manifest schema'
  }
  if ([string]$PackageManifest.version -ne $Version) {
    throw 'package manifest version does not match requested version'
  }
  $VersionPath = Join-Path $PackageRoot 'VERSION'
  if (-not (Test-Path -LiteralPath $VersionPath -PathType Leaf) -or (Get-Content -Encoding UTF8 -Raw -LiteralPath $VersionPath).Trim() -ne $Version) {
    throw 'package VERSION does not match requested version'
  }

  $RequiredFiles = @(
    'VERSION', 'bin/mesh-agent.exe', 'bin/mesh-cloudhub.exe', 'bin/mesh-desktop.exe',
    'bin/mesh-update-server.exe', 'bin/meshctl.exe'
  )
  foreach ($RelativePath in $RequiredFiles) {
    if (-not (Test-Path -LiteralPath (Resolve-PackageFile $RelativePath) -PathType Leaf)) {
      throw ('required update package file is missing: ' + $RelativePath)
    }
  }

  $ManifestPaths = @($PackageManifest.files | ForEach-Object { [string]$_.path })
  [string[]]$SortedPaths = @($ManifestPaths)
  [System.Array]::Sort($SortedPaths, [System.StringComparer]::Ordinal)
  if (($ManifestPaths -join [char]10) -cne ($SortedPaths -join [char]10)) {
    throw 'package manifest file list is not sorted'
  }
  $Covered = @{}
  foreach ($File in @($PackageManifest.files)) {
    $RelativePath = [string]$File.path
    if ($RelativePath -ieq 'manifest.json') { throw 'manifest cannot cover itself' }
    $Source = Resolve-PackageFile $RelativePath
    $Key = $RelativePath.Replace('\', '/').ToLowerInvariant()
    if ($Covered.ContainsKey($Key)) {
      throw ('duplicate package manifest file: ' + $RelativePath)
    }
    if (-not (Test-Path -LiteralPath $Source -PathType Leaf)) {
      throw ('package manifest references missing file: ' + $RelativePath)
    }
    $Info = Get-Item -LiteralPath $Source
    $ActualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $Source).Hash.ToLowerInvariant()
    if ([int64]$Info.Length -ne [int64]$File.size -or $ActualHash -cne ([string]$File.sha256).ToLowerInvariant()) {
      throw ('package manifest hash or size mismatch: ' + $RelativePath)
    }
    $Covered[$Key] = $true
  }
  foreach ($SourceFile in @(Get-ChildItem -LiteralPath $PackageRoot -File -Recurse)) {
    $RelativePath = $SourceFile.FullName.Substring($PackageRoot.Length + 1).Replace('\', '/')
    if ($RelativePath -ceq 'manifest.json') {
      continue
    }
    if (-not $Covered.ContainsKey($RelativePath.ToLowerInvariant())) {
      throw ('package contains file not covered by manifest: ' + $RelativePath)
    }
  }

  $PackageMode = [string]$PackageManifest.mode
  if ($PackageMode -eq 'release') {
    if (-not [bool]$PackageManifest.signing.code_signed) {
      throw 'release package is not marked code signed'
    }
    $CertificateThumbprint = ([string]$PackageManifest.signing.certificate_thumbprint).Replace(' ', '').ToUpperInvariant()
    if ($CertificateThumbprint -notmatch '^[0-9A-F]{40}$') {
      throw 'release package certificate thumbprint is invalid'
    }
    foreach ($Executable in @(Get-ChildItem -LiteralPath (Join-Path $PackageRoot 'bin') -File -Filter '*.exe')) {
      $Signature = Get-AuthenticodeSignature -LiteralPath $Executable.FullName
      if ($Signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid -or $null -eq $Signature.SignerCertificate) {
        throw ('Authenticode signature is invalid: ' + $Executable.Name)
      }
      $ActualThumbprint = $Signature.SignerCertificate.Thumbprint.Replace(' ', '').ToUpperInvariant()
      if ($ActualThumbprint -cne $CertificateThumbprint) {
        throw ('Authenticode signer certificate mismatch: ' + $Executable.Name)
      }
    }
  } elseif ($PackageMode -eq 'development') {
    if ([bool]$PackageManifest.signing.code_signed -or -not [string]::IsNullOrWhiteSpace([string]$PackageManifest.signing.certificate_thumbprint)) {
      throw 'development package must be explicitly unsigned'
    }
  } else {
    throw ('unsupported package mode: ' + $PackageMode)
  }

  $InstalledVersionPath = Join-Path $BaseDir 'VERSION'
  if (-not (Test-Path -LiteralPath $InstalledVersionPath -PathType Leaf)) {
    throw 'installed VERSION is missing; cannot establish last-known-good'
  }
  $CurrentVersion = (Get-Content -Encoding UTF8 -Raw -LiteralPath $InstalledVersionPath).Trim()
  if ($CurrentVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') {
    throw 'installed VERSION is invalid; cannot establish last-known-good'
  }

  if ($WaitPid -gt 0) {
    $Waiting = Get-Process -Id $WaitPid -ErrorAction SilentlyContinue
    if ($null -ne $Waiting) {
      Wait-Process -Id $WaitPid -Timeout 60 -ErrorAction Stop
      if (Get-Process -Id $WaitPid -ErrorAction SilentlyContinue) { throw 'requesting process did not exit' }
    }
  }
  $script:Services = @(Get-OwnedServices | ForEach-Object {
    [pscustomobject]@{ name=$_.Name; mode=$_.StartMode; running=($_.State -ne 'Stopped'); delayed=$_.DelayedAutoStart }
  })
  Write-Host ('Stopping owned services: ' + (($script:Services | ForEach-Object { $_.name }) -join ', '))
  # Snapshot all states before stopping any service, including the update server.
  Stop-OwnedRuntime
  $script:Quiesced = $true
  Remove-UpdateTree $LastKnownGoodRoot
  New-Item -ItemType Directory -Force -Path $BackupDir | Out-Null
  $ManagedFiles = @($PackageManifest.files | ForEach-Object { [string]$_.path }) + @('manifest.json')
  foreach ($RelativePath in $ManagedFiles) { Backup-Target $RelativePath }
  $script:BackupReady = $true
  $BackupDocument = [ordered]@{
    schema='meshlink-last-known-good-v1'; current_version=$CurrentVersion; attempted_version=$Version
    created_at=(Get-Date).ToUniversalTime().ToString('o'); files=$script:BackupRecords; services=$script:Services
  }
  $BackupDocument | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $BackupDir 'backup-manifest.json') -Encoding UTF8
  foreach ($RelativePath in $ManagedFiles) {
    $Src = Resolve-PackageFile $RelativePath
    $Target = Join-Path $BaseDir $RelativePath
    Assert-NoReparse $Target
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Target) | Out-Null
    $script:Changed = $true
    Copy-Item -LiteralPath $Src -Destination $Target -Force
  }
  Restore-Services
  Write-Result 'success' ('Meshlink ' + $Version + ' installed; services restored.')
  Start-Desktop
} catch {
  $Failure = $_.Exception.Message
  Write-Host ('update failed: ' + $Failure)
  if ($script:Changed) {
    try {
      # Stop-Service is performed by Stop-OwnedRuntime before any rollback copy.
      Stop-OwnedRuntime
      Restore-LastKnownGood
    } catch { $Failure += '; rollback failed: ' + $_.Exception.Message; Write-Host $Failure }
  }
  try { Restore-Services } catch { $Failure += '; service restoration failed: ' + $_.Exception.Message; Write-Host $Failure }
  try { Write-Result 'failed' $Failure } catch { Write-Host ('cannot write failure result: ' + $_.Exception.Message) }
  try { Start-Desktop } catch { Write-Host ('cannot launch desktop: ' + $_.Exception.Message) }
  exit 1
} finally {
  try { Remove-UpdateTree $Stage } catch { Write-Host ('stage cleanup failed: ' + $_.Exception.Message) }
  try { Stop-Transcript | Out-Null } catch {}
  if ($null -ne $Lock) { $Lock.Dispose() }
}
