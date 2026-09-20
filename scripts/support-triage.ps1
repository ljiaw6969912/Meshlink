[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [ValidateNotNullOrEmpty()]
  [string]$InputPath,

  [string]$OutputPath
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Assert-AllowedProperties {
  param(
    [Parameter(Mandatory = $true)]
    $Value,
    [Parameter(Mandatory = $true)]
    [string[]]$Allowed,
    [Parameter(Mandatory = $true)]
    [string]$Context
  )

  if ($null -eq $Value -or $Value -is [string] -or $Value -is [Collections.IEnumerable]) {
    throw "$Context must be a JSON object."
  }
  foreach ($property in $Value.PSObject.Properties) {
    if ($Allowed -notcontains $property.Name) {
      throw "Unsupported property '$($property.Name)' in $Context."
    }
  }
}

function Get-OptionalProperty {
  param(
    [Parameter(Mandatory = $true)]
    $Value,
    [Parameter(Mandatory = $true)]
    [string]$Name,
    $Default = $null
  )

  $property = $Value.PSObject.Properties[$Name]
  if ($null -eq $property) {
    return $Default
  }
  return $property.Value
}

function Get-RequiredString {
  param(
    [Parameter(Mandatory = $true)]
    $Value,
    [Parameter(Mandatory = $true)]
    [string]$Name,
    [Parameter(Mandatory = $true)]
    [string]$Context
  )

  $raw = Get-OptionalProperty -Value $Value -Name $Name
  if ($null -eq $raw -or [string]::IsNullOrWhiteSpace([string]$raw)) {
    throw "$Context.$Name is required."
  }
  $text = ([string]$raw).Trim()
  if ($text.Length -gt 256) {
    throw "$Context.$Name exceeds 256 characters."
  }
  return $text
}

function Get-EnumValue {
  param(
    $Value,
    [Parameter(Mandatory = $true)]
    [string[]]$Allowed,
    [Parameter(Mandatory = $true)]
    [string]$Context,
    [string]$Default = "unknown"
  )

  if ($null -eq $Value -or [string]::IsNullOrWhiteSpace([string]$Value)) {
    return $Default
  }
  $normalized = ([string]$Value).Trim().ToLowerInvariant()
  if ($Allowed -notcontains $normalized) {
    throw "$Context has an unsupported value."
  }
  return $normalized
}

function Get-CodeValue {
  param(
    $Value,
    [Parameter(Mandatory = $true)]
    [string]$Context
  )

  if ($null -eq $Value -or [string]::IsNullOrWhiteSpace([string]$Value)) {
    return ""
  }
  $code = ([string]$Value).Trim().ToLowerInvariant()
  if ($code -notmatch '^[a-z][a-z0-9._-]{0,63}$') {
    throw "$Context must be a structured code of at most 64 characters."
  }
  return $code
}

function Get-UTCTimestamp {
  param(
    $Value,
    [Parameter(Mandatory = $true)]
    [string]$Context,
    [switch]$Required
  )

  if ($null -eq $Value -or [string]::IsNullOrWhiteSpace([string]$Value)) {
    if ($Required) {
      throw "$Context is required."
    }
    return $null
  }
  $text = ([string]$Value).Trim()
  if (-not $text.EndsWith("Z", [StringComparison]::OrdinalIgnoreCase)) {
    throw "$Context must be an RFC 3339 UTC timestamp ending in Z."
  }
  $parsed = [DateTimeOffset]::MinValue
  $valid = [DateTimeOffset]::TryParse(
    $text,
    [Globalization.CultureInfo]::InvariantCulture,
    [Globalization.DateTimeStyles]::RoundtripKind,
    [ref]$parsed
  )
  if (-not $valid) {
    throw "$Context must be an RFC 3339 UTC timestamp ending in Z."
  }
  return $parsed.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ", [Globalization.CultureInfo]::InvariantCulture)
}

function Get-Reference {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Identifier
  )

  $sha256 = [Security.Cryptography.SHA256]::Create()
  try {
    $bytes = [Text.Encoding]::UTF8.GetBytes($Identifier)
    $hash = $sha256.ComputeHash($bytes)
    $hex = -join ($hash | ForEach-Object { $_.ToString("x2") })
    return "sha256:" + $hex.Substring(0, 16)
  } finally {
    $sha256.Dispose()
  }
}

