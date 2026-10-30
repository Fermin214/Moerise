# Moerise

Moerise 是基于 Memos 的开源自托管笔记产品，支持记录、阅读、回顾和思考。Moerise Core 提供笔记同步、文字与附件记录、每日回顾、飞书记录与定时推送，以及备份恢复。Memos 保存笔记原文与附件，并负责编辑和删除。

服务提供 Web、只读 REST/MCP 和飞书入口。Web 支持 Memos 账户登录、时间线、关键词和标签日期筛选、完整笔记管理、附件及持久设置。扩展页面与 Android 的开发安排见[路线图](docs/ROADMAP.md)。产品行为见[产品需求](docs/PRD.md)。

Core 已提供中文混合检索、相关笔记与语义漫步接口，见[搜索](docs/SEARCH.md)。百炼向量接入及索引构建见[向量模型](docs/EMBEDDING.md)，LLM 协议及历史生成任务恢复见 [AI](docs/AI.md)。

外部机器入口使用只读操作白名单。回顾计划、带权漫步和历史生成任务按路线图继续迁移；目标规则统一见 PRD。

## 可以使用的功能

- 在正式 Web 使用绑定的 Memos 账户登录，刷新页面和 Core 重启后保持登录，退出使该会话失效。
- 实时浏览本人正常笔记，按创建时间倒序分页；组合关键词、标签与创建日期筛选，通过独立入口管理归档笔记。
- 在卡片内编辑，使用 Ctrl+Enter 保存、Esc 取消；打开独立详情地址，返回来源页面，删除前二次确认。
- 保留原文空白、换行和正文标签创建私有笔记，支持纯图片和纯文件附件、受保护图片预览与下载；网络重试使用同一幂等键。
- 在 Web 设置回顾、推送时段、时区、模型、默认洞察 Prompt 和飞书渠道；保存后生效，凭据加密保管，重启和索引重建保留设置。
- 同步 Memos 0.31.x 中本人创建的正常与归档笔记，修复修改、归档和删除带来的索引变化。
- 通过独立的机器访问令牌读取笔记、查看同步状态和持久任务结果。
- 通过只读 REST 或 MCP 获取、列出和搜索本人笔记，读取已有回顾；写入 Scope 也不能开放写操作或生成。
- 通过绑定身份的飞书应用机器人，在专用私聊直接记录文字、图片与文件。
- 简单随机选取每日回顾，默认选取创建满 72 小时的笔记，排除最近 7 天已选内容；当天结果固定，支持多个推送时段。
- 通过飞书应用机器人或自定义机器人 Webhook 推送回顾：每次先单独发送一次 `「今日回顾」`，再逐篇发送完整正文和原图，末尾显示标签与创建日期。日期按配置时区显示，长文按序分片，重启后继续发送。不附加链接、片号或状态提示，普通文件不推送；没有可发送内容时保持静默。
- 服务重启后恢复同步任务；需要时重建索引或从备份恢复产品状态。
- 可选启用百炼模型，按段落分块并在后台分批构建向量索引，复用正文未变的向量并恢复中断进度。
- 通过 REST 或 MCP `semantic_search` 组合中文关键词、标签与语义检索，返回命中片段、行位置和原文链接。检索无需 LLM；向量模型关闭或故障时仍可搜索关键词与标签，响应包含降级原因。
- 通过 REST 或 MCP `get_related_memos` 查找相关笔记，排除自身并合并重复片段；复用文档向量，无需额外模型调用，原笔记与结果均复查来源。
- 通过 REST 或 MCP 继续话题、换角度或随机漫步，排除路径内笔记并降低历史阅读记录的优先级；外部读取不更新阅读历史。
- LLM Provider 支持 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages，供模型连接检查及历史任务恢复使用。

Moerise 的笔记接口会向 Memos 复查原文，来源服务不可用时返回错误。每个部署连接一个 Memos 实例和一个账号。

飞书记录方式、回顾参数和推送说明见[每日回顾](docs/DAILY-REVIEW.md)、[飞书入口](docs/FEISHU.md)与[调度说明](docs/NOTIFICATIONS.md)。

## 运行条件

