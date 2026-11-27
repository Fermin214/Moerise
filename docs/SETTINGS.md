# Moerise 系统设置与动态凭据管理

部署环境管理基础设施配置。Web 和 Android 可修改本机偏好及服务端产品设置，服务端加密保存模型和消息渠道凭据。

Web 从侧栏底部的账号入口打开设置。在「通用」中可退出登录，即使设置模块无法下载或接口请求失败，退出入口也可用。退出请求失败时，私人内容保持隐藏，可在登录页重试退出。待退出期间，标签页只暂存原会话的 CSRF 令牌，刷新后直接向 Core 重试注销。若另一标签页已登录新会话，Core 拒绝原令牌，旧标签页结束重试并显示登录表单，新会话保持有效。

---

## 一、配置分层与优先级

1. **部署环境配置（基础设施）**：
   - 包括 Memos 根地址、绑定作者账号、Memos PAT、对外公开域名（`MOERISE_PUBLIC_ORIGIN`）、监听端口、数据目录以及状态加密主密钥（`MOERISE_STATE_ENCRYPTION_KEY`）。
   - 通过 `.env` 环境变量或 `config.json` 传入，供 Core 启动时加载。
2. **产品持久设置（日常偏好与凭据）**：
   - 包括时区、每日回顾频率与时段、观星开关、Embedding 配置与 API Key、QQ、飞书和 Telegram 渠道凭据。
   - 首次启动时从部署配置初始化并存入 `state.sqlite3`；后续以已保存设置为准。模型、渠道与观星设置保存后热生效；时区、回顾规则和推送时段从下一个当地日生效，当天已固定计划保持原规则。重启服务或重建索引均不丢失设置。
3. **本机偏好**：
   - 语言与统计开关在本机即时生效，Web 使用 LocalStorage，Android 使用 SharedPreferences。

| 设置页签 | 内容 |
| --- | --- |
| 通用 | 语言、可搜索的 IANA 时区选择、采用设备时区、上次同步时间、同步和退出登录 |
| 检索与观星 | 向量模型配置摘要、索引进度、观星开关及星图更新 |
| 回顾与推送 | 每日回顾规则、推送开关与时段、定时目的地、独立的消息渠道编辑入口及最近发送记录 |
| 隐私 | 统计开关、诊断摘要的预览与复制、GitHub 问题反馈 |

服务端开关单项保存，离开设置页后已排队的单项提交仍会在登录会话内完成。时区、规则、时段和各项凭据在各自的编辑窗口确认提交。关闭有修改的窗口时会确认是否放弃。Web 的「同步」从 Core 刷新当前或最近使用的时间线，沿用筛选条件并更新浏览器会话内的快照。Core 按服务端周期从 Memos 同步原文。

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
- Web 和 Android 串行提交单项及凭据编辑，使用上次保存返回的版本。遇到 `409 SETTINGS_CONFLICT` 时保留编辑，重载设置后可以继续核对并提交。
- Web 重载时更新未编辑的字段。若重载改变了表单中 Embedding API 地址的协议、主机或端口，提交替换密钥前须确认显示的发送地址。
- 清空正在使用的 Embedding 密钥时，同次提交停用 Embedding。清空渠道的必需密钥便会停用该渠道。若该渠道是定时推送目的地，同次提交还关闭推送。
- 使用回调模式的飞书应用机器人仍有 App Secret 时，清空 Verification Token 前须明确选择同时清空 App Secret。两项清空和关联的推送停用在同一个请求内提交。
- 修改语言和统计开关只写入本机。时区、回顾规则和推送时段沿用已固定的当天计划，新规则次日生效。观星和推送开关立即生效。停用定时推送所用的渠道时，客户端在同一请求关闭推送。
- **密钥操作指令**：
  - `{"action":"keep"}`：保持原密钥；
  - `{"action":"replace","value":"新密钥"}`：替换密钥；
  - `{"action":"clear"}`：清空密钥。

### 候选凭据检查

启用或编辑 Embedding 时，客户端将同一份候选 `version`、`settings.embedding` 和 `secrets.embedding_api_key` 依次提交给 `POST /api/app/v1/models/embedding/check` 和 `PATCH /api/app/v1/settings`。变更 API 地址的协议、主机或端口时，探测和保存均要求替换或清空已存密钥，返回 `422 EMBEDDING_KEY_ORIGIN` 提示沿用旧密钥的请求。仅提交 `{ "version": 1 }` 时检查该版本的已保存配置。

`POST /api/app/v1/channels/check` 接收同样的版本和增量对象，以及显式 `target`。`settings.channel` 和 `secrets` 只接收目标渠道字段。`qq` 换取 access token，`telegram` 调用 `getMe`，`feishu-app` 换取 tenant token，`feishu-webhook` 在本地检查 URL 和字段格式。

两种检查均在复制设置后释放运行读锁，再执行外部请求。成功响应包含 `version`、`status: "ok"` 和 `duration_ms`。探测不发送消息、不重启机器人，也不增加设置版本。检查期间的其他设置写入可以独立完成，后续保存仍需通过版本检查。

### 3. 热生效与配置切换
- 收到 `PATCH /api/app/v1/settings` 后，Core 在校验与加密落盘成功后立即更新内存中的最新配置快照并应用到后续请求。
- 切换 Embedding 向量空间会触发新空间代次的索引构建。
- 「设置 → 检索与观星」中的 `stargazing.enabled` 在新实例默认关闭，保存后热生效。关闭后隐藏观星入口、停止布局调度并丢弃正在计算的旧任务结果。向量索引、星图快照、语义检索和漫步继续保留。重新开启时复用已有星图。
- 观星与 Embedding 均开启时，显示每日 `03:00` 更新说明与手动「重新生成星图」入口。
- `MOERISE_STARGAZING_ENABLED` 或 JSON 的 `stargazing.enabled` 在尚未保存产品设置时生效。升级实例既无已保存设置也无显式部署值时，沿用旧版开启默认值。布局容器的启停由部署端管理，见[运行指南](OPERATIONS.md)。
