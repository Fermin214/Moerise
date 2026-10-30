# 产品设置

基础设施参数使用部署配置：Memos 连接与作者、PAT、监听地址、目录、机器令牌、浏览器 Origin 和状态加密主密钥。日常设置包括时区、回顾、推送时段、模型、洞察 Prompt 和消息渠道。

## 存储与优先级

首次启动从有效部署配置初始化日常设置，保存到 `state.sqlite3`；后续启动以持久值为准。修改部署配置不覆盖已经保存的日常设置。索引重建只处理 `index.sqlite3`，重启和正常升级保留设置。

模型与渠道凭据使用部署中的 `web.state_encryption_key` 加密，并按用途派生独立密钥。该主密钥随部署配置单独保管，恢复产品状态需要相同密钥。

## 客户端协议

`GET /api/app/v1/settings` 返回：

- `version`：设置版本，首次初始化为 1。
- `settings`：`timezone`、`review`、`notifications`、`embedding`、`llm`、`channel` 和 `insights`。模型配置不含 `api_key`；渠道配置不含密钥或 Webhook URL。
- `secrets`：各凭据的 `configured` 布尔状态。
- `channel_capabilities`：`text`、`images` 和 `record` 布尔状态。

`PATCH /api/app/v1/settings` 需要浏览器会话、同源 Origin 和 CSRF，提交读取到的 `version`、需要修改的 `settings` 字段及 `secrets` 操作。对象字段合并，数组整体替换；未知字段和空值返回 422。

密钥名称为 `llm_api_key`、`embedding_api_key`、`app_secret`、`verification_token`、`encrypt_key`、`webhook_url`、`webhook_secret`。每个操作是以下形式之一：

```json
{"action":"keep"}
{"action":"replace","value":"new-secret"}
{"action":"clear"}
```

省略密钥操作等同保持。替换必须提供非空真实值，不能提交遮蔽字符；保持和清空不能携带值。响应不包含密钥原文。

版本过期返回 409 `SETTINGS_CONFLICT`；应用繁忙返回 503 `SETTINGS_BUSY`；字段不合法返回 422 `INVALID_ARGUMENT`。失败不发布部分配置。成功响应与 GET 结构一致，版本递增。

## 生效与资源

保存先校验并构造资源，再排空使用旧设置的请求和工作单元，提交设置并发布资源。连接暂时不可用不阻止保存合法配置；实际调用报告来源或供应商错误，保存成功不表示外部连接已通过检查。LLM 变更不重建向量；Embedding 保持模型空间与索引代次一致。

回顾参数、时段和时区变更不替换已经固定的当天批次；后续日期使用新值。已入队任务保留发送状态，不因设置保存重发。更换飞书应用密钥前，将旧队列密文迁移到稳定状态密钥；无法解密时拒绝保存。

只有 Webhook URL 的渠道支持文字。配置应用图片上传凭据后支持图文；应用机器人具备完整记录凭据后支持记录。能力由服务端按实际配置计算。
