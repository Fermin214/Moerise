# Moerise 开发指引

Moerise 是基于 Memos 的单用户自托管笔记系统。`internal/` 是按业务前缀组织的单个 Go 包，使用 SQLite；`web/` 使用原生 JavaScript、HTML 和 CSS，由 Node.js 构建、Go 内嵌提供服务。工具链版本以 [go.mod](go.mod) 和 [web/package.json](web/package.json) 为准。

## 按任务定位

模块职责与源码入口见[代码导览](docs/CODEBASE.md)，环境准备与启动命令见[开发指南](docs/DEVELOPMENT.md)。

| 任务 | 入口 |
| --- | --- |
| 了解可用功能、安装方式 | [README](README.md) |
| 确定产品行为与开发范围 | [产品与架构](docs/PRODUCT-ARCHITECTURE.md)、[路线图](docs/ROADMAP.md) |
| 修改 Web 布局、导航、交互或登录 | [Web 界面](docs/WEB.md)、[Web 集成设计](docs/DESIGN.md) |
| 修改客户端 API、外部 REST、MCP 或权限 | [API v1](docs/API-V1.md) |
| 修改检索、向量、漫步或地图 | [检索系统](docs/SEARCH.md)、[向量与语义检索](docs/EMBEDDING.md)、[产品与架构](docs/PRODUCT-ARCHITECTURE.md) |
| 修改向量模型或产品设置 | [向量与语义检索](docs/EMBEDDING.md)、[系统设置](docs/SETTINGS.md) |
| 修改回顾、飞书记录或渠道推送 | [每日回顾](docs/DAILY-REVIEW.md)、[飞书接入](docs/FEISHU.md)、[通知与推送](docs/NOTIFICATIONS.md) |
| 修改配置、部署或恢复流程 | [运行与恢复](docs/OPERATIONS.md) |
| Android 构建、联调及发版验收 | [Android 开发](docs/ANDROID.md)、[发版前真机检查](docs/android/RELEASE-CHECKLIST.md) |
| 选择测试、准备浏览器或集成环境 | [开发指南](docs/DEVELOPMENT.md)、[脚本说明](scripts/README.md) |

## 关键约束

- Memos 保存笔记原文、标签和附件，只通过官方 API 写入与同步；客户端日常读取走本地同步索引，写前核验作者身份并与写入直通保持一致。一个实例绑定一名作者，管理员 PAT 同样受作者范围限制。
- `/api/app/v1` 使用本人会话，可执行笔记写入和产品设置操作，保持同源、Cookie 和 CSRF 校验。`/api/ext/v1` 与 `/mcp` 只读，配置仅接受 `notes:read`、`sync:read`、`reviews:read`。
- `state.sqlite3` 保存需备份的产品状态，包括设置、会话与回顾计划；`index.sqlite3` 保存可重建的检索、向量和地图索引。重建索引保留产品状态。
- 正常与归档笔记的全部分页成功后，才能发布投影并推进同步水位。
- `internal/migrations/state/`、`internal/migrations/index/` 分别管理双库迁移，按编号和摘要记账。结构变化新增迁移，保持已应用 SQL 不变。
- 地图使用后台快照，点选详情读取本地笔记投影。业务规则放在共享服务中，客户端与外部路由负责协议和权限。

## 实现与文档

- 手写源码和测试默认不加解释性注释。用命名表达意图，用行为测试固定容易破坏的约束。禁止用注释解释临时绕路、吞错或重复重试。只保留工具指令、必需的许可声明，以及代码和测试都无法交代的外部协议限制。
- 接口变化一起检查对应路由、`internal/*models.go`、`internal/openapi_gen.go`、接口文档及多端调用方（`web/src/`、`mobile/`、`tests/`、`scripts/acceptance/`、`scripts/android/seed-*.mjs`）。
- 状态、认证与同步边界及快照恢复改动，须齐步排查全部错误出口与同类重置入口，复用共享淘汰与恢复屏障；数据库迁移以包含非 JSON、中文和空字段的真实存量行验证。多入口的状态变更共用终态和持久化边界。改动时覆盖成功、失败、中断与续跑，并核对历史有效记录和后续副作用。
- Go 阻塞通道、流与锁资源在 `t.Cleanup` 中释放；定向 `-race` 运行显式指定 `-timeout`（如 `120s`），依赖 Git 历史的测试须保证在 `fetch-depth: 1` 浅克隆下仍执行核心断言。
- 配置与设置变化一起检查 `internal/config*.go`、`internal/settings*.go`、前端设置页、配置模板和配置文档，覆盖热更新与版本冲突。
- 前端修改落在 `web/src/`，`web/dist/` 由构建生成。界面文案同时维护中文源文案、`web/src/en.js` 和相关浏览器流程。
- 行为变化同步更新对应文档和配置示例，新增文档在[文档导航](docs/README.md)登记。功能说明描述实际行为，规划集中写入路线图。
- 公开文档只保留产品、接口、配置、架构和必要开发说明。内部计划、过程记录、验收结果、凭据与临时产物放在 Git 忽略的 `.local/` 中。

## 验证入口

所有命令从仓库根目录执行：

| 改动范围 | 验证入口 |
| --- | --- |
| Go 业务与接口 | `go test ./...`、`go test -race -timeout=30m -count=1 ./...`、`go vet ./...`、`gofmt -l main.go internal tests web/*.go scripts/testweb` |
| Web 逻辑、样式与构建 | `npm --prefix web run build`、`npm --prefix web test` |
| Web 界面与交互 | `npm --prefix web run test:browser` |
| 共用脚本与策略 | `node --test scripts/lib/*.test.mjs scripts/android/fixture-policy.test.mjs` |
| CI 路径与门禁 | 安装 `scripts/android/requirements.txt`，运行 `python3 -B scripts/android/ci-scope.test.py` 与 `bash .agents/skills/moerise-ci/inspect.sh workflow main` |
| 发布守护 | `python3 -B -m unittest discover -s scripts/release -p 'test_*.py'` |
| 同步、Memos 协议、迁移、备份恢复或通知推送 | `MOERISE_V030_BINARY=<发行包内的 moerise 路径> go test -tags=integration ./tests -v -count=1`，基线和布局服务见[开发指南](docs/DEVELOPMENT.md#五真实-memos-集成测试) |
| Compose、Core、Web 联合验收 | `node scripts/acceptance/check-compose.mjs --browser`，其他功能脚本见[脚本说明](scripts/README.md) |

Web 依赖通过 `npm --prefix web ci` 安装。浏览器流程需要 Playwright 浏览器；真实 Memos 集成与联合验收需要 Docker。具体功能的联合验收脚本及参数见[脚本说明](scripts/README.md)。

Dallas 已配置轻量 JDK 17、Android CLI SDK 和离线 Robolectric 缓存。加载 `~/.config/android/environment.sh` 后可在推送前直接运行 `./gradlew` 验证纯 JVM/Robolectric 单测，使用 `--no-daemon --max-workers=2`，与 `go test -race` 分开运行。Dallas 不安装或运行 Android 模拟器。正式签名 APK、全变体构建和 Core API 联调由 GitHub Actions 验证。正式签名 APK 按[发版前真机检查](docs/android/RELEASE-CHECKLIST.md)验收。
