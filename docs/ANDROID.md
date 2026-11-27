# Android 开发

Android 工程位于 `mobile/`，应用模块为 `androidApp`，纯 Kotlin 逻辑位于 `core/model`、`core/sync` 和 `core/markdown`。Android 包名为 `io.github.fermin214.moerise`，最低系统版本为 Android 10（API 29），目标和编译 SDK 为 36。应用、benchmark 和发行检查共用 `mobile/gradle.properties` 的 `moerise.minSdk`。

## 文件与产物

| 路径 | 职责与版本管理 |
| --- | --- |
| `mobile/androidApp/src/main/`、`src/debug/` | 应用入口、平台集成、资源及开发网络配置，提交源码 |
| `mobile/androidApp/src/main/`、`mobile/core/` | Android 应用及纯 Kotlin 模型、同步和 Markdown 逻辑，提交源码 |
| `mobile/androidApp/src/test/`、`mobile/core/*/src/test/`、`mobile/androidApp/src/androidTest/` | 协议、恢复与设备回归流程，提交测试和虚构契约数据 |
| `mobile/benchmark/`、`mobile/androidApp/src/benchmark/` | 性能 runner 与隔离性能场景，提交源码，发行包不包含性能 Activity |
| `mobile/androidApp/schemas/` | Room 数据库版本契约，提交并保留已发行版本 |
| `mobile/gradle/`、`gradlew*`、`*.gradle.kts` | 可复现构建、依赖版本与摘要校验，提交；Wrapper JAR 是构建入口 |
| `mobile/androidApp/src/main/baselineProfiles/` | 经设备采集、整理后用于发行的文本优化规则，提交；原始采集包放在 `.local/android/` |
| `scripts/android/` | 可重复使用的构建、设备、隔离服务与发行工具，提交；入口见[脚本说明](../scripts/README.md) |
| `docs/android/THIRD_PARTY_NOTICES.md`、`docs/android/licenses/*.txt` | 发行依赖清单和基础许可正文，随依赖更新 |
| `mobile/**/build/`、`.gradle/`、`.kotlin/`、`.cxx/` | 构建、代码生成、报告、缓存与提取的依赖许可，忽略 |
| `.local/android/` | APK、ADB 日志、截图、录屏、Perfetto trace、Profile 原始采集、隔离服务数据与发行归档，忽略 |
| `mobile/local.properties`、`.idea/`、签名档案与密钥 | 机器配置或私密资料，忽略；发行密钥放在仓库外 |

新增文件先归入对应模块；可复用的测试与脚本属于源码。一次性排障脚本、执行记录和设备导出物放在 `.local/android/`。不把整个测试、schema、Profile 源规则或 Gradle 校验目录加入忽略规则。第三方归档中的许可由 `licenses.py` 写入构建报告目录，并随发行许可 ZIP 分发。

## Linux 工具链

需要 Linux x86_64、JDK 17、Android SDK 命令行工具、Python 3.11 以上、curl 和 util-linux。设置 `JAVA_HOME` 和 `ANDROID_HOME`，用 SDK Manager 安装平台、构建工具和 ADB：

```bash
"$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager" 'platforms;android-36' 'build-tools;36.0.0' platform-tools
scripts/android/check.sh
```

未设置 `JAVA_HOME` 和 `ANDROID_HOME` 时，脚本从 `~/.local/share/moerise-android/` 查找工具链；可通过 `MOERISE_ANDROID_TOOLS` 指定工具目录。

Gradle Wrapper 固定为 8.13，并校验发行包 SHA-256。依赖版本见 [版本目录](../mobile/gradle/libs.versions.toml)：Kotlin 2.2.20、Jetpack Compose 1.9.3、AGP 8.11.1、KSP 2.2.20-2.0.4、Room 2.8.3 和 SQLite 2.6.1。

## 检查与 APK

`scripts/android/check.sh` 运行纯 Kotlin 模块测试、Android 单元测试、lint 和 debug APK 构建，并检查非零执行数量。Android 单元测试使用 Robolectric 与系统 SQLite 文件数据库，覆盖关闭重开后草稿、冻结请求、游标和账号隔离。

Room 当前版本为 2（基线 [schema 1](../mobile/androidApp/schemas/io.github.fermin214.moerise.data.LocalDatabase/1.json) 经 `LOCAL_DB_MIGRATION_1_2` 升级），包含账号、草稿、冻结请求、功能恢复记录及本地笔记同步表。正式发行后发生结构变化时递增 Room 版本并新增迁移，保留已发行的 schema 文件。

共享检查覆盖数据库关闭重开、创建与编辑的丢失响应恢复、保存确认事务期间续写，以及原键重放返回外部修改时的冲突合并。队列保存每次点击时的内容快照；后续输入只在本机保存，继续提交的修订在创建确认后绑定远端笔记。

