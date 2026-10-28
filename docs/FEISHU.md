# 飞书入口

飞书应用机器人通过 HTTP 事件回调接收绑定账号在专用私聊中发送的文字、图片和文件，保存到 Memos 并回复记录结果。同一应用机器人按计划主动推送每日回顾。

## 应用与配置

需要启用机器人能力的飞书企业自建应用，以及以下应用身份权限（`tenant_access_token`）：

| Scope | 权限名称 | 用途 |
| --- | --- | --- |
| `im:message.p2p_msg:readonly` | 读取用户发给机器人的单聊消息 | 接收私聊文字、图片和文件记录 |
| `im:message:send_as_bot` | 以应用的身份发消息 | 回复记录结果和主动推送每日回顾 |
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

## 私聊记录

向专用私聊直接发送文字即可创建私有笔记，无需前缀。整条文字按原样保存，包含首尾空白、换行和正文中的 `#标签`；标签由 Memos 解析。例如发送 `记录 今天的想法 #日记` 时，`记录` 两字也是正文的一部分。

所有文字都作为笔记正文处理，包括 `搜索`、`查看`、`洞察`、`问答`、`回顾`、`换一组`、`打开` 等文字，不调用查询、AI 或回顾操作。每日回顾通过计划主动推送。

| 消息 | 保存方式 |
| --- | --- |
| 文字 | 原样保存为笔记正文，至多 65536 UTF-8 字节；空文字或纯空白文字返回失败提示 |
| 图片 | 下载原消息图片，保存为带图片附件的笔记 |
| 文件 | 下载原消息文件，保存为带文件附件的笔记 |
| 其他消息类型 | 回复支持的记录方式 |

图片与文件沿用附件记录链路，单文件至多 8 MiB。保存成功回复“已保存”、Memos 资源名及原文链接；无效输入回复简短错误提示，来源或平台临时故障进入队列重试。重复事件不会重复创建笔记或上传附件，回复失败也不会重新记录。

机器人仅处理绑定 `user_open_id` 的用户私聊消息；其他发送者、群聊和机器人消息不进入记录队列。记录不依赖 Embedding、LLM 或定时回顾开关。

## 定时回顾

配置时区、推送时段与每次笔记数量，例如：

```json
{
  "timezone": "Asia/Hong_Kong",
  "review": {"count": 3},
  "notifications": {"enabled": true, "times": ["09:00", "18:00"]}
}
```

`times` 为互不重复的 `HH:MM` 列表，每天 1 至 24 次，列表长度即每日次数；未设置列表时兼容 `time`，默认 `09:00`。同一天所有时段共享 Core 固定的回顾批次，刷新、重启、修改回顾规则或增加时段不会重新选取。关闭推送仍生成当天结果；空候选只记录时段已处理，不向平台发送。停机恢复只补建当天已到达时段的任务，已有队列继续恢复，不新建往日推送。

