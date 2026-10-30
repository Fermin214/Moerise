# Moerise 运行与恢复

Moerise 支持 Linux 原生二进制和 Docker Linux 容器部署，两者都需要可访问的 Memos 0.31.x 实例与对应账号的 PAT。以下命令在仓库根目录执行，使用 Bash。Windows、macOS 原生服务端支持见[路线图](ROADMAP.md#后续方向与开发输入)。

## 配置

配置模板为 `config.example.json` 和 `.env.example`。服务默认读取 `.local/config.json`，也可通过 `MOERISE_CONFIG_FILE` 或命令参数 `--config` 指定路径，参数优先。配置按内置默认值、JSON、非空 `MOERISE_*` 环境变量的顺序覆盖，JSON 字符串不进行变量替换。Compose 自动将可选的 `.env` 传给 Core；二进制运行时由进程环境传入。

| 配置 | 含义与要求 |
| --- | --- |
| `memos.instance_id` | 稳定且唯一的实例标识，8 至 128 个字母、数字、下划线或连字符 |
| `memos.base_url` | 服务进程可直接访问的 Memos HTTP(S) 根地址 |
| `memos.public_url` | 使用者打开原文的 Memos 根地址 |
| `memos.user` | PAT 所属账号的 `users/{id}` 资源名，取自 `/api/v1/auth/me` |
| `memos.token` | 该账号的 Memos PAT，至少 16 字符 |
| `access_tokens` | Moerise 独立访问令牌列表，每项有名称、令牌和 Scope；令牌至少 32 字符 |
| `cursor_secret` | 独立随机密钥，至少 32 字符，用于分页签名及按来源和幂等键派生新资源标识；随配置备份 |
| `web.enabled` | Web 客户端会话开关，默认关闭 |
| `web.public_origin` | Web 公开 Origin，启用会话时必填；使用 HTTPS，仅回环地址允许 HTTP |
| `web.state_encryption_key` | 启用会话时必填，独立随机 32 字节密钥的标准 Base64 编码，用于按用途派生状态加密密钥 |
| `timezone` | IANA 时区名，用于产品日期与调度；初始默认值 `Asia/Hong_Kong` |
| `state_dir`、`index_dir` | 不同的可写持久目录，默认 `/data/state`、`/data/index` |
| `sync` | 增量轮询、全量修复、重叠时间、退避及任务租约 |
| `review` | 随机回顾数量、等待期、近期排除期与归档范围，默认 3 条、72 小时、7 天，详见[每日回顾](DAILY-REVIEW.md) |
| `notifications` | 主动推送开关和 `times` 时段列表，默认关闭、每天 `09:00` 一次；使用顶层时区 |
| `channel` | 飞书应用凭据、事件验证、绑定身份及推送渠道，详见[飞书入口](FEISHU.md) |
| `embedding` | 独立的向量模型配置，默认关闭；百炼配置与连接检查见[向量模型](EMBEDDING.md) |
| `llm` | 独立的对话模型配置，默认关闭；协议、限额与连接检查见 [AI](AI.md) |

扩展令牌须各不相同，且与 Memos PAT 分开。分别用密码管理器生成至少 32 字符的随机令牌和分页密钥；安装了 OpenSSL 时也可使用 `openssl rand -hex 32`。Scope 说明见 [API v1](API-V1.md#资源与身份)。

模板中的 `operator` 包含 `notes:read`、`sync:read`、`reviews:read`，`reader` 包含 `notes:read`、`reviews:read`。外部机器入口由只读操作白名单约束，写入 Scope 也不能开放写操作或生成。原生客户端通过独立浏览器会话创建笔记，请求需携带 `Idempotency-Key`；503 `WRITE_OUTCOME_UNKNOWN` 表示写入结果不明，只能使用同一键和请求体重试，完整规则见[原生客户端](API-V1.md#原生客户端)。

扩展 MCP 使用相同的 Moerise 令牌，连接服务的 `/mcp`；具体工具和语义见 [API v1](API-V1.md#每日回顾与-mcp)。

飞书应用回调和后台事件处理随 `serve` 启动。待处理事件加密暂存于产品数据库，业务完成后删除密文。配置状态主密钥后，队列使用独立派生密钥；保存设置时迁移旧 AppSecret 密文，无法解密则保存失败。迁移前的旧队列仍需原应用 Secret，迁移后的队列与设置需相同状态主密钥。恢复快照前结合 Memos 核实快照之后的写入；已有幂等记录保留资源标识，新记录按配置密钥和幂等键派生标识。事件回复的结果不明状态与笔记写入结果分别保存。

调度记录已处理的日期和时段，恢复运行后处理当天到期时段，不补建往日推送。关闭推送仍生成当天回顾。发送结果不明时通过通知 API 查看并确认，见[调度与发送队列](NOTIFICATIONS.md)。恢复产品状态快照将等待及执行中的通知、未完成事件的回复标为 `unknown`。

每日回顾每次先单独发送一次 `「今日回顾」`，再逐篇发送完整正文、原图及尾注 `#标签  创建于YYYY-MM-DD`；无标签时尾注只有 `创建于YYYY-MM-DD`。日期取来源 `create_time`，按顶层 `timezone` 换算。不附加链接、片号或状态提示，普通文件不推送。没有可发送内容的笔记不发尾注，整次均无内容时不发标题。长文按请求大小限制切分，按正文片顺序拼接可还原完整原文。Webhook 发送图片需额外配置应用的 `app_id`、`app_secret` 及图片资源权限，见[飞书入口](FEISHU.md#自定义机器人-webhook)。发送途中笔记变更时，跳过该笔记的剩余分片。

服务绑定来源根地址和账号。更换来源或账号时使用新的 `instance_id` 和数据卷。根地址不包含账号密码、路径、查询参数或片段；Memos 请求直接连接来源，不读取环境中的 HTTP 代理设置。

默认增量间隔为 60 秒，全量修复间隔为 6 小时，水位重叠为 5 分钟。`check-config` 校验有效配置；来源连接、版本和账号在同步时检查。

设置状态加密主密钥后，首次启动将有效日常配置加密写入 `state.sqlite3`。后续启动以持久值为准，部署中的回顾、时区、模型与渠道字段只用于首次初始化；基础设施仍按 JSON 和环境变量加载。旧实例首次启用时保留原有自定义值。密钥不匹配时启动失败，不能用默认值覆盖密文。字段与密钥操作见[产品设置](SETTINGS.md)。

### 环境参数

环境变量使用以下对应关系；空值保留 JSON 或内置默认值。`MOERISE_ACCESS_TOKEN` 非空时以单个 `environment` 凭据替换 JSON 令牌列表，包含全部支持的 Scope；MCP 的工具集合始终只读。

| 环境变量 | 配置 |
| --- | --- |
| `MOERISE_MEMOS_INSTANCE_ID`、`MOERISE_MEMOS_BASE_URL`、`MOERISE_MEMOS_PUBLIC_URL` | `memos.instance_id`、`base_url`、`public_url` |
| `MOERISE_MEMOS_USER`、`MOERISE_MEMOS_TOKEN` | `memos.user`、`token` |
| `MOERISE_ACCESS_TOKEN`、`MOERISE_CURSOR_SECRET` | 机器接口令牌、分页及幂等密钥 |
| `MOERISE_WEB_ENABLED`、`MOERISE_PUBLIC_ORIGIN`、`MOERISE_STATE_ENCRYPTION_KEY` | `web.enabled`、`web.public_origin`、`web.state_encryption_key` |
| `MOERISE_TIMEZONE`、`MOERISE_STATE_DIR`、`MOERISE_INDEX_DIR` | 时区、状态目录、索引目录 |
| `MOERISE_REVIEW_COUNT`、`MOERISE_REVIEW_MIN_AGE_HOURS`、`MOERISE_REVIEW_RECENT_DAYS` | 回顾条数、等待小时、排除天数 |
| `MOERISE_REVIEW_INCLUDE_ARCHIVED` | 是否包含归档，`true` 或 `false` |
| `MOERISE_NOTIFICATIONS_ENABLED`、`MOERISE_NOTIFICATIONS_TIMES` | 推送开关、逗号分隔时段，例如 `09:00,18:00` |
| `MOERISE_NOTIFICATIONS_TIME` | 单时段时间；配置了 `times` 时以列表为准 |
| `MOERISE_CHANNEL_KIND` | `feishu-app` 或 `feishu-webhook` |
| `MOERISE_FEISHU_APP_ID`、`MOERISE_FEISHU_APP_SECRET` | 应用凭据 |
| `MOERISE_FEISHU_VERIFICATION_TOKEN`、`MOERISE_FEISHU_ENCRYPT_KEY` | 回调校验及可选加密 |
| `MOERISE_FEISHU_USER_OPEN_ID` | 专用私聊身份与主动推送接收者 |
| `MOERISE_FEISHU_WEBHOOK_URL`、`MOERISE_FEISHU_WEBHOOK_SECRET` | 自定义机器人地址与可选签名密钥 |

只通过环境运行二进制时，填写全部必要来源、鉴权及密钥变量即可省略 JSON 文件。Compose 挂载 `.local/config.json`；采用环境配置时先复制模板创建此文件，再由 `.env` 覆盖必要值。服务不支持浏览器设置，修改配置后需重启。

### Web 会话

使用 `openssl rand -base64 32` 生成独立的状态加密主密钥，与 PAT、机器令牌和分页密钥分别保存。`web.public_origin` 只填写协议、主机和可选端口，例如 `https://notes.example.com`；本地联调可用 `http://127.0.0.1:8787`。主机名使用 ASCII 或 Punycode；服务会规范化大小写、IPv6 和默认端口，以匹配浏览器 Origin。启用后，Core 必须能够访问 Memos 的官方 Connect `SignIn`、`RefreshToken`、`SignOut` 和 REST `/api/v1/auth/me`。

客户端通过 Memos 用户名和密码登录，登录账户必须等于实例绑定作者及后台 PAT 所属账户。浏览器会话 ID 只保存摘要，刷新凭据以按用途派生的 AEAD 密钥加密保存在 `state.sqlite3`，短期访问令牌只保存在内存。登录有效期不超过登录时的刷新凭据到期时间，上游续期不延长登录期限。普通重启和升级通过刷新凭据恢复登录；到期或更换加密密钥后需要重新登录。退出先删除本地会话，上游清理失败仍完成本地退出。

产品快照包含刷新凭据密文，状态加密主密钥随部署配置单独备份。恢复产品快照会清空客户端会话，所有浏览器需要重新登录；索引重建保留会话。

## 二进制运行

在 Linux 上从源码构建需要 Go 1.27+，建议使用该系列的最新补丁版本。运行编译产物无需 Go 工具链或其他语言运行时：

```bash
CGO_ENABLED=0 go build -trimpath -o build/moerise .
mkdir -p .local/backups
cp config.example.json .local/config.json
```

按配置表填写 `.local/config.json`，并将 `state_dir`、`index_dir` 分别设为运行账号可写的目录，例如 `.local/state`、`.local/index`。相对路径以进程工作目录为基准。

```bash
./build/moerise check-config
./build/moerise serve --host 127.0.0.1
```

`serve` 通过 `--host` 和 `--port` 设置监听地址与端口，默认值分别为 `0.0.0.0` 和 `8787`。长期运行时使用操作系统的服务管理器，设置二进制路径、工作目录、配置路径和进程重启策略。

## 容器部署

需要支持 Linux 容器的 Docker Engine 和 Compose 插件。获取源码后准备配置与备份目录：

```bash
mkdir -p .local/backups
cp config.example.json .local/config.json
```

按配置表填写 `.local/config.json`，数据目录沿用模板中的 `/data/state`、`/data/index`。`memos.base_url` 必须从容器内部可达；同一 Compose 网络中的服务可通过服务名访问。

Linux 的配置与备份绑定挂载需允许 UID 10001 读取配置、写入备份目录。例如，在启动前执行：

```bash
sudo chown 10001:10001 .local/config.json .local/backups
sudo chmod 600 .local/config.json
sudo chmod 700 .local/backups
```

也可用 ACL 授予对应权限。使用 rootless Docker 或用户命名空间映射时，按实际 UID 映射设置宿主机权限。命名数据卷由镜像中的 UID 10001 目录初始化。

启动服务并检查配置：

```bash
docker compose up -d --build moerise
docker compose exec -T moerise moerise check-config
docker compose ps
```

以上命令只启动 Moerise，连接配置中的已有 Memos。随附 Compose 配置使用命名卷保存产品状态和索引，将配置以只读方式挂载，并将 `.local/backups/` 挂载到 `/backups`。容器以 UID 10001 运行，根文件系统只读，内存上限为 384 MiB、CPU 上限为 1 核；可按负载调整 Compose 配置。

宿主机端口由 `MOERISE_PORT` 设置，默认只发布回环地址。Dockerfile 使用锁定摘要的 Go 构建镜像；运行镜像仅包含静态二进制和 CA 证书，时区数据嵌入二进制。依赖版本和校验和由 Go 模块文件管理。

需要额外部署测试 Memos 时，`docker compose --profile local up -d memos` 可启动随附的 Memos 0.31.0 容器，宿主机端口由 `MEMOS_PORT` 设置。账号初始化和 PAT 创建使用 Memos 自身的流程，再将连接信息填入 Moerise 配置。

## 运行检查

| 入口 | 含义 |
| --- | --- |
| `/docs`、`/openapi.json` | 交互式 API 文档与 OpenAPI 定义 |
| `/health/live` | 进程存活与服务版本 |
| `/health/ready` | 来源健康、已有成功全量同步且工作循环存活时返回 200，否则 503 |
| `/api/ext/v1/sync` | 使用 `sync:read` 凭据查看同步状态与错误代码 |
| `moerise check-embedding` | 使用内置虚构文本检查向量模型连接、返回数量和维度；独立于 Memos 与数据库运行 |
| `moerise check-llm` | 使用内置虚构文本检查对话模型的流式连接与完成状态；独立于 Memos 与数据库运行 |

服务启动后执行全量同步。就绪检查返回 503 时，通过同步状态与服务日志检查来源连接、版本、账号及任务错误。请求方式与错误代码见 [API v1](API-V1.md)。

对外访问时，通过反向代理配置 HTTPS，并按 API 契约提供扩展令牌。同一产品目录只允许一个服务写入进程，恢复和索引重建需停止服务。

## 存储与备份范围

| 对象 | 内容 | 备份或恢复方式 |
| --- | --- | --- |
| Memos 数据 | 原文、附件、账号和令牌状态 | Memos 实例自身的完整数据备份 |
| `state_dir/state.sqlite3` | 绑定、水位、持久任务、笔记创建幂等记录及产品状态 | 扩展状态快照 |
| `index_dir/index.sqlite3` | 笔记投影、扫描暂存和索引代次 | 从 Memos 重建 |
| 配置文件 | 来源、访问令牌、时区和其他参数 | 独立配置备份 |

数据库类型与同步顺序见[产品与架构](PRODUCT-ARCHITECTURE.md#数据与身份)。配置和备份按敏感数据保存；`.local/` 已排除 Git 和镜像构建上下文。自行选择其他目录时，将其排除版本管理。

笔记创建幂等记录保存键、规范化内容摘要、分配的 `memoId` 和处理状态，不缓存笔记原文。这些记录用于重启后的结果复查和同 ID 重试，须随产品状态备份，清理索引时保留。恢复早于创建操作的快照会丢失对应幂等记录，重放这些请求前需结合 Memos 原文核实结果。

## 备份与恢复

附件文件由 Memos 保存，需纳入 Memos 备份。Moerise 产品状态保存附件创建的资源标识和部分完成状态；恢复后使用原幂等键及完整请求体继续上传。扩展快照不包含文件字节。

以下原生命令使用部署版本的二进制、工作目录和配置。容器命令使用随附 Compose 的挂载路径。每次备份选择新的输出目录。

### 备份产品状态

产品状态可在服务运行时备份：

```bash
./build/moerise backup --output .local/backups/state-snapshot
```

容器部署使用：

```bash
docker compose exec -T moerise moerise backup --output /backups/state-snapshot
```

输出目录包含 `state.sqlite3` 和 `manifest.json`。快照通过 SQLite `VACUUM INTO` 生成一致副本，再检查完整性、外键、来源绑定和迁移摘要。备份格式为 `1`，清单包含服务版本和数据库 SHA-256。

完整恢复还需要单独备份配置文件、Memos 数据和运行版本。Memos 备份应包含原文、附件、账号及令牌状态，按对应版本的备份方法执行。产品状态快照不包含这些数据。

### 恢复产品状态

先选择与备份迁移结构兼容的代码或镜像，并配置相同的来源实例、地址和账号。

原生部署通过服务管理器或终端停止服务后执行：

```bash
./build/moerise restore --source .local/backups/state-snapshot --safety-backup .local/backups/before-restore
```

容器部署使用：

```bash
docker compose stop moerise
docker compose run --rm --no-deps -T moerise restore --source /backups/state-snapshot --safety-backup /backups/before-restore
docker compose up -d --no-build moerise
```

恢复会校验摘要、数据库、迁移和来源绑定，另存覆盖前的产品数据库，清理同步进度及同步任务，并删除派生索引。`--safety-backup` 使用新的目录名。重新启动服务后执行全量同步，产品历史和人工状态从快照恢复。

恢复同时清空客户端会话，旧浏览器 Cookie 不能继续使用；普通重启和索引重建保留有效会话。

### 重建索引

原生部署停止服务后执行，再通过服务管理器或 `serve` 命令启动：

```bash
./build/moerise rebuild-index
```

容器部署使用：

```bash
docker compose stop moerise
docker compose run --rm --no-deps -T moerise rebuild-index
docker compose up -d --no-build moerise
```

索引重建清理投影、分块、向量、构建进度与相关同步进度，服务启动后重新扫描 Memos；启用 `embedding` 时自动分批构建向量索引。笔记创建幂等记录保留在 `state.sqlite3`，重建后相同键仍沿用原分配的 `memoId`。回顾历史、阅读和通知状态同样保留。索引进度通过 `GET /api/ext/v1/sync` 的 `vector_index` 查看，详见[向量索引](EMBEDDING.md#分块与向量索引)。

## 升级与回滚

1. 升级前备份产品状态、配置和 Memos 数据，记录对应的源码提交或镜像摘要。
2. 停止服务，切换到目标代码或镜像。源码部署重新构建目标版本二进制；使用随附 Compose 构建时执行 `docker compose build moerise`。
3. 以同一配置和数据目录启动服务，检查就绪状态及同步结果。
4. 回滚时停止服务，切换到目标版本；数据库结构不匹配时，恢复该版本对应的状态备份，再启动并检查。

源码改动可使用 `git revert <提交>` 生成反向提交。数据库迁移不会随 Git 操作逆向执行；数据恢复也不会切换代码或镜像。已应用的迁移必须保持原样，结构变更使用新的编号迁移。
