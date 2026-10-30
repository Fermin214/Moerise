# Moerise API v1

原生客户端前缀为 `/api/app/v1`，机器 REST 前缀为 `/api/ext/v1`，MCP 位于 `/mcp`。客户端路由见 [client_api.go](../internal/client_api.go)，机器路由见 [api.go](../internal/api.go)，完整字段契约见 [openapi.json](../internal/openapi.json)。服务通过 `/openapi.json` 提供契约，通过 `/docs` 提供交互文档。

机器入口提供本人笔记读取、中文混合检索、相关笔记、语义漫步、已有回顾和同步状态读取。操作是否只读由服务端白名单判定；搜索和漫步的 POST 保持可用。

## 原生客户端

启用 `web.enabled` 后，客户端使用同源 Cookie。登录账户必须匹配绑定的 Memos 作者；客户端路径拒绝 Authorization，机器路径只接受独立 Bearer，不使用浏览器 Cookie。

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| GET | `/api/app/v1/capabilities` | 匿名获取密码登录方式与笔记入口状态；`ready` 表示入口启用，来源故障由实际请求反馈 |
| POST | `/api/app/v1/session` | 接收 `username`、`password`，验证官方登录与续期链路，返回身份、时区、会话到期时间和 `csrf_token` |
| GET | `/api/app/v1/session` | 核验本人身份，必要时刷新上游凭据，返回会话信息 |
| DELETE | `/api/app/v1/session` | 先撤销本地会话，再尝试上游退出，清 Cookie 并返回 204；上游不可用仍可退出 |
| GET | `/api/app/v1/settings` | 返回设置版本、日常配置、凭据配置状态、渠道能力及内置洞察 Prompt |
| PATCH | `/api/app/v1/settings` | 提交版本、日常配置和密钥操作，保存并生效，返回递增版本的设置视图 |
| GET | `/api/app/v1/notes` | 实时读取 Memos 本人笔记，默认 `state=NORMAL&order=created_desc&page_size=20` |
| GET | `/api/app/v1/notes/{memo_id}` | 返回来源复查后的最新 Note，不记录阅读 |
| POST | `/api/app/v1/notes` | 接收 `{content,attachments}`，必填 `Idempotency-Key`，首次 201、重放 200，均直接返回 Note |
| PATCH | `/api/app/v1/notes/{memo_id}` | 原文编辑、归档或恢复、添加或移除附件；提交 `source_hash` 和 `Idempotency-Key` |
| DELETE | `/api/app/v1/notes/{memo_id}` | 提交 `{source_hash}`，删除笔记，返回 204 |
| GET | `/api/app/v1/tags` | `state=NORMAL/ARCHIVED` 范围内的全部标签，返回 `{tags:[{tag,count}]}` |
| GET | `/api/app/v1/calendar` | 接收 `state`、必填 `month=YYYY-MM`，返回 `{month,days:[{date,count}]}` |
| GET、HEAD | `/api/app/v1/notes/{memo_id}/attachments/{attachment_id}/content` | 会话、作者及父笔记关联检查后的受保护文件流，支持单段 Range |
| GET | `/api/app/v1/admin/notifications` | 读取通知，支持单个 `status` 参数 |
| GET | `/api/app/v1/admin/notifications/{notification_id}` | 读取通知、尝试和确认详情 |
| POST | `/api/app/v1/admin/notifications/{notification_id}/resolve` | 接收 `decision`、`receipt`，调用通知回执核验和恢复服务 |

所有登录和状态变更请求要求 Origin 精确匹配 `web.public_origin`，拒绝缺失或 `null`；登录后的写请求还要求会话对应的 `X-CSRF-Token`。无有效本地会话时，退出只检查 Origin 并幂等清 Cookie。HTTPS Cookie 使用 `__Host-moerise_session`，回环 HTTP 使用 `moerise_local_session`，均具有 HttpOnly、SameSite=Lax 和 Path=/。不设置 Domain；HTTPS 还设置 Secure。

