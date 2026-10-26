# Moerise API v1

API 前缀为 `/api/ext/v1`。路由由 [api.go](../internal/api.go) 定义，字段模型见 [models.go](../internal/models.go)，完整字段契约见 [openapi.json](../internal/openapi.json)。服务通过 `/openapi.json` 提供同一契约，通过 `/docs` 提供交互文档。本文说明访问范围、分页、任务和错误语义。

本版本提供笔记读取、创建、同步管理，以及每日回顾 REST 和扩展 MCP。检索与 AI 按[路线图](ROADMAP.md)接入。

## 资源与身份

一个服务部署绑定一个 Memos 实例和一个账号。资源主键使用 `instance_id + user + name`，其中 `name` 沿用 Memos 的 `memos/{id}`，账号使用 `users/{id}`。账号资源名来自 `/api/v1/auth/me`，可以包含用户名，不能假设为整数。

服务索引和输出范围为绑定账号本人创建的正常及归档笔记。其他账号的公开笔记和管理员可直接管理的其他作者笔记，均在此范围之外。身份绑定保存在产品数据库中。更换数据源地址或账号时使用新的 `instance_id` 和数据卷。

扩展入口使用独立的 Bearer 凭据。Memos PAT 仅由服务端适配层读取。

| Scope | 允许操作 |
| --- | --- |
| `notes:read` | 读取身份、笔记列表和单条笔记 |
| `notes:write` | 创建私有笔记与重放创建请求 |
| `sync:read` | 读取同步状态和任务结果 |
| `sync:write` | 请求增量同步或全量修复 |
| `reviews:read` | 读取已有每日批次和指定回顾批次 |
| `reviews:write` | 获取或创建每日批次、换一组、记录打开与略过 |

只读凭据不能创建笔记或触发同步，写入请求返回 403 `FORBIDDEN`。笔记创建支持基础附件；笔记修改和消息平台操作按路线图提供。

## 路由

| 方法 | 路径 | Scope | 结果 |
| --- | --- | --- | --- |
| GET | `/health/live` | 无 | 进程存活与服务版本 |
| GET | `/health/ready` | 无 | `ready` 或 `degraded` |
| GET | `/api/ext/v1/me` | `notes:read` | 实例、账号、凭据名称、Scope、时区 |
| GET | `/api/ext/v1/notes` | `notes:read` | 已索引笔记的分页结果，逐条复查来源 |
| POST | `/api/ext/v1/notes` | `notes:write` | 首次成功 201；幂等重放成功 200，直接返回 `Note` |
| GET | `/api/ext/v1/notes/{memo_id}` | `notes:read` | 直接复查并读取一条来源笔记 |
| GET | `/api/ext/v1/sync` | `sync:read` | 水位、完整同步时间、来源状态、数量与任务统计 |
| POST | `/api/ext/v1/sync` | `sync:write` | 202，返回持久任务 |
| GET | `/api/ext/v1/jobs/{job_id}` | `sync:read` | 持久任务与执行结果 |
| GET | `/api/ext/v1/reviews/daily` | `reviews:read` | 读取已有当日批次；尚未创建时返回 404 |
| POST | `/api/ext/v1/reviews/daily` | `reviews:write` | 获取或创建固定当日批次，200 |
| POST | `/api/ext/v1/reviews/daily/replace` | `reviews:write` | 换一组，必填 `Idempotency-Key`，200 |
| GET | `/api/ext/v1/reviews/{batch_id}` | `reviews:read` | 读取指定批次，200 |
| POST | `/api/ext/v1/reviews/{batch_id}/items/{memo_id}/open` | `reviews:write` | 记录首次打开时间，200 |
| POST | `/api/ext/v1/reviews/{batch_id}/items/{memo_id}/skip` | `reviews:write` | 记录首次略过时间，200 |
| POST | `/hooks/feishu` | 飞书平台凭据与身份校验 | URL 验证或事件持久入队应答，200 |

