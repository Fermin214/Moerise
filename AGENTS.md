# 给 Agent 的说明

Moerise 是基于 Memos 的自托管笔记扩展服务。先读 [README](README.md)，再按任务读 `docs/` 中对应的文档；功能范围见[路线图](docs/ROADMAP.md)，模块和数据边界见[产品与架构](docs/PRODUCT-ARCHITECTURE.md)。

## 从哪里改

- `main.go` 处理命令和进程启动，业务代码在 `internal/`；`memos.go` 封装官方 Memos API。
- 改接口时一起检查 `api.go`、`models.go` 和 `openapi.json`，并更新 [API v1](docs/API-V1.md)。这些文件都在 `internal/`。
- 改配置时一起检查 `internal/config.go`、`config.example.json` 和[配置说明](docs/OPERATIONS.md#配置)。

## 容易改错的地方

- Memos 保存笔记原文和附件，只通过官方 API 访问。即使用管理员 PAT，Moerise 也只处理绑定作者的笔记；读取前的来源身份和原文复查不能省略。
- `state.sqlite3` 是需备份的产品状态，`index.sqlite3` 是可重建索引。重建索引不能清空产品状态。
- 正常与归档笔记的全部分页都成功后，才能发布投影并推进水位。同步任务需要支持合并、重试和重启恢复。
- `internal/migrations/` 按编号和摘要记账。结构变化新增迁移，不改写已应用的 SQL。

## 写代码

匹配周围代码的写法、命名和注释密度。选能清楚解决问题的简单方案，只定义正在使用的抽象。验证改动涉及的重要行为，不为简单的样式改动写测试。

开发完成后更新对应的文档，并与代码一同提交。

## 运行与检查

使用 Go 1.27+，在仓库根目录执行：

```bash
go test ./...
go vet ./...
gofmt -l main.go internal tests
```

格式检查应无输出。同步、Memos 协议或备份恢复有改动时，运行 `go test -tags=integration ./tests -v -count=1`，它使用独立的 Memos 容器和临时数据。环境准备与完整流程见[开发指南](docs/DEVELOPMENT.md)。
