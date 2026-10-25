# Moerise API v1

API 前缀为 `/api/ext/v1`。路由由 [api.go](../internal/api.go) 定义，字段模型见 [models.go](../internal/models.go)，完整字段契约见 [openapi.json](../internal/openapi.json)。服务通过 `/openapi.json` 提供同一契约，通过 `/docs` 提供交互文档。本文说明访问范围、分页、任务和错误语义。

本版本提供笔记读取与同步管理。笔记写入、回顾、检索、AI 和扩展 MCP 按[路线图](ROADMAP.md)接入。

## 资源与身份

一个服务部署绑定一个 Memos 实例和一个账号。资源主键使用 `instance_id + user + name`，其中 `name` 沿用 Memos 的 `memos/{id}`，账号使用 `users/{id}`。账号资源名来自 `/api/v1/auth/me`，可以包含用户名，不能假设为整数。

服务索引和输出范围为绑定账号本人创建的正常及归档笔记。其他账号的公开笔记和管理员可直接管理的其他作者笔记，均在此范围之外。身份绑定保存在产品数据库中。更换数据源地址或账号时使用新的 `instance_id` 和数据卷。

扩展入口使用独立的 Bearer 凭据。Memos PAT 仅由服务端适配层读取。

| Scope | 允许操作 |
| --- | --- |
| `notes:read` | 读取身份、笔记列表和单条笔记 |
| `sync:read` | 读取同步状态和任务结果 |
| `sync:write` | 请求增量同步或全量修复 |

只读凭据不能触发同步。笔记创建、修改、附件上传和消息平台操作在后续接口中提供。

## 路由

| 方法 | 路径 | Scope | 结果 |
| --- | --- | --- | --- |
| GET | `/health/live` | 无 | 进程存活与服务版本 |
| GET | `/health/ready` | 无 | `ready` 或 `degraded` |
| GET | `/api/ext/v1/me` | `notes:read` | 实例、账号、凭据名称、Scope、时区 |
| GET | `/api/ext/v1/notes` | `notes:read` | 已索引笔记的分页结果，逐条复查来源 |
| GET | `/api/ext/v1/notes/{memo_id}` | `notes:read` | 直接复查并读取一条来源笔记 |
| GET | `/api/ext/v1/sync` | `sync:read` | 水位、完整同步时间、来源状态、数量与任务统计 |
| POST | `/api/ext/v1/sync` | `sync:write` | 202，返回持久任务 |
| GET | `/api/ext/v1/jobs/{job_id}` | `sync:read` | 持久任务与执行结果 |

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
| 404 | `SOURCE_NOT_FOUND`、`JOB_NOT_FOUND`、`NOT_FOUND` | 移除失效资源或重新开始查询 |
| 405 | `METHOD_NOT_ALLOWED` | 检查方法和路由 |
| 422 | `INVALID_ARGUMENT`、`INVALID_PAGE_TOKEN` | 修正参数，游标失效时重读第一页 |
| 503 | `UPSTREAM_UNAVAILABLE`、`UPSTREAM_AUTH`、`IDENTITY_MISMATCH` | 检查来源连接与绑定 |
| 503 | `UNSUPPORTED_UPSTREAM`、`UPSTREAM_PROTOCOL`、`UPSTREAM_REQUEST_REJECTED` | 检查 Memos 版本与适配层 |
| 500 | `INTERNAL` | 通过请求标识和服务日志定位 |

业务响应携带 `X-Request-ID` 和 `Cache-Control: no-store`。日志记录任务标识与错误代码，省略笔记正文、请求凭据和模型密钥。
