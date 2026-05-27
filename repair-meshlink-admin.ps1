$ErrorActionPreference = "Continue"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$resultPath = Join-Path $scriptDir "repair-result.txt"
Start-Transcript -Path $resultPath -Force | Out-Null

function Finish($code) {
  Stop-Transcript | Out-Null
  Write-Host ""
  Write-Host "Result written to: $resultPath"
  Pause
  exit $code
}

$candidates = @(
  $scriptDir,
  (Join-Path $scriptDir "dist\meshlink-latest"),
  (Join-Path $scriptDir "dist\meshlink-config-picker")
)
$root = $null
foreach ($candidate in $candidates) {
  if ((Test-Path (Join-Path $candidate "bin\mesh-agent.exe")) -and (Test-Path (Join-Path $candidate "configs\hub.json"))) {
    $root = $candidate
    break
  }
}

if ($null -eq $root) {
  Write-Host "No usable Meshlink package directory found." -ForegroundColor Red
  Finish 1
}

$agent = Join-Path $root "bin\mesh-agent.exe"
$config = Join-Path $root "configs\hub.json"
$log = Join-Path $root "configs\logs\MeshlinkAgent.log"
$fgLog = Join-Path $scriptDir "agent-foreground.txt"

Write-Host "Meshlink service repair"
Write-Host "Agent:  $agent"
Write-Host "Config: $config"
Write-Host ""

if (-not ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole] "Administrator")) {
  Write-Host "Please run this script as Administrator." -ForegroundColor Red
  Finish 1
}

if (-not (Test-Path $agent)) {
  Write-Host "mesh-agent.exe not found." -ForegroundColor Red
  Finish 1
}

if (-not (Test-Path $config)) {
  Write-Host "hub.json not found." -ForegroundColor Red
  Finish 1
}

Write-Host "Stopping existing service..."
$svc = Get-Service -Name MeshlinkAgent -ErrorAction SilentlyContinue
if ($null -ne $svc) {
  Stop-Service -Name MeshlinkAgent -ErrorAction SilentlyContinue
  Start-Sleep -Seconds 2
}

if ($null -eq $svc) {
  Write-Host "Service does not exist. Installing..."
  & $agent -service install -service-name MeshlinkAgent -config $config
  if ($LASTEXITCODE -ne 0) {
    Write-Host "Service install failed. Exit code: $LASTEXITCODE" -ForegroundColor Red
    Finish $LASTEXITCODE
  }
} else {
  Write-Host "Updating service binary path..."
  $binPath = "`"$agent`" -service run -service-name MeshlinkAgent -config `"$config`""
  sc.exe config MeshlinkAgent binPath= $binPath start= auto | Out-Host
}

Write-Host "Starting service..."
Start-Service -Name MeshlinkAgent
Start-Sleep -Seconds 3

Write-Host ""
Write-Host "Service status:"
Get-Service -Name MeshlinkAgent | Format-Table Name, Status, StartType
$currentService = Get-Service -Name MeshlinkAgent -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "Port 8443 listener:"
netstat -ano | Select-String ":8443"

if (Test-Path $log) {
  Write-Host ""
  Write-Host "Latest logs:"
  Get-Content -Encoding UTF8 $log -Tail 20
}

if ($null -eq $currentService -or $currentService.Status -ne "Running") {
  Write-Host ""
  Write-Host "Service is not running. Running foreground diagnostic for 8 seconds..."
  if (Test-Path $fgLog) {
    Remove-Item -LiteralPath $fgLog -Force
  }
  $proc = Start-Process -FilePath $agent -ArgumentList @("-config", $config) -NoNewWindow -RedirectStandardOutput $fgLog -RedirectStandardError $fgLog -PassThru
  Start-Sleep -Seconds 8
  if (-not $proc.HasExited) {
    Write-Host "Foreground agent stayed running for 8 seconds; stopping diagnostic process."
    Stop-Process -Id $proc.Id -Force
  } else {
    Write-Host "Foreground agent exited with code: $($proc.ExitCode)"
  }
  if (Test-Path $fgLog) {
    Write-Host ""
    Write-Host "Foreground diagnostic output:"
    Get-Content -Encoding UTF8 $fgLog -Tail 80
  }
}

Write-Host ""
Finish 0
