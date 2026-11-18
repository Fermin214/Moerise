#requires -Version 7.2
<#
.SYNOPSIS
构建并归档 Windows Android 发行 APK。
.EXAMPLE
pwsh -NoProfile -File scripts/android/release.ps1 unsigned
.EXAMPLE
pwsh -NoProfile -File scripts/android/release.ps1 sign -ValidateOnly
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet('unsigned', 'sign')]
    [string] $Mode = 'unsigned',
    [string] $SourceRoot = 'C:\Projects\Moerise',
    [string] $SigningFile,
    [ValidateRange(1, 2147483647)] [int] $VersionCode = 1,
    [switch] $ValidateOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
. (Join-Path $PSScriptRoot 'environment.ps1') -ResolveOnly -SourceRoot $SourceRoot
$toolchain = Get-MoeriseAndroidEnvironment -RepositoryRoot $androidRepoRoot

function Invoke-Text([string] $Tool, [string[]] $Arguments) {
    $text = & $Tool @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "$([IO.Path]::GetFileName($Tool)) 执行失败，退出码 $LASTEXITCODE。"
    }
    return ($text | ForEach-Object { "$_" }) -join "`n"
}

function Invoke-Logged([string] $Tool, [string[]] $Arguments, [string] $Log) {
    & $Tool @Arguments 2>&1 | Tee-Object -FilePath $Log |
        ForEach-Object { Write-Host "$_" }
    if ($LASTEXITCODE -ne 0) {
        throw "$([IO.Path]::GetFileName($Tool)) 执行失败，退出码 $LASTEXITCODE；日志：$Log"
    }
}

function Get-BuildTool([string] $Name) {
    foreach ($suffix in @('.exe', '.bat', '')) {
        $path = Join-Path $toolchain.AndroidHome "build-tools/36.0.0/$Name$suffix"
        if (Test-Path -LiteralPath $path -PathType Leaf) { return $path }
    }
    throw "Android Build Tools 36.0.0 缺少 $Name。"
}

$python = $null
$pythonChoices = if ($env:MOERISE_ANDROID_PYTHON) { @($env:MOERISE_ANDROID_PYTHON) } else {
    @('C:\Users\Fermin\AppData\Local\hermes\hermes-agent\venv\Scripts\python.exe', 'python.exe', 'python3.exe')
}
foreach ($choice in $pythonChoices) {
    $command = Get-Command $choice -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($command -and $command.Source -notmatch '[\\/]Microsoft[\\/]WindowsApps[\\/]') {
        $python = $command.Source
        break
    }
}
if (-not $python) { throw '设置 MOERISE_ANDROID_PYTHON 为 Python 3.11 以上的 python.exe 路径。' }
$pythonVersion = Invoke-Text $python @('-X', 'utf8', '-c', 'import sys; print("%d.%d" % sys.version_info[:2])')
if ([version] $pythonVersion -lt [version] '3.11') { throw '发行检查需要 Python 3.11 以上。' }
$aapt2 = Get-BuildTool 'aapt2'
$zipalign = Get-BuildTool 'zipalign'
$apksigner = Get-BuildTool 'apksigner'
$gradle = Join-Path $androidRepoRoot 'mobile/gradlew.bat'
if (-not (Test-Path -LiteralPath $gradle -PathType Leaf)) { throw '找不到 mobile/gradlew.bat。' }
$profileRoot = Join-Path $androidRepoRoot 'mobile/androidApp/src/main/baselineProfiles'
$requireProfile = $true
foreach ($kind in @('baseline', 'startup')) {
    $profile = Join-Path $profileRoot "$kind-prof.txt"
    if (-not (Test-Path -LiteralPath $profile -PathType Leaf) -or
        [string]::IsNullOrWhiteSpace([IO.File]::ReadAllText($profile))) {
        $requireProfile = $false
    }
}

$keystore = $null
if ($Mode -eq 'sign') {
    if ($SigningFile) {
        try {
            $signing = Get-Content -LiteralPath $SigningFile -Raw | ConvertFrom-Json
            $fields = @($signing.PSObject.Properties.Name)
            $signing = [pscustomobject] @{
                keystore = $signing.keystore
                keyAlias = if ('alias' -in $fields) { $signing.alias } else { $signing.keyAlias }
                storePassword = if ('store_password' -in $fields) { $signing.store_password } else { $signing.storePassword }
                keyPassword = if ('key_password' -in $fields) { $signing.key_password } else { $signing.keyPassword }
            }
            foreach ($field in @('keystore', 'keyAlias', 'storePassword', 'keyPassword')) {
                if (-not $signing.$field) { throw 'invalid signing file' }
            }
        } catch { throw '签名档案需要 keystore、alias、store_password、key_password。' }
    }
    foreach ($name in @('MOERISE_ANDROID_KEYSTORE', 'MOERISE_ANDROID_KEY_ALIAS',
        'MOERISE_ANDROID_STORE_PASSWORD', 'MOERISE_ANDROID_KEY_PASSWORD')) {
        if (-not $SigningFile -and [string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($name, 'Process'))) {
            throw "签名需要环境变量 $name。"
        }
    }
    $keyPath = if ($SigningFile) { $signing.keystore } else { $env:MOERISE_ANDROID_KEYSTORE }
    if (-not (Test-Path -LiteralPath $keyPath -PathType Leaf)) {
        throw 'MOERISE_ANDROID_KEYSTORE 指定的密钥文件不存在。'
    }
    $keystore = (Resolve-Path -LiteralPath $keyPath).Path
}
$gitArguments = @('-C', $androidRepoRoot)
$commit = Invoke-Text 'git.exe' ($gitArguments + @('rev-parse', 'HEAD'))
$trackedClean = [string]::IsNullOrWhiteSpace(
    (Invoke-Text 'git.exe' ($gitArguments + @('status', '--porcelain', '--untracked-files=no'))))
