# Moerise 开发指南

本指南面向代码贡献者。以下命令在仓库根目录执行。模块职责见[产品与架构](PRODUCT-ARCHITECTURE.md)，功能范围见[路线图](ROADMAP.md)。

## 环境与工具

- Go 1.27+：构建服务，运行测试、格式化和静态检查，建议使用该系列的最新补丁版本。
- Node.js 24+ 与 npm：构建和运行正式 Web，执行 Web 测试。
- Git：版本管理。
- Memos 0.31.x：手动联调时使用独立测试实例和账号。
- Docker Engine：运行真实 Memos 集成验收；日常构建和基础测试无需 Docker。

开发验证使用 Linux 或 Linux 容器，服务端支持 Linux 原生二进制和 Docker Linux 容器。服务构建无需 C 编译器；`go test -race` 需要 C 工具链。Windows、macOS 原生服务端支持见[路线图](ROADMAP.md#后续方向与开发输入)。容器部署见[运行与恢复](OPERATIONS.md#容器部署)。

## 安装依赖与运行

依赖及校验和保存在 `go.mod` 和 `go.sum`。首次运行 Go 命令时会自动下载，也可预先执行：

```bash
go mod download
go mod verify
```

按[配置说明](OPERATIONS.md#配置)创建 `.local/config.json`：

- 填写服务进程可访问的测试 Memos 地址、账号资源名和 PAT。
- 将 `state_dir` 和 `index_dir` 分别设为 `.local/state`、`.local/index`。
- 生成独立的 Moerise 访问令牌和分页密钥。

```bash
go run . check-config
go run . serve --host 127.0.0.1
```

`check-config` 检查合并持久设置后的有效配置。来源版本与账号在同步时检查，运行状态见[运行检查](OPERATIONS.md#运行检查)。命令的 `--config` 参数优先于 `MOERISE_CONFIG_FILE`，两者均省略时读取 `.local/config.json`。

基础设施按内置默认值、JSON、非空 `MOERISE_*` 环境变量依次覆盖。配置状态加密主密钥后，日常字段从部署值初始化一次，随后以 Web 保存的持久值为准。`MOERISE_NOTIFICATIONS_TIMES=09:00,18:00` 和 `MOERISE_REVIEW_COUNT` 提供初始时段及每次条数；完整变量见[配置说明](OPERATIONS.md#配置)。基础设施变更通过重启读取。

## 检查与验证

```bash
go test ./...
go vet ./...
gofmt -l main.go internal tests
```

格式检查应无输出，修正格式使用 `gofmt -w main.go internal tests`。基础测试使用 HTTP 模拟响应和临时数据库，无需 Memos、Docker、模型或真实凭据，覆盖分页中途失败、身份变化、任务合并与重启恢复、备份格式兼容、恢复损坏检查和写入互斥。记录测试补充并发幂等、请求与 Scope 边界、写入期间身份变化、不可信响应和跨作者资源冲突。

每日回顾业务流程验证简单随机选取、默认 72 小时等待期与 7 天重复排除期、归档开关、候选不足和空池，以及显式自定义时长的保留。固定批次检查并发读取、规则修改后沿用当天结果、关闭推送仍生成结果、来源变更、当地跨日，以及索引重建和备份恢复后的历史保留；选择规则不附加主题配额、纪念日或历史优先排序。

通知业务流程验证多时段共享批次、添加时段不重新选取、按时段去重、空池静默及来源故障重试。停机恢复仅补建当天已到达时段，不新建往日推送，队列中的任务继续恢复；另覆盖夏令时跳时与重复小时、平台拒绝、发送中断、回执复查、HTTP 权限及快照恢复后的结果确认。

机器 REST 与 MCP 验证单条获取、分页列表、搜索和已有回顾读取；机器 REST 使用只读操作白名单，MCP 的工具发现及直接调用均不能创建或更换回顾、记录打开和略过、更新阅读历史或触发 AI 生成，即使令牌带写入或 `ai:generate` Scope。不存在的当天回顾返回明确结果，不在读取时创建。回顾创建、更换和阅读记录的业务测试直接调用共享服务。

飞书记录验证整条文字原样写入，包括首尾空白、换行、标签和看似命令的正文；图片及文件使用附件链路，重复事件与发送失败不重复记录。待处理事件按入队时的正文语义及幂等键恢复。

回顾验证每次推送以单独一条 `「今日回顾」` 开始，随后逐篇发送完整正文、原图及尾注 `#标签  创建于YYYY-MM-DD`；无标签时尾注只有 `创建于YYYY-MM-DD`，日期取来源 `create_time` 并按配置 `timezone` 换算。不附加链接、片号或状态提示，普通文件不推送。正文按编码后的 18 KiB 请求预算分片，保持 UTF-8 边界，按发送顺序可重组原文；图片验证从 Memos 读取的原始字节、上传结果复用和纯图片笔记。没有可发送内容的笔记不发尾注，整次均无内容时不发标题。逐片检查回执，结果不明时等待确认再发送后片；确认已送达后继续，确认未送达只为该片换 UUID 重发，重启后不重复发送已确认的标题。笔记变更时跳过其剩余分片。

向量模型测试使用 DashScope 和 OpenAI 兼容 HTTP 模拟服务，覆盖文档与查询参数、分批与返回位置、并发限制、有限重试、取消与超时，以及错误数量、维度和数值的拒绝。命令流程测试从配置文件运行连接检查，验证省略模型配置时默认关闭，连接检查不打开数据库。供应商异常响应、可选维度参数和并发边界使用专项测试，基础 Memos 验收不覆盖这些行为。

模型实调配置与命令见[向量模型](EMBEDDING.md#连接检查)。`check-embedding` 使用内置虚构文本；基础测试与 CI 无需真实模型凭据。

LLM 适配验证 Chat Completions、Responses 和 Anthropic Messages 的原生请求、完整 JSON 与流式 SSE、完成标记、用量、限流与故障重试、取消、并发等待、输出后失败不重放和敏感错误清理。命令流程验证 `check-llm` 真实读取流式响应、省略模型配置时默认关闭，且不创建产品数据库。协议失败与流中断需用可控 HTTP 服务触发，真实模型连接检查不能稳定覆盖这些边界。模型配置与实调方法见 [AI](AI.md)。

旧结构化 AI 业务测试直接调用共享 `AIService`，用于检查生成逻辑和旧任务恢复。证据流程验证指定来源与相关历史的边界、同文多来源、精确标签、状态及创建时间半开区间，以及过滤先于召回上限。专项测试覆盖转义后的 JSON 预算、UTF-8 裁切、微秒日期边界、无效引用和收集后来源变化；这些边界无法通过不稳定的模型输出可靠触发。证据字段和省略规则见 [AI](AI.md#证据与引用)。

洞察流程使用可控模型验证事实、解释、追问与引用的映射，检查输入总预算和完整条目输出。失败路径覆盖虚构引用、无引用事实、缺少结束记录、输出后的模型故障、回调取消，以及输出前和完成前的来源修改、删除与作者变化；这些情况均不能产生成功完成事件。

多条洞察检查只使用选定来源、相同正文的独立资源标识、跨笔记解释引用双方、实际使用来源，以及选定来源被过滤时返回证据不足。

问答流程验证问题和标签、状态、日期筛选传入共享检索，区分有据回答、部分证据不足、零命中及模型不可用；零命中不调用模型，普通问答不新增来源笔记。

时间线流程使用超过单次笔记上限的近期记录验证早期来源仍被召回，检查空时间段、半开区间、创建与修改及正文日期的区别、跨段引用索引和合并后的总预算。因预算丢失整个时间段时，结果必须指出缺失并使用证据不足状态。

AI 入口隔离由 `TestExternalAIEndpointsRejectEveryCredential` 和 `TestAIAPIMCPVisibilityAndDirectCalls` 验证：机器 REST 拒绝生成请求，MCP 不公布生成工具且拒绝直接调用，持有 `notes:read` 与 `ai:generate` 也不能调用模型。共享服务测试检查引用、内容和完成事件、错误及取消传播；旧请求解析器单独检查严格 JSON 和请求大小。App 装配保证 AI、搜索和飞书复用服务实例、模型提供商与并发限制。

飞书 AI 队列的恢复流程验证生成结果先落库、事件重投和重启复用结果、生成中断终止、长结果按序分片，以及独立回执和结果不明处理。每片发送前复查来源，失效后只发送固定提示；数据库迁移和快照恢复检查任务与分片状态。专用私聊文字全部走记录入口，不以 AI 命令触发生成。

向量索引流程测试覆盖分批持久化、模型失败、重启续建、归档及正文增量更新、删除清理、向量空间切换、构建期间投影变化、取消，以及索引重建和状态恢复。分块来源位置、UTF-8 边界、来源复查和错误向量的专项测试用于覆盖完整流程难以稳定触发的边界。真实 Memos 验收搭配 DashScope 模拟服务，验证服务进程自动构建、状态接口、故障恢复、空间替换和产品状态保留。

混合搜索验证覆盖请求 trim 后的 Unicode 码点与 UTF-8 字节边界、标签格式及精确 AND、默认状态和数量、未知字段 422、机器只读白名单拒绝非 POST 请求的 403、Scope，以及 MCP 只读、幂等、非破坏提示。排名检查涵盖中文单字与双字短查询、英文数字整词、FTS 词项安全引用、三通道各前 100 片段、RRF k=60、笔记最高片段分数、最多 3 个片段和稳定排序。协议与分块位置见[搜索](SEARCH.md)。

搜索的模型与索引检查覆盖活动代次内存 float32 缓存复用、配置空间和投影修订失配、模型关闭及失败的显式降级、取消不降级，以及独立于 embedding 的关键词和片段构建。索引检查涵盖启动补建、增量修改删除和重建。仅对完整流程难以可靠触发的边界补充专项测试。

相关笔记验证覆盖 REST 重复 `tags` 的精确 AND、默认值和边界、未知参数及重复 `state`/`limit` 的 422、机器只读白名单拒绝非 GET 请求的 403、Scope，以及 MCP `RelatedRequest` 输入校验与只读工具提示。召回检查涵盖全量长正文词项、超长词项排除、共同标签数量排序、自身排除、最多三个片段与重复正文合并。语义检查覆盖同代次文档向量逐片段归一化、去重后等权平均并归一化、正余弦，以及无额外模型调用和三种降级原因。相关契约见[搜索](SEARCH.md#相关笔记)。

支持竞争检测的环境还可执行：

```bash
go test -race ./...
```

同步、Memos 协议和恢复改动使用真实接口验收。先启动 Docker Engine，再执行：

```bash
go test -tags=integration ./tests -v -count=1
```

验收自动编译服务，创建独立 Memos 容器、匿名数据卷、账号和临时配置，结束后清理。使用 Docker 发布的回环端口，因此 Docker 引擎需与测试进程位于同一台机器。验收覆盖：

- 凭据、只读范围、管理员 PAT 的作者隔离，以及读取时的原文复查。
- 首次同步、分页游标、新增、修改、归档、删除和回溯时间修改。
- 来源断连、服务重启后的任务恢复、在线备份、恢复互斥和索引重建。
- 文本与标签记录、幂等重放与冲突、原文修改和删除，以及写入状态跨重启、索引重建和备份恢复。
- 通过代理模拟上游写入成功后响应丢失、写入前返回 503，验证结果不明时先复查原资源、重发沿用同一资源标识，Memos 中只有一条笔记。
- 附件部分上传失败、上传成功后响应丢失、文件内容读取，以及重启、索引重建和备份恢复后的资源复用。
- 共享回顾服务创建、更换批次及记录打开与略过，REST 与 MCP 只读共享已有批次；机器 REST 拒绝相应写入，MCP 验证初始化、获取与分页、只读工具提示及写入和生成工具在发现与直接调用中的禁用；重启和恢复后沿用同一批次。
- 真实 Memos 与飞书平台模拟器联调：事件去重、其他身份隔离、无前缀文字原样保存、图片与文件记录，以及恢复业务执行前的入站事件快照后复查原资源。
- 同一机器人按时段推送固定批次，每次只发送一条独立的 `「今日回顾」`；逐篇发送完整正文、原图和标签日期尾注，日期按配置时区换算，无标签时只显示创建日期。正文按序分片，图片读取 Memos 原始字节后上传发送；覆盖纯图片笔记、普通文件跳过、空笔记无尾注及整次无内容时不发标题。不附加链接、片号或状态提示。平台响应丢失后暂停后片，通过回执确认放行，重启复用上传及发送进度，已确认标题不重复发送；来源删除或变化时跳过该笔记剩余分片，来源故障重试。
- 中文单字、双字和短查询、语义改写与标签精确 AND，REST 与 MCP 共享搜索结果；DashScope 模拟服务覆盖语义可用、关闭及失败降级。
- 搜索候选原文修改、删除、作者越界、状态或标签变化后的丢弃与投影修正，搜索前后身份复查，以及来源故障使整请求失败且不返回缓存正文。
- 关键词与向量索引跨重启恢复，关闭模型也可搜索；队列中的飞书搜索任务按加密查询恢复，发送前重新搜索并检查来源变化。
- 相关笔记在 REST 与 MCP 间的结果一致性；归档原笔记召回正常候选，返回原笔记资源与 URL、结果片段位置和语义降级原因。
- 相关笔记原文在输出前消失或越界返回 404 `SOURCE_NOT_FOUND`，处理中改变返回可重试的 409 `SOURCE_CHANGED`；候选变化丢弃并修正投影，任意来源故障不输出原笔记或部分结果；重启复用索引且不新增产品状态。

### 客户端 API 与正式 Web

`tests/client_acceptance_test.go` 的 `TestClientMemosAcceptance` 使用真实 Memos 和 Core 进程，通过 HTTP 验证原生客户端 API：

```bash
go test -tags=integration ./tests -run '^TestClientMemosAcceptance$' -v -count=1
```

该流程覆盖未登录与错误密码、非绑定账户拒绝、本人正常笔记分页、创建时间及同时间资源名倒序、置顶与编辑不改变创建排序、含空白换行和标签的原文创建、同键重放与冲突、来源断连、Core 重启后的会话及幂等状态、索引重建后的会话与来源列表、退出后旧 Cookie 失效，以及带写入 Scope 的机器令牌仍不能写入或生成。响应检查包括 Cookie 属性、禁止缓存和上游敏感字段隔离。该测试不启动 Web 或浏览器；Origin、CSRF、续期与退出竞争、快照恢复后会话失效由 `internal/client_api_test.go` 和 `internal/sessions_test.go` 补充覆盖。

`tests/client_management_acceptance_test.go` 补充笔记管理、附件及原生来源边界，各流程使用独立的 Memos 实例：

```bash
go test -tags=integration ./tests -run '^TestClient(Management|NativeExactTags|MutationTimestamps|AttachmentDeletionPreflight|ExistingAttachmentCounts)MemosAcceptance$' -v -count=1
```

该流程检查原文保留、纯图片及纯文件创建、编辑冲突、归档恢复、删除、关键词/日期/多标签组合筛选和全量标签日历统计，并从 Memos 官方 API 核对纯图片的空正文与附件归属。原生标签覆盖标点、组合字符及 emoji；正文和状态编辑检查修改时间推进及创建时间保留。附件检查覆盖跨重启读取、GET/HEAD/单区间 Range、图片内嵌及文件下载、匿名与无关笔记拒绝、退出后失效、超过十个已有附件的笔记管理、正文仍引用附件时的可修正错误，以及上传成功后响应丢失时跨重启复用附件。

使用 Node.js 24+ 构建并检查正式 Web：

```bash
npm --prefix web run build
npm --prefix web test
```

Core 启用 `web.enabled`，并将 `web.public_origin` 设为浏览器访问的 Web Origin。Web 的 `PUBLIC_ORIGIN` 与其一致，`CORE_ORIGIN` 固定指向 Core。启动示例：

```bash
CORE_ORIGIN=http://127.0.0.1:8787 PUBLIC_ORIGIN=http://localhost:3000 \
npm --prefix web start
```

正式浏览器流程使用构建后的 Web 和真实 Memos，关闭 Embedding、LLM 与飞书，完成登录、本人正常笔记分页、原文创建、刷新、Core 重启后继续访问及退出。创建内容包含首尾空白、换行和正文标签，并从 Memos 官方 API 读回核对；同一创建请求重试复用原幂等键且只产生一条笔记。管理流程覆盖纯图片与纯文件、附件增删、归档恢复、删除确认、跨页组合筛选、详情刷新及返回来源页，并检查桌面和窄屏布局。从 Memos 编辑旧笔记后检查 Web 的创建时间排序，从 Memos 新增后刷新应可见；来源断连时请求失败，恢复后可继续访问。退出后旧 Cookie 不能访问客户端 API，机器令牌不能代替浏览器会话。登录后的可见文案、元素、布局与样式严格以原型为准，产品行为遵循 PRD。

Web 使用两栏布局，侧栏关键词、日历和标签在任意页面跳回时间线并组合筛选。归档入口位于用户菜单；详情路由 `/notes/{id}` 支持直接访问和登录后恢复，返回来源页时恢复筛选及浏览位置。卡片内编辑支持 Ctrl+Enter 保存、Esc 取消；附件与正文分别编辑，正文中的标签保持原样。

`web/test/browser.mjs` 使用正式 Web 和隔离 HTTP 来源，在真实 Chromium 中检查页面交互、附件、来源返回、冲突提示及迟到响应隔离。它从 `MOERISE_PLAYWRIGHT_MODULE` 指定的已安装 Playwright 模块加载浏览器运行时：

```bash
MOERISE_PLAYWRIGHT_MODULE=/absolute/path/to/playwright/index.mjs \
node --test web/test/browser.mjs
```

默认使用 Playwright 安装的 Chromium；设置 `MOERISE_PLAYWRIGHT_CHANNEL=chrome` 可复用本机 Chrome。

`web/test/settings-browser.mjs` 使用同一浏览器运行方式，检查设置保存、密钥移除、版本冲突、繁忙重试、时区与侧栏刷新、路由离开及退出后的迟到响应。它通过正式静态资源和同源代理加载设置模块。

`TestClientSettingsMemosAcceptance` 使用真实 Memos 和 Core 检查旧配置初始化、设置版本、模型保存生效、LLM 与向量资源隔离、Embedding 代次切换、重启及索引重建；通过 `go test -tags=integration ./tests -run '^TestClientSettingsMemosAcceptance$' -v -count=1` 执行。难以由浏览器稳定触发的排空超时、并发会话边界及旧队列密文迁移由 `internal/settings*_test.go` 和 `internal/feishu_queue_migration_test.go` 补充。

该流程补充 Node 测试中的路由、草稿幂等和同源代理检查。真实 Memos 的联合浏览器流程还需覆盖来源版本、原文读回及服务重启。

浏览器回归覆盖会话失效后的草稿及附件恢复、创建和编辑的原键原请求体重试、并发请求失效后编辑器解锁提交、自动分页及多个编辑器重排时输入焦点保留、同时间资源名跨页排序、正文原生附件图片及下载、失败提交刷新后操作恢复，以及侧栏聚合复用、月份切换取消、缓存到期与写入或页面重新获得焦点后的刷新。`web/test/markdown.test.mjs` 补充附件映射边界与 64 KiB 未闭合标记的解析耗时检查。

### 模型与索引联合流程

`scripts/check-models.mjs` 使用真实 Memos、Linux Core、正式 Web、Chromium 和可控模型服务。需要 Node.js 24、Docker Linux 引擎及已安装的 Playwright；脚本自行编译 Core 和 Web，Docker 首次运行可下载 Go 与 Memos 镜像及 Go 依赖。PowerShell 示例：

```powershell
$env:MOERISE_PLAYWRIGHT_MODULE = 'C:/path/to/playwright/index.mjs'
$env:MOERISE_PLAYWRIGHT_CHANNEL = 'chrome'
node scripts/check-models.mjs
```

省略 `MOERISE_PLAYWRIGHT_CHANNEL` 时使用 Playwright 安装的 Chromium。模型模拟器运行在宿主机，Core 通过 `host.docker.internal` 访问，Linux 引擎使用 `host-gateway` 映射。来源与 Core 使用独立 Docker 网络，发布端口只绑定回环地址。

流程覆盖两类模型独立启停、Web 保存与连接检查、版本冲突和错误脱敏、通用文档与查询协议、部分索引持久化、Core 重启续建、未变片段复用、Embedding 空间替换时阻止混用、LLM 变更保留向量代次，以及索引重建后的设置、密钥与浏览器会话。末尾检查模型关闭后的关键词搜索、笔记写入、退出及窄屏布局。轮询间隔为 1 秒，来源同步间隔为 5 秒；来源透传代理将请求起始时间至少间隔 150 毫秒，控制联合验收的并行流量，Memos 自身的限流规则继续生效。

每次创建 `.local/models-acceptance-<随机ID>/`，保存脱敏 `checks.json` 和桌面、窄屏截图。流程结束或失败后关闭浏览器、回收独立容器、数据卷与网络，并删除配置凭据、二进制及数据库。供应商异常格式、取消与并发边界由 `internal/embedding_test.go` 补充，迟到响应和设置冲突由 `web/test/settings-browser.mjs` 补充。既有百炼及快照恢复链路使用 `TestVectorIndexAcceptance`，持久设置与模型替换使用 `TestClientSettingsMemosAcceptance`。

### 真实 Compose 部署验收

`scripts/check-compose.mjs` 使用 Node.js 24 标准库、Docker CLI 和 Compose 2.24+，依次运行 bundled 与 external 两种拓扑。Docker Engine 与脚本必须位于同一台机器，并能运行 Linux 容器。脚本通过回环地址和随机空闲端口访问服务，不需要 npm 依赖、模型或飞书凭据。

```bash
node scripts/check-compose.mjs
node scripts/check-compose.mjs --skip-build
node scripts/check-compose.mjs --skip-build --keep
```

默认构建 Core 与 Web，镜像标签使用独立项目名。`--skip-build` 使用环境变量 `MOERISE_IMAGE_TAG` 指定的已有镜像标签，省略时使用 `stage34`；Core 与 Web 必须同时具有该标签。PowerShell 示例：

```powershell
$env:MOERISE_IMAGE_TAG = 'stage34'
node scripts/check-compose.mjs --skip-build --keep
```

每次执行生成 `.local/compose-acceptance-<随机ID>/`，从 `.env.example` 生成独立 `.env`，设置随机账户密码、PAT、机器令牌、分页密钥和状态加密主密钥。`MOERISE_ENV_FILE` 指向该文件的绝对路径，供 Compose 服务的 `env_file` 使用。所有 Compose 调用显式传入同一 `--project-name moerise-acceptance-<随机ID>` 和 `--env-file`，项目环境与宿主机的部署变量隔离。

两种场景分别创建全新账户与数据卷：

- bundled 使用 `memos` profile，通过 `compose up -d memos` 启动固定版本及摘要的官方 Memos；完成首个 ADMIN 账户、官方登录、PAT 创建及 `/api/v1/auth/me` 作者确认后启动 Core/Web。
- external 使用空 profile，先通过 `compose create moerise` 创建独立项目网络，再通过 `docker run` 启动相同官方 Memos 镜像。来源使用网络别名 `source`，Core 连接 `http://source:5230`；来源容器没有 Compose 项目标签，独立启停。

external 来源的镜像从 `compose --profile memos config --format json` 读取，与 bundled 共用 `services.memos.image` 的版本及摘要。

bundled 的容器与卷全部删除后才创建 external 场景。验收通过 Web 的同源 API 检查 Cookie、Origin、CSRF、原文创建与官方接口读回、编辑、归档、组合筛选、删除、纯文件附件及受保护读取、设置版本保存与过期版本拒绝。随后检查 Core/Memos 重启、Core/Web 强制重建、停止 Core 后运行 `rebuild-index` 对会话、设置、笔记和附件的保留，并导出备份，检查 `manifest.json`、SQLite 文件头和 SHA-256。

快照使用独立目录，目录权限为 0755、文件权限为 0644；包含 `.env` 的父目录仍为 0700。Core 只读挂载快照目录，UID 10001 可读取文件，该挂载只包含快照文件。备份后先保存不同的时区、回顾数量及新版本，再恢复快照；恢复后旧 Cookie 被拒绝，重新登录必须读到备份时的设置版本和值，笔记及附件继续从 Memos 读取。

来源停止后先请求真实笔记与附件 API，检查失败响应不返回缓存正文或凭据，再等待 Web `/health/ready` 返回 503，并检查 Core 与 Web `/health/live` 均返回 200。Core 就绪检查读取同步状态，默认同步间隔为 60 秒，因此降级检查允许等待 90 秒。来源恢复后继续使用原会话读取设置、笔记与附件。Web 的 `/api/ext/v1/notes` 与 `/mcp` 必须返回 404；external 场景还检查停止应用后独立 Memos 仍能读取原文。

默认在 `finally` 中删除独立来源容器，再执行独立项目的 `compose down --volumes`，删除脚本构建的专用镜像及临时凭据，仅留下无密钥的 `checks.json`。复用的镜像不删除。Windows 递归删除前检查绝对路径、仓库 `.local` 父目录、随机目录名及符号链接。检查失败或收到 SIGINT/SIGTERM 时也进入资源回收；回收错误以非零退出码报告。

Docker 命令使用异步子进程，并绑定中断信号及命令超时。子进程退出后才进入回收；忽略终止信号的子进程在 1 秒后强制结束，回收命令使用独立的超时。构建中断属于难以稳定由真实构建触发的分支，专项检查使用可控子进程覆盖退出等待、忽略 SIGTERM 和预先取消：

```bash
node --test scripts/test/compose-process.test.mjs
```

`--keep` 仅在全部检查成功后留下 external 场景，写入 `runtime.json`，字段为 `root`、`project`、`envFile`、`url`、`username`、`password`。浏览器使用 `url` 及该账户登录；该文件和 `.env` 含测试凭据，位于 Git 忽略目录。`checks.json` 保存检查名称、耗时及结果，不保存响应正文或凭据。浏览器检查结束后，在仓库根目录使用运行信息回收：

```powershell
$runtime = Get-Content -Raw '.local/compose-acceptance-<随机ID>/runtime.json' | ConvertFrom-Json
$env:MOERISE_ENV_FILE = $runtime.envFile
docker rm -f -v "$($runtime.project)-source"
docker compose --file compose.yaml --project-name $runtime.project --env-file $runtime.envFile down --volumes --remove-orphans
Remove-Item -LiteralPath $runtime.envFile, (Join-Path $runtime.root 'runtime.json')
```

若使用默认构建并加 `--keep`，还需删除 `moerise:<project>` 和 `moerise-web:<project>` 两个专用镜像。Docker Desktop 引擎若要求提升权限，脚本与清理命令均在可访问引擎的提升权限终端执行。

### 平台适配与持续集成

平台适配测试使用 HTTP 模拟服务验证主动发送的目标与 UUID、租户凭据、文字及图片回执、Webhook 签名与请求大小，以及明确拒绝、响应不可信和重定向处理。回执边界另测相同正文的不同通知，拒绝重复绑定、发送时间不符和会话不符的回执。

正文和图片的真实 Memos 流程可单独执行：

```bash
go test -tags=integration ./tests -run '^TestFeishuFullReviewDeliveryWithRealMemosAcceptance$' -v -count=1
```

该流程使用真实 Memos 和飞书平台模拟器，验证单次标题、长文按序分片及完整还原、三张原图（含纯图片笔记）、每篇标签日期尾注、普通文件跳过、图片响应丢失后的回执确认，以及重开状态库后续发。

真实飞书出站测试需显式指定配置，用可读的短样例查看实际格式：向应用接收者发送一条独立的 `「今日回顾」`、样例正文、两张原图及每篇笔记的标签日期尾注：

```bash
MOERISE_FEISHU_LIVE_CONFIG="$PWD/.local/config.json" \
go test -tags=integration ./tests -run '^TestFeishuLiveReviewAcceptance$' -v -count=1
```

测试读取应用消息核对标题、完整正文和标签日期尾注，并下载图片检查原始尺寸，同时验证状态库重开后的续发。它创建独立 Docker Memos 和临时状态。可选设置 `MOERISE_FEISHU_LIVE_WEBHOOK` 为包含 `url`、`secret` 的 JSON 文件路径，增加 Webhook 标题、正文、图片及尾注实发，上传图片共用应用凭据。子测试可用 `-run 'TestFeishuLiveReviewAcceptance/feishu-webhook$'` 单独执行。不设置 `MOERISE_FEISHU_LIVE_CONFIG` 时跳过测试。

CI 在 Linux 上执行模块校验、格式检查、静态检查、基础测试、无 cgo 构建、竞争检测、漏洞检查及真实 Memos 验收。漏洞检查也可单独运行：

```bash
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

## 检索评测

`scripts/evaluate-search.py` 通过相同 API 比较 `keyword`、`semantic`、`hybrid`。准备问题集，例如 `[{"query":"如何减少遗忘","expected":["memos/id"],"tags":[],"state":"NORMAL"}]`，然后运行：

```bash
python3 scripts/evaluate-search.py --questions .local/search-questions.json
```

脚本从配置中选择具备 `notes:read` 的凭据，读取索引状态，记录 Hit@5、MRR@5、中位延迟和 P95；无期望资源的问题仅记录结果。错误或模式降级返回非零退出码；报告文件使用独立路径，不保存笔记正文或凭据。合成向量仅用于协议验证，真实模型效果需使用实际问题和人工标注。

固定工程语料的实模型验收通过 `TestStage3LiveAcceptance` 执行。问题与期望章节保存在 `tests/testdata/stage3-questions.json`，Git 需能读取数据集指定的语料提交。它只采用配置中的 `embedding` 参数，自动创建独立 Memos、账号、产品状态、索引和服务进程，导入 67 条工程笔记；不连接配置中的 Memos。执行环境为 Linux，需可用的 Git、Docker CLI 与 Docker Engine，并能访问百炼：

```bash
MOERISE_STAGE3_CONFIG="$PWD/.local/config.json" \
MOERISE_STAGE3_OUTPUT="$PWD/.local/stage3-live.json" \
go test -tags=integration ./tests -run '^TestStage3LiveAcceptance$' -v -count=1 -timeout=15m
```

测量使用 1024 维、批量 20、模型并发 2，记录三种检索模式的 20 题命中、相关与漫步路径、首次索引、批量 20/10 对照、查询并发 1/2/4，以及服务进程的 RSS、峰值 RSS 和 CPU 时间。请求间隔控制来源流量，间隔不计入延迟；模型实际并发不能超过配置。报告仅保存公共工程语料的章节引用、摘要与检索结果，不保存凭据、向量或笔记正文，输出路径必须不存在。调用真实供应商会产生模型用量；未设置 `MOERISE_STAGE3_CONFIG` 时该验收自动跳过。

一万条、1024 维向量的本地计算与索引基准使用：

```bash
python3 scripts/benchmark-search.py --output .local/search-benchmark.json
```

Windows 或 macOS 主机可使用 Linux Go 容器：

```bash
python3 scripts/benchmark-search.py --docker-image golang:1.27.3-bookworm \
  --output .local/search-benchmark.json
```

工具默认 `GOMAXPROCS=2`，分进程测量首次索引批量 5/20、应用缓存冷启动、热候选扫描，以及 1/2/4 路语义和混合 Search 服务调用。可用 `--cpu` 调整运行线程，用可重复的 `--phase` 选择场景。数据库位于临时目录，结束后自动清理；JSON 保留工作负载、源码摘要、原始基准输出和 RSS 等指标。

`BenchmarkSemanticCache10000x1024` 的构建与预热不计入查询耗时，可用 `go test ./internal -run '^$' -bench '^BenchmarkSemanticCache10000x1024$' -benchmem` 单独执行。合成向量基准使用进程内模型与来源，不含远端模型和 Memos 网络成本；应用缓存冷启动不清理操作系统文件缓存。

`TestDiscoveryAcrossEntrypointsAcceptance` 覆盖 REST、MCP 的 Scope 与相同路径结果；显式阅读由共享 `SearchService.Open` 写入，机器 REST 拒绝阅读写入，MCP 不提供阅读写工具；队列中的飞书漫步结果按持久状态恢复发送。专项测试补充来源中途变化、随机读取请求数量、路径边界与阅读记录的重建及备份恢复。

## AI 评测

`TestStage4LiveAcceptance` 采用配置中的 `llm`，创建独立 Memos、账号、状态、索引与服务进程，导入 `tests/testdata/stage4-cases.json` 中的固定虚构语料。同步并核对来源后关闭服务进程，打开共享 App，直接调用 `AIService.Insight` 或 `AIService.Answer`，不通过机器 REST 或 MCP 生成。笔记创建时间、修改时间及归档状态通过官方 API 设置并读回核对。向量模型关闭，检索明确返回关键词和标签降级；来源请求按 125 毫秒间隔发送，间隔计入端到端延迟。

```bash
MOERISE_STAGE4_CONFIG="$PWD/.local/config.json" \
MOERISE_STAGE4_OUTPUT="$PWD/.local/stage4-live.json" \
MOERISE_STAGE4_CONCURRENCY=true \
go test -tags=integration ./tests -run '^TestStage4LiveAcceptance$' -v -count=1 -timeout=30m
```

测试校验数据集摘要。时间线案例通过问题、标签和日期筛选召回，期望来源写在 `expected.sources`，不使用 `AIRequest.names` 参数。`MOERISE_STAGE4_CASES` 可指定逗号分隔的案例 ID；`MOERISE_STAGE4_CONCURRENCY=false` 跳过附加并发测量，`true` 使用同一案例分别执行并发 1、2、4。

每次调用通过业务事件回调收集引用、内容和完成事件，检查来源版本、原文字节与行号、引用归属、实际使用来源、时间段和完成结构。报告逐例落盘，包含原始业务事件、完整业务结果、期望要点、检索与首段及总延迟、供应商用量、执行共享服务的测试进程 CPU 与 RSS。供应商缺少的用量为 `null`；缓存用量与输入缓存命中分别按供应商字段记录。报告输出路径必须不存在。内容需逐项对照原文，核对事实、遗漏、错引和无依据结论；字符串命中与模型自评仅作辅助。

`tests/ai_acceptance_test.go` 中的 `TestAISelectedSourceSharedServiceAndQueuedFeishuAcceptance` 采用真实 Memos、可控模型和飞书平台模拟器，验证机器 REST/MCP 拒绝生成且不调用模型、共享 `AIService` 的选定来源结果及已入队飞书结果的发送与去重。`TestAICachedDeliveryRebuildAndRestoreWithRealMemosAcceptance` 覆盖已完成生成缓存、索引重建、Store 重开、快照恢复、回执确认和来源修改后的发送限制。

多次执行的结果使用标准库脚本汇总：

```bash
python3 scripts/summarize-ai.py \
  --dataset tests/testdata/stage4-cases.json \
  --reports .local/stage4-live.json \
  --reviews .local/stage4-review.json \
  --output .local/stage4-summary.json
```

`--reports` 和 `--reviews` 均可指定多个文件。脚本核对固定问题、筛选、期望、来源快照与片段，保留每次尝试及报告摘要，为每例选择时间上最后一个完整有效结果；后续失败单独记录。供应商缺失用量保留 `null`，中位数采用标准中位数，P95 使用 nearest-rank。

审阅文件可为数组或包含 `reviews` 的对象，每条使用 `id`、`source_report`、`verdict` 和 `notes`。`source_report` 绑定报告文件名，可用 `result_sha256` 进一步绑定完整原始 `AIResult` 的规范 JSON 指纹；重新生成的结果须匹配对应审阅。汇总中的 `complete` 表示完整执行覆盖，内容评价另存 `agent_review`，人工评分栏独立保留。汇总拒绝覆盖已有文件。

## 接口与提交

API 字段定义位于 `internal/models.go`，路由位于 `api.go`，内嵌契约为 `openapi.json`。接口变化同步修改契约与 [API v1](API-V1.md)；数据库结构变化新增编号迁移，禁止改写已应用的 SQL。

依赖变化使用 `go mod tidy` 更新模块文件。保持改动范围完整、提交信息具体，并同步更新受影响的配置或操作说明。实现边界与文档维护规则见[开发约定](../AGENTS.md)。
