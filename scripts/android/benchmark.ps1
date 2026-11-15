#requires -Version 7.2
<#
.SYNOPSIS
在 Windows 采集 Android Baseline Profile 或运行真机性能测量。
.EXAMPLE
pwsh -NoProfile -File scripts/android/benchmark.ps1 profile
.EXAMPLE
pwsh -NoProfile -File scripts/android/benchmark.ps1 startup -ValidateOnly
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet('profile', 'run', 'startup', 'startup-without-profile', 'stars')]
    [string] $Mode = 'run',
    [string] $Serial = $env:ANDROID_SERIAL,
    [string] $Fixture = $env:MOERISE_ANDROID_FIXTURE,
    [string] $SourceRoot = 'C:\Projects\Moerise',
    [switch] $ValidateOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
. (Join-Path $PSScriptRoot 'device-lock.ps1')
$repoRoot = Resolve-MoeriseAndroidSourceRoot $SourceRoot

function Resolve-Tool([string] $Override, [string[]] $Candidates, [string] $Hint) {
    $choices = if ($Override) { @($Override) } else { $Candidates }
    foreach ($choice in $choices) {
        if ([string]::IsNullOrWhiteSpace($choice)) { continue }
        $command = Get-Command $choice -CommandType Application -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if ($command -and $command.Source -notmatch '[\\/]Microsoft[\\/]WindowsApps[\\/]') {
            return $command.Source
        }
    }
    throw "找不到工具。$Hint"
}

function Invoke-Text([string] $Tool, [string[]] $Arguments) {
    $text = & $Tool @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        $details = ($text | ForEach-Object { "$_" }) -join "`n"
        throw "$([IO.Path]::GetFileName($Tool)) 执行失败，退出码 $LASTEXITCODE。`n$details"
    }
    return ($text | ForEach-Object { "$_" }) -join "`n"
}

function Invoke-Logged([string] $Tool, [string[]] $Arguments, [string] $Log) {
    & $Tool @Arguments 2>&1 | Tee-Object -FilePath $Log -Append |
        ForEach-Object { Write-Host "$_" }
    if ($LASTEXITCODE -ne 0) {
        throw "$([IO.Path]::GetFileName($Tool)) 执行失败，退出码 $LASTEXITCODE；日志：$Log"
    }
}

function Invoke-Adb([Parameter(ValueFromRemainingArguments = $true)][string[]] $Command) {
    Invoke-Text $adb (@('-s', $Serial) + $Command)
}

function Write-Json([string] $Path, $Value) {
    [IO.File]::WriteAllText($Path, ($Value | ConvertTo-Json -Depth 12) + "`n",
        [Text.UTF8Encoding]::new($false))
}

function Get-ResultDirectories {
    if (Test-Path -LiteralPath $outputsRoot) {
        Get-ChildItem -LiteralPath $outputsRoot -Directory | Where-Object {
            $_.Name.StartsWith('connected_android_test') -or $_.Name -eq 'androidTest-results'
        } | ForEach-Object { $_.FullName }
    }
    if (Test-Path -LiteralPath $reportsRoot) { $reportsRoot }
}

function Copy-Results {
    foreach ($directory in @(Get-ResultDirectories)) {
        foreach ($file in Get-ChildItem -LiteralPath $directory -File -Recurse) {
            $relative = [IO.Path]::GetRelativePath($benchmarkBuild, $file.FullName)
            $target = Join-Path $output "raw/$relative"
            $null = New-Item -ItemType Directory -Force -Path (Split-Path $target -Parent)
            Copy-Item -LiteralPath $file.FullName -Destination $target
        }
    }
}

if ([string]::IsNullOrWhiteSpace($Serial) -or $Serial -match '\s' -or $Serial.StartsWith('-')) {
    throw '用 -Serial 或 ANDROID_SERIAL 指定 adb devices 列出的单个设备序列号。'
}
if ([string]::IsNullOrWhiteSpace($Fixture) -or -not (Test-Path -LiteralPath $Fixture -PathType Leaf)) {
    throw '用 -Fixture 或 MOERISE_ANDROID_FIXTURE 指定隔离服务的 connection.json。'
}
$Fixture = (Resolve-Path -LiteralPath $Fixture).Path
try {
    $connection = Get-Content -LiteralPath $Fixture -Raw -Encoding utf8 | ConvertFrom-Json
    $origin = [uri] $connection.origin
    if (-not $origin.IsAbsoluteUri -or $origin.Scheme -ne 'http' -or
        $origin.Host -notin @('127.0.0.1', 'localhost', '[::1]', '::1') -or
        $origin.AbsolutePath -ne '/' -or $origin.Query -or $origin.Fragment -or $origin.UserInfo -or
        $connection.username -ne 'android-owner' -or
        [string]::IsNullOrWhiteSpace($connection.password)) {
        throw 'invalid fixture'
    }
} catch {
    throw '连接档案须包含 HTTP 回环来源、android-owner 账号及非空密码，来源只包含协议、主机和端口。'
}
$fixturePort = $origin.Port
$deviceConnection = [pscustomobject] @{
    origin = $connection.origin; username = $connection.username; password = $connection.password
}
Remove-Variable connection

