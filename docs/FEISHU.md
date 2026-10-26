# 飞书入口

飞书应用机器人通过 HTTP 事件回调接收绑定账号的单聊消息，调用记录、搜索、相关笔记与回顾业务服务，再回复原消息。同一应用机器人按时区主动推送每日回顾。笔记正文和附件保存在 Memos，检索服务和回顾批次与 REST、MCP 共用。

## 应用与配置

需要启用机器人能力的飞书企业自建应用，以及以下应用身份权限（`tenant_access_token`）：

| Scope | 权限名称 | 用途 |
| --- | --- | --- |
| `im:message.p2p_msg:readonly` | 读取用户发给机器人的单聊消息 | 接收记录、查询和回顾命令，以及图片和文件消息 |
| `im:message:send_as_bot` | 以应用的身份发消息 | 回复命令和主动推送每日回顾 |
| `im:message:readonly` | 获取单聊、群组消息 | 查询平台消息回执，下载消息中的图片和文件资源 |

应用须订阅 HTTP 回调事件 `im.message.receive_v1`，使用者须在应用可用范围内。权限或事件配置变更后需发布应用版本。

在配置文件的 `channel` 中填写：

| 字段 | 填写内容 |
| --- | --- |
| `kind` | `feishu-app` |
| `app_id` | App ID，形如 `cli_...` |
| `app_secret` | 同一应用的 App Secret，至少 16 字符 |
| `verification_token` | 事件订阅的 Verification Token，至少 16 字符 |
| `user_open_id` | 使用者在该应用下的 Open ID，形如 `ou_...`；决定谁能操作笔记及接收推送 |
| `encrypt_key` | 启用事件加密时填写对应 Encrypt Key，否则为 `null` |
| `destination` | 为 `null` 时使用 `user_open_id`；填写时必须与其相同 |
| `webhook_url`、`webhook_secret` | 应用机器人模式均为 `null` |

`app_id`、`app_secret`、`verification_token`、`user_open_id` 须同时填写为 JSON 字符串；全部为 `null` 时关闭应用机器人入口。凭据仅保存在服务端。`user_open_id` 不能用手机号、Union ID 或其他应用的 Open ID 替代。

回调地址为 Moerise 的公网 HTTPS 根地址加 `/hooks/feishu`。服务启动后保存平台回调配置；域名变更后需更新地址并通过验证。

服务先检查 Verification Token、应用标识、发送者和单聊类型；配置 Encrypt Key 时，额外验证 `X-Lark-*` 签名并解密事件。URL 验证回显经过 Token 校验的 `challenge`。其他发送者、机器人消息和群聊事件直接应答，不读写 Memos，也不发送私人内容。

