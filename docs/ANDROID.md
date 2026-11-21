# Android 开发

Android 工程位于 `mobile/`，应用模块为 `androidApp`，共享界面、协议与持久化模块为 `shared`。Android 包名为 `io.github.fermin214.moerise`，最低系统版本为 Android 10（API 29），目标和编译 SDK 为 36。应用、共享库、benchmark 和发行检查共用 `mobile/gradle.properties` 的 `moerise.minSdk`。

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

需要 Linux x86_64、Python 3.11 以上、curl 和 util-linux。安装脚本下载 Temurin JDK 17.0.21+9 与 Android 官方命令行工具，核验固定 SHA-256，然后安装 SDK 平台和构建工具：

```bash
scripts/android/setup.sh
scripts/android/check.sh
```

工具默认位于 `~/.local/share/moerise-android/`；可通过 `MOERISE_ANDROID_TOOLS` 改变目录。已有工具链时设置 `JAVA_HOME` 和 `ANDROID_HOME`，后者需包含 `platforms;android-36`、`build-tools;36.0.0` 与 `platform-tools`。

Gradle Wrapper 固定为 8.13，并校验发行包 SHA-256。依赖版本见 [版本目录](../mobile/gradle/libs.versions.toml)：Kotlin 2.2.20、Compose Multiplatform 1.9.1、AGP 8.11.1、KSP 2.2.20-2.0.4、Room 2.8.3 和 SQLite 2.6.1。

## 检查与 APK

`scripts/android/check.sh` 运行纯 Kotlin 模块测试、Android 单元测试、lint 和 debug APK 构建，并检查非零执行数量。Android 单元测试使用 Robolectric 与系统 SQLite 文件数据库，覆盖关闭重开后草稿、冻结请求、游标和账号隔离。

Android 0.1.0 是首次发行，Room 版本为 1，以完整的 [schema 1](../mobile/androidApp/schemas/io.github.fermin214.moerise.data.LocalDatabase/1.json) 建立初始结构，包含账号、草稿、冻结请求、快照和功能恢复记录。开发期 APK 与数据库不提供兼容迁移，也不作为首版升级验证的起点；首版设备流程使用新安装数据。正式发行后发生结构变化时，递增 Room 版本并新增迁移，保留已发行的 schema。

共享检查覆盖数据库关闭重开、创建与编辑的丢失响应恢复、保存确认事务期间续写，以及原键重放返回外部修改时的冲突合并。队列保存每次点击时的内容快照；后续输入只在本机保存，继续提交的修订在创建确认后绑定远端笔记。洞察使用真实 Room 文件检查重开后保留新建请求、生成请求和部分回复，读取恢复不会自动生成。

构建默认限制一个 Gradle worker、单 CPU、1.5 GiB JVM 堆与 768 MiB 元空间；不并发执行多个构建。`MOERISE_ANDROID_CPU` 可指定允许使用的 CPU 编号。构建日志和设备产物放在 `.local/android/`。

APK 位于 `mobile/androidApp/build/outputs/apk/debug/androidApp-debug.apk`。开发包名为 `io.github.fermin214.moerise.dev`，桌面名称为 Moerise Dev，可与发行包并存。连接启用 USB 调试的设备后安装：

```bash
source scripts/android/environment.sh
adb devices
adb install -r mobile/androidApp/build/outputs/apk/debug/androidApp-debug.apk
```

提交后执行 `scripts/android/archive-debug.sh M2` 可按里程碑和提交号归档 debug APK，检查签名、APK 中 native 库的 16 KiB 对齐，以及 64 位 ELF LOAD 段对齐，并输出 SHA-256。归档入口要求已跟踪文件没有改动。

发行配置只允许 HTTPS；debug 配置允许 localhost、127.0.0.1 和 IPv6 回环的 HTTP。凭据、数据库和受保护内容排除云备份及设备迁移，持久数据库使用 `noBackupFilesDir`。

## 共享与平台边界

`androidApp` 使用标准 Android 插件、Compose、Coroutines 和 Room，数据库使用系统 SQLite。`core/model`、`core/sync`、`core/markdown` 为纯 Kotlin 模块，不依赖 `android.*`。数据库沿用 `noBackupFilesDir/data/moerise.sqlite3`，Keystore 沿用 `moerise-session-v1`。