身份响应只包含 `instance_id`、`user.name`、`user.display_name`、`timezone`、`expires_at`、`csrf_token`。Memos PAT、访问和刷新凭据不返回客户端。登录有效期不超过登录时的刷新凭据到期时间；上游续期只更新服务端凭据，不延长登录期限，读取和业务响应不设置 Cookie。会话跨普通重启恢复，产品快照恢复会清空会话，索引重建保留会话。密码登录关闭或需要其他认证方式的来源返回 501 `PASSWORD_LOGIN_UNSUPPORTED`。

客户端列表接受 `state=NORMAL/ARCHIVED`、`page_size=1..100`、`page_token`，排序仅支持 `created_desc`。`query` 是基础关键词，最多 256 个 Unicode 码点和 1024 个 UTF-8 字节；重复 `tag` 参数按精确 AND 组合，最多 20 个，每个最多 100 个 Unicode 码点和 400 个 UTF-8 字节，兼容 `C++`、`R&D`、组合字符及 emoji 等原生标签；`date=YYYY-MM-DD` 按产品时区限定创建日期。各条件在服务端分页前组合，拒绝未知参数及除 `tag` 外的重复参数。

创建时间相同按资源名倒序，置顶和编辑不改变创建时间排序。列表直接从 Memos 取候选并复查来源，来源新增后刷新即可读取。游标绑定列表种类、来源、作者、状态、排序及筛选，与机器索引游标不可互换，索引重建不会使实时列表游标失效。来源在跨页读取中改变可返回 409 `SOURCE_CHANGED`；资源预算耗尽返回错误。标签和日历统计覆盖对应来源范围，不从客户端已加载页推算。

创建和编辑保留正文首尾空白、换行及全部 `#标签`，不接受独立标签数组。创建固定 PRIVATE。正文最多 65536 UTF-8 字节；正文非空白或至少保留一个合法附件，纯图片和纯普通文件均可保存，不补占位文字。新增 `attachments` 为 `{filename,type,content}` 数组，`content` 是 Base64 原始字节；单次最多十个，原始总大小不超过 8 MiB。

每次独立提交使用新键，超时、结果不明或重新登录后的同次重试使用原键及原请求体。同键不同请求返回 409 `IDEMPOTENCY_CONFLICT`；创建结果不明返回 503 `WRITE_OUTCOME_UNKNOWN`。持久幂等属于实例，不绑定某次浏览器会话。

Web 会话失效时隐藏私人界面，在页面内存中保存创建和编辑草稿、已读取的附件及原提交；同一账户重新登录后恢复。主动退出清空这些数据；草稿不写入浏览器持久存储。

编辑接收 `content`、`state`、新增 `attachments`、`remove_attachments` 及必填 `source_hash`；未提交的正文和状态保持原值，已有附件默认保留。十个附件限制只约束单次新增，来源已有更多附件仍可编辑、归档或减少附件。移除列表使用完整附件资源名，且必须真实关联父笔记。`state=ARCHIVED` 归档，`state=NORMAL` 恢复。修改正文或状态推进来源修改时间，创建时间保持原值。编辑和删除先复查来源版本，变化返回 409 `SOURCE_CHANGED`。Memos 更新没有原子条件写入，复查与写入之间的外部并发仍可能按最后一次写入生效。

附件移除先检查请求完成后的正文。Markdown 图片仍引用待删除附件时，在写入前返回 422 `ATTACHMENT_REFERENCED`；不合法的原生图片地址、HTML 引用或其他来源删除前置条件返回 422 `ATTACHMENT_DELETE_REJECTED`，两者均不可原样重试。Web 保留草稿并允许修正正文，可在同一请求中去掉图片引用和附件；普通链接、行内代码和代码块不按图片引用处理。

客户端 Note 的附件只包含 `name`、`filename`、`type`、`size`、`content_url`，`url` 为 Moerise 详情路径。图片和下载始终使用 `content_url`；该路径只接受资源标识，不接受上游地址。每次读取复查登录、作者、父笔记与附件关联，返回 `no-store`、`nosniff` 和安全文件名。PNG、JPEG、GIF、WebP、AVIF 内联，其余类型按 `application/octet-stream` 下载。支持 GET、HEAD 和单段 `Range: bytes=...`，多段或无效范围返回 416，单次读取上限 128 MiB。

