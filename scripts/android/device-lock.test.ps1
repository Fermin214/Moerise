#requires -Version 7.2
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'device-lock.ps1')
$repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$testRoot = Join-Path $repo ".local/android/lock-test-$([Guid]::NewGuid().ToString('N'))"
$null = New-Item -ItemType Directory -Path $testRoot -Force
# 测试只替换自身脚本作用域的路径，子入口的生产常量不变。
$script:MoeriseIntegrationRoot = $testRoot
$script:MoeriseDeviceLockPath = Join-Path $testRoot 'device-build.lock'
$savedToken = $env:MOERISE_ANDROID_LOCK_TOKEN
$stream = $null
function Assert-Value([bool] $Condition, [string] $Message) {
    if (-not $Condition) { throw $Message }
}
function Write-Record($Record) {
    $bytes = [Text.Encoding]::UTF8.GetBytes(($Record | ConvertTo-Json) + "`n")
    $stream.SetLength(0); $stream.Position = 0; $stream.Write($bytes, 0, $bytes.Length); $stream.Flush($true)
}
function Expect-Failure([scriptblock] $Command, [string] $Pattern) {
    try { & $Command } catch {
        Assert-Value ($_.Exception.Message -match $Pattern) "异常不符合预期：$($_.Exception.Message)"
        return
    }
    throw '应拒绝该设备锁'
}
try {
    $stream = [IO.File]::Open($script:MoeriseDeviceLockPath, 'OpenOrCreate', 'ReadWrite', 'Read')
    $record = [ordered] @{ token = 'isolated-test-token'; pid = $PID
        process_start_ticks = (Get-Process -Id $PID).StartTime.ToUniversalTime().Ticks.ToString()
        source_root = $testRoot; owner = 'test'; task = 'lock'; output_root = $testRoot }
    Write-Record $record
    $env:MOERISE_ANDROID_LOCK_TOKEN = $record.token
    Assert-Value (Test-MoeriseDeviceLockBusy) '排他句柄应占用锁'
    $null = Assert-MoeriseDeviceLock $testRoot
    Expect-Failure { $probe = [IO.File]::Open($script:MoeriseDeviceLockPath, 'Open', 'ReadWrite', 'Read'); $probe.Dispose() } 'being used|正由|占用'
    $env:MOERISE_ANDROID_LOCK_TOKEN = 'copied-wrong-token'
    Expect-Failure { $null = Assert-MoeriseDeviceLock $testRoot } '需要通过'
    $env:MOERISE_ANDROID_LOCK_TOKEN = $record.token
    $realTicks = $record.process_start_ticks
    $record.process_start_ticks = '1'
    Write-Record $record
    Expect-Failure { $null = Assert-MoeriseDeviceLock $testRoot } 'PID 被复用'
    $record.process_start_ticks = $realTicks
    Write-Record $record
    $child = @'
. $env:MOERISE_TEST_HELPER
$script:MoeriseIntegrationRoot = $env:MOERISE_TEST_ROOT
$script:MoeriseDeviceLockPath = Join-Path $env:MOERISE_TEST_ROOT 'device-build.lock'
$null = Assert-MoeriseDeviceLock $env:MOERISE_TEST_ROOT
'@
    $env:MOERISE_TEST_HELPER = Join-Path $PSScriptRoot 'device-lock.ps1'
    $env:MOERISE_TEST_ROOT = $testRoot
    & pwsh -NoProfile -EncodedCommand ([Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($child)))
    Assert-Value ($LASTEXITCODE -eq 0) '子进程必须能继承活跃锁'
    $stream.Dispose(); $stream = $null
    Assert-Value (-not (Test-MoeriseDeviceLockBusy)) '残留记录不能占锁'
    Expect-Failure { $null = Assert-MoeriseDeviceLock $testRoot } '需要通过'
    # 另一进程持锁时，父进程复制其令牌也不能冒充后代。
    $foreign = @'
$ErrorActionPreference = 'Stop'
$path = Join-Path $env:MOERISE_TEST_ROOT 'device-build.lock'
$handle = [IO.File]::Open($path, 'Open', 'ReadWrite', 'Read')
try {
    $record = @{ token = 'isolated-test-token'; pid = $PID; source_root = $env:MOERISE_TEST_ROOT
        process_start_ticks = (Get-Process -Id $PID).StartTime.ToUniversalTime().Ticks.ToString() }
    $bytes = [Text.Encoding]::UTF8.GetBytes(($record | ConvertTo-Json) + "`n")
    $handle.SetLength(0); $handle.Write($bytes, 0, $bytes.Length); $handle.Flush($true)
    [IO.File]::WriteAllText((Join-Path $env:MOERISE_TEST_ROOT 'ready'), 'ready')
    $deadline = [DateTime]::UtcNow.AddSeconds(10)
    while (-not (Test-Path (Join-Path $env:MOERISE_TEST_ROOT 'done')) -and [DateTime]::UtcNow -lt $deadline) {
        Start-Sleep -Milliseconds 50
    }
} finally { $handle.Dispose() }
'@
    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($foreign))
    $foreignProcess = Start-Process pwsh -ArgumentList @('-NoProfile', '-EncodedCommand', $encoded) `
        -WindowStyle Hidden -PassThru
    $deadline = [DateTime]::UtcNow.AddSeconds(10)
    while (-not (Test-Path -LiteralPath (Join-Path $testRoot 'ready')) -and [DateTime]::UtcNow -lt $deadline) {
        Start-Sleep -Milliseconds 50
    }
    Assert-Value (Test-Path -LiteralPath (Join-Path $testRoot 'ready')) '持锁子进程启动超时'
    Expect-Failure { $null = Assert-MoeriseDeviceLock $testRoot } '不属于设备锁持有者'
    [IO.File]::WriteAllText((Join-Path $testRoot 'done'), 'done')
    Assert-Value ($foreignProcess.WaitForExit(10000)) '持锁子进程退出超时'
    Assert-Value (-not (Test-MoeriseDeviceLockBusy)) '子进程退出后应释放句柄'
    Write-Host '跨进程互斥、残留文件、错误令牌、PID重用及进程继承检查通过。'
} finally {
    if ($stream) { $stream.Dispose() }
    $env:MOERISE_ANDROID_LOCK_TOKEN = $savedToken
    Remove-Item Env:MOERISE_TEST_HELPER, Env:MOERISE_TEST_ROOT -ErrorAction SilentlyContinue
    $resolved = (Resolve-Path -LiteralPath $testRoot).Path
    if (-not $resolved.StartsWith("$repo\.local\android\", [StringComparison]::OrdinalIgnoreCase)) {
        throw '测试清理路径超出仓库'
    }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
