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

共享检查覆盖 schema 1→3 迁移、创建与编辑的丢失响应恢复、保存确认事务期间续写，以及原键重放返回外部修改时的冲突合并。洞察使用真实 Room 文件检查重开后保留新建请求、生成请求和部分回复，读取恢复不会自动生成。

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

`commonMain` 使用 Compose、Coroutines 和 Room；Android 数据库路径位于 `androidMain`，Linux JVM 文件数据库构造位于 `jvmMain`。共享代码不引用 `android.*`。独立本地模式、iOS 平台和各功能的交付范围见[开发路线图](ROADMAP.md#1-阶段四android-客户端)。

## Core 连接与真实服务联调

连接页接受与 `web.public_origin` 相同的 Web 入口，API 位于 `/api/app/v1`。登录密码仅用于登录请求；会话 Cookie 使用 Android Keystore 的 AES-GCM 密钥加密后存入免备份目录，CSRF 只保存在内存。Cookie 限定同一 scheme、host 和端口，不跟随重定向，不启用底层写入自动重试。

服务来源、实例 ID 和作者资源名共同组成账号隔离键。恢复会话时重新读取身份，身份变化要求登录；非 GET/HEAD 操作携带 Origin 和 CSRF，CSRF 校验失败时仅刷新一次。退出先清除本机身份，再尝试撤销服务端会话；旧响应不能覆盖新登录的 Cookie。

真实服务检查需要 Go 1.27、Node.js 24 及 Docker。Core 观星契约基线为 `062185f`，Memos 固定为 `0.31.0` 及脚本中的镜像摘要。执行：

```bash
scripts/android/live-check.sh
```

脚本构建独立 Core，启动受 CPU 和内存限制的 Memos 容器、Web 代理和本机可控假模型，使用虚构账号检查登录、重开会话、同键创建、实时读回、删除与注销，以及洞察的生成、重放、异常结束、停止和回复存笔记。假模型只接受隔离服务的临时密钥，不连接外部模型。测试结束清理容器、来源数据和临时凭据；非敏感日志留在 `.local/android/live-*/`。Linux JVM 检查使用与 Android 相同的 OkHttp 引擎；Keystore、设备生命周期和触控通过 Android 设备流程检查。

隔离实例通过 Memos 官方 API 将 `contentLengthLimit` 设为 65,536，用于覆盖 Core 的
64 KiB 内容上限。真实服务的笔记长度还受其 Memos 实例配置约束。

单独联调设备时运行 `scripts/android/start-fixture.sh`。脚本输出 Web 来源、私有连接档案路径和 `adb reverse` 命令；连接档案只用于隔离环境，包含虚构账号和临时凭据。按 Ctrl+C 清理服务与来源数据。观星六类顶层状态及更新中的固定响应样本位于 `mobile/shared/src/commonTest/resources/contracts/map/`。

`scripts/android/stars-check.sh` 构建独立布局镜像，启动限制为半个 CPU、1 GiB 内存的
布局 worker、私有模型卷和 32 篇固定笔记。共享 OkHttp 客户端读取真实 UMAP 快照，
检查新增与向量变化的 transform、删除、主动重建和模型文件丢失后的重新拟合。
检查比较旧坐标、星体属性、frame 与正文摘要，并从 worker 模型文件确认 UMAP 类型
和首次拟合数量；布局镜像摘要与结果保存在隔离服务日志目录。结束时清理专用容器、
卷及凭据。该入口与 Gradle 构建串行执行。

## 草稿与设备流程

已配置账号启动后异步恢复本机草稿和检查会话；编辑器不等待网络，时间线读取安排在编辑器首帧之后。短输入在 300 毫秒防抖后落盘，持续输入最多等待约一秒合并落盘；保存、切页和进入后台时立即刷入数据库。保存请求在发送前冻结正文、来源摘要和幂等键，重试沿用原始请求。提交期间的续写保留在同一篇笔记中，服务器版本发生变化时进入合并页面。

`note_editor_ready` 标记从 Activity 创建开始，到本机草稿恢复后编辑器完成首个可输入帧结束，同时调用 `reportFullyDrawn`。此标记用于设备启动测量。

设备验收使用独立包 `io.github.fermin214.moerise.fixture`，数据与普通安装隔离。脚本会清空该验收包的数据；连接档案在设备测试后删除。隔离服务运行时执行：

```bash
export ANDROID_SERIAL=设备序列号
export MOERISE_ANDROID_FIXTURE=/绝对路径/connection.json
scripts/android/acceptance.sh
```

设备流程覆盖登录、输入、Activity 重建、保存与时间线读回，结果写入 `.local/android/device-acceptance.log`。输入法、进程终止和后台恢复分别通过设备操作及持久化恢复检查补充。

## 笔记管理与附件

时间线点选笔记后读取 Core 的实时正文。详情页支持编辑、归档、恢复与删除；归档列表位于设置。编辑草稿按笔记资源名独立保存，返回主页恢复主页草稿。归档和恢复在发送前记录原始请求和幂等键；删除结果不明时读取详情确认，资源确实不存在才清理本机记录。

选择文件后复制到免备份目录，完成大小检查、SHA-256 计算和落盘再绑定草稿。文件复制期间禁用服务器保存；绑定时检查账号和草稿 UUID，迟到结果不能写入其他草稿。新增文件最多 10 个，合计不超过 8 MiB；请求以 JSON Base64 提交，编码后的 JSON 不超过 Core 的 12 MiB 限制。发送期间可续写正文，附件变更在请求完成后操作。未确认请求和冲突草稿持有文件引用；没有引用且超过一天的暂存文件在保存后回收。

详情中的图片只读取该笔记管理的附件，可见时下载并持有位图，离开视口释放。图片下载和解码最多并行两个任务，图片内容上限为 8 MiB，解码最长边为 1024 像素。外部图片显示链接，由系统浏览器打开；原始 HTML 作为文本显示。下载按来源与笔记资源校验，写入应用缓存，通过只读 FileProvider URI 交给系统应用。系统打开附件的缓存按 256 MiB 和 24 小时回收；账号切换、退出或会话失效清理受保护下载缓存。

编辑来源冲突时展示提交正文、服务器正文和本机合并文本。附件列表用于确认要移除的远端文件，以及需要重新添加的本机文件；不根据同名判断上传完成。笔记保存成功后，续写绑定前会比较完整附件集合，并用字节摘要确认新增文件。

设置中的本机草稿列出有修改、冲突或保存结果不明的记录，远端笔记删除后仍可恢复本机正文。冲突可合并到远端笔记，或另存为使用新 UUID 的主页草稿；另存保留原始冲突请求和已有主页草稿，附件按显式选择加入，不继承远端移除操作，也不自动提交。

“删除本机账号数据”经确认后停止旧任务，等待取消记账和本机写入结束，再删除该账号的草稿、请求、功能恢复记录、快照和暂存附件并退出。其他账号的记录和引用文件保留。

## 搜索与日期

搜索入口位于列表顶部。关键词、语义和混合检索共用正文、标签、状态和日期筛选，结果通过 Core 的游标分页。更换条件会取消旧查询并清除旧游标；游标失效时保留阅读结果，重新搜索从第一页开始。混合检索降为关键词时显示降级结果，语义不可用时可切换关键词检索。

搜索页通过结果、标签和日历页签组织内容，底部按钮提交组合筛选。标签来自 Core 的使用统计；日历显示指定月份的笔记日期，正常、归档和全部状态分别读取统计。详情中的标签可直接打开对应筛选。

月份和笔记时间使用会话提供的服务时区，日期查询直接提交服务端日期字符串。搜索结果和列表的滚动位置保留在账号内，详情返回后恢复原查询；退出或会话失效清理搜索内容。

## 回顾与漫步

导航抽屉提供主页、回顾和漫步入口。回顾读取 `/reviews/daily` 的固定计划，显示服务返回的日期和时区；条目通过底部选择按钮和下一条切换。计划更新时，同一批次保持选择位置，新批次从第一条开始。不可用条目可继续切换，空计划显示空状态；打开详情后返回原条目和阅读位置。

漫步支持随机开始，以及从正常笔记的详情和回顾条目指定起点。继续请求只提交服务返回的笔记资源名和访问路径；拒绝重复路径、错误成员和不属于账号的数据。同一页面最多一个漫步请求，失败保留已访问路径，重试沿用原请求内容。结束漫步清理本机会话。

离开页面或进入后台会取消回顾与漫步读取。回顾在返回时刷新计划；漫步返回后保留结果和重试入口，不自动再次请求。会话失效、退出和账号变化会清理探索内容。向量索引关闭、尚未就绪、起点归档、来源变化和读取中断分别显示错误；向量设置通过系统浏览器打开 Web 设置。

## 洞察

洞察使用对话、材料和历史页签。材料可从正常和归档笔记中搜索选择，也可从笔记详情、回顾和漫步直接加入。材料预算及来源摘要以 `/chat-materials/resolve` 为准；修改材料后显式应用到会话，再发起一键洞察或提问。历史列表通过游标分页。

新建、材料变更、生成、删除和保存回复在发送前持久记录原始请求。新建使用固定 `request_id`，生成使用 `request_id`、会话版本和语言；请求结果不明时保留原体。材料更新通过读取会话版本与材料引用判断是否成功；删除先读取结果，仍然存在时再次确认。保存回复使用服务端消息版本，可按原请求恢复。

生成使用 POST SSE，单次预算为 135 秒，连接预算为 10 秒。解析器按完整行解码 UTF-8，识别 CRLF、多行数据、半帧、错误帧及异常结束；只有合法 `complete` 帧表示成功。普通 API 请求维持 65 秒预算，底层关闭写入重试和自动重连。流式文本合并后批量更新界面，部分回复定期落盘。

离页和进入后台取消流式读取；返回页面和进程重启只读取持久会话，生成中的会话每 3 秒读取状态。停止操作调用服务端 cancel，再重新读取。失败、中断和取消后的重新生成由按钮触发，使用新请求 ID 与适用版本；原请求尚未出现在会话时按原 ID 恢复。服务器完成结果替换本机预览，旧部分回复仍可恢复。完整回复通过服务端 save 接口保存为笔记，正文超过 UTF-8 上限时提示先生成总结。

账号恢复材料位于 Room 的 `feature_states`，独立于可清理的时间线快照；退出和会话失效清除界面内容，重新登录后可读取同一账号的恢复材料。

## 观星

观星直接使用 Compose Canvas 绘制 Core 发布的快照，支持 `umap-balanced-v1` 和旧 `pca-topics-v3`。坐标、星体属性、主题、子主题与关联来自服务端，客户端校验有限数值、资源唯一性和成员引用。未知算法或缺失契约显示兼容错误并停止自动读取和重建。

星图支持单指平移、双指缩放、放大缩小、主题聚焦、主体与全图取景、星图与摄影氛围及系统全屏。绘制和点击共用坐标变换，扩大触控范围后选择最近的真实笔记。主题与笔记页签提供列表入口，未定位笔记通过筛选读取；点星或点选列表后显示实时正文，可进入完整详情、漫步和洞察。

辉光、密度柔光和主题文字按快照缓存，密度柔光只由真实语义点生成。单个 Canvas 绘制星点，按视口裁剪；静止页面不运行持续动画。近景最多读取三个可见摘要，串行请求并按资源名和来源摘要缓存；手势、页签、页面或后台变化会取消旧请求。返回星图恢复视口、选择与阅读位置。

同一坐标框架更新保留视口，新框架重新取景；来源摘要变化会清除旧摘要和阅读结果，迟到响应不能覆盖新选择。更新失败和网络中断保留可读快照。稳定页面每 60 秒检查，生成或重建期间每 3 秒检查，失败后至少退避 10 秒；离页和后台停止读取。

重新生成在 POST 前保存账号、原框架、发起时间和观察状态。202 丢失时保留结果不明记录，返回或重启只通过 GET 观察；出现新框架表示有新快照可用。失败后按退避时间重试，再次提交由确认按钮触发。快照更新失败结束观察，客户端不自动重发重建 POST。

Android 将响应体积、解析、首绘、绘制和摘要请求计数写入 `MoeriseMap` 日志及 `moerise_map_*` Trace 计数器，日志仅含指标和布局版本。设备测量可通过 `adb logcat -s MoeriseMap:I` 收集，结合系统帧耗时和 Perfetto 分析。

## 性能测量与 Baseline Profile

`mobile/benchmark/` 是独立的 AndroidX Macrobenchmark 测试模块。目标包为
`io.github.fermin214.moerise.benchmark`：`benchmark` 构建启用 R8 和资源裁剪，
`profile` 构建保留原始方法名用于采集，二者均关闭应用调试、使用测试签名并只对回环
地址开放 HTTP。固定星图 Activity 只包含在这两个构建中。
测试 runner 使用相同的调试签名，Perfetto 三个组件固定为 1.0.1；应用包与 runner
均检查签名、ZIP 和 64 位 native 库的 16 KiB 对齐。

连接固定 arm64 真机和隔离服务后执行：

```bash
export ANDROID_SERIAL=设备序列号
export MOERISE_ANDROID_FIXTURE=/绝对路径/connection.json
scripts/android/benchmark.sh profile
scripts/android/benchmark.sh run
```

脚本清空性能包的数据，通过真实 Core/Memos 准备 100 篇固定笔记，其中包括 64 KiB
正文和 10 个附件。测试在计时区间外完成登录。Profile 采集使用 API 33 及以上设备，
将设备生成的规则写入 `androidApp/src/main/baselineProfiles/` 下的
`baseline-prof.txt` 和 `startup-prof.txt`；
启动规则与星图规则分别采集，AGP 在构建时合并和转写。更新规则后提交源文件，再构建
发行包。设备性能测试最低 API 为 28。

| 场景 | 采样与输出 |
| --- | --- |
| 冷启动 | 无 Profile 与使用 Profile 各 50 次，记录 TTID、TTFD、`note_editor_ready` |
| 后台返回 | 同进程 HOT 启动，两种编译模式各 50 次，记录 `note_foreground_ready` |
| 星图首绘 | 1,000/5,000 点与两种氛围共四组，每组 50 次，记录解析、完整首绘和系统帧耗时 |
| 星图手势 | 四组场景分别执行平移、双指缩放、摘要到达和快照刷新，每组 10 次 |

星图场景复用共享 `StarCanvas`，使用确定性的坐标、星体属性、主题与关联；计时从
完整 JSON 可读时开始，摄影氛围包含密度柔光生成。网络和 Core 布局耗时通过真实观星
流程另行记录。场景名、编译模式、响应体积、JSON、系统 trace、APK SHA-256、提交号、
设备、页大小、电池和温度信息保存在 `.local/android/benchmark-*`。
`benchmark-report.py` 从原始 `benchmarkData.json` 计算每项指标的 P50/P95，
写入 `summary.json`。可用 `benchmark.sh startup` 或 `benchmark.sh stars` 单独测量。
Core 请求累计数写入 `MoeriseStartup` 日志与 `moerise_network_requests` Trace 计数器，
可按启动区间区分会话恢复和首帧后的列表请求。

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
16 KiB 对齐。设备实际运行、HTTPS 与同签名升级采用
[设备检查](android/DEVICE-CHECKS.md)中的发行流程。
[安装与升级](android/INSTALL.md)可随发行包分发。

运行时依赖来自 `:androidApp:exportReleaseDependencies`，第三方许可清单见
[Android 第三方许可](android/THIRD_PARTY_NOTICES.md)。更新依赖后重新导出清单并执行
`python3 scripts/android/licenses.py`；CI 使用 `--check` 比较。

Android 标签采用 `android-v*`。`android-release.yml` 在标签事件中读取
`ANDROID_SIGNING_KEYSTORE`（Base64 keystore）、`ANDROID_KEY_ALIAS`、
`ANDROID_STORE_PASSWORD`、`ANDROID_KEY_PASSWORD`，校验标签与 APK 版本后创建草稿发行。
手动 workflow 默认构建 unsigned 包，可通过 `sign` 输入选择签名。APK、SHA-256、
构建 JSON 和许可 ZIP 用作发行附件，mapping 保存在构建产物中。
