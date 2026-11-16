# Android 设备检查

使用独立 Core/Memos 数据集和虚构笔记。自动化安装包为 `.fixture`，性能包为
`.benchmark`，发行包为 `io.github.fermin214.moerise`。每份设备报告记录提交号、APK
SHA-256、签名证书摘要、设备型号、系统/API、ABI、页大小、界面语言和服务来源。
日志、截图、数据库副本和 trace 放入 `.local/android/`。

Android 0.1.0 首次发行使用新安装数据，Room 版本为 1，
以完整的 [schema 1](../../mobile/shared/schemas/io.github.fermin214.moerise.data.LocalDatabase/1.json) 建库。
开发期 APK 和数据库不提供兼容迁移，首版设备流程不包含旧开发版覆盖升级。
同一首版安装内的关闭重开、离线队列和输入恢复仍按下述流程检查。

## 隔离服务自动化

启动 `scripts/android/start-fixture.sh`，在另一个终端设置：

```bash
source scripts/android/environment.sh
export ANDROID_SERIAL=设备序列号
export MOERISE_ANDROID_FIXTURE=/绝对路径/connection.json
scripts/android/acceptance.sh
python3 scripts/android/device-recovery.py --serial "$ANDROID_SERIAL" \
  --fixture "$MOERISE_ANDROID_FIXTURE" --output .local/android/recovery
```

`acceptance.sh` 会清空 `.fixture` 包的数据，覆盖登录、记录、Activity 重建、搜索、
单条笔记原位编辑、切换落盘、顶部草稿恢复、归档恢复、删除和探索导航。恢复脚本通过设备测试输入唯一标记，
读取隔离包的 SQLite 与 WAL 副本，证明离线队列和未完成输入落盘后再终止进程。
Linux 入口按 `MOERISE_ANDROID_TEST_CLASS` 的逗号分隔列表逐组运行，每组启动新的应用进程；
单个用例限制为 5 分钟，一组 instrumentation 限制为 10 分钟。超时后仍提取设备诊断并关闭隔离包。
一组失败后继续收集其他组与离线恢复的结果，任何阶段失败都会使验收退出失败。

## 集中检查与定向重跑

Android CI 先构建一对 fixture APK，再并行运行六组设备流程：输入法、编辑恢复、
搜索阅读、归档删除、洞察导航、离线恢复。每组使用独立模拟器和 Core/Memos 数据集，
单组失败不取消其他组。JVM、lint、发行构建和接口检查同时运行。
结果页按组显示成功、失败和耗时，`android-device-<组名>` 附件保存该组的日志和诊断。
修复依据整批结果整理，重跑时选择涉及的组。

设备调试使用没有开放 PR 的独立分支，推送后通过已有的 `Checks` 工作流选择设备组。
修复集中通过后更新 PR 分支，触发完整检查：

```bash
gh workflow run checks.yml --ref debug/android-device -f mode=android-device -f suite=all
gh workflow run checks.yml --ref debug/android-device -f mode=android-device -f suite=ime
```

`suite` 支持 `all`、`ime`、`editing`、`search`、`actions`、`insights`、`recovery`。
`android-device` 模式只执行所选设备组；PR 的自动检查执行完整流程。
相同源码需要重试偶发失败时，用 `gh run rerun <运行编号> --failed` 只重跑失败任务。
修改源码后需要重新构建；重试旧运行仍检查原提交。
仅修改宿主脚本或文档时，可以传入 `-f fixture_run=<原运行编号>` 复用原运行的
`android-fixture-apks`，跳过 Gradle。Android 源码或 APK 摘要不一致时，复用校验失败。
同一源码的 APK 在各组之间复用；安装前核对 Android 源文件和两个 APK 的 SHA-256。
源码或包有变化时拒绝复用。Linux 手动复用入口为：

```bash
export MOERISE_ANDROID_SKIP_BUILD=1
export MOERISE_ANDROID_SUITE=ime
scripts/android/ci-device-check.sh
```

省略 `MOERISE_ANDROID_SKIP_BUILD` 时构建 APK 并生成
`.local/android/fixture-artifacts.json`。设备流程的逐组结果写入
`.local/android/device-results.tsv`。

## Windows 集中入口

