# Moerise API v1 规范

Moerise 服务端提供两组 RESTful API 以及一个 Model Context Protocol (MCP) 接口：

1. **客户端应用接口 (`/api/app/v1`)**：面向 Moerise Web 及 Android 客户端，基于同源 Cookie 与 CSRF Token 鉴权。
2. **外部开放接口 (`/api/ext/v1`)**：面向外部脚本与第三方集成，基于 Bearer Token 鉴权，仅开放只读接口。
3. **MCP 协议接口 (`/mcp`)**：面向 Claude Desktop、Cursor 等支持 MCP 标准的外部 AI Agent，仅开放只读工具。

完整接口契约以服务内置的 `/openapi.json` 及交互式文档页面 `/docs` 为准。

`GET /api/app/v1/settings` 返回独立的 `channel_status`。`PATCH` 用 `settings.channel.kind` 选择定时回顾目的地，用 `secrets.qq_app_secret`、`secrets.app_secret`、`secrets.webhook_app_secret` 分别更换 QQ、飞书应用和 Webhook 图片上传密钥。读写都返回密钥配置状态。字段和增量更新示例见[系统设置](SETTINGS.md)。

---

## 一、客户端应用接口 (`/api/app/v1`)

客户端接口采用会话 Cookie 认证机制。登录请求校验 `Origin`；登录后的非只读请求还需校验 `X-CSRF-Token`。机器 Bearer Token 不用于客户端接口。

### 1. 认证与会话管理

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `GET` | `/api/app/v1/capabilities` | 获取登录方式、笔记能力、Core `version` 与统计转发状态 `telemetry: {"schema":1,"allowed":false}`。 |
| `POST` | `/api/app/v1/telemetry` | 用本人会话、同源和 CSRF 提交设备已同意分享的统计批次。 |
| `POST` | `/api/app/v1/session` | 提交 `username` 与 `password` 登录，返回用户信息与 `csrf_token` 并签发 Cookie。 |
| `GET` | `/api/app/v1/session` | 校验登录状态，返回用户身份、时区与会话过期时间。 |
| `DELETE` | `/api/app/v1/session` | 登出并销毁会话，清除浏览器 Cookie。 |

登录与会话读取均返回 `stargazing_enabled` 布尔值，供客户端控制观星入口；该字段与 Embedding 开关独立。

统计请求采用 `application/json`，上限 48 KiB、1–256 行，每行计数 1–65,535。Core 按平台每 24 小时最多尝试转发一次。返回 `204` 后客户端丢弃该批次。实例禁止时返回 `403 TELEMETRY_DISABLED`，超限或字段不符时返回 `422 INVALID_ARGUMENT`。字段表与计数口径见[统计说明](PRIVACY.md)和[版本 1 协议](../internal/telemetry/schema-v1.json)。

### 2. 笔记管理与时间线

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `GET` | `/api/app/v1/notes` | 从本地索引分页拉取时间线笔记，支持按标签、日期、归档状态筛选，按创建时间倒序。 |
| `GET` | `/api/app/v1/notes/{memo_id}` | 读取本地同步的单篇笔记详细内容与附件列表。 |
| `GET` | `/api/app/v1/sync/notes` | 按固定同步游标与日志序增量同步本地笔记变更与删除墓碑（支持 `full` / `delta` / `reset` 模式）。 |
| `POST` | `/api/app/v1/notes` | 创建笔记，接收正文与附件，强制要求 `Idempotency-Key` 请求头；直写 Memos 并即时落库索引，返回轻量化 `attachment_receipts` 摘要回执。 |
| `PATCH` | `/api/app/v1/notes/{memo_id}` | 编辑笔记正文、更新标签、归档或恢复笔记，要求 `Idempotency-Key`；直写 Memos 后即时刷新本地索引与变更日志。 |
| `DELETE` | `/api/app/v1/notes/{memo_id}` | 删除指定笔记，JSON 请求体需携带 `source_hash`；直写 Memos 并写入删除墓碑。 |
| `GET` | `/api/app/v1/tags` | 获取所有使用中的 `#标签` 及其关联笔记统计计数。 |
| `GET` | `/api/app/v1/calendar` | 获取指定月份每天的笔记数量分布。 |
| `GET`, `HEAD` | `/api/app/v1/notes/{memo_id}/attachments/{attachment_id}/content` | 受保护的媒体附件内容流，支持分段 Range 请求。 |

