#requires -Version 7.2
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateSet('prepare', 'run', 'ui', 'manual', 'status', 'record', 'cleanup')]
    [string] $Action,
    [string] $Session,
    [string] $Serial = $env:ANDROID_SERIAL,
    [string] $Fixture,
    [string] $BuildManifest,
    [ValidateSet('editing', 'actions', 'search', 'insights', 'navigation', 'lifecycle', 'ime', 'stars',
        'recovery', 'attachments', 'home', 'language')]
    [string[]] $Group,
    [ValidateSet('passed', 'failed', 'inconclusive')]
    [string] $Outcome,
    [string] $Note,
    [ValidateSet('login', 'editing', 'search', 'actions', 'insights', 'language', 'attachments',
        'home-smoke', 'navigation', 'archive', 'insights-material', 'attachment-cancel')]
    [string] $Flow,
    [string] $NoteName,
    [switch] $Retry,
    [switch] $Specialists,
    [switch] $ValidateOnly
)
$ErrorActionPreference = 'Stop'
$repo = 'C:\Projects\Moerise'
. (Join-Path $PSScriptRoot 'environment.ps1') -ResolveOnly
$toolchain = Get-MoeriseAndroidEnvironment $repo
$adb = Join-Path $toolchain.AndroidHome 'platform-tools/adb.exe'
$python = if ($env:MOERISE_ANDROID_PYTHON) { $env:MOERISE_ANDROID_PYTHON } else {
    'C:\Users\Fermin\AppData\Local\hermes\hermes-agent\venv\Scripts\python.exe'
}
if ($Session) {
    $Session = Resolve-MoeriseAndroidOutputRoot ([IO.Path]::GetFullPath($Session))
    $statePath = Join-Path $Session 'session.json'
    if (Test-Path -LiteralPath $statePath) {
        $state = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
        if (-not $Serial) { $Serial = $state.serial }
    }
} elseif ($Action -ne 'prepare') {
    throw '传入 prepare 输出的 -Session 路径。'
}
if ($Action -eq 'prepare' -and (-not $Fixture -or -not $BuildManifest)) {
    throw 'prepare 需要 -Fixture 和同提交的 -BuildManifest，不在手机验收中构建。'
}
if ($ValidateOnly) {
    [ordered] @{
        action = $Action; session = $Session; serial = $Serial
        connection = 'USB adb reverse / HTTP loopback fixture'
        install = ($Action -eq 'prepare'); uninstall = ($Action -eq 'cleanup')
        reset_app_data = $false; change_phone_network = $false
        group = $Group; repeat_passed = [bool] $Retry
    } | ConvertTo-Json -Depth 4
    return
}
if ($Action -eq 'status') {
    if (-not (Test-Path -LiteralPath $statePath)) { throw '会话不存在。' }
    Get-Content -LiteralPath $statePath
    return
}
if (-not $Serial) {
    $ready = @(& $adb devices | Where-Object { $_ -match '^(\S+)\s+device$' } |
        ForEach-Object { ($_ -split '\s+')[0] })
    if ($ready.Count -ne 1) { throw '传入唯一真机的 -Serial。' }
    $Serial = $ready[0]
}
if ($Serial -match '^emulator-') { throw 'phone.ps1 用于真机；模拟器使用 CI 入口。' }
if ($Fixture) { $Fixture = (Resolve-Path -LiteralPath $Fixture).Path }
if ($BuildManifest) { $BuildManifest = (Resolve-Path -LiteralPath $BuildManifest).Path }
& (Join-Path $PSScriptRoot 'with-device-lock.ps1') -Owner phone-session -Task "phone-$Action" -Command {
    param($context)
    $directory = if ($Session) { $Session } else { Join-Path $context.output_root 'session' }
    $arguments = @('-X', 'utf8', (Join-Path $repo 'scripts/android/phone-session.py'), $Action,
        '--repo', $repo, '--session', $directory, '--output', $context.output_root,
        '--commit', $context.commit, '--adb', $adb, '--serial', $Serial)
    if ($Fixture) { $arguments += @('--fixture', $Fixture) }
    if ($BuildManifest) { $arguments += @('--manifest', $BuildManifest) }
    foreach ($item in $Group) { $arguments += @('--group', $item) }
    if ($Retry) { $arguments += '--retry' }
    if ($Specialists) { $arguments += '--specialists' }
    if ($Flow) { $arguments += @('--flow', $Flow) }
    if ($NoteName) { $arguments += @('--note-name', $NoteName) }
    if ($Outcome) { $arguments += @('--outcome', $Outcome) }
    if ($Note) { $arguments += @('--note', $Note) }
    $savedJava = $env:JAVA_HOME
    try {
        $env:JAVA_HOME = $toolchain.JavaHome
        & $python @arguments
    } finally { $env:JAVA_HOME = $savedJava }
    if ($LASTEXITCODE -ne 0) { throw "手机流程 $Action 未完成；结果目录：$($context.output_root)" }
}