构建与设备操作从 `C:\Projects\Moerise` 的整合源码执行。SDK、工具和 Gradle 缓存共用
主仓库 `.local/android/tools`、`.local/android/gradle-home` 与
`.local/android/project-cache`。JDK 使用 `JAVA_HOME` 指定的完整 JDK 17，默认检查
`C:\Program Files\Microsoft\jdk-17.0.12.7-hotspot`。

所有 worktree 共用 `C:\Projects\Moerise\.local\android\device-build.lock`。
`with-device-lock.ps1` 持有 Windows 文件排他句柄，记录任务、持有者、PID、进程启动时间、
源码分支与提交号。锁忙时立即失败；文件中的旧记录不会阻挡已释放的句柄。
子入口验证令牌、持有者启动时间及进程继承关系。状态查询不构建、不安装：

```powershell
.\scripts\android\with-device-lock.ps1 -Status
.\scripts\android\build.ps1 -ValidateOnly
```

统一流程采用独立结果目录，日志与 APK 摘要写入主仓库
`.local/android/<任务>-<UTC时间>-<随机标识>/`。先启动隔离服务，或选择已经运行的实例，
将其输出的 `connection.json` 绝对路径传入：

```powershell
$fixture = (Resolve-Path -LiteralPath (
    Read-Host '输入隔离实例 connection.json 的绝对路径')).Path
.\scripts\android\with-device-lock.ps1 -Owner android-acceptance -Task device -Command {
    param($context)
    $source = $context.source_root
    & "$source\scripts\android\build.ps1" -SourceRoot $source -Offline
    & "$source\scripts\android\acceptance.ps1" -SourceRoot $source -SkipBuild -Recovery `
        -Fixture $fixture
}
```

`-Offline` 仅适用于所需依赖已缓存的环境；首次下载依赖时省略该参数。
省略 `-Serial` 时，从 `adb devices` 动态选择唯一已就绪设备；有多个设备时必须指定。
`-SkipBuild` 复用同一源码提交生成的 APK，通过 `build-artifacts.json` 核对提交和两个 APK 摘要；
跨任务复用时用 `-BuildManifest` 指定原构建清单。`-VisualSetup` 只运行登录准备，
可用于后续截图。输入通过 instrumentation，避免中文输入法改写 `shell input text`。
测试只清空 `.fixture`，设备连接档案只包含登录字段，宿主临时文件和设备副本在结束时删除。
既有端口映射保留，
新建映射在普通验收结束时删除；视觉准备的映射供页面检查继续使用。

跨任务复用构建的命令如下；`$fixture` 使用上例选择的连接档案，
清单路径取自原构建任务的 `output_root`：

```powershell
$buildManifest = (Resolve-Path -LiteralPath (
    Read-Host '输入原构建任务 build-artifacts.json 的绝对路径')).Path