参考：[接收消息](https://open.feishu.cn/document/server-docs/im-v1/message/events/receive)、[发送消息](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message/create)、[回复消息](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message/reply)、[获取消息](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message/get)、[获取消息资源](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message-resource/get)。事件加密与签名按[官方 Go SDK](https://github.com/larksuite/oapi-sdk-go/tree/v3.12.2/event)的协议适配。

## 交互

| 消息 | 操作 |
| --- | --- |
| `记录 正文` | 创建私有笔记，正文内的 `#标签` 由 Memos 解析 |
| 图片或文件消息 | 下载原消息资源并保存为附件笔记，单文件至多 8 MiB |
| `查看 memos/资源标识` | 复查并返回该笔记原文摘要及链接 |
| `搜索 查询词` | 搜索正常笔记，默认返回前 5 条 |
| `搜索 #标签 查询词` | 同时使用标签精确过滤；也支持 `搜索 #标签` 只搜标签 |
| `相关 memos/资源标识` | 从指定笔记查找相关笔记，默认返回前 5 条正常候选 |
| `回顾` | 获取或创建固定当日批次 |
| `换一组` | 创建后续批次，事件重投复用同一换组键 |
| `打开 批次标识 memos/资源标识` | 保存打开时间 |
| `略过 批次标识 memos/资源标识` | 保存略过时间 |
| 其他消息 | 返回操作说明 |

普通文本须有 `记录` 前缀才保存。图片和文件消息本身表示保存附件。保存结果返回 Memos 资源名及原文链接；查询与回顾回复前复查来源身份和正文。来源故障时等待重试，不使用旧正文。

### 搜索回复

搜索调用与 REST 和 MCP 共用的 `SearchService`，无需 LLM。`#标签` 同时作为标签过滤，多个标签使用 AND，格式与记录标签一致。查询长度、状态与结果结构见[搜索](SEARCH.md)。

结果显示命中片段、起止行位置、笔记资源名和 Memos 原文 URL。语义关闭、索引未就绪或模型失败时继续返回关键词与标签结果，并附简短降级提示；请求取消不作为降级成功处理。来源故障时等待重试，不发送缓存文本。

搜索查询加密保存在事件结果引用中。发送回复时重新搜索，并执行来源版本、身份和逐条原文复查，结果随来源变化更新；事件结果引用不保存可直接发送的搜索正文。

### 相关笔记回复

`相关 memos/资源标识` 与 REST、MCP 共用相关笔记服务，使用 `limit=5`、`state=NORMAL`。原笔记可以是归档笔记，不受候选状态过滤；结果排除自身。算法及错误见[相关笔记](SEARCH.md#相关笔记)。

回复展示原笔记资源名与 Memos URL，以及相关结果的片段、起止行号、资源名与 URL；语义降级时显示原因。发送时重新获取相关结果并核实原笔记与候选，不直接发送已保存的正文。原笔记消失或作者越界按 `SOURCE_NOT_FOUND` 处理，处理中改变按可重试的 `SOURCE_CHANGED` 处理；任意来源故障不发送原笔记或部分结果。

## 定时回顾

将 `notifications.enabled` 设为 `true`，`notifications.time` 填入本地时间，例如 `09:00`。时区读取顶层 `timezone`。应用机器人固定发给绑定的 `user_open_id`；`destination` 留空或填写同一 Open ID。

推送包含批次日期、批次标识、笔记创建时间、原文摘要、资源标识及链接。发送前重新读取批次内的来源，原文修改后使用新内容；删除的笔记不输出正文和链接。来源故障或身份变化时等待处理，不发送缓存内容。

在同一单聊中发送 `打开 批次标识 memos/资源标识`、`略过 批次标识 memos/资源标识` 或 `换一组`，操作共享回顾历史。点击 Memos 原文链接不会自动登记打开；打开与略过以显式命令或 REST/MCP 操作为准。推送成功只更新通知状态。

应用发送和回复使用持久 UUID，可信成功响应保存平台 `message_id`。结果不明时可通过通知接口提交该消息标识复查：发送者必须为该应用，回复须对应原事件，主动推送须包含对应日期与批次标识。平台 UUID 的去重时效不能替代产品状态，处理方法见[调度与发送队列](NOTIFICATIONS.md)。

## 自定义机器人 Webhook

只需向群发送回顾时，可在飞书群添加自定义机器人，并使用以下渠道配置：

```json
{
  "channel": {
    "kind": "feishu-webhook",
    "app_id": null,
    "app_secret": null,
    "verification_token": null,
    "encrypt_key": null,
    "user_open_id": null,
    "destination": null,
    "webhook_url": "https://open.feishu.cn/open-apis/bot/v2/hook/replace-with-your-hook",
    "webhook_secret": null
  },
  "notifications": {"enabled": true, "time": "09:00"}
}
```

机器人启用签名校验时，将对应密钥填写到 `webhook_secret`。请求携带秒级时间戳，按飞书协议使用 `timestamp + "\n" + secret` 作为 HMAC-SHA256 密钥，对空消息计算签名并进行 Base64 编码。服务不跟随 HTTP 重定向。协议参考：[自定义机器人](https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot)。

Webhook 只发送回顾，不能接收记录、打开、略过或换组命令；这些操作使用 REST/MCP。成功响应中的 `code: 0` 或 `StatusCode: 0` 保存为 `webhook:通知标识`，表示平台接受请求；它不是可查询的消息标识。超时或不可信响应保留 `unknown`，需结合群内消息人工确认，调用结果确认接口时省略 `receipt`。

## 事件与恢复

回调先将事件持久入队再应答，后台工作循环执行耗时操作。按消息标识去重并保存事件标识及摘要，同一消息的正文变化返回冲突。记录幂等键由消息标识生成；换组、打开和略过调用共享回顾服务。

待处理消息使用 AES-GCM 加密暂存，密钥由应用 Secret 派生，密文绑定来源实例、作者及消息标识。业务完成后删除入站消息密文，只保留操作结果引用、状态、回复 UUID 和平台回执；搜索结果引用包含加密查询，供发送时重新搜索。备份配置时保留应用 Secret；Secret 变化后，旧密文需要原 Secret 才能继续处理。更换绑定使用者或应用后，旧身份的事件不进入发送流程。

记录和附件资源标识由独立分页密钥、来源绑定及幂等键派生后持久保存，旧记录沿用已有标识。配置保持一致时，恢复业务执行前的事件快照也能先复查原资源，避免另建笔记或附件。更换分页密钥后，数据库中已有标识继续有效；恢复旧快照前须恢复配套配置，并核实快照之后的来源操作。

回复与主动推送共用持久发送队列，均保存发送尝试、UUID 和平台回执。关闭定时回顾不影响交互回复。请求已发出但未收到可信回执时，保留 `unknown` 状态，暂停该回复的自动重发；笔记写入与回复状态分别保存。服务重启将中断的业务处理恢复为等待状态，已完成业务不会因回复故障重复保存。

事件队列随产品状态一起备份，索引重建不会删除事件。恢复旧快照后，尚未完成的事件继续复查并执行业务，其回复先进入 `unknown`；先核实平台消息，再确认已送达或未送达，避免重复回复。
