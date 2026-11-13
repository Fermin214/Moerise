# Moerise

**基于 Memos 的单用户自托管个人笔记增强系统。**

[English](README.en.md) · [快速上手](docs/GETTING-STARTED.md) · [运行与恢复](docs/OPERATIONS.md) · [API 文档](docs/API-V1.md) · [参与贡献](CONTRIBUTING.md)

---

Moerise 以 [Memos](https://usememos.com/) 为笔记数据底座，围绕记录、阅读、回顾与思考构建完整的增强体验。它为 Memos 补充了现代化的双语 Web 界面、全文与向量混合检索、打破笔记孤岛的“随机漫步”、全局思维可视化的“认知地图”、带上下文的 AI 深度对话，以及无压力的多渠道“每日回顾”（支持 Telegram 与飞书）。

笔记原文、标签与附件始终保存在 Memos 原生数据库中，Moerise 通过官方 API 读写，按用户输入保存正文与附件。

---

## 核心特性

- 📝 **原生笔记与现代化 Web**
  - 提供清爽的浅色两栏式 Web 界面，原生支持**简体中文与英语**自适应切换。
  - 完整支持 Markdown 写作、标签过滤、图片与附件管理，支持卡片内即时编辑与详情独立路由。
- 🔍 **双引擎智能检索**
  - 时间线支持实时关键词筛选；机器 REST/MCP 与混合搜索使用 SQLite FTS5 中文预分词索引。
  - 支持接入 OpenAI 兼容接口或阿里云百炼等向量模型（Embedding），提供语义搜索与基于 RRF 算法的混合检索。
- 🧭 **随机漫步 (Random Walk)**
  - 打破传统线性和标签分类的壁垒，从任意笔记或随机起点出发，基于语义相似度与受控随机抽样在笔记间漫游。
  - 单次漫步自动去重，激发跨主题的思想碰撞。
- 🗺️ **认知地图 (Cognitive Map)**
  - 将长期积累的笔记经高维向量降维投影至二维语义地图，直观呈现个人思考的重心、知识集群与主题脉络。
  - 后台预建轻量快照，普通变化按 24 小时间隔合并更新，支持平移缩放与点击直达实时笔记。
- 💬 **上下文 AI 洞察 (AI Insights)**
  - 支持在 Web 界面中自由多选笔记作为上下文材料，与大语言模型展开流式对话、反思与深度追问。
  - 支持 OpenAI Chat Completions、OpenAI Responses 与 Anthropic Messages 协议；支持自定义 Prompt，将完整助手回复原样保存回 Memos。
- 📬 **无压力每日回顾 (Daily Review)**
  - 每日从符合条件的历史笔记中等概率随机抽取条目（支持成熟等待期与近期去重过滤）。
  - 采用全天固定计划与分时段分配策略；不设未读红点、不搞打卡激励，拒绝任务焦虑。
  - 支持定时推送到 **Telegram 机器人**或**飞书应用机器人 / Webhook**。
  - 新安装默认不启用消息渠道，可在设置中按需选择。
- 🔌 **开放生态与外部 Agent 接入**
  - 提供遵循 Model Context Protocol (MCP) 标准的只读 MCP Server 以及机器 REST API。
  - 允许 Claude Desktop、Cursor 等外部 AI 工具安全检索与阅读你的笔记，服务端严格执行只读白名单，杜绝越权写入风险。
- 🛡️ **严格的数据主权与双库架构**
  - 单实例严格绑定 Memos 单一作者，鉴权边界清晰。
  - 采用双 SQLite 架构：`state.sqlite3` 保存核心产品状态（设置、AI 会话、回顾历史）；`index.sqlite3` 保存随时可从 Memos 完全重建的检索与地图索引。

基础记录、阅读、关键词筛选与每日回顾无需模型。语义搜索、漫步与地图需要 Embedding，AI 对话需要 LLM。启用模型功能后，相关笔记内容会发送给你配置的模型服务。

---

## 系统架构

```mermaid
flowchart LR
    Browser["Web 浏览器"] -->|"同源 Cookie / API"| Web["Moerise Web 服务"]
    Web -->|"固定 API 反向代理"| Core["Moerise Core 服务端"]
    Agent["外部 Agent / MCP 客户端"] -->|"只读 Bearer Token"| Core
    Feishu["飞书平台"] <-->|"事件回调 / 消息推送"| Core
    Core -->|"消息推送"| Telegram["Telegram 平台"]

    Core -->|"官方 API / 同步与读写"| Memos["官方 Memos (0.31.x)"]
    Core --> State["state.sqlite3 (业务状态)"]
    Core --> Index["index.sqlite3 (可重建索引)"]
    Core -.-> Embedding["向量服务 (可选)"]
    Core -.-> LLM["大语言模型 (可选)"]
```

---

## 快速开始

Moerise 推荐使用 Docker Compose 进行一体化部署，自带官方 Memos 0.31.x；同时也支持连接你已有的 Memos 实例。Core 与 Web 使用 GHCR 的版本镜像，支持 AMD64 和 ARM64。

### 1. 部署一体化服务

```bash
# 克隆仓库
git clone https://github.com/Fermin214/Moerise.git
cd Moerise

# 配置环境变量
cp .env.example .env

# 先启动 Memos 创建初始管理员账户
docker compose up -d memos
```

1. 打开浏览器访问 `http://localhost:5231`，完成 Memos 初始账户注册。
2. 在 Memos「设置」→「访问令牌」中生成一个个人访问令牌 (PAT)。
3. 携带 PAT 查询作者资源名，记录响应中的 `user.name`（格式如 `users/abc123`）：

   ```bash
   curl -H "Authorization: Bearer <你的-PAT>" http://localhost:5231/api/v1/auth/me
   ```

4. 在 `.env` 中设置：

| 变量 | 内容 |
| --- | --- |
| `MOERISE_MEMOS_TOKEN` | Memos PAT |
| `MOERISE_MEMOS_USER` | 上一步返回的完整 `user.name` |
| `MOERISE_MEMOS_INSTANCE_ID` | 唯一且稳定的实例标识，如 `primary-memos` |
| `MOERISE_ACCESS_TOKEN` | 外部 REST/MCP 的独立访问密钥 |
| `MOERISE_CURSOR_SECRET` | 分页签名的独立随机密钥 |
| `MOERISE_STATE_ENCRYPTION_KEY` | 加密持久凭据的 32 字节 Base64 密钥 |

分别生成三个独立密钥，并填入对应变量：

```bash
openssl rand -hex 32     # MOERISE_ACCESS_TOKEN
openssl rand -hex 32     # MOERISE_CURSOR_SECRET
openssl rand -base64 32  # MOERISE_STATE_ENCRYPTION_KEY
```

### 2. 启动完整服务

```bash
docker compose pull moerise web
docker compose up -d
```

启动完成后，访问 `http://localhost:3000`，使用你的 Memos 账户名和密码直接登录 Moerise Web。

> 详细的环境变量配置、连接现有 Memos 实例、HTTPS 反向代理及服务器资源规格建议，请参阅[安装与运行指南](docs/OPERATIONS.md)。

---

## 文档导航

[文档索引](docs/README.md)

| 领域 | 文档 | 说明 |
| --- | --- | --- |
| **入门与部署** | [快速上手](docs/GETTING-STARTED.md) | 从零安装与初始化流程 |
| | [运行与恢复](docs/OPERATIONS.md) | 生产部署、配置参数、资源配置档位、数据备份与升级回滚 |
| **功能指南** | [Web 界面](docs/WEB.md) | 界面交互、双语偏好、同源架构与安全模型 |
| | [每日回顾](docs/DAILY-REVIEW.md) | 回顾抽取算法、全天计划分时分配与无压力交互 |
| | [飞书接入](docs/FEISHU.md) | 飞书应用机器人私聊速记与群 Webhook 定时推送 |
| | [Telegram 接入](docs/TELEGRAM.md) | Telegram 机器人定时回顾推送配置 |
| | [AI 对话与洞察](docs/AI.md) | LLM 接入协议、上下文规模控制、Prompt 配置与连接诊断 |
| | [向量与语义检索](docs/EMBEDDING.md) | Embedding 模型适配（OpenAI / 百炼）、分块与向量构建 |
| | [检索系统](docs/SEARCH.md) | FTS5 中文关键词分词、余弦相似度检索与 RRF 融合排序 |
| | [系统设置](docs/SETTINGS.md) | 版本化动态设置、敏感凭据加密与热生效机制 |
| **架构与开发** | [产品架构](docs/PRODUCT-ARCHITECTURE.md) | 模块设计、数据边界、状态划分与单用户安全边界 |
| | [代码导览](docs/CODEBASE.md) | 源码入口、目录职责与修改指引 |
| | [开发脚本](scripts/README.md) | 联合验收、性能基准与质量评测 |
| | [系统设计](docs/DESIGN.md) | Web 集成设计与前后端通信规范 |
| | [API 规范](docs/API-V1.md) | 客户端 `/api/app/v1` 与开放机器 `/api/ext/v1`、`/mcp` 接口契约 |
| | [开发指南](docs/DEVELOPMENT.md) | 本地研发环境配置、测试执行与代码检查 |
| | [产品路线图](docs/ROADMAP.md) | 已交付能力与未来版本演进规划（含 Android 客户端） |

---

## 开源协议

本项目基于 [MIT License](LICENSE) 开源。复用的 Memos 样式与资源遵循其[上游开源协议](web/MEMOS-LICENSE)。
