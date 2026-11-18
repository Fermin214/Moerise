#requires -Version 7.2
[CmdletBinding()]
param(
    [switch] $ResolveOnly,
    [string] $SourceRoot = 'C:\Projects\Moerise'
)
. (Join-Path $PSScriptRoot 'device-lock.ps1')

# 工具链解析与进程环境初始化分开，便于无设备检查。
function Get-MoeriseAndroidEnvironment {
    [CmdletBinding()]
    param(
        [string] $RepositoryRoot = 'C:\Projects\Moerise'
    )

    $RepositoryRoot = Resolve-MoeriseAndroidSourceRoot $RepositoryRoot
    $toolsRoot = Join-Path $RepositoryRoot '.local/android/tools'
    $javaHome = if ($env:JAVA_HOME) { $env:JAVA_HOME } else {
        $bundledJava = Join-Path $toolsRoot 'jdk-17.0.21+9'
        $installedJava = 'C:\Program Files\Microsoft\jdk-17.0.12.7-hotspot'
        if (Test-Path -LiteralPath $installedJava) { $installedJava }
        elseif (Test-Path -LiteralPath $bundledJava) { $bundledJava } else {
            $java = Get-Command java.exe -CommandType Application -ErrorAction SilentlyContinue |
                Select-Object -First 1
            if (-not $java) { throw '设置 JAVA_HOME 为 JDK 17 目录。' }
            Split-Path (Split-Path $java.Source -Parent) -Parent
        }
    }
    $javaHome = [IO.Path]::GetFullPath($javaHome)
    $releaseFile = Join-Path $javaHome 'release'
    if (-not (Test-Path -LiteralPath (Join-Path $javaHome 'bin/javac.exe') -PathType Leaf) -or
        -not (Test-Path -LiteralPath $releaseFile -PathType Leaf) -or
        -not (Select-String -LiteralPath $releaseFile -Pattern '^JAVA_VERSION="17(?:[."+-])' -Quiet)) {
        throw 'JAVA_HOME 必须指向完整的 JDK 17。'
    }
    $sdkRoot = Join-Path $toolsRoot 'android-sdk'
    $sdkRoot = [IO.Path]::GetFullPath($sdkRoot)
    foreach ($component in @('platforms/android-36/android.jar', 'build-tools/36.0.0/aapt2.exe')) {
        if (-not (Test-Path -LiteralPath (Join-Path $sdkRoot $component) -PathType Leaf)) {
            throw "共享 Android SDK 缺少 $component：$sdkRoot。"
        }
    }
    $gradleHome = Join-Path $RepositoryRoot '.local/android/gradle-home'
    [pscustomobject] @{
        ToolsRoot = [IO.Path]::GetFullPath($toolsRoot)
        JavaHome = $javaHome
        AndroidHome = $sdkRoot
        GradleUserHome = [IO.Path]::GetFullPath($gradleHome)
        ProjectCache = Join-Path $RepositoryRoot '.local/android/project-cache'
    }
}

$androidRepoRoot = Resolve-MoeriseAndroidSourceRoot $SourceRoot
if (-not $ResolveOnly) {
    $androidEnvironment = Get-MoeriseAndroidEnvironment -RepositoryRoot $androidRepoRoot
    $env:JAVA_HOME = $androidEnvironment.JavaHome
    $env:ANDROID_HOME = $androidEnvironment.AndroidHome
    $env:ANDROID_SDK_ROOT = $androidEnvironment.AndroidHome
    $env:GRADLE_USER_HOME = $androidEnvironment.GradleUserHome
    $androidPathEntries = @(
        (Join-Path $env:JAVA_HOME 'bin'),
        (Join-Path $env:ANDROID_HOME 'platform-tools'),
        (Join-Path $env:ANDROID_HOME 'cmdline-tools/latest/bin'),
        (Join-Path $androidRepoRoot '.local/tools/go/bin'),
        'C:\Users\Fermin\AppData\Local\Programs\DockerDesktop\resources\bin',
        'C:\Users\Fermin\AppData\Local\Programs\DockerDesktop\resources\cli-plugins'
    )
    $env:PATH = (($androidPathEntries + ($env:PATH -split ';')) | Select-Object -Unique) -join ';'
}