构建默认限制一个 Gradle worker、单 CPU、1.5 GiB JVM 堆与 768 MiB 元空间；不并发执行多个构建。`MOERISE_ANDROID_CPU` 可指定允许使用的 CPU 编号。构建日志和设备产物放在 `.local/android/`。

APK 位于 `mobile/androidApp/build/outputs/apk/debug/androidApp-debug.apk`。开发包名为 `io.github.fermin214.moerise.dev`，桌面名称为 Moerise Dev，可与发行包并存。连接启用 USB 调试的设备后安装：

```bash
source scripts/android/environment.sh
adb devices
adb install -r mobile/androidApp/build/outputs/apk/debug/androidApp-debug.apk
```

发行配置只允许 HTTPS；debug 配置允许 localhost、127.0.0.1 和 IPv6 回环的 HTTP。凭据、数据库和受保护内容排除云备份及设备迁移，持久数据库使用 `noBackupFilesDir`。

## 共享与平台边界

`androidApp` 使用标准 Android 插件、Compose、Coroutines 和 Room，数据库使用系统 SQLite。`core/model`、`core/sync`、`core/markdown` 为纯 Kotlin 模块，不依赖 `android.*`。数据库沿用 `noBackupFilesDir/data/moerise.sqlite3`，Keystore 沿用 `moerise-session-v1`。

主页的颜色、字号、编辑器、笔记卡片和导航侧栏与手机 Web 共用视觉规范。笔记列表在平板上居中显示，阅读区最大宽度为 800 dp。侧栏的日历、导航与标签共用纵向滚动区域。底部固定一行，左侧显示用户图标和用户名，右侧显示齿轮，点击整行打开设置。

笔记卡片时间使用服务时区，统一显示 `YYYY-MM-DD HH:mm`。三点菜单提供编辑、复制原始正文及适用的归档、删除和探索操作。应用不提供独立详情页，来源阅读动作回到主页并定位对应卡片。

主页底部的写作胶囊随页面常驻。归档、回顾和漫步从顶栏「+」新建笔记，已有笔记从三点菜单进入编辑。新建与编辑共用独立底部写作浮层，关闭时保留来源、筛选和浏览位置。取消、点遮罩和系统返回先冻结输入、落盘并清除活动记录，再关闭浮层。保存把冻结修订、待发请求和活动记录的清理一起提交，成功后关闭浮层。归档撤销提示在底部替换胶囊，展示三秒，撤销后胶囊恢复。

`active-note-editor` 按账号记录 `kind`（`new` 或 `edit`）、`draft_id` 和编辑笔记时可用的 `key`。冷启动时，若所选账号无需重新登录且对应草稿仍存在，应用会恢复写作浮层，离线时也可恢复。旧记录通过草稿槽位推断类型。主动关闭或保存入队后清除匹配记录，草稿未保存时只留在本机。

在距屏幕左缘至少 32 dp、且未被交互控件占用的位置横向右拖可拉出侧栏，向左拖可收起。写作浮层中的长正文内部滚动并跟随光标，动作区提供附件、`#` 标签、保存和取消。退到后台会清除焦点并收起键盘。

已同步笔记只识别服务端标签：纯标签行不显示，句中标签原位着色，底部只保留未在句中显示的标签。主页与归档的标签在原页面筛选，回顾、漫步和观星的标签跳转主页筛选。待发笔记从正文识别标签，只展示、不筛选。段落和同一引用块内的单回车保留换行；代码、链接、转义和 Markdown 标题不识别为标签。编辑时实时高亮有效标签，输入前缀可从已缓存或已同步的本机标签中快选，不发网络请求。

## Core 连接与真实服务联调

连接页接受与 `web.public_origin` 相同的 Web 入口，API 位于 `/api/app/v1`。登录密码仅用于登录请求；会话 Cookie 使用 Android Keystore 的 AES-GCM 密钥加密后存入免备份目录，CSRF 只保存在内存。Cookie 限定同一 scheme、host 和端口，不跟随重定向，不启用底层写入自动重试。
会话恢复、连接检查与普通 API 共用 65 秒请求时限，建立连接的时限为 10 秒。
GET 与只读 POST `/search` 遇到复用连接的 EOF 时，
清除该客户端的空闲连接后重连一次，两次请求共用 65 秒时限。各客户端使用独立连接池；
空闲连接在 IO 线程清理；写入发送前先清理空闲连接，传输失败交给调用方处理；已持久化的请求沿用原键和正文恢复。连接诊断日志包含操作类型、资源类别及异常类型链。

