param(
  [Parameter(Mandatory = $true)]
  [string]$ConfigPath,

  [Parameter(Mandatory = $true)]
  [string]$OutputPath,

  [switch]$AllowNonLoopback
)

$ErrorActionPreference = "Stop"

function Test-Property {
  param(
    $Object,
    [string]$Name
  )

  return $null -ne $Object -and $Object.PSObject.Properties.Name -contains $Name
}

function Get-RequiredText {
  param(
    $Object,
    [string]$Name,
    [string]$Context
  )

  if (-not (Test-Property -Object $Object -Name $Name)) {
    throw "$Context.$Name is required"
  }
  $value = [string]$Object.$Name
  if ([string]::IsNullOrWhiteSpace($value)) {
    throw "$Context.$Name must not be empty"
  }
  return $value.Trim()
}

function Get-OptionalText {
  param(
    $Object,
    [string]$Name
  )

  if (-not (Test-Property -Object $Object -Name $Name)) {
    return $null
  }
  $value = [string]$Object.$Name
  if ([string]::IsNullOrWhiteSpace($value)) {
    return $null
  }
  return $value.Trim()
}

function Assert-NoSensitiveKeys {
  param(
    $Value,
    [string]$Path = "config"
  )

  if ($null -eq $Value) {
    return
  }
  if ($Value -is [string] -or $Value -is [ValueType]) {
    return
  }
  if ($Value -is [Collections.IEnumerable] -and -not ($Value -is [Management.Automation.PSCustomObject])) {
    $index = 0
    foreach ($item in $Value) {
      Assert-NoSensitiveKeys -Value $item -Path "$Path[$index]"
      $index++
    }
    return
  }

  foreach ($property in $Value.PSObject.Properties) {
    $name = $property.Name.ToLowerInvariant()
    if ($name -match "password|token|secret|private.?key|key_pem|clipboard|rdp_content|file_content|user_content") {
      throw "$Path.$($property.Name) is not allowed in benchmark configuration"
    }
    Assert-NoSensitiveKeys -Value $property.Value -Path "$Path.$($property.Name)"
  }
}

function Convert-ToNonNegativeInt {
  param(
    $Value,
    [string]$Name,
    [int]$Minimum = 0
  )

  $number = 0
  if (-not [int]::TryParse([string]$Value, [Globalization.NumberStyles]::Integer, [Globalization.CultureInfo]::InvariantCulture, [ref]$number)) {
    throw "$Name must be an integer"
  }
  if ($number -lt $Minimum) {
    throw "$Name must be at least $Minimum"
  }
  return $number
}

function Convert-ToNonNegativeInt64 {
  param(
    $Value,
    [string]$Name
  )

  $number = [long]0
  if (-not [long]::TryParse([string]$Value, [Globalization.NumberStyles]::Integer, [Globalization.CultureInfo]::InvariantCulture, [ref]$number)) {
    throw "$Name must be an integer"
  }
  if ($number -lt 0) {
    throw "$Name must not be negative"
  }
  return $number
}

function Convert-ToNonNegativeDouble {
  param(
    $Value,
    [string]$Name
  )

  $number = [double]0
  if (-not [double]::TryParse([string]$Value, [Globalization.NumberStyles]::Float, [Globalization.CultureInfo]::InvariantCulture, [ref]$number)) {
    throw "$Name must be numeric"
  }
  if ([double]::IsNaN($number) -or [double]::IsInfinity($number) -or $number -lt 0) {
    throw "$Name must be a finite non-negative number"
  }
  return $number
}

function Round-Metric {
  param([double]$Value)

  return [Math]::Round($Value, 6, [MidpointRounding]::AwayFromZero)
}

function Get-NearestRank {
  param(
    [double[]]$SortedValues,
    [double]$Percentile
  )

  if ($SortedValues.Count -eq 0) {
    return $null
  }
  $index = [Math]::Ceiling($Percentile * $SortedValues.Count) - 1
  if ($index -lt 0) {
    $index = 0
  }
  return Round-Metric -Value $SortedValues[$index]
}