- 官方 Memos 0.31.x 及对应账号的个人访问令牌（PAT），可由随附 Compose 部署，也可连接已有实例。
- 服务端支持 Linux 原生二进制和 Docker Linux 容器。
- 从源码构建需要 Go 1.27+；容器部署需要支持 Linux 容器的 Docker Engine 与 Compose 插件。
- 分别用于产品状态和可重建索引的持久目录。

## 安装与使用

容器部署在仓库根目录执行。先复制环境模板并启动官方 Memos：

```bash
cp .env.example .env
docker compose up -d memos
```

打开 `http://localhost:5231`，按 Memos 官方流程创建账户，在账户设置中创建 PAT。使用该 PAT 请求 `GET http://localhost:5231/api/v1/auth/me`，将响应 `user.name` 填入 `.env` 的 `MOERISE_MEMOS_USER`，PAT 填入 `MOERISE_MEMOS_TOKEN`。

在 `.env` 中填写稳定且唯一的 `MOERISE_MEMOS_INSTANCE_ID`，分别生成机器令牌、分页密钥与状态加密密钥：

```bash
openssl rand -hex 32     # MOERISE_ACCESS_TOKEN
openssl rand -hex 32     # MOERISE_CURSOR_SECRET
openssl rand -base64 32  # MOERISE_STATE_ENCRYPTION_KEY
```

填写后启动完整服务：

```bash
docker compose up -d --build
docker compose ps
curl --fail http://localhost:3000/health/ready
```

打开 `http://localhost:3000`，使用同一 Memos 账户登录。基础笔记操作无需模型或飞书配置；日常设置在 Web 保存后生效。Memos、产品状态、索引与备份使用独立数据卷。

连接已有 Memos 时，将 `.env` 中 `COMPOSE_PROFILES` 留空，填写容器可达的 `MOERISE_MEMOS_BASE_URL`、浏览器可达的 `MOERISE_MEMOS_PUBLIC_URL` 及本人 PAT、资源名和独立密钥，再执行相同启动命令。详细首启、外部 Memos、HTTPS、旧配置迁移、备份与升级见[运行与恢复](docs/OPERATIONS.md#容器部署)。

对外访问使用 HTTPS，并将 `MOERISE_PUBLIC_ORIGIN` 设置为浏览器访问的完整来源。Core 的 `/docs`、机器 REST/MCP 与飞书回调使用独立的 `8787` 回环端口，Web 只代理客户端 API。Web 独立构建见 [Web 指南](docs/WEB.md)，Linux 二进制运行见[运行与恢复](docs/OPERATIONS.md#二进制运行)，机器接口见 [API v1](docs/API-V1.md)。

## 文档

| 文档 | 用途 |
| --- | --- |
| [产品需求](docs/PRD.md) | 产品行为、入口职责、首个完整版本与长期候选 |
| [运行与恢复](docs/OPERATIONS.md) | 配置、部署、备份、恢复和版本回滚 |
| [Web](docs/WEB.md) | 登录、时间线、创建、同源代理和 Web 构建部署 |
| [API v1](docs/API-V1.md) | 客户端会话与原文创建、机器只读范围、分页和错误处理 |
| [每日回顾](docs/DAILY-REVIEW.md) | 选择参数、固定批次、近期去重与记录恢复 |
| [飞书入口](docs/FEISHU.md) | 应用配置、身份绑定、私聊记录与事件恢复 |
| [调度与发送队列](docs/NOTIFICATIONS.md) | 时区调度、发送尝试、结果不明处理与快照恢复 |
| [向量模型](docs/EMBEDDING.md) | 百炼模型配置、连接检查、分批调用与错误处理 |
| [AI](docs/AI.md) | 三种 LLM 协议、洞察、问答、时间线、引用与故障处理 |
| [搜索](docs/SEARCH.md) | 中文短查询、相关笔记、标签过滤、混合排名与来源复查 |
| [开发指南](docs/DEVELOPMENT.md) | 贡献代码所需的环境、工具、运行和验证方法 |
| [产品与架构](docs/PRODUCT-ARCHITECTURE.md) | 产品方向、模块职责、数据边界和技术依据 |
| [路线图](docs/ROADMAP.md) | 可用能力、产品阶段与后续开发任务 |

## 开源协议

Moerise 使用 [MIT 协议](LICENSE)，Copyright (c) 2026 Fermin214。
