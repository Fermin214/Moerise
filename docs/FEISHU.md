# 飞书接入指南

Moerise 支持接入飞书平台，提供**企业自建应用机器人**与**群自定义机器人 Webhook** 两种模式：
- **飞书应用机器人**：支持在专属私聊中双向快速记录文字、图片和文件至 Memos，并支持按时段主动推送图文每日回顾。
- **群自定义机器人 Webhook**：仅用于定时向飞书群单向推送每日回顾。

---

## 一、飞书自建应用准备

若需要私聊速记与完整的图文每日回顾，请在[飞书开放平台](https://open.feishu.cn/)创建自建应用：

### 1. 权限申请 (Scope)
在「开发配置」→「权限管理」中开通以下应用权限（`tenant_access_token`）：

| 权限标识 (Scope) | 权限名称 | 必需场景 | 用途说明 |
| --- | --- | --- | --- |
| `im:message.p2p_msg:readonly` | 读取用户发给机器人的单聊消息 | 私聊记录 | 接收用户在私聊中发送的文字、图片与文件 |
| `im:message:send_as_bot` | 以应用的身份发消息 | 记录与推送 | 回复记录结果，并主动推送每日回顾卡片与分片 |
| `im:message:readonly` | 获取单聊、群组消息 | 记录与推送 | 查询平台消息回执，下载用户发送的图片/文件 |
| `im:resource` | 获取与上传图片或文件资源 | 图文推送 | 上传回顾笔记中的图片资源至飞书获取 `image_key` |

### 2. 事件订阅配置
1. 在「开发配置」→「事件与回调」中，将**请求网址 URL** 配置为：`https://你的域名/hooks/feishu`。
2. 添加事件监听：`im.message.receive_v1`（接收消息 v2.0）。
3. 如开启了“Encrypt Key（事件加密）”，需将密钥同步填入 Moerise 配置。
4. 完成配置后，务必创建并**发布应用版本**，使权限与事件正式生效。

---

## 二、配置说明

可在 Web 界面的「设置」→「消息渠道」中可视化配置，或在服务端配置文件中填写：

```json
{
  "channel": {
    "kind": "feishu-app",
    "app_id": "cli_xxxxxxxxxxxxxxxx",
    "app_secret": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "verification_token": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
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
| `kind` | string | 选择 `feishu-app`（应用机器人）或 `feishu-webhook`（群自定义机器人） |
| `app_id` | string | 飞书应用凭证 App ID |
| `app_secret` | string | 飞书应用凭证 App Secret |
| `verification_token` | string | 飞书事件回调校验 Token |
| `encrypt_key` | string / null | 飞书事件加密 Key（未开启加密填 `null`） |
| `user_open_id` | string | 绑定的用户 Open ID（格式如 `ou_...`），决定谁有权在私聊中速记以及接收单聊回顾推送 |
| `destination` | string / null | 推送目标 Open ID；默认为 `null` 即发送给 `user_open_id` |

> [!TIP]
> 环境变量配置时支持 `MOERISE_FEISHU_APP_ID`、`MOERISE_FEISHU_APP_SECRET`、`MOERISE_FEISHU_USER_OPEN_ID` 等变量。

---

## 三、私聊双向速记

用户在飞书与机器人的一对一专属私聊中直接发送内容即可快速创建 Memos 笔记：

| 发送内容 | 写入 Memos 规则 | 结果反馈 |
| --- | --- | --- |
| **纯文字** | 原样保存为 Markdown 笔记，保留换行、空格与正文中的 `#标签`（最大 64 KiB） | 机器人在私聊中回复“已保存”及 Memos 笔记链接 |
| **图片** | 下载原图原样保存为带有图片附件的 Memos 笔记 | 机器人回复保存结果 |
| **文件** | 下载文件原样保存为带有文件附件的 Memos 笔记（单文件最大 8 MiB） | 机器人回复保存结果 |

- **安全边界**：机器人只响应配置的 `user_open_id` 用户；来自其他飞书用户的私聊消息、群聊消息或机器人自身的消息均会被自动忽略。
- **纯粹记录原则**：私聊不提供复杂指令系统，所有发送的文字均视为真实笔记正文，绝不误截断或擅自丢弃内容。

---

## 四、定时回顾推送规格

按照设置的推送时段（如 09:00、18:00），飞书机器人将有序推送当前时段分配的精选笔记：

1. **推送顺序**：
   - 每次推送首先单独发送一条标题：`「今日回顾」`；
   - 随后按笔记顺序依次发送完整正文、原图附件；
   - 每篇笔记末尾发送时间与标签尾注：`#标签  创建于YYYY-MM-DD`（无标签时仅发送日期）。
2. **长正文安全切分**：
   - 考虑飞书单条消息体积限制与 JSON 转义膨胀，长正文按 **18 KiB 请求预算**严格在换行符或 UTF-8 字符边界切分后分批发送，保证还原后分毫不差。
3. **原图传输**：
   - 图片通过 Memos 提取并直接上传至飞书，转换为高清原图消息发出；纯图片笔记按原图及尾注正常呈现。

---

## 五、群自定义机器人 Webhook

若仅需向指定飞书群推送每日回顾文本，可在飞书群内添加“自定义机器人”，并配置：

```json
{
  "channel": {
    "kind": "feishu-webhook",
    "webhook_url": "https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
    "webhook_secret": "可选安全签名Secret"
  }
}
```

- Webhook 模式无需配置公网回调域名，仅需 Core 能够对外发起 HTTPS 请求。
- 支持基于 HMAC-SHA256 的时间戳签名防伪校验。
- 若需通过 Webhook 发送图片，需同时提供拥有 `im:resource` 权限的自建应用 `app_id` 与 `app_secret` 用于图片上传。
