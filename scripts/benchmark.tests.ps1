$ErrorActionPreference = "Stop"

$scriptPath = Join-Path $PSScriptRoot "benchmark.ps1"
if (-not (Test-Path -LiteralPath $scriptPath -PathType Leaf)) {
  throw "benchmark.ps1 is missing"
}
$exampleConfigPath = Join-Path $PSScriptRoot "benchmark.config.example.json"
if (-not (Test-Path -LiteralPath $exampleConfigPath -PathType Leaf)) {
  throw "benchmark.config.example.json is missing"
}

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

$command = Get-Command -Name $scriptPath
foreach ($parameterName in @("ConfigPath", "OutputPath", "AllowNonLoopback")) {
  Assert-True $command.Parameters.ContainsKey($parameterName) "missing parameter $parameterName"
}

$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ("meshlink-task11b-test-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tempRoot | Out-Null
$listener = $null

try {
  $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
  $listener.Start()
  $port = ([Net.IPEndPoint]$listener.LocalEndpoint).Port
  $processName = (Get-Process -Id $PID).ProcessName

  $configPath = Join-Path $tempRoot "measured-config.json"
  $outputPath = Join-Path $tempRoot "measured-result.json"
  $config = [ordered]@{
    schema_version = "task11b.benchmark-config.v1"
    benchmark_id = "task11b-script-test"
    environment = [ordered]@{
      measurement_scope = "local_simulated"
      scenario = "loopback-script-test"
      product_version = "test-version"
      source_revision = "test-revision"
      hardware_label = "test-hardware"
      peer_hardware_label = "test-peer-hardware"
      hub_version = "test-hub-version"
      relay_version = "test-relay-version"
      network_profile = "loopback"
      nat_profile = "not-applicable"
      rdp_profile = "test-rdp-profile"
    }
    sampling = [ordered]@{
      warmup_count = 1
      sample_count = 3
      process_interval_ms = 50
      probe_timeout_ms = 1000
    }
    connection_probe = [ordered]@{
      enabled = $true
      target_label = "loopback-listener"
      host = "127.0.0.1"
      port = $port
    }
    process_names = @($processName)
    observations = [ordered]@{
      rdp_first_frame_ms = @(1000, 1200, 1100)
      direct_path_types = @("lan_direct", "relay", "public_direct", "failed")
      relay_bytes_in = 1073741824
      relay_bytes_out = 536870912
    }
    relay_cost = [ordered]@{
      price_per_gib = 0.25
      region = "test-region"
      currency = "USD"
    }
  }
  Write-JsonFile -Value $config -Path $configPath

  & $scriptPath -ConfigPath $configPath -OutputPath $outputPath | Out-Null
  Assert-True (Test-Path -LiteralPath $outputPath -PathType Leaf) "result file was not created"
  $raw = Get-Content -LiteralPath $outputPath -Raw
  $result = $raw | ConvertFrom-Json

  Assert-Equal $result.schema_version "task11b.performance-result.v1" "unexpected result schema version"
  Assert-Equal $result.benchmark_id "task11b-script-test" "benchmark id was not preserved"
  Assert-Equal $result.environment.measurement_scope "local_simulated" "measurement scope was not preserved"
  Assert-Equal $result.environment.peer_hardware_label "test-peer-hardware" "peer hardware label was not preserved"
  Assert-Equal $result.environment.hub_version "test-hub-version" "Hub version was not preserved"
  Assert-Equal $result.environment.relay_version "test-relay-version" "Relay version was not preserved"
  Assert-Equal $result.environment.network_profile "loopback" "network profile was not preserved"
  Assert-Equal $result.environment.nat_profile "not-applicable" "NAT profile was not preserved"
  Assert-Equal $result.environment.rdp_profile "test-rdp-profile" "RDP profile was not preserved"
  Assert-Equal $result.methodology.percentile_method "nearest_rank" "percentile method must be explicit"
  Assert-Equal $result.metrics.connection_establishment_ms.status "measured" "loopback connection should be measured"
  Assert-Equal $result.metrics.connection_establishment_ms.count 3 "connection sample count excludes warmup"
  Assert-Equal $result.metrics.rdp_first_frame_ms.p50 1100 "RDP p50 must use nearest-rank"
  Assert-Equal $result.metrics.rdp_first_frame_ms.p95 1200 "RDP p95 must use nearest-rank"
  Assert-Equal $result.metrics.direct_success_rate.attempts 4 "direct attempt count is wrong"
  Assert-Equal $result.metrics.direct_success_rate.direct_successes 2 "direct success count is wrong"
  Assert-Equal $result.metrics.direct_success_rate.percent 50 "direct success percent is wrong"
  Assert-Equal $result.metrics.relay_traffic.total_bytes 1610612736 "relay total bytes is wrong"
  Assert-Equal $result.metrics.relay_bandwidth_cost.billable_gib 1.5 "relay GiB conversion is wrong"
  Assert-Equal $result.metrics.relay_bandwidth_cost.amount 0.375 "relay cost formula is wrong"
  Assert-Equal $result.metrics.relay_bandwidth_cost.currency "USD" "cost currency was not preserved"
  Assert-Equal $result.metrics.client_resources.status "measured" "current PowerShell process should be sampled"
  Assert-Equal $result.metrics.client_resources.cpu_percent.status "measured" "CPU submetric must expose its own status"
  Assert-Equal $result.metrics.client_resources.working_set_bytes.status "measured" "memory submetric must expose its own status"
  Assert-True ($result.metrics.client_resources.working_set_bytes.p50 -gt 0) "working set p50 must be positive"
  Assert-Equal $result.privacy.contains_user_content $false "result must declare no user content"
  Assert-Equal $result.privacy.contains_credentials $false "result must declare no credentials"
  Assert-Equal $result.privacy.telemetry_sent $false "script must not send telemetry"
  foreach ($forbidden in @("password", "token", "private_key", "clipboard", "rdp_content")) {
    Assert-True (-not $raw.ToLowerInvariant().Contains($forbidden)) "result contains forbidden field or content: $forbidden"
  }

  $pendingConfigPath = Join-Path $tempRoot "pending-config.json"
  $pendingOutputPath = Join-Path $tempRoot "pending-result.json"
  $pendingConfig = [ordered]@{
    schema_version = "task11b.benchmark-config.v1"
    benchmark_id = "task11b-pending-test"
    environment = [ordered]@{
      measurement_scope = "local_simulated"
      scenario = "no-real-observations"
      product_version = "test-version"
      source_revision = "test-revision"
      hardware_label = "test-hardware"
    }
    sampling = [ordered]@{
      warmup_count = 0
      sample_count = 1
      process_interval_ms = 50
      probe_timeout_ms = 100
    }
    connection_probe = [ordered]@{ enabled = $false }
    process_names = @("task11b-process-that-does-not-exist")
    observations = [ordered]@{
      rdp_first_frame_ms = @()
      direct_path_types = @()
    }
  }
  Write-JsonFile -Value $pendingConfig -Path $pendingConfigPath

  & $scriptPath -ConfigPath $pendingConfigPath -OutputPath $pendingOutputPath | Out-Null
  $pending = Get-Content -LiteralPath $pendingOutputPath -Raw | ConvertFrom-Json
  Assert-Equal $pending.metrics.connection_establishment_ms.status "pending_real_environment" "disabled connection probe must stay pending"
  Assert-Equal $pending.metrics.rdp_first_frame_ms.status "pending_real_environment" "missing RDP evidence must stay pending"
  Assert-Equal $pending.metrics.direct_success_rate.status "pending_real_environment" "missing path evidence must stay pending"
  Assert-Equal $pending.metrics.relay_traffic.status "pending_real_environment" "missing relay counters must stay pending"
  Assert-Equal $pending.metrics.relay_bandwidth_cost.status "pending_explicit_cost_input" "missing price must not use a built-in value"
  Assert-Equal $pending.metrics.client_resources.status "not_available" "missing process must be reported honestly"
  Assert-Equal $pending.metrics.client_resources.cpu_percent.status "not_available" "missing CPU samples need an explicit status"
  Assert-Equal $pending.metrics.client_resources.working_set_bytes.status "not_available" "missing memory samples need an explicit status"

  $connectionObservationPath = Join-Path $tempRoot "connection-observation.json"
  $connectionObservationOutput = Join-Path $tempRoot "connection-observation-output.json"
  $connectionObservation = [ordered]@{
    schema_version = "task11b.benchmark-config.v1"
    benchmark_id = "task11b-connection-observation-test"
    environment = $pendingConfig.environment
    sampling = $pendingConfig.sampling
    connection_probe = [ordered]@{ enabled = $false }
    process_names = @()
    observations = [ordered]@{
      connection_establishment_ms = @(10, 30, 20)
      rdp_first_frame_ms = @()
      direct_path_types = @()
    }
  }
  Write-JsonFile -Value $connectionObservation -Path $connectionObservationPath
  & $scriptPath -ConfigPath $connectionObservationPath -OutputPath $connectionObservationOutput | Out-Null
  $connectionResult = Get-Content -LiteralPath $connectionObservationOutput -Raw | ConvertFrom-Json
  Assert-Equal $connectionResult.metrics.connection_establishment_ms.status "measured" "explicit product connection samples must be aggregated"
  Assert-Equal $connectionResult.metrics.connection_establishment_ms.source "explicit_product_connection_observations" "product connection samples must not be labeled as TCP probe"
  Assert-Equal $connectionResult.metrics.connection_establishment_ms.p50 20 "product connection p50 must use nearest-rank"

  $emptyVersionPath = Join-Path $tempRoot "empty-version.json"
  $emptyVersionOutput = Join-Path $tempRoot "empty-version-output.json"
  $emptyVersion = [ordered]@{
    schema_version = "task11b.benchmark-config.v1"
    benchmark_id = "task11b-empty-version-test"
    environment = [ordered]@{
      measurement_scope = "local_simulated"
      scenario = "empty-version-fallback"
      product_version = ""
      source_revision = ""
      hardware_label = "test-hardware"
    }
    sampling = $pendingConfig.sampling
    connection_probe = [ordered]@{ enabled = $false }
    process_names = @()
    observations = $pendingConfig.observations
  }
  Write-JsonFile -Value $emptyVersion -Path $emptyVersionPath
  & $scriptPath -ConfigPath $emptyVersionPath -OutputPath $emptyVersionOutput | Out-Null
  $emptyVersionResult = Get-Content -LiteralPath $emptyVersionOutput -Raw | ConvertFrom-Json
  $expectedVersion = (Get-Content -LiteralPath (Join-Path (Split-Path $PSScriptRoot -Parent) "VERSION") -Raw).Trim()
  Assert-Equal $emptyVersionResult.environment.product_version $expectedVersion "empty product version must fall back to VERSION"
  Assert-True (-not [string]::IsNullOrWhiteSpace([string]$emptyVersionResult.environment.source_revision)) "empty revision must fall back to the local revision"

  $invalidCostPath = Join-Path $tempRoot "invalid-cost.json"
  $invalidCostOutput = Join-Path $tempRoot "invalid-cost-output.json"
  $invalidCost = $pendingConfig.PSObject.Copy()
  $invalidCost.relay_cost = [ordered]@{ price_per_gib = 0.1; currency = "USD" }
  Write-JsonFile -Value $invalidCost -Path $invalidCostPath
  $invalidCostFailed = $false
  try {
    & $scriptPath -ConfigPath $invalidCostPath -OutputPath $invalidCostOutput | Out-Null
  } catch {
    $invalidCostFailed = $_.Exception.Message -match "region"
  }
  Assert-True $invalidCostFailed "price without an explicit region must fail validation"

  $nonLoopbackPath = Join-Path $tempRoot "non-loopback.json"
  $nonLoopbackOutput = Join-Path $tempRoot "non-loopback-output.json"
  $nonLoopback = [ordered]@{
    schema_version = "task11b.benchmark-config.v1"
    benchmark_id = "task11b-non-loopback-test"
    environment = $config.environment
    sampling = $pendingConfig.sampling
    connection_probe = [ordered]@{
      enabled = $true
      target_label = "documentation-only"
      host = "192.0.2.1"
      port = 9
    }
    process_names = @()
    observations = $pendingConfig.observations
  }
  Write-JsonFile -Value $nonLoopback -Path $nonLoopbackPath
  $nonLoopbackFailed = $false
  try {
    & $scriptPath -ConfigPath $nonLoopbackPath -OutputPath $nonLoopbackOutput | Out-Null
  } catch {
    $nonLoopbackFailed = $_.Exception.Message -match "AllowNonLoopback"
  }
  Assert-True $nonLoopbackFailed "non-loopback probe must require explicit authorization"

  $exampleConfig = Get-Content -LiteralPath $exampleConfigPath -Raw | ConvertFrom-Json
  $exampleConfig.benchmark_id = "task11b-example-config-test"
  $exampleConfig.sampling.warmup_count = 0
  $exampleConfig.sampling.sample_count = 1
  $exampleConfig.sampling.process_interval_ms = 50
  $exampleConfig.process_names = @($processName)
  $exampleTestConfigPath = Join-Path $tempRoot "example-test-config.json"
  $exampleTestOutputPath = Join-Path $tempRoot "example-test-output.json"
  Write-JsonFile -Value $exampleConfig -Path $exampleTestConfigPath
  & $scriptPath -ConfigPath $exampleTestConfigPath -OutputPath $exampleTestOutputPath | Out-Null
  $exampleResult = Get-Content -LiteralPath $exampleTestOutputPath -Raw | ConvertFrom-Json
  Assert-Equal $exampleResult.schema_version "task11b.performance-result.v1" "example config must produce the result schema"
  Assert-Equal $exampleResult.metrics.client_resources.status "measured" "example config must support local process sampling"

  Write-Output "benchmark.tests.ps1: PASS"
} finally {
  if ($null -ne $listener) {
    $listener.Stop()
  }
  Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
}
