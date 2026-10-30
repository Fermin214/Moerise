# 产品设置

基础设施参数使用部署配置：Memos 连接与作者、PAT、监听地址、目录、机器令牌、浏览器 Origin 和状态加密主密钥。日常设置包括时区、回顾、推送时段、模型、洞察 Prompt 和消息渠道。

## 存储与优先级

配置状态加密主密钥后的首次启动，从有效部署配置初始化日常设置，保存到 `state.sqlite3`；后续启动以持久值为准。修改部署配置不覆盖已经保存的日常设置。索引重建只处理 `index.sqlite3`，重启和正常升级保留设置。

模型与渠道凭据使用部署中的 `web.state_encryption_key` 加密，并按用途派生独立密钥。该主密钥随部署配置单独保管，恢复产品状态需要相同密钥。

## 客户端协议

`GET /api/app/v1/settings` 返回：

- `version`：设置版本，首次初始化为 1。
- `settings`：`timezone`、`review`、`notifications`、`embedding`、`llm`、`channel` 和 `insights`。模型配置不含 `api_key`；渠道配置不含密钥或 Webhook URL。
- `secrets`：各凭据的 `configured` 布尔状态。
- `channel_capabilities`：`text`、`images` 和 `record` 布尔状态。
- `default_insight_prompt`：内置洞察 Prompt，供界面恢复默认值后保存。

`PATCH /api/app/v1/settings` 需要浏览器会话、同源 Origin 和 CSRF，提交读取到的 `version`、需要修改的 `settings` 字段及 `secrets` 操作。对象字段合并，数组整体替换；未知字段和空值返回 422。

只提交 `notifications.time` 时，将有效推送时段替换为该单时段。提交非空 `notifications.times` 时以列表为准，`time` 与最早时段保持一致；同时提交两个字段也遵循此规则。空 `times` 回退到 `time`，省略 `time` 时回退到保存前的最早时段。响应中的 `times` 始终列出有效时段。

密钥名称为 `llm_api_key`、`embedding_api_key`、`app_secret`、`verification_token`、`encrypt_key`、`webhook_url`、`webhook_secret`。每个操作是以下形式之一：

```json
{"action":"keep"}
{"action":"replace","value":"new-secret"}
{"action":"clear"}
```

省略密钥操作等同保持。替换必须提供非空真实值，不能提交遮蔽字符；保持和清空不能携带值。响应不包含密钥原文。

版本过期返回 409 `SETTINGS_CONFLICT`；应用繁忙返回 503 `SETTINGS_BUSY`；字段不合法返回 422 `INVALID_ARGUMENT`。失败不发布部分配置。成功响应与 GET 结构一致，版本递增。

## 生效与资源

保存先等待使用旧设置的请求和工作单元排空，校验并构造新资源，再提交设置并发布资源。排空最多等待 5 秒，超时返回忙错误。连接暂时不可用不阻止保存合法配置；实际调用报告来源或供应商错误，保存成功不表示外部连接已通过检查。LLM 变更不重建向量；Embedding 保持模型空间与索引代次一致。

回顾参数、时段和时区变更不替换已经固定的当天批次；沿用变更前时区的日程至其下一个当地午夜，之后使用新值，过渡信息随设置持久保存。推送开关立即生效。已入队任务保留发送状态，不因设置保存重发。更换飞书应用密钥前，将旧队列密文迁移到稳定状态密钥；无法解密时拒绝保存。

修改应用、接收目标或 Webhook 不重复处理已经入队或已处理的日期时段。旧通知仍可查询和确认，结果不明的任务须经人工确认后才能继续；其发送与回执核对使用原渠道。尚需恢复的旧渠道凭据随产品设置加密保存，不在设置响应中返回，重启后继续可用。新入队的通知使用新渠道。

只有 Webhook URL 的渠道支持文字。配置应用图片上传凭据后支持图文；应用机器人具备完整记录凭据后支持记录。能力由服务端按实际配置计算。

## 模型与索引状态

Web 设置页独立管理 LLM 和 Embedding，支持选择 OpenAI 兼容 Embedding 或百炼，配置维度和是否发送维度参数。保存后可分别检查连接；表单中的模型修改须先保存。索引进度每 5 秒刷新，显示等待、构建、就绪或失败，以及完成片段数。离开页面、重新加载或保存设置会取消旧检查；其他页面更改版本后要求重新加载。漫步与地图入口显示 Embedding 或索引状态，洞察入口单独显示 LLM 状态。

`GET /api/app/v1/models` 要求本人会话，返回设置 `version`、`embedding`、`llm`、`index_status` 和 `vector_index`。两类模型各包含 `enabled`、`configured`、`protocol` 和 `model`；`configured` 表示地址、密钥和模型均已填写，不表示远端可用。索引状态为 `disabled`（关闭）、`waiting`（等待完整同步或构建计划）、`building`（构建）、`ready`（配置与投影一致）、`unavailable`（构建失败并等待重试）。`vector_index` 包含活动与构建代次、片段总数、完成数和脱敏错误代码，旧代次可能与新配置不匹配，只有 `ready` 才可用于语义查询。

`POST /api/app/v1/models/embedding/check` 和 `/api/app/v1/models/llm/check` 接收 `{version}`，要求本人会话、Origin 和 CSRF。版本变化返回 `SETTINGS_CONFLICT`。检查复用有效 Provider 的并发控制，Embedding 检查虚构文档与查询，LLM 检查虚构文本的流式回复；不新增笔记、设置或旧生成任务。成功返回 `{version,status:"ok",duration_ms}`，失败返回模型错误代码，不返回供应商错误正文、密钥或生成内容。检查总预算最多 55 秒，各模型仍受自身超时配置约束，超时返回 `MODEL_CHECK_TIMEOUT`。关闭的模型分别返回 `EMBEDDING_DISABLED` 或 `LLM_DISABLED`。