$variant = if ($Mode -eq 'profile') { 'profile' } else { 'benchmark' }
$classPrefix = 'io.github.fermin214.moerise.benchmark.'
$classes = switch ($Mode) {
    'profile' { "${classPrefix}BaselineProfileGenerator" }
    'startup' { "${classPrefix}StartupBenchmark" }
    'startup-without-profile' { "${classPrefix}StartupBenchmark#coldWithoutProfile,${classPrefix}StartupBenchmark#foregroundWithoutProfile" }
    'stars' { "${classPrefix}StarBenchmark" }
    'run' { "${classPrefix}StartupBenchmark,${classPrefix}StarBenchmark" }
}
$profilesRoot = Join-Path $repoRoot 'mobile/androidApp/src/main/baselineProfiles'
if ($variant -eq 'benchmark' -and $Mode -ne 'startup-without-profile') {
    $baseline = Join-Path $profilesRoot 'baseline-prof.txt'
    if (-not (Test-Path -LiteralPath $baseline -PathType Leaf) -or
        [string]::IsNullOrWhiteSpace([IO.File]::ReadAllText($baseline))) {
        throw '先运行 benchmark.ps1 profile 生成设备 Profile。'
    }
}

. (Join-Path $PSScriptRoot 'environment.ps1') -ResolveOnly -SourceRoot $repoRoot
$toolchain = Get-MoeriseAndroidEnvironment -RepositoryRoot $repoRoot
$javaHome = $toolchain.JavaHome
$sdkRoot = $toolchain.AndroidHome
$adb = Resolve-Tool $env:MOERISE_ANDROID_ADB @((Join-Path $sdkRoot 'platform-tools/adb.exe'), 'adb.exe') `
    '安装 SDK Platform-Tools，或设置 MOERISE_ANDROID_ADB。'
$node = Resolve-Tool $env:MOERISE_ANDROID_NODE @('node.exe') '设置 MOERISE_ANDROID_NODE 为 Node.js 24 以上的程序路径。'
$nodeVersion = Invoke-Text $node @('--version')
if ($nodeVersion -notmatch '^v(\d+)\.' -or [int] $Matches[1] -lt 24) {
    throw '数据准备需要 Node.js 24 以上。'
}
$python = $null
$python = Resolve-Tool $env:MOERISE_ANDROID_PYTHON @(
    'C:\Users\Fermin\AppData\Local\hermes\hermes-agent\venv\Scripts\python.exe', 'python.exe', 'python3.exe') `
    '安装 Python 3.11 以上，或设置 MOERISE_ANDROID_PYTHON 为 python.exe 的完整路径。'
$pythonVersion = Invoke-Text $python @('-X', 'utf8', '-c', 'import sys; print("%d.%d" % sys.version_info[:2])')
if ([version] $pythonVersion -lt [version] '3.11') { throw 'APK 检查与汇总需要 Python 3.11 以上。' }
$gradle = Join-Path $repoRoot 'mobile/gradlew.bat'
if (-not (Test-Path -LiteralPath $gradle -PathType Leaf)) { throw '找不到 mobile/gradlew.bat。' }
$taskVariant = if ($variant -eq 'profile') { 'Profile' } else { 'Benchmark' }
$gradleArguments = @('-p', (Join-Path $repoRoot 'mobile'), '--no-daemon', '--max-workers=1',
    '--no-parallel', '--console=plain',
    '-Dorg.gradle.jvmargs=-Xmx1536m -XX:MaxMetaspaceSize=768m -XX:ActiveProcessorCount=1 -Dfile.encoding=UTF-8',
    ":androidApp:assemble${taskVariant}", ":benchmark:assemble${taskVariant}")
