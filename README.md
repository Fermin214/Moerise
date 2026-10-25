# Moerise

Moerise 是基于 Memos 的自托管笔记扩展服务，提供笔记同步、只读 HTTP API、同步任务管理和备份恢复。笔记原文和附件保存在 Memos，编辑笔记继续使用 Memos。

每日回顾、飞书交互、语义检索、AI 问答及 Android 客户端的开发安排见[路线图](docs/ROADMAP.md)。

## 可以使用的功能

- 同步 Memos 0.31.x 中本人创建的正常与归档笔记，修复修改、归档和删除带来的索引变化。
- 通过独立的访问令牌读取笔记、查看同步状态或请求同步。
- 服务重启后恢复同步任务；需要时重建索引或从备份恢复产品状态。

Moerise 的笔记接口会向 Memos 复查原文，来源服务不可用时返回错误。每个部署连接一个 Memos 实例和一个账号。

## 运行条件

- 可访问的 Memos 0.31.x 实例，以及对应账号的个人访问令牌（PAT）。
- 从源码构建需要 Go 1.27+；容器部署需要支持 Linux 容器的 Docker Engine 与 Compose 插件。
- 分别用于产品状态和可重建索引的持久目录。

## 安装与使用

Go 二进制的构建、配置和运行方法见[运行与恢复](docs/OPERATIONS.md#二进制运行)。使用容器时，可在获取源码后按以下 Compose 示例部署；命令在仓库根目录执行，使用 Bash：

```bash
mkdir -p .local/backups
cp config.example.json .local/config.json
```

编辑 `.local/config.json`，填写 Memos 地址、账号资源名和 PAT，并为 Moerise 生成独立的访问令牌和分页密钥。字段说明见[配置说明](docs/OPERATIONS.md#配置)。基础功能无需配置模型或飞书。

Linux 部署需先按[容器部署](docs/OPERATIONS.md#容器部署)设置配置和备份目录的挂载权限，再启动服务：

```bash
docker compose up -d --build moerise
```

启动后访问服务的 `/docs`，点击 **Authorize** 填入 Moerise 访问令牌即可调用接口。`/health/ready` 返回 200 表示已连接来源并完成首次全量同步。接口使用方法见 [API v1](docs/API-V1.md)。

随附 Compose 配置仅发布回环端口。监听地址、对外访问、备份和升级见[运行与恢复](docs/OPERATIONS.md)。

## 文档

| 文档 | 用途 |
| --- | --- |
| [运行与恢复](docs/OPERATIONS.md) | 配置、部署、备份、恢复和版本回滚 |
| [API v1](docs/API-V1.md) | 访问范围、笔记分页、同步任务和错误处理 |
| [开发指南](docs/DEVELOPMENT.md) | 贡献代码所需的环境、工具、运行和验证方法 |
| [产品与架构](docs/PRODUCT-ARCHITECTURE.md) | 产品方向、模块职责、数据边界和技术依据 |
| [路线图](docs/ROADMAP.md) | 已完成阶段、后续功能和通过条件 |
