$ErrorActionPreference = "Stop"

$scriptPath = Join-Path $PSScriptRoot "support-triage.ps1"
$powerShellPath = [Diagnostics.Process]::GetCurrentProcess().MainModule.FileName
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ("meshlink-task11e-test-" + [Guid]::NewGuid().ToString("N"))
$failures = [Collections.Generic.List[string]]::new()

function Assert-True {
  param(
    [bool]$Condition,
    [string]$Message
  )

  if (-not $Condition) {
    throw $Message
  }
}

function Assert-Equal {
  param(
    $Actual,
    $Expected,
    [string]$Message
  )

  if ($Actual -ne $Expected) {
    throw "$Message (actual=$Actual expected=$Expected)"
  }
}

function Write-JsonFile {
  param(
    [Parameter(Mandatory = $true)]
    $Value,
    [Parameter(Mandatory = $true)]
    [string]$Path
  )

  $json = $Value | ConvertTo-Json -Depth 12
  [IO.File]::WriteAllText($Path, $json, [Text.UTF8Encoding]::new($false))
}

function Invoke-TriageProcess {
  param(
    [Parameter(Mandatory = $true)]
    [string]$InputPath,
    [string]$OutputPath
  )

  $arguments = @(
    "-NoLogo",
    "-NoProfile",
    "-NonInteractive",
    "-ExecutionPolicy", "Bypass",
    "-File", $scriptPath,
    "-InputPath", $InputPath
  )
  if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
    $arguments += @("-OutputPath", $OutputPath)
  }

  $previousErrorActionPreference = $ErrorActionPreference
  $ErrorActionPreference = "Continue"
  try {
    $outputLines = & $powerShellPath @arguments 2>&1
    $exitCode = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $previousErrorActionPreference
  }
  $output = ($outputLines | ForEach-Object { $_.ToString() } | Out-String).Trim()
  [pscustomobject]@{
    ExitCode = $exitCode
    Output = $output
  }
}

function Invoke-Test {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Name,
    [Parameter(Mandatory = $true)]
    [scriptblock]$Body
  )

  try {
    & $Body
    Write-Host "PASS: $Name"
  } catch {
    $failures.Add("${Name}: $($_.Exception.Message)")
    Write-Host "FAIL: $Name"
  }
}

New-Item -ItemType Directory -Path $tempRoot | Out-Null

