#requires -Version 7.2
# Windows 的所有 worktree 共用同一文件句柄；文件内容仅用于诊断。
$script:MoeriseIntegrationRoot = 'C:\Projects\Moerise'
$script:MoeriseDeviceLockPath = 'C:\Projects\Moerise\.local\android\device-build.lock'
. (Join-Path $PSScriptRoot 'windows-process.ps1')

function Resolve-MoeriseAndroidSourceRoot([string] $SourceRoot = $script:MoeriseIntegrationRoot) {
    $resolved = (Resolve-Path -LiteralPath $SourceRoot -ErrorAction Stop).Path.TrimEnd('\', '/')
    if (-not $resolved.Equals($script:MoeriseIntegrationRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Android 构建和设备操作的 SourceRoot 必须为 C:\Projects\Moerise。'
    }
    return $resolved
}

function Get-MoeriseDeviceLock {
    if (-not (Test-Path -LiteralPath $script:MoeriseDeviceLockPath -PathType Leaf)) { return $null }
    $stream = [IO.File]::Open($script:MoeriseDeviceLockPath, 'Open', 'Read', 'ReadWrite')
    try {
        $reader = [IO.StreamReader]::new($stream, [Text.Encoding]::UTF8, $false, 1024, $true)
        try { $text = $reader.ReadToEnd() } finally { $reader.Dispose() }
        if ($text) {
            try { return $text | ConvertFrom-Json -ErrorAction Stop }
            catch { return $null }
        }
    } finally { $stream.Dispose() }
}

function Test-MoeriseDeviceLockBusy {
    if (-not (Test-Path -LiteralPath $script:MoeriseDeviceLockPath -PathType Leaf)) { return $false }
    try {
        $probe = [IO.File]::Open($script:MoeriseDeviceLockPath, 'Open', 'ReadWrite', 'Read')
        $probe.Dispose()
        return $false
    } catch [IO.IOException] {
        if (($_.Exception.HResult -band 0xffff) -in @(32, 33)) { return $true }
        throw
    }
}

function Get-MoeriseAndroidBuildProcesses {
    @(Get-MoeriseWindowsProcesses @('java.exe') | Where-Object {
        -not $_.CommandLine -or
        ($_.CommandLine -match 'GradleWrapperMain|GradleDaemon|org\.gradle\.launcher\.GradleMain' -and
        ($_.CommandLine -match 'C:[\\/]Projects[\\/]Moerise[\\/]' -or
            $_.CommandLine -match '[\\/]worktrees[\\/][^\\/]+[\\/]Moerise[\\/]mobile[\\/]'))
    } | Select-Object ProcessId, ParentProcessId, Name, ProcessStartTicks)
}

function Assert-MoeriseDeviceLock([string] $SourceRoot = $script:MoeriseIntegrationRoot) {
    $resolved = Resolve-MoeriseAndroidSourceRoot $SourceRoot
    $record = Get-MoeriseDeviceLock
    if (-not $record -or -not $env:MOERISE_ANDROID_LOCK_TOKEN -or
        $record.token -cne $env:MOERISE_ANDROID_LOCK_TOKEN -or
        $record.source_root -ne $resolved -or -not (Test-MoeriseDeviceLockBusy)) {
        throw '需要通过 with-device-lock.ps1 的主仓库队列执行。'
    }
    $ownerProcess = Get-Process -Id $record.pid -ErrorAction SilentlyContinue
    if (-not $ownerProcess -or
        $ownerProcess.StartTime.ToUniversalTime().Ticks.ToString() -cne $record.process_start_ticks) {
        throw '设备锁持有进程已退出或 PID 被复用。'
    }
    # 令牌只能由持锁进程及其子进程继承，其他终端复制环境变量无效。
    $ancestor = $PID
    $visited = [Collections.Generic.HashSet[int]]::new()
    $processTree = @{}
    foreach ($entry in [MoeriseAndroid.WindowsProcesses]::Snapshot()) {
        $processTree[$entry.ProcessId] = $entry.ParentProcessId
    }
    while ($ancestor -ne [int] $record.pid -and $ancestor -gt 0 -and $visited.Add($ancestor)) {
        if (-not $processTree.ContainsKey($ancestor)) { break }
        $ancestor = [int] $processTree[$ancestor]
    }
    if ($ancestor -ne [int] $record.pid) { throw '调用进程不属于设备锁持有者的进程树。' }
    return $record
}

function Resolve-MoeriseAndroidOutputRoot([string] $OutputRoot) {
    $resolved = [IO.Path]::GetFullPath($OutputRoot)
    $parent = "$script:MoeriseIntegrationRoot\.local\android\"
    if (-not $resolved.StartsWith($parent, [StringComparison]::OrdinalIgnoreCase)) {
        throw '结果目录必须位于 C:\Projects\Moerise\.local\android\ 下。'
    }
    # 禁止目录联接把结果或清理操作引向其他目录。
    $cursor = $resolved
    while ($cursor -and $cursor.Length -ge $script:MoeriseIntegrationRoot.Length) {
        if ((Test-Path -LiteralPath $cursor) -and
            (Get-Item -LiteralPath $cursor).Attributes.HasFlag([IO.FileAttributes]::ReparsePoint)) {
            throw '结果目录不能包含目录链接。'
        }
        $cursor = Split-Path $cursor -Parent
    }
    return $resolved
}
