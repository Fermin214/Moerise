# 飞书入口

飞书应用机器人通过 HTTP 事件回调接收绑定账号在专用私聊中发送的文字、图片和文件，保存到 Memos 并回复记录结果。同一应用机器人按计划主动推送每日回顾。

本页说明已实现的接入和恢复协议。产品规则见 [PRD](PRD.md#1-飞书机器人)，纯附件写入、渠道能力提示及全天计划分时推送分别按 [ROADMAP](ROADMAP.md#阶段三完整-web-与一体化-compose) 的 3.2、3.3、3.6 交付。

## 应用与配置

需要启用机器人能力的飞书企业自建应用，以及以下应用身份权限（`tenant_access_token`）：

| Scope | 权限名称 | 用途 |
| --- | --- | --- |
| `im:message.p2p_msg:readonly` | 读取用户发给机器人的单聊消息 | 接收私聊文字、图片和文件记录 |
| `im:message:send_as_bot` | 以应用的身份发消息 | 回复记录结果和主动推送每日回顾 |
| `im:message:readonly` | 获取单聊、群组消息 | 查询平台消息回执，下载消息中的图片和文件资源 |
| `im:resource` | 获取与上传图片或文件资源 | 上传回顾图片，并读取消息图片资源 |

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

所有文字都作为笔记正文保存，包括 `搜索`、`查看`、`洞察`、`问答`、`回顾`、`换一组`、`打开` 等。私聊不支持查询、AI 或回顾命令，每日回顾按计划主动推送。

| 消息 | 保存方式 |
| --- | --- |
| 文字 | 原样保存为笔记正文，至多 65536 UTF-8 字节；空文字或纯空白文字返回失败提示 |
| 图片 | 下载原消息图片，保存为带图片附件的笔记 |
| 文件 | 下载原消息文件，保存为带文件附件的笔记 |
| 其他消息类型 | 回复支持的记录方式 |

图片与文件保存为 Memos 附件，单文件至多 8 MiB。保存成功回复“已保存”、Memos 资源名及原文链接；无效输入回复简短错误提示，来源或平台临时故障进入队列重试。重复事件只创建一次笔记或附件，回复重试复用已保存的结果。

既有纯附件路径补入文件名占位正文；3.2 与原生客户端一起修正共享写入校验，仅保存真实正文及附件，不补文字，也不批量改写历史笔记。旧事件继续按原幂等标识恢复。

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

`times` 为互不重复的 `HH:MM` 列表，每天 1 至 24 次，列表长度即每日次数；未设置列表时使用 `time`，默认 `09:00`。既有调度让多个时段复用同一日批次，并补建当天全部到期时段。3.6 将其改为全天固定计划、各时段不同笔记及只补最近一次，刷新或修改设置不替换当天计划；已经入队的任务继续恢复，不补建往日计划。关闭推送仍生成当天结果；空候选不向平台发送。

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

### 正文与图片

每次推送先单独发送一条 `「今日回顾」`，整次只发一次。随后按笔记顺序发送完整正文和原图，每篇末尾发送 `#标签  创建于YYYY-MM-DD`；无标签时只发 `创建于YYYY-MM-DD`。日期取来源 `create_time`，按配置 `timezone` 换算。不附加链接、片号或状态提示。长正文按编码后的 **18 KiB 请求预算**切分，计入 JSON 转义及应用接口的嵌套编码；切分保持 UTF-8 字符完整，并尽量沿换行分隔，各片按序发送。

每条笔记先发送正文，再按附件顺序发送图片，最后发送标签和创建日期尾注；纯图片笔记依次发送图片及尾注。图片通过 Memos 文件接口读取，原始字节上传飞书取得 `image_key`，再发送图片消息。普通文件不推送，也不发送附件链接。

单图上限 10 MiB，格式和分辨率遵守飞书[上传图片](https://open.feishu.cn/document/server-docs/im-v1/image/create)接口限制。图片已删除，或平台不支持其尺寸、格式时，跳过该图。没有可发送正文或图片的笔记不发尾注，整次均无内容时也不发标题；来源故障和平台临时故障进入重试。

首次拆片读取原文，此后每篇笔记的分片发送前检查来源版本。笔记在发送期间修改、删除或不再可见时，跳过该笔记尚未发送的分片，继续其他笔记。笔记分片保存来源、正文区间或图片资源名，以及上传后的 `image_key`；重启复用上传结果。

阅读推送即可完成回顾，无需发送操作命令。推送不登记打开或略过记录；专用私聊中的文字始终作为笔记正文保存。

应用发送和回复使用持久 UUID，可信成功响应保存平台 `message_id`。标题、每个正文片、每张图片及每篇尾注各有独立通知与回执；前片为 `unknown` 时，后片等待。通过通知接口确认该片已送达后才放行下一片；确认未送达则为该片生成新 UUID 后重发。重启后已确认送达的标题不重复发送。

携带应用消息回执时，服务核对发送应用、发送时间及已知会话，拒绝已绑定其他通知的回执。记录回复须对应原事件；回顾文字须匹配完整分片，图片须匹配上传的 `image_key`。UUID 在平台去重有效期内生效，发送进度由产品数据库持久保存。处理方式见[调度与发送队列](NOTIFICATIONS.md#查询与处理)。

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

Webhook 发送文字只需地址和可选签名密钥。发送图片还需填写 `app_id`、`app_secret`，对应应用须启用机器人能力及 `im:resource` 权限；其他应用字段保持 `null`。服务用应用凭据上传图片，再通过 Webhook 发送图片消息。未配置上传凭据时只发送文字，图片静默跳过。

URL-only Webhook 是合法文字渠道。文字模式遇到纯图片笔记直接跳过，图文笔记只发文字，不增加提示或补抽笔记；3.3 的设置界面展示实际文字/图文能力。发送期间来源变化时停止余片，结果不明继续按队列确认机制处理。

Webhook 与应用机器人使用相同的标题、正文、原图和尾注顺序，共用 18 KiB 正文分片预算，签名字段计入最终请求，发送前还检查请求总大小不超过 20 KiB。Webhook 只发送回顾，不接收入站记录或操作命令。产品状态变更通过具备相应 Scope 的 REST 入口执行；外部 MCP 只读，可获取、搜索笔记和读取已有回顾，不提供状态写入工具。

成功响应中的 `code: 0` 或 `StatusCode: 0` 保存为 `webhook:通知标识`。Webhook 不支持消息回执查询；超时或响应无法确认时保留 `unknown`，后片等待。需结合群内消息逐片确认，调用结果确认接口时省略 `receipt`。

## 事件与恢复

回调先将事件持久入队再应答，后台工作循环执行耗时操作。按消息标识去重并保存事件标识及摘要，同一消息的正文变化返回冲突。记录幂等键由消息标识生成。

待处理消息使用 AES-GCM 加密暂存，密钥由应用 Secret 派生，密文绑定来源实例、作者及消息标识。业务完成后删除入站消息密文，保留操作结果引用、状态、回复 UUID 和平台回执。队列中的搜索任务保存加密查询，发送时重新搜索；AI 任务保存加密请求、完整结果与分片。备份配置时一并保存应用 Secret，密文解密需要加密时使用的 Secret。更换绑定使用者或应用后，身份不匹配的事件不进入发送流程。

记录和附件资源标识由独立分页密钥、来源绑定及幂等键派生后持久保存，重试使用同一标识。配置保持一致时，恢复业务执行前的事件快照会先复查资源，避免另建笔记或附件。更换分页密钥不改变已持久保存的标识；恢复快照时须使用配套配置，并核实快照之后的来源操作。

回复与主动推送共用持久发送队列，均保存发送尝试、UUID 和平台回执。关闭定时回顾不影响交互回复。请求已发出但未收到可信回执时，保留 `unknown` 状态，暂停该回复的自动重发；笔记写入与回复状态分别保存。服务重启将中断的记录等业务恢复为等待状态，已完成业务不会因回复故障重复保存；AI 生成中断则明确终止。

事件队列随产品状态一起备份，索引重建不会删除事件。恢复快照后，尚未完成的记录事件继续复查并执行业务，其回复先进入 `unknown`；AI 任务使用完整结果或回复中断提示。先核实平台消息，再确认已送达或未送达，避免重复回复。
