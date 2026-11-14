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

共享编辑器检查覆盖 schema 1→2 迁移、创建与编辑的丢失响应恢复、保存确认事务期间续写，以及原键重放返回外部修改时的冲突合并。

构建默认限制一个 Gradle worker、单 CPU、1.5 GiB JVM 堆与 768 MiB 元空间；不并发执行多个构建。`MOERISE_ANDROID_CPU` 可指定允许使用的 CPU 编号。构建日志和设备产物放在 `.local/android/`。

APK 位于 `mobile/androidApp/build/outputs/apk/debug/androidApp-debug.apk`。连接启用 USB 调试的设备后安装：

```bash
source scripts/android/environment.sh
adb devices
adb install -r mobile/androidApp/build/outputs/apk/debug/androidApp-debug.apk
```

提交后执行 `scripts/android/archive-debug.sh M2` 可按里程碑和提交号归档 debug APK，检查签名、APK 中 native 库的 16 KiB 对齐，以及 64 位 ELF LOAD 段对齐，并输出 SHA-256。归档入口要求已跟踪文件没有改动。

发行配置只允许 HTTPS；debug 配置允许 localhost、127.0.0.1 和 IPv6 回环的 HTTP。凭据、数据库和受保护内容排除云备份及设备迁移，持久数据库使用 `noBackupFilesDir`。

## 共享与平台边界

`commonMain` 使用 Compose、Coroutines 和 Room；Android 数据库路径位于 `androidMain`，Linux JVM 文件数据库构造位于 `jvmMain`。共享代码不引用 `android.*`。独立本地模式、iOS 平台和各功能的交付范围见[开发路线图](ROADMAP.md#1-阶段四android-客户端)。

## Core 连接与真实服务联调

连接页接受与 `web.public_origin` 相同的 Web 入口，API 位于 `/api/app/v1`。登录密码仅用于登录请求；会话 Cookie 使用 Android Keystore 的 AES-GCM 密钥加密后存入免备份目录，CSRF 只保存在内存。Cookie 限定同一 scheme、host 和端口，不跟随重定向，不启用底层写入自动重试。

服务来源、实例 ID 和作者资源名共同组成账号隔离键。恢复会话时重新读取身份，身份变化要求登录；非 GET/HEAD 操作携带 Origin 和 CSRF，CSRF 校验失败时仅刷新一次。退出先清除本机身份，再尝试撤销服务端会话；旧响应不能覆盖新登录的 Cookie。

真实服务检查需要 Go 1.27、Node.js 24 及 Docker。Core 观星契约基线为 `062185f`，Memos 固定为 `0.31.0` 及脚本中的镜像摘要。执行：

```bash
scripts/android/live-check.sh
```

脚本构建独立 Core，启动受 CPU 和内存限制的 Memos 容器及 Web 代理，使用虚构账号检查登录、重开会话、同键创建、实时读回、删除与注销。测试结束清理容器、来源数据和临时凭据；非敏感日志留在 `.local/android/live-*/`。Linux JVM 检查使用与 Android 相同的 OkHttp 引擎；Keystore、设备生命周期和触控通过 Android 设备流程检查。

单独联调设备时运行 `scripts/android/start-fixture.sh`。脚本输出 Web 来源、私有连接档案路径和 `adb reverse` 命令；连接档案只用于隔离环境，包含虚构账号和临时凭据。按 Ctrl+C 清理服务与来源数据。观星六类顶层状态及更新中的固定响应样本位于 `mobile/shared/src/commonTest/resources/contracts/map/`。

## 草稿与设备流程

已配置账号启动后先恢复本机草稿，再加载时间线和检查会话。编辑器不等待网络；正文在 300 毫秒防抖后落盘，保存、切页和进入后台时立即刷入数据库。保存请求在发送前冻结正文、来源摘要和幂等键，重试沿用原始请求。提交期间的续写保留在同一篇笔记中，服务器版本发生变化时进入合并页面。

`note_editor_ready` 标记从 Activity 创建开始，到本机草稿恢复后编辑器完成首个可输入帧结束，同时调用 `reportFullyDrawn`。此标记用于设备启动测量。

设备验收使用独立包 `io.github.fermin214.moerise.fixture`，数据与普通安装隔离。脚本会清空该验收包的数据；连接档案在设备测试后删除。隔离服务运行时执行：

```bash
export ANDROID_SERIAL=设备序列号
export MOERISE_ANDROID_FIXTURE=/绝对路径/connection.json
scripts/android/acceptance.sh
```

设备流程覆盖登录、输入、Activity 重建、保存与时间线读回，结果写入 `.local/android/device-acceptance.log`。输入法、进程终止和后台恢复分别通过设备操作及持久化恢复检查补充。
