# Moerise 开发指引

Moerise 是基于 Memos 的单用户自托管笔记系统。`internal/` 是按业务前缀组织的单个 Go 包，使用 SQLite；`web/` 使用原生 JavaScript、HTML 和 CSS，Node.js 提供构建、静态服务与同源代理。工具链版本以 [go.mod](go.mod) 和 [web/package.json](web/package.json) 为准。

## 按任务定位

只读取任务涉及的代码和文档。模块职责与源码入口集中在[代码导览](docs/CODEBASE.md)，环境准备与启动命令见[开发指南](docs/DEVELOPMENT.md)。

| 任务 | 入口 |
| --- | --- |
| 了解可用功能、安装方式 | [README](README.md) |
| 确定产品行为与开发范围 | [产品与架构](docs/PRODUCT-ARCHITECTURE.md)、[路线图](docs/ROADMAP.md) |
| 修改 Web 布局、导航、交互或登录 | [Web 界面](docs/WEB.md)、[Web 集成设计](docs/DESIGN.md) |
| 修改客户端 API、外部 REST、MCP 或权限 | [API v1](docs/API-V1.md) |
| 修改检索、向量、漫步或地图 | [检索系统](docs/SEARCH.md)、[向量与语义检索](docs/EMBEDDING.md)、[产品与架构](docs/PRODUCT-ARCHITECTURE.md) |
| 修改 AI 洞察、模型协议或设置 | [AI 对话与洞察](docs/AI.md)、[系统设置](docs/SETTINGS.md) |
| 修改回顾、飞书记录或渠道推送 | [每日回顾](docs/DAILY-REVIEW.md)、[飞书接入](docs/FEISHU.md)、[通知与推送](docs/NOTIFICATIONS.md) |
| 修改配置、部署或恢复流程 | [运行与恢复](docs/OPERATIONS.md) |
| 选择测试、准备浏览器或集成环境 | [开发指南](docs/DEVELOPMENT.md)、[脚本说明](scripts/README.md) |

## 关键约束

- Memos 保存笔记原文、标签和附件，只通过官方 API 访问。一个实例绑定一名作者，管理员 PAT 同样受作者范围限制；来源身份实时核验，笔记原文通过官方 API 复查。
- `/api/app/v1` 使用本人会话，可执行笔记写入和产品设置操作，保持同源、Cookie 和 CSRF 校验。`/api/ext/v1` 与 `/mcp` 只读，配置仅接受 `notes:read`、`sync:read`、`reviews:read`。
- `state.sqlite3` 保存需备份的产品状态，包括设置、会话、聊天与回顾计划；`index.sqlite3` 保存可重建的检索、向量和地图索引。重建索引保留产品状态。
- 正常与归档笔记的全部分页成功后，才能发布投影并推进同步水位。
- `internal/migrations/state/`、`internal/migrations/index/` 分别管理双库迁移，按编号和摘要记账。结构变化新增迁移，保持已应用 SQL 不变。
- 地图使用后台快照，点选详情读取实时原文。业务规则放在共享服务中，客户端与外部路由负责协议和权限。

## 实现与文档

- 接口变化一起检查对应路由、`internal/*models.go`、`internal/openapi.json`、接口文档和调用端。
- 配置与设置变化一起检查 `internal/config*.go`、`internal/settings*.go`、前端设置页、配置模板和配置文档，覆盖热更新与版本冲突。
- 前端修改落在 `web/src/`，`web/dist/` 由构建生成。界面文案同时维护中文源文案、`web/src/en.js` 和相关浏览器流程。
- 行为变化同步更新对应文档和配置示例，新增文档在[文档导航](docs/README.md)登记。功能说明描述实际行为，规划集中写入路线图。
- 公开文档只保留产品、接口、配置、架构和必要开发说明。内部计划、过程记录、验收结果、凭据与临时产物放在 Git 忽略的 `.local/` 中。

## 验证入口

所有命令从仓库根目录执行。优先运行覆盖改动的现有测试，下表列出各范围的完整验证入口：

| 改动范围 | 验证入口 |
| --- | --- |
| Go 业务与接口 | `go test ./...`、`go vet ./...`、`gofmt -l main.go internal tests` |
| Web 逻辑、样式与构建 | `npm --prefix web run build`、`npm --prefix web test` |
| Web 界面与交互 | `npm --prefix web run test:browser` |
| 共用脚本工具 | `node --test scripts/lib/*.test.mjs` |
| 同步、Memos 协议、迁移、备份恢复或通知推送 | `go test -tags=integration ./tests -v -count=1` |
| Compose、Core、Web 联合验收 | `node scripts/acceptance/check-compose.mjs --browser`，其他功能脚本见[脚本说明](scripts/README.md) |

Web 依赖通过 `npm --prefix web ci` 安装。浏览器流程需要 Playwright 浏览器；真实 Memos 集成与联合验收需要 Docker。具体功能的联合验收脚本及参数见[脚本说明](scripts/README.md)。

界面改动检查桌面和手机视口的导航、触控、内容间距、溢出与键盘焦点，同时覆盖简体中文和英语。

完成改动前，用对应流程检查最终行为、关键边界和文档一致性。只有已有端到端流程无法可靠覆盖关键不变量、边界或失败路径时，才增加少量专项测试。复用未受改动影响的验证结果，仅在新改动、失败或剩余风险需要时扩大或重复检查。纯文档修改检查事实、链接和差异即可。