登录页使用通铺宣纸、居中青印和衬线题签，语言入口位于右上角。首次登录需要输入服务器地址。已有账号重新登录时沿用保存的地址，点击更换服务器后显示地址输入框。表单未填完整时不可提交，提交期间锁定输入和切换。提交时清除输入焦点、收起键盘并遮蔽密码；失败保留输入，成功后清除密码。

服务来源、实例 ID 和作者资源名共同组成账号隔离键。恢复会话时重新读取身份，身份变化要求登录；非 GET/HEAD 操作携带 Origin 和 CSRF，CSRF 校验失败时仅刷新一次。退出先清除本机身份，再尝试撤销服务端会话；旧响应不能覆盖新登录的 Cookie。

真实服务检查需要 Go 1.27、Node.js 24 及 Docker。Core 观星契约基线为 `062185f`，Memos 固定为 `0.31.0` 及脚本中的镜像摘要。执行：

```bash
scripts/android/live-check.sh
```

脚本构建独立 Core，启动受 CPU 和内存限制的 Memos 容器和内嵌 Web 服务，使用虚构账号检查登录、重开会话、同键创建、实时读回、删除与注销。观星夹具使用本机隔离 Embedding 模型。测试结束清理容器、来源数据和临时凭据。非敏感日志留在 `.local/android/live-*/`。Linux JVM 检查使用与 Android 相同的 OkHttp 引擎。

隔离实例通过 Memos 官方 API 将 `contentLengthLimit` 设为 65,536，用于覆盖 Core 的
64 KiB 内容上限。真实服务的笔记长度还受其 Memos 实例配置约束。

一次性设备联调使用以下命令启动隔离服务：

```bash
MOERISE_ANDROID_FIXTURE_DISPOSE_ON_EXIT=1 scripts/android/start-fixture.sh
```

脚本输出 Web 来源、私有连接档案路径和 `adb reverse` 命令。连接档案包含虚构账号和临时凭据。按 Ctrl+C 停止服务并清理容器、数据卷和本机连接档案。观星六类顶层状态及更新中的固定响应样本位于 `mobile/androidApp/src/test/resources/contracts/map/`。

复用已有观星语料时，设置
`MOERISE_ANDROID_CORPUS` 为笔记 JSON 数组路径、`MOERISE_ANDROID_VECTORS` 为按相同行序排列的
Float32 小端向量文件、`MOERISE_ANDROID_DIMENSIONS` 为向量维度（默认 1024）。
脚本通过 Memos 官方 API 导入正文，向量服务直接返回已有数据，Core 生成真实索引与 UMAP
快照；运行环境需要 `moerise-android-layout:fixture` 镜像。连接档案记录语料数量、维度和
两份输入的 SHA-256。`MOERISE_ANDROID_LAYOUT_IMAGE` 可指定布局镜像，
`MOERISE_ANDROID_FIXTURE_PORT` 指定 Web 端口，`MOERISE_ANDROID_WEB_DIST` 指定 Web 构建目录。

`scripts/android/stars-check.sh` 构建独立布局镜像，启动限制为半个 CPU、1 GiB 内存的
布局 worker、私有模型卷和 32 篇固定笔记。共享 OkHttp 客户端读取真实 UMAP 快照，
检查新增与向量变化的 transform、删除、主动重建和模型文件丢失后的重新拟合。
检查比较旧坐标、星体属性、frame 与正文摘要，并从 worker 模型文件确认 UMAP 类型
和首次拟合数量；布局镜像摘要与结果保存在隔离服务日志目录。结束时清理专用容器、
卷及凭据。该入口与 Gradle 构建串行执行。

## 记录与同步

手机窗口固定竖屏。Activity 重建、切入后台和进程重开时，编辑内容仍通过本机数据库恢复。文件选择结果先到达时，会等原账号的目标草稿恢复后导入。切换账号、需要重新登录或目标草稿恢复失败时，结束这次选择。

已配置账号启动后异步恢复草稿并检查会话。首页显示无图标的暖白写作胶囊，羽化底色隔开滚动文字；空稿显示“今天想到了什么……”，有草稿时显示单行预览。首页时间线在胶囊首帧后读取本机索引，断网时也可直接浏览。归档、回顾和漫步通过顶栏「+」打开写作浮层。首页的归档撤销提示在胶囊原位显示 3 秒。

主页和归档使用 `#F7F6F2` 手卷底色，条目由两侧缩进的细线分隔；回顾和漫步保留 `#FCFBF8` 暖白册页。长笔记连同附件和末尾标签整体折叠，底部时间与操作始终在折叠区外。落款按服务时区显示当日 `HH:mm`、同年 `M月D日` 或 `Nov 24`、跨年 `yyyy年M月D日` 或 `yyyy-MM-dd`；跨零点、前台恢复或服务时区改变时刷新，不重建编辑器或改变列表位置。

