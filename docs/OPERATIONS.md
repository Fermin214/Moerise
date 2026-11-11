# Moerise 运行与恢复

Moerise 支持 Linux 原生二进制和 Docker Linux 容器部署，两者都需要可访问的 Memos 0.31.x 实例与对应账号的 PAT。以下命令在仓库根目录执行，使用 Bash。Windows、macOS 原生服务端支持见[路线图](ROADMAP.md#后续方向与开发输入)。

## 配置

配置模板为 `config.example.json` 和 `.env.example`。服务默认读取 `.local/config.json`，也可通过 `MOERISE_CONFIG_FILE` 或命令参数 `--config` 指定路径，参数优先。配置按内置默认值、JSON、非空 `MOERISE_*` 环境变量的顺序覆盖，JSON 字符串不进行变量替换。Compose 将 `.env` 传给 Core；二进制运行时由进程环境传入。环境变量包含完整来源和鉴权参数时无需 JSON 文件。

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
| `embedding` | 独立的向量模型配置，默认关闭；通用及百炼配置与连接检查见[向量模型](EMBEDDING.md) |
| `llm` | 独立的对话模型配置，默认关闭；协议、限额与连接检查见 [AI](AI.md) |

Moerise 访问令牌须各不相同，且与 Memos PAT 分开。分别用密码管理器生成至少 32 字符的随机令牌和分页密钥；安装了 OpenSSL 时也可使用 `openssl rand -hex 32`。Scope 说明见 [API v1](API-V1.md#资源与身份)。

模板中的 `operator` 包含 `notes:read`、`sync:read`、`reviews:read`，`reader` 包含 `notes:read`、`reviews:read`。外部机器入口由只读操作白名单约束，写入 Scope 也不能开放写操作或生成。原生客户端通过独立浏览器会话创建笔记，请求需携带 `Idempotency-Key`；503 `WRITE_OUTCOME_UNKNOWN` 表示写入结果不明，只能使用同一键和请求体重试，完整规则见[原生客户端](API-V1.md#原生客户端)。

MCP 使用相同的 Moerise 令牌，连接服务的 `/mcp`；具体工具和语义见 [API v1](API-V1.md#每日回顾与-mcp)。

飞书应用回调和后台事件处理随 `serve` 启动。待处理事件加密暂存于产品数据库，业务完成后删除密文。配置状态主密钥后，队列使用独立派生密钥；保存设置时迁移旧 AppSecret 密文，无法解密则保存失败。迁移前的旧队列仍需原应用 Secret，迁移后的队列与设置需相同状态主密钥。恢复快照前结合 Memos 核实快照之后的写入；已有幂等记录保留资源标识，新记录按配置密钥和幂等键派生标识。事件回复的结果不明状态与笔记写入结果分别保存。

调度记录已处理的日期和时段，恢复运行后处理当天到期时段，不补建往日推送。关闭推送仍生成当天回顾。发送结果不明时通过通知 API 查看并确认，见[调度与发送队列](NOTIFICATIONS.md)。恢复产品状态快照将等待及执行中的通知、未完成事件的回复标为 `unknown`。

每日回顾每次先单独发送一次 `「今日回顾」`，再逐篇发送完整正文、原图及尾注 `#标签  创建于YYYY-MM-DD`；无标签时尾注只有 `创建于YYYY-MM-DD`。日期取来源 `create_time`，按顶层 `timezone` 换算。不附加链接、片号或状态提示，普通文件不推送。没有可发送内容的笔记不发尾注，整次均无内容时不发标题。长文按请求大小限制切分，按正文片顺序拼接可还原完整原文。Webhook 发送图片需额外配置应用的 `app_id`、`app_secret` 及图片资源权限，见[飞书入口](FEISHU.md#自定义机器人-webhook)。发送途中笔记变更时，跳过该笔记的剩余分片。

服务绑定来源根地址和账号。更换来源或账号时使用新的 `instance_id` 和数据卷。根地址不包含账号密码、路径、查询参数或片段；Memos 请求直接连接来源，不读取环境中的 HTTP 代理设置。

默认增量间隔为 60 秒，全量修复间隔为 6 小时，水位重叠为 5 分钟。`check-config` 校验有效配置；来源连接、版本和账号在同步时检查。

设置状态加密主密钥后，首次启动将有效日常配置加密写入 `state.sqlite3`。后续启动以持久值为准，部署中的回顾、时区、模型与渠道字段只用于首次初始化；基础设施仍按 JSON 和环境变量加载。旧实例首次启用时保留原有自定义值。密钥不匹配时启动失败，不能用默认值覆盖密文。字段与密钥操作见[产品设置](SETTINGS.md)。

### 环境参数

环境变量使用以下对应关系；空值保留 JSON 或内置默认值。`MOERISE_ACCESS_TOKEN` 非空时以单个 `environment` 凭据替换 JSON 令牌列表，包含 `notes:read`、`sync:read` 和 `reviews:read`；MCP 的工具集合始终只读。

| 环境变量 | 配置 |
| --- | --- |
| `MOERISE_MEMOS_INSTANCE_ID`、`MOERISE_MEMOS_BASE_URL`、`MOERISE_MEMOS_PUBLIC_URL` | `memos.instance_id`、`base_url`、`public_url` |
| `MOERISE_MEMOS_USER`、`MOERISE_MEMOS_TOKEN` | `memos.user`、`token` |
| `MOERISE_MEMOS_PAGE_SIZE` | `memos.page_size`；`0` 按进程内存预算选择，`1` 至 `1000` 固定批量 |
| `MOERISE_SYNC_POLL_SECONDS`、`MOERISE_SYNC_FULL_SECONDS`、`MOERISE_SYNC_OVERLAP_SECONDS` | 增量间隔、全量间隔和水位重叠秒数 |
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

只通过环境运行二进制时，填写全部必要来源、鉴权及密钥变量即可省略 JSON 文件。Compose 使用 `.env`，不要求宿主机配置文件或备份目录。基础设施变化通过重新创建容器生效；日常字段通过 Web 设置保存，已有持久值不再由部署文件覆盖。

### Web 会话

使用 `openssl rand -base64 32` 生成独立的状态加密主密钥，与 PAT、机器令牌和分页密钥分别保存。`web.public_origin` 只填写协议、主机和可选端口，例如 `https://notes.example.com`；本地联调可用 `http://127.0.0.1:8787`。主机名使用 ASCII 或 Punycode；服务会规范化大小写、IPv6 和默认端口，以匹配浏览器 Origin。启用后，Core 必须能够访问 Memos 的官方 Connect `SignIn`、`RefreshToken`、`SignOut` 和 REST `/api/v1/auth/me`。

客户端通过 Memos 用户名和密码登录，登录账户必须等于实例绑定作者及后台 PAT 所属账户。浏览器会话 ID 只保存摘要，刷新凭据以按用途派生的 AEAD 密钥加密保存在 `state.sqlite3`，短期访问令牌只保存在内存。登录有效期不超过登录时的刷新凭据到期时间，上游续期不延长登录期限。普通重启和升级通过刷新凭据恢复登录，到期后需要重新登录。已有加密状态必须沿用原主密钥，密钥不匹配会阻止 Core 启动，重新登录不能修复。退出先删除本地会话，上游清理失败仍完成本地退出。

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

需要支持 Linux 容器的 Docker Engine 和 Compose 2.24+。以下为官方 Memos、Core 和 Web 的完整部署；默认仅发布宿主机回环端口。

### 全新 Memos

复制环境模板，先单独启动官方 Memos：

```bash
cp .env.example .env
docker compose up -d memos
```

在 `http://localhost:5231` 创建首个账户，并在 Memos 账户设置创建 PAT。使用本人 PAT 请求官方接口，响应的 `user.name` 是绑定资源名：

服务器上部署时，可先使用 `ssh -L 5231:127.0.0.1:5231 -L 3000:127.0.0.1:3000 user@host` 将两个回环端口转发至本机，完成初始化和 Web 登录，再配置正式 HTTPS 入口。

```bash
read -r -s -p "Memos PAT: " MEMOS_PAT
echo
curl --fail --silent --show-error \
  -H "Authorization: Bearer ${MEMOS_PAT}" \
  http://localhost:5231/api/v1/auth/me
unset MEMOS_PAT
```

填写 `.env` 的 `MOERISE_MEMOS_USER` 和 `MOERISE_MEMOS_TOKEN`，指定稳定唯一的 `MOERISE_MEMOS_INSTANCE_ID`。使用 `openssl rand -hex 32` 分别生成机器令牌与分页密钥，使用 `openssl rand -base64 32` 生成独立状态加密密钥，填入对应字段。妥善备份 `.env`，可在 Linux 执行 `chmod 600 .env`。

Core 的来源根地址使用容器网络中的 `http://memos:5230`；`MOERISE_MEMOS_PUBLIC_URL` 使用浏览器可达的地址。首位 Web 登录者不能更换作者绑定，登录账户与 PAT 所属账户必须一致。

启动完整服务：

```bash
docker compose up -d --build
docker compose exec -T moerise moerise check-config
docker compose ps
curl --fail http://localhost:3000/health/ready
```

打开 `http://localhost:3000` 使用同一 Memos 用户名和密码登录。首次构建会下载锁定摘要的基础镜像与 Go 依赖。后续代码更新后使用 `docker compose up -d --build`；仅更改 `.env` 时使用 `docker compose up -d`，单纯 `restart` 不会重新读取容器环境。

### 服务器资源

Compose 的 CPU 和内存参数是各服务的上限，不是预留资源。默认 Core 为 2 核、384 MiB，Memos 为 2 核、256 MiB，Web 为 0.5 核、128 MiB。CPU 上限可以大于服务器的可用核数，操作系统仍按实际资源调度。

通过 `.env` 的 `MOERISE_CPU_LIMIT`、`MOERISE_MEMORY_LIMIT`、`MEMOS_CPU_LIMIT`、`MEMOS_MEMORY_LIMIT`、`WEB_CPU_LIMIT` 和 `WEB_MEMORY_LIMIT` 调整上限。以下配置适用于模型使用外部 API 的单用户实例；附件、备份和本地模型需要额外磁盘或内存。

| 服务器 | 配置文件 | Core CPU / 内存上限 | Memos 内存上限 | Web 内存上限 |
| --- | --- | --- | --- | --- |
| 1 核、1 GiB | `deploy/resources/1c1g.env` | 1 / 256 MiB | 192 MiB | 96 MiB |
| 2 核、1 GiB | `deploy/resources/2c1g.env` | 2 / 384 MiB | 192 MiB | 96 MiB |
| 2 核、2–4 GiB | `deploy/resources/2c2g.env` | 2 / 512 MiB | 256 MiB | 128 MiB |
| 4 核、4–8 GiB | `deploy/resources/4c4g.env` | 2 / 768 MiB | 256 MiB | 128 MiB |

保留包含身份与密钥的 `.env`，追加资源文件选择服务器档位。例如：

```bash
docker compose --env-file .env --env-file deploy/resources/2c2g.env up -d --build
```

后一个文件覆盖资源上限，Core 仍从 `.env` 加载来源身份和产品初始化参数。使用自定义环境文件时，沿用 `MOERISE_ENV_FILE` 的路径设置。

2 核、4 GiB 的服务器使用 `2c2g.env` 即可，三个服务的内存上限合计为 896 MiB，其余内存供操作系统、文件缓存和其他服务使用。额外内存有助于容纳系统缓存，但增大 Core 上限不会自动缩短来源读取时间。

Core 启动时读取 Linux 物理内存、cgroup v1/v2 及可见父级配额。存在容器内存上限时，Go 软内存限制取有效上限的三分之二；原生运行且无 cgroup 内存上限时，进程预算取物理内存的四分之一、最多 1 GiB，再取其中三分之二作为 Go 软限制。其余空间用于文件缓存、数据库及临时缓冲。设置 `GOMEMLIMIT=256MiB` 可覆盖自动值，`GOMEMLIMIT=off` 使用 Go 的默认无软限制行为；容器硬上限仍有效。启动日志记录 CPU、进程内存预算和 Go 软限制。

`memos.page_size` 默认为 `0`。自动模式按进程预算和 Go 软限制中较小的值选择上游批量：小于 64 MiB 时每页 100 篇，64 至不足 128 MiB 时每页 500 篇，至少 128 MiB 时每页 1000 篇。固定设置 `1` 至 `1000` 可覆盖自动选择。同步使用完整批量；时间线先读取 100 篇或客户端所需条数加一，以较大者为准，并受批量上限限制。同时间组跨页时，使用完整批量重新收集并核对，额外请求计入分页预算。

标签和日历两次读取 Memos 官方统计接口，固定作者、状态及月份条件，核对返回作者、总数、逐篇创建/更新时间和标签计数，并在前后验证来源身份。时间戳数量必须与总数相符，单标签计数不能超过总数；元数据或计数变化、来源故障和超限均返回错误。统计只处理所需的官方元数据，不加载全库正文。笔记展示、搜索结果和地图点选详情继续复查官方原文。地图点集读取每日快照，不分页核对全库。

Memos 笔记 JSON 接口按有效 CPU 数选择 2 至 4 个并发名额，单核也保留两个名额以重叠网络等待。有效 CPU 同时受容器及父级配额、进程可用 CPU 和 Go 执行线程限制。分页响应超过 32 MiB 时，在同一游标位置减半批量重试，并在该客户端后续请求中沿用较小批量；单条响应仍超限则返回明确错误。同步、向量构建和地图生成按等待顺序共用一个后台执行名额，每个向量批次结束后交给等待者，等待可取消。交互读取不等待该后台名额，数据库事务仍按单连接执行。搜索保留精确 float32 计算，按有效 CPU 数选择最多 4 个分片。

增量同步仍默认为 60 秒，全量修复为 6 小时，水位重叠为 5 分钟。低配实例可将 `MOERISE_SYNC_POLL_SECONDS` 调到 `120` 或 `300`；从 Memos 外部修改的笔记进入搜索索引也会相应变慢。浏览原文继续访问官方来源。

### 外部 Memos

将 `COMPOSE_PROFILES` 留空，设置外部 Memos 的根地址、公开地址、实例标识、本人资源名和 PAT。其余密钥及 Origin 与一体化部署相同，执行 `docker compose up -d --build` 只启动 Core 和 Web。容器中的 `localhost` 指向容器自身；外部根地址必须从 Core 容器可达，并放通官方 REST 与 Connect 认证路径。

从内置 Memos 切换前先备份数据；停止内置服务使用 `docker compose --profile memos stop memos`。更换来源或账号需使用新的实例标识和产品数据卷，不能将旧绑定状态直接指向另一来源。

### 入口与持久化

| 参数或数据卷 | 默认值与职责 |
| --- | --- |
| `COMPOSE_PROFILES` | `memos` 启动官方 Memos，留空使用外部实例；保留 `local` profile 别名 |
| `COMPOSE_PROJECT_NAME` | `moerise-local`；同一部署保持不变，决定命名卷及网络的名称 |
| `WEB_BIND_ADDRESS`、`WEB_PORT` | `127.0.0.1`、`3000`，正式 Web 入口 |
| `MEMOS_PORT`、`MOERISE_PORT` | `5231`、`8787`，Memos 与 Core 的回环端口 |
| `MOERISE_PUBLIC_ORIGIN` | `http://localhost:3000`；同一个参数传给 Core 和 Web |
| `MOERISE_IMAGE_TAG` | Core 和 Web 的本地构建标签，分别维护两个镜像 |
| `MOERISE_ENV_FILE` | Core 环境文件路径，默认 `.env`；使用其他环境文件时与 Compose 的 `--env-file` 指向同一个文件 |
| `memos-data` | 官方 Memos 账户、笔记和附件 |
| `state-data` | Core 产品状态、持久设置、会话和任务 |
| `index-data` | 可重建索引 |
| `backup-data` | Core `/backups` 的状态快照；需导出至宿主机或其他存储 |

Core 和 Web 使用非 root 用户、只读根文件系统和独立临时目录；Core 默认 384 MiB、2 核，Web 默认 128 MiB、0.5 核。命名卷由镜像目录的所有者初始化，无需预建配置和备份绑定挂载。Core 的 scratch 镜像包含静态二进制、CA 证书及嵌入式时区数据；Web 镜像独立构建，无产品数据卷，也不接收 `.env` 或 Memos 凭据。

Memos、Core、Web 各有存活检查；Web 的 `/health/ready` 反映来源连接及首次同步结果。Core 存活后 Web 即可启动，来源暂时不可用不会阻止登录页显示。`docker compose down` 保留命名卷，日常维护不要添加 `--volumes`。

### HTTPS

远程访问将 `MOERISE_PUBLIC_ORIGIN` 改为公开 HTTPS 来源，例如 `https://notes.example.com`，重新创建 Core 和 Web。由宿主机 HTTPS 入口反向代理到 Web：

```caddyfile
notes.example.com {
    reverse_proxy 127.0.0.1:3000
}
```

该示例适用于已有 Caddy 及正确指向宿主机的域名。入口位于容器中时使用同一 Docker 网络的 `web:3000`，或将 `WEB_BIND_ADDRESS` 设置为可被入口访问的宿主机地址。Web 不代理机器接口或飞书回调；需要这些入口时，将特定路径单独转发到 Core 的 `8787` 端口。Memos 官方管理页使用独立入口。

### 旧 Compose 配置

沿用原部署的 Compose 项目名，才能继续使用已有的 `state-data`、`index-data` 和 `memos-data`。将 `.local/config.json` 中的基础设施参数及原密钥填入 `.env`；已有加密状态必须使用原状态主密钥，持久日常设置无需重新填写。

使用高级 JSON 配置时可在 `compose.override.yaml` 的 `services.moerise.volumes` 中增加 `.local/config.json:/run/moerise/config.json:ro`，保持 UID 10001 可读。原 `.local/backups/` 文件继续通过原路径读取；需要容器访问旧快照时添加 `.local/backups:/legacy-backups:ro`，恢复命令的 `--source` 使用该目录。新的备份写入 `/backups`，按下文命令导出。

尚无持久产品设置的旧实例，首次升级需加载完整旧 JSON，并将 `.env` 的时区、回顾、推送和渠道等日常字段留空，或填入原有值，避免模板默认值覆盖后被首次持久化。模型配置同样沿用旧 JSON。首次启动完成后，日常设置由 Web 保存；基础设施参数及原密钥继续保存在部署配置中。

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

地图在完整向量索引就绪后由后台预建，普通新增、修改和删除每 24 小时最多合并一次；没有变化不读取全量向量或重算布局。打开地图只读取已发布快照，重启也可直接恢复。首次生成和每日更新与同步、向量构建串行，减少小核数服务器的 CPU 及内存竞争；已有同空间快照在索引更新期间继续可读，模型空间切换和索引重建需要重新准备。点选原文与附件继续实时读取。

来源列表、标签/日历侧栏和聊天材料分页共用每请求 55 秒预算，包含认证、设置资源锁等待和来源读取；地图同为 55 秒，聊天生成使用独立的 115 秒预算。Web 普通代理超时为 60 秒，聊天流为 125 秒。来源请求遇到 429 返回 `UPSTREAM_RATE_LIMITED`，稍后重试；Core 不绕过 Memos 的来源限流，也不以缓存原文或部分结果替代失败。标签/日历两次核对官方统计，每次最多 10,000 篇、32 MiB 校验数据；来源分页扫描另受 `memos.max_pages` 约束。`LIVE_LIST_LIMIT` 或分页预算错误需缩小筛选范围、检查来源响应大小与访问量。基础分页仍可在较大总库中读取单页。

对外访问时，通过反向代理配置 HTTPS，并按 API 契约提供 Moerise 访问令牌。同一产品目录只允许一个服务写入进程，恢复和索引重建需停止服务。

## 存储与备份范围

| 对象 | 内容 | 备份或恢复方式 |
| --- | --- | --- |
| Memos 数据 | 原文、附件、账号和令牌状态 | Memos 实例自身的完整数据备份 |
| `state_dir/state.sqlite3` | 绑定、水位、持久任务、笔记创建幂等记录及产品状态 | 扩展状态快照 |
| `index_dir/index.sqlite3` | 笔记投影、扫描暂存和索引代次 | 从 Memos 重建 |
| 配置文件 | 来源、访问令牌、时区和其他参数 | 独立配置备份 |

数据库类型与同步顺序见[产品与架构](PRODUCT-ARCHITECTURE.md#数据与身份)。配置和备份按敏感数据保存；`.local/` 已排除 Git 和镜像构建上下文。自行选择其他目录时，将其排除版本管理。

笔记创建幂等记录保存键、规范化内容摘要、分配的 `memoId` 和处理状态，不缓存笔记原文。这些记录用于重启后的结果复查和同 ID 重试，须随产品状态备份，清理索引时保留。恢复早于创建操作的快照会丢失对应幂等记录，重放这些请求前需结合 Memos 原文核实结果。

旧渠道 AI 请求、结果和分片正文只保留到全部分片送达或确认完成。升级和最终通知提交会自动清空这些字段，事件摘要、发送尝试、回执、确认与分片标识继续保存；未知发送在确认前不清理。此操作不删除 Web 聊天或 Memos 笔记，也不改写既有备份文件。SQLite 清空字段不等同于对旧备份或存储介质进行安全擦除。

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
mkdir -p .local/backups
docker compose cp moerise:/backups/state-snapshot .local/backups/state-snapshot
```

输出目录包含 `state.sqlite3` 和 `manifest.json`。快照通过 SQLite `VACUUM INTO` 生成一致副本，再检查完整性、外键、来源绑定和迁移摘要。备份格式为 `1`，清单包含服务版本和数据库 SHA-256。

`backup-data` 与运行容器独立，导出副本还应复制至其他存储。完整恢复还需要单独备份 `.env` 或配置文件、Memos 数据和运行版本。Memos 备份应包含原文、附件、账号及令牌状态，按对应版本的备份方法执行。产品状态快照不包含这些数据。

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

换机或备份卷丢失时，可直接只读挂载宿主机导出的快照。恢复前先恢复 Memos 数据及原 `.env`，保持来源绑定、分页密钥和状态主密钥一致。Linux 上为容器 UID 10001 设置快照读取权限：

```bash
sudo chown -R 10001:10001 .local/backups/state-snapshot
docker compose stop moerise
docker compose run --rm --no-deps -T \
  -v "$(pwd)/.local/backups/state-snapshot:/restore-source:ro" \
  moerise restore --source /restore-source --safety-backup /backups/before-restore
docker compose up -d --no-build
```

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
2. 停止服务，切换到目标代码或镜像。源码部署重新构建目标版本二进制；使用随附 Compose 构建时执行 `docker compose build moerise web`，再执行 `docker compose up -d` 重新创建服务。
3. 以同一配置和数据目录启动服务，检查就绪状态及同步结果。
4. 回滚时停止服务，切换到目标版本；数据库结构不匹配时，恢复该版本对应的状态备份，再启动并检查。

使用已有开发版本的数据升级时，沿用原配置、状态加密主密钥和数据目录，保留会话、持久设置、回顾、聊天及已有向量与地图。首次启动按编号和摘要应用数据库迁移，并清理已完成旧渠道 AI 任务的内容副本，未发和未知结果继续保留。产品快照恢复会撤销浏览器会话，需重新登录；索引重建保留产品状态并重新生成向量和布局。升级检查应包括本人原文和附件、设置版本、回顾及聊天历史，启用 Embedding 时另查索引状态。

源码改动可使用 `git revert <提交>` 生成反向提交。数据库迁移不会随 Git 操作逆向执行；数据恢复也不会切换代码或镜像。已应用的迁移必须保持原样，结构变更使用新的编号迁移。

Memos 镜像独立锁定版本与摘要，升级按官方迁移说明执行。只升级 Web 可使用 `docker compose up -d --build --no-deps web`，只升级 Core 可使用 `docker compose up -d --build --no-deps moerise`；两者均沿用原项目名及数据卷。删除服务前先导出笔记、附件、产品快照及部署密钥；`docker compose down --volumes` 会删除该项目的数据卷。
