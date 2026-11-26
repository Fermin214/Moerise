# Moerise 运行、维护与恢复指南

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
| `MOERISE_ACCESS_TOKEN` | `access_tokens` | 可选 | 外部机器 REST API 与 MCP 调用的 Bearer 访问令牌；留空则禁用 `/api/ext/v1` 与 `/mcp` |
| `MOERISE_CURSOR_SECRET` | `cursor_secret` | 必填 | 用于分页签名与资源安全哈希的独立随机密钥（至少 32 字符） |
| `MOERISE_STATE_ENCRYPTION_KEY` | `web.state_encryption_key` | Web 或机器人私聊收件必填 | 加密存储凭据和待写入正文的 32 字节 Base64 编码主密钥 |
| `MOERISE_WEB_ENABLED` | `web.enabled` | Compose 为 `true`；Core 默认 `false` | 是否启用 Web 客户端会话支持 |
| `MOERISE_TELEMETRY_FORWARDING` | `telemetry_forwarding` | `true` | 允许 Core 把用户同意分享的汇总转发给构建时指定的项目接收端。重启 Core 后生效。 |
| `MOERISE_PUBLIC_ORIGIN` | `web.public_origin` | Web 启用时必填 | Web 客户端公开 origin，协议、主机名和端口须与浏览器访问地址一致 |
| `MOERISE_TIMEZONE` | `timezone` | `Asia/Hong_Kong` | IANA 时区标识，用于每日回顾计算与时间线显示 |
| `MOERISE_STATE_DIR` | `state_dir` | `/data/state` | 保存业务数据库 `state.sqlite3` 的持久化目录 |
| `MOERISE_INDEX_DIR` | `index_dir` | `/data/index` | 保存可重建索引库 `index.sqlite3` 的持久化目录 |
| `MOERISE_LAYOUT_URL` | — | 原生 `http://127.0.0.1:8766`；Compose `http://layout:8766` | Core 访问的私有布局服务地址 |
| `MOERISE_STARGAZING_ENABLED` | `stargazing.enabled` | 新实例为 `false` | 已保存设置优先，尚未保存时可用部署值显式启停 |
| `MOERISE_LAYOUT_STATE` | — | `/data` | Python 布局服务的模型、缓存和任务目录 |
| `LAYOUT_MEMORY_LIMIT` / `LAYOUT_CPU_LIMIT` | — | `1g` / `1.0` | Compose 布局服务的内存和 CPU 上限 |

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

在浏览器打开 `http://localhost:5231` 创建账户并获取 PAT 与资源名，配置到 `.env` 后拉取镜像并启动整套服务。内嵌 Web 的 Core 使用 GHCR 中 `MOERISE_IMAGE_TAG` 对应的版本镜像，自动匹配 AMD64 或 ARM64：

```bash
docker compose pull moerise
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

已有实例需为 Memos 0.31.x，绑定账号与 PAT 须与 `.env` 中的资源名一致。容器内的 `localhost` 指容器自身。随后运行 `docker compose pull moerise` 和 `docker compose up -d` 完成对接。源码部署使用 `docker compose up -d --build`。

### 观星按需启用

内置 Memos 默认使用 `COMPOSE_PROFILES=memos`，外接 Memos 时留空。启用观星时给内置 Memos 追加 `,stargazing`，外接 Memos 则设为 `stargazing`。执行 `docker compose pull layout` 和 `docker compose up -d layout`，配置 Embedding，再在「设置 → 通用」打开「启用观星」。

停用观星时，在「设置 → 通用」关闭「启用观星」，停止布局服务，再从 `COMPOSE_PROFILES` 移除 `stargazing`：

```bash
docker compose stop layout
```

已有持久设置的实例沿用保存值。升级时，既无保存值也无显式部署值的旧实例继续开启观星。应用开关只控制 Core 布局调度。关闭后保留快照、向量和 `layout-data` 卷，语义检索与漫步照常使用。

### 3. HTTPS 与运行检查

Core 与 Memos 的端口默认绑定回环地址。Core 在 `http://localhost:3000` 同时提供网页与 API。远程访问时，在 Core 前配置 HTTPS 反向代理，将 `MOERISE_PUBLIC_ORIGIN` 设为浏览器访问的完整 origin（如 `https://notes.example.com`）；按需开放只读 REST/MCP 和飞书回调路径。

