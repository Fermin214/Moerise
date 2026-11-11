# Telegram 回顾推送

[English](TELEGRAM.md)

Moerise 支持通过 Telegram Bot 定时发送每日回顾。每个实例可选择一个生效的消息渠道：Telegram、飞书应用机器人、飞书 Webhook 或不启用。

启用后，Moerise 发送完整正文、按顺序拆分的长文与图片原文件。标题为 `Daily review`，每篇末尾附标签与创建日期，如 `#ideas  Created on 2026-01-15`。笔记、标签和附件保留原语言。

---

## 一、创建与配置机器人

### 1. 创建 Bot

1. 在 Telegram 打开官方 **@BotFather**。
2. 发送 `/newbot`，按提示设置显示名与用户名。
3. 妥善保存生成的 **Bot Token**。

### 2. 初始化私聊

打开与新 Bot 的私聊并发送 `/start`。用户先联系机器人后，机器人才能向其发送私聊消息。

### 3. 获取 Chat ID

通过 `getUpdates` 查询数字 Chat ID：

```bash
read -rs -p "Telegram Bot Token: " TELEGRAM_BOT_TOKEN
printf '\n'
curl --silent --show-error "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getUpdates"
unset TELEGRAM_BOT_TOKEN
```

读取响应中的 `message.chat.id`，私聊通常为正整数，如 `123456789`。

- **群聊**：将机器人加入群，发送提及机器人的消息，读取 `message.chat.id`，通常为负数。
- **频道**：将机器人设为有发帖权限的管理员，发送频道帖子，读取 `channel_post.chat.id`。

---

## 二、配置 Moerise

### Web 设置

打开「设置」→「消息渠道」：

1. 选择 **Telegram**。
2. 填入 **Telegram Bot Token**。
3. 将推送目标设为数字 **Chat ID**。
4. 启用回顾推送，设置时段与时区并保存。

Bot Token 加密保存在 `state.sqlite3`，设置接口不回传原文。

### 环境变量初始化

可在 `.env` 中设置：

```dotenv
MOERISE_CHANNEL_KIND=telegram
MOERISE_TELEGRAM_BOT_TOKEN=123456789:ABCdefGHIjklMNOpqrSTUvwxYZ
MOERISE_TELEGRAM_CHAT_ID=123456789
MOERISE_NOTIFICATIONS_ENABLED=true
MOERISE_NOTIFICATIONS_TIMES=09:00,18:00
MOERISE_TIMEZONE=Asia/Shanghai
```

部署值仅用于首次初始化日常设置，之后以 Web 保存的值为准。

---

## 三、推送格式与恢复

1. **长文分片**：
   - 单条消息最多 4,096 个 UTF-16 单元。
   - 优先按段落和换行拆分，保持 UTF-8 完整。
   - 正文按纯文本发送，不启用 Markdown 解析与自动链接预览。
2. **空白与不可见字符**：
   - 只含空白或不可见字符的分片改为 `.txt` 附件，避免 Telegram 将其视为空消息。
3. **图片原文件**：
   - 使用 `sendDocument`，每张最多 10 MiB，不对原字节进行压缩处理。
4. **限流与重试**：
   - HTTP 429 时读取 Bot API 响应中的 `parameters.retry_after`，冷却记录跨任务、跨重启生效。
   - 超时或结果无法确认的发送进入 `unknown`，通过[通知管理接口](NOTIFICATIONS.md)查询与处理。

Telegram 会规范化纯文本消息的首尾空白。Moerise 保留提交的正文，并将平台会判为空的分片改用文件发送。Telegram 没有按任意消息 ID 查询历史消息的通用接口，处理 `unknown` 前需在目标聊天中核实是否收到。
