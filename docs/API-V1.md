# Moerise API v1 规范

Moerise 服务端提供两组 RESTful API 以及一个 Model Context Protocol (MCP) 接口：
1. **客户端应用接口 (`/api/app/v1`)**：面向 Moerise Web 及未来的移动客户端，基于同源 Cookie 与 CSRF Token 鉴权。
2. **外部开放接口 (`/api/ext/v1`)**：面向外部脚本与第三方集成，基于 Bearer Token 鉴权，**严格只读**。
3. **MCP 协议接口 (`/mcp`)**：面向 Claude Desktop、Cursor 等支持 MCP 标准的外部 AI Agent，**严格只读**。

完整接口契约以服务内置的 `/openapi.json` 及交互式文档页面 `/docs` 为准。

---

## 一、客户端应用接口 (`/api/app/v1`)

客户端接口采用会话 Cookie 认证机制。所有写操作均强制校验 `Origin` 与 `X-CSRF-Token`。

### 1. 认证与会话管理

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `GET` | `/api/app/v1/capabilities` | 获取服务端能力描述（登录方式与服务健康状态）。 |
| `POST` | `/api/app/v1/session` | 提交 `username` 与 `password` 登录，返回用户信息与 `csrf_token` 并签发 Cookie。 |
| `GET` | `/api/app/v1/session` | 校验当前登录状态，返回用户身份、时区与会话过期时间。 |
| `DELETE` | `/api/app/v1/session` | 登出并销毁会话，清除浏览器 Cookie。 |

### 2. 笔记管理与时间线

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `GET` | `/api/app/v1/notes` | 分页拉取时间线笔记，支持按标签、日期、归档状态筛选，按创建时间倒序。 |
| `GET` | `/api/app/v1/notes/{memo_id}` | 获取单篇笔记最新详细内容与附件列表。 |
| `POST` | `/api/app/v1/notes` | 创建笔记，接收正文与附件，强制要求 `Idempotency-Key` 请求头。 |
| `PATCH` | `/api/app/v1/notes/{memo_id}` | 编辑笔记正文、更新标签、归档或恢复笔记，要求 `Idempotency-Key`。 |
| `DELETE` | `/api/app/v1/notes/{memo_id}` | 删除指定笔记。 |
| `GET` | `/api/app/v1/tags` | 获取所有使用中的 `#标签` 及其关联笔记统计计数。 |
| `GET` | `/api/app/v1/calendar` | 获取指定月份每天的笔记数量分布。 |
| `GET` | `/api/app/v1/notes/{memo_id}/attachments/{attachment_id}/content` | 受保护的媒体附件内容流，支持分段 Range 请求。 |

### 3. 检索、漫步与认知地图

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `POST` | `/api/app/v1/search` | 全文与语义检索，支持 `keyword`、`semantic` 与 `hybrid` 模式，返回相关度排序分页。 |
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
| `DELETE` | `/api/app/v1/chats/{chat_id}` | 删除指定对话。 |
| `POST` | `/api/app/v1/chats/{chat_id}/messages` | 向大模型发送消息，返回 Server-Sent Events (SSE) 流式回复。 |
| `POST` | `/api/app/v1/chats/{chat_id}/messages/{request_id}/cancel` | 主动停止正在生成的回复。 |
| `POST` | `/api/app/v1/chats/{chat_id}/messages/{request_id}/save` | 将选定的助手回复原样保存为 Memos 笔记。 |

### 6. 系统设置与诊断

| 方法 | 路径 | 功能说明 |
| --- | --- | --- |
| `GET` | `/api/app/v1/settings` | 读取当前系统设置与渠道能力矩阵。 |
| `PATCH` | `/api/app/v1/settings` | 版本化更新系统设置与敏感凭据。 |
| `GET` | `/api/app/v1/models` | 获取模型与向量索引当前构建进度与状态。 |
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
| `GET` | `/api/ext/v1/notes/{memo_id}/related` | `notes:read` | 基于高维向量相似度查找与指定笔记最相关的其他笔记。 |
| `GET` | `/api/ext/v1/reviews/daily` | `reviews:read` | 读取当天已生成的每日回顾结果（不主动触发生成）。 |
| `GET` | `/api/ext/v1/sync/status` | `sync:read` | 查看 Memos 同步水位与当前索引构建状态。 |

---

## 三、Model Context Protocol (`/mcp`)

Moerise 实现了 Anthropic 提出的 Model Context Protocol (MCP) 标准，使各类 AI 工具能够安全检索知识库。

- **安全红线**：MCP Server 仅暴露只读工具，绝不暴露创建、修改、删除笔记或更改系统配置的任何工具。
- **暴露的 MCP 工具**：
  - `search_notes`：多模式检索笔记。
  - `get_note`：通过资源名或 ID 获取单篇笔记内容。
  - `list_notes`：按时间线分页列出笔记。
  - `get_related_notes`：查找语义相似的相关笔记。
  - `get_daily_review`：读取当天的每日回顾内容。