```bash
curl --fail http://localhost:3000/health/ready
docker compose logs --tail=100 moerise
```

就绪检查需静态资源正常、来源健康、全量同步完成且后台循环存活；否则返回 503。同步状态可通过带 `sync:read` 凭据的 `GET /api/ext/v1/sync` 查看。

### 4. Linux 二进制运行

使用与部署版本一致的 Linux 二进制和独立配置，数据目录分别指向产品状态与索引：

```bash
./build/moerise check-config --config .local/config.json
./build/moerise serve --config .local/config.json --host 127.0.0.1
```

发行二进制内嵌 Web，启用 `web.enabled` 后由同一端口提供网页。源码构建先运行 `npm --prefix web run build`，再运行 `go build .`。`--config` 优先于 `MOERISE_CONFIG_FILE`；二者省略时读取 `.local/config.json`。

启用观星时，使用 Python 3.12 在独立终端或服务管理器中启动布局服务：

```bash
python3.12 -m venv .local/layout-venv
.local/layout-venv/bin/pip install -r services/layout/requirements.txt
MOERISE_LAYOUT_STATE="$PWD/.local/layout-data" \
  .local/layout-venv/bin/python services/layout/server.py serve
```

服务默认仅监听 `127.0.0.1:8766`。将模型目录持久化，Core 默认连接该地址。布局服务不可用时，已发布星图仍可读取；首次生成和后续更新会重试。生产环境通过 systemd 等进程管理器启动 Core 和布局服务。

---

## 三、服务器资源配置建议

Moerise Core 会自动根据 Linux cgroup 的内存限制与 CPU 配额动态计算任务并发度与分批预算。在 `deploy/resources/` 目录下提供了常见主机规格的预设配置，可直接合并入 `.env`：

| 服务器规格 | 推荐配置文件 | Core / Memos 内存上限 | 基础服务合计上限 |
| --- | --- | --- | --- |
| 1 核 / 1 GiB | `deploy/resources/1c1g.env` | 256 / 192 MiB | 448 MiB |
| 2 核 / 1 GiB | `deploy/resources/2c1g.env` | 384 / 192 MiB | 576 MiB |
| 2 核 / 2–4 GiB | `deploy/resources/2c2g.env` | 512 / 256 MiB | 768 MiB |
| 4 核 / 4–8 GiB | `deploy/resources/4c4g.env` | 768 / 256 MiB | 1,024 MiB |

表中不含布局服务。启用观星应另计 `LAYOUT_MEMORY_LIMIT` 的 1 GiB 上限，建议整机至少 2 GiB；1 GiB 档仅用于基础服务预算。布局任务结束后科学计算子进程退出，父进程保持轻量。笔记长度、分块数量、向量维度和模型并发均影响开销；为操作系统及其他服务预留空间。2C4G 主机可使用 `2c2g.env` 作为初始配置。

Core 读取 cgroup v1/v2 及父级限制，设置 Go 内存预算、来源分页与计算并发；显式 `GOMEMLIMIT` 优先。同步与向量构建共享后台名额，布局计算由独立服务执行，不占用该名额。观星读取只加载快照，每日服务时区 `03:00` 按需调度增量更新并支持手动重建。布局卷 `layout-data` 保存 UMAP 模型与计算缓存，可重建；保留该卷有助于延续增量坐标框架。

---

## 四、数据备份与容灾恢复

Moerise 采用双库分离存储：