function New-Statistics {
  param([double[]]$Values)

  if ($Values.Count -eq 0) {
    return [ordered]@{
      count = 0
      min = $null
      p50 = $null
      p95 = $null
      p99 = $null
      max = $null
      mean = $null
    }
  }
  [double[]]$sorted = @($Values | Sort-Object)
  $sum = 0.0
  foreach ($value in $sorted) {
    $sum += $value
  }
  return [ordered]@{
    count = $sorted.Count
    min = Round-Metric -Value $sorted[0]
    p50 = Get-NearestRank -SortedValues $sorted -Percentile 0.50
    p95 = Get-NearestRank -SortedValues $sorted -Percentile 0.95
    p99 = Get-NearestRank -SortedValues $sorted -Percentile 0.99
    max = Round-Metric -Value $sorted[$sorted.Count - 1]
    mean = Round-Metric -Value ($sum / $sorted.Count)
  }
}

function New-ResourceStatistics {
  param([double[]]$Values)

  $stats = New-Statistics -Values $Values
  return [ordered]@{
    status = if ($stats.count -gt 0) { "measured" } else { "not_available" }
    count = $stats.count
    min = $stats.min
    p50 = $stats.p50
    p95 = $stats.p95
    p99 = $stats.p99
    max = $stats.max
    mean = $stats.mean
  }
}

function New-PendingDurationMetric {
  param([string]$Source)

  $stats = New-Statistics -Values @()
  return [ordered]@{
    status = "pending_real_environment"
    source = $Source
    unit = "ms"
    count = $stats.count
    min = $stats.min
    p50 = $stats.p50
    p95 = $stats.p95
    p99 = $stats.p99
    max = $stats.max
    mean = $stats.mean
  }
}

function New-DurationMetric {
  param(
    [double[]]$Values,
    [string]$Source
  )

  $stats = New-Statistics -Values $Values
  return [ordered]@{
    status = "measured"
    source = $Source
    unit = "ms"
    count = $stats.count
    min = $stats.min
    p50 = $stats.p50
    p95 = $stats.p95
    p99 = $stats.p99
    max = $stats.max
    mean = $stats.mean
  }
}

function Test-LoopbackHost {
  param([string]$HostValue)

  if ($HostValue -eq "localhost" -or $HostValue -eq "127.0.0.1" -or $HostValue -eq "::1") {
    return $true
  }
  $address = $null
  if ([Net.IPAddress]::TryParse($HostValue, [ref]$address)) {
    return [Net.IPAddress]::IsLoopback($address)
  }
  return $false
}

function Invoke-TcpProbe {
  param(
    [string]$HostValue,
    [int]$Port,
    [int]$TimeoutMS,
    [int]$WarmupCount,
    [int]$SampleCount,
    [string]$TargetLabel
  )

  $values = [Collections.Generic.List[double]]::new()
  $successes = 0
  for ($index = 0; $index -lt ($WarmupCount + $SampleCount); $index++) {
    $client = [Net.Sockets.TcpClient]::new()
    $stopwatch = [Diagnostics.Stopwatch]::StartNew()
    $connected = $false
    try {
      $asyncResult = $client.BeginConnect($HostValue, $Port, $null, $null)
      if ($asyncResult.AsyncWaitHandle.WaitOne($TimeoutMS, $false)) {
        $client.EndConnect($asyncResult)
        $connected = $client.Connected
      }
    } catch {
      $connected = $false
    } finally {
      $stopwatch.Stop()
      $client.Dispose()
    }
    if ($index -ge $WarmupCount -and $connected) {
      $successes++
      $values.Add($stopwatch.Elapsed.TotalMilliseconds)
    }
  }

  $stats = New-Statistics -Values $values.ToArray()
  return [ordered]@{
    status = "measured"
    source = "active_tcp_probe"
    target_label = $TargetLabel
    unit = "ms"
    attempts = $SampleCount
    successes = $successes
    failures = $SampleCount - $successes
    count = $stats.count
    min = $stats.min
    p50 = $stats.p50
    p95 = $stats.p95
    p99 = $stats.p99
    max = $stats.max
    mean = $stats.mean
  }
}