try {
  Invoke-Test "script syntax and parameter contract" {
    Assert-True (Test-Path -LiteralPath $scriptPath -PathType Leaf) "support-triage.ps1 is missing"
    $tokens = $null
    $parseErrors = $null
    [void][Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$parseErrors)
    Assert-Equal @($parseErrors).Count 0 "support-triage.ps1 has parse errors"

    $command = Get-Command -Name $scriptPath
    foreach ($parameterName in @("InputPath", "OutputPath")) {
      Assert-True $command.Parameters.ContainsKey($parameterName) "missing parameter $parameterName"
    }
  }

  Invoke-Test "fake RDP disabled scenario has fixed redacted schema" {
    $inputPath = Join-Path $tempRoot "rdp-disabled-input.json"
    $outputPath = Join-Path $tempRoot "rdp-disabled-report.json"
    $input = [ordered]@{
      schema_version = "meshlink.support-triage-input.v1"
      case = [ordered]@{
        account_id = "account-real-001"
        device_id = "device-real-009"
        from_utc = "2026-07-15T01:00:00Z"
        to_utc = "2026-07-15T01:15:00Z"
      }
      security = [ordered]@{ status = "clear"; observed_at_utc = "2026-07-15T01:01:00Z" }
      account = [ordered]@{ status = "ok"; observed_at_utc = "2026-07-15T01:02:00Z" }
      login = [ordered]@{ status = "ok"; observed_at_utc = "2026-07-15T01:03:00Z" }
      enrollment = [ordered]@{ status = "ok"; observed_at_utc = "2026-07-15T01:04:00Z" }
      connection = [ordered]@{
        status = "connected"
        path_type = "lan_direct"
        path_state = "lan_direct_connected"
        observed_at_utc = "2026-07-15T01:05:00Z"
      }
      relay = [ordered]@{ status = "not_used"; observed_at_utc = "2026-07-15T01:05:00Z" }
      rdp = [ordered]@{
        status = "unreachable"
        enabled = $false
        service_state = "stopped"
        firewall_status = "unknown"
        observed_at_utc = "2026-07-15T01:06:00Z"
      }
      diagnostic_findings = @(
        [ordered]@{ code = "rdp_unavailable"; severity = "fail"; observed_at_utc = "2026-07-15T01:06:00Z" }
      )
    }
    Write-JsonFile -Value $input -Path $inputPath

    $run = Invoke-TriageProcess -InputPath $inputPath -OutputPath $outputPath
    Assert-Equal $run.ExitCode 0 "triage process failed: $($run.Output)"
    Assert-True (Test-Path -LiteralPath $outputPath -PathType Leaf) "report file was not created"

    $raw = Get-Content -LiteralPath $outputPath -Raw
    $report = $raw | ConvertFrom-Json
    $propertyNames = @($report.PSObject.Properties.Name | Sort-Object) -join ","
    Assert-Equal $propertyNames "actions,case,classification,evidence,generated_at_utc,privacy,schema_version" "top-level schema changed"
    Assert-Equal $report.schema_version "meshlink.support-triage-report.v1" "unexpected schema version"
    Assert-Equal $report.classification.category "target_rdp" "wrong incident category"
    Assert-Equal $report.classification.layer "rdp" "wrong failing layer"
    Assert-Equal $report.classification.code "rdp_not_enabled" "wrong RDP diagnosis"
    Assert-Equal $report.classification.priority "P3" "wrong priority"
    Assert-Equal $report.classification.stop_and_escalate_security $false "ordinary RDP case must not be escalated as security"
    Assert-Equal $report.privacy.local_only $true "report must be local only"
    Assert-Equal $report.privacy.telemetry_sent $false "script must not upload telemetry"
    Assert-Equal $report.privacy.user_content_collected $false "script must not collect user content"
    Assert-Equal $report.privacy.credentials_collected $false "script must not collect credentials"
    Assert-True $report.case.account_ref.StartsWith("sha256:") "account ID was not pseudonymized"
    Assert-True $report.case.device_ref.StartsWith("sha256:") "device ID was not pseudonymized"
    Assert-True (-not $raw.Contains("account-real-001")) "raw account ID leaked"
    Assert-True (-not $raw.Contains("device-real-009")) "raw device ID leaked"
    Assert-True (@($report.evidence.source) -contains "diagnose") "existing diagnosis finding was not aggregated"
    Assert-True (@($report.actions) -join " " -match "Enable Remote Desktop") "RDP enablement action is missing"
    foreach ($forbidden in @("candidate_addresses", "clipboard", "private_key", "raw_traffic", "screen_content", "file_content")) {
      Assert-True (-not $raw.ToLowerInvariant().Contains($forbidden)) "report contains forbidden field: $forbidden"
    }
  }

  Invoke-Test "default output remains in the local current directory" {
    $inputPath = Join-Path $tempRoot "default-output-input.json"
    $input = [ordered]@{
      schema_version = "meshlink.support-triage-input.v1"
      case = [ordered]@{
        account_id = "account-default"
        device_id = "device-default"
        from_utc = "2026-07-15T02:00:00Z"
        to_utc = "2026-07-15T02:05:00Z"
      }
    }
    Write-JsonFile -Value $input -Path $inputPath

    Push-Location $tempRoot
    try {
      $run = Invoke-TriageProcess -InputPath $inputPath
    } finally {
      Pop-Location
    }
    Assert-Equal $run.ExitCode 0 "default output run failed: $($run.Output)"
    $reportedPath = @($run.Output -split "`r?`n")[-1].Trim()
    Assert-True (Test-Path -LiteralPath $reportedPath -PathType Leaf) "default report file was not created"
    Assert-Equal (Split-Path -Parent ([IO.Path]::GetFullPath($reportedPath))) ([IO.Path]::GetFullPath($tempRoot)) "default report escaped the local current directory"
  }

  Invoke-Test "sensitive and address fields are rejected without report" {
    $inputPath = Join-Path $tempRoot "forbidden-input.json"
    $outputPath = Join-Path $tempRoot "forbidden-report.json"
    $input = [ordered]@{
      schema_version = "meshlink.support-triage-input.v1"
      case = [ordered]@{
        account_id = "account-forbidden"
        device_id = "device-forbidden"
        from_utc = "2026-07-15T03:00:00Z"
        to_utc = "2026-07-15T03:05:00Z"
      }
      connection = [ordered]@{
        status = "connected"
        candidate_addresses = @("198.51.100.1:1234")
        token = "super-secret-value"
      }
    }
    Write-JsonFile -Value $input -Path $inputPath

    $run = Invoke-TriageProcess -InputPath $inputPath -OutputPath $outputPath
    Assert-True ($run.ExitCode -ne 0) "forbidden input unexpectedly succeeded"
    Assert-True (-not (Test-Path -LiteralPath $outputPath)) "forbidden input generated a report"
    Assert-True (-not $run.Output.Contains("super-secret-value")) "error output leaked a secret value"
  }

  Invoke-Test "reversed time range is rejected" {
    $inputPath = Join-Path $tempRoot "invalid-range-input.json"
    $outputPath = Join-Path $tempRoot "invalid-range-report.json"
    $input = [ordered]@{
      schema_version = "meshlink.support-triage-input.v1"
      case = [ordered]@{
        account_id = "account-range"
        device_id = "device-range"
        from_utc = "2026-07-15T04:10:00Z"
        to_utc = "2026-07-15T04:00:00Z"
      }
    }
    Write-JsonFile -Value $input -Path $inputPath

    $run = Invoke-TriageProcess -InputPath $inputPath -OutputPath $outputPath
    Assert-True ($run.ExitCode -ne 0) "invalid time range unexpectedly succeeded"
    Assert-True (-not (Test-Path -LiteralPath $outputPath)) "invalid time range generated a report"
  }

  Invoke-Test "missing input file returns an error" {
    $missingInput = Join-Path $tempRoot "missing.json"
    $outputPath = Join-Path $tempRoot "missing-report.json"
    $run = Invoke-TriageProcess -InputPath $missingInput -OutputPath $outputPath
    Assert-True ($run.ExitCode -ne 0) "missing input unexpectedly succeeded"
    Assert-True (-not (Test-Path -LiteralPath $outputPath)) "missing input generated a report"
  }
} finally {
  Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
}

if ($failures.Count -gt 0) {
  foreach ($failure in $failures) {
    Write-Host "  $failure"
  }
  throw "$($failures.Count) support triage test(s) failed"
}

Write-Host "PASS: all support triage tests"
