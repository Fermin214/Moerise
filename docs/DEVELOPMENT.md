# Moerise 开发指南

本指南面向代码贡献者。以下命令在仓库根目录执行。模块职责见[产品与架构](PRODUCT-ARCHITECTURE.md)，功能范围见[路线图](ROADMAP.md)。

## 环境与工具

- Go 1.27+：构建服务，运行测试、格式化和静态检查，建议使用该系列的最新补丁版本。
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

`check-config` 只检查配置格式。来源版本与账号在同步时检查，运行状态见[运行检查](OPERATIONS.md#运行检查)。命令的 `--config` 参数优先于 `MOERISE_CONFIG_FILE`，两者均省略时读取 `.local/config.json`。

配置按内置默认值、JSON、非空 `MOERISE_*` 环境变量依次覆盖。`MOERISE_NOTIFICATIONS_TIMES=09:00,18:00` 设置每天两个时段，`MOERISE_REVIEW_COUNT` 控制每次条数；完整变量见[配置说明](OPERATIONS.md#配置)。修改部署环境后重启进程读取配置。

## 检查与验证

```bash
go test ./...
go vet ./...
gofmt -l main.go internal tests
```

格式检查应无输出，修正格式使用 `gofmt -w main.go internal tests`。基础测试使用 HTTP 模拟响应和临时数据库，无需 Memos、Docker、模型或真实凭据，覆盖分页中途失败、身份变化、任务合并与重启恢复、备份格式兼容、恢复损坏检查和写入互斥。记录测试补充并发幂等、请求与 Scope 边界、写入期间身份变化、不可信响应和跨作者资源冲突。

每日回顾业务流程验证简单随机选取、默认 72 小时等待期与 7 天重复排除期、归档开关、候选不足和空池，以及显式自定义时长的保留。固定批次检查并发读取、规则修改后沿用当天结果、关闭推送仍生成结果、来源变更、当地跨日，以及索引重建和备份恢复后的历史保留；选择规则不附加主题配额、纪念日或历史优先排序。

通知业务流程验证多时段共享批次、添加时段不重新选取、按时段去重、空池静默及来源故障重试。停机恢复仅补建当天已到达时段，不新建往日推送，已有队列继续恢复；另覆盖夏令时跳时与重复小时、平台拒绝、发送中断、回执复查、HTTP 权限及旧快照恢复后的结果确认。

外部 MCP 验证单条获取、分页列表、搜索和已有回顾读取；工具发现及直接调用均不能创建或更换回顾、记录打开和略过、更新阅读历史，即使令牌带写入 Scope。不存在的当天回顾返回明确结果，不在读取时创建。

飞书记录验证整条普通文字原样写入，包括首尾空白、换行、标签和看似命令的正文；图片及文件沿用附件链路，重复事件与发送失败不重复记录。回顾推送只发送笔记正文，不添加链接、日期、时间、片号或说明，不发送图片、附件或附件链接。验证正文按编码后的 18 KiB 请求预算直接分片、UTF-8 边界、按发送顺序重组完整正文、没有文字正文时静默跳过，以及应用机器人与 Webhook 的纯文字发送。逐片检查回执，结果不明时等待确认再发送后片；确认前片已送达后继续，确认未送达仅为该片换 UUID 重发。发送途中笔记变更时，跳过该笔记的剩余分片，不发送变更提示。

向量模型测试使用 DashScope HTTP 模拟服务，覆盖文档与查询参数、分批与返回位置、并发限制、有限重试、取消与超时，以及错误数量、维度和数值的拒绝。命令流程测试从配置文件运行连接检查，并验证旧版关闭模型的配置兼容、检查不打开数据库。供应商异常响应和并发边界使用专项测试，基础 Memos 验收不覆盖这些行为。

模型实调配置与命令见[向量模型](EMBEDDING.md#连接检查)。`check-embedding` 使用内置虚构文本；基础测试与 CI 无需真实模型凭据。

LLM 适配验证 Chat Completions、Responses 和 Anthropic Messages 的原生请求、完整 JSON 与流式 SSE、完成标记、用量、限流与故障重试、取消、并发等待、输出后失败不重放和敏感错误清理。命令流程验证 `check-llm` 真实读取流式响应、旧配置默认关闭，且不创建产品数据库。协议失败与流中断需用可控 HTTP 服务触发，真实模型连接检查不能稳定覆盖这些边界。模型配置与实调方法见 [AI](AI.md)。

证据流程验证指定来源与相关历史的边界、同文多来源、精确标签、状态及创建时间半开区间，以及过滤先于召回上限。专项测试覆盖转义后的 JSON 预算、UTF-8 裁切、微秒日期边界、无效引用和收集后来源变化；这些边界无法通过不稳定的模型输出可靠触发。证据字段和省略规则见 [AI](AI.md#证据与引用)。

洞察流程使用可控模型验证事实、解释、追问与引用的映射，检查输入总预算和完整条目输出。失败路径覆盖虚构引用、无引用事实、缺少结束记录、输出后的模型故障、回调取消，以及输出前和完成前的来源修改、删除与作者变化；这些情况均不能产生成功完成事件。

多条洞察检查只使用选定来源、相同正文的独立资源标识、跨笔记解释引用双方、实际使用来源，以及选定来源被过滤时返回证据不足。

问答流程验证问题和标签、状态、日期筛选传入共享检索，区分有据回答、部分证据不足、零命中及模型不可用；零命中不调用模型，普通问答不新增来源笔记。

时间线流程使用超过单次笔记上限的近期记录验证早期来源仍被召回，检查空时间段、半开区间、创建与修改及正文日期的区别、跨段引用索引和合并后的总预算。因预算丢失整个时间段时，结果必须指出缺失并使用证据不足状态。

AI 入口验证 `notes:read` 与 `ai:generate` 的组合权限、严格 JSON 边界、REST 完整结果和 SSE、MCP 可见性与非幂等提示。SSE 验证引用、内容、完成和错误事件的结构，以及业务失败、短写、flush 失败和客户端断开时的取消传播。生产启动路径共享同一模型提供商和并发限制，关闭模型时接口返回固定错误。

既有飞书 AI 任务的恢复流程验证生成结果先落库、事件重投和重启复用结果、生成中断终止、长结果按序分片，以及独立回执和结果不明处理。每片发送前复查来源，失效后只发送固定提示；状态恢复通过新增迁移与既有快照恢复流程验证。专用私聊的新文字全部走记录入口，不以 AI 命令触发生成。

向量索引流程测试覆盖分批持久化、模型失败、重启续建、归档及正文增量更新、删除清理、向量空间切换、构建期间投影变化、取消，以及索引重建和状态恢复。分块来源位置、UTF-8 边界、来源复查和错误向量的专项测试用于覆盖完整流程难以稳定触发的边界。真实 Memos 验收搭配 DashScope 模拟服务，验证服务进程自动构建、状态接口、故障恢复、空间替换和产品状态保留。

混合搜索验证覆盖请求 trim 后的 Unicode 码点与 UTF-8 字节边界、标签格式及精确 AND、默认状态和数量、未知字段 422、非 POST 405、Scope，以及 MCP 只读、幂等、非破坏提示。排名检查涵盖中文单字与双字短查询、英文数字整词、FTS 词项安全引用、三通道各前 100 片段、RRF k=60、笔记最高片段分数、最多 3 个片段和稳定排序。协议与分块位置见[搜索](SEARCH.md)。

搜索的模型与索引检查覆盖活动代次内存 float32 缓存复用、配置空间和投影修订失配、模型关闭及失败的显式降级、取消不降级，以及独立于 embedding 的关键词和片段构建。迁移检查涵盖索引迁移 003、旧投影启动补建、增量修改删除和重建。仅对完整流程难以可靠触发的边界补充专项测试。

相关笔记验证覆盖 REST 重复 `tags` 的精确 AND、默认值和边界、未知参数及重复 `state`/`limit` 的 422、非 GET 的 405、Scope，以及 MCP `RelatedRequest` 输入校验与只读工具提示。召回检查涵盖全量长正文词项、超长词项排除、共同标签数量排序、自身排除、最多三个片段与重复正文合并。语义检查覆盖同代次文档向量逐片段归一化、去重后等权平均并归一化、正余弦，以及无额外模型调用和三种降级原因。相关契约见[搜索](SEARCH.md#相关笔记)。

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
- REST 创建回顾、换组、打开与略过，MCP 只读共享已有批次；MCP 初始化、获取与分页、工具提示，以及写工具在发现和直接调用中的禁用；重启和恢复后沿用同一批次。
- 真实 Memos 与飞书平台模拟器联调：事件去重、其他身份隔离、无前缀文字原样保存、图片与文件记录，以及恢复业务执行前的入站事件快照后复查原资源。
- 同一机器人按时段主动推送固定批次，长文直接按正文分片，按发送顺序可重组完整正文，每片只含笔记文字，不添加链接、日期、时间、片号或说明，不发送图片、附件或附件链接；平台已收到但响应丢失后暂停后片，通过回执确认放行，重启复用发送进度；来源删除静默跳过，拆片后版本变化跳过该笔记剩余旧片，不发送变更提示，其他笔记继续，来源故障进入错误与重试流程。
- 中文单字、双字和短查询、语义改写与标签精确 AND，REST 与 MCP 共享搜索结果；DashScope 模拟服务覆盖语义可用、关闭及失败降级。
- 搜索候选原文修改、删除、作者越界、状态或标签变化后的丢弃与投影修正，搜索前后身份复查，以及来源故障使整请求失败且不返回缓存正文。
- 关键词与向量索引跨重启恢复，关闭模型仍可搜索；既有飞书搜索结果引用中的加密查询在恢复发送时重新搜索，发送前来源变化生效。
- 相关笔记在 REST 与 MCP 间的结果一致性；归档原笔记召回正常候选，返回原笔记资源与 URL、结果片段位置和语义降级原因。
- 相关笔记原文在输出前消失或越界返回 404 `SOURCE_NOT_FOUND`，处理中改变返回可重试的 409 `SOURCE_CHANGED`；候选变化丢弃并修正投影，任意来源故障不输出原笔记或部分结果；重启复用索引且不新增产品状态。

平台适配测试使用 HTTP 模拟服务验证主动发送的目标与 UUID、租户凭据、文字消息回执、Webhook 签名与请求大小，以及明确拒绝、响应不可信和重定向处理。

完整正文分片的真实 Memos 流程可单独执行：

```bash
go test -tags=integration ./tests -run '^TestFeishuFullReviewDeliveryWithRealMemosAcceptance$' -v -count=1
```

该流程使用真实 Memos 和飞书平台模拟器，验证带附件笔记只推送完整正文、按发送顺序重组分片、响应丢失后的单片确认及重开状态库后续发。模拟器用于可控故障验证；私聊入站的文字和图片直记需使用真实事件回调验证。

真实飞书出站测试需显式指定配置，会向配置的应用接收者实际发送合成回顾（两片纯文字全文）：

```bash
MOERISE_FEISHU_LIVE_CONFIG="$PWD/.local/config.json" \
go test -tags=integration ./tests -run '^TestFeishuLiveReviewAcceptance$' -v -count=1
```

测试读取应用消息回执核对完整正文，并验证状态库重开后的续发。它创建独立 Docker Memos 和临时状态，不连接或修改配置中的 Memos。可选设置 `MOERISE_FEISHU_LIVE_WEBHOOK` 为包含 `url`、`secret` 的 JSON 文件路径，增加 Webhook 纯文字实发。子测试可用 `-run 'TestFeishuLiveReviewAcceptance/feishu-webhook$'` 单独执行。不设置 `MOERISE_FEISHU_LIVE_CONFIG` 时跳过测试。

CI 在 Linux 上执行模块校验、格式检查、静态检查、基础测试、无 cgo 构建、竞争检测、漏洞检查及真实 Memos 验收。漏洞检查也可单独运行：

```bash
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

## 检索评测

`scripts/evaluate-search.py` 通过相同 API 比较 `keyword`、`semantic`、`hybrid`。准备问题集，例如 `[{"query":"如何减少遗忘","expected":["memos/id"],"tags":[],"state":"NORMAL"}]`，然后运行：

```bash
python3 scripts/evaluate-search.py --questions .local/search-questions.json
```

脚本从配置中选择具备 `notes:read` 的凭据，读取索引状态，记录 Hit@5、MRR@5、中位延迟和 P95；无期望资源的问题仅记录结果。错误或模式降级返回非零退出码；报告文件拒绝覆盖既有文件，不保存笔记正文或凭据。合成向量仅用于协议验证，真实模型效果需使用实际问题和人工标注。

固定工程语料的实模型验收通过 `TestStage3LiveAcceptance` 执行。问题与期望章节保存在 `tests/testdata/stage3-questions.json`，Git 历史需包含其中指定的语料提交。它只采用配置中的 `embedding` 参数，自动创建独立 Memos、账号、产品状态、索引和服务进程，导入 67 条工程笔记；不连接配置中的 Memos。执行环境为 Linux，需可用的 Git、Docker CLI 与 Docker Engine，并能访问百炼：

```bash
MOERISE_STAGE3_CONFIG="$PWD/.local/config.json" \
MOERISE_STAGE3_OUTPUT="$PWD/.local/stage3-live.json" \
go test -tags=integration ./tests -run '^TestStage3LiveAcceptance$' -v -count=1 -timeout=15m
```

复现验收报告时，将配置设为 1024 维、批量 20、模型并发 2。验收记录三种检索模式的 20 题命中、相关与漫步路径、首次索引、批量 20/10 对照、查询并发 1/2/4，以及服务进程的 RSS、峰值 RSS 和 CPU 时间。请求间隔控制来源流量，间隔不计入延迟；模型实际并发不能超过配置。报告仅保存公共工程语料的章节引用、摘要与检索结果，不保存凭据、向量或笔记正文，拒绝覆盖既有报告。调用真实供应商会产生模型用量；未设置 `MOERISE_STAGE3_CONFIG` 时该验收自动跳过。

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

语义漫步集成验收覆盖 REST、MCP 的 Scope 与相同路径结果，显式阅读由 REST 写入、MCP 不写阅读历史；既有飞书漫步结果的恢复发送复用持久队列。专项测试补充来源中途变化、随机读取请求数量、路径边界与阅读记录的重建及备份恢复。

## AI 评测

`TestStage4LiveAcceptance` 采用配置中的 `llm`，创建独立 Memos、账号、状态、索引与服务进程，导入 `tests/testdata/stage4-cases.json` 中的固定虚构语料。笔记创建时间、修改时间及归档状态通过官方 API 设置并读回核对。向量模型关闭，检索明确返回关键词和标签降级；来源请求按 125 毫秒间隔发送，间隔计入端到端延迟。

```bash
MOERISE_STAGE4_CONFIG="$PWD/.local/config.json" \
MOERISE_STAGE4_OUTPUT="$PWD/.local/stage4-live.json" \
MOERISE_STAGE4_CONCURRENCY=true \
go test -tags=integration ./tests -run '^TestStage4LiveAcceptance$' -v -count=1 -timeout=30m
```

测试校验数据集摘要。时间线案例通过问题、标签和日期筛选召回，期望来源写在 `expected.sources`，不使用洞察接口的 `names` 参数。`MOERISE_STAGE4_CASES` 可指定逗号分隔的案例 ID；`MOERISE_STAGE4_CONCURRENCY=false` 跳过附加并发测量，`true` 使用同一案例分别执行并发 1、2、4。

每个请求解析完整 SSE，检查来源版本、原文字节与行号、引用归属、实际使用来源、时间段和完成结构。报告逐例落盘，包含原始事件、完整业务结果、期望要点、检索与首段及总延迟、供应商用量、服务进程 CPU 与 RSS。供应商缺少的用量为 `null`；缓存用量与输入缓存命中分别按供应商字段记录。报告拒绝覆盖旧文件。内容需逐项对照原文，核对事实、遗漏、错引和无依据结论；字符串命中与模型自评仅作辅助。

`TestAISelectedSourceAcrossRESTMCPAndFeishuAcceptance` 与 `TestAICachedDeliveryRebuildAndRestoreWithRealMemosAcceptance` 采用真实 Memos、可控模型和飞书平台模拟器，覆盖跨入口结果、Scope、重投、已完成生成缓存、索引重建、Store 重开、快照恢复、回执确认和来源修改后的发送限制。

多次执行的结果使用标准库脚本汇总：

```bash
python3 scripts/summarize-ai.py \
  --dataset tests/testdata/stage4-cases.json \
  --reports .local/stage4-live.json \
  --reviews .local/stage4-review.json \
  --output .local/stage4-summary.json
```

`--reports` 和 `--reviews` 均可指定多个文件。脚本核对固定问题、筛选、期望、来源快照与片段，保留每次尝试及原报告摘要，为每例选择时间上最后一个完整有效结果；后续失败仍单独记录。旧时间线请求仅在已知数据摘要之间按 `names` 契约修正兼容。供应商缺失用量保留 `null`，中位数采用标准中位数，P95 使用 nearest-rank。

审阅文件可为数组或包含 `reviews` 的对象，每条使用 `id`、`source_report`、`verdict` 和 `notes`。`source_report` 绑定报告文件名，可用 `result_sha256` 进一步绑定完整原始 `AIResult` 的规范 JSON 指纹；重新生成的结果须匹配对应审阅。汇总中的 `complete` 表示完整执行覆盖，内容评价另存 `agent_review`，人工评分栏独立保留。汇总拒绝覆盖已有文件。

## 接口与提交

API 字段定义位于 `internal/models.go`，路由位于 `api.go`，内嵌契约为 `openapi.json`。接口变化同步修改契约与 [API v1](API-V1.md)；数据库结构变化新增编号迁移，禁止改写已应用的 SQL。

依赖变化使用 `go mod tidy` 更新模块文件。保持改动范围完整、提交信息具体，并同步更新受影响的配置或操作说明。实现边界与文档维护规则见[开发约定](../AGENTS.md)。
