# Moerise 快速上手

[产品介绍](../README.md)

Moerise 支持 Linux 原生二进制与 Docker Linux 容器。容器部署需要 Docker Engine 和 Docker Compose 2.24 及以上版本，默认拉取 GHCR 的 AMD64/ARM64 版本镜像。一体化部署包含 Memos 0.31.x、内嵌 Web 的 Moerise Core 和观星布局服务，每个实例绑定一名 Memos 作者。

## 一、使用新的 Memos 实例

在仓库根目录执行：

```bash
cp .env.example .env
docker compose up -d memos
```

1. 打开 `http://localhost:5231`，注册 Memos 账户。
2. 在 Memos「设置 → 访问令牌」生成个人访问令牌（PAT）。
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
| `MOERISE_ACCESS_TOKEN` | 外部机器 REST/MCP 访问密钥（可选；留空则关闭外部接口） |
| `MOERISE_CURSOR_SECRET` | 分页签名的独立随机密钥 |
| `MOERISE_STATE_ENCRYPTION_KEY` | 加密持久凭据的 32 字节 Base64 密钥 |

分别生成随机密钥：

```bash
openssl rand -hex 32     # MOERISE_CURSOR_SECRET
openssl rand -base64 32  # MOERISE_STATE_ENCRYPTION_KEY
openssl rand -hex 32     # MOERISE_ACCESS_TOKEN（可选）
```

> [!IMPORTANT]
> 请妥善备份 `.env` 与 `MOERISE_STATE_ENCRYPTION_KEY`。主密钥丢失后，无法解密已保存的模型和渠道凭据。

5. 启动完整服务：

```bash
docker compose pull moerise layout
docker compose up -d
docker compose ps
curl --fail http://localhost:3000/health/ready
```

打开 `http://localhost:3000`，使用绑定的 Memos 用户名和密码登录（Memos 需允许密码登录）。界面默认匹配浏览器语言，也可在登录页或「设置 → 通用」切换简体中文与英语。

## 二、连接已有 Memos 实例

已有实例需为 Memos 0.31.x：

1. 获取目标作者的 PAT 与 `user.name`。
2. 在 `.env` 中设置：

```dotenv
COMPOSE_PROFILES=stargazing
MOERISE_MEMOS_BASE_URL=https://memos.example.com
MOERISE_MEMOS_PUBLIC_URL=https://memos.example.com
```

- `MOERISE_MEMOS_BASE_URL`：Core 容器内可访问的 Memos 地址。
- `MOERISE_MEMOS_PUBLIC_URL`：浏览器打开 Memos 原文时使用的地址。
- 不需要观星时，将 `COMPOSE_PROFILES` 留空即可。

3. 填入作者绑定与密钥后启动：

```bash
docker compose pull moerise layout
docker compose up -d
```

不使用观星时只拉取 `moerise`。

## 三、配置可选功能

基础记录、阅读、关键词筛选和每日回顾无需模型或消息渠道。可在「设置」中按需开启增强功能：

- **AI（Embedding 与 LLM）**：在「设置 → AI」配置向量模型（OpenAI 兼容或阿里云百炼 DashScope）与大语言模型（OpenAI Chat Completions、OpenAI Responses 或 Anthropic Messages）。向量索引就绪后可使用语义搜索、混合检索、漫步与观星；AI 洞察只需配置大语言模型。
- **消息渠道**：在「设置 → 回顾与推送」按需配置 [QQ](QQ.md)、[Telegram](TELEGRAM.md) 或 [飞书](FEISHU.md)，支持私聊速记、指令查回顾和定时推送。新安装默认不启用任何渠道。
- **Android 客户端**：从 Releases 下载签名 APK 并填写对外 HTTPS 服务地址登录，详见 [Android 安装与升级](android/INSTALL.md)。

## 四、反向代理与 HTTPS

Compose 默认端口绑定回环地址。公网部署时，在 Core 前配置 Nginx、Caddy 或 Traefik 等 HTTPS 反向代理，并设置：

```dotenv
MOERISE_PUBLIC_ORIGIN=https://notes.example.com
```

`MOERISE_PUBLIC_ORIGIN` 必须与浏览器访问的协议、主机名和端口一致。将域名转发至 Core 对外映射的 3000 端口即可；网页、`/api/app/v1`、外部只读 REST 与 MCP（`/api/ext/v1`、`/mcp`）共用同一入口，外部接口仍需独立令牌。

## 五、服务器资源配置

Core 会读取容器 cgroup 限额调整内存预算与并发，常用规格预设位于 `deploy/resources/`：

| 服务器规格 | 配置文件 |
| --- | --- |
| 1 核 / 1 GiB | `deploy/resources/1c1g.env` |
| 2 核 / 1 GiB | `deploy/resources/2c1g.env` |
| 2 核 / 2–4 GiB | `deploy/resources/2c2g.env` |
| 4 核 / 4–8 GiB | `deploy/resources/4c4g.env` |

将对应文件内容合并到 `.env` 即可。预设值为容器内存上限，请为操作系统、布局服务及其他进程预留空间。

## 六、备份与升级

数据保存在独立命名卷中：

- `memos-data`：Memos 原文、附件与账户。
- `state-data`：Moerise 产品状态（`state.sqlite3`）。
- `index-data`：可重建索引（`index.sqlite3`）。
- `backup-data`：备份输出，挂载至 `/backups`。

Compose 会在卷名前添加项目名前缀，升级时请保持 `COMPOSE_PROJECT_NAME`、Memos 实例标识与状态加密密钥一致。

1. 按[运行与恢复](OPERATIONS.md)备份 Memos 数据、Moerise 产品状态与 `.env`。
2. 将 `.env` 中的 `MOERISE_IMAGE_TAG` 设为目标版本。从旧版独立 Web 容器升级至内嵌 Web 时，将 `MOERISE_PORT`、`MOERISE_BIND_ADDRESS` 改为原 `WEB_PORT`、`WEB_BIND_ADDRESS`，并删除旧的 `WEB_*` 变量。
3. 拉取新镜像并重建容器（`down` 不加 `--volumes`，保留全部数据卷）：

   ```bash
   docker compose pull moerise layout
   docker compose down --remove-orphans
   docker compose up -d
   curl --fail http://localhost:3000/health/ready
   ```

完整备份、恢复与回滚命令见[运行与恢复指南](OPERATIONS.md)。
