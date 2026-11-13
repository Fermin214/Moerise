# Moerise 运行、维护与恢复指南

[English](OPERATIONS.en.md)

Moerise 支持 Linux 原生二进制运行以及 Docker 容器部署。本文档提供生产环境配置、容器化编排、服务器资源规格推荐、数据备份恢复及索引重建的完整运维指导。

---

## 一、配置参数与环境变量对照

配置模板位于仓库根目录的 `.env.example` 与 `config.example.json`。下表列出常用环境变量，非空值优先于 JSON；模型与渠道等日常字段仅在首次初始化时读取部署值，之后以加密持久设置为准。

| 环境变量 | JSON 键名 | 默认值 | 说明与约束 |
| --- | --- | --- | --- |
| `MOERISE_MEMOS_INSTANCE_ID` | `memos.instance_id` | 必填 | Memos 实例的唯一稳定标识（8 至 128 字符） |
| `MOERISE_MEMOS_BASE_URL` | `memos.base_url` | 必填 | Core 服务直接访问 Memos 的 HTTP(S) 地址 |
| `MOERISE_MEMOS_PUBLIC_URL` | `memos.public_url` | 必填 | 浏览器中打开 Memos 原文的外部可访问基地址 |
| `MOERISE_MEMOS_USER` | `memos.user` | 必填 | 绑定的 Memos 作者资源名（格式如 `users/abc123`） |
| `MOERISE_MEMOS_TOKEN` | `memos.token` | 必填 | 绑定账号的 Memos 个人访问令牌 (PAT) |
| `MOERISE_ACCESS_TOKEN` | `access_tokens` | 必填 | 外部机器 REST API 与 MCP 调用的 Bearer 访问令牌 |
| `MOERISE_CURSOR_SECRET` | `cursor_secret` | 必填 | 用于分页签名与资源安全哈希的独立随机密钥（至少 32 字符） |
| `MOERISE_STATE_ENCRYPTION_KEY` | `web.state_encryption_key` | Web 或飞书应用机器人启用时必填 | 加密存储凭据的 32 字节 Base64 编码主密钥 |
| `MOERISE_WEB_ENABLED` | `web.enabled` | Compose 为 `true`；Core 默认 `false` | 是否启用 Web 客户端会话支持 |
| `MOERISE_PUBLIC_ORIGIN` | `web.public_origin` | Web 启用时必填 | Web 客户端公开 origin，协议、主机名和端口须与浏览器访问地址一致 |
| `MOERISE_TIMEZONE` | `timezone` | `Asia/Hong_Kong` | IANA 时区标识，用于每日回顾计算与时间线显示 |
| `MOERISE_STATE_DIR` | `state_dir` | `/data/state` | 保存业务数据库 `state.sqlite3` 的持久化目录 |
| `MOERISE_INDEX_DIR` | `index_dir` | `/data/index` | 保存可重建索引库 `index.sqlite3` 的持久化目录 |

### 独立随机密钥生成

在终端使用 OpenSSL 生成所需密钥并填入 `.env`：

```bash
openssl rand -hex 32     # MOERISE_ACCESS_TOKEN
openssl rand -hex 32     # MOERISE_CURSOR_SECRET
openssl rand -base64 32  # MOERISE_STATE_ENCRYPTION_KEY
```

---

## 二、Docker Compose 生产部署

### 1. 一体化部署（包含内置 Memos 0.31.x）

```bash
git clone https://github.com/Fermin214/Moerise.git
cd Moerise
cp .env.example .env

# 启动 Memos 服务并注册管理员
docker compose up -d memos
```

在浏览器打开 `http://localhost:5231` 创建账户并获取 PAT 与资源名，配置到 `.env` 后拉取镜像并启动整套服务。Core 与 Web 使用 GHCR 中 `MOERISE_IMAGE_TAG` 对应的版本镜像，自动匹配 AMD64 或 ARM64：

```bash
docker compose pull moerise web
docker compose up -d
docker compose ps
```

### 2. 连接已有 Memos 实例

在已有自托管 Memos 实例的场景下，在 `.env` 中关闭内置 Memos profile 并指定已有地址：

```dotenv
COMPOSE_PROFILES=
MOERISE_MEMOS_BASE_URL=https://memos.example.com
MOERISE_MEMOS_PUBLIC_URL=https://memos.example.com
```

已有实例需为 Memos 0.31.x，绑定账号与 PAT 须与 `.env` 中的资源名一致；容器内的 `localhost` 指容器自身。随后运行 `docker compose pull moerise web` 和 `docker compose up -d` 完成对接。

### 3. HTTPS 与运行检查

本地部署的 Web、Core 与 Memos 端口默认绑定回环地址。远程访问时，在 Web 前配置 HTTPS 反向代理，并将 `MOERISE_PUBLIC_ORIGIN` 设为浏览器访问的完整 origin（如 `https://notes.example.com`）。Web 只代理 `/api/app/v1`；机器 REST/MCP 和飞书回调按需转发至 Core 的 8787 端口。

```bash
curl --fail http://localhost:3000/health/ready
docker compose logs --tail=100 moerise web
```

就绪检查需静态资源正常、来源健康、全量同步完成且后台循环存活；否则返回 503。同步状态可通过带 `sync:read` 凭据的 `GET /api/ext/v1/sync` 查看。