function Read-Signal {
  param(
    $Root,
    [Parameter(Mandatory = $true)]
    [string]$Name,
    [Parameter(Mandatory = $true)]
    [string[]]$AllowedProperties,
    [Parameter(Mandatory = $true)]
    [string[]]$AllowedStatuses,
    [string]$CodeProperty = "reason_code"
  )

  $signal = Get-OptionalProperty -Value $Root -Name $Name
  if ($null -eq $signal) {
    return [ordered]@{
      source = $Name
      status = "unknown"
      observed_at_utc = $null
      code = ""
    }
  }
  Assert-AllowedProperties -Value $signal -Allowed $AllowedProperties -Context $Name
  return [ordered]@{
    source = $Name
    status = Get-EnumValue -Value (Get-OptionalProperty -Value $signal -Name "status") -Allowed $AllowedStatuses -Context "$Name.status"
    observed_at_utc = Get-UTCTimestamp -Value (Get-OptionalProperty -Value $signal -Name "observed_at_utc") -Context "$Name.observed_at_utc"
    code = Get-CodeValue -Value (Get-OptionalProperty -Value $signal -Name $CodeProperty) -Context "$Name.$CodeProperty"
  }
}

function New-Classification {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Category,
    [Parameter(Mandatory = $true)]
    [string]$Layer,
    [Parameter(Mandatory = $true)]
    [string]$Priority,
    [Parameter(Mandatory = $true)]
    [string]$Code,
    [Parameter(Mandatory = $true)]
    [string]$Summary,
    [bool]$StopAndEscalateSecurity = $false
  )

  return [ordered]@{
    category = $Category
    layer = $Layer
    priority = $Priority
    code = $Code
    summary = $Summary
    stop_and_escalate_security = $StopAndEscalateSecurity
  }
}

if (-not (Test-Path -LiteralPath $InputPath -PathType Leaf)) {
  throw "InputPath does not identify a local file."
}

$inputFile = Get-Item -LiteralPath $InputPath
if ($inputFile.Length -gt 1048576) {
  throw "InputPath exceeds the 1 MiB metadata-only limit."
}

$rawInput = Get-Content -LiteralPath $inputFile.FullName -Raw
try {
  $inputData = $rawInput | ConvertFrom-Json
} catch {
  throw "InputPath does not contain valid JSON."
}

Assert-AllowedProperties -Value $inputData -Allowed @(
  "schema_version",
  "case",
  "security",
  "account",
  "login",
  "enrollment",
  "connection",
  "relay",
  "rdp",
  "diagnostic_findings"
) -Context "root"

$schemaVersion = Get-RequiredString -Value $inputData -Name "schema_version" -Context "root"
if ($schemaVersion -ne "meshlink.support-triage-input.v1") {
  throw "root.schema_version is unsupported."
}

$caseData = Get-OptionalProperty -Value $inputData -Name "case"
if ($null -eq $caseData) {
  throw "root.case is required."
}
Assert-AllowedProperties -Value $caseData -Allowed @("account_id", "device_id", "from_utc", "to_utc") -Context "case"
$accountID = Get-RequiredString -Value $caseData -Name "account_id" -Context "case"
$deviceID = Get-RequiredString -Value $caseData -Name "device_id" -Context "case"
$fromUTC = Get-UTCTimestamp -Value (Get-OptionalProperty -Value $caseData -Name "from_utc") -Context "case.from_utc" -Required
$toUTC = Get-UTCTimestamp -Value (Get-OptionalProperty -Value $caseData -Name "to_utc") -Context "case.to_utc" -Required
if ([DateTimeOffset]::Parse($fromUTC) -gt [DateTimeOffset]::Parse($toUTC)) {
  throw "case.from_utc must not be later than case.to_utc."
}

