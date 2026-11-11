# Moerise 快速上手

[产品介绍](../README.md) · [English](GETTING-STARTED.md)

Moerise 支持 Linux 原生二进制与 Docker Linux 容器。容器部署需要 Docker Engine 和 Docker Compose 2.24 或更高版本。一体化服务包含 Memos 0.31.x、Moerise Core 与 Web，每个 Moerise 实例绑定一名 Memos 作者。

---

## 一、使用新的 Memos 实例

在仓库根目录执行：

```bash
cp .env.example .env
docker compose up -d memos
```

1. 打开 `http://localhost:5231`，注册 Memos 账户。
2. 在 Memos「设置」→「访问令牌」中生成个人访问令牌 (PAT)。
3. 携带 PAT 查询作者资源名：

   ```bash
   curl -H "Authorization: Bearer <你的-PAT>" http://localhost:5231/api/v1/auth/me
   ```

   记录响应中的 `user.name`（如 `users/abc123`），这是资源标识，与登录用户名不同。
4. 编辑 `.env`：

| 变量 | 说明 |
| --- | --- |
| `MOERISE_MEMOS_TOKEN` | Memos PAT |
| `MOERISE_MEMOS_USER` | `/api/v1/auth/me` 返回的完整 `user.name` |
| `MOERISE_MEMOS_INSTANCE_ID` | 唯一且稳定的实例标识，如 `primary-memos` |
| `MOERISE_TIMEZONE` | IANA 时区，如 `Asia/Shanghai`、`Europe/London` |
| `MOERISE_ACCESS_TOKEN` | 外部机器 REST/MCP 的独立访问密钥 |
| `MOERISE_CURSOR_SECRET` | 分页签名的独立随机密钥 |
| `MOERISE_STATE_ENCRYPTION_KEY` | 加密持久凭据的 32 字节 Base64 密钥 |

分别生成三个独立密钥：

```bash
openssl rand -hex 32     # MOERISE_ACCESS_TOKEN
openssl rand -hex 32     # MOERISE_CURSOR_SECRET
openssl rand -base64 32  # MOERISE_STATE_ENCRYPTION_KEY
```

> [!IMPORTANT]
> 妥善备份 `.env` 与 `MOERISE_STATE_ENCRYPTION_KEY`。主密钥丢失后，无法恢复已加密的模型和渠道凭据。

5. 启动完整服务：

```bash
docker compose up -d --build
docker compose ps
curl --fail http://localhost:3000/health/ready
```

打开 `http://localhost:3000`，使用绑定的 Memos 账户名和密码登录。Memos 需允许密码登录。界面自动匹配浏览器语言，也可在登录页或侧栏切换简体中文与英语。

---

## 二、连接已有 Memos 实例

已有实例需为 Memos 0.31.x：

1. 获取目标作者的 PAT 与 `user.name`。
2. 在 `.env` 中设置：

```dotenv
COMPOSE_PROFILES=
MOERISE_MEMOS_BASE_URL=https://memos.example.com
MOERISE_MEMOS_PUBLIC_URL=https://memos.example.com
```

- `MOERISE_MEMOS_BASE_URL`：Core 容器内可访问的 Memos 地址。
- `MOERISE_MEMOS_PUBLIC_URL`：浏览器打开 Memos 原文时使用的地址。

3. 填入作者绑定及独立密钥后启动：

```bash
docker compose up -d --build
```

---

## 三、配置可选功能

基础记录、阅读、关键词筛选和每日回顾无需第三方模型或消息渠道。在 Web「设置」中启用增强能力：

- **向量模型**：配置 OpenAI 兼容接口或百炼 DashScope，检查连接并等待索引构建完成，随后可使用语义搜索、混合搜索、随机漫步与认知地图。
- **大语言模型**：配置 OpenAI Chat Completions、OpenAI Responses 或 Anthropic Messages，检查连接后在 AI 洞察中选择笔记并展开对话。聊天无需 Embedding。
- **消息渠道**：[Telegram](TELEGRAM.zh-CN.md) 提供英文标题的回顾推送，[飞书](FEISHU.md) 提供中文回顾与私聊记录。新安装默认不启用渠道。

---

## 四、反向代理与 HTTPS

Compose 默认端口绑定回环地址。公网部署时，在 Web 前配置 Nginx、Caddy 或 Traefik 等 HTTPS 反向代理，并设置：

```dotenv
MOERISE_PUBLIC_ORIGIN=https://notes.example.com
```

将该域名转发至 Web 的 3000 端口。Web 内部代理 `/api/app/v1`；外部机器 REST 与 MCP（`/api/ext/v1`、`/mcp`）按需直接转发至 Core 的 8787 端口。

---

## 五、服务器资源配置

Core 根据容器 cgroup 调整内存预算与并发，常用档位见 `deploy/resources/`：

| 服务器规格 | 配置文件 |
| --- | --- |
| 1 核 / 1 GiB | `deploy/resources/1c1g.env` |
| 2 核 / 1 GiB | `deploy/resources/2c1g.env` |
| 2 核 / 2–4 GiB | `deploy/resources/2c2g.env` |
| 4 核 / 4–8 GiB | `deploy/resources/4c4g.env` |

将对应文件内容合并到 `.env`。这些值是内存上限，不是实测占用或笔记容量保证；为操作系统及其他服务预留空间。

---

## 六、备份与升级

数据保存在独立的命名卷中：

- `memos-data`：Memos 原文、附件与账户。
- `state-data`：Moerise 产品状态，包含 `state.sqlite3`。
- `index-data`：可重建索引，包含 `index.sqlite3`。
- `backup-data`：备份输出，挂载至 `/backups`。

Compose 会在卷名前添加项目名前缀。升级时保持 `COMPOSE_PROJECT_NAME`、Memos 实例标识与状态加密密钥一致。

1. 按[运行与恢复](OPERATIONS.md)备份 Memos 数据、Moerise 产品状态与 `.env`。
2. 切换到目标源码版本后重新构建并检查就绪状态：

   ```bash
   docker compose up -d --build
   curl --fail http://localhost:3000/health/ready
   ```

完整备份与恢复命令见[运行指南](OPERATIONS.md)。