来源连接成功、全量同步完成且工作循环存活时，就绪检查返回 200；其他情况返回 503。存活检查适合区分进程状态与来源故障。

## 笔记与分页

列表参数：

| 参数 | 默认值 | 约束 |
| --- | --- | --- |
| `state` | `NORMAL` | `NORMAL`、`ARCHIVED` |
| `page_size` | 20 | 1 至 100 |
| `page_token` | 空字符串 | 服务返回的不透明游标，最多 4096 字符 |

笔记按 `update_time DESC, name DESC` 排序，使用键值分页。游标通过 HMAC 签名，并绑定实例、账号、归档状态和索引代次。重建索引后旧游标失效，调用方从第一页重新读取。

列表从本地投影确定候选资源，再请求 Memos 获取最新正文并复查作者。来源已消失时删除对应投影，来源已归档时移出正常列表。单页可能少于 `page_size`，甚至为空；调用方应以 `next_page_token` 是否为空判断分页结束。

投影会随同步和来源复查变化，分页不提供跨请求的固定快照。需要稳定集合的业务应在服务内部建立自己的批次。

```http
GET /api/ext/v1/notes?state=NORMAL&page_size=20
Authorization: Bearer <reader-token>
```

响应包含 `notes` 和 `next_page_token`。单条资源路径中的 `memo_id` 是 `memos/{id}` 的标识部分，`job_id` 是任务 UUID。

附件数组沿用 Memos 返回的附件元数据。`source_hash` 包含正文、状态、标签、附件和关系等来源信息；正文摘要单独保存在索引中，供后续向量任务判断正文变化。

来源身份或版本无法验证、Memos 连接失败时，读取接口返回 503。接口不使用旧缓存正文替代来源复查。

## 创建笔记

```http
POST /api/ext/v1/notes
Authorization: Bearer <operator-token>
Idempotency-Key: capture-20261025-001
Content-Type: application/json

{"content":"记录一个想法。","tags":["工作/灵感","记录"]}
```

请求头 `Idempotency-Key` 必填，长度为 1 至 128 个字符，只允许 ASCII 字母、数字、`.`、`_`、`:` 和 `-`。每次独立创建使用新的键；同一创建操作的重试复用原键与原请求体。

| 字段 | 必填 | 约束 |
| --- | --- | --- |
| `content` | 是 | 字符串，不能是空字符串或仅含空白 |
| `tags` | 否 | 字符串数组，省略时为 `[]`，输入最多 20 项 |

每个标签为 1 至 100 个 Unicode 字符。标签由一个或多个非空段组成，段之间用 `/` 分隔，每段只允许 Unicode 字母、Unicode 十进制数字、下划线和连字符。上标数字等其他数字形式不接受。标签不能包含 `#` 或空白，也不能以 `/` 开头或结尾，不能含连续 `/`。

服务将标签去重并按字符串升序排序。规范化内容由原样保留的 `content` 和去重排序后的 `tags` 决定；省略 `tags` 与 `tags:[]` 等价，标签顺序或重复项的差异不构成内容冲突。非空标签生成 `#tag1 #tag2\n\n` 前缀并写入正文，使 Memos 按正文解析标签；空标签不生成前缀。原始正文与生成前缀合计的 UTF-8 编码长度不能超过 65536 字节。正文自身包含的标签也由 Memos 解析，层级标签还会包含父级，例如 `parent/child` 同时返回 `parent`。响应的 `tags` 以来源复查结果为准。

还可提供 `attachments` 数组，其他字段返回 422 `INVALID_ARGUMENT`。笔记固定创建为 `PRIVATE`，作者为绑定账号；`creator`、`visibility` 和 `state` 不能指定。

### 基础附件

`attachments` 为有序数组，最多 10 项，省略与 `[]` 等价。文件解码后的总大小至多 8 MiB，请求 JSON 至多 12 MiB，每项包含：

