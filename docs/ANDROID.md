# Android 开发

Android 工程位于 `mobile/`，应用模块为 `androidApp`，共享界面、协议与持久化模块为 `shared`。Android 包名为 `io.github.fermin214.moerise`，最低系统版本为 Android 8.0（API 26），目标和编译 SDK 为 36。

## Linux 工具链

需要 Linux x86_64、Python 3.11 以上、curl 和 util-linux。安装脚本下载 Temurin JDK 17.0.21+9 与 Android 官方命令行工具，核验固定 SHA-256，然后安装 SDK 平台和构建工具：

```bash
scripts/android/setup.sh
scripts/android/check.sh
```

工具默认位于 `~/.local/share/moerise-android/`；可通过 `MOERISE_ANDROID_TOOLS` 改变目录。已有工具链时设置 `JAVA_HOME` 和 `ANDROID_HOME`，后者需包含 `platforms;android-36`、`build-tools;36.0.0` 与 `platform-tools`。

Gradle Wrapper 固定为 8.13，并校验发行包 SHA-256。依赖版本见 [版本目录](../mobile/gradle/libs.versions.toml)：Kotlin 2.2.20、Compose Multiplatform 1.9.1、AGP 8.11.1、KSP 2.2.20-2.0.4、Room 2.8.3 和 SQLite 2.6.1。

## 检查与 APK

`scripts/android/check.sh` 顺序运行共享 JVM 测试、Android lint 和 debug APK 构建，并检查测试报告中实际执行的数量。JVM 测试使用 Room 与 BundledSQLiteDriver 的临时文件数据库，覆盖关闭重开后草稿和冻结请求的恢复及账号隔离。

构建默认限制一个 Gradle worker、单 CPU、1.5 GiB JVM 堆与 768 MiB 元空间；不并发执行多个构建。`MOERISE_ANDROID_CPU` 可指定允许使用的 CPU 编号。构建日志和设备产物放在 `.local/android/`。

APK 位于 `mobile/androidApp/build/outputs/apk/debug/androidApp-debug.apk`。连接启用 USB 调试的设备后安装：

```bash
source scripts/android/environment.sh
adb devices
adb install -r mobile/androidApp/build/outputs/apk/debug/androidApp-debug.apk
```

发行配置只允许 HTTPS；debug 配置允许 localhost、127.0.0.1 和 IPv6 回环的 HTTP。凭据、数据库和受保护内容排除云备份及设备迁移，持久数据库使用 `noBackupFilesDir`。

## 共享与平台边界

`commonMain` 使用 Compose、Coroutines 和 Room；Android 数据库路径位于 `androidMain`，Linux JVM 文件数据库构造位于 `jvmMain`。共享代码不引用 `android.*`。独立本地模式、iOS 平台和各功能的交付范围见[开发路线图](ROADMAP.md#1-阶段四android-客户端)。
