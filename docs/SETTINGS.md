# Moerise 系统设置与动态凭据管理

Moerise 采用分层配置与版本化持久设置架构：底层基础设施参数由部署环境管理，而日常偏好与模型凭据可在 Web 界面动态调整并加密持久化。

---

## 一、配置分层与优先级

1. **部署环境配置（基础设施）**：
   - 包括 Memos 根地址、绑定作者账号、Memos PAT、对外公开域名（`MOERISE_PUBLIC_ORIGIN`）、监听端口、数据目录以及状态加密主密钥（`MOERISE_STATE_ENCRYPTION_KEY`）。
   - 通过 `.env` 环境变量或 `config.json` 传入，供 Core 启动时加载。
2. **产品持久设置（日常偏好与凭据）**：
   - 包括时区、每日回顾频率与时段、观星开关、Embedding 配置与 API Key、QQ、飞书和 Telegram 渠道凭据。
   - 首次启动时从部署配置初始化并存入 `state.sqlite3`；后续以已保存设置为准。模型、渠道与观星设置保存后热生效；时区、回顾规则和推送时段从下一个当地日生效，当天已固定计划保持原规则。重启服务或重建索引均不丢失设置。
   - 界面分为四个页签：「通用」（语言、时区、观星与星图更新）、「Embedding」（配置、连接检查与语义索引）、「回顾与推送」（每日回顾、消息渠道、统一保存操作与发送记录）以及仅控制客户端遥测开关的「隐私」。

---

## 二、敏感凭据加密机制

所有模型 API Key、机器人 Token 及 Webhook 密钥均在服务端使用 AES-256-GCM 算法加密存储：
- 加密密钥由部署环境变量 `MOERISE_STATE_ENCRYPTION_KEY` 派生；
- 接口返回设置时不回传敏感密钥原文，仅返回布尔状态 `configured`；
- 备份数据库时必须同步保管好状态加密主密钥，否则无法解密凭据。

---

## 三、设置更新与热生效协议

### 1. 读取设置
- 请求：`GET /api/app/v1/settings`
- 返回内容包含设置版本号 `version`、产品设置、凭据状态和 `channel_capabilities`。其中 `text` 和 `images` 表示所选推送目的地是否已具备所需凭据和收件地址。Telegram 可直接填写 Chat ID，飞书群使用 Webhook URL。`record` 单独表示机器人是否已配置私聊速记。
- `channel_status` 按 `qq`、`telegram`、`feishu-app` 返回连接状态 `state`、原因 `detail`、本人私聊地址 `peer` 和 Telegram 启动链接 `link`。首次使用在「回顾与推送」页的对应渠道区域填写密钥，之后可更换或清除。
- 「回顾」中的 `channel.kind` 只决定定时发送目的地。三个机器人可同时接收私聊，群 Webhook 只用于推送。选择群或频道后，该时段的笔记也会发送到该群或频道。

### 2. 增量更新设置
- 请求：`PATCH /api/app/v1/settings`
- 携带读取到的版本号 `version`，在 `settings` 中提交需要修改的字段，在 `secrets` 中提交密钥操作动作。
- **密钥操作指令**：
  - `{"action":"keep"}`：保持原密钥；
  - `{"action":"replace","value":"新密钥"}`：替换密钥；
  - `{"action":"clear"}`：清空密钥。
- **版本并发控制**：提交的版本号若落后于服务端版本，接口返回 409 `SETTINGS_CONFLICT`，防止多端修改相互覆盖。

### 3. 热生效与配置切换
- 收到 `PATCH /api/app/v1/settings` 后，Core 在校验与加密落盘成功后立即更新内存中的最新配置快照并应用到后续请求。
- 切换 Embedding 向量空间会触发新空间代次的索引构建。
- 「设置 → 通用」中的 `stargazing.enabled` 在新实例默认关闭，保存后热生效。关闭后隐藏观星入口、停止布局调度并丢弃正在计算的旧任务结果。向量索引、星图快照、语义检索和漫步继续保留。重新开启时复用已有星图。
- 「设置 → 通用」还提供每日 `03:00` 更新说明与手动「重新生成星图」入口。
- `MOERISE_STARGAZING_ENABLED` 或 JSON 的 `stargazing.enabled` 在尚未保存产品设置时生效。升级实例既无已保存设置也无显式部署值时，沿用旧版开启默认值。布局容器的启停由部署端管理，见[运行指南](OPERATIONS.md)。