function Get-ProcessSnapshot {
  param([string[]]$ProcessNames)

  $processes = @()
  foreach ($name in $ProcessNames) {
    $processes += @(Get-Process -Name $name -ErrorAction SilentlyContinue)
  }
  $unique = @($processes | Sort-Object -Property Id -Unique)
  $cpuSeconds = 0.0
  $cpuReadable = $true
  $workingSetBytes = [long]0
  foreach ($process in $unique) {
    try {
      $workingSetBytes += [long]$process.WorkingSet64
      if ($null -eq $process.CPU) {
        $cpuReadable = $false
      } else {
        $cpuSeconds += [double]$process.CPU
      }
    } catch {
      $cpuReadable = $false
    }
  }
  $formattedCPUPercent = $null
  if (-not $cpuReadable -and $unique.Count -gt 0) {
    try {
      $processIDs = @($unique | ForEach-Object { [int]$_.Id })
      $formattedCPUPercent = 0.0
      $formattedProcesses = @(Get-CimInstance -ClassName Win32_PerfFormattedData_PerfProc_Process -ErrorAction Stop | Where-Object { $processIDs -contains [int]$_.IDProcess })
      if ($formattedProcesses.Count -eq 0) {
        $formattedCPUPercent = $null
      } else {
        foreach ($formattedProcess in $formattedProcesses) {
          $formattedCPUPercent += [double]$formattedProcess.PercentProcessorTime
        }
      }
    } catch {
      $formattedCPUPercent = $null
    }
  }
  return [pscustomobject]@{
    Count = $unique.Count
    CPUSeconds = if ($cpuReadable) { $cpuSeconds } else { $null }
    FormattedCPUPercent = $formattedCPUPercent
    WorkingSetBytes = $workingSetBytes
  }
}

function Measure-ClientResources {
  param(
    [string[]]$ProcessNames,
    [int]$WarmupCount,
    [int]$SampleCount,
    [int]$IntervalMS,
    [int]$LogicalProcessors
  )

  if ($ProcessNames.Count -eq 0) {
    return [ordered]@{
      status = "not_available"
      source = "local_process_counters"
      configured_process_names = @()
      matched_process_count = 0
      sample_count = 0
      cpu_percent = New-ResourceStatistics -Values @()
      working_set_bytes = New-ResourceStatistics -Values @()
    }
  }

  $cpuValues = [Collections.Generic.List[double]]::new()
  $memoryValues = [Collections.Generic.List[double]]::new()
  $maxProcessCount = 0
  for ($index = 0; $index -lt ($WarmupCount + $SampleCount); $index++) {
    $before = Get-ProcessSnapshot -ProcessNames $ProcessNames
    Start-Sleep -Milliseconds $IntervalMS
    $after = Get-ProcessSnapshot -ProcessNames $ProcessNames
    $matched = [Math]::Max($before.Count, $after.Count)
    $maxProcessCount = [Math]::Max($maxProcessCount, $matched)
    if ($index -lt $WarmupCount -or $matched -eq 0) {
      continue
    }
    $memoryValues.Add([double]$after.WorkingSetBytes)
    if ($null -ne $before.CPUSeconds -and $null -ne $after.CPUSeconds) {
      $delta = [double]$after.CPUSeconds - [double]$before.CPUSeconds
      if ($delta -ge 0) {
        $cpu = ($delta * 1000.0 / $IntervalMS / [Math]::Max(1, $LogicalProcessors)) * 100.0
        $cpuValues.Add([Math]::Min(100.0, [Math]::Max(0.0, $cpu)))
      }
    } elseif ($null -ne $after.FormattedCPUPercent) {
      $cpu = [double]$after.FormattedCPUPercent / [Math]::Max(1, $LogicalProcessors)
      $cpuValues.Add([Math]::Min(100.0, [Math]::Max(0.0, $cpu)))
    }
  }

  $status = if ($memoryValues.Count -gt 0 -and $cpuValues.Count -gt 0) {
    "measured"
  } elseif ($memoryValues.Count -gt 0 -or $cpuValues.Count -gt 0) {
    "partial"
  } else {
    "not_available"
  }
  return [ordered]@{
    status = $status
    source = "local_process_counters"
    configured_process_names = @($ProcessNames)
    matched_process_count = $maxProcessCount
    sample_count = $memoryValues.Count
    cpu_percent = New-ResourceStatistics -Values $cpuValues.ToArray()
    working_set_bytes = New-ResourceStatistics -Values $memoryValues.ToArray()
  }
}

