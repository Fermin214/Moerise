# Moerise 开发指引

Moerise 是基于 Memos 的单用户自托管笔记服务。服务端使用 Go 1.27+ 和 SQLite，支持 Linux 二进制及 Docker Linux 容器。

## 按任务查阅

| 任务 | 入口 |
| --- | --- |
| 了解可用功能、安装方式 | [README](README.md) |
| 确定产品行为与开发范围 | [产品需求](docs/PRD.md)、[路线图](docs/ROADMAP.md)、[Web 集成设计](docs/DESIGN.md) |
| 调整模块、存储或来源访问 | [产品与架构](docs/PRODUCT-ARCHITECTURE.md) |
| 修改 REST、MCP、权限或响应 | [API v1](docs/API-V1.md) |
| 修改飞书记录或回顾推送 | [飞书接入](docs/FEISHU.md)、[通知与推送](docs/NOTIFICATIONS.md) |
| 修改配置、部署或恢复流程 | [运行与恢复](docs/OPERATIONS.md) |
| 选择测试、准备集成环境 | [开发指南](docs/DEVELOPMENT.md) |

只读取任务涉及的文档和代码。

## 代码入口

- `main.go`：命令和进程启动；`internal/`：业务服务、HTTP 与平台适配；`tests/`：真实 Memos 集成测试。
- 接口变化一起检查 `internal/api.go`、`internal/models.go`、`internal/openapi.json` 和接口文档。
- 配置变化一起检查 `internal/config.go`、`internal/config_environment.go`、配置模板和配置文档。

## 关键约束

- Memos 保存笔记原文、标签和附件，只通过官方 API 访问。一个实例绑定一名作者，管理员 PAT 同样受作者范围限制；读取时复查来源身份和原文。
- `state.sqlite3` 保存需备份的产品状态，`index.sqlite3` 保存可重建索引。重建索引保留产品状态。
- 正常与归档笔记的全部分页成功后，才能发布投影并推进同步水位。
- `internal/migrations/` 按编号和摘要记账。结构变化新增迁移，保持已应用 SQL 不变。
- 外部 MCP 只读；写入 Scope 也不能开放笔记或产品状态写操作。

## 实现与文档

使用简单、直接的方案，只增加有明确用途的抽象和状态。匹配周围代码的命名和写法，以实际产品行为完成任务。

使用简体中文。功能说明描述代码支持的行为，规划集中写入路线图；公开文档只保留产品、接口、配置、架构和必要开发说明。内部计划、过程记录、验收结果与凭据放在 Git 忽略的目录中。

行为变化同步更新文档和配置示例，代码与对应文档一起提交。

## 验证入口

常用检查在仓库根目录执行，按改动范围选择：

```bash
go test ./...
go vet ./...
gofmt -l main.go internal tests
```

同步、Memos 协议或备份恢复变化，使用 `go test -tags=integration ./tests -v -count=1`。该流程创建独立 Memos 容器和临时数据，环境及可选实测见开发指南。

检查实际功能、关键边界和回归风险。复用未受改动影响的验证结果，失败后修复并复测相关项。纯文档修改检查事实、链接和差异即可。
