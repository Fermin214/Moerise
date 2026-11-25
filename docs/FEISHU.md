# 飞书接入指南

Moerise 支持两种飞书接入方式：

- **飞书应用机器人**：在专属私聊中记录文字、图片和文件，支持 `/回顾` 查询和按时段推送图文每日回顾。
- **群自定义机器人 Webhook**：向指定飞书群定时推送每日回顾（可选配应用凭据发送图片）。

## 一、飞书自建应用准备

在[飞书开放平台](https://open.feishu.cn/)创建企业自建应用并完成以下配置：

### 1. 开通应用权限 (Scope)

在「开发配置 → 权限管理」开通以下权限（`tenant_access_token`）：

| 权限标识 (Scope) | 权限名称 | 必需场景 | 用途 |
| --- | --- | --- | --- |
| `im:message.p2p_msg:readonly` | 读取用户发给机器人的单聊消息 | 私聊记录 | 接收私聊中的文字、图片与文件 |
| `im:message:send_as_bot` | 以应用的身份发消息 | 记录与推送 | 回复写入回执并发送每日回顾 |
| `im:message:readonly` | 获取单聊、群组消息 | 记录与推送 | 下载私聊中的图片和文件 |
| `im:resource` | 获取与上传图片或文件资源 | 图文推送 | 上传回顾笔记图片获取 `image_key` |

### 2. 事件订阅

1. 新建应用推荐在「开发配置 → 事件与回调」选择**长连接**，然后在 Moerise 中填入 App ID 和 App Secret 并保存。使用公网回调模式时，将请求地址设为 `https://你的域名/hooks/feishu`。
2. 添加事件监听：`im.message.receive_v1`（接收消息 v2.0）。
3. 如开启了事件加密，将 Encrypt Key 同步填入 Moerise。
4. 创建并发布应用版本，使权限和事件生效。

公网回调模式在首次处理网址验证或有效私聊事件前显示「等待回调验证」；更换凭据或接收模式后需重新验证。

## 二、配置说明

飞书应用私聊队列依赖 `web.state_encryption_key`（即 `MOERISE_STATE_ENCRYPTION_KEY`）加密暂存正文；即使关闭 Web，也需要配置独立的 32 字节 Base64 密钥（`openssl rand -base64 32`）。

可在 Web 或 Android 的「设置 → 回顾与推送」中配置，或通过配置文件首次初始化：

```json
{
  "channel": {
    "kind": "feishu-app",
    "feishu_mode": "websocket",
    "app_id": "cli_xxxxxxxxxxxxxxxx",
    "app_secret": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "verification_token": null,
    "encrypt_key": null,
    "user_open_id": "ou_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "destination": null,
    "webhook_url": null,
    "webhook_secret": null
  },
  "notifications": {
    "enabled": true,
    "times": ["09:00", "18:00"]
  }
}
```

### 字段说明

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `kind` | string | 定时回顾推送目标，可选 `feishu-app` 或 `feishu-webhook` |
| `feishu_mode` | string | `websocket`（长连接）或 `callback`（公网回调） |
| `app_id` | string | 飞书应用 App ID |
| `app_secret` | string | 飞书应用 App Secret |
| `verification_token` | string / null | 公网回调校验 Token |
| `encrypt_key` | string / null | 事件加密 Key（未开启填 `null`） |
| `user_open_id` | string / null | 个人应用可留空，由首条有效私聊自动登记；多人可见的应用应显式填写本人 `ou_...` |

> [!TIP]
> 首次初始化也支持 `MOERISE_FEISHU_APP_ID`、`MOERISE_FEISHU_APP_SECRET`、`MOERISE_FEISHU_USER_OPEN_ID` 等环境变量；在设置页保存后以持久设置为准。

## 三、私聊速记

在飞书与机器人的一对一私聊中发送消息即可写入 Memos：

| 发送内容 | 写入规则 | 私聊回复 |
| --- | --- | --- |
| **文字** | 原样保存为 Markdown 笔记，保留换行、空格与 `#标签`（上限 64 KiB） | 回复“已保存”及笔记链接 |
| **图片** | 下载原图并保存为带图片附件的笔记 | 回复保存结果 |
| **文件** | 下载文件并保存为带文件附件的笔记（单文件上限 8 MiB） | 回复保存结果 |

- 机器人仅响应已指定或已登记的本人私聊。
- 单独发送 `/回顾` 或 `/review` 查询当天回顾摘录；发送 `//回顾` 或 `//review` 会去掉首个斜杠并按普通笔记保存。其余文字均作为笔记正文原样保存。

## 四、定时回顾推送规格

按设置的推送时段（如 `09:00`、`18:00`）投递当天分配的回顾笔记：

1. **推送顺序**：
   - 先发送标题消息 `「今日回顾」`；
   - 按顺序发送每篇笔记的正文和图片附件；
   - 每篇末尾发送尾注 `#标签  创建于YYYY-MM-DD`（无标签时仅发日期）。
2. **长文切分**：
   - 按飞书单条消息体积限制，以 **18 KiB 请求预算**在换行或 UTF-8 字符边界顺序切分发送。
3. **图片附件**：
   - 从 Memos 读取原图上传至飞书后发送图片消息；纯图片笔记正常发送原图与尾注。

## 五、群自定义机器人 Webhook

仅需向飞书群推送每日回顾时，可在群内添加“自定义机器人”并配置：

```json
{
  "channel": {
    "kind": "feishu-webhook",
    "webhook_url": "https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
    "webhook_secret": "可选安全签名Secret"
  }
}
```

- 无需公网回调域名，只需 Core 能访问飞书 HTTPS 接口；支持 HMAC-SHA256 签名校验。
- 如需随 Webhook 推送图片，开启 `channel.webhook_images` 并填写具备 `im:resource` 权限的 `webhook_app_id` 与 `webhook_app_secret`（对应环境变量 `MOERISE_FEISHU_WEBHOOK_IMAGES`、`MOERISE_FEISHU_WEBHOOK_APP_ID`、`MOERISE_FEISHU_WEBHOOK_APP_SECRET`）。
