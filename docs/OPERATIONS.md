# Moerise 运行、维护与恢复指南

Moerise 支持 Linux 原生二进制运行以及 Docker 容器部署。本文档提供生产环境配置、容器化编排、服务器资源规格推荐、数据备份恢复及索引重建的完整运维指导。

---

## 一、配置参数与环境变量对照

配置模板位于仓库根目录的 `.env.example` 与 `config.example.json`。所有参数均可通过环境变量传入（以 `MOERISE_*` 为前缀），非空环境变量优先于配置文件。

| 环境变量 | JSON 键名 | 默认值 | 说明与约束 |
| --- | --- | --- | --- |
| `MOERISE_MEMOS_INSTANCE_ID` | `memos.instance_id` | 必填 | 当前 Memos 实例的唯一标识（8 至 128 字符） |
| `MOERISE_MEMOS_BASE_URL` | `memos.base_url` | 必填 | Core 服务直接访问 Memos 的 HTTP(S) 地址 |
| `MOERISE_MEMOS_PUBLIC_URL` | `memos.public_url` | 必填 | 浏览器中打开 Memos 原文的外部可访问基地址 |
| `MOERISE_MEMOS_USER` | `memos.user` | 必填 | 绑定的 Memos 作者资源名（格式如 `users/abc123`） |
| `MOERISE_MEMOS_TOKEN` | `memos.token` | 必填 | 绑定账号的 Memos 个人访问令牌 (PAT) |
| `MOERISE_ACCESS_TOKEN` | `access_tokens` | 必填 | 外部机器 REST API 与 MCP 调用的 Bearer 访问令牌 |
| `MOERISE_CURSOR_SECRET` | `cursor_secret` | 必填 | 用于分页签名与资源安全哈希的独立随机密钥（32 字符） |
| `MOERISE_STATE_ENCRYPTION_KEY` | `web.state_encryption_key` | 必填 | 加密存储凭据的 32 字节 Base64 编码主密钥 |
| `MOERISE_WEB_ENABLED` | `web.enabled` | `true` | 是否启用 Web 客户端会话支持 |
| `MOERISE_PUBLIC_ORIGIN` | `web.public_origin` | 必填 | Web 客户端公开域名与协议（如 `https://notes.example.com`） |
| `MOERISE_TIMEZONE` | `timezone` | `Asia/Shanghai` | IANA 时区标识，用于每日回顾计算与时间线显示 |
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

在浏览器打开 `http://localhost:5231` 创建账户并获取 PAT 与资源名，配置到 `.env` 后启动整套服务：

```bash
docker compose up -d --build
docker compose ps
```

### 2. 连接已有 Memos 实例

在已有自托管 Memos 实例的场景下，在 `.env` 中关闭内置 Memos profile 并指定已有地址：

```dotenv
COMPOSE_PROFILES=
MOERISE_MEMOS_BASE_URL=https://memos.example.com
MOERISE_MEMOS_PUBLIC_URL=https://memos.example.com
```

随后运行 `docker compose up -d --build` 即可完成对接。

---

## 三、服务器资源配置建议

Moerise Core 会自动根据 Linux cgroup 的内存限制与 CPU 配额动态计算任务并发度与分批预算。在 `deploy/resources/` 目录下提供了常见主机规格的预设配置，可直接合并入 `.env`：

| 服务器规格 | 推荐配置文件 | 适用笔记规模 | 建议并发与内存 |
| --- | --- | --- | --- |
| 1 核 / 1 GiB | `deploy/resources/1c1g.env` | < 1,000 条 | 适合轻量个人速记，后台索引单线程串行构建 |
| 2 核 / 2 GiB | `deploy/resources/2c2g.env` | 1,000 ~ 10,000 条 | 绝大多数个人自托管首选，响应迅速、资源均衡 |
| 4 核 / 4 GiB+ | `deploy/resources/4c4g.env` | > 10,000 条 | 大规模笔记库，多核向量并行计算与瞬间检索 |

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
docker compose exec moerise moerise backup --output /data/state/backup-$(date +%Y%m%d).sqlite3
```

> [!IMPORTANT]
> 必须将 `.env`（尤其是 `MOERISE_STATE_ENCRYPTION_KEY`）与备份文件一同安全异地归档。若密钥丢失，恢复后的数据库将无法解密模型与渠道配置。

### 2. 数据恢复

恢复时需停止服务，替换 `state.sqlite3`，然后重启：

```bash
docker compose stop moerise
# 复制备份文件覆盖 /data/state/state.sqlite3
docker compose start moerise
```

### 3. 索引重建 (Reindex)

当发生异常损坏或升级了全新分词算法时，可手动触发索引重建。索引重建会清空 `index.sqlite3` 并重新从 Memos 扫描全部笔记，**绝不影响**用户的 AI 会话、回顾计划与系统配置：

```bash
docker compose exec moerise moerise reindex
```