.\scripts\android\with-device-lock.ps1 -Owner android-acceptance -Task recovery -Command {
    param($context)
    & "$($context.source_root)\scripts\android\acceptance.ps1" -SourceRoot $context.source_root `
        -SkipBuild -Recovery -BuildManifest $buildManifest -Fixture $fixture
}
```

笔记正文和时间戳点击不导航，三点菜单没有“打开笔记”，来源阅读回主页定位卡片。
顶部新建使用
`note_editor` / `save_note`，原位编辑使用包含完整笔记名或本机记录 ID 的
`note_editor_<key>` / `save_note_<key>` / `cancel_note_<key>`。
设备流程只允许一个编辑器：编辑已有笔记时隐藏顶部新建，顶部未完成输入仍在本机保存；
切换到另一条前先落盘前一条，重新编辑恢复对应修订；Activity 重建和离页返回后恢复单条编辑。
保存采用点击时的修订，取消不上传，保存或取消后恢复顶部输入。
手机视口仅通过主页列表的滚动语义定位卡片，先等待页面和列表就绪；
侧栏底部的设置、账号按钮与弹窗底部操作直接点击，不向无滚动祖先的控件发送滚动操作。
搜索使用主页顶部输入及 IME 搜索操作；洞察材料与历史通过弹窗访问，
选材按钮可用后再点击。材料阅读回主页的流程直接断言目标卡片可见，避免测试滚动掩盖定位错误。

`FixtureActivityRule` 使用 UiAutomation shell 显式启动 `.fixture`，再交给
ActivityScenario 管理重建与销毁，兼容限制后台 Activity 启动的设备系统。
输入法流程使用 Compose 的 `InterceptPlatformTextInput` 独占编辑器的输入会话，
从编辑器请求创建真实的 Android InputConnection，覆盖组合输入、提交、光标编辑及 Activity 重建恢复。
每次 Activity 重建都重新安装拦截器，系统输入法的启用列表和默认设置无需改写；
手机窗口固定竖屏，输入法流程检查竖屏配置及两次 Activity 重建后的落盘内容和输入连接。
系统键盘的候选选择与切换操作见交互检查表。
失败用例的截图、连接和操作状态、异常和全部 Java 线程栈、应用日志写入 `.local/android/device-diagnostics.tar`；
CI 同时保存 `device-fixture-core.log` 和 `device-fixture-fixture.log`，用于区分界面状态与来源服务错误。

恢复入口由 `acceptance.ps1 -Recovery` 调用，分阶段运行 `OfflineQueueTest`：

1. 移除该 fixture 的单个反向端口；保存两篇笔记，验证输入框清空和时间线出现离线笔记，
   再输入一段未点击保存的正文。
2. 用 `run-as tar` 读取 SQLite/WAL，在临时目录确认两篇笔记和未完成输入落盘；
   宿主机 `force-stop` 后重开，验证两篇已保存队列笔记与一份未提交新建输入恢复；分别从队列卡片菜单编辑，
   检查单编辑器及 Activity 重建恢复，取消后恢复顶部未完成输入。
3. 返回桌面并恢复反向端口，全程不点击同步；从 Memos 官方 API 读回两篇笔记，
   验证后台自动排空且未完成输入没有上传。
4. 再次终止进程并重开，验证输入恢复、远端每篇恰好一份。结果写入
   `recovery.json` 和分阶段日志，数据库副本随临时目录删除。

Android 的 `force-stop` 会阻止系统任务自动启动，因此后台联网场景使用返回桌面，
杀进程场景明确重开应用。搜索按钮旁的同步图标显示“同步中”“连接成功”“断开连接”
三种状态：`SYNCING` 与 `Pending` 显示“同步中”，`Connected` 与 `Attention`
显示“连接成功”，`Offline` 与 `SignIn` 显示“断开连接”。每次真实进入 `SYNCING` 时，
图标在 800 毫秒内旋转一周，随后静止；同步完成或失败时立即将旋转角度归零，动画不循环。
检查三种图形、状态切换及有限旋转，点击后检查连接、发送队列并刷新笔记；
待同步和冲突通过对应笔记处理，不依赖连接状态文字。

## Fixture 生命周期

`start-fixture.ps1` 在隐藏进程中启动隔离服务。`fixture-control.ps1 status`
只显示实例身份和进程信息；`stop` 关闭指定实例的 Node/Core，保存 Memos/Layout 容器、
语料、会话、状态和索引；`-Resume` 重新绑定原端口并复用数据；`dispose` 销毁指定实例。
连接档案、控制令牌、数据库和配置仅保存在限制访问的 fixture 目录，不能复制到报告。

新建实例使用以下入口，连接档案路径由脚本输出：

```powershell
.\scripts\android\with-device-lock.ps1 -Owner fixture-maintenance -Task fixture-start -Command {
    param($context)
    & "$($context.source_root)\scripts\android\start-fixture.ps1" -SourceRoot $context.source_root
}
```

复用实例时，目录必须为主仓库 `.local/android/fixture-<12位十六进制>/`。
先查询状态，运行中的实例直接使用其连接档案。只有服务进程已停止、原 Memos 容器和
语料实例的 Layout 容器仍存在时，才执行 `-Resume`；容器已经删除的实例需要重新创建。
停止并恢复普通实例的示例：

```powershell
$fixtureRoot = (Resolve-Path -LiteralPath (
    Read-Host '输入要复用的 fixture 目录绝对路径')).Path
.\scripts\android\fixture-control.ps1 status -FixtureRoot $fixtureRoot
.\scripts\android\with-device-lock.ps1 -Owner fixture-maintenance -Task fixture -Command {
    param($context)
    $source = $context.source_root
    & "$source\scripts\android\fixture-control.ps1" stop -FixtureRoot $fixtureRoot
    & "$source\scripts\android\start-fixture.ps1" -SourceRoot $source -Resume -FixtureRoot $fixtureRoot
}
```

语料实例在新建和恢复前都需要设置 `MOERISE_ANDROID_CORPUS`、
`MOERISE_ANDROID_VECTORS` 和匹配的 `MOERISE_ANDROID_DIMENSIONS`，
分别指向笔记 JSON、相同行序的 Float32 小端向量及其维度（默认 1024）。

没有控制端口的旧实例使用 `stop-legacy -NodePid <PID> -ProcessStartTicks <UTC ticks>`。
PID 从指定实例的 `status` 列表读取，启动时间用
`(Get-Process -Id <PID>).StartTime.ToUniversalTime().Ticks.ToString()` 获取。
脚本核对 Node 命令中的实例路径，并只停止该进程和其直接启动的 Core，禁止按进程名批量清理。

真实语料使用每页 100 条、30 秒增量轮询、6 小时全量轮询和 10 秒同步退避。
Core 的 Memos 请求经过 fixture 回环代理，原文复查与同步共用约每秒 1.5 次的请求余量；
代理只转发官方 API，来源公共地址、实例身份和作者范围保持一致。
模型替身串行处理，队列满时返回 429 与 `Retry-After`；Embedding 并发为 1，重试为 3。
恢复时保留模型地址与密钥，避免改变向量空间；模型限流通过正式设置接口更新持久版本，
临时设置会话随后注销。语料与向量摘要必须与原实例一致，恢复流程不会重新导入笔记。

## 性能队列

在同一真机上先运行 `benchmark.ps1 profile`，再运行 `benchmark.ps1 run`。
仅检查不带 Profile 的启动与前台恢复时使用 `benchmark.ps1 startup-without-profile`。
两个入口都在公共锁的 `-Command` 内执行，参数为 `-SourceRoot`、`-Serial` 和 `-Fixture`。
性能入口发现任何已连接模拟器即报错。保持 fixture 数据量、设备温度、语言和电源条件一致，
关联同一提交号的 `device.json`、原始结果、trace 与 `summary.json`。
入口先构建，再显式执行 `adb install -r -t` 和 instrumentation，安装输出写入独立日志。报告要求 50 次启动/首绘或 10 次手势的有效样本和非空 trace；`FirstMs` 指标按原始字段读取，样本不足或缺失指标会失败。

启动基准的后端固定数据集为 100 篇笔记，设备首屏实际缓存 20 篇，
包括一篇 64 KiB 长文及其 10 个附件元数据。登录和缓存准备在计时区间外完成，
缓存就绪后才开始启动测量。下表的 100 条长快照场景需额外分页加载并单独检查。

## 交互和恢复

| 场景 | 操作 | 预期 |
| --- | --- | --- |
| 中文输入法 | 拼音组合输入、候选选择、换行、光标内插和删除；提交前后切换输入法 | 组合文本保持完整，保存和读回一致 |
| 英文与无障碍 | 中英切换、TalkBack、字体放大至 1.3/2 倍 | 控件可达，正文可编辑，弹窗和按钮不被键盘遮挡 |
| 竖屏和后台 | 手机转动方向，输入正文并选附件，回主页、返回、终止进程重开 | 手机应用保持竖屏；已落盘内容和附件绑定恢复，不重复发送 |
| 单条编辑 | 顶部输入未完成正文；依次编辑两条笔记，切换或重建、离页返回 | 同一时间只有一个编辑器；前一条先落盘，重新编辑恢复本机修订，顶部新建隐藏 |
| 保存和取消 | 连续修改正文后立即保存；另一条的本机修订取消后重新编辑 | 保存只提交点击时修订；其他未保存内容不上传，保存或取消后顶部输入恢复 |
| 主页阅读 | 点击卡片正文、时间戳和三点菜单；从回顾、漫步、洞察或观星阅读来源 | 正文和时间戳不导航，无“打开笔记”菜单或独立详情页；来源阅读回主页并定位目标 |
| 系统文件 | 选择图片及普通文件，取消选择；读取过程中切到后台再返回；超限文件 | 有效文件保留，取消无新增，超限有错误且原草稿可用 |
| 附件读取 | Markdown 含管理图片、外部图片、HTML、64 KiB 正文及 10 个附件；打开、移除附件 | 管理图片按可见区域读取，外部图片为链接，HTML 为文本，系统只获得只读 URI |
| 长快照 | 分页加载至 100 篇；编辑 64 KiB 正文及 10 个附件，滚动列表；断开隔离服务后终止进程重开 | 100 条本机快照可恢复，读取快照和长文渲染不阻塞编辑器就绪或输入 |
| 离线 | 断网保存多篇笔记，继续输入未完成正文；终止进程重开，再恢复连接 | 离线笔记显示在时间线，连接恢复后自动上传；未完成输入不提交 |
| 同步图标 | 发起同步、保持 Pending、完成或中断同步，断开连接后重试，再恢复网络 | SYNCING/Pending 显示“同步中”，Connected/Attention 显示“连接成功”，Offline/SignIn 显示“断开连接”；每次真实 SYNCING 在 800 毫秒内旋转一周后静止，完成或失败立即归零，动画不循环；点击触发连接检查、队列发送和笔记刷新 |
| 队列续写 | 从队列卡片三点菜单编辑并追加内容，发送期间再修改正文 | 原键完成后以 PATCH 续传，不重复创建，附件资源与字节一致 |
| 结果不明 | 创建/PATCH 成功后丢弃响应、终止进程、重开应用 | 自动沿用原键同体恢复，不重复创建；外部修改进入比较合并 |
| 来源删除/冲突 | 编辑期间在 Web 删除或修改笔记，回到本机冲突记录，另存为新草稿 | 原始请求与正文仍可读取；新草稿不自动提交 |
| 清理账号 | 准备两个账号的草稿；一个账号有恢复请求，确认删除该账号本机数据 | 退出登录；该账号记录和暂存文件清除，另一个账号内容可恢复 |
| 会话与来源 | 401、显式退出、同账号重登、换服务及重绑实例；注入迟到响应 | 草稿隔离，失效页面隐藏，旧请求不重放到其他身份 |

## 探索

| 功能 | 场景与预期 |
| --- | --- |
| 搜索 | 标签、服务日期、归档和三种搜索模式组合；翻页、更换条件、失效游标；主页原位编辑保持查询及滚动 |
| 回顾 | 跨服务日期、空计划、不可用条目、同批次位置恢复 |
| 漫步 | 随机与指定起点、连续下一条、模型禁用、请求失败重试和结束；路径不重复，离页不继续请求 |
| 洞察 | 预算超限、正常/归档材料、15 秒静默后继续 SSE、停止、断线与后台恢复、追问、历史和回复存笔记；复查不再次调用模型 |
| 观星 | 平移、双指缩放、主题聚焦、取景、全屏、两种氛围、点星和辅助列表；拖动不误点，阅读来源回主页定位，再进观星恢复视口 |
| 星图更新 | 六种顶层状态、旧快照继续显示、正文/摘要变化、frame 更换、202 丢失和重建失败；不自动重发 POST，旧响应不覆盖新正文 |

## 发行与升级

使用已有发行密钥签名，本机签名档案保存在 `signing.json`，由发行入口的
`-SigningFile` 参数读取。签名配置见 [Android 开发](../ANDROID.md#windows-发行入口)。
使用签名发行包，通过系统信任的 HTTPS 来源完成登录、重启、含附件记录、SSE 与观星。
首版从新安装开始，检查完整 schema 1 建库、Secure Cookie、证书校验、文件打开和返回，
以及同一安装内的草稿、冻结请求、语言与登录状态恢复。

后续正式版本从已发行版本准备草稿、冻结请求、语言与登录状态，再使用相同包名、
相同签名且更高 `versionCode` 的发行 APK 覆盖安装。核对上述数据和会话恢复；
数据库结构变化时，新增并验证对应迁移。升级检查以正式发行版本为起点。

至少覆盖 API 26、API 36 以及 16 KiB 页设备。页大小使用 `adb shell getconf PAGESIZE`
记录。静态 APK 对齐检查与设备运行分别记录；16 KiB 设备启动后实际打开数据库、读写
草稿、选择附件并进入观星。检查系统备份和设备迁移不包含凭据、数据库或附件。

性能入口和结果口径见 [Android 开发](../ANDROID.md)。发行设备闭环与性能数据关联同一
源代码提交；性能包的数据与正式账号隔离。