Web 正文中的原生 `/file/attachments/{id}/{filename}` 引用按父笔记附件表转换为受保护的 `content_url`，未知或不属于该笔记的引用保持文字。侧栏标签按笔记状态、日历按状态和月份复用聚合结果，缓存有效期一分钟；写入、手动刷新笔记或页面重新获得焦点后刷新聚合，被替代的聚合请求会取消。

客户端沿用 `error.code/message/retryable/request_id`：401 表示会话失效，403 表示身份范围或请求边界错误，503 表示来源、配置或存储故障，不返回 Bearer 认证挑战。私人响应使用 `Cache-Control: no-store`。通知确认的 `resolved_by` 为登录用户资源名；其业务决策和回执要求见[通知说明](NOTIFICATIONS.md)。

### 产品设置

设置接口复用本人会话与账号检查，拒绝 Authorization 和查询参数。PATCH 还要求精确匹配的 Origin、会话对应的 `X-CSRF-Token`，并在读取请求体前检查会话有效性。机器 REST 和外部 MCP 无法读写产品设置。

GET 和 PATCH 的成功响应均为 `SettingsView`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `version` | integer | 设置版本，初始化为 1，保存成功递增 |
| `settings` | object | `timezone`、`review`、`notifications`、`embedding`、`llm`、`channel`、`insights` 七组日常设置 |
| `secrets` | object | 七种凭据各自的 `{configured: boolean}` 状态，不包含凭据原文 |
| `channel_capabilities` | object | 服务端按配置计算的 `text`、`images`、`record` 布尔能力 |
| `default_insight_prompt` | string | 内置洞察 Prompt，可用于恢复 `settings.insights.default_prompt` |

`review` 包含条数、等待时长、重复排除期及归档开关；`notifications` 包含启用状态、兼容单时段的 `time` 及多时段 `times`。模型配置含启用状态、协议、地址、模型及调用预算，均无 `api_key`；`channel` 包含类型、应用标识、用户标识及目标，无应用密钥、校验 Token、加密密钥或 Webhook 凭据。具体字段见 OpenAPI。

PATCH 请求体仅接受 `version`、`settings`、`secrets`，上限 128 KiB，Content-Type 必须为 `application/json`。`version` 必填且为正整数；至少提交一个设置字段或密钥操作。设置对象递归合并，数组整体替换；省略字段保持原值。所有层级拒绝重复字段、未知字段和显式 `null`，设置对象内也不能嵌入密钥或基础设施字段。

只提交 `notifications.time` 时替换有效时段列表；提交非空 `notifications.times` 时以列表为准，并将 `time` 同步为最早时段。空列表回退到 `time`，省略 `time` 时使用保存前的最早时段。GET 和 PATCH 响应的 `times` 均返回有效时段。

```json
{
  "version": 1,
  "settings": {
    "timezone": "Asia/Hong_Kong",
    "review": {"count": 3, "min_age_hours": 72, "recent_days": 7},
    "notifications": {"times": ["09:00", "18:00"]},
    "insights": {"default_prompt": "探索所选笔记的联系"}
  },
  "secrets": {
    "llm_api_key": {"action": "replace", "value": "new-secret"},
    "embedding_api_key": {"action": "keep"},
    "webhook_secret": {"action": "clear"}
  }
}
```

密钥名称仅允许 `llm_api_key`、`embedding_api_key`、`app_secret`、`verification_token`、`encrypt_key`、`webhook_url`、`webhook_secret`。操作对象只允许 `action` 和 `value`：`keep`、`clear` 禁止携带 `value`；`replace` 必须提供非空真实值，不能使用遮蔽字符。省略操作等同保持。

