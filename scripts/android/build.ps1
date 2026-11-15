#requires -Version 7.2
[CmdletBinding()]
param(
    [string] $SourceRoot = 'C:\Projects\Moerise',
    [string[]] $Tasks = @(':shared:jvmTest', ':androidApp:lintFixture',
        ':androidApp:assembleFixture', ':androidApp:assembleFixtureAndroidTest'),
    [switch] $Offline,
    [switch] $ValidateOnly
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'environment.ps1') -ResolveOnly -SourceRoot $SourceRoot
$toolchain = Get-MoeriseAndroidEnvironment -RepositoryRoot $androidRepoRoot
if (-not $Tasks.Count -or @($Tasks | Where-Object { $_ -notmatch '^:[A-Za-z0-9_:]+$' }).Count) {
    throw '-Tasks 只接受 Gradle 任务路径。'
}
$arguments = @('-p', (Join-Path $androidRepoRoot 'mobile'), '--no-daemon', '--max-workers=1',
    '--no-parallel', '--console=plain', '--project-cache-dir', $toolchain.ProjectCache,
    '-Dorg.gradle.jvmargs=-Xmx1536m -XX:MaxMetaspaceSize=768m -XX:ActiveProcessorCount=1 -Dfile.encoding=UTF-8') + $Tasks
if ($Offline) { $arguments += '--offline' }
if ($ValidateOnly) {
    [pscustomobject] @{ source_root = $androidRepoRoot; gradle_home = $toolchain.GradleUserHome
        project_cache = $toolchain.ProjectCache; arguments = $arguments } | ConvertTo-Json -Depth 3
    return
}
$record = Assert-MoeriseDeviceLock $androidRepoRoot
$saved = @{}
foreach ($name in @('JAVA_HOME', 'ANDROID_HOME', 'ANDROID_SDK_ROOT', 'GRADLE_USER_HOME', 'PATH')) {
    $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
try {
    . (Join-Path $PSScriptRoot 'environment.ps1') -SourceRoot $androidRepoRoot
    & (Join-Path $androidRepoRoot 'mobile/gradlew.bat') @arguments 2>&1 |
        Tee-Object -FilePath (Join-Path $record.output_root 'build.log')
    if ($LASTEXITCODE -ne 0) { throw 'Android 构建失败。' }
    if (':androidApp:assembleFixture' -in $Tasks -and ':androidApp:assembleFixtureAndroidTest' -in $Tasks) {
        $artifacts = foreach ($relative in @(
            'mobile/androidApp/build/outputs/apk/fixture/androidApp-fixture.apk',
            'mobile/androidApp/build/outputs/apk/androidTest/fixture/androidApp-fixture-androidTest.apk'
        )) {
            $path = Join-Path $androidRepoRoot $relative
            [pscustomobject] @{ path = $relative
                sha256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() }
        }
        [pscustomobject] @{ source_root = $androidRepoRoot; commit = $record.commit; artifacts = @($artifacts) } |
            ConvertTo-Json -Depth 4 |
            Set-Content -LiteralPath (Join-Path $record.output_root 'build-artifacts.json') -Encoding utf8
    }
} finally {
    foreach ($name in $saved.Keys) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
}