应用机器人固定发给绑定的 `user_open_id`；`destination` 留空或填写同一 Open ID。环境配置可使用以下变量，非空值覆盖 JSON；完整规则见[配置说明](OPERATIONS.md#配置)：

| 环境变量 | 用途 |
| --- | --- |
| `MOERISE_TIMEZONE` | 日期与时段使用的时区 |
| `MOERISE_REVIEW_COUNT` | 每次回顾笔记数量 |
| `MOERISE_REVIEW_MIN_AGE_HOURS`、`MOERISE_REVIEW_RECENT_DAYS` | 等待期和重复排除期，默认 72 小时、7 天 |
| `MOERISE_REVIEW_INCLUDE_ARCHIVED` | 是否包含归档笔记，默认 `false` |
| `MOERISE_NOTIFICATIONS_ENABLED` | 是否主动推送 |
| `MOERISE_NOTIFICATIONS_TIMES` | 逗号分隔的时段，例如 `09:00,18:00` |
| `MOERISE_CHANNEL_KIND` | 应用机器人填 `feishu-app`，Webhook 填 `feishu-webhook` |
| `MOERISE_FEISHU_APP_ID`、`MOERISE_FEISHU_APP_SECRET` | 应用凭据 |
| `MOERISE_FEISHU_VERIFICATION_TOKEN`、`MOERISE_FEISHU_ENCRYPT_KEY` | 回调校验和可选加密 |
| `MOERISE_FEISHU_USER_OPEN_ID` | 专用私聊身份与应用推送接收者 |
| `MOERISE_FEISHU_WEBHOOK_URL`、`MOERISE_FEISHU_WEBHOOK_SECRET` | Webhook 地址及可选签名密钥 |

### 纯文字全文

推送逐条发送完整笔记正文，附回顾日期、笔记创建时间、片号及原文链接。长正文按编码后的 **18 KiB 请求预算**切分，预算包含消息头、JSON 转义及应用接口的嵌套编码，不是正文字符数上限；切分保持 UTF-8 字符完整，并尽量沿换行分隔。各片按序发送完整内容。

应用机器人和 Webhook 均只发送文字正文和原文链接，不发送图片、附件或附件链接。没有文字正文的笔记不产生推送消息。私聊中的图片和文件仍可记录到 Memos；查看附件需打开 Memos 原文。

首次拆片使用读取到的原文，此后每片发送前检查来源与已保存版本。笔记在发送期间改变时，发送简短提示和最新原文链接，取消该笔记尚未发送的旧分片，再继续其他笔记；删除或不再可见的笔记片段跳过，不发送正文。来源故障进入错误与重试流程，不作为空池处理。分片保存来源和正文区间，不另存整篇回顾正文。

阅读推送即可完成回顾，无需发送操作命令。专用私聊中的文字始终作为笔记正文记录，点击 Memos 原文链接不会自动登记打开。

应用发送和回复使用持久 UUID，可信成功响应保存平台 `message_id`。每个正文片有独立通知与回执；前片为 `unknown` 时，后片等待。通过通知接口确认该片已送达后才放行下一片；确认未送达则为该片生成新 UUID 后重发。

携带应用消息回执时，服务核对发送应用；记录回复还需对应原事件，回顾文本需匹配该片完整内容。平台 UUID 的去重时效不能替代持久发送状态，处理方式见[调度与发送队列](NOTIFICATIONS.md#查询与处理)。

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
  "notifications": {"enabled": true, "times": ["09:00", "18:00"]}
}
```

机器人启用签名校验时，将对应密钥填写到 `webhook_secret`。请求携带秒级时间戳，按飞书协议使用 `timestamp + "\n" + secret` 作为 HMAC-SHA256 密钥，对空消息计算签名并进行 Base64 编码。服务不跟随 HTTP 重定向。协议参考：[自定义机器人](https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot)。

Webhook 只需配置地址和可选签名密钥，按片发送纯文字全文和原文链接，无需应用上传凭据。原文链接使用 `memos.public_url`，应将其设为接收者可访问的 Memos 地址；需要登录时由接收者使用自己的 Memos 登录状态。

Webhook 与应用机器人共用 18 KiB 正文分片预算，签名字段计入最终请求，发送前还检查请求总大小不超过 20 KiB。Webhook 只发送回顾，不接收入站记录或操作命令。产品状态变更通过具备相应 Scope 的 REST 入口执行；外部 MCP 只读，可获取、搜索笔记和读取已有回顾，不提供状态写入工具。

成功响应中的 `code: 0` 或 `StatusCode: 0` 保存为 `webhook:通知标识`，表示平台接受该片；它不是可查询的消息标识。超时或不可信响应保留 `unknown`，后片等待。需结合群内消息逐片确认，调用结果确认接口时省略 `receipt`。

## 事件与恢复

回调先将事件持久入队再应答，后台工作循环执行耗时操作。按消息标识去重并保存事件标识及摘要，同一消息的正文变化返回冲突。记录幂等键由消息标识生成。

待处理消息使用 AES-GCM 加密暂存，密钥由应用 Secret 派生，密文绑定来源实例、作者及消息标识。业务完成后删除入站消息密文，保留操作结果引用、状态、回复 UUID 和平台回执；搜索结果引用包含加密查询，供发送时重新搜索，AI 另行保存加密请求、完整结果与分片。备份配置时保留应用 Secret；Secret 变化后，旧密文需要原 Secret 才能继续处理。更换绑定使用者或应用后，旧身份的事件不进入发送流程。

记录和附件资源标识由独立分页密钥、来源绑定及幂等键派生后持久保存，旧记录沿用已有标识。配置保持一致时，恢复业务执行前的事件快照也能先复查原资源，避免另建笔记或附件。更换分页密钥后，数据库中已有标识继续有效；恢复旧快照前须恢复配套配置，并核实快照之后的来源操作。

回复与主动推送共用持久发送队列，均保存发送尝试、UUID 和平台回执。关闭定时回顾不影响交互回复。请求已发出但未收到可信回执时，保留 `unknown` 状态，暂停该回复的自动重发；笔记写入与回复状态分别保存。服务重启将中断的记录等业务恢复为等待状态，已完成业务不会因回复故障重复保存；AI 生成中断则明确终止。

事件队列随产品状态一起备份，索引重建不会删除事件。恢复旧快照后，尚未完成的记录等事件继续复查并执行业务，其回复先进入 `unknown`；AI 仅复用完整结果或回复中断提示。先核实平台消息，再确认已送达或未送达，避免重复回复。
