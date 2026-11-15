#requires -Version 7.2
[CmdletBinding()]
param(
    [string] $SourceRoot = 'C:\Projects\Moerise',
    [string] $Serial = $env:ANDROID_SERIAL,
    [string] $Fixture = $env:MOERISE_ANDROID_FIXTURE,
    [string] $BuildManifest,
    [ValidatePattern('^(QuickNoteTest|ImeLifecycleTest)(#[A-Za-z][A-Za-z0-9_]*)?$')]
    [string[]] $TestClass = @('QuickNoteTest', 'ImeLifecycleTest'),
    [switch] $SkipBuild,
    [switch] $Recovery,
    [switch] $VisualSetup,
    [switch] $ValidateOnly
)
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
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
if ($VisualSetup -and $Recovery) { throw '-VisualSetup 与 -Recovery 不能同时使用。' }
if ($ValidateOnly) {
    [pscustomobject] @{ source_root = $androidRepoRoot; serial = $Serial; fixture_port = $origin.Port
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
if (-not $Serial) {
    $devices = & $adb devices
    if ($LASTEXITCODE -ne 0) { throw '读取 ADB 设备列表失败。' }
    $ready = @($devices | Where-Object { $_ -match '^(\S+)\s+device$' } | ForEach-Object { ($_ -split '\s+')[0] })
    if ($ready.Count -ne 1) { throw '需要恰好一台已连接设备，或传入 -Serial。' }
    $Serial = $ready[0]
}
if ($Serial -match '\s' -or $Serial.StartsWith('-')) { throw '设备序列号无效。' }
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
$expectedPaths = @(
    'mobile/androidApp/build/outputs/apk/fixture/androidApp-fixture.apk',
    'mobile/androidApp/build/outputs/apk/androidTest/fixture/androidApp-fixture-androidTest.apk'
)
if (@($manifest.artifacts).Count -ne $expectedPaths.Count) { throw '构建清单需要两个设备流程 APK。' }
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
    $credentialPushed = $true
    $deviceCredentialPath = Join-Path (Split-Path $fixturePath -Parent) "device-connection-$([Guid]::NewGuid().ToString('N')).json"
    $deviceConnection | ConvertTo-Json | Set-Content -LiteralPath $deviceCredentialPath -Encoding utf8
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    & icacls $deviceCredentialPath /inheritance:r /grant:r "${identity}:F" 'SYSTEM:F' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw '设备连接档案权限设置失败。' }
    Remove-Variable deviceConnection
    $null = Invoke-Adb @('push', $deviceCredentialPath, '/data/local/tmp/moerise-android-fixture.json')
    $classes = if ($VisualSetup) { 'io.github.fermin214.moerise.QuickNoteTest' } else {
        ($TestClass | ForEach-Object { "io.github.fermin214.moerise.$_" }) -join ','
    }
    $arguments = @('shell', 'am', 'instrument', '-w', '-e', 'class', $classes)
    if ($VisualSetup) { $arguments += @('-e', 'visualSetup', 'true') }
    $arguments += 'io.github.fermin214.moerise.fixture.test/androidx.test.runner.AndroidJUnitRunner'
    $result = Invoke-Adb $arguments -TimeoutSeconds 600 `
        -Log (Join-Path $record.output_root 'instrumentation.log')
    if ($result -notmatch '(?m)^OK \([1-9][0-9]* tests?\)' -or $result -match 'FAILURES!!!|INSTRUMENTATION_FAILED') {
        throw '设备自动化失败；查看 instrumentation.log。'
    }
    if ($Recovery) {
        $python = if ($env:MOERISE_ANDROID_PYTHON) { $env:MOERISE_ANDROID_PYTHON } else {
            'C:\Users\Fermin\AppData\Local\hermes\hermes-agent\venv\Scripts\python.exe'
        }
        & $python -X utf8 (Join-Path $PSScriptRoot 'device-recovery.py') --serial $Serial --adb $adb `
            --fixture $fixturePath --source-root $androidRepoRoot --output $record.output_root 2>&1 |
            Tee-Object -FilePath (Join-Path $record.output_root 'recovery.log')
        if ($LASTEXITCODE -ne 0) { throw '离线与进程恢复流程失败。' }
    }
    $device = [ordered] @{ source_root = $androidRepoRoot; commit = $record.commit; serial = $Serial
        fixture_port = $origin.Port; visual_setup = [bool] $VisualSetup }
    foreach ($entry in @{ model = 'ro.product.model'; api = 'ro.build.version.sdk'
        android = 'ro.build.version.release'; abi = 'ro.product.cpu.abi' }.GetEnumerator()) {
        $device[$entry.Key] = (Invoke-Adb @('shell', 'getprop', $entry.Value)).Trim()
    }
    $device.page_size = (Invoke-Adb @('shell', 'getconf', 'PAGESIZE')).Trim()
    $device | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $record.output_root 'device.json') -Encoding utf8
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
