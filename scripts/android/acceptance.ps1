#requires -Version 7.2
[CmdletBinding()]
param(
    [string] $SourceRoot = 'C:\Projects\Moerise',
    [string] $Serial = $env:ANDROID_SERIAL,
    [string] $Fixture = $env:MOERISE_ANDROID_FIXTURE,
    [string] $BuildManifest,
    [string] $Group = $(if ($env:MOERISE_ANDROID_SUITE) { $env:MOERISE_ANDROID_SUITE } else { 'all' }),
    [ValidatePattern('^(QuickNoteTest|ImeLifecycleTest|StarRenderingTest)(#[A-Za-z][A-Za-z0-9_]*)?$')]
    [string[]] $TestClass = @(),
    [switch] $SkipBuild,
    [switch] $Recovery,
    [switch] $VisualSetup,
    [switch] $ValidateOnly
)
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
if ($Serial -notmatch '^emulator-[0-9]+$') {
    throw '此入口只接受显式 emulator 序列号；真机使用 scripts/android/phone.ps1 prepare/run 的持久会话。'
}
if (-not $ValidateOnly -and $env:GITHUB_ACTIONS -ne 'true') {
    throw '安装和清空数据只用于 GitHub Actions 的隔离模拟器；真机使用 scripts/android/phone.ps1。'
}
if ($TestClass.Count -gt 0) {
    if ($PSBoundParameters.ContainsKey('Group') -and $Group -ne 'diagnostic') {
        throw '-TestClass 仅用于 -Group diagnostic，不能与业务组叠加。'
    }
    $Group = 'diagnostic'
}
if ($Recovery) {
    if ($TestClass.Count -gt 0 -or ($PSBoundParameters.ContainsKey('Group') -and $Group -ne 'recovery')) {
        throw '-Recovery 单独选择恢复专项；多个分组使用 -Group all。'
    }
    $Group = 'recovery'
}
if ($Group -eq 'diagnostic' -and $TestClass.Count -eq 0) { throw 'diagnostic 需要显式 -TestClass。' }
if ($VisualSetup -and $Group -ne 'diagnostic') { throw '-VisualSetup 仅用于显式诊断；recovery 自动准备登录。' }
$catalog = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'maestro/catalog.json') -Raw | ConvertFrom-Json
$uiGroups = @($catalog.groups.PSObject.Properties.Name)
if ($Group -notin ($uiGroups + @('all', 'ui', 'lifecycle', 'ime', 'recovery', 'stars', 'diagnostic'))) {
    throw "未知设备检查组：$Group。"
}
$groups = if ($Group -eq 'all') {
    $uiGroups + @('lifecycle', 'ime', 'recovery', 'stars')
} elseif ($Group -eq 'ui') { $uiGroups } else { @($Group) }
$needsTests = @($groups | Where-Object { $_ -notin $uiGroups }).Count -gt 0
. (Join-Path $PSScriptRoot 'environment.ps1') -ResolveOnly -SourceRoot $SourceRoot
$toolchain = Get-MoeriseAndroidEnvironment -RepositoryRoot $androidRepoRoot
$adb = Join-Path $toolchain.AndroidHome 'platform-tools/adb.exe'
if (-not $Fixture -or -not (Test-Path -LiteralPath $Fixture -PathType Leaf)) {
    throw '传入 -Fixture 连接档案。'
}
$fixturePath = (Resolve-Path -LiteralPath $Fixture).Path
try {
    $connection = Get-Content -LiteralPath $fixturePath -Raw | ConvertFrom-Json
    $origin = [Uri] $connection.origin
    if (-not $origin.IsAbsoluteUri -or $origin.Scheme -ne 'http' -or -not $origin.IsLoopback -or
        $origin.AbsolutePath -ne '/' -or $origin.Query -or $origin.Fragment -or $origin.UserInfo -or
        $connection.username -ne 'android-owner' -or -not $connection.password) { throw 'invalid fixture' }
    $deviceConnection = [pscustomobject] @{
        origin = $connection.origin; username = $connection.username; password = $connection.password
    }
} catch { throw '设备流程需要 HTTP 回环来源和 android-owner 隔离账号。' }
Remove-Variable connection
if ($ValidateOnly) {
    [pscustomobject] @{ source_root = $androidRepoRoot; serial = $Serial; fixture_port = $origin.Port
        groups = $groups; test_apk = $needsTests; ui_runner = 'scripts/android/maestro-run.py'
        skip_build = [bool] $SkipBuild; recovery = [bool] $Recovery; visual_setup = [bool] $VisualSetup
        gradle_home = $toolchain.GradleUserHome } | ConvertTo-Json
    return
}
$record = Assert-MoeriseDeviceLock $androidRepoRoot
function Invoke-Adb([string[]] $Arguments, [int] $TimeoutSeconds = 120, [string] $Log) {
    $info = [Diagnostics.ProcessStartInfo]::new($adb)
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    foreach ($argument in (@('-s', $Serial) + $Arguments)) { $info.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $info
    try {
        $null = $process.Start()
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        $completed = $process.WaitForExit($TimeoutSeconds * 1000)
        if (-not $completed) {
            $process.Kill()
            $process.WaitForExit()
        }
        $text = ($stdout.GetAwaiter().GetResult(), $stderr.GetAwaiter().GetResult() |
            Where-Object { $_ }) -join "`n"
        if ($Log) { $text | Set-Content -LiteralPath $Log -Encoding utf8 }
        if (-not $completed) { throw "ADB 操作超时（$TimeoutSeconds 秒）：$($Arguments[0])。`n$text" }
        if ($process.ExitCode -ne 0) { throw "ADB 操作失败，退出码 $($process.ExitCode)：$($Arguments[0])。`n$text" }
        return $text
    } finally { $process.Dispose() }
}
if ((Invoke-Adb @('get-state')).Trim() -ne 'device') { throw 'ADB 设备不可用。' }
if (-not $SkipBuild) {
    & (Join-Path $PSScriptRoot 'build.ps1') -SourceRoot $androidRepoRoot `
        -Tasks @(':androidApp:assembleFixture', ':androidApp:assembleFixtureAndroidTest')
}
if (-not $BuildManifest) { $BuildManifest = Join-Path $record.output_root 'build-artifacts.json' }
if (-not (Test-Path -LiteralPath $BuildManifest -PathType Leaf)) {
    throw '缺少 build-artifacts.json；先运行 build.ps1，或用 -BuildManifest 指定相同提交的构建清单。'
}
$manifest = Get-Content -LiteralPath $BuildManifest -Raw | ConvertFrom-Json
if ($manifest.source_root -ne $androidRepoRoot -or $manifest.commit -ne $record.commit) {
    throw '构建清单的源码目录或提交号不匹配。'
}
$expectedPaths = @('mobile/androidApp/build/outputs/apk/fixture/androidApp-fixture.apk')
if ($needsTests) { $expectedPaths += 'mobile/androidApp/build/outputs/apk/androidTest/fixture/androidApp-fixture-androidTest.apk' }
foreach ($relative in $expectedPaths) {
    $artifact = @($manifest.artifacts | Where-Object { $_.path -ceq $relative })
    if ($artifact.Count -ne 1 -or $artifact[0].sha256 -cne
        (Get-FileHash -LiteralPath (Join-Path $androidRepoRoot $relative) -Algorithm SHA256).Hash.ToLowerInvariant()) {
        throw "APK 与构建清单不匹配：$relative。"
    }
}
$reverseAdded = $false
$credentialPushed = $false
$deviceCredentialPath = $null
$failure = $null
$python = if ($env:MOERISE_ANDROID_PYTHON) { $env:MOERISE_ANDROID_PYTHON } else {
    'C:\Users\Fermin\AppData\Local\hermes\hermes-agent\venv\Scripts\python.exe'
}
$results = [Collections.Generic.List[string]]::new()
$results.Add("group`tstatus`tseconds")
$failedGroups = [Collections.Generic.List[string]]::new()
function Invoke-Instrumentation([string] $Classes, [string] $Log, [bool] $Visual = $false) {
    $null = Invoke-Adb @('shell', 'am', 'force-stop', 'io.github.fermin214.moerise.fixture')
    $arguments = @('shell', 'am', 'instrument', '-w', '-e', 'class', $Classes,
        '-e', 'timeout_msec', '300000', '-e', 'visualSetup', $Visual.ToString().ToLowerInvariant(),
        'io.github.fermin214.moerise.fixture.test/androidx.test.runner.AndroidJUnitRunner')
    $result = Invoke-Adb $arguments -TimeoutSeconds 600 -Log $Log
    if ($result -notmatch '(?m)^OK \([1-9][0-9]* tests?\)' -or $result -match 'FAILURES!!!|INSTRUMENTATION_FAILED') {
        throw "设备自动化失败；查看 $Log。"
    }
}
try {
    $reverses = Invoke-Adb @('reverse', '--list')
    $reverseAdded = $reverses -notmatch "(?m)^\S+\s+tcp:$($origin.Port)\s+tcp:$($origin.Port)$"
    $null = Invoke-Adb @('reverse', "tcp:$($origin.Port)", "tcp:$($origin.Port)")
    foreach ($relative in $expectedPaths) {
        $apk = Join-Path $androidRepoRoot $relative
        $null = Invoke-Adb @('install', '-r', '-t', $apk) `
            -Log (Join-Path $record.output_root "$([IO.Path]::GetFileNameWithoutExtension($apk))-install.log")
        "$((Get-FileHash -LiteralPath $apk -Algorithm SHA256).Hash.ToLowerInvariant())  $relative" |
            Add-Content -LiteralPath (Join-Path $record.output_root 'apks.sha256')
    }
    if ((Invoke-Adb @('shell', 'pm', 'clear', 'io.github.fermin214.moerise.fixture')).Trim() -ne 'Success') {
        throw '测试包数据清理失败。'
    }
    if ($needsTests) {
        $deviceCredentialPath = Join-Path (Split-Path $fixturePath -Parent) "device-connection-$([Guid]::NewGuid().ToString('N')).json"
        $deviceConnection | ConvertTo-Json | Set-Content -LiteralPath $deviceCredentialPath -Encoding utf8
        $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
        & icacls $deviceCredentialPath /inheritance:r /grant:r "${identity}:F" 'SYSTEM:F' | Out-Null
        if ($LASTEXITCODE -ne 0) { throw '设备连接档案权限设置失败。' }
    }
    Remove-Variable deviceConnection
    foreach ($item in $groups) {
        $watch = [Diagnostics.Stopwatch]::StartNew()
        $status = 'passed'
        try {
            if ($item -in $uiGroups) {
                $output = Join-Path $record.output_root "maestro/$item"
                $null = New-Item -ItemType Directory -Path $output -Force
                & $python -X utf8 (Join-Path $PSScriptRoot 'maestro-run.py') --serial $Serial --adb $adb `
                    --fixture $fixturePath --output $output --group $item --source-root $androidRepoRoot 2>&1 |
                    Tee-Object -FilePath (Join-Path $output 'host.log')
                if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath (Join-Path $output 'results.json') -PathType Leaf)) {
                    throw "Maestro $item 失败或缺少 results.json。"
                }
            } else {
                $credentialPushed = $true
                $null = Invoke-Adb @('push', $deviceCredentialPath, '/data/local/tmp/moerise-android-fixture.json')
                $classes = switch ($item) {
                    lifecycle { 'io.github.fermin214.moerise.QuickNoteTest#loginWriteRecreateSaveAndReadBack' }
                    ime { 'io.github.fermin214.moerise.ImeLifecycleTest' }
                    stars { 'io.github.fermin214.moerise.StarRenderingTest' }
                    recovery { 'io.github.fermin214.moerise.QuickNoteTest#loginWriteRecreateSaveAndReadBack' }
                    diagnostic { ($TestClass | ForEach-Object { "io.github.fermin214.moerise.$_" }) -join ',' }
                }
                Invoke-Instrumentation -Classes $classes -Log (Join-Path $record.output_root "$item-instrumentation.log") `
                    -Visual ($item -eq 'recovery' -or [bool] $VisualSetup)
                if ($item -eq 'recovery') {
                    & $python -X utf8 (Join-Path $PSScriptRoot 'device-recovery.py') --serial $Serial --adb $adb `
                        --fixture $fixturePath --source-root $androidRepoRoot `
                        --output (Join-Path $record.output_root 'device-recovery') 2>&1 |
                        Tee-Object -FilePath (Join-Path $record.output_root 'recovery.log')
                    if ($LASTEXITCODE -ne 0) { throw '离线与进程恢复流程失败。' }
                }
            }
        } catch {
            $status = 'failed'
            $failedGroups.Add($item)
            Write-Warning $_.Exception.Message
        } finally {
            $watch.Stop()
            $results.Add("$item`t$status`t$([int] $watch.Elapsed.TotalSeconds)")
            $results | Set-Content -LiteralPath (Join-Path $record.output_root 'device-results.tsv') -Encoding utf8
        }
    }
    $device = [ordered] @{ source_root = $androidRepoRoot; commit = $record.commit; serial = $Serial
        fixture_port = $origin.Port; groups = $groups }
    foreach ($entry in @{ model = 'ro.product.model'; api = 'ro.build.version.sdk'
        android = 'ro.build.version.release'; abi = 'ro.product.cpu.abi' }.GetEnumerator()) {
        $device[$entry.Key] = (Invoke-Adb @('shell', 'getprop', $entry.Value)).Trim()
    }
    $device.page_size = (Invoke-Adb @('shell', 'getconf', 'PAGESIZE')).Trim()
    $device | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $record.output_root 'device.json') -Encoding utf8
    if ($failedGroups.Count -gt 0) { throw "设备组失败：$($failedGroups -join ', ')；结果目录：$($record.output_root)" }
} catch { $failure = $_ } finally {
    if ($credentialPushed) {
        try { $null = Invoke-Adb @('shell', 'rm', '-f', '/data/local/tmp/moerise-android-fixture.json') }
        catch { if (-not $failure) { $failure = $_ } }
    }
    if ($deviceCredentialPath -and (Test-Path -LiteralPath $deviceCredentialPath)) {
        try { Remove-Item -LiteralPath $deviceCredentialPath -Force }
        catch { if (-not $failure) { $failure = $_ } }
    }
    if ($reverseAdded -and -not $VisualSetup) {
        try { $null = Invoke-Adb @('reverse', '--remove', "tcp:$($origin.Port)") }
        catch { if (-not $failure) { $failure = $_ } }
    }
}
if ($failure) { throw $failure }
Write-Host $record.output_root