$gradleArguments = @('-p', (Join-Path $androidRepoRoot 'mobile'), '--no-daemon', '--max-workers=1',
    '--no-parallel', '--console=plain',
    '-Dorg.gradle.jvmargs=-Xmx1536m -XX:MaxMetaspaceSize=768m -XX:ActiveProcessorCount=1 -Dfile.encoding=UTF-8',
    ':androidApp:assembleRelease', ':androidApp:lintRelease', ':androidApp:exportReleaseDependencies',
    "-PmoeriseVersionCode=$VersionCode")
if ($toolchain.ProjectCache) { $gradleArguments += @('--project-cache-dir', $toolchain.ProjectCache) }
if ($ValidateOnly) {
    [pscustomobject] @{
        mode = $Mode; commit = $commit; tracked_clean = $trackedClean
        java_home = $toolchain.JavaHome; android_home = $toolchain.AndroidHome
        gradle_user_home = $toolchain.GradleUserHome; project_cache = $toolchain.ProjectCache
        python = $python; aapt2 = $aapt2; zipalign = $zipalign; apksigner = $apksigner
        gradle_arguments = $gradleArguments
    } | ConvertTo-Json -Depth 4
    return
}
if (-not $trackedClean) { throw '发行构建要求已跟踪文件没有修改；先提交或还原这些修改。' }
$queue = Assert-MoeriseDeviceLock $androidRepoRoot

$savedEnvironment = @{}
foreach ($name in @('JAVA_HOME', 'ANDROID_HOME', 'ANDROID_SDK_ROOT', 'PATH', 'GRADLE_USER_HOME',
    'MOERISE_ANDROID_KEYSTORE', 'MOERISE_ANDROID_KEY_ALIAS', 'MOERISE_ANDROID_STORE_PASSWORD',
    'MOERISE_ANDROID_KEY_PASSWORD')) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$pushedLocation = $false