### 3. 检索、漫步与观星

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `POST` | `/api/app/v1/search` | 基于本地索引执行检索；`keyword` 走 FTS5 分词与标签交集，`semantic` 与 `hybrid` 使用向量索引融合召回并按相关度分页。 |
| `POST` | `/api/app/v1/walks/start` | 启动一次随机漫步，可指定起始笔记或由系统随机挑选起点。 |
| `POST` | `/api/app/v1/walks/next` | 提交走过的路径，基于语义关联抽取未访问过的下一篇漫步笔记。 |
| `GET` | `/api/app/v1/map` | 读取观星快照，包含完整点集 `points`（含 `excerpt` 摘要与 `create_time`）、语义主题 `topics` 和笔记关联 `links`；主题可包含语义子主题 `districts`。 |
| `POST` | `/api/app/v1/map/rebuild` | 排队重新生成星图与主题，返回 `202 {"status":"queued"}`；要求本人会话、同源与 CSRF 校验。 |

观星使用均衡 UMAP，每日在服务时区 03:00 按需检查并调度更新（普通变化不再高频率自动重算），未变笔记复用坐标；星系和星团沿用关键词命名。`stellar` 定义可复现的星体属性；`layout_frame` 标识坐标框架，主动重建或更换向量空间时变化；`update_state` 为 `building` 或 `unavailable` 时表示生成正在进行或失败，已发布快照继续可读。`unavailable_reason` 为 `layout` 表示布局服务不可用，为 `layout_incompatible` 表示布局服务版本不兼容。后台健康告警可单独出现，不表示一次重建失败。字段定义由 [OpenAPI 生成器](../internal/openapi_gen.go) 产出并经 `/openapi.json` 提供，布局说明见[观星](STARGAZING.md)。

`stargazing.enabled=false` 时，读取返回 `status: disabled`、`disabled_reason: stargazing` 和空点集；重建返回 `409 STARGAZING_DISABLED`。Embedding 关闭时对应 `disabled_reason: embedding` 与 `409 EMBEDDING_DISABLED`。关闭不删除已发布快照或向量。

### 4. 每日回顾

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `GET` | `/api/app/v1/reviews/daily` | 获取当天全天固定回顾计划及全部选中笔记。 |

### 5. 系统设置与诊断

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `GET` | `/api/app/v1/settings` | 读取系统设置与渠道能力矩阵。 |
| `PATCH` | `/api/app/v1/settings` | 版本化更新系统设置与敏感凭据。 |
| `GET` | `/api/app/v1/models` | 获取模型与向量索引构建进度与状态。 |
| `POST` | `/api/app/v1/models/embedding/check` | 用 `{version, settings?, secrets?}` 探测已保存或候选 Embedding 配置的连通性与维度。 |
| `POST` | `/api/app/v1/channels/check` | 用 `{version, target, settings?, secrets?}` 只读验证一个候选渠道的配置。 |

---

## 二、外部开放接口 (`/api/ext/v1`)

外部接口供自动化脚本使用，使用 HTTP 请求头 `Authorization: Bearer <MOERISE_ACCESS_TOKEN>` 鉴权。未配置 `access_tokens` 时，`/api/ext/v1` 与 `/mcp` 自动关闭并返回 404；启用时受限于只读白名单：