function Get-LocalEnvironment {
  $cpuModel = [string]$env:PROCESSOR_IDENTIFIER
  $logicalProcessors = [Environment]::ProcessorCount
  $totalMemoryBytes = $null
  try {
    $processor = Get-CimInstance -ClassName Win32_Processor -ErrorAction Stop | Select-Object -First 1
    if ($null -ne $processor -and -not [string]::IsNullOrWhiteSpace([string]$processor.Name)) {
      $cpuModel = ([string]$processor.Name).Trim()
    }
    $computer = Get-CimInstance -ClassName Win32_ComputerSystem -ErrorAction Stop
    if ($null -ne $computer.TotalPhysicalMemory) {
      $totalMemoryBytes = [long]$computer.TotalPhysicalMemory
    }
  } catch {
    # The schema keeps nullable hardware fields when local WMI is unavailable.
  }
  return [pscustomobject]@{
    OS = [Environment]::OSVersion.VersionString
    Architecture = [string]$env:PROCESSOR_ARCHITECTURE
    CPUModel = $cpuModel
    LogicalProcessors = $logicalProcessors
    TotalMemoryBytes = $totalMemoryBytes
  }
}

function Get-DefaultProductVersion {
  $versionPath = Join-Path (Split-Path $PSScriptRoot -Parent) "VERSION"
  if (Test-Path -LiteralPath $versionPath -PathType Leaf) {
    return (Get-Content -LiteralPath $versionPath -Raw).Trim()
  }
  return "unknown"
}

function Get-DefaultRevision {
  $root = Split-Path $PSScriptRoot -Parent
  try {
    $revision = (& git -C $root rev-parse --short=12 HEAD 2>$null)
    if ($LASTEXITCODE -eq 0 -and -not [string]::IsNullOrWhiteSpace([string]$revision)) {
      return ([string]$revision).Trim()
    }
  } catch {
    # Keep a stable explicit fallback when git is unavailable.
  }
  return "unknown"
}

$resolvedConfigPath = (Resolve-Path -LiteralPath $ConfigPath).Path
$config = Get-Content -LiteralPath $resolvedConfigPath -Raw | ConvertFrom-Json
Assert-NoSensitiveKeys -Value $config

$configSchema = Get-RequiredText -Object $config -Name "schema_version" -Context "config"
if ($configSchema -ne "task11b.benchmark-config.v1") {
  throw "config.schema_version must be task11b.benchmark-config.v1"
}
$benchmarkID = Get-RequiredText -Object $config -Name "benchmark_id" -Context "config"
if (-not (Test-Property -Object $config -Name "environment")) {
  throw "config.environment is required"
}
$environment = $config.environment
$measurementScope = Get-RequiredText -Object $environment -Name "measurement_scope" -Context "config.environment"
if ($measurementScope -notin @("local_simulated", "real_windows_network")) {
  throw "config.environment.measurement_scope must be local_simulated or real_windows_network"
}
$scenario = Get-RequiredText -Object $environment -Name "scenario" -Context "config.environment"
$hardwareLabel = Get-RequiredText -Object $environment -Name "hardware_label" -Context "config.environment"
$productVersion = Get-OptionalText -Object $environment -Name "product_version"
if ($null -eq $productVersion) {
  $productVersion = Get-DefaultProductVersion
}
$sourceRevision = Get-OptionalText -Object $environment -Name "source_revision"
if ($null -eq $sourceRevision) {
  $sourceRevision = Get-DefaultRevision
}
$peerHardwareLabel = Get-OptionalText -Object $environment -Name "peer_hardware_label"
$hubVersion = Get-OptionalText -Object $environment -Name "hub_version"
$relayVersion = Get-OptionalText -Object $environment -Name "relay_version"
$networkProfile = Get-OptionalText -Object $environment -Name "network_profile"
$natProfile = Get-OptionalText -Object $environment -Name "nat_profile"
$rdpProfile = Get-OptionalText -Object $environment -Name "rdp_profile"

