# Moerise

**随手记下，再次遇见，接着想下去。**

[English](README.md) · [安装指南](docs/OPERATIONS.md#容器部署) · [参与贡献](CONTRIBUTING.md)

Moerise 是基于 Memos 的开源自托管个人笔记产品。你可以用几句话记下一个想法，重读几个月前的记录，也可以沿着一条笔记，找到那些早已忘记的联系。

记下来之后，这些想法还可以重新回到你的生活里。

## 和自己的笔记多待一会儿

- **先把此刻记下来。** 用 Markdown 写作，添加标签、图片或文件。想找回某段记录时，可以翻时间线，也可以按日期和标签筛选。
- **遇见以前的自己。** 每日回顾从旧笔记里挑出几条，供你随时重读；也可以通过 Telegram 或飞书定时送来。
- **随意走走。** 随机开始，或者从一条笔记出发，在相关想法之间漫步。同一次漫步中，每一步都会遇见一条还没走过的笔记。
- **看看想法聚在哪里。** 认知地图让语义相近的笔记靠得更近。每日快照呈现大致趋势，点开某个位置就能读到实时原文。
- **带着笔记一起思考。** 亲手选择要交给 AI 的笔记，提问、联想，再继续追问。对话会保存下来，完整回复也可以另存为新笔记。

关键词搜索可以直接使用。配置 Embedding 后，可以使用语义与混合搜索、漫步和地图；配置 LLM 后，可以围绕笔记聊天。基础记录、阅读和每日回顾都不依赖模型。

## 笔记仍在 Memos 里

Memos 保存原文、标签、附件和账户。Moerise 通过官方 API 读写，可以直接连接已有的 Memos 笔记库。每个 Moerise 实例绑定一个 Memos 0.31.x 实例中的一名作者。

Moerise Core 负责搜索索引、回顾日程、聊天和设置。产品状态可以备份，索引可以重建，外部工具可以通过只读 REST 或 MCP 接入。启用模型功能后，所需笔记会发送给你配置的模型服务。

Web 支持**简体中文和英语**。首次访问按浏览器语言显示，登录页和侧栏都可以切换。

消息渠道按需选择：[Telegram](docs/TELEGRAM.md) 提供英文回顾推送，[飞书](docs/FEISHU.md) 提供中文推送及私聊记录。新安装默认不启用消息渠道。

## 在自己的服务器上使用

可以用 Docker Compose 部署完整服务，也可以连接已经运行的 Memos；服务端还支持 Linux 二进制。仓库提供从 1 核 1 GiB 到 4 核 8 GiB 的资源配置。

```bash
git clone https://github.com/Fermin214/Moerise.git
cd Moerise
cp .env.example .env
docker compose up -d memos
```

创建 Memos 账户，在 `.env` 中填写绑定信息和独立密钥后，启动 Moerise。[安装指南](docs/OPERATIONS.md#容器部署)包含完整步骤、已有 Memos 的连接方式及 HTTPS 配置。部署后打开 `http://localhost:3000`，使用同一 Memos 账户登录。

## 一起把它做好

Moerise 使用 Go、SQLite 和没有第三方运行依赖的 JavaScript 前端。欢迎用中文或英文报告问题、讨论设计、完善翻译或提交代码。

贡献入口见 [CONTRIBUTING.md](CONTRIBUTING.md)，详细开发环境见[开发指南](docs/DEVELOPMENT.md)。后续安排，包括 Android App，集中在[路线图](docs/ROADMAP.md)。

| 想了解什么 | 去哪里看 |
| --- | --- |
| 安装、配置、资源、备份和升级 | [运行与恢复](docs/OPERATIONS.md) |
| 界面功能和 Web 部署 | [Web](docs/WEB.md) |
| 客户端接口与只读 REST/MCP | [API v1](docs/API-V1.md) |
| 回顾与消息渠道 | [每日回顾](docs/DAILY-REVIEW.md)、[飞书入口](docs/FEISHU.md)、[Telegram](docs/TELEGRAM.md) |
| 模型与搜索 | [AI](docs/AI.md)、[向量模型](docs/EMBEDDING.md)、[搜索](docs/SEARCH.md) |
| 组件职责与数据边界 | [产品与架构](docs/PRODUCT-ARCHITECTURE.md) |

## 开源协议

[MIT](LICENSE) · Copyright (c) 2026 Fermin214。复用的 Memos 样式遵循[上游协议](web/MEMOS-LICENSE)。
