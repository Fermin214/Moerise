# Moerise

Moerise 是基于 Memos 的开源、自托管笔记产品，围绕记录、阅读、回顾和思考提供扩展能力。仓库提供 Moerise Core 服务，支持笔记同步、文本与附件记录、每日回顾、飞书交互与定时推送，以及备份恢复。笔记原文和附件保存在 Memos，编辑和删除使用 Memos。

产品按 **Core → 飞书 → 完整 Web 与一体化 Compose → Android** 交付。首个完整产品版本包含完整 Web 笔记管理、每日回顾、随机语义漫步、自然聊天式洞察与连续追问、二维认知地图、浏览器设置和通用模型配置。具体范围见[产品需求](docs/PRD.md)，实施顺序见[路线图](docs/ROADMAP.md)。

已有中文混合检索、相关笔记与语义漫步的接口见[搜索](docs/SEARCH.md)，百炼向量接入及索引构建见[向量模型](docs/EMBEDDING.md)，LLM 协议、带引用的分析、问答和主题时间线见 [AI](docs/AI.md)。这些接口是可复用的工程能力，Web 的产品体验按上述规划交付。

## 可以使用的功能

- 同步 Memos 0.31.x 中本人创建的正常与归档笔记，修复修改、归档和删除带来的索引变化。
- 通过独立的访问令牌读取笔记、查看同步状态或请求同步。
- 使用具备 `notes:write` Scope 的令牌创建私有笔记，支持标签、基础附件和持久幂等重试。
- 通过 REST 获取每日回顾、换一组，并保存打开与略过记录；外部 MCP 只读获取、列出和搜索笔记，读取已有回顾。
- 通过绑定身份的飞书应用机器人，在专用私聊直接记录文字、图片与文件。
- 简单随机选取每日回顾，默认等待 72 小时、排除最近 7 天；当天结果固定，支持多个推送时段。
- 通过飞书应用机器人或自定义机器人 Webhook 推送纯文字全文和原文链接，长文按序分片，重启后继续发送；不发送图片、附件或附件链接。
- 服务重启后恢复同步任务；需要时重建索引或从备份恢复产品状态。
- 可选启用百炼模型，按段落分块并在后台分批构建向量索引，复用正文未变的向量并恢复中断进度。
- 通过 REST 或 MCP `semantic_search` 组合中文关键词、标签与语义检索，返回命中片段、行位置和原文链接；无需 LLM，关闭向量模型或模型故障时仍可搜索关键词与标签，并明确返回降级原因。
- 通过 REST 或 MCP `get_related_memos` 查找相关笔记，排除自身并合并重复片段；复用文档向量，无需额外模型调用，原笔记与结果均复查来源。
- 通过 REST 或 MCP 继续话题、换角度或随机漫步，排除路径内笔记并降低最近阅读的优先级；REST 显式打开的阅读记录纳入产品状态备份。
- 可选接入 OpenAI Chat Completions、OpenAI Responses 或 Anthropic Messages，生成带原文引用的单条及多条洞察、笔记问答和主题时间线。REST 支持完整 JSON 与 SSE，MCP 共享业务结果。

Moerise 的笔记接口会向 Memos 复查原文，来源服务不可用时返回错误。每个部署连接一个 Memos 实例和一个账号。

飞书记录方式、回顾参数和纯文字推送说明见[每日回顾](docs/DAILY-REVIEW.md)、[飞书入口](docs/FEISHU.md)与[调度说明](docs/NOTIFICATIONS.md)。

## 运行条件

- 可访问的 Memos 0.31.x 实例，以及对应账号的个人访问令牌（PAT）。
- 服务端支持 Linux 原生二进制和 Docker Linux 容器。
- 从源码构建需要 Go 1.27+；容器部署需要支持 Linux 容器的 Docker Engine 与 Compose 插件。
- 分别用于产品状态和可重建索引的持久目录。

## 安装与使用

Linux 二进制的构建、配置和运行方法见[运行与恢复](docs/OPERATIONS.md#二进制运行)。使用容器时，可在获取源码后按以下 Compose 示例部署；命令在仓库根目录执行，使用 Bash：

```bash
mkdir -p .local/backups
cp config.example.json .local/config.json
```

编辑 `.local/config.json`，填写 Memos 地址、账号资源名和 PAT，并为 Moerise 生成独立的访问令牌和分页密钥。字段说明见[配置说明](docs/OPERATIONS.md#配置)。基础功能无需配置模型或飞书。

Linux 部署需先按[容器部署](docs/OPERATIONS.md#容器部署)设置配置和备份目录的挂载权限，再启动服务：

```bash
docker compose up -d --build moerise
```

启动后访问服务的 `/docs`，点击 **Authorize** 填入 Moerise 访问令牌即可调用接口。`/health/ready` 返回 200 表示已连接来源并完成首次全量同步。接口使用方法见 [API v1](docs/API-V1.md)。

随附 Compose 部署 Core，可通过 `local` profile 启动开发用 Memos，仅发布回环端口。完整 Web 与三组件一体化 Compose 的交付见[路线图](docs/ROADMAP.md#阶段三完整-web-与一体化-compose)。监听地址、对外访问、备份和升级见[运行与恢复](docs/OPERATIONS.md)。

## 文档

| 文档 | 用途 |
| --- | --- |
| [产品需求](docs/PRD.md) | 产品行为、入口职责、首个完整版本与长期候选 |
| [运行与恢复](docs/OPERATIONS.md) | 配置、部署、备份、恢复和版本回滚 |
| [API v1](docs/API-V1.md) | 访问范围、笔记创建与幂等重试、分页、同步任务和错误处理 |
| [每日回顾](docs/DAILY-REVIEW.md) | 选择参数、固定批次、近期去重与记录恢复 |
| [飞书入口](docs/FEISHU.md) | 应用配置、身份绑定、私聊记录与事件恢复 |
| [调度与发送队列](docs/NOTIFICATIONS.md) | 时区调度、发送尝试、结果不明处理与快照恢复 |
| [向量模型](docs/EMBEDDING.md) | 百炼模型配置、连接检查、分批调用与错误处理 |
| [AI](docs/AI.md) | 三种 LLM 协议、洞察、问答、时间线、引用与故障处理 |
| [搜索](docs/SEARCH.md) | 中文短查询、相关笔记、标签过滤、混合排名与来源复查 |
| [开发指南](docs/DEVELOPMENT.md) | 贡献代码所需的环境、工具、运行和验证方法 |
| [产品与架构](docs/PRODUCT-ARCHITECTURE.md) | 产品方向、模块职责、数据边界和技术依据 |
| [路线图](docs/ROADMAP.md) | 四个产品阶段、可复用工程基础、交付任务和完成标准 |

## 开源协议

Moerise 使用 [MIT 协议](LICENSE)，Copyright (c) 2026 Fermin214。