if (-not (Test-Property -Object $config -Name "sampling")) {
  throw "config.sampling is required"
}
$sampling = $config.sampling
$warmupCount = Convert-ToNonNegativeInt -Value $sampling.warmup_count -Name "sampling.warmup_count"
$sampleCount = Convert-ToNonNegativeInt -Value $sampling.sample_count -Name "sampling.sample_count" -Minimum 1
$processIntervalMS = Convert-ToNonNegativeInt -Value $sampling.process_interval_ms -Name "sampling.process_interval_ms" -Minimum 50
$probeTimeoutMS = Convert-ToNonNegativeInt -Value $sampling.probe_timeout_ms -Name "sampling.probe_timeout_ms" -Minimum 1

$connectionMetric = New-PendingDurationMetric -Source "active_tcp_probe_not_configured"
if (Test-Property -Object $config -Name "connection_probe") {
  $probe = $config.connection_probe
  if ((Test-Property -Object $probe -Name "enabled") -and [bool]$probe.enabled) {
    $probeHost = Get-RequiredText -Object $probe -Name "host" -Context "config.connection_probe"
    if (-not $AllowNonLoopback -and -not (Test-LoopbackHost -HostValue $probeHost)) {
      throw "non-loopback connection_probe requires -AllowNonLoopback"
    }
    $probePort = Convert-ToNonNegativeInt -Value $probe.port -Name "connection_probe.port" -Minimum 1
    if ($probePort -gt 65535) {
      throw "connection_probe.port must not exceed 65535"
    }
    $targetLabel = Get-RequiredText -Object $probe -Name "target_label" -Context "config.connection_probe"
    $connectionMetric = Invoke-TcpProbe -HostValue $probeHost -Port $probePort -TimeoutMS $probeTimeoutMS -WarmupCount $warmupCount -SampleCount $sampleCount -TargetLabel $targetLabel
  }
}

$observations = if (Test-Property -Object $config -Name "observations") { $config.observations } else { [pscustomobject]@{} }
$connectionValues = [Collections.Generic.List[double]]::new()
if (Test-Property -Object $observations -Name "connection_establishment_ms") {
  foreach ($value in @($observations.connection_establishment_ms)) {
    if ($null -ne $value) {
      $connectionValues.Add((Convert-ToNonNegativeDouble -Value $value -Name "observations.connection_establishment_ms"))
    }
  }
}
if ($connectionValues.Count -gt 0) {
  $connectionMetric = New-DurationMetric -Values $connectionValues.ToArray() -Source "explicit_product_connection_observations"
}
$rdpValues = [Collections.Generic.List[double]]::new()
if (Test-Property -Object $observations -Name "rdp_first_frame_ms") {
  foreach ($value in @($observations.rdp_first_frame_ms)) {
    if ($null -ne $value) {
      $rdpValues.Add((Convert-ToNonNegativeDouble -Value $value -Name "observations.rdp_first_frame_ms"))
    }
  }
}
$rdpMetric = if ($rdpValues.Count -gt 0) {
  New-DurationMetric -Values $rdpValues.ToArray() -Source "explicit_rdp_first_frame_observations"
} else {
  New-PendingDurationMetric -Source "real_rdp_observation_required"
}