主页的颜色、字号、编辑器、笔记卡片和导航侧栏与手机 Web 共用视觉规范。笔记列表在平板上居中显示，阅读区最大宽度为 800 dp。侧栏的日历、导航与标签共用一个纵向滚动区域；设置与账号占用独立的底部空间，滚动内容不会被底部控件遮挡。

笔记卡片时间使用服务时区，统一显示 `YYYY-MM-DD HH:mm`。正文和时间戳点击均保持在主页，三点菜单提供编辑、归档、删除和探索操作。应用不提供“打开笔记”按钮或独立详情页，来源阅读动作回到主页并定位对应卡片。

同一时间只编辑一条笔记。编辑已有笔记时隐藏顶部新建输入框，未提交的新建内容保存在本机；保存或取消编辑后恢复顶部输入。切换到另一条笔记前先完成前一条输入的本机落盘，重新编辑时恢复对应内容。切页和 Activity 重建恢复单个编辑器及其输入，未点击保存的修订不上传。

编辑目标通过按账号隔离的 `active-note-editor` 功能记录持久保存，记录笔记键与草稿 ID；进程重开时恢复该目标，保存或取消后清空记录中的编辑目标。顶部新建草稿与各笔记的本机修订分别落盘，界面只显示一个编辑器。

从页面横向 25%–75% 的中部向右滑动呼出侧栏，屏幕两侧留给系统返回；输入器、选字和地图拖动不触发侧栏。

笔记的原生 `#标签` 集中显示为去重胶囊，正文省去对应的标签标记。`# 标题`、`## 标题` 按 Markdown 标题层级显示；代码、链接与转义标记保留字面内容。编辑和保存使用原始 Markdown。

## Core 连接与真实服务联调

连接页接受与 `web.public_origin` 相同的 Web 入口，API 位于 `/api/app/v1`。登录密码仅用于登录请求；会话 Cookie 使用 Android Keystore 的 AES-GCM 密钥加密后存入免备份目录，CSRF 只保存在内存。Cookie 限定同一 scheme、host 和端口，不跟随重定向，不启用底层写入自动重试。
会话恢复、连接检查与普通 API 共用 65 秒请求时限，建立连接的时限为 10 秒。
GET 与只读 POST `/search`、`/chat-materials/resolve` 遇到复用连接的 EOF 时，
清除该客户端的空闲连接后重连一次，两次请求共用 65 秒时限。各客户端使用独立连接池；
空闲连接在 IO 线程清理；写入发送前先清理空闲连接，传输失败交给调用方处理；已持久化的请求沿用原键和正文恢复。连接诊断日志包含操作类型、资源类别及异常类型链。

首次登录需要输入服务器地址。已有账号重新登录时沿用保存的地址，点击更换服务器后显示地址输入框；修改地址清除密码。提交登录和打开侧栏时清除输入焦点并收起键盘。

服务来源、实例 ID 和作者资源名共同组成账号隔离键。恢复会话时重新读取身份，身份变化要求登录；非 GET/HEAD 操作携带 Origin 和 CSRF，CSRF 校验失败时仅刷新一次。退出先清除本机身份，再尝试撤销服务端会话；旧响应不能覆盖新登录的 Cookie。

真实服务检查需要 Go 1.27、Node.js 24 及 Docker。Core 观星契约基线为 `062185f`，Memos 固定为 `0.31.0` 及脚本中的镜像摘要。执行：

```bash
scripts/android/live-check.sh
```

脚本构建独立 Core，启动受 CPU 和内存限制的 Memos 容器、Web 代理和本机可控假模型，使用虚构账号检查登录、重开会话、同键创建、实时读回、删除与注销，以及洞察的生成、重放、异常结束、停止和回复存笔记。假模型只接受隔离服务的临时密钥，不连接外部模型。测试结束清理容器、来源数据和临时凭据；非敏感日志留在 `.local/android/live-*/`。Linux JVM 检查使用与 Android 相同的 OkHttp 引擎；Keystore、设备生命周期和触控通过 Android 设备流程检查。

隔离实例通过 Memos 官方 API 将 `contentLengthLimit` 设为 65,536，用于覆盖 Core 的
64 KiB 内容上限。真实服务的笔记长度还受其 Memos 实例配置约束。