写作浮层使用全屏 18% 暖墨遮罩，宣纸笺顶部圆角、底部直角。面板避开状态栏、刘海与键盘；导航栏内边距由面板内容承担，动作栏不落入导航区域。点击遮罩关闭并保留本机草稿。

新建和编辑共用底部写作浮层，退出后保留来源页面、筛选条件和滚动位置。输入在 300 毫秒防抖后落盘，持续输入最多等待约一秒合并落盘。主动关闭浮层、切页和进入后台时立即刷入数据库。进程回收后恢复仍处于打开状态的写作浮层，主动关闭后可从原入口继续写作。

点击保存先在 SQLite 中冻结正文、来源摘要、附件和幂等键，将记录加入同步队列，同时清除该草稿的活动浮层标记。本机提交成功后关闭浮层。离线笔记直接显示在时间线中，连接恢复后串行上传，重开应用也会恢复队列。尚未点击保存的输入内容不自动提交。账号、服务实例与原始请求绑定，重试复用原键原体，来源冲突保留原文并进入合并界面。

队列记录保存稳定的本机时间与已提交修订的快照。编辑队列笔记后再次点击保存，会记住这次修订；原请求确认后使用新的远端来源摘要继续提交。确认期间的后续输入作为本机编辑副本保留，只有再次点击保存才进入发送队列。本机保存时间为 0 的记录不推算日期。

本机新建和入队笔记统一通过 `TimelineNoteCard` 与服务器笔记共同排序展示，仅同步失败或存在冲突时在右上角显示红色感叹号，待同步普通卡片不显示黄色待发角标；确认后沿用同一卡片位置。

顶部工具栏仅在待同步、离线、需要登录或冲突时显示同步图标。主页可下拉刷新，「设置 → 通用」显示上次同步时间并提供「同步」。该页底部的「账号」区读取本机会话，离线和服务端设置读取失败时也可退出登录。前台空闲每 180 秒检查增量，手动操作、保存入队和网络恢复可立即触发。进程内首次进入前台、存在待发记录或距上次成功同步至少 10 秒时，从后台返回也会触发同步。已就绪的时间线只在本地投影版本变化时重读，旧 Core 仍使用远程分页。

本地全量同步就绪后（Room v2 通过 `LOCAL_DB_MIGRATION_1_2` 维护 `notes`、`note_tags`、`sync_state` 和 `cached_snapshots` 表），时间线、标签统计、当月日历、离线详情和关键词筛选直接读取本地索引。编辑在后台确认后，回到前台会更新仍在使用的筛选结果和分页。归档及恢复先保存状态请求并移出来源列表。响应丢失时保留原请求和幂等键供核对重试。提前撤销会持久化，收到归档回执后用最新来源摘要在同一事务内转为恢复请求。

短笔记打开编辑时自动聚焦到末尾纯标签行之前，长笔记从正文顶部开始浏览。原位 Markdown 着色保留正文、标记与输入法组合区。主页、归档和纯标签或日期筛选中的长卡将正文、附件和末尾标签整体折叠；离线待发笔记确认后保留展开状态。图片全屏预览支持双击或双指缩放，可用右下角下载或长按「保存到相册」，原图写入 `Pictures/Moerise/`。超过 8 MiB 或无法解码的图片仍可外部打开。

Android Application 持有同一组会话、数据库和控制器；界面与系统任务共享串行发送入口，取得同步锁后重新判断是否需要同步。已保存队列通过持久 JobScheduler 网络任务恢复，系统停止任务只取消这次同步，原键原体仍可重放。前台空闲时掉线会将下次重试提前至 5 秒内，网络恢复并通过系统校验时立即唤醒。后台任务使用系统退避与执行配额。普通进程回收和重启保留任务，系统“强行停止”后需要重新启动 App 才恢复调度。

`note_editor_ready` 标记从 Activity 创建开始，到本机草稿恢复后首页 `compose_capsule` 首帧可交互结束，同时调用 `reportFullyDrawn`。设备写作流程另行点击胶囊，通过浮层的 `quick_editor` 验证输入就绪。

## 本地设备测试

`mobile/androidApp/src/androidTest/` 的测试在本地设备运行。测试目标使用 `fixture` 变体 `io.github.fermin214.moerise.fixture`，与发行包数据隔离。GitHub Actions 的 Android 检查覆盖 JVM、Lint、APK 构建和真实 Core API。