| HTTP 状态 | 错误代码 | 含义 |
| --- | --- | --- |
| 401 | `SESSION_UNAUTHORIZED` | 浏览器会话缺失或失效 |
| 403 | `FORBIDDEN`、`ACCOUNT_NOT_BOUND`、`ORIGIN_REJECTED`、`CSRF_REJECTED` | 身份、Origin 或 CSRF 不符合要求 |
| 409 | `SETTINGS_CONFLICT` | 版本过期，重新读取并基于新版本修改 |
| 415 | `UNSUPPORTED_MEDIA_TYPE` | 请求体不是 JSON |
| 422 | `INVALID_ARGUMENT` | 请求超限、格式或字段错误、版本缺失或非正数、修改为空 |
| 503 | `SETTINGS_BUSY`、`SETTINGS_UNAVAILABLE` | 应用繁忙或尚未配置状态加密密钥；会话、来源及存储故障沿用对应服务错误 |

失败不发布部分配置。设置持久化、生效顺序、渠道能力及队列兼容规则见[产品设置](SETTINGS.md)。

## 资源与身份

一个服务部署绑定一个 Memos 实例和一个账号。资源主键使用 `instance_id + user + name`，其中 `name` 沿用 Memos 的 `memos/{id}`，账号使用 `users/{id}`。账号资源名来自 `/api/v1/auth/me`，可以包含用户名，不能假设为整数。

服务索引和输出范围为绑定账号本人创建的正常及归档笔记。其他账号的公开笔记和管理员可直接管理的其他作者笔记，均在此范围之外。身份绑定保存在产品数据库中。更换数据源地址或账号时使用新的 `instance_id` 和数据卷。

扩展入口使用独立的 Bearer 凭据。Memos PAT 仅由服务端适配层读取。

| Scope | 允许操作 |
| --- | --- |
| `notes:read` | 读取身份、笔记列表和单条笔记，执行搜索、查找相关笔记及漫步 |
| `sync:read` | 读取同步状态和任务结果 |
| `reviews:read` | 读取已有当日回顾和指定批次 |

服务端先验证机器 Bearer，再检查操作白名单及读取 Scope。缺少凭据或凭据无效返回 401 `UNAUTHENTICATED`；已认证但不在白名单中的操作统一返回 403 `FORBIDDEN`，不解析写入参数、不调用来源或模型。`notes:write`、`sync:write`、`reviews:write`、`discovery:write` 和 `ai:generate` 均不能增加外部操作。

机器 REST 不提供笔记写入、手动同步、创建或更换回顾、阅读标记、通知管理、洞察或问答生成。`/api/ext/v1/notifications` 及其子路径、`/api/ext/v1/insights` 和 `/api/ext/v1/answers` 也遵循上述拒绝规则。未列入白名单的方法和路径返回 403。

## 路由

| 方法 | 路径 | Scope | 结果 |
| --- | --- | --- | --- |
| GET | `/health/live` | 无 | 进程存活与服务版本 |
| GET | `/health/ready` | 无 | `ready` 或 `degraded` |
| GET | `/api/ext/v1/me` | `notes:read` | 实例、账号、凭据名称、Scope、时区 |
| GET | `/api/ext/v1/notes` | `notes:read` | 已索引笔记的分页结果，逐条复查来源 |
| GET | `/api/ext/v1/notes/{memo_id}` | `notes:read` | 直接复查并读取一条来源笔记 |
| GET | `/api/ext/v1/notes/{memo_id}/related` | `notes:read` | 200，返回原笔记、相关结果与语义使用状态 |
| POST | `/api/ext/v1/search` | `notes:read` | 200，返回混合搜索结果与语义使用状态 |
| GET | `/api/ext/v1/search/index` | `notes:read` | 向量索引代次与就绪状态，结构同同步状态的 `vector_index` |
| POST | `/api/ext/v1/discovery/walk` | `notes:read` | 200，返回一个漫步结果及后续路径 |
| GET | `/api/ext/v1/sync` | `sync:read` | 水位、完整同步时间、来源状态、数量、任务统计与向量索引进度 |
| GET | `/api/ext/v1/jobs/{job_id}` | `sync:read` | 持久任务与执行结果 |
| GET | `/api/ext/v1/reviews/daily` | `reviews:read` | 读取已有当日批次；尚未创建时返回 404 |
| GET | `/api/ext/v1/reviews/{batch_id}` | `reviews:read` | 读取指定批次，200 |
| POST | `/hooks/feishu` | 飞书平台凭据与身份校验 | URL 验证或事件持久入队应答，200 |