$pathTypes = @()
if (Test-Property -Object $observations -Name "direct_path_types") {
  $pathTypes = @($observations.direct_path_types)
}
$directMetric = [ordered]@{
  status = "pending_real_environment"
  source = "product_path_observations_required"
  attempts = 0
  direct_successes = 0
  ratio = $null
  percent = $null
}
if ($pathTypes.Count -gt 0) {
  $directSuccesses = 0
  foreach ($path in $pathTypes) {
    $normalized = ([string]$path).Trim().ToLowerInvariant()
    if ($normalized -notin @("lan_direct", "public_direct", "relay", "failed")) {
      throw "observations.direct_path_types contains unsupported value: $path"
    }
    if ($normalized -eq "lan_direct" -or $normalized -eq "public_direct") {
      $directSuccesses++
    }
  }
  $ratio = [double]$directSuccesses / $pathTypes.Count
  $directMetric = [ordered]@{
    status = "measured"
    source = "explicit_product_path_observations"
    attempts = $pathTypes.Count
    direct_successes = $directSuccesses
    ratio = Round-Metric -Value $ratio
    percent = Round-Metric -Value ($ratio * 100.0)
  }
}

$hasRelayIn = Test-Property -Object $observations -Name "relay_bytes_in"
$hasRelayOut = Test-Property -Object $observations -Name "relay_bytes_out"
if ($hasRelayIn -xor $hasRelayOut) {
  throw "observations.relay_bytes_in and relay_bytes_out must be provided together"
}
$relayMetric = [ordered]@{
  status = "pending_real_environment"
  source = "relay_counter_observations_required"
  bytes_in = $null
  bytes_out = $null
  total_bytes = $null
  gib = $null
}
if ($hasRelayIn -and $hasRelayOut) {
  $relayBytesIn = Convert-ToNonNegativeInt64 -Value $observations.relay_bytes_in -Name "observations.relay_bytes_in"
  $relayBytesOut = Convert-ToNonNegativeInt64 -Value $observations.relay_bytes_out -Name "observations.relay_bytes_out"
  if ($relayBytesIn -gt ([long]::MaxValue - $relayBytesOut)) {
    throw "relay byte total exceeds Int64 capacity"
  }
  $relayTotalBytes = $relayBytesIn + $relayBytesOut
  $relayGiB = [double]$relayTotalBytes / 1073741824.0
  $relayMetric = [ordered]@{
    status = "measured"
    source = "explicit_relay_counter_observations"
    bytes_in = $relayBytesIn
    bytes_out = $relayBytesOut
    total_bytes = $relayTotalBytes
    gib = Round-Metric -Value $relayGiB
  }
}

$costMetric = [ordered]@{
  status = "pending_explicit_cost_input"
  source = "explicit_price_region_currency_required"
  region = $null
  currency = $null
  price_per_gib = $null
  billable_gib = if ($relayMetric.status -eq "measured") { $relayMetric.gib } else { $null }
  amount = $null
  formula = "(relay_bytes_in + relay_bytes_out) / 1073741824 * price_per_gib"
  assumptions = @("1 GiB = 1073741824 bytes", "bytes_in and bytes_out are both billable")
}
if (Test-Property -Object $config -Name "relay_cost") {
  $relayCost = $config.relay_cost
  if (Test-Property -Object $relayCost -Name "price_per_gib") {
    $pricePerGiB = Convert-ToNonNegativeDouble -Value $relayCost.price_per_gib -Name "relay_cost.price_per_gib"
    $region = Get-RequiredText -Object $relayCost -Name "region" -Context "config.relay_cost"
    $currency = (Get-RequiredText -Object $relayCost -Name "currency" -Context "config.relay_cost").ToUpperInvariant()
    if ($currency -notmatch "^[A-Z]{3}$") {
      throw "config.relay_cost.currency must be a three-letter currency code"
    }
    if ($relayMetric.status -eq "measured") {
      $amount = ([double]$relayMetric.total_bytes / 1073741824.0) * $pricePerGiB
      $costMetric = [ordered]@{
        status = "measured"
        source = "explicit_cost_input"
        region = $region
        currency = $currency
        price_per_gib = Round-Metric -Value $pricePerGiB
        billable_gib = $relayMetric.gib
        amount = [Math]::Round($amount, 12, [MidpointRounding]::AwayFromZero)
        formula = "(relay_bytes_in + relay_bytes_out) / 1073741824 * price_per_gib"
        assumptions = @("1 GiB = 1073741824 bytes", "bytes_in and bytes_out are both billable")
      }
    } else {
      $costMetric.status = "pending_relay_traffic"
      $costMetric.source = "explicit_cost_input_without_relay_counters"
      $costMetric.region = $region
      $costMetric.currency = $currency
      $costMetric.price_per_gib = Round-Metric -Value $pricePerGiB
    }
  }
}