if ($toolchain.ProjectCache) {
    $gradleArguments += @('--project-cache-dir', $toolchain.ProjectCache)
}
if ($ValidateOnly) {
    [pscustomobject] @{
        mode = $Mode; serial = $Serial; variant = $variant; fixture_port = $fixturePort
        java_home = $javaHome; android_home = $sdkRoot; adb = $adb; node = $node; python = $python
        gradle_user_home = $toolchain.GradleUserHome; project_cache = $toolchain.ProjectCache
        gradle_arguments = $gradleArguments
    } | ConvertTo-Json -Depth 4
    return
}

# 只设置子进程需要的环境，结束时恢复调用方会话。
$savedEnvironment = @{}
foreach ($name in @('JAVA_HOME', 'ANDROID_HOME', 'ANDROID_SDK_ROOT', 'PATH', 'ANDROID_SERIAL',
    'MOERISE_ANDROID_FIXTURE', 'GRADLE_USER_HOME')) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$queue = Assert-MoeriseDeviceLock $repoRoot
$pushedLocation = $false
try {
    $env:JAVA_HOME = $javaHome
    $env:ANDROID_HOME = $sdkRoot
    $env:ANDROID_SDK_ROOT = $sdkRoot
    $env:PATH = "$javaHome\bin;$sdkRoot\platform-tools;$env:PATH"
    $env:ANDROID_SERIAL = $Serial
    $env:MOERISE_ANDROID_FIXTURE = $Fixture
    $env:GRADLE_USER_HOME = $toolchain.GradleUserHome
    Push-Location -LiteralPath $repoRoot
    $pushedLocation = $true
    $commit = Invoke-Text 'git.exe' @('rev-parse', 'HEAD')
    $dirty = -not [string]::IsNullOrWhiteSpace((Invoke-Text 'git.exe' @('status', '--porcelain')))
    $output = Join-Path $queue.output_root "benchmark-$Mode"
    $null = New-Item -ItemType Directory -Path $output
    $benchmarkBuild = Join-Path $repoRoot 'mobile/benchmark/build'
    $outputsRoot = Join-Path $benchmarkBuild 'outputs'
    $reportsRoot = Join-Path $benchmarkBuild 'reports/androidTests'
    $metadata = [ordered] @{
        commit = $commit; dirty = $dirty; mode = $Mode; variant = $variant; serial = $Serial
        started_at = [DateTime]::UtcNow.ToString('o'); status = 'running'
        fixture_notes = 100
        cached_notes = $(if ($Mode -eq 'stars') { 0 } else { 20 })
        cached_long_note_bytes = $(if ($Mode -eq 'stars') { 0 } else { 65536 })
        cached_long_note_attachments = $(if ($Mode -eq 'stars') { 0 } else { 10 })
        network = 'adb reverse to isolated Core/Memos'; fixture_port = $fixturePort
        gradle_user_home = $toolchain.GradleUserHome; project_cache = $toolchain.ProjectCache
        java_home = $javaHome; android_home = $sdkRoot; gradle_succeeded = $false; execution = 'adb'
        profile_mode = $(if ($Mode -eq 'startup-without-profile') { 'without' } else { 'compare' })
    }
    $credentialCleanupRequired = $false
    $deviceCredentialPath = $null
    $reverseAdded = $false
    $buildStarted = $false
    $archived = $false
    $failure = $null
    Write-Json (Join-Path $output 'device.json') $metadata
    try {
        if ((Invoke-Adb @('get-state')).Trim() -ne 'device') { throw '指定设备的 ADB 状态不是 device。' }
        $allDevices = Invoke-Text $adb @('devices')
        if ($Serial.StartsWith('emulator-') -or $allDevices -match '(?m)^emulator-\S+\s+') {
            throw '性能测量只使用真机；先关闭所有模拟器。'
        }
        foreach ($property in @{
            model = 'ro.product.model'; android = 'ro.build.version.release'
            api = 'ro.build.version.sdk'; abi = 'ro.product.cpu.abi'
        }.GetEnumerator()) {
            $metadata[$property.Key] = (Invoke-Adb @('shell', 'getprop', $property.Value)).Trim()
        }
        $minimumApi = if ($Mode -eq 'profile') { 33 } else { 28 }
        if ([int] $metadata.api -lt $minimumApi) { throw "此模式要求 Android API $minimumApi 以上。" }
        $metadata.page_size = (Invoke-Adb @('shell', 'getconf', 'PAGESIZE')).Trim()
        $metadata.battery_before = Invoke-Adb @('shell', 'dumpsys', 'battery')
        $metadata.thermal_before = Invoke-Adb @('shell', 'dumpsys', 'thermalservice')
        Invoke-Logged $node @('scripts/android/seed-benchmark.mjs') (Join-Path $output 'seed.log')
        $reverses = Invoke-Adb @('reverse', '--list')
        $reverseAdded = $reverses -notmatch "(?m)^\S+\s+tcp:$fixturePort\s+tcp:$fixturePort$"
        $null = Invoke-Adb @('reverse', "tcp:$fixturePort", "tcp:$fixturePort")
        # 未安装性能包时无需清空；已安装时必须成功清空。
        $installed = Invoke-Adb @('shell', 'pm', 'list', 'packages', 'io.github.fermin214.moerise.benchmark')
        if ($installed.Trim() -split '\r?\n' -contains 'package:io.github.fermin214.moerise.benchmark') {
            $cleared = Invoke-Adb @('shell', 'pm', 'clear', 'io.github.fermin214.moerise.benchmark')
            if ($cleared.Trim() -ne 'Success') { throw '无法清空性能包数据。' }
        }
        $credentialCleanupRequired = $true
        $deviceCredentialPath = Join-Path (Split-Path $Fixture -Parent) "device-connection-$([Guid]::NewGuid().ToString('N')).json"
        $deviceConnection | ConvertTo-Json | Set-Content -LiteralPath $deviceCredentialPath -Encoding utf8
        $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
        & icacls $deviceCredentialPath /inheritance:r /grant:r "${identity}:F" 'SYSTEM:F' | Out-Null
        if ($LASTEXITCODE -ne 0) { throw '设备连接档案权限设置失败。' }
        Remove-Variable deviceConnection
        $null = Invoke-Adb @('push', $deviceCredentialPath, '/data/local/tmp/moerise-android-fixture.json')
        foreach ($directory in @(Get-ResultDirectories)) {
            $resolved = (Resolve-Path -LiteralPath $directory).Path
            if (-not $resolved.StartsWith("$benchmarkBuild\", [StringComparison]::OrdinalIgnoreCase) -or
                (Get-Item -LiteralPath $resolved).Attributes.HasFlag([IO.FileAttributes]::ReparsePoint)) {
                throw '性能输出清理路径必须位于 mobile/benchmark/build 内，且不能是目录链接。'
            }
            Remove-Item -LiteralPath $resolved -Recurse -Force
        }
        $buildStarted = $true
        Invoke-Logged $gradle $gradleArguments (Join-Path $output 'run.log')
        $metadata.gradle_succeeded = $true
        Invoke-Logged $python @('-X', 'utf8', 'scripts/android/benchmark-device.py',
            '--serial', $Serial, '--adb', $adb, '--variant', $variant,
            '--classes', $classes, '--output', $output) (Join-Path $output 'device-run.log')
        Copy-Results
        $archived = $true
        $apk = Join-Path $repoRoot "mobile/androidApp/build/outputs/apk/$variant/androidApp-$variant.apk"
        Copy-Item -LiteralPath $apk -Destination (Join-Path $output 'target.apk')
        $metadata.apk_sha256 = (Get-FileHash -LiteralPath $apk -Algorithm SHA256).Hash.ToLowerInvariant()
        $runnerDirectory = Join-Path $benchmarkBuild "outputs/apk/$variant"
        $runnerMetadata = Get-Content -LiteralPath (Join-Path $runnerDirectory 'output-metadata.json') -Raw -Encoding utf8 | ConvertFrom-Json
        $runnerElements = @($runnerMetadata.elements)
        if ($runnerElements.Count -ne 1) { throw '性能 runner 必须输出单个 APK。' }
        $runnerApk = [IO.Path]::GetFullPath((Join-Path $runnerDirectory $runnerElements[0].outputFile))
        if ((Split-Path $runnerApk -Parent) -ne $runnerDirectory -or
            [IO.Path]::GetExtension($runnerApk) -ne '.apk') { throw '性能 runner APK 路径无效。' }
        Copy-Item -LiteralPath $runnerApk -Destination (Join-Path $output 'runner.apk')
        $metadata.runner_apk_sha256 = (Get-FileHash -LiteralPath $runnerApk -Algorithm SHA256).Hash.ToLowerInvariant()
        $verifyArguments = @('-X', 'utf8', 'scripts/android/verify-apk.py', (Join-Path $output 'target.apk'),
            '--release', '--package', 'io.github.fermin214.moerise.benchmark')
        if ($variant -eq 'benchmark' -and $Mode -ne 'startup-without-profile') { $verifyArguments += '--require-profile' }
        Invoke-Logged $python $verifyArguments (Join-Path $output 'target-verification.txt')
        Invoke-Logged $python @('-X', 'utf8', 'scripts/android/verify-apk.py', (Join-Path $output 'runner.apk'),
            '--package', 'io.github.fermin214.moerise.benchmark.runner', '--same-signer-as', (Join-Path $output 'target.apk')) `
            (Join-Path $output 'runner-verification.txt')
        if ($variant -eq 'profile') {
            $rulesByKind = @{}
            foreach ($kind in @('baseline', 'startup')) {
                $rules = @(Get-ChildItem -LiteralPath (Join-Path $output 'raw') -File -Recurse |
                    Where-Object { $_.Name.EndsWith("-$kind-prof.txt", [StringComparison]::Ordinal) -or
                        ($kind -eq 'baseline' -and $_.Name.EndsWith('-startup-prof.txt', [StringComparison]::Ordinal)) } |
                    ForEach-Object { [IO.File]::ReadAllLines($_.FullName) } |
                    Where-Object { -not [string]::IsNullOrWhiteSpace($_) } |
                    Sort-Object -CaseSensitive -Unique)
                if (-not $rules.Count) { throw "没有设备生成的非空 $kind Profile。" }
                $rulesByKind[$kind] = $rules
            }
            $null = New-Item -ItemType Directory -Force -Path $profilesRoot
            foreach ($kind in @('baseline', 'startup')) {
                $target = Join-Path $profilesRoot "$kind-prof.txt"
                [IO.File]::WriteAllText($target, ($rulesByKind[$kind] -join "`n") + "`n",
                    [Text.UTF8Encoding]::new($false))
                Write-Host "$target : $($rulesByKind[$kind].Count) 条设备规则"
            }
        } else {
            Write-Json (Join-Path $output 'device.json') $metadata
            $reportMode = if ($Mode -eq 'startup-without-profile') { 'startup' } else { $Mode }
            Invoke-Logged $python @('-X', 'utf8', 'scripts/android/benchmark-report.py', $output, '--mode', $reportMode) (Join-Path $output 'report.log')
        }
    } catch {
        $failure = $_
    } finally {
        # 凭据清理先于归档，避免结果文件异常阻断设备清理。
        if ($credentialCleanupRequired) {
            try {
                $null = Invoke-Adb @('shell', 'rm', '-f', '/data/local/tmp/moerise-android-fixture.json')
                $metadata.fixture_removed = $true
            } catch {
                $metadata.fixture_removed = $false
                if (-not $failure) { $failure = $_ }
                Write-Warning '设备连接档案清理失败；恢复连接后删除 /data/local/tmp/moerise-android-fixture.json。'
            }
        }
        if ($deviceCredentialPath -and (Test-Path -LiteralPath $deviceCredentialPath)) {
            try { Remove-Item -LiteralPath $deviceCredentialPath -Force }
            catch { if (-not $failure) { $failure = $_ } }
        }
        if ($reverseAdded) {
            try { $null = Invoke-Adb @('reverse', '--remove', "tcp:$fixturePort") }
            catch { if (-not $failure) { $failure = $_ } }
        }
        if ($buildStarted -and -not $archived) {
            try { Copy-Results } catch {
                if (-not $failure) { $failure = $_ }
                Write-Warning '原始结果归档失败。'
            }
        }
        foreach ($entry in @{
            battery = @('shell', 'dumpsys', 'battery')
            thermal = @('shell', 'dumpsys', 'thermalservice')
            memory = @('shell', 'dumpsys', 'meminfo', 'io.github.fermin214.moerise.benchmark')
        }.GetEnumerator()) {
            if (-not $metadata.Contains('api')) { break }
            try { $metadata[$entry.Key] = Invoke-Adb $entry.Value } catch {
                $metadata[$entry.Key] = $null
            }
        }
        $metadata.finished_at = [DateTime]::UtcNow.ToString('o')
        $metadata.status = if ($failure) { 'failed' } else { 'succeeded' }
        if ($failure) { $metadata.failure = $failure.Exception.Message }
        Write-Json (Join-Path $output 'device.json') $metadata
    }
    if ($failure) { throw $failure }
    Write-Host $output
} finally {
    if ($pushedLocation) { Pop-Location }
    foreach ($name in $savedEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], 'Process')
    }
}