| 用例 | 运行前提 |
| --- | --- |
| `StarRenderingTest` | 使用 `.fixture` 包和内存 `MockEngine`，无需测试服或连接档案 |
| `ImeLifecycleTest` | 使用 `.fixture` 包，设备必须能访问隔离服务，并在 `/data/local/tmp/moerise-android-fixture.json` 提供 `origin`、`username` 和 `password` |
| `QuickNoteTest` | 使用和 `ImeLifecycleTest` 相同的连接配置。测试账号须预置至少 30 条可编辑的正常笔记。在新隔离实例中可运行 `MOERISE_ANDROID_FIXTURE=/绝对路径/connection.json node scripts/android/seed-benchmark.mjs`，也可通过 API 预置 |

运行 `ImeLifecycleTest` 或 `QuickNoteTest` 前，若更换隔离实例，先用 `adb shell pm clear io.github.fermin214.moerise.fixture` 清除已安装测试包的数据，确保用例进入登录页并读取新档案。用[一次性联调命令](#core-连接与真实服务联调)启动隔离服务后，执行脚本给出的 `adb reverse` 命令，再把连接档案中的三个登录字段写入设备档案。运行完成后删除设备档案和本机的临时副本。

连接单台设备后，可按类运行。下面的命令选择 `StarRenderingTest`：

```bash
mobile/gradlew -p mobile :androidApp:connectedFixtureAndroidTest \
  -Pandroid.testInstrumentationRunnerArguments.class=io.github.fermin214.moerise.StarRenderingTest
```

## 笔记管理与附件

时间线的三点菜单支持打开写作浮层、归档、恢复与删除。来源阅读在主页定位卡片并读取 Core 的实时正文，归档列表位于侧栏浏览导航。编辑草稿按笔记资源名保存，切换编辑目标先落盘前一条。归档与恢复在发送前记录原始请求和幂等键，结果不明时使用原请求核对。归档回执到达后，提前撤销使用回执中的状态和摘要结算，仍处于归档时发起恢复。删除结果不明时通过笔记读取接口复查，资源确实不存在才清理本机记录。

卡片恢复内容后在共享本机锁内登记单个编辑器，网络确认会同步更新正在编辑的笔记。请求途中开始编辑也会收到确认，后续保存沿用对应远端笔记，避免重新创建。

选择文件后复制到免备份目录，完成大小检查、SHA-256 计算和落盘再绑定草稿。文件复制期间禁用服务器保存；绑定时检查账号和草稿 UUID，迟到结果不能写入其他草稿。新增文件最多 10 个，合计不超过 8 MiB；请求以 JSON Base64 提交，编码后的 JSON 不超过 Core 的 12 MiB 限制。发送期间可续写正文，附件变更在请求完成后操作。未确认请求和冲突草稿持有文件引用；没有引用且超过一天的暂存文件在保存后回收。

笔记中的内嵌图片只读取该笔记管理的附件。图片下载和解码最多并行两个任务，内嵌上限为 8 MiB，解码最长边为 1024 像素。超限或解码失败的附件保留系统打开入口，外部图片显示浏览器链接，原始 HTML 作为文本显示。下载按来源与笔记资源校验，写入应用缓存，通过只读 FileProvider URI 交给系统应用。系统打开附件的缓存按 256 MiB 和 24 小时回收。账号切换、退出或会话失效清理受保护下载缓存。会话失效清理中断后，冷启动先完成清理，再开放该账号的缓存读取。

保存相册以 `MediaStore` 的待发布状态隔离尚未写完的图片。提交前核对会话、绑定账号与缓存代际；退出、切换账号、会话失效或中断时移除未发布记录，冷启动恢复清理强制停止留下的记录。已发布图片不随会话或缓存清理删除。

编辑来源冲突时展示提交正文、服务器正文和本机合并文本。附件列表用于确认要移除的远端文件，以及需要重新添加的本机文件；不根据同名判断上传完成。笔记保存成功后，若服务端响应附带 `attachment_receipts`，优先校对本地计算的文件 SHA-256 与幂等回执摘要，匹配即可直接确认而无需重新下载附件字节；缺少回执时再按字节流和摘要确认。

待同步与冲突记录通过时间线三点菜单编辑，远端笔记删除后仍可恢复本机正文。冲突可合并到远端笔记，或另存为使用新 UUID 的主页输入；另存保留原始冲突请求和已有主页输入，附件按显式选择加入，不继承远端移除操作。点击保存后加入同步队列。

在「设置 → 通用 → 账号」选择「退出登录」。普通退出清除会话与受保护下载缓存，保留账号的草稿、待发笔记、冲突副本、暂存附件和 Room 数据。勾选同时清除本地数据时，应用删除该账号的本机记录、索引和文件。如果存在待发笔记、冲突副本或暂存附件，应用列出数量并要求再次确认。清除途中中断后，下次启动先完成本机会话与受保护下载缓存清理，再尝试恢复登录状态，同时继续处理待删除的暂存附件。其他账号的记录及服务器已保存的笔记仍可使用。

## 搜索与日期

搜索入口位于列表顶部。已同步本地索引后，关键词及空查询直接在本机 SQLite 中筛选与按创建时间倒序分页，支持正文、标签、状态和日期；语义与混合检索继续提交 Core 保持服务器相关度顺序，包括混合查询降为关键词后的结果。空查询保留用户选择的模式，更换条件取消旧查询并清除旧游标；游标失效时保留阅读结果，重新搜索从第一页开始。

搜索在主页工具栏展开，通过输入和 IME 搜索操作提交查询；标签与日历筛选通过侧栏访问。首页就绪和增量同步有变更后预热标签与当月日历，侧栏复用统计结果。本地索引就绪后，从 Room 按服务时区的月界查询日历，本机笔记状态或标签变化后刷新计数。首次同步完成前，本机写入会使该账号已保存的筛选快照失效，侧栏在联网时重新读取计数。笔记卡片中的标签可直接应用对应筛选。

侧栏标签在归档页统计归档笔记，其余页面统计正常笔记。日历在所有页面统计正常笔记，点选日期进入主页。从其他页面进入时只应用日期筛选，在主页再次点选同一天可清除日期，保留主页标签与关键词。

月份和笔记时间使用会话提供的服务时区，日期查询直接提交服务端日期字符串。写作浮层保留来源列表的滚动位置，保存确认后刷新已加载的搜索范围，同一张仍匹配的卡片沿用原列表键。退出或会话失效时清理搜索内容。

## 设置

设置分为「通用」「检索与观星」「回顾与推送」「隐私」四个页签。通用含语言、时区和本机同步摘要。检索与观星含向量模型配置、索引进度和星图开关。回顾与推送汇总规则、推送时段、目的地、独立渠道与发送记录。语言和统计开关即时写入本机。

首次启动的欢迎框提供默认关闭的统计开关，安装升级沿用本机选择。登录本人 Core 且实例允许分享时，开启开关才会汇总使用次数、操作结果、等待时间档、客户端与 Core 版本和 Android API 级别。开启满 24 小时后才可发送，每 24 小时最多尝试一次。发送前冻结批次，收到 Core 的 HTTP `2xx` 后清除已发送的计数。重试保留每个窗口的原始版本和 48 小时寿命，进程运行时按窗口到期时间清理，启动与回前台也会清理过期统计。关闭统计会立即停止采集和发送，写入失败时可重试。关闭统计或切换账号会清除未发数据。项目维护者保留每日汇总 30 天。「设置 → 隐私」可随时预览和复制本机诊断摘要，近期异常会附带脱敏信息。

检索与观星中的观星开关与 Core 共用配置，新实例默认关闭。关闭后隐藏导航入口，停止观星读取并清除本机星图视图。重新启用后按服务状态恢复。连接身份携带 `stargazing_enabled`，禁用响应通过 `disabled_reason` 区分观星关闭与 Embedding 关闭。布局服务不可用时会提示检查 `layout` 服务。

服务端设置通过版本化 PATCH 串行保存。凭据编辑窗口冻结候选配置，探测成功后提交同一版本和候选。版本冲突时保留输入并提供重载入口。设置刷新按变化字段更新受影响的视图和缓存。保存、复制和归档结果从底部提示，归档可在 3 秒内撤销。

## 回顾与漫步

导航抽屉提供主页、回顾和漫步入口。回顾读取 `/reviews/daily` 的固定计划，支持叠卡跟手横滑、指示点和下一条按钮切换，页眉空白保留抽屉手势。计划更新时，同一批次保持选择位置，新批次从第一条开始。不可用条目可继续切换，空计划显示空状态。

漫步支持随机开始，以及从正常笔记卡片和回顾条目指定起点。继续请求只提交服务返回的笔记资源名和访问路径；拒绝重复路径、错误成员和不属于账号的数据。同一页面最多一个漫步请求，失败保留已访问路径，重试沿用原请求内容。结束漫步清理本机会话。

漫步开始页将「随机开始」放在单列起点目录上方，窄屏最多四条，宽屏最多六条。优先从本机 `notes` 库读取最多 20 条正常笔记，按清洗后正文至少 12 字、较短正文、纯附件的顺序挑选起点，各组维持时间倒序。起点行右侧显示服务时区日期，上一条笔记在顶部足迹只显示摘要。配置和预览读取失败均可重试。

打开首篇或继续漫步换卡后，阅读区回到顶部。漫步请求在途、可重试或阅读期间，操作栏固定在页面底部。漫步正文、附件与标签下方显示服务时区日期，并可通过「在主页查看」定位原笔记。回顾的标签与落款贴近卡片底边，长文可在卡内完整滚动，落款按经过的完整 24 小时显示「今天」或「N 天前」。展示漫步条目时后台准备 `POST /walks/next`，继续时复用预取结果。切换页面或结束漫步立即取消在途请求。

离开页面或进入后台会取消回顾与漫步读取。回顾在返回时刷新计划；漫步返回后保留结果和重试入口，不自动再次请求。会话失效、退出和账号变化会清理探索内容。向量索引关闭、尚未就绪、起点归档、来源变化和读取中断分别显示对应状态。

## 观星

观星直接使用 Compose Canvas 绘制 Core 发布的 `umap-balanced-v1` 快照，移除旧 `pca-topics-v3` 兼容路径。坐标、点位摘要 `excerpt`、`create_time`、星体属性、主题、子主题与关联来自服务端，客户端校验有限数值、资源唯一性和成员引用。未知算法或缺失契约显示兼容错误并停止自动读取和重建。

星图支持单指平移、双指缩放、主题聚焦、回到全图、工具栏主动触发重新生成星图以及系统全屏。绘制和点击共用坐标变换，在触控范围内选择最近的真实笔记。点星或点选未定位笔记会在图内阅读卡优先显示本地或实时正文，`MapUnplacedNotes` 将创建日期与关闭按钮顶部对齐并用 `notePresentation` 解析底部标签。手机使用底部阅读卡，宽屏使用右侧阅读区。系统返回先关闭阅读卡并保留镜头，再退出全屏或离开页面。

辉光、密度柔光和主题文字按快照缓存，密度柔光只由真实语义点生成。单个 Canvas 绘制星点，按视口裁剪；静止页面不运行持续动画。近景与悬停摘要优先读取点位 `excerpt` 和本机 `notes` 表而无需额外网络往返。返回星图优先从 `cached_snapshots` 恢复上次快照、视口、选择与阅读位置。

星点摘要沿用 Web 处理：移除 Markdown 图片，保留链接文字，去除标题、粗体与引用标记，压缩空白后按 Unicode 码点取前 100 字符。正文为空时使用首个附件文件名。

同一坐标框架更新保留视口，新框架重新取景；来源摘要变化会清除旧摘要和阅读结果，迟到响应不能覆盖新选择。更新失败和网络中断保留可读快照。稳定页面每 60 秒检查，生成或重建期间每 3 秒检查，失败后至少退避 10 秒；离页和后台停止读取。

浏览和重试均通过 GET 读取已发布快照。后台布局由 Core 根据索引变化更新。

Android 将响应体积、解析、首绘、绘制和摘要请求计数写入 `MoeriseMap` 日志及 `moerise_map_*` Trace 计数器，日志仅含指标和布局版本。设备测量可通过 `adb logcat -s MoeriseMap:I` 收集，结合系统帧耗时和 Perfetto 分析。

## 性能测量与 Baseline Profile

`mobile/benchmark/` 是独立的 AndroidX Macrobenchmark 测试模块。目标包为
`io.github.fermin214.moerise.benchmark`：`benchmark` 构建启用 R8 和资源裁剪，
`profile` 构建保留原始方法名用于采集，二者均关闭应用调试、使用测试签名并只对回环
地址开放 HTTP。固定星图 Activity 只包含在这两个构建中。
测试 runner 使用相同的调试签名，Perfetto tracing 三个组件固定为 1.0.1；应用包与 runner
均检查签名、ZIP 和 64 位 native 库的 16 KiB 对齐。
runner 的 Trace Processor 固定为官方 v59.1，`trace-processor.py` 校验各 ABI 的 SHA-256 并下载到忽略的构建目录。该版本可识别 Android 16 trace 中的 RenderThread 线程名；程序仅进入测试 runner。

在 Linux 或 WSL2 中通过 USB 连接固定 arm64 真机，确认 `adb devices` 能列出设备，并启动隔离服务后执行：

```bash
export ANDROID_SERIAL=设备序列号
export MOERISE_ANDROID_FIXTURE=/绝对路径/connection.json
scripts/android/benchmark.sh profile
scripts/android/benchmark.sh run
```

脚本构建 APK 后通过 `benchmark-device.py` 执行带安装日志的 `adb install -r -t` 和 instrumentation，清空性能包的数据，通过真实 Core/Memos 准备后端固定数据集 100 篇笔记。
启动首屏实际缓存 20 篇，其中包含一篇 64 KiB 长文及其 10 个附件元数据；
登录和首屏缓存准备在计时区间外完成，之后测量冷启动与后台返回。
100 条本机快照的长列表场景通过额外分页加载检查。
Profile 采集使用 API 33 及以上设备，
将设备生成的规则写入 `androidApp/src/main/baselineProfiles/` 下的
`baseline-prof.txt` 和 `startup-prof.txt`；
启动规则与星图规则分别采集；Baseline 规则合并两组路径，Startup 规则只保留启动路径。AGP 在构建时转写。更新规则后提交源文件，再构建
发行包。Profile 为可选优化；同时存在两份非空规则时，发行脚本检查 APK 内的编译产物。
`benchmark.sh startup-without-profile` 只测量无 Profile 的启动与前台恢复，无需预先生成规则。
设备性能测试的最低 API 取自 [`moerise.minSdk`](../mobile/gradle.properties)，后续规划见[路线图](ROADMAP.md#1-android-客户端演进)。

| 场景 | 采样与输出 |
| --- | --- |
| 冷启动 | 无 Profile 与使用 Profile 各 50 次，记录 TTID、TTFD、`note_editor_ready` |
| 后台返回 | 同进程 HOT 启动，两种编译模式各 50 次，记录 `note_foreground_ready` |
| 星图首绘 | 1,000/5,000 点与两种氛围共四组，每组 50 次，记录解析、完整首绘和系统帧耗时 |
| 星图手势 | 四组场景分别执行平移、双指缩放、摘要到达和快照刷新，每组 10 次 |

星图场景复用共享 `StarCanvas`，使用确定性的坐标、星体属性、主题与关联；计时从
Activity 创建开始，包含固定快照生成、JSON 解析和完整首绘，摄影氛围包含密度柔光生成。网络和 Core 布局耗时通过真实观星
流程另行记录。场景名、编译模式、响应体积、JSON、系统 trace、APK SHA-256、提交号、
设备、页大小、电池和温度信息保存在 `.local/android/benchmark-*`。
`StarSprites` 按配色将星核与径向光晕缓存到同一张纹理，逐星复用，减少渐变着色器切换。
星核与光晕分别应用透明度，通过浮点变换保持平移和缩放连续；特殊星光芒和选中环单独绘制。
`benchmark-report.py` 从原始 `benchmarkData.json` 计算每项指标的 P50/P95，
写入 `summary.json`。可用 `benchmark.sh startup` 或 `benchmark.sh stars` 单独测量。
Core 请求累计数写入 `MoeriseStartup` 日志与 `moerise_network_requests` Trace 计数器，
可按启动区间区分会话恢复和首帧后的列表请求。

## 发行

发行 ABI 为 `arm64-v8a`、`x86_64`。构建、R8、依赖许可检查和静态
16 KiB 对齐检查共用以下入口：

```bash
scripts/android/release.sh --unsigned
```

签名时设置 `MOERISE_ANDROID_KEYSTORE`、`MOERISE_ANDROID_KEY_ALIAS`、
`MOERISE_ANDROID_STORE_PASSWORD` 和 `MOERISE_ANDROID_KEY_PASSWORD`，然后运行
`scripts/android/release.sh --sign`。密码通过环境变量传给 `apksigner`，密钥放在
私有目录或 CI Secret 中。入口要求 Git 工作区干净，产物归档到
`.local/android/releases/v<版本>-code<versionCode>-<提交号前七位>/`，包含 APK、SHA-256、构建信息、
安装说明和第三方声明；R8 mapping 随构建保存。

`verify-apk.py` 检查包名、版本、发行调试标志、签名、ZIP 和 native ELF 的
16 KiB 对齐，并拒绝打包 SQLite 原生库。`Checks` 同时运行发行 Lint，并构建 `benchmark` 与 `profile` APK。

运行时依赖来自 `:androidApp:exportReleaseDependencies`，第三方许可清单见
[Android 第三方许可](android/THIRD_PARTY_NOTICES.md)。更新依赖后重新导出清单并执行
`python3 scripts/android/licenses.py`；CI 使用 `--check` 比较。

Android 与 Linux 联合发布在同一个 `v<版本>` Release。合并后 `Main CI` 从 GitHub Actions Secrets 读取
`ANDROID_SIGNING_KEYSTORE`（Base64 keystore）、`ANDROID_KEY_ALIAS`、
`ANDROID_STORE_PASSWORD` 和 `ANDROID_KEY_PASSWORD`。正式 APK 的 `versionCode` 必须高于 `v0.3.0`，签名证书必须与该版本一致。手动运行 `Android release` 时使用临时密钥签名，并验证 APK 与发布附件。

发布前使用当次 `Main CI` 的正式签名 APK 按[发版前真机检查](android/RELEASE-CHECKLIST.md)验证覆盖升级和主要交互。`Publish release` 核对 APK 摘要、构建提交和产物完整性，再用原产物发布。具体步骤见[发布](DEVELOPMENT.md#发布)，随 APK 分发的说明见[安装与升级](android/INSTALL.md)。