$processNames = @()
if (Test-Property -Object $config -Name "process_names") {
  $processNames = @($config.process_names | ForEach-Object { ([string]$_).Trim() } | Where-Object { $_ })
}
$localEnvironment = Get-LocalEnvironment
$resourceMetric = Measure-ClientResources -ProcessNames $processNames -WarmupCount $warmupCount -SampleCount $sampleCount -IntervalMS $processIntervalMS -LogicalProcessors $localEnvironment.LogicalProcessors

$pending = [Collections.Generic.List[string]]::new()
foreach ($entry in @(
  @{ Name = "connection_establishment_ms"; Status = $connectionMetric.status },
  @{ Name = "rdp_first_frame_ms"; Status = $rdpMetric.status },
  @{ Name = "direct_success_rate"; Status = $directMetric.status },
  @{ Name = "relay_traffic"; Status = $relayMetric.status },
  @{ Name = "relay_bandwidth_cost"; Status = $costMetric.status },
  @{ Name = "client_resources"; Status = $resourceMetric.status }
)) {
  if ($entry.Status -ne "measured") {
    $pending.Add($entry.Name)
  }
}

$result = [ordered]@{
  schema_version = "task11b.performance-result.v1"
  generated_at_utc = [DateTime]::UtcNow.ToString("o", [Globalization.CultureInfo]::InvariantCulture)
  benchmark_id = $benchmarkID
  environment = [ordered]@{
    measurement_scope = $measurementScope
    scenario = $scenario
    product_version = $productVersion
    source_revision = $sourceRevision
    hardware_label = $hardwareLabel
    peer_hardware_label = $peerHardwareLabel
    hub_version = $hubVersion
    relay_version = $relayVersion
    network_profile = $networkProfile
    nat_profile = $natProfile
    rdp_profile = $rdpProfile
    os = $localEnvironment.OS
    architecture = $localEnvironment.Architecture
    cpu_model = $localEnvironment.CPUModel
    logical_processors = $localEnvironment.LogicalProcessors
    total_memory_bytes = $localEnvironment.TotalMemoryBytes
  }
  methodology = [ordered]@{
    warmup_count = $warmupCount
    sample_count = $sampleCount
    process_interval_ms = $processIntervalMS
    probe_timeout_ms = $probeTimeoutMS
    percentile_method = "nearest_rank"
  }
  privacy = [ordered]@{
    contains_user_content = $false
    contains_credentials = $false
    telemetry_sent = $false
  }
  metrics = [ordered]@{
    connection_establishment_ms = $connectionMetric
    rdp_first_frame_ms = $rdpMetric
    direct_success_rate = $directMetric
    relay_traffic = $relayMetric
    relay_bandwidth_cost = $costMetric
    client_resources = $resourceMetric
  }
  pending_real_environment = $pending.ToArray()
}

$outputDirectory = Split-Path -Parent $OutputPath
if (-not [string]::IsNullOrWhiteSpace($outputDirectory)) {
  [IO.Directory]::CreateDirectory([IO.Path]::GetFullPath($outputDirectory)) | Out-Null
}
$resolvedOutputPath = [IO.Path]::GetFullPath($OutputPath)
$json = $result | ConvertTo-Json -Depth 12
[IO.File]::WriteAllText($resolvedOutputPath, $json, [Text.UTF8Encoding]::new($false))
Write-Output $resolvedOutputPath