$securityEvidence = Read-Signal -Root $inputData -Name "security" -AllowedProperties @("status", "observed_at_utc", "reason_code") -AllowedStatuses @("clear", "suspected", "confirmed", "unknown")
$accountEvidence = Read-Signal -Root $inputData -Name "account" -AllowedProperties @("status", "observed_at_utc", "reason_code") -AllowedStatuses @("ok", "locked", "disabled", "quota_exceeded", "unknown")
$loginEvidence = Read-Signal -Root $inputData -Name "login" -AllowedProperties @("status", "observed_at_utc", "reason_code") -AllowedStatuses @("ok", "failed", "unknown")
$enrollmentEvidence = Read-Signal -Root $inputData -Name "enrollment" -AllowedProperties @("status", "observed_at_utc", "reason_code") -AllowedStatuses @("ok", "failed", "unknown")
$connectionEvidence = Read-Signal -Root $inputData -Name "connection" -AllowedProperties @("status", "path_type", "path_state", "observed_at_utc", "error_code") -AllowedStatuses @("connected", "failed", "offline", "unknown") -CodeProperty "error_code"
$relayEvidence = Read-Signal -Root $inputData -Name "relay" -AllowedProperties @("status", "observed_at_utc", "error_code") -AllowedStatuses @("ok", "degraded", "failed", "not_used", "unknown") -CodeProperty "error_code"
$rdpEvidence = Read-Signal -Root $inputData -Name "rdp" -AllowedProperties @("status", "enabled", "service_state", "firewall_status", "observed_at_utc", "error_code") -AllowedStatuses @("reachable", "unreachable", "unknown") -CodeProperty "error_code"

$connectionData = Get-OptionalProperty -Value $inputData -Name "connection"
$pathType = "unknown"
$pathState = "unknown"
if ($null -ne $connectionData) {
  $pathType = Get-EnumValue -Value (Get-OptionalProperty -Value $connectionData -Name "path_type") -Allowed @("direct", "lan_direct", "public_direct", "relay", "unknown") -Context "connection.path_type"
  $pathState = Get-EnumValue -Value (Get-OptionalProperty -Value $connectionData -Name "path_state") -Allowed @("connected", "connecting", "trying_lan_direct", "trying_public_direct", "lan_direct_connected", "public_direct_connected", "fallback_relay", "failed", "closed", "offline", "rdp-unreachable", "unknown") -Context "connection.path_state"
}
$connectionEvidence.path_type = $pathType
$connectionEvidence.path_state = $pathState

$rdpData = Get-OptionalProperty -Value $inputData -Name "rdp"
$rdpEnabled = $null
$rdpServiceState = "unknown"
$rdpFirewallStatus = "unknown"
if ($null -ne $rdpData) {
  $rdpEnabled = Get-OptionalProperty -Value $rdpData -Name "enabled"
  if ($null -ne $rdpEnabled -and $rdpEnabled -isnot [bool]) {
    throw "rdp.enabled must be a JSON boolean or null."
  }
  $rdpServiceState = Get-EnumValue -Value (Get-OptionalProperty -Value $rdpData -Name "service_state") -Allowed @("running", "stopped", "unknown") -Context "rdp.service_state"
  $rdpFirewallStatus = Get-EnumValue -Value (Get-OptionalProperty -Value $rdpData -Name "firewall_status") -Allowed @("allowed", "blocked", "unknown") -Context "rdp.firewall_status"
}
$rdpEvidence.enabled = $rdpEnabled
$rdpEvidence.service_state = $rdpServiceState
$rdpEvidence.firewall_status = $rdpFirewallStatus

