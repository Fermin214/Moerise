#requires -Version 7.2
[CmdletBinding()]
param(
    [Parameter(Position = 0)] [ValidateSet('status', 'stop', 'stop-legacy', 'dispose')]
    [string] $Action = 'status',
    [Parameter(Mandatory)] [string] $FixtureRoot,
    [int] $NodePid,
    [string] $ProcessStartTicks,
    [switch] $RequireStopped
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'device-lock.ps1')
$FixtureRoot = Resolve-MoeriseAndroidOutputRoot $FixtureRoot
if ((Split-Path $FixtureRoot -Parent) -ne 'C:\Projects\Moerise\.local\android' -or
    (Split-Path $FixtureRoot -Leaf) -notmatch '^fixture-[a-f0-9]{12}$') { throw '实例目录无效。' }
if ($Action -ne 'status') { $null = Assert-MoeriseDeviceLock }
$lifecyclePath = Join-Path $FixtureRoot 'lifecycle.json'
$active = $false
$state = $null
if (Test-Path -LiteralPath $lifecyclePath) {
    $lifecycle = Get-Content -LiteralPath $lifecyclePath -Raw | ConvertFrom-Json
    $controlOrigin = [uri] $lifecycle.origin
    if ($controlOrigin.Scheme -ne 'http' -or -not $controlOrigin.IsLoopback -or
        $controlOrigin.AbsolutePath -ne '/' -or $controlOrigin.UserInfo -or
        $controlOrigin.Query -or $controlOrigin.Fragment) { throw '实例控制地址无效。' }
    $headers = @{ Authorization = "Bearer $($lifecycle.token)" }
    try {
        $state = Invoke-RestMethod "$($lifecycle.origin)/status" -Headers $headers -TimeoutSec 3
        $active = $state.id -ceq $lifecycle.id -and $state.root -eq $FixtureRoot
    } catch { $active = $false }
}
# 检查精确命令路径，避免旧实例没有控制端口时被误判为已停止。
$processes = @(Get-MoeriseWindowsProcesses @('node.exe', 'moerise.exe') | Where-Object {
    $_.Name -in @('node.exe', 'moerise.exe') -and $_.CommandLine -and
        $_.CommandLine.Replace('/', '\').Contains($FixtureRoot, [StringComparison]::OrdinalIgnoreCase)
})
if ($Action -eq 'status') {
    [pscustomobject] @{ fixture_root = $FixtureRoot; active = $active
        processes = @($processes | Select-Object ProcessId, ParentProcessId, Name, ProcessStartTicks)
        service = $state } | ConvertTo-Json -Depth 5
    if ($RequireStopped -and ($active -or $processes.Count)) { throw '实例进程仍在运行。' }
    return
}
if ($Action -in @('stop', 'dispose') -and $active) {
    $node = Get-Process -Id $state.nodePid -ErrorAction Stop
    $match = @($processes | Where-Object { $_.ProcessId -eq $node.Id -and $_.Name -eq 'node.exe' })
    if ($match.Count -ne 1 -or
        $node.StartTime.ToUniversalTime().Ticks.ToString() -cne $match[0].ProcessStartTicks -or
        $match[0].CommandLine -notmatch 'scripts[\\/]android[\\/]fixture\.mjs') {
        throw '实例控制响应与 Node 进程身份不匹配。'
    }
    $startTicks = $match[0].ProcessStartTicks
    $null = Invoke-RestMethod "$($lifecycle.origin)/$Action" -Method Post -Headers $headers -TimeoutSec 5
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    do {
        $remaining = Get-Process -Id $state.nodePid -ErrorAction SilentlyContinue
        $sameProcess = $remaining -and $remaining.StartTime.ToUniversalTime().Ticks.ToString() -ceq $startTicks
        if (-not $sameProcess) { break }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    if ($sameProcess) { throw '实例停止超时。' }
    if ($Action -eq 'dispose' -and (Test-Path -LiteralPath (Join-Path $FixtureRoot 'config.json'))) {
        throw '实例退出但清理失败；查看 fixture-error.log。'
    }
    return
}
if ($Action -eq 'stop-legacy') {
    if (-not $NodePid -or -not $ProcessStartTicks) { throw '旧实例需要 -NodePid 和 -ProcessStartTicks。' }
    $node = Get-Process -Id $NodePid -ErrorAction Stop
    $match = @($processes | Where-Object { $_.ProcessId -eq $NodePid -and $_.Name -eq 'node.exe' })
    if ($match.Count -ne 1 -or
        $node.StartTime.ToUniversalTime().Ticks.ToString() -cne $ProcessStartTicks -or
        $match[0].CommandLine -notmatch 'scripts[\\/]android[\\/]fixture\.mjs') {
        throw 'PID、启动时间与指定实例命令不匹配。'
    }
    $children = @($processes | Where-Object {
        $_.Name -eq 'moerise.exe' -and $_.ParentProcessId -eq $NodePid -and
            $_.ExecutablePath -eq (Join-Path $FixtureRoot 'moerise.exe')
    } | ForEach-Object {
        $child = Get-Process -Id $_.ProcessId -ErrorAction Stop
        if ($child.StartTime.ToUniversalTime().Ticks.ToString() -cne $_.ProcessStartTicks) {
            throw 'Core PID 在停止前被复用。'
        }
        [pscustomobject] @{ id = $child.Id; ticks = $_.ProcessStartTicks }
    })
    $node | Stop-Process -ErrorAction Stop
    foreach ($child in $children) {
        $process = Get-Process -Id $child.id -ErrorAction SilentlyContinue
        if ($process -and $process.StartTime.ToUniversalTime().Ticks.ToString() -ceq $child.ticks) {
            $process | Stop-Process -ErrorAction Stop
        }
    }
    return
}
if ($Action -eq 'stop' -and -not $processes.Count) { return }
if ($Action -ne 'dispose' -or $processes.Count) { throw '指定实例不可控；旧进程先使用 stop-legacy。' }
# 已停止实例的显式销毁只涉及该实例记录的容器及目录。
$config = Get-Content -LiteralPath (Join-Path $FixtureRoot 'config.json') -Raw | ConvertFrom-Json
$name = [string] $config.memos.instance_id
if ($name -notmatch '^moerise-android-[a-f0-9]{12}$') { throw '隔离容器名称无效。' }
$connection = Get-Content -LiteralPath (Join-Path $FixtureRoot 'connection.json') -Raw | ConvertFrom-Json
$containers = @($name)
if ($connection.PSObject.Properties.Name -contains 'layoutContainer') {
    if ($connection.layoutContainer -cne "$name-layout") { throw '布局容器身份不匹配。' }
    $containers += "$name-layout"
}
foreach ($container in $containers) {
    & docker rm -fv $container | Out-Null
    if ($LASTEXITCODE -ne 0) { throw '指定隔离容器清理失败。' }
}
$resolvedRoot = (Resolve-Path -LiteralPath $FixtureRoot).Path
$null = Resolve-MoeriseAndroidOutputRoot $resolvedRoot
Remove-Item -LiteralPath $resolvedRoot -Recurse -Force
