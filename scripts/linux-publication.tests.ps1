$ErrorActionPreference = 'Stop'
function Assert-True([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
function Assert-Throws([scriptblock]$Action, [string]$Message) {
  $failed = $false
  try { & $Action | Out-Null } catch { $failed = $true }
  Assert-True $failed $Message
}
$tokens = $null
$errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'publish-local.ps1'), [ref]$tokens, [ref]$errors)
Assert-True (@($errors).Count -eq 0) 'Publisher syntax is invalid'
foreach ($definition in $ast.FindAll({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] }, $false)) {
  . ([scriptblock]::Create($definition.Extent.Text))
}
Assert-True ($null -ne (Get-Command Get-MeshlinkLinuxPackages -ErrorAction SilentlyContinue)) 'Linux publication validation is missing'
$temporary = Join-Path ([IO.Path]::GetTempPath()) ('meshlink-linux-publish-tests-' + [guid]::NewGuid().ToString('N'))
try {
  $cache = Join-Path $temporary '.cache'
  New-Item -ItemType Directory -Path $cache -Force | Out-Null
  $archives = @()
  foreach ($arch in @('amd64', 'arm64')) {
    $file = "meshlink-linux-$arch.tar.gz"
    $path = Join-Path $cache $file
    [IO.File]::WriteAllText($path, "verified-$arch-archive")
    $archives += [pscustomobject]@{file=$file; size=(Get-Item -LiteralPath $path).Length; sha256=(Get-FileHash -LiteralPath $path).Hash}
  }
  $receipt = [ordered]@{schema='meshlink-linux-packages-v1'; version='0.1.14'; archives=$archives}
  $receiptPath = Join-Path $cache 'linux-coordinator-packages.json'
  [IO.File]::WriteAllText($receiptPath, ($receipt | ConvertTo-Json -Depth 5))
  $actual = @(Get-MeshlinkLinuxPackages -Root $temporary -Version '0.1.14')
  Assert-True ($actual.Count -eq 2) 'One architecture was lost'
  Assert-True ($actual[0].file -ceq 'meshlink-linux-amd64.tar.gz' -and $actual[1].file -ceq 'meshlink-linux-arm64.tar.gz') 'Wrong distribution filenames'
  Assert-Throws { Get-MeshlinkLinuxPackages -Root $temporary -Version '0.1.15' } 'Stale Linux version was accepted'
  $receipt.archives = @($archives[0], $archives[0])
  [IO.File]::WriteAllText($receiptPath, ($receipt | ConvertTo-Json -Depth 5))
  Assert-Throws { Get-MeshlinkLinuxPackages -Root $temporary -Version '0.1.14' } 'Duplicate architecture was accepted'
  $receipt.archives = $archives
  [IO.File]::WriteAllText($receiptPath, ($receipt | ConvertTo-Json -Depth 5))
  [IO.File]::WriteAllText((Join-Path $cache $archives[1].file), 'tampered')
  Assert-Throws { Get-MeshlinkLinuxPackages -Root $temporary -Version '0.1.14' } 'Tampered Linux archive was accepted'
  $receipt.archives = @($archives[0])
  [IO.File]::WriteAllText($receiptPath, ($receipt | ConvertTo-Json -Depth 5))
  Assert-Throws { Get-MeshlinkLinuxPackages -Root $temporary -Version '0.1.14' } 'Incomplete Linux build was accepted'
  Write-Output 'Linux publication tests passed (both architectures, version, duplicates, hash, incomplete build)'
} finally {
  $full = [IO.Path]::GetFullPath($temporary)
  $base = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
  if (-not $full.StartsWith($base, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path $full -Leaf) -notlike 'meshlink-linux-publish-tests-*') { throw 'Unsafe test cleanup path' }
  if (Test-Path -LiteralPath $full) { Remove-Item -LiteralPath $full -Recurse -Force }
}
