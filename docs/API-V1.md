# Moerise API v1 规范

[English](API-V1.en.md)

Moerise 服务端提供两组 RESTful API 以及一个 Model Context Protocol (MCP) 接口：

1. **客户端应用接口 (`/api/app/v1`)**：面向 Moerise Web 及未来的移动客户端，基于同源 Cookie 与 CSRF Token 鉴权。
2. **外部开放接口 (`/api/ext/v1`)**：面向外部脚本与第三方集成，基于 Bearer Token 鉴权，**严格只读**。
3. **MCP 协议接口 (`/mcp`)**：面向 Claude Desktop、Cursor 等支持 MCP 标准的外部 AI Agent，**严格只读**。

完整接口契约以服务内置的 `/openapi.json` 及交互式文档页面 `/docs` 为准。

---

## 一、客户端应用接口 (`/api/app/v1`)

客户端接口采用会话 Cookie 认证机制。登录请求校验 `Origin`；登录后的非只读请求还需校验 `X-CSRF-Token`。机器 Bearer Token 不用于客户端接口。

### 1. 认证与会话管理

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `GET` | `/api/app/v1/capabilities` | 获取支持的登录方式与笔记能力描述。 |
| `POST` | `/api/app/v1/session` | 提交 `username` 与 `password` 登录，返回用户信息与 `csrf_token` 并签发 Cookie。 |
| `GET` | `/api/app/v1/session` | 校验登录状态，返回用户身份、时区与会话过期时间。 |
| `DELETE` | `/api/app/v1/session` | 登出并销毁会话，清除浏览器 Cookie。 |

### 2. 笔记管理与时间线

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `GET` | `/api/app/v1/notes` | 分页拉取时间线笔记，支持按标签、日期、归档状态筛选，按创建时间倒序。 |
| `GET` | `/api/app/v1/notes/{memo_id}` | 获取单篇笔记最新详细内容与附件列表。 |
| `POST` | `/api/app/v1/notes` | 创建笔记，接收正文与附件，强制要求 `Idempotency-Key` 请求头。 |
| `PATCH` | `/api/app/v1/notes/{memo_id}` | 编辑笔记正文、更新标签、归档或恢复笔记，要求 `Idempotency-Key`。 |
| `DELETE` | `/api/app/v1/notes/{memo_id}` | 删除指定笔记，JSON 请求体需携带 `source_hash`。 |
| `GET` | `/api/app/v1/tags` | 获取所有使用中的 `#标签` 及其关联笔记统计计数。 |
| `GET` | `/api/app/v1/calendar` | 获取指定月份每天的笔记数量分布。 |
| `GET`, `HEAD` | `/api/app/v1/notes/{memo_id}/attachments/{attachment_id}/content` | 受保护的媒体附件内容流，支持分段 Range 请求。 |

### 3. 检索、漫步与认知地图

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `POST` | `/api/app/v1/search` | 默认 `keyword`，按创建时间返回 Memos 实时筛选结果；`semantic` 与 `hybrid` 使用索引召回，按相关度分页。 |
| `POST` | `/api/app/v1/walks/start` | 启动一次随机漫步，可指定起始笔记或由系统随机挑选起点。 |
| `POST` | `/api/app/v1/walks/next` | 提交走过的路径，基于语义关联抽取未访问过的下一篇漫步笔记。 |
| `GET` | `/api/app/v1/map` | 读取 2D 认知地图最新预建快照（含全量点位投影坐标与标签）。 |

### 4. 每日回顾

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `GET` | `/api/app/v1/reviews/daily` | 获取当天全天固定回顾计划及全部选中笔记。 |

### 5. 上下文 AI 洞察

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `GET` | `/api/app/v1/chats` | 分页获取历史 AI 对话列表。 |
| `POST` | `/api/app/v1/chats` | 创建新对话会话，指定对话标题与绑定的笔记上下文材料。 |
| `GET` | `/api/app/v1/chats/{chat_id}` | 读取指定对话详情与完整消息记录。 |
| `PATCH` | `/api/app/v1/chats/{chat_id}` | 携带 `version` 更新标题或材料资源名 `names`。 |
| `DELETE` | `/api/app/v1/chats/{chat_id}` | 携带 `version` 删除指定对话。 |
| `GET` | `/api/app/v1/chat-materials` | 从 Memos 分页筛选材料，支持正常与归档笔记。 |
| `POST` | `/api/app/v1/chat-materials/resolve` | 提交材料资源名 `names`，复查原文并返回材料规模与来源摘要。 |
| `POST` | `/api/app/v1/chats/{chat_id}/messages` | 向大模型发送消息，返回 Server-Sent Events (SSE) 流式回复。 |
| `POST` | `/api/app/v1/chats/{chat_id}/messages/{request_id}/cancel` | 主动停止正在生成的回复。 |
| `POST` | `/api/app/v1/chats/{chat_id}/messages/{request_id}/save` | 将选定的助手回复原样保存为 Memos 笔记。 |

### 6. 系统设置与诊断

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `GET` | `/api/app/v1/settings` | 读取系统设置与渠道能力矩阵。 |
| `PATCH` | `/api/app/v1/settings` | 版本化更新系统设置与敏感凭据。 |
| `GET` | `/api/app/v1/models` | 获取模型与向量索引构建进度与状态。 |
| `POST` | `/api/app/v1/models/llm/check` | 发送探测请求测试 LLM 连通性。 |
| `POST` | `/api/app/v1/models/embedding/check` | 发送探测请求测试 Embedding 连通性与维度。 |

---

## 二、外部开放接口 (`/api/ext/v1`)

外部接口供自动化脚本使用，使用 HTTP 请求头 `Authorization: Bearer <MOERISE_ACCESS_TOKEN>` 鉴权。外部接口受限于只读白名单：

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

- **安全红线**：MCP Server 仅暴露只读工具，绝不暴露创建、修改、删除笔记或更改系统配置的任何工具。
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
- 模型连接检查需提交设置 `version`；设置修改后先重新读取版本。通知管理接口与确认请求见[通知调度](NOTIFICATIONS.md)。
- 错误响应统一为 `{"error":{"code":"...","message":"...","retryable":false,"request_id":"..."}}`。来源不可用时不返回缓存正文；当日回顾尚未生成时，机器 REST/MCP 返回 `REVIEW_NOT_FOUND`。