| 方法 | 路径 | 所需 Scope | 功能说明 |
| --- | --- | --- | --- |
| `GET` | `/api/ext/v1/notes` | `notes:read` | 分页读取绑定作者的笔记列表。 |
| `GET` | `/api/ext/v1/notes/{memo_id}` | `notes:read` | 获取单篇笔记最新原文与元数据。 |
| `POST` | `/api/ext/v1/search` | `notes:read` | 执行关键词、语义或混合搜索，默认 `hybrid`。 |
| `GET` | `/api/ext/v1/notes/{memo_id}/related` | `notes:read` | 结合关键词、标签及已有文档向量寻找相关笔记，不额外调用模型。 |
| `GET` | `/api/ext/v1/reviews/daily` | `reviews:read` | 读取当天已生成的每日回顾结果（不主动触发生成）。 |
| `GET` | `/api/ext/v1/reviews/{batch_id}` | `reviews:read` | 读取已有回顾批次，复查来源。 |
| `GET` | `/api/ext/v1/sync` | `sync:read` | 查看 Memos 同步水位与向量索引状态。 |
| `GET` | `/api/ext/v1/search/index` | `notes:read` | 查看向量构建与发布代次。 |
| `GET` | `/api/ext/v1/me` | `notes:read` | 查看实例绑定、凭据名称与 Scope。 |
| `GET` | `/api/ext/v1/jobs/{job_id}` | `sync:read` | 查看已有同步任务。 |
| `POST` | `/api/ext/v1/discovery/walk` | `notes:read` | 提交起点及路径，执行只读语义漫步。 |

---

## 三、Model Context Protocol (`/mcp`)

Moerise 实现了 Anthropic 提出的 Model Context Protocol (MCP) 标准，使各类 AI 工具能够安全检索知识库。

- **只读边界**：MCP Server 仅暴露只读工具，不提供创建、修改、删除笔记或更改系统配置的接口。
- **传输方式**：`/mcp` 使用无会话 Streamable HTTP，返回 JSON；调用方需支持远程 HTTP MCP 并携带 Bearer Token。
- **暴露的 MCP 工具**：
  - `semantic_search`：多模式检索笔记，要求 `notes:read`。
  - `get_memo`：通过 `name`（如 `memos/abc123`）获取笔记，要求 `notes:read`。
  - `list_memos`：分页列出笔记，要求 `notes:read`。
  - `get_related_memos`：查找相关笔记，要求 `notes:read`。
  - `semantic_walk`：随机开始或沿语义候选前进，要求 `notes:read`。
  - `get_review_batch`：通过 `batch_id` 读取已有批次，要求 `reviews:read`。
  - `read_daily_review`：读取已生成的当日回顾，要求 `reviews:read`。

## 四、请求与失败处理

- 创建笔记接收 `content` 和可选 `attachments`，正文最多 65,536 UTF-8 字节；标签写在正文中，由 Memos 原生解析。编辑需提交最新 `source_hash`，可更新 `content`、`state`、`attachments` 和 `remove_attachments`。
- 创建或编辑结果不明时，保留相同 `Idempotency-Key` 和完整请求体重试；同键不同内容返回冲突。删除要求来源摘要匹配。
- 聊天创建需 `request_id`；生成需 `request_id`、`version`、`kind`，以及适用的 `content` 和 `language`。同一请求 ID 的重试复用原 turn；保存回复需携带助手消息的 `version`。
- 模型连接检查需提交设置 `version`；设置修改后先重新读取版本。
- `GET /api/app/v1/admin/notifications` 可按 `kind`（`review` 或 `reply`）和 `status` 组合筛选。筛选后优先返回失败记录，同一状态按创建时间倒序，最多 100 条。状态为 `pending`、`running`、`sent`、`failed`；失败记录通过 `POST /api/app/v1/admin/notifications/{id}/retry` 和空对象请求体重试，见[通知调度](NOTIFICATIONS.md)。
- 错误响应统一为 `{"error":{"code":"...","message":"...","retryable":false,"request_id":"..."}}`。外部只读接口（`/api/ext/v1`、`/mcp`）在来源不可用时不返回缓存正文；当日回顾尚未生成时，机器 REST/MCP 返回 `REVIEW_NOT_FOUND`。
