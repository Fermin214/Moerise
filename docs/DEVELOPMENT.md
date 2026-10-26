# Moerise 开发指南

本指南面向代码贡献者。以下命令在仓库根目录执行。模块职责见[产品与架构](PRODUCT-ARCHITECTURE.md)，功能范围见[路线图](ROADMAP.md)。

## 环境与工具

- Go 1.27+：构建服务，运行测试、格式化和静态检查，建议使用该系列的最新补丁版本。
- Git：版本管理。
- Memos 0.31.x：手动联调时使用独立测试实例和账号。
- Docker Engine：运行真实 Memos 集成验收；日常构建和基础测试无需 Docker。

开发验证使用 Linux 或 Linux 容器，服务端支持 Linux 原生二进制和 Docker Linux 容器。服务构建无需 C 编译器；`go test -race` 需要 C 工具链。Windows、macOS 原生服务端支持见[路线图](ROADMAP.md#后续方向与开发输入)。容器部署见[运行与恢复](OPERATIONS.md#容器部署)。

## 安装依赖与运行

依赖及校验和保存在 `go.mod` 和 `go.sum`。首次运行 Go 命令时会自动下载，也可预先执行：

```bash
go mod download
go mod verify
```

按[配置说明](OPERATIONS.md#配置)创建 `.local/config.json`：

- 填写服务进程可访问的测试 Memos 地址、账号资源名和 PAT。
- 将 `state_dir` 和 `index_dir` 分别设为 `.local/state`、`.local/index`。
- 生成独立的 Moerise 访问令牌和分页密钥。

```bash
go run . check-config
go run . serve --host 127.0.0.1
```

`check-config` 只检查配置格式。来源版本与账号在同步时检查，运行状态见[运行检查](OPERATIONS.md#运行检查)。命令的 `--config` 参数优先于 `MOERISE_CONFIG_FILE`，两者均省略时读取 `.local/config.json`。

## 检查与验证

```bash
go test ./...
go vet ./...
gofmt -l main.go internal tests
```

格式检查应无输出，修正格式使用 `gofmt -w main.go internal tests`。基础测试使用 HTTP 模拟响应和临时数据库，无需 Memos、Docker、模型或真实凭据，覆盖分页中途失败、身份变化、任务合并与重启恢复、备份格式兼容、恢复损坏检查和写入互斥。记录测试补充并发幂等、请求与 Scope 边界、写入期间身份变化、不可信响应和跨作者资源冲突。

每日回顾业务流程测试覆盖固定批次、并发入口、换组幂等、主题上限、来源变更、当地跨日，以及索引重建和备份恢复后的历史保留。

支持竞争检测的环境还可执行：

```bash
go test -race ./...
```

同步、Memos 协议和恢复改动使用真实接口验收。先启动 Docker Engine，再执行：

```bash
go test -tags=integration ./tests -v -count=1
```

验收自动编译服务，创建独立 Memos 容器、匿名数据卷、账号和临时配置，结束后清理。使用 Docker 发布的回环端口，因此 Docker 引擎需与测试进程位于同一台机器。验收覆盖：

- 凭据、只读范围、管理员 PAT 的作者隔离，以及读取时的原文复查。
- 首次同步、分页游标、新增、修改、归档、删除和回溯时间修改。
- 来源断连、服务重启后的任务恢复、在线备份、恢复互斥和索引重建。
- 文本与标签记录、幂等重放与冲突、原文修改和删除，以及写入状态跨重启、索引重建和备份恢复。
- 通过代理模拟上游写入成功后响应丢失、写入前返回 503，验证结果不明时先复查原资源、重发沿用同一资源标识，Memos 中只有一条笔记。
- 附件部分上传失败、上传成功后响应丢失、文件内容读取，以及重启、索引重建和备份恢复后的资源复用。
- REST 与 MCP 共享每日批次、换组幂等、打开和略过记录；MCP 初始化、工具状态提示及只读权限限制；重启和恢复后共享同一批次。

CI 在 Linux 上执行模块校验、格式检查、静态检查、基础测试、无 cgo 构建、竞争检测、漏洞检查及真实 Memos 验收。漏洞检查也可单独运行：

```bash
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

## 接口与提交

API 字段定义位于 `internal/models.go`，路由位于 `api.go`，内嵌契约为 `openapi.json`。接口变化同步修改契约与 [API v1](API-V1.md)；数据库结构变化新增编号迁移，禁止改写已应用的 SQL。

依赖变化使用 `go mod tidy` 更新模块文件。保持改动范围完整、提交信息具体，并同步更新受影响的配置或操作说明。实现边界与文档维护规则见[开发约定](../AGENTS.md)。
