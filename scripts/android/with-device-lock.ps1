#requires -Version 7.2
[CmdletBinding()]
param(
    [string] $Owner,
    [string] $Task = 'device',
    [string] $SourceRoot = 'C:\Projects\Moerise',
    [string] $OutputRoot,
    [scriptblock] $Command,
    [switch] $Status
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'device-lock.ps1')
if ($Status) {
    $record = Get-MoeriseDeviceLock
    $busy = Test-MoeriseDeviceLockBusy
    [pscustomobject] @{ busy = $busy; lock_path = $script:MoeriseDeviceLockPath
        build_processes = @(Get-MoeriseAndroidBuildProcesses)
        holder = if ($record) {
            $record | Select-Object owner, task, pid, process_start_ticks, source_root, branch, commit, dirty, output_root, started_at
        } else { $null }
    } | ConvertTo-Json -Depth 5
    return
}
if ([string]::IsNullOrWhiteSpace($Owner) -or -not $Command -or $Task -notmatch '^[A-Za-z0-9_-]+$') {
    throw '传入 -Owner、-Command 和只包含字母数字、下划线或连字符的 -Task。'
}
$SourceRoot = Resolve-MoeriseAndroidSourceRoot $SourceRoot
if (-not $OutputRoot) {
    $stamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ')
    $OutputRoot = Join-Path $SourceRoot ".local/android/$Task-$stamp-$([Guid]::NewGuid().ToString('N').Substring(0, 8))"
}
$OutputRoot = Resolve-MoeriseAndroidOutputRoot $OutputRoot
$null = New-Item -ItemType Directory -Force -Path (Split-Path $script:MoeriseDeviceLockPath -Parent)
$lock = $null
try {
    $lock = [IO.File]::Open($script:MoeriseDeviceLockPath, 'OpenOrCreate', 'ReadWrite', 'Read')
} catch [IO.IOException] {
    $holder = Get-MoeriseDeviceLock
    throw "Android 队列忙：owner=$($holder.owner)，task=$($holder.task)，pid=$($holder.pid)，source=$($holder.source_root)。"
}
$savedEnvironment = @{}
foreach ($name in @('MOERISE_ANDROID_LOCK_TOKEN', 'MOERISE_ANDROID_SOURCE_ROOT', 'MOERISE_ANDROID_OUTPUT_ROOT')) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
try {
    $buildProcesses = @(Get-MoeriseAndroidBuildProcesses)
    if ($buildProcesses.Count) {
        throw "存在 Android Gradle 进程：$($buildProcesses.ProcessId -join ', ')；先检查这些进程的生命周期。"
    }
    if (Test-Path -LiteralPath $OutputRoot) { throw '结果目录已存在；每次任务使用独立目录。' }
    $commit = & git -C $SourceRoot rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw '无法读取源码提交号。' }
    $branch = & git -C $SourceRoot branch --show-current
    if ($LASTEXITCODE -ne 0) { throw '无法读取源码分支。' }
    $changes = & git -C $SourceRoot status --porcelain --untracked-files=no
    if ($LASTEXITCODE -ne 0) { throw '无法读取源码差异。' }
    $record = [ordered] @{
        owner = $Owner; task = $Task; pid = $PID
        process_start_ticks = (Get-Process -Id $PID).StartTime.ToUniversalTime().Ticks.ToString()
        source_root = $SourceRoot; branch = "$branch"; commit = "$commit"
        dirty = -not [string]::IsNullOrWhiteSpace(($changes -join "`n")); output_root = $OutputRoot
        started_at = [DateTime]::UtcNow.ToString('o'); token = [Guid]::NewGuid().ToString('N')
    }
    $bytes = [Text.UTF8Encoding]::new($false).GetBytes(($record | ConvertTo-Json) + "`n")
    $lock.SetLength(0); $lock.Write($bytes, 0, $bytes.Length); $lock.Flush($true)
    $null = New-Item -ItemType Directory -Path $OutputRoot
    ([pscustomobject] $record) | Select-Object owner, task, pid, process_start_ticks, source_root, branch, commit, dirty, output_root, started_at |
        ConvertTo-Json | Set-Content -LiteralPath (Join-Path $OutputRoot 'queue.json') -Encoding utf8
    $env:MOERISE_ANDROID_LOCK_TOKEN = $record.token
    $env:MOERISE_ANDROID_SOURCE_ROOT = $SourceRoot
    $env:MOERISE_ANDROID_OUTPUT_ROOT = $OutputRoot
    & $Command ([pscustomobject] $record)
} finally {
    $lock.Dispose()
    foreach ($name in $savedEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], 'Process')
    }
}