| 字段 | 含义 |
| --- | --- |
| `filename` | 非空文件名，UTF-8 至多 255 字节；不含路径分隔符、控制字符或两端空白，不能为 `.` 或 `..` |
| `type` | 小写 MIME 类型，不带参数，例如 `text/plain`、`image/png` |
| `content` | 非空文件字节的标准 Base64 编码 |

```json
{"content":"附上资料。","tags":["资料"],"attachments":[{"filename":"hello.txt","type":"text/plain","content":"aGVsbG8="}]}
```

服务先创建私有笔记，再通过 Memos 官方附件 API 上传并关联文件。每个位置的附件分配持久资源标识；相同幂等键的文件名、类型、字节或顺序变化返回 409 `IDEMPOTENCY_CONFLICT`。产品状态只保存摘要和资源标识，文件字节保存在 Memos。Memos 处理图片时可能剥离元数据，文件大小以来源结果为准。

部分上传失败或结果不明返回 503 `WRITE_OUTCOME_UNKNOWN`，笔记与已上传文件仍可在 Memos 中打开。使用原键及完整请求体重试，服务先复查原笔记与附件，仅补齐缺失文件；全部关联成功后才确认整次创建成功。重启、索引重建及恢复产品状态后继续使用原资源标识。已经复查确认的部分笔记被删除后返回 404，不重建。成功请求的重放读取来源的新状态，不恢复使用者随后删除的附件。

### 幂等与来源复查

首次请求成功返回 201，响应体直接使用已有 `Note` 模型。相同键与相同规范化内容重放成功返回 200，响应为从 Memos 复查的最新 `Note`，包含来源中的后续修改。同一键对应不同规范化内容返回 409 `IDEMPOTENCY_CONFLICT`。分配的来源资源发生冲突，或确认创建时 Memos 未完整保留请求标签，返回 409 `WRITE_CONFLICT`，调用方按原资源检查来源内容。

调用 Memos 前，服务将幂等键、规范化内容摘要、分配的 `memoId` 和处理状态持久写入 `state.sqlite3`，不将笔记原文存入产品状态。发生超时、5xx 或响应不可信等无法确定写入结果的情况时，返回 503 `WRITE_OUTCOME_UNKNOWN`，且 `error.retryable=true`。调用方只用相同键与相同请求体重试；服务先按持久分配的 `memoId` 复查，必要时才使用同一 ID 创建，重试不会另行生成笔记 ID。

已确认成功的来源笔记被删除后，同键重放返回 404 `SOURCE_NOT_FOUND`，不重建笔记。POST 返回笔记前及每次重放均验证来源身份，并从 Memos 复查作者与正文。身份或来源故障时返回错误，不输出旧正文；幂等记录不作为正文缓存。来源连接与身份错误沿用下表中的 503 错误。

幂等记录随产品状态备份，服务重启后继续使用，索引重建保留记录。恢复快照只能恢复其中已有的幂等记录；快照生成后创建的记录需结合 Memos 原文核实，不能将旧请求直接视为尚未写入。

## 飞书事件

应用机器人回调入口为 `/hooks/feishu`，仅在应用凭据完整时启用；使用平台事件 Token 和可选 Encrypt Key 验证，不使用 Moerise Bearer 凭据。绑定使用者的单聊消息持久入队后返回 `{"code":0}`；URL 验证返回 `{"challenge":"..."}`。重复消息沿用同一记录操作，其他身份和群聊不进入业务。事件配置、命令及恢复语义见[飞书入口](FEISHU.md)。

## 每日回顾与 MCP

回顾 POST 请求体为 `{}`。日期由服务端按配置时区计算，不接受调用方覆盖日期或规则。获取或创建回顾会保存批次和选中历史，因此需要写 Scope；只读 Scope 只能读取已经生成的批次。换组键采用笔记创建相同的字符和长度限制，范围为账号、日期、时区和规则版本。

