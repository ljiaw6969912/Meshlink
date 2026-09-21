#Requires -RunAsAdministrator
<#
Publish binaries already built by scripts/build.ps1. This never cleans or copies
runtime directories. Run elevated; optionally use -ResultPath to observe the
outcome from a non-elevated caller. Do not use build-clean.ps1 for local upgrades.
#>
param(
  [string]$RepositoryRoot = (Join-Path $PSScriptRoot '..'),
  [string]$ResultPath = '',
  [switch]$IncludeLinux
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'package-clean.ps1') -FunctionsOnly

function Get-MeshlinkSourceInformation([string]$Root) {
  $protocolSource = Join-Path $Root 'internal\config\config.go'
  if (-not (Test-Path -LiteralPath $protocolSource) -or (Get-Content -LiteralPath $protocolSource -Raw) -notmatch 'tcp_tls_control_v2') {
    throw 'Refusing to publish source without tcp_tls_control_v2'
  }
  $commit = (& git -C $Root rev-parse --verify HEAD 2>$null | Out-String).Trim()
  if ($LASTEXITCODE -ne 0 -or $commit -notmatch '^[0-9a-f]{40,64}$') { throw 'Cannot identify source Git commit' }
  $status = @(& git -C $Root status --porcelain --untracked-files=normal 2>$null)
  if ($LASTEXITCODE -ne 0) { throw 'Cannot determine whether source has uncommitted changes' }
  return [ordered]@{
    directory = $Root
    commit = $commit
    includes_uncommitted_changes = ($status.Count -gt 0)
    tracked_changes = (@($status | Where-Object { $_ -notmatch '^\?\?' }).Count -gt 0)
    untracked_changes = (@($status | Where-Object { $_ -match '^\?\?' }).Count -gt 0)
    recorded_at = [datetime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
    description = 'Source checkout observed at publication; binaries verified against scripts/build.ps1 metadata'
  }
}

function Get-MeshlinkLinuxPackages([string]$Root, [string]$Version) {
  $cache = Join-Path $Root '.cache'
  $receipt = Get-Content -LiteralPath (Join-Path $cache 'linux-coordinator-packages.json') -Raw -Encoding UTF8 | ConvertFrom-Json
  if ($receipt.schema -cne 'meshlink-linux-packages-v1' -or $receipt.version -cne $Version) { throw 'Linux packages do not match VERSION' }
  $expected = @('meshlink-linux-amd64.tar.gz', 'meshlink-linux-arm64.tar.gz')
  if (@($receipt.archives).Count -ne 2) { throw 'Both Linux architectures must be built before publication' }
  $seen = @{}
  foreach ($archive in $receipt.archives) {
    $file = [string]$archive.file
    if ($file -cnotin $expected -or $seen.ContainsKey($file)) { throw "Unexpected or duplicate Linux package: $file" }
    $seen[$file] = $true
    $source = Join-Path $cache $file
    $item = Get-Item -LiteralPath $source
    if ($item.PSIsContainer -or $item.Length -ne $archive.size -or (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash -ne $archive.sha256) { throw "Linux package hash mismatch: $file" }
    [pscustomobject]@{file=$file; source=$source; sha256=$archive.sha256; size=$archive.size}
  }
}

$releaseRoot = 'C:\Users\Administrator\Desktop\wireguard\release'
$destinationRoot = Join-Path $releaseRoot 'meshlink'
$runtime = New-MeshlinkRuntimeState
$before = $null
$failures = New-Object 'System.Collections.Generic.List[string]'
$utf8 = New-Object Text.UTF8Encoding($false)
$result = [ordered]@{ succeeded=$false; directory=$destinationRoot; archive=(Join-Path $releaseRoot 'meshlink-无配置.zip'); preserved_data_files=0; errors=@() }
try {
  if ($ResultPath) {
    $ResultPath = [IO.Path]::GetFullPath($ResultPath)
    if ($ResultPath.StartsWith($releaseRoot.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)) { throw 'ResultPath must be outside the release directory' }
    if (-not (Test-Path -LiteralPath (Split-Path $ResultPath -Parent) -PathType Container)) { throw 'ResultPath parent directory does not exist' }
    if (Test-Path -LiteralPath $ResultPath) { throw 'ResultPath must be a new file' }
  }
  $root = (Resolve-Path -LiteralPath $RepositoryRoot).Path.TrimEnd('\')
  $bin = Join-Path $root 'bin'
  $version = (Get-Content -LiteralPath (Join-Path $root 'VERSION') -Raw -Encoding UTF8).Trim()
  if ($version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') { throw 'VERSION is invalid' }
  $metadata = Read-MeshlinkBuildMetadata -BinDirectory $bin -Version $version
  $linuxPackages = @()
  if ($IncludeLinux) { $linuxPackages = @(Get-MeshlinkLinuxPackages -Root $root -Version $version) }
  $source = Get-MeshlinkSourceInformation $root
  $metadata | Add-Member -NotePropertyName source -NotePropertyValue $source -Force
  $result.version = $version
  $result.build_time = $metadata.build_time
  $result.source_commit = $source.commit
  $result.source_dirty = $source.includes_uncommitted_changes
  $copies = @{}
  foreach ($relative in @(Get-MeshlinkCompiledFiles)) { $copies['bin/' + $relative] = Join-Path $bin $relative.Replace('/', '\') }
  $copies['bin/wintun.dll'] = Join-Path $bin 'wintun.dll'
  foreach ($relative in @('LICENSE', 'THIRD_PARTY_NOTICES.md', 'VERSION', 'scripts/support-triage.ps1')) { $copies[$relative] = Join-Path $root $relative }
  $copyHashes = @{}
  foreach ($relative in $copies.Keys) {
    if (-not (Test-Path -LiteralPath $copies[$relative] -PathType Leaf)) { throw "Required publication file is missing: $relative" }
    $copyHashes[$relative] = (Get-FileHash -LiteralPath $copies[$relative] -Algorithm SHA256).Hash
    Get-MeshlinkDestinationPath -Root $destinationRoot -Relative $relative | Out-Null
  }
  Assert-MeshlinkNoReparsePoint $releaseRoot
  Stop-MeshlinkRuntime $runtime
  $before = Get-MeshlinkDataHashes $destinationRoot
  $result.preserved_data_files = $before.Count
  New-Item -ItemType Directory -Force -Path $destinationRoot | Out-Null
  foreach ($relative in $copies.Keys) {
    Assert-MeshlinkStopped
    $target = Get-MeshlinkDestinationPath -Root $destinationRoot -Relative $relative
    New-Item -ItemType Directory -Force -Path (Split-Path $target -Parent) | Out-Null
    Copy-Item -LiteralPath $copies[$relative] -Destination $target -Force
    if ((Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash -ne $copyHashes[$relative]) { throw "Published file hash mismatch: $relative" }
  }
  Assert-MeshlinkStopped
  $launcher = "@echo off`r`ncd /d `"%~dp0`"`r`nstart `"`" `"%~dp0bin\mesh-desktop.exe`"`r`n"
  [IO.File]::WriteAllText((Get-MeshlinkDestinationPath -Root $destinationRoot -Relative 'start-meshlink.bat'), $launcher, [Text.Encoding]::ASCII)
  [IO.File]::WriteAllText((Get-MeshlinkDestinationPath -Root $destinationRoot -Relative 'build-metadata.json'), (($metadata | ConvertTo-Json -Depth 10) + "`n"), $utf8)
  Read-MeshlinkBuildMetadata -BinDirectory (Join-Path $destinationRoot 'bin') -Version $version -MetadataPath (Join-Path $destinationRoot 'build-metadata.json') | Out-Null
  Assert-MeshlinkDataUnchanged -Root $destinationRoot -Before $before
  # The nested packager observes already-stopped, disabled services, so only the
  # outer finally restores the original runtime after all validation completes.
  $packageOutput = & (Join-Path $PSScriptRoot 'package-clean.ps1') -SourceDirectory $destinationRoot
  $packageResult = ($packageOutput | Out-String) | ConvertFrom-Json
  if ($packageResult.Archive -ne $result.archive -or -not $packageResult.StoppedBeforePackaging) { throw 'Clean-package verification did not report the expected archive' }
  $result.archive_files = $packageResult.Files
  $result.archive_sha256 = (Get-FileHash -LiteralPath $result.archive -Algorithm SHA256).Hash.ToLowerInvariant()
  $result.linux_archives = @()
  foreach ($linuxPackage in $linuxPackages) {
    Assert-MeshlinkStopped
    $target = Get-MeshlinkDestinationPath -Root $releaseRoot -Relative $linuxPackage.file
    Copy-Item -LiteralPath $linuxPackage.source -Destination $target -Force
    if ((Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash -ne $linuxPackage.sha256) { throw "Published Linux package hash mismatch: $($linuxPackage.file)" }
    $result.linux_archives += [ordered]@{path=$target; sha256=$linuxPackage.sha256; size=$linuxPackage.size}
  }
  Assert-MeshlinkStopped
  Assert-MeshlinkDataUnchanged -Root $destinationRoot -Before $before
} catch { $failures.Add($_.Exception.Message) }
finally {
  try { if ($null -ne $before) { Assert-MeshlinkDataUnchanged -Root $destinationRoot -Before $before } } catch { $failures.Add($_.Exception.Message) }
  try { Restore-MeshlinkRuntime $runtime } catch { $failures.Add($_.Exception.Message) }
}
$result.succeeded = ($failures.Count -eq 0)
$result.errors = @($failures.ToArray())
$json = $result | ConvertTo-Json -Depth 8
if ($ResultPath -and -not (Test-Path -LiteralPath $ResultPath) -and (Test-Path -LiteralPath (Split-Path $ResultPath -Parent) -PathType Container) -and -not $ResultPath.StartsWith($releaseRoot.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)) {
  [IO.File]::WriteAllText($ResultPath, $json, $utf8)
}
Write-Output $json
if (-not $result.succeeded) { throw ($failures -join '; ') }