### 4. Linux 二进制运行

使用与部署版本一致的 Linux 二进制和独立配置，数据目录分别指向产品状态与索引：

```bash
./build/moerise check-config --config .local/config.json
./build/moerise serve --config .local/config.json --host 127.0.0.1
```

Web 需单独构建与运行，步骤见 [Web 指南](WEB.md)。`--config` 优先于 `MOERISE_CONFIG_FILE`；二者省略时读取 `.local/config.json`。

---

## 三、服务器资源配置建议

Moerise Core 会自动根据 Linux cgroup 的内存限制与 CPU 配额动态计算任务并发度与分批预算。在 `deploy/resources/` 目录下提供了常见主机规格的预设配置，可直接合并入 `.env`：

| 服务器规格 | 推荐配置文件 | Core / Memos / Web 内存上限 | 合计上限 |
| --- | --- | --- | --- |
| 1 核 / 1 GiB | `deploy/resources/1c1g.env` | 256 / 192 / 96 MiB | 544 MiB |
| 2 核 / 1 GiB | `deploy/resources/2c1g.env` | 384 / 192 / 96 MiB | 672 MiB |
| 2 核 / 2–4 GiB | `deploy/resources/2c2g.env` | 512 / 256 / 128 MiB | 896 MiB |
| 4 核 / 4–8 GiB | `deploy/resources/4c4g.env` | 768 / 256 / 128 MiB | 1,152 MiB |

以上为服务内存上限，不是实测占用或笔记容量保证。笔记长度、分块数量、向量维度和模型并发均影响开销；为操作系统及其他服务预留空间。2C4G 主机可使用 `2c2g.env` 作为初始配置。

Core 读取 cgroup v1/v2 及父级限制，设置 Go 内存预算、来源分页与计算并发；显式 `GOMEMLIMIT` 优先。同步、向量构建和地图生成串行使用后台执行名额，交互来源读取使用独立请求队列。地图读取只加载快照，普通变化按 24 小时间隔合并更新，点选原文保持实时。

---

## 四、数据备份与容灾恢复

Moerise 严格实行**双库物理分离**机制：

```
/data/
├── state/
│   └── state.sqlite3    <-- 核心业务数据 (配置、会话、回顾、AI记录) -> 必须备份！
└── index/
    └── index.sqlite3    <-- 索引与地图数据 -> 可完全重建，无需备份
```

### 1. 在线热备份

Moerise 支持在线安全备份，无需停机：

**Docker 容器环境：**

```bash
docker compose exec -T moerise moerise backup --output /backups/state-snapshot
mkdir -p .local/backups
docker compose cp moerise:/backups/state-snapshot .local/backups/state-snapshot
```

每次选择新的输出目录；其中包含 `state.sqlite3` 与 `manifest.json`。备份使用 SQLite 一致快照，并校验完整性、外键、来源绑定、迁移摘要及 SHA-256。另行备份 Memos 的原文、附件与账户数据；Moerise 状态备份不包含这些内容。

> [!IMPORTANT]
> 必须将 `.env`（尤其是 `MOERISE_STATE_ENCRYPTION_KEY`）与备份文件一同安全异地归档。若密钥丢失，恢复后的数据库将无法解密模型与渠道配置。

### 2. 数据恢复

恢复时先使用兼容版本和相同的来源实例、地址、账号与主密钥，停止 Core 后执行恢复命令：

```bash
docker compose stop moerise
docker compose run --rm --no-deps -T moerise restore --source /backups/state-snapshot --safety-backup /backups/before-restore
docker compose up -d --no-build moerise
```

`--safety-backup` 使用新的目录，保存被替换的产品状态。恢复命令校验快照，清理派生索引与同步进度，并撤销旧浏览器会话；重启后重新全量同步，需要重新登录。普通服务重启保留有效会话。

### 3. 索引重建 (Reindex)

重建索引需先停止 Core；同一产品目录只允许一个写入进程。操作清理派生索引及同步进度，重启后从 Memos 重新扫描与构建，保留 AI 会话、回顾计划、设置及写入幂等记录：

```bash
docker compose stop moerise
docker compose run --rm --no-deps -T moerise rebuild-index
docker compose up -d --no-build moerise
```

原生部署停止服务后，分别使用 `backup --output`、`restore --source --safety-backup` 和 `rebuild-index` 命令，沿用原配置及数据目录。

## 五、升级与回滚

1. 备份 Memos 数据、Moerise 产品状态及配置，记录源码提交或镜像摘要。
2. 保持 Compose 项目名、来源绑定、数据卷和状态加密密钥一致，将 `.env` 中的 `MOERISE_IMAGE_TAG` 设为目标版本，执行 `docker compose pull moerise web` 和 `docker compose up -d`。
3. 检查就绪状态、原文和附件、设置版本、回顾与聊天历史；启用 Embedding 时另查索引构建状态。
4. 回滚时先停止服务并切换代码或镜像；数据库结构不兼容时，恢复目标版本对应的状态备份，再启动。

数据库迁移不会随 Git 回滚自动逆向执行。Memos 镜像在 Compose 中独立固定版本与摘要，升级 Memos 前先验证 Moerise 对目标协议版本的支持。