$findingEvidence = @()
$findingCodes = @()
$findings = Get-OptionalProperty -Value $inputData -Name "diagnostic_findings" -Default @()
if ($null -ne $findings) {
  if ($findings -is [string]) {
    throw "diagnostic_findings must be a JSON array."
  }
  if ($findings -isnot [Collections.IEnumerable] -and $findings -isnot [pscustomobject]) {
    throw "diagnostic_findings must contain JSON objects."
  }
  foreach ($finding in @($findings)) {
    Assert-AllowedProperties -Value $finding -Allowed @("code", "severity", "observed_at_utc") -Context "diagnostic_findings[]"
    $code = Get-CodeValue -Value (Get-OptionalProperty -Value $finding -Name "code") -Context "diagnostic_findings[].code"
    if ([string]::IsNullOrWhiteSpace($code)) {
      throw "diagnostic_findings[].code is required."
    }
    $severity = Get-EnumValue -Value (Get-OptionalProperty -Value $finding -Name "severity") -Allowed @("ok", "warn", "fail") -Context "diagnostic_findings[].severity"
    $findingEvidence += [ordered]@{
      source = "diagnose"
      status = $severity
      observed_at_utc = Get-UTCTimestamp -Value (Get-OptionalProperty -Value $finding -Name "observed_at_utc") -Context "diagnostic_findings[].observed_at_utc"
      code = $code
    }
    $findingCodes += $code
  }
}

$classification = New-Classification -Category "no_issue_identified" -Layer "none" -Priority "P4" -Code "no_issue_identified" -Summary "The supplied metadata does not identify a failing layer."
$actions = @(
  "Confirm the ticket time range and rerun triage if the problem is reproducible.",
  "Do not request or attach user content, credentials, addresses, or raw traffic."
)

if ($securityEvidence.status -in @("suspected", "confirmed")) {
  $classification = New-Classification -Category "security_event" -Layer "security" -Priority "P1" -Code "security_escalation_required" -Summary "Security indicators are present; ordinary support triage must stop." -StopAndEscalateSecurity $true
  $actions = @(
    "Stop ordinary troubleshooting and preserve only approved audit metadata.",
    "Escalate to the security incident commander through the approved channel.",
    "Do not reset, delete, or broaden access until the security owner authorizes it."
  )
} elseif ($accountEvidence.status -in @("locked", "disabled", "quota_exceeded")) {
  $code = if ($accountEvidence.status -eq "quota_exceeded") { "quota_exceeded" } else { "account_unavailable" }
  $classification = New-Classification -Category "account_or_quota" -Layer "account" -Priority "P3" -Code $code -Summary "The account state or quota blocks the requested operation."
  $actions = @(
    "Verify account status and the effective plan or quota with an authorized operator.",
    "Resolve the account or quota condition before network troubleshooting."
  )
} elseif ($loginEvidence.status -eq "failed") {
  $classification = New-Classification -Category "login" -Layer "login" -Priority "P3" -Code "login_failed" -Summary "Authentication did not complete successfully."
  $actions = @(
    "Verify the account state and approved login error code.",
    "Do not request a password, token, one-time code, or screen recording."
  )
} elseif ($enrollmentEvidence.status -eq "failed") {
  $classification = New-Classification -Category "enrollment" -Layer "enrollment" -Priority "P3" -Code "enrollment_failed" -Summary "The device did not complete enrollment."
  $actions = @(
    "Verify device ownership, enrollment status, and approved enrollment error code.",
    "Retry enrollment only after the blocking account or device condition is resolved."
  )
} elseif (
  $connectionEvidence.status -in @("failed", "offline") -or
  $pathState -in @("failed", "closed", "offline") -or
  @($findingCodes | Where-Object { $_ -in @("service_not_running", "virtual_adapter_unavailable", "network_blocked", "target_offline", "connection_failed") }).Count -gt 0
) {
  $classification = New-Classification -Category "network_path" -Layer "path" -Priority "P3" -Code "network_path_unavailable" -Summary "The device or network path is not available."
  $actions = @(
    "Confirm both devices are online and the local agent and virtual adapter are running.",
    "Retry after the approved firewall or network-path condition is corrected."
  )
} elseif ($relayEvidence.status -in @("degraded", "failed") -or $findingCodes -contains "relay_path_anomaly") {
  $classification = New-Classification -Category "relay" -Layer "relay" -Priority "P3" -Code "relay_degraded" -Summary "The Relay path is failed or degraded."
  $actions = @(
    "Check the approved Relay status and quota metadata for the ticket time range.",
    "Retry after Relay health or the network egress condition is restored."
  )
} elseif ($rdpEnabled -eq $false -or $rdpServiceState -eq "stopped") {
  $classification = New-Classification -Category "target_rdp" -Layer "rdp" -Priority "P3" -Code "rdp_not_enabled" -Summary "Remote Desktop is disabled or its service is stopped on the target device."
  $actions = @(
    "On the target Windows device, enable Remote Desktop in Settings.",
    "Confirm the Remote Desktop service is running and the authorized user may sign in.",
    "Run the local diagnosis again; if still unreachable, verify the RDP firewall rule."
  )
} elseif ($rdpFirewallStatus -eq "blocked") {
  $classification = New-Classification -Category "target_rdp" -Layer "rdp" -Priority "P3" -Code "rdp_firewall_blocked" -Summary "The target device firewall blocks Remote Desktop."
  $actions = @(
    "Enable the approved Remote Desktop firewall rule on the target Windows device.",
    "Run the local diagnosis again without collecting user content or traffic."
  )
} elseif ($rdpEvidence.status -eq "unreachable" -or $pathState -eq "rdp-unreachable" -or $findingCodes -contains "rdp_unavailable") {
  $classification = New-Classification -Category "target_rdp" -Layer "rdp" -Priority "P3" -Code "rdp_unreachable" -Summary "The network path is available but the target Remote Desktop endpoint is unreachable."
  $actions = @(
    "Confirm Remote Desktop is enabled and its service is running on the target Windows device.",
    "Verify the approved Remote Desktop firewall rule, then rerun local diagnosis."
  )
}