单独联调设备时运行 `scripts/android/start-fixture.sh`。脚本输出 Web 来源、私有连接档案路径和 `adb reverse` 命令；连接档案只用于隔离环境，包含虚构账号和临时凭据。按 Ctrl+C 清理服务与来源数据。观星六类顶层状态及更新中的固定响应样本位于 `mobile/androidApp/src/test/resources/contracts/map/`。

Windows 通过公共锁调用 `scripts/android/start-fixture.ps1`，命令见[设备检查](android/DEVICE-CHECKS.md#fixture-生命周期)。复用已有观星语料时，设置
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

## 记录、同步与设备流程

手机窗口固定竖屏。Activity 重建、切入后台和进程重开时，编辑内容仍通过本机数据库恢复。
自动化按输入法、编辑、搜索、归档删除、洞察和离线恢复分组，集中结果与定向重跑见
[设备检查](android/DEVICE-CHECKS.md#集中检查与定向重跑)。

已配置账号启动后异步恢复输入内容和检查会话；编辑器不等待网络，时间线读取安排在编辑器首帧之后。空正文显示浅色提示“今天想到了什么……”。输入在 300 毫秒防抖后落盘，持续输入最多等待约一秒合并落盘；切页和进入后台时立即刷入数据库。

点击保存先在 SQLite 中冻结正文、来源摘要、附件和幂等键，将记录加入同步队列，并腾出输入框。离线笔记直接显示在时间线中；连接恢复后串行上传，重开应用也会恢复队列。尚未点击保存的输入内容不自动提交。账号、服务实例与原始请求绑定，重试复用原键原体；响应丢失不创建重复笔记，来源冲突保留原文并进入合并界面。

队列记录保存稳定的本机时间与已提交修订的快照。编辑队列笔记后再次点击保存，会记住这次修订；原请求确认后使用新的远端来源摘要继续提交。确认期间的后续输入作为本机编辑副本保留，只有再次点击保存才进入发送队列。本机保存时间为 0 的记录不推算日期。

本机新建和入队笔记统一通过 `TimelineNoteCard` 与服务器笔记共同排序展示，仅同步失败或存在冲突时在右上角显示红色感叹号，待同步普通卡片不显示黄色待发角标；确认后沿用同一卡片位置。

顶部工具栏的线性云朵图标仅在同步中、待同步、离线、需要登录或冲突时显示；已连接且无待办状态时隐藏，手动同步入口移至侧栏底部账户区与列表下拉刷新。点击非连接状态的图标会检查连接、发送队列并刷新，登录失效时进入登录页。前台连接正常时按节奏轮询增量笔记变更（`/api/app/v1/sync/notes`），从后台返回立即恢复检查。

本地全量同步就绪后（Room v2 通过 `LOCAL_DB_MIGRATION_1_2` 维护 `notes`、`note_tags`、`sync_state` 和 `cached_snapshots` 表），时间线、标签统计、当月日历、离线详情和关键词筛选直接读取本地索引，标签点击和刷新原地切换而不清空页面。漫步和洞察会话优先复用本地快照，离线也可快速还原已有对话或预览；编辑打开已有笔记时默认将光标停在正文末尾、`#标签` 之前。

Android Application 持有同一组会话、数据库和控制器；界面与系统任务共享串行发送入口。已保存队列通过持久 JobScheduler 网络任务恢复，系统停止任务只取消这次同步，原键原体仍可重放。网络回调在前台立即唤醒检查；后台任务使用系统退避与执行配额。普通进程回收和重启保留任务，系统“强行停止”后需要重新启动 App 才恢复调度。

`note_editor_ready` 标记从 Activity 创建开始，到本机草稿恢复后编辑器完成首个可输入帧结束，同时调用 `reportFullyDrawn`。此标记用于设备启动测量。

设备验收使用独立包 `io.github.fermin214.moerise.fixture`，数据与普通安装隔离。脚本会清空该验收包的数据；连接档案在设备测试后删除。隔离服务运行时执行：

```bash
export ANDROID_SERIAL=设备序列号
export MOERISE_ANDROID_FIXTURE=/绝对路径/connection.json
scripts/android/acceptance.sh
```

设备流程覆盖登录、输入、Activity 重建、保存与时间线读回，结果写入 `.local/android/device-acceptance.log`。输入法、进程终止和后台恢复分别通过设备操作及持久化恢复检查补充。

## 笔记管理与附件

时间线的三个点菜单支持就地编辑、归档、恢复与删除；来源阅读在主页定位卡片并读取 Core 的实时正文，归档列表位于账号菜单。编辑内容按笔记资源名保存在本机，切换编辑目标先落盘前一条，保存或取消后恢复顶部新建输入。归档和恢复在发送前记录原始请求和幂等键；删除结果不明时通过笔记读取接口复查，资源确实不存在才清理本机记录。

卡片恢复内容后在共享本机锁内登记单个编辑器，网络确认会同步更新正在编辑的笔记。请求途中开始编辑也会收到确认，后续保存沿用对应远端笔记，避免重新创建。

选择文件后复制到免备份目录，完成大小检查、SHA-256 计算和落盘再绑定草稿。文件复制期间禁用服务器保存；绑定时检查账号和草稿 UUID，迟到结果不能写入其他草稿。新增文件最多 10 个，合计不超过 8 MiB；请求以 JSON Base64 提交，编码后的 JSON 不超过 Core 的 12 MiB 限制。发送期间可续写正文，附件变更在请求完成后操作。未确认请求和冲突草稿持有文件引用；没有引用且超过一天的暂存文件在保存后回收。

笔记中的图片只读取该笔记管理的附件，可见时下载并持有位图，离开视口释放。图片下载和解码最多并行两个任务，图片内容上限为 8 MiB，解码最长边为 1024 像素。外部图片显示链接，由系统浏览器打开；原始 HTML 作为文本显示。下载按来源与笔记资源校验，写入应用缓存，通过只读 FileProvider URI 交给系统应用。系统打开附件的缓存按 256 MiB 和 24 小时回收；账号切换、退出或会话失效清理受保护下载缓存。

编辑来源冲突时展示提交正文、服务器正文和本机合并文本。附件列表用于确认要移除的远端文件，以及需要重新添加的本机文件；不根据同名判断上传完成。笔记保存成功后，若服务端响应附带 `attachment_receipts`，优先校对本地计算的文件 SHA-256 与幂等回执摘要，匹配即可直接确认而无需重新下载附件字节；缺少回执时再按字节流和摘要确认。

待同步与冲突记录通过时间线三点菜单编辑，远端笔记删除后仍可恢复本机正文。冲突可合并到远端笔记，或另存为使用新 UUID 的主页输入；另存保留原始冲突请求和已有主页输入，附件按显式选择加入，不继承远端移除操作。点击保存后加入同步队列。

在侧栏的账号菜单选择“删除本机账号数据”。确认后，应用停止该账号的任务，等待本机写入结束，删除草稿、待发请求、功能恢复记录、快照和暂存附件，再退出登录。其他账号的本机记录和已存入 Memos 的笔记不受影响。

## 搜索与日期

搜索入口位于列表顶部。已同步本地索引后，关键词及空查询直接在本机 SQLite 中筛选与按创建时间倒序分页，支持正文、标签、状态和日期；语义与混合检索继续提交 Core 保持服务器相关度顺序，包括混合查询降为关键词后的结果。空查询保留用户选择的模式，更换条件取消旧查询并清除旧游标；游标失效时保留阅读结果，重新搜索从第一页开始。

搜索在主页工具栏展开，通过输入和 IME 搜索操作提交查询；标签与日历筛选通过侧栏访问。标签与日历在本地索引就绪后直接从 Room 实时统计，切换标签原地过滤而不空白闪烁。笔记卡片中的标签可直接应用对应筛选。

月份和笔记时间使用会话提供的服务时区，日期查询直接提交服务端日期字符串。搜索结果和列表的滚动位置保留在账号内，原位编辑不跳转独立页面；退出或会话失效清理搜索内容。

## 设置

设置分为「通用」「AI」「回顾与推送」「隐私」四个标签页，去掉多余页签的折叠层级：通用页含语言、时区、`stargazing.enabled`、`stargazing.llm_naming`、每日 03:00 更新提示及“重新生成星图”按钮；AI 页集中大模型、Embedding 诊断与默认洞察 Prompt；回顾与推送页汇总回顾规则、消息渠道与发送记录，并将保存操作条固定在渠道设置之后、发送记录之前；隐私页独立展示本地遥测与隐私开关而不附带表单保存条。语言支持简体中文、英语和跟随系统。时区保存后同步更新本机连接档案、卡片时间与日历，并刷新受影响的列表和探索配置。

首次启动的欢迎框提供默认关闭的统计开关，安装升级沿用本机选择。登录本人 Core 且实例允许分享时，开启开关才会汇总使用次数、操作结果、等待时间档、客户端与 Core 版本和 Android API 级别。本机最多保留 48 小时，每 24 小时在前台最多发送一批，关闭统计会清除未发数据。项目维护者保留每日汇总 90 天。可在「设置 → 隐私」调整选择、预览并删减本机诊断摘要，或打开项目 Issues 反馈。

通用设置中的观星开关与 Core 共用配置。关闭后隐藏导航入口，停止观星读取并清除本机星图视图；重新启用后按服务状态恢复。连接身份携带 `stargazing_enabled`，禁用响应通过 `disabled_reason` 区分观星关闭与 Embedding 关闭。

设置通过版本化 PATCH 保存。发送时冻结改动，保存期间继续编辑的字段按修订合并，版本冲突保留输入并提供重新加载入口。密钥采用保持、替换和清除操作，只在编辑内存中保存，离页时清除；模型检查使用已保存的配置，配置与账号变化会取消旧检查及轮询。

## 回顾与漫步

导航抽屉提供主页、回顾和漫步入口。回顾读取 `/reviews/daily` 的固定计划，使用叠卡、圆点与下一条切换，漫步和洞察操作位于卡片外。计划更新时，同一批次保持选择位置，新批次从第一条开始。不可用条目可继续切换，空计划显示空状态；来源阅读回到主页定位笔记，再次进入回顾恢复原条目和阅读位置。

漫步支持随机开始，以及从正常笔记卡片和回顾条目指定起点。继续请求只提交服务返回的笔记资源名和访问路径；拒绝重复路径、错误成员和不属于账号的数据。同一页面最多一个漫步请求，失败保留已访问路径，重试沿用原请求内容。结束漫步清理本机会话。

漫步初始页面优先从本地 `notes` 库抽取或读取六篇正常笔记作为浮动预览，并读取模型配置；配置和预览读取失败均可重试。开启预取时会在展示当前条目的同时后台准备下一篇 `POST /walks/next`，点击「下一条」直接消费预取结果；切换页面或结束漫步立即取消在途请求。向量配置入口打开 App 设置。

离开页面或进入后台会取消回顾与漫步读取。回顾在返回时刷新计划；漫步返回后保留结果和重试入口，不自动再次请求。会话失效、退出和账号变化会清理探索内容。向量索引关闭、尚未就绪、起点归档、来源变化和读取中断分别显示对应状态。

## 洞察

打开洞察后可添加正常和归档笔记，也可从笔记卡片、回顾和漫步直接加入材料。日期按服务时区选择，点「应用日期」后逐页勾选整段范围。多个标签取并集。「查看已选」可移除筛选范围外的材料，也可打开原文。点「应用所选」或「清空材料」后替换会话材料，关闭弹窗会放弃修改。超过 1,000 条时可缩小范围或转为逐条勾选。材料以 Unicode 码点计数，上限 20,000 字。提交会同时核对笔记的来源摘要，只有发生变化的笔记需要重选。旧版 Core 支持逐条选材。

新建、材料变更、生成、删除和保存回复在发送前持久记录原始请求。新建使用固定 `request_id`，生成使用 `request_id`、会话版本和语言；请求结果不明时保留原体。材料更新通过读取会话版本与材料引用判断是否成功；删除生成中的会话先确认停止终态，再按会话版本删除。保存回复使用服务端消息版本，成功后先记录笔记资源名，后续读取失败也不会重复保存。

生成使用 POST SSE，发送前清理该客户端的空闲连接。传输中断后，用户可以通过“检查回复”确认生成结果，避免重复调用模型。单次预算为 135 秒，连接预算为 10 秒。解析器按完整行解码 UTF-8，识别 CRLF、多行数据、半帧、错误帧及异常结束；只有合法 `complete` 帧表示成功。普通 API 请求维持 65 秒预算，GET 与只读 POST `/search`、`/chat-materials/resolve` 遇到 EOF 时清除空闲连接并重连一次，两个请求共用时限；写操作、HTTP 错误和其他网络错误不自动重试。流式 Markdown 通过 `splitMarkdownBlocks` 拆分已完成的块配合 `key` 与 `rememberMarkdownState(..., retainState = true)`、`markdownAnimations(animateTextSize = { this })` 缓存前序段落，不拆散未闭合围栏代码块；部分回复异步节拍落盘且历史会话索引保存在 `cached_snapshots`。

离页和进入后台会取消流式读取。再次打开洞察时先显示本地保存的会话与消息再后台刷新状态，生成中的会话每 3 秒读取状态。断线后可点「检查回复」核对同一请求，再决定是否重新生成。阅读上文时可点击底部居中的白色圆形向下箭头按钮平滑滚回最新。生成结束后可从更多菜单修改材料，后续提问使用新版本。

「更多 → 保存为笔记」默认选择最近一条完整回答，可另选提问、其他回答，以及服务端已保存的主动停止回复。含不完整回答的保存需要再次确认。所选消息按对话顺序写入一篇笔记，上限为 65,536 个 UTF-8 字节。连接中断后可用原操作键重试。删除对话会保留已存入 Memos 的笔记。

账号恢复材料位于 Room 的 `feature_states`，快照与会话索引缓存位于 `cached_snapshots`，独立于可清理的本地笔记表；退出和会话失效清除界面内容，重新登录后可读取同一账号的恢复材料。

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

连接固定 arm64 真机和隔离服务后执行：

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
设备性能测试的最低 API 取自 [`moerise.minSdk`](../mobile/gradle.properties)，后续性能工作见[路线图](ROADMAP.md#112-android-010-后续工作)。

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

### Windows 性能入口

Windows 使用 PowerShell 7.2 以上、JDK 17、Android API 36、Build Tools 36.0.0、
Platform-Tools 和 Node.js 24 以上；性能汇总另需 Python 3.11 以上。工具路径按下述规则解析，
在仓库根目录输入设备序列号和隔离连接档案路径后执行：

```powershell
$env:ANDROID_SERIAL = Read-Host '输入真机设备序列号'
$env:MOERISE_ANDROID_FIXTURE = (Resolve-Path -LiteralPath (
    Read-Host '输入隔离实例 connection.json 的绝对路径')).Path
.\scripts\android\with-device-lock.ps1 -Owner android-performance -Task profile -Command {
    param($context)
    & "$($context.source_root)\scripts\android\benchmark.ps1" profile -SourceRoot $context.source_root
}
.\scripts\android\with-device-lock.ps1 -Owner android-performance -Task benchmark -Command {
    param($context)
    & "$($context.source_root)\scripts\android\benchmark.ps1" run -SourceRoot $context.source_root
}
```

`profile` 采集并合并设备生成的 Baseline 与 Startup Profile；`run` 测量启动和星图，
`startup` 仅测启动，`stars` 仅测星图。每次调用只启动一次 Gradle，限制单 worker、
单 JVM 处理器，关闭并行构建与常驻进程。所有 Windows 构建和设备操作通过
`with-device-lock.ps1` 共用主仓库的公共锁，子入口要求有效的锁令牌。Profile 采集至少需要 API 33，测量至少需要
API 29。带 Profile 的测量要求已有非空的 `baseline-prof.txt`；`startup-without-profile` 只执行两组不带 Profile 的启动与前台恢复场景，无需源规则。完整对比要求两组使用同一 APK、设备、缓存数据和工具版本。

设备和连接档案也可通过 `-Serial`、`-Fixture` 参数传入。档案必须来自
`start-fixture.ps1` 或 `start-fixture.sh` 启动的隔离服务，包含 HTTP 回环来源、
`android-owner` 账号与临时密码。
服务在服务器运行时，先建立电脑到服务器的 SSH 本地端口转发并保持连接，端口与
档案来源一致；脚本负责该设备的 `adb reverse`。数据准备复用 `seed-benchmark.mjs`，
通过真实 Core/Memos 创建并检查后端固定数据集 100 篇笔记，同时核对启动首屏的
20 条缓存包含 64 KiB 长文及其 10 个附件元数据。性能包登录后等待该缓存就绪再开始测量。

Windows 的 `environment.ps1` 固定使用主仓库 `.local/android/tools/android-sdk`、
`.local/android/gradle-home` 和 `.local/android/project-cache`。
JDK 优先使用 `JAVA_HOME`，随后依次查找
`C:\Program Files\Microsoft\jdk-17.0.12.7-hotspot`、
`.local/android/tools/jdk-17.0.21+9` 和 PATH，并校验完整 JDK 17。
性能脚本的 `MOERISE_ANDROID_ADB`、`MOERISE_ANDROID_NODE`、
`MOERISE_ANDROID_PYTHON` 可分别指定程序的完整路径，支持路径中含空格。Python 的
WindowsApps 商店别名不会作为解释器使用。脚本只为子进程配置环境，退出时恢复调用方
会话，不写入用户或系统环境。

`environment.ps1` 提供 `Get-MoeriseAndroidEnvironment`，返回工具链和缓存路径，
并导出仓库根目录变量 `$androidRepoRoot`。点调用时初始化进程内的 `JAVA_HOME`、
SDK、PATH 与 Gradle 缓存；加 `-ResolveOnly` 则只加载路径解析功能。
Windows 入口使用上述共享 Gradle 缓存和工程缓存，
`GRADLE_OPTS` 和 `JAVA_OPTS` 继续传给 Gradle。
仅检查参数、档案格式、Profile 前置条件和工具路径时执行：

```powershell
pwsh -NoProfile -File scripts/android/benchmark.ps1 startup -ValidateOnly
```

该检查读取本机配置并查询 Node.js、Python 版本，不调用 ADB、Gradle 或数据准备脚本。
正式执行会清空 `.benchmark` 包的数据，在结束或发生错误时删除设备上的临时连接档案。
Windows 结果目录为公共锁任务目录下的 `benchmark-<模式>/`，可从
`queue.json` 的 `output_root` 定位；目录保存运行日志、原始 JSON、
系统 trace、测试报告、目标 APK 与 `device.json`。元数据包含提交号、工作区改动标记、
设备序列号、系统、ABI、页大小、电池、温度、内存和 APK SHA-256，不复制连接档案。
构建失败时也归档已生成的原始结果和错误信息；测量成功后复用
`benchmark-report.py` 输出每项指标的 P50/P95 和 `summary.json`。报告支持 AndroidX `Mode.First` 的 `FirstMs` 字段，保留原字段名，并严格检查迭代数、非空 trace、成功的安装和 instrumentation 记录。缺失采样不能计作通过。

性能目标和取样条件见[路线图](ROADMAP.md#18-快速启动与性能目标)，完整设备操作见
[设备检查](android/DEVICE-CHECKS.md)。

## 发行

发行 ABI 为 `arm64-v8a`、`x86_64`。构建、R8、发行 lint、依赖许可检查和静态
16 KiB 对齐检查共用以下入口：

```bash
scripts/android/release.sh --unsigned
```

签名时设置 `MOERISE_ANDROID_KEYSTORE`、`MOERISE_ANDROID_KEY_ALIAS`、
`MOERISE_ANDROID_STORE_PASSWORD` 和 `MOERISE_ANDROID_KEY_PASSWORD`，然后运行
`scripts/android/release.sh --sign`。密码通过环境变量传给 `apksigner`，密钥放在
私有目录或 CI Secret 中。入口要求 Git 工作区干净，产物归档到
`.local/android/releases/v版本-提交号/`，包含 APK、SHA-256、构建信息、
安装说明和第三方声明；R8 mapping 随构建保存。

`verify-apk.py` 检查包名、版本、发行调试标志、签名、ZIP 和 native ELF 的
16 KiB 对齐。首次发行以新安装数据完成设备实际运行与 HTTPS 检查，使用完整 schema 1；
后续正式版本再验证同签名覆盖升级及所需的数据库迁移。具体操作见
[设备检查](android/DEVICE-CHECKS.md#发行与升级)。
[安装与升级](android/INSTALL.md)可随发行包分发。

运行时依赖来自 `:androidApp:exportReleaseDependencies`，第三方许可清单见
[Android 第三方许可](android/THIRD_PARTY_NOTICES.md)。更新依赖后重新导出清单并执行
`python3 scripts/android/licenses.py`；CI 使用 `--check` 比较。

Android 与 Linux 联合发布在同一个 `v<版本>` Release。合并后 `Main CI` 从 GitHub Actions Secrets 读取
`ANDROID_SIGNING_KEYSTORE`（Base64 keystore）、`ANDROID_KEY_ALIAS`、
`ANDROID_STORE_PASSWORD` 和 `ANDROID_KEY_PASSWORD`。发行构建在 API 29 模拟器全新安装签名 APK，
检查登录、笔记保存与读回、应用重开后的会话。隔离 HTTPS 的临时 CA 安装到
模拟器系统信任库，应用使用发行版证书校验与 Secure Cookie。结果归档为 `android-release-checks`。
`Publish release` 校验该次构建的 APK 摘要与冒烟结果一致，使用原产物发布 APK、SHA-256、
构建 JSON 和许可 ZIP。具体步骤见[发布](DEVELOPMENT.md#发布)。

PR 的 Android 检查在 API 36 模拟器运行登录、记录、编辑、附件、洞察选材、草稿恢复与离线队列流程。
手动运行 `Android release` 使用临时密钥签名并执行相同的设备检查。

### Windows 发行入口

Windows 使用 PowerShell 7.2 以上与 Python 3.11 以上，工具链及本机缓存路径由
`environment.ps1` 解析。在仓库根目录执行：

```powershell
$env:MOERISE_ANDROID_PYTHON = 'C:\工具\Python\python.exe'
.\scripts\android\with-device-lock.ps1 -Owner android-release -Task release-unsigned -Command {
    param($context)
    & "$($context.source_root)\scripts\android\release.ps1" unsigned -SourceRoot $context.source_root
}
```

`unsigned` 是默认模式。签名模式读取已有的 `MOERISE_ANDROID_KEYSTORE`、
`MOERISE_ANDROID_KEY_ALIAS`、`MOERISE_ANDROID_STORE_PASSWORD` 和
`MOERISE_ANDROID_KEY_PASSWORD` 环境变量，然后执行：

```powershell
.\scripts\android\with-device-lock.ps1 -Owner android-release -Task release-sign -Command {
    param($context)
    & "$($context.source_root)\scripts\android\release.ps1" sign -SourceRoot $context.source_root
}
```

也可在上述锁内调用 `release.ps1 sign -SigningFile <本机签名档案绝对路径>`。
`signing.json` 保存本机签名档案，字段为 `keystore`、`alias`、`store_password`、
`key_password`；`keystore` 指向已有发行密钥。签名档案和密钥保存在仓库外的私有目录，
不随源码或发行产物分发。
Windows 构建默认使用 Gradle 中的 `versionCode`，也可以用 `-VersionCode` 显式指定。

密码通过 `apksigner` 的 `env:` 参数读取，不写入命令行参数或归档。脚本使用已有密钥，
只进行本机构建、签名与归档。发行构建要求已跟踪文件没有修改，未跟踪文件不阻断。
每次调用通过一次 Gradle 执行 release APK、发行 lint 和运行时依赖导出，限制
单 worker、单 JVM 处理器，并关闭并行构建和常驻进程；多个构建顺序运行。
随后执行许可检查、16 KiB ZIP 与 ELF 对齐检查；签名模式另检查 APK 签名。
Windows SDK 工具按 `.exe`、`.bat`、无扩展名依次选择。

版本名、版本号和源 APK 路径读取实际的 `output-metadata.json`。产物保存到
公共锁任务目录下的 `releases/v<版本>-code<versionCode>-<提交号前七位>/`，包含 APK、SHA-256、构建元数据、
原始输出元数据、构建和校验日志、第三方许可 ZIP、许可目录、安装说明及 R8 mapping；
许可 ZIP 不包含 mapping。

无设备检查使用：

```powershell
pwsh -NoProfile -File scripts/android/release.ps1 unsigned -ValidateOnly
pwsh -NoProfile -File scripts/android/release.ps1 sign -ValidateOnly
```

该模式检查工具路径、Python 版本及签名配置，输出构建参数和 `tracked_clean`；
正式构建才要求 `tracked_clean` 为 `true`。检查不运行 Gradle、ADB、签名或 APK 构建，
不修改环境变量。