```
/data/
├── state/
│   └── state.sqlite3    <-- 核心业务数据 (配置、会话、回顾) -> 必须备份！
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

重建索引需先停止 Core。同一产品目录只允许一个写入进程。操作清理派生索引及同步进度，重启后从 Memos 重新扫描与构建，保留回顾计划、设置及写入幂等记录：

```bash
docker compose stop moerise
docker compose run --rm --no-deps -T moerise rebuild-index
docker compose up -d --no-build moerise
```

原生部署停止服务后，分别使用 `backup --output`、`restore --source --safety-backup` 和 `rebuild-index` 命令，沿用原配置及数据目录。

## 五、升级与回滚

1. 升级前执行一次手工 `backup`，另存 Memos 数据、原 `config.json`、`.env` 和状态加密密钥，记录源码提交或镜像摘要。Core 启动时会在待执行迁移前使用 `VACUUM INTO` 热备 `state.sqlite3`，校验完整性和外键后保存到 `<state_dir>/backups/auto-pre-<version>-<timestamp>.sqlite3`，最多保留最近 5 份。`<version>` 取执行迁移的 Core 版本。
2. 保持 Compose 项目名、来源绑定、数据卷和状态加密密钥一致，将 `.env` 中的 `MOERISE_IMAGE_TAG` 设为目标版本。启用观星时，确保 `COMPOSE_PROFILES` 包含 `stargazing`，执行 `docker compose pull moerise layout`；停用观星时只拉取 `moerise`。独立 Web 容器部署升级为内嵌 Web 时，将 `MOERISE_PORT` 改为原 `WEB_PORT`（默认 `3000`），将 `MOERISE_BIND_ADDRESS` 设为原 `WEB_BIND_ADDRESS`，删除旧 `WEB_*` 配置；`MOERISE_PUBLIC_ORIGIN` 沿用对外地址。执行 `docker compose down --remove-orphans` 清除旧 Web 容器，再运行 `docker compose up -d`。`down` 不加 `--volumes`，命名卷保留。
3. 先升级 Core，检查就绪状态、原文、附件、设置版本与回顾，再强制刷新 Web 或升级 Android APK。过渡期 `0.3.0` Android 仍可读写和同步笔记、检索、回顾、漫步、观星与上报遥测。洞察端点返回 404，旧设置页须升级客户端后使用。

启用观星的原生或独立部署，须将 Core 和布局服务更新到同一发行版本并重启两者。原生部署使用目标版本的 `services/layout/` 和对应 Python 依赖，独立容器部署更新 Core 与 `layout` 镜像。保留布局服务的数据目录或卷。`0.3.0` 布局服务即使 `/health` 返回 `200`，也不支持新 Core 的命名协议；版本不兼容时 Core 保留已发布星图，待兼容服务上线后恢复生成。

升级应用迁移 `010` 后，`0.3.0` 无法直接打开状态库。Core 读取原部署文件时忽略退役的 `llm`、`insights`、`stargazing.llm_naming`，其余配置字段仍须通过严格校验。历史对话表及旧版加密设置保留在状态库。

回退 `0.3.0` 时先停止服务。使用升级后的 Core 执行 `moerise rollback --backup <快照文件>`，明确填写升级生成的 `auto-pre-0.4.0-*.sqlite3` 文件，或手工备份目录下的 `state.sqlite3` 文件。`--backup` 参数要求路径指向文件，且应使用升级前记录的目标快照。命令先生成 `pre-rollback-*` 安全副本，再替换状态库并清理同步进度与派生索引。

```bash
docker compose stop moerise
docker compose run --rm --no-deps -T moerise rollback --backup /backups/pre-upgrade/state.sqlite3
# 然后恢复原配置并将 MOERISE_IMAGE_TAG 改回 0.3.0
docker compose up -d --no-build moerise
```

原生部署同样先指定快照回滚，再恢复原 `config.json`、状态加密密钥和 `0.3.0` 二进制。重启后派生索引从 Memos 重建。
