#requires -Version 7.2
[CmdletBinding()]
param(
    [string] $SourceRoot = 'C:\Projects\Moerise',
    [string] $FixtureRoot,
    [switch] $Resume,
    [switch] $ValidateOnly
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'environment.ps1') -ResolveOnly -SourceRoot $SourceRoot
$fixtureParent = Join-Path $androidRepoRoot '.local/android'
if (-not $FixtureRoot) {
    if ($Resume) { throw '-Resume 需要 -FixtureRoot。' }
    $FixtureRoot = Join-Path $fixtureParent ('fixture-' + [Guid]::NewGuid().ToString('N').Substring(0, 12))
}
$FixtureRoot = Resolve-MoeriseAndroidOutputRoot $FixtureRoot
if ((Split-Path $FixtureRoot -Parent) -ne $fixtureParent -or
    (Split-Path $FixtureRoot -Leaf) -notmatch '^fixture-[a-f0-9]{12}$') {
    throw '实例目录须为主仓库 .local/android/fixture-<12位十六进制>。'
}
if ($Resume -and -not (Test-Path -LiteralPath (Join-Path $FixtureRoot 'connection.json'))) {
    throw '找不到可恢复的连接档案。'
}
if ($ValidateOnly) {
    [pscustomobject] @{ source_root = $androidRepoRoot; fixture_root = $FixtureRoot; resume = [bool] $Resume } |
        ConvertTo-Json
    return
}
$queue = Assert-MoeriseDeviceLock $androidRepoRoot
if ($Resume) {
    & (Join-Path $PSScriptRoot 'fixture-control.ps1') status -FixtureRoot $FixtureRoot -RequireStopped
} elseif (Test-Path -LiteralPath $FixtureRoot) { throw '新实例目录已存在。' }
$saved = @{}
foreach ($name in @('JAVA_HOME', 'ANDROID_HOME', 'ANDROID_SDK_ROOT', 'GRADLE_USER_HOME', 'PATH')) {
    $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
try {
    . (Join-Path $PSScriptRoot 'environment.ps1') -SourceRoot $androidRepoRoot
    $null = New-Item -ItemType Directory -Path $FixtureRoot -Force
    # 连接档案与数据库只供运行用户和 SYSTEM 访问。
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    & icacls $FixtureRoot /inheritance:r /grant:r "${identity}:(OI)(CI)F" 'SYSTEM:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw '实例目录权限设置失败。' }
    & go -C $androidRepoRoot build -p 1 -o (Join-Path $FixtureRoot 'moerise.exe') .
    if ($LASTEXITCODE -ne 0) { throw 'Core 构建失败' }
    $node = (Get-Command node.exe -CommandType Application -ErrorAction Stop |
        Select-Object -First 1).Source
    $arguments = @((Join-Path $androidRepoRoot 'scripts/android/fixture.mjs'), $FixtureRoot)
    if ($Resume) { $arguments += '--resume' }
    $process = Start-Process -FilePath $node -ArgumentList $arguments -WorkingDirectory $androidRepoRoot `
        -WindowStyle Hidden -RedirectStandardOutput (Join-Path $FixtureRoot 'fixture.log') `
        -RedirectStandardError (Join-Path $FixtureRoot 'fixture-error.log') -PassThru
    $deadline = [DateTime]::UtcNow.AddMinutes(20)
    while ([DateTime]::UtcNow -lt $deadline) {
        $process.Refresh()
        if ($process.HasExited) { throw "隔离服务退出异常；查看 $FixtureRoot\fixture-error.log。" }
        if (Test-Path -LiteralPath (Join-Path $FixtureRoot 'instance.json')) {
            $instance = Get-Content -LiteralPath (Join-Path $FixtureRoot 'instance.json') -Raw | ConvertFrom-Json
            if ($instance.nodePid -eq $process.Id) {
                Write-Host (Join-Path $FixtureRoot 'connection.json')
                return
            }
        }
        Start-Sleep -Milliseconds 500
    }
    throw '实例准备超时；使用 fixture-control.ps1 status 查看指定实例。'
} finally {
    foreach ($name in $saved.Keys) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
}