$report = [ordered]@{
  schema_version = "meshlink.support-triage-report.v1"
  generated_at_utc = [DateTimeOffset]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ss.fffZ", [Globalization.CultureInfo]::InvariantCulture)
  privacy = [ordered]@{
    local_only = $true
    telemetry_sent = $false
    user_content_collected = $false
    credentials_collected = $false
    identifier_transform = "sha256-16"
  }
  case = [ordered]@{
    account_ref = Get-Reference -Identifier $accountID
    device_ref = Get-Reference -Identifier $deviceID
    from_utc = $fromUTC
    to_utc = $toUTC
  }
  classification = $classification
  evidence = @(
    $securityEvidence,
    $accountEvidence,
    $loginEvidence,
    $enrollmentEvidence,
    $connectionEvidence,
    $relayEvidence,
    $rdpEvidence
  ) + $findingEvidence
  actions = $actions
}

if ([string]::IsNullOrWhiteSpace($OutputPath)) {
  $fileName = "support-triage-" + [DateTimeOffset]::UtcNow.ToString("yyyyMMddTHHmmssfffZ") + ".json"
  $OutputPath = Join-Path (Get-Location).Path $fileName
}
$fullOutputPath = [IO.Path]::GetFullPath($OutputPath)
$outputDirectory = Split-Path -Parent $fullOutputPath
if (-not (Test-Path -LiteralPath $outputDirectory -PathType Container)) {
  throw "OutputPath parent directory does not exist."
}
if (Test-Path -LiteralPath $fullOutputPath) {
  throw "OutputPath already exists; choose a new local file."
}

$temporaryPath = $fullOutputPath + ".tmp-" + [Guid]::NewGuid().ToString("N")
try {
  $json = $report | ConvertTo-Json -Depth 12
  [IO.File]::WriteAllText($temporaryPath, $json, [Text.UTF8Encoding]::new($false))
  Move-Item -LiteralPath $temporaryPath -Destination $fullOutputPath
} finally {
  if (Test-Path -LiteralPath $temporaryPath) {
    Remove-Item -LiteralPath $temporaryPath -Force
  }
}

Write-Output $fullOutputPath