来源连接成功、全量同步完成且工作循环存活时，就绪检查返回 200；其他情况返回 503。存活检查适合区分进程状态与来源故障。

启用 `embedding` 时，同步状态包含 `vector_index`，关闭时省略。`active` 是已发布代次，`building` 是构建中的代次，无对应代次时为 `null`；两者包含模型、维度、分块版本、向量空间摘要、投影修订号，以及笔记数、片段数、已完成片段数和最近错误代码。`projection_revision` 随投影变化递增；`ready=true` 表示已发布代次与配置和投影修订一致，来源连接和查询是否可用需单独检查。向量索引构建和模型故障不影响基础就绪检查。构建与恢复规则见[向量索引](EMBEDDING.md#分块与向量索引)。

## 笔记与分页

列表参数：

| 参数 | 默认值 | 约束 |
| --- | --- | --- |
| `state` | `NORMAL` | `NORMAL`、`ARCHIVED` |
| `page_size` | 20 | 1 至 100 |
| `page_token` | 空字符串 | 服务返回的不透明游标，最多 4096 字符 |

笔记按 `update_time DESC, name DESC` 排序，使用键值分页。游标通过 HMAC 签名，并绑定实例、账号、归档状态和索引代次。重建索引使分页游标失效，调用方从第一页重新读取。

列表从本地投影确定候选资源，再请求 Memos 获取最新正文并复查作者。来源已消失时删除对应投影，来源已归档时移出正常列表。单页可能少于 `page_size`，甚至为空；调用方应以 `next_page_token` 是否为空判断分页结束。

投影会随同步和来源复查变化，分页不提供跨请求的固定快照。需要稳定集合的业务应在服务内部建立自己的批次。

```http
GET /api/ext/v1/notes?state=NORMAL&page_size=20
Authorization: Bearer <reader-token>
```

响应包含 `notes` 和 `next_page_token`。单条资源路径中的 `memo_id` 是 `memos/{id}` 的标识部分，`job_id` 是任务 UUID。

附件数组沿用 Memos 返回的附件元数据。`source_hash` 包含正文、状态、标签、附件和关系等来源信息；正文摘要单独保存在索引中，供后续向量任务判断正文变化。

来源身份或版本无法验证、Memos 连接失败时，读取接口返回 503，不以缓存正文替代来源复查。

## 搜索

```http
POST /api/ext/v1/search
Authorization: Bearer <reader-token>
Content-Type: application/json

{"query":"如何整理灵感","tags":["工作/灵感"],"state":"NORMAL","limit":10}
```

`SearchRequest` 仅接受以下字段；未知字段及参数不合法返回 422 `INVALID_ARGUMENT`，非 POST 方法返回 403 `FORBIDDEN`。

| 字段 | 必填 | 默认值与约束 |
| --- | --- | --- |
| `query` | 是 | 字符串，trim 后非空，最多 256 个 Unicode 码点且最多 1024 个 UTF-8 字节 |
| `mode` | 否 | 默认 `hybrid`，可选 `keyword`、`semantic`；语义模式不可用时返回错误 |
| `tags` | 否 | `[]`；输入最多 20 项，使用字母、十进制数字、下划线、连字符和斜杠分段的标签格式，去重排序，精确 AND 过滤 |
| `state` | 否 | `NORMAL`；可选 `NORMAL`、`ARCHIVED`、`ALL` |
| `limit` | 否 | 10；整数，1 至 20 |

所有召回通道共用作者、状态和标签过滤。响应为 `SearchResponse`：

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `query` | string | trim 后的查询 |
| `mode` | string | 实际模式；混合模式语义降级后为 `keyword` |
| `results` | `SearchHit[]` | 排名后的笔记，无命中时为 `[]` |
| `semantic` | object | `used: bool`、`reason: string`；使用语义时 reason 为空字符串，否则为 `EMBEDDING_DISABLED`、`INDEX_NOT_READY` 或具体模型错误代码 |

每个 `SearchHit` 包含 `note: Note`、`score: number` 和最多 3 项的 `matches: SearchMatch[]`。每个片段含 `ordinal: int`、`byte_start: int`、`byte_end: int`、`line_start: int`、`line_end: int`、`content: string` 和 `signals: string[]`；signals 仅包含 `keyword`、`semantic`、`tag`。字节位置对应原始正文 UTF-8 编码，使用从 0 开始的半开区间；行号从 1 开始，分块规则见[搜索](SEARCH.md)。`note` 使用来源复查后的正文和原文 URL。

关键词 BM25、标签匹配和正余弦语义分别取前 100 个片段，按 RRF（k=60）融合后再按笔记合并，笔记分数取最高片段分数，每条最多返回 3 个片段，排序稳定。模型关闭、索引未就绪或模型失败时降级为关键词与标签搜索；请求取消不降级。`score` 用于融合排名，不能用作余弦值或置信度。

搜索前复查 Memos 版本与身份，返回前逐条读取候选原文；修改、删除、作者越界或不再满足过滤条件的候选丢弃并修正投影，最后再次复查身份。来源故障使整个请求失败，不输出缓存正文。分词、内存缓存、迁移与降级规则见[搜索](SEARCH.md)。

显式 `keyword` 模式不调用模型，语义原因为 `KEYWORD_MODE`；`semantic` 模式不使用关键词通道，模型关闭、未就绪或失败时返回 503 和明确原因。默认 `hybrid` 保留自动降级行为。

## 语义漫步

`POST /api/ext/v1/discovery/walk` 接受 `name`、`mode`、`path`、`include_archived`；默认 `continue`，`angle` 换角度，`random` 随机选择且允许省略起点。路径输入最多 100 项，返回一个 `item`、后续 `path`、`mode`、`exhausted`、`warnings`。漫步只读，使用 `notes:read`；模型关闭或向量未就绪时只有随机模式可用。起点变化、来源故障及最近阅读加权规则见[语义漫步](SEARCH.md#语义漫步)。

## 相关笔记

```http
GET /api/ext/v1/notes/example/related?tags=园艺&tags=随记&state=NORMAL&limit=10
Authorization: Bearer <reader-token>
```

`memo_id` 为原笔记 `memos/{id}` 的标识部分，资源名须匹配 `^memos/[a-zA-Z0-9_-]+$`。仅接受以下查询参数：

| 参数 | 默认值与约束 |
| --- | --- |
| `tags` | `[]`；重复字段传数组，例如 `?tags=园艺&tags=随记`；输入最多 20 项，格式、去重排序和精确 AND 过滤同搜索 |
| `state` | `NORMAL`；可选 `NORMAL`、`ARCHIVED`、`ALL`，仅允许出现一次 |
| `limit` | 10；整数，1 至 20，仅允许出现一次 |

未知参数、重复 `state`/`limit`、非法资源名或参数返回 422 `INVALID_ARGUMENT`；非 GET 方法返回 403 `FORBIDDEN`。状态和显式标签只过滤候选，不过滤原笔记，因此可以从归档原笔记查找正常候选。

响应 `RelatedResponse` 包含 `source: Note`、`results: SearchHit[]` 和 `semantic: SearchSemantic`；无相关结果时 `results=[]`。命中字段、字节区间、行号和分数含义复用搜索契约。关键词从全量原文提取，标签通道匹配共同标签，语义复用文档向量；不将原文截为搜索短查询，不调用查询模型或 LLM。语义降级原因限定为 `EMBEDDING_DISABLED`、`INDEX_NOT_READY`、`NO_SOURCE_VECTORS`，成功使用语义时 `used=true`、`reason=""`。算法见[相关笔记](SEARCH.md#相关笔记)。

先验证来源版本与身份，再读取原笔记并修正投影；候选逐条原文复查同搜索。输出前再次读取原笔记、核实来源摘要与身份：原笔记消失或作者越界返回 404 `SOURCE_NOT_FOUND`，处理中改变返回 409 `SOURCE_CHANGED` 且 `retryable=true`。任意来源故障使整个请求失败，不输出原笔记或部分结果。

## 飞书事件

应用机器人回调入口为 `/hooks/feishu`，仅在应用凭据完整时启用；使用平台事件 Token 和可选 Encrypt Key 验证，不使用 Moerise Bearer 凭据。绑定使用者的单聊消息持久入队后返回 `{"code":0}`；URL 验证返回 `{"challenge":"..."}`。文字消息全部原样记录为笔记，不解析命令。重复消息沿用同一记录操作，其他身份和群聊不进入业务。事件配置、记录方式及恢复语义见[飞书入口](FEISHU.md)。

## 每日回顾与 MCP

机器 REST 只读取已有回顾批次。当天没有批次时返回 404 `REVIEW_NOT_FOUND`，读取操作不创建批次、不更换内容、不更新打开或略过记录。

响应直接返回 `ReviewBatch`，含 `id`、`date`、`timezone`、`rule_version`、`sequence`、`created_at` 和有序 `items`。条目含 `name`、`selected_source_hash`、`selected_at`、独立的 `opened_at` 与 `skipped_at`、`unavailable`，以及可用时的最新 `note`。来源删除或作者变化时保留条目位置，`unavailable=true` 并省略 `note`。选择、参数和恢复规则见[每日回顾](DAILY-REVIEW.md)。

扩展 MCP 位于 `/mcp`，使用官方 Go MCP SDK 的无会话 Streamable HTTP 传输，返回 JSON。客户端对该 URL 发送 MCP 请求，使用与 REST 相同的 Moerise Bearer 凭据；请求体上限为 64 KiB。协议初始化、请求头和错误按 MCP 协议处理，业务错误使用工具结果的 `isError` 及包含 `code`、`message`、`retryable` 的 JSON 文本。MCP GET、DELETE 不提供事件流或会话管理。含跨站来源的请求受到同源检查。

| 工具 | Scope | 行为 |
| --- | --- | --- |
| `get_memo` | `notes:read` | 读取 `name` 指定的本人笔记，返回最新 `Note` 全文与附件 |
| `list_memos` | `notes:read` | 按 `state`、`page_size`、`page_token` 分页读取，返回 `notes` 和 `next_page_token` |
| `semantic_search` | `notes:read` | 只读、幂等、非破坏；参数为 `SearchRequest`，结构化结果为 `SearchResponse` |
| `get_related_memos` | `notes:read` | 只读、幂等、非破坏；参数为 `RelatedRequest`，结构化结果为 `RelatedResponse` |
| `semantic_walk` | `notes:read` | 只读、幂等、非破坏；参数为 `WalkRequest`，结构化结果为 `WalkResponse`，随机模式可返回不同笔记 |
| `read_daily_review` | `reviews:read` | 读取已有当日批次；不存在时返回 `REVIEW_NOT_FOUND`，不创建批次 |
| `get_review_batch` | `reviews:read` | 读取指定 `batch_id` |

外部 MCP 仅提供只读工具，不支持创建笔记、创建或更换回顾、记录打开或略过、更新阅读历史。令牌中的写入 Scope 和 `ai:generate` 不增加 MCP 工具，工具发现和直接调用均执行只读限制。服务端按读取 Scope 注册工具，`readOnlyHint` 只用于展示提示。REST 与 MCP 共享只读业务服务，均不能通过机器凭据写入产品状态。

`get_memo` 接受 `{"name":"memos/example"}`，读取前验证来源账户，返回前获取 Memos 原文；不存在或不属于绑定作者时返回 `SOURCE_NOT_FOUND`。`list_memos` 接受可省略的 `state`（默认 `NORMAL`，也支持 `ARCHIVED`）、`page_size`（默认 20，范围 1—100）、`page_token`。它复用 REST 笔记列表的分页与原文复查，同一实例、作者、索引代次及状态下的游标可在 REST 与 MCP 间互用；无效游标返回 `INVALID_PAGE_TOKEN`。来源故障使获取和列表请求失败，不返回缓存正文。两个工具均只读、幂等、非破坏，不更新阅读历史。

`semantic_search` 仅对具备 `notes:read` 的凭据可见及可调用，设置 `readOnlyHint=true`、`idempotentHint=true`、`destructiveHint=false`。REST 和 MCP 复用 `SearchService`，搜索无需 LLM；来源复查引起的派生投影修正不改变产品记录。

`get_related_memos` 使用相同 Scope 可见性及工具提示。`RelatedRequest` 为 JSON 对象，例如 `{"name":"memos/example","tags":["园艺"],"state":"NORMAL","limit":10}`；`name` 必填，格式与相关 REST 资源名一致，其他字段的默认值及约束同相关查询参数。未知字段或非法参数以 `isError=true` 和错误文本返回；业务错误文本为包含 `code`、`message`、`retryable` 的 JSON。成功结构化输出为 `RelatedResponse`。

## 同步任务

`GET /api/ext/v1/sync` 读取同步状态，`GET /api/ext/v1/jobs/{job_id}` 返回 `{"job": Job}`。同步由服务端任务队列执行，机器 REST 不接受手动入队。

`mode` 支持 `incremental` 和 `full`。全量任务也用于修复删除、归档变化和时间回溯修改。

每个实例最多保留一个等待中的同步任务；重复请求返回该任务，全量请求可提升其模式。执行期间到达的新请求形成后续任务。任务按顺序执行，不并行覆盖投影。

| 状态 | 含义 |
| --- | --- |
| `pending` | 等待执行或按退避时间重试 |
| `running` | 执行中，按页更新租约 |
| `succeeded` | 执行完成，或触发请求已合并到另一个持久任务 |

合并任务的 `result.merged_into` 指向承接任务。调用方沿该标识查询结果。普通成功结果包含 `mode`、`observed`、`changed`、`removed`。

失败保留任务标识、尝试次数、错误代码和下一次执行时间。服务重启将遗留执行任务转为全量修复，或合并到已有等待任务。最近 500 条完成记录保留在数据库中，等待和执行任务不参与历史清理。

## 时间与错误

接口时间使用带时区的 ISO 8601 字符串；`available_at` 和 `lease_until` 使用 Unix 秒。配置时区用于产品日期与调度。

错误结构统一为：

```json
{
  "error": {
    "code": "UPSTREAM_UNAVAILABLE",
    "message": "Memos is unavailable",
    "retryable": true,
    "request_id": "uuid"
  }
}
```

| HTTP 状态 | 常见错误代码 | 处理 |
| --- | --- | --- |
| 401 | `UNAUTHENTICATED` | 检查扩展 Bearer 凭据 |
| 403 | `FORBIDDEN` | 检查凭据读取 Scope 和服务端操作白名单 |
| 404 | `SOURCE_NOT_FOUND`、`JOB_NOT_FOUND`、`REVIEW_NOT_FOUND`、`NOT_FOUND` | 移除失效资源或重新开始查询 |
| 405 | `METHOD_NOT_ALLOWED` | 检查方法和路由 |
| 409 | `SOURCE_CHANGED` | 来源复查时原文改变；按响应 `retryable` 处理，重新读取来源后重试 |
| 422 | `INVALID_ARGUMENT`、`INVALID_PAGE_TOKEN` | 修正参数，游标失效时重读第一页 |
| 503 | `UPSTREAM_UNAVAILABLE`、`UPSTREAM_AUTH`、`IDENTITY_MISMATCH` | 检查来源连接与绑定 |
| 503 | `UNSUPPORTED_UPSTREAM`、`UPSTREAM_PROTOCOL`、`UPSTREAM_REQUEST_REJECTED` | 检查 Memos 版本与适配层 |
| 500 | `INTERNAL` | 通过请求标识和服务日志定位 |

业务响应携带 `X-Request-ID` 和 `Cache-Control: no-store`。日志记录任务标识与错误代码，省略笔记正文、请求凭据和模型密钥。