try {
    . (Join-Path $PSScriptRoot 'environment.ps1') -SourceRoot $androidRepoRoot
    if ($Mode -eq 'sign' -and $SigningFile) {
        $env:MOERISE_ANDROID_KEYSTORE = $keystore
        $env:MOERISE_ANDROID_KEY_ALIAS = $signing.keyAlias
        $env:MOERISE_ANDROID_STORE_PASSWORD = $signing.storePassword
        $env:MOERISE_ANDROID_KEY_PASSWORD = $signing.keyPassword
        Remove-Variable signing
    }
    Push-Location -LiteralPath $androidRepoRoot
    $pushedLocation = $true
    $archiveRoot = $queue.output_root
    $null = New-Item -ItemType Directory -Force -Path $archiveRoot
    $buildLog = Join-Path $archiveRoot "release-$($commit.Substring(0, 7))-$Mode-code$VersionCode.log"
    Invoke-Logged $gradle $gradleArguments $buildLog
    $null = Invoke-Text $python @('-X', 'utf8', 'scripts/android/licenses.py', '--check')

    $apkDirectory = Join-Path $androidRepoRoot 'mobile/androidApp/build/outputs/apk/release'
    $outputMetadataPath = Join-Path $apkDirectory 'output-metadata.json'
    $outputMetadata = Get-Content -LiteralPath $outputMetadataPath -Raw -Encoding utf8 | ConvertFrom-Json
    $elements = @($outputMetadata.elements)
    if ($outputMetadata.applicationId -ne 'io.github.fermin214.moerise' -or $elements.Count -ne 1) {
        throw '发行输出必须是 io.github.fermin214.moerise 的单个 APK。'
    }
    $version = [string] $elements[0].versionName
    $builtVersionCode = [long] $elements[0].versionCode
    if ($version -notmatch '^[A-Za-z0-9][A-Za-z0-9._+-]*$' -or $builtVersionCode -ne $VersionCode) {
        throw 'output-metadata.json 缺少有效的发行版本。'
    }
    $sourceApk = [IO.Path]::GetFullPath((Join-Path $apkDirectory $elements[0].outputFile))
    if ((Split-Path $sourceApk -Parent) -ne $apkDirectory -or
        [IO.Path]::GetExtension($sourceApk) -ne '.apk' -or
        -not (Test-Path -LiteralPath $sourceApk -PathType Leaf)) {
        throw 'output-metadata.json 的 APK 路径无效。'
    }
    $output = Join-Path $archiveRoot "releases/v$version-code$versionCode-$($commit.Substring(0, 7))"
    $null = New-Item -ItemType Directory -Force -Path $output
    $baseName = "moerise-android-v$version"
    $filename = if ($Mode -eq 'unsigned') { "$baseName-unsigned.apk" } else { "$baseName.apk" }
    $apk = Join-Path $output $filename
    $null = Invoke-Text $zipalign @('-P', '16', '-f', '4', $sourceApk, $apk)
    if ($Mode -eq 'sign') {
        $null = Invoke-Text $apksigner @('sign', '--ks', $keystore, '--ks-key-alias', $env:MOERISE_ANDROID_KEY_ALIAS,
            '--ks-pass', 'env:MOERISE_ANDROID_STORE_PASSWORD', '--key-pass', 'env:MOERISE_ANDROID_KEY_PASSWORD', $apk)
    }
    $verifyArguments = @('-X', 'utf8', 'scripts/android/verify-apk.py', $apk,
        '--release', '--package', 'io.github.fermin214.moerise', '--version', $version,
        '--version-code', "$VersionCode")
    if ($requireProfile) { $verifyArguments += '--require-profile' }
    if ($Mode -eq 'unsigned') { $verifyArguments += '--unsigned' }
    Invoke-Logged $python $verifyArguments (Join-Path $output "$([IO.Path]::GetFileNameWithoutExtension($filename))-verification.txt")
    foreach ($file in @('mobile/androidApp/build/reports/third-party-notices/THIRD_PARTY_NOTICES.md', 'docs/android/INSTALL.md', 'LICENSE',
        'mobile/androidApp/build/outputs/mapping/release/mapping.txt')) {
        Copy-Item -LiteralPath (Join-Path $androidRepoRoot $file) -Destination $output -Force
    }
    # 逐文件复制，保证同一版本再次归档不会产生 licenses/licenses。
    $licensesRoot = Join-Path $androidRepoRoot 'mobile/androidApp/build/reports/third-party-notices/licenses'
    foreach ($file in Get-ChildItem -LiteralPath $licensesRoot -File -Recurse) {
        $relative = [IO.Path]::GetRelativePath($licensesRoot, $file.FullName)
        $target = Join-Path $output "licenses/$relative"
        $null = New-Item -ItemType Directory -Force -Path (Split-Path $target -Parent)
        Copy-Item -LiteralPath $file.FullName -Destination $target -Force
    }
    Copy-Item -LiteralPath $outputMetadataPath -Destination (Join-Path $output 'output-metadata.json') -Force
    Move-Item -LiteralPath $buildLog -Destination (Join-Path $output "$([IO.Path]::GetFileNameWithoutExtension($filename))-build.log") -Force
    $checksum = (Get-FileHash -LiteralPath $apk -Algorithm SHA256).Hash.ToLowerInvariant()
    $utf8 = [Text.UTF8Encoding]::new($false)
    [IO.File]::WriteAllText("$apk.sha256", "$checksum  $filename`n", $utf8)
    $metadata = [ordered] @{
        commit = $commit; apk = $filename; sha256 = $checksum; signed = ($Mode -eq 'sign')
        version_name = $version; version_code = $versionCode
        source_date_epoch = [long] (Invoke-Text 'git.exe' @('show', '-s', '--format=%ct', $commit))
        java = ((Invoke-Text (Join-Path $env:JAVA_HOME 'bin/java.exe') @('-version')) -split '\r?\n')[0]
        build_tools = '36.0.0'
    }
    [IO.File]::WriteAllText("$apk.json", ($metadata | ConvertTo-Json -Depth 4) + "`n", $utf8)

    $noticesPath = Join-Path $output "$baseName-notices.zip"
    if (Test-Path -LiteralPath $noticesPath) { Remove-Item -LiteralPath $noticesPath -Force }
    $notices = [IO.Compression.ZipFile]::Open($noticesPath, [IO.Compression.ZipArchiveMode]::Create)
    try {
        $noticeFiles = @('THIRD_PARTY_NOTICES.md', 'INSTALL.md', 'LICENSE') |
            ForEach-Object { Join-Path $output $_ }
        $noticeFiles += @(Get-ChildItem -LiteralPath (Join-Path $output 'licenses') -File -Recurse |
            ForEach-Object { $_.FullName })
        foreach ($file in $noticeFiles | Sort-Object) {
            $entry = [IO.Path]::GetRelativePath($output, $file).Replace('\', '/')
            $null = [IO.Compression.ZipFileExtensions]::CreateEntryFromFile(
                $notices, $file, $entry, [IO.Compression.CompressionLevel]::Optimal)
        }
    } finally { $notices.Dispose() }
    Write-Host "$apk`nSHA-256: $checksum"
} finally {
    if ($pushedLocation) { Pop-Location }
    foreach ($name in $savedEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], 'Process')
    }
}