响应直接返回 `ReviewBatch`，含 `id`、`date`、`timezone`、`rule_version`、`sequence`、`created_at` 和有序 `items`。条目含 `name`、`selected_source_hash`、`selected_at`、独立的 `opened_at` 与 `skipped_at`、`unavailable`，以及可用时的最新 `note`。来源删除或作者变化时保留条目位置，`unavailable=true` 并省略 `note`。打开或略过不在批次中的资源返回 404 `REVIEW_ITEM_NOT_FOUND`。选择、参数和恢复规则见[每日回顾](DAILY-REVIEW.md)。

扩展 MCP 位于 `/mcp`，使用官方 Go MCP SDK 的无会话 Streamable HTTP 传输，返回 JSON。客户端对该 URL 发送 MCP 请求，使用与 REST 相同的 Moerise Bearer 凭据；请求体上限为 64 KiB。协议初始化、请求头和错误按 MCP 协议处理，业务错误使用工具结果的 `isError` 及包含 `code`、`message`、`retryable` 的 JSON 文本。MCP GET、DELETE 不提供事件流或会话管理。含跨站来源的请求受到同源检查。

| 工具 | Scope | 状态变化 |
| --- | --- | --- |
| `read_daily_review` | `reviews:read` | 读取已有当日批次 |
| `get_review_batch` | `reviews:read` | 读取指定 `batch_id` |
| `get_daily_review` | `reviews:write` | 首次获取时保存当日批次和选中历史 |
| `replace_daily_review` | `reviews:write` | 使用 `idempotency_key` 创建新批次 |
| `open_review_memo` | `reviews:write` | 保存 `batch_id`、`name` 对应条目的首次打开时间 |
| `skip_review_memo` | `reviews:write` | 保存 `batch_id`、`name` 对应条目的首次略过时间 |

会改变状态的工具均设置 `readOnlyHint=false`。工具提示只帮助客户端展示，服务端依据凭据 Scope 决定可见及可调用的工具。REST 与 MCP 以同一绑定账号共享批次、换组键与条目状态。

## 同步任务

```http
POST /api/ext/v1/sync
Authorization: Bearer <operator-token>
Content-Type: application/json

{"mode":"full"}
```

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

接口时间使用带时区的 ISO 8601 字符串；`available_at` 和 `lease_until` 使用 Unix 秒。配置时区用于产品日期与后续调度。

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
| 403 | `FORBIDDEN` | 检查凭据 Scope |
| 404 | `SOURCE_NOT_FOUND`、`JOB_NOT_FOUND`、`REVIEW_NOT_FOUND`、`REVIEW_ITEM_NOT_FOUND`、`NOT_FOUND` | 移除失效资源或重新开始查询 |
| 405 | `METHOD_NOT_ALLOWED` | 检查方法和路由 |
| 409 | `IDEMPOTENCY_CONFLICT` | 同一幂等键已对应其他规范化内容；独立创建使用新键 |
| 409 | `WRITE_CONFLICT` | 检查来源资源冲突，不将冲突资源作为创建结果 |
| 422 | `INVALID_ARGUMENT`、`INVALID_PAGE_TOKEN` | 修正参数，游标失效时重读第一页 |
| 503 | `UPSTREAM_UNAVAILABLE`、`UPSTREAM_AUTH`、`IDENTITY_MISMATCH` | 检查来源连接与绑定 |
| 503 | `UNSUPPORTED_UPSTREAM`、`UPSTREAM_PROTOCOL`、`UPSTREAM_REQUEST_REJECTED` | 检查 Memos 版本与适配层 |
| 503 | `WRITE_OUTCOME_UNKNOWN` | `retryable=true`，仅用相同幂等键与请求体重试 |
| 500 | `INTERNAL` | 通过请求标识和服务日志定位 |

业务响应携带 `X-Request-ID` 和 `Cache-Control: no-store`。日志记录任务标识与错误代码，省略笔记正文、请求凭据和模型密钥。
